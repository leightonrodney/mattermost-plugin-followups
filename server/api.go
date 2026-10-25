package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
	"github.com/leightonrodney/mattermost-plugin-followups/server/store"
)

// maxRequestBodyBytes bounds how much of a request body a handler will read.
// Reminder payloads are small; this is generous and still keeps a single
// request from costing unbounded memory.
const maxRequestBodyBytes = 64 << 10

// newRouter builds the plugin's HTTP API.
//
// The Mattermost server forwards plugin requests without the panic recovery,
// body size limit or security headers it applies to its own REST API, so the
// plugin applies its own.
func (p *Plugin) newRouter() *mux.Router {
	router := mux.NewRouter()
	router.Use(p.logRequests, p.recoverPanic, limitRequestBody, setSecurityHeaders)

	api := router.PathPrefix("/api/v1").Subrouter()

	// Every route below this point requires an authenticated user. There is no
	// unauthenticated endpoint in this plugin at all.
	api.Use(requireUser)

	api.HandleFunc("/config", p.handleGetConfig).Methods(http.MethodGet)
	api.HandleFunc("/parse", p.handleParse).Methods(http.MethodPost)

	api.HandleFunc("/reminders", p.handleList).Methods(http.MethodGet)
	api.HandleFunc("/reminders", p.handleCreate).Methods(http.MethodPost)
	api.HandleFunc("/reminders/{id}", p.handleUpdate).Methods(http.MethodPut)
	api.HandleFunc("/reminders/{id}", p.handleDelete).Methods(http.MethodDelete)
	api.HandleFunc("/reminders/{id}/cancel", p.handleCancel).Methods(http.MethodPost)
	api.HandleFunc("/reminders/{id}/snooze", p.handleSnooze).Methods(http.MethodPost)
	api.HandleFunc("/reminders/{id}/complete", p.handleComplete).Methods(http.MethodPost)

	// Interactive message buttons. The server authenticates these the same way
	// and sets the same header, so they sit behind requireUser too.
	api.HandleFunc("/actions/snooze", p.handleActionSnooze).Methods(http.MethodPost)
	api.HandleFunc("/actions/complete", p.handleActionComplete).Methods(http.MethodPost)

	router.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	return router
}

// userIDFrom returns the authenticated user for a request.
//
// Mattermost-User-ID is set by the server only after it has authenticated the
// request, and the server strips any copy the client tried to send, which is
// what makes it safe to trust here.
func userIDFrom(r *http.Request) string {
	return r.Header.Get("Mattermost-User-ID")
}

func requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userIDFrom(r) == "" {
			writeError(w, http.StatusUnauthorized, "not authorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Payloads ---------------------------------------------------------------

// reminderPayload is how a reminder is presented to the webapp. It is a
// deliberate projection rather than the stored struct, so that adding an
// internal field cannot accidentally publish it.
type reminderPayload struct {
	ID          string             `json:"id"`
	Message     string             `json:"message"`
	DueAt       int64              `json:"due_at"`
	CreateAt    int64              `json:"create_at"`
	UpdateAt    int64              `json:"update_at"`
	Status      string             `json:"status"`
	Timezone    string             `json:"timezone"`
	Recurrence  *recurrencePayload `json:"recurrence,omitempty"`
	ChannelID   string             `json:"channel_id,omitempty"`
	PostID      string             `json:"post_id,omitempty"`
	TeamID      string             `json:"team_id,omitempty"`
	Permalink   string             `json:"permalink,omitempty"`
	RepeatText  string             `json:"repeat_text,omitempty"`
	Occurrences int                `json:"occurrences,omitempty"`
}

type recurrencePayload struct {
	Type         string `json:"type"`
	Interval     int    `json:"interval,omitempty"`
	Weekdays     []int  `json:"weekdays,omitempty"`
	DayOfMonth   int    `json:"day_of_month,omitempty"`
	Hour         int    `json:"hour"`
	Minute       int    `json:"minute"`
	EverySeconds int64  `json:"every_seconds,omitempty"`
}

func (p *Plugin) toPayload(rec *reminder.Reminder) reminderPayload {
	payload := reminderPayload{
		ID:          rec.ID,
		Message:     rec.Message,
		DueAt:       rec.DueAt,
		CreateAt:    rec.CreateAt,
		UpdateAt:    rec.UpdateAt,
		Status:      string(rec.Status),
		Timezone:    rec.Timezone,
		ChannelID:   rec.ChannelID,
		PostID:      rec.PostID,
		TeamID:      rec.TeamID,
		Occurrences: rec.Occurrences,
	}
	if rec.IsRecurring() {
		payload.RepeatText = rec.Recurrence.Describe()
		payload.Recurrence = toRecurrencePayload(rec.Recurrence)
	}
	// The permalink is recomputed per request, so a user who has since lost
	// access to the channel stops being given a link to it.
	payload.Permalink = p.permalink(rec)
	return payload
}

func toRecurrencePayload(rec *reminder.Recurrence) *recurrencePayload {
	weekdays := make([]int, 0, len(rec.Weekdays))
	for _, wd := range rec.Weekdays {
		weekdays = append(weekdays, int(wd))
	}
	return &recurrencePayload{
		Type:         string(rec.Type),
		Interval:     rec.Interval,
		Weekdays:     weekdays,
		DayOfMonth:   rec.DayOfMonth,
		Hour:         rec.Hour,
		Minute:       rec.Minute,
		EverySeconds: rec.EverySeconds,
	}
}

func fromRecurrencePayload(payload *recurrencePayload) (*reminder.Recurrence, error) {
	if payload == nil || payload.Type == "" {
		return nil, nil
	}
	weekdays := make([]time.Weekday, 0, len(payload.Weekdays))
	for _, wd := range payload.Weekdays {
		if wd < 0 || wd > 6 {
			return nil, errors.New("weekday must be between 0 and 6")
		}
		weekdays = append(weekdays, time.Weekday(wd))
	}
	rec := &reminder.Recurrence{
		Type:         reminder.RecurrenceType(payload.Type),
		Interval:     payload.Interval,
		Weekdays:     weekdays,
		DayOfMonth:   payload.DayOfMonth,
		Hour:         payload.Hour,
		Minute:       payload.Minute,
		EverySeconds: payload.EverySeconds,
	}
	if err := rec.Validate(); err != nil {
		return nil, err
	}
	return rec, nil
}

// --- Handlers ---------------------------------------------------------------

// handleGetConfig gives the webapp the limits it needs to validate input before
// sending it, and the user's timezone so that times render consistently.
func (p *Plugin) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	config := p.getConfiguration()

	writeJSON(w, http.StatusOK, map[string]any{
		"max_reminders":      config.ReminderLimit(),
		"max_recurring":      config.RecurringLimit(),
		"min_interval_secs":  int(config.MinInterval().Seconds()),
		"max_horizon_days":   int(config.MaxHorizon().Hours() / 24),
		"scheduler_enabled":  config.EnableScheduler,
		"max_message_length": reminder.MaxMessageRunes,
		"default_hour":       reminder.DefaultHour,
		"timezone":           p.service.Timezone(userID).String(),
	})
}

// handleParse previews what a typed phrase would mean, so that the webapp can
// show "Friday 3 April at 09:00" under the box as the user types rather than
// reimplementing the grammar in TypeScript.
func (p *Plugin) handleParse(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)

	var body struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	loc := p.service.Timezone(userID)
	parsed, err := reminder.Parse(body.Text, p.service.Now().In(loc), loc)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"due_at":      parsed.DueAt.UnixMilli(),
		"message":     parsed.Message,
		"repeat_text": parsed.Recurrence.Describe(),
		"recurrence":  toRecurrencePayloadOrNil(parsed.Recurrence),
	})
}

func toRecurrencePayloadOrNil(rec *reminder.Recurrence) *recurrencePayload {
	if rec == nil || rec.Type == reminder.RecurrenceNone {
		return nil
	}
	return toRecurrencePayload(rec)
}

func (p *Plugin) handleList(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)

	if _, err := p.service.Purge(userID, p.service.Now()); err != nil {
		p.client.Log.Warn("Failed to apply retention", "user_id", userID, "error", err.Error())
	}

	reminders, err := p.service.List(userID)
	if err != nil {
		p.fail(w, r, err)
		return
	}

	payloads := make([]reminderPayload, 0, len(reminders))
	for _, rec := range reminders {
		payloads = append(payloads, p.toPayload(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reminders": payloads})
}

// createRequest is the webapp's way of creating a reminder. Either `text` is
// given and parsed with the same grammar as the slash command, or `due_at` and
// an optional recurrence are given explicitly by the form.
type createRequest struct {
	Text       string             `json:"text,omitempty"`
	Message    string             `json:"message,omitempty"`
	DueAt      int64              `json:"due_at,omitempty"`
	Recurrence *recurrencePayload `json:"recurrence,omitempty"`
	ChannelID  string             `json:"channel_id,omitempty"`
	PostID     string             `json:"post_id,omitempty"`
}

func (p *Plugin) handleCreate(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)

	var body createRequest
	if !decodeBody(w, r, &body) {
		return
	}

	loc := p.service.Timezone(userID)
	input := createInput{Timezone: loc.String()}

	switch {
	case body.Text != "":
		parsed, err := reminder.Parse(body.Text, p.service.Now().In(loc), loc)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Message = parsed.Message
		input.DueAt = parsed.DueAt
		input.Recurrence = parsed.Recurrence

	case body.DueAt > 0:
		rec, err := fromRecurrencePayload(body.Recurrence)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Message = body.Message
		input.DueAt = time.UnixMilli(body.DueAt)
		input.Recurrence = rec

	default:
		writeError(w, http.StatusBadRequest, "give either a phrase to parse or a due time")
		return
	}

	// Linking a reminder to a message is only allowed when the user can still
	// read that channel. Without this check, somebody could attach a reminder
	// to a post ID they guessed and later receive a permalink to it.
	if body.PostID != "" || body.ChannelID != "" {
		channelID, teamID, err := p.resolveMessageLink(userID, body.ChannelID, body.PostID)
		if err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		input.ChannelID = channelID
		input.PostID = body.PostID
		input.TeamID = teamID
	}

	rec, err := p.service.Create(userID, input)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p.toPayload(rec))
}

// resolveMessageLink checks that the user may link a reminder to the given
// message, and returns the channel and team to record.
func (p *Plugin) resolveMessageLink(userID, channelID, postID string) (string, string, error) {
	if postID != "" && channelID == "" {
		// Derive the channel from the post rather than trusting the caller for
		// both, so the two cannot be made to disagree.
		post, err := p.client.Post.GetPost(postID)
		if err != nil || post == nil {
			return "", "", errors.New("that message is not available")
		}
		channelID = post.ChannelId
	}

	if !p.client.User.HasPermissionToChannel(userID, channelID, model.PermissionReadChannel) {
		return "", "", errors.New("that message is not available")
	}

	channel, err := p.client.Channel.Get(channelID)
	if err != nil || channel == nil {
		return "", "", errors.New("that message is not available")
	}
	return channelID, channel.TeamId, nil
}

func (p *Plugin) handleUpdate(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	id := mux.Vars(r)["id"]

	var body struct {
		Message     *string            `json:"message,omitempty"`
		DueAt       int64              `json:"due_at,omitempty"`
		Recurrence  *recurrencePayload `json:"recurrence,omitempty"`
		ClearRepeat bool               `json:"clear_repeat,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	var updated *reminder.Reminder

	if body.Message != nil {
		rec, err := p.service.Retext(userID, id, *body.Message)
		if err != nil {
			p.fail(w, r, err)
			return
		}
		updated = rec
	}

	if body.DueAt > 0 {
		var rec *reminder.Recurrence
		if !body.ClearRepeat {
			parsed, err := fromRecurrencePayload(body.Recurrence)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			rec = parsed
		}
		result, err := p.service.Reschedule(userID, id, time.UnixMilli(body.DueAt), rec)
		if err != nil {
			p.fail(w, r, err)
			return
		}
		updated = result
	}

	if updated == nil {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	writeJSON(w, http.StatusOK, p.toPayload(updated))
}

func (p *Plugin) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := p.service.Delete(userIDFrom(r), mux.Vars(r)["id"]); err != nil {
		p.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (p *Plugin) handleCancel(w http.ResponseWriter, r *http.Request) {
	rec, err := p.service.Cancel(userIDFrom(r), mux.Vars(r)["id"])
	if err != nil {
		p.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p.toPayload(rec))
}

func (p *Plugin) handleSnooze(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Minutes int `json:"minutes"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Minutes <= 0 {
		writeError(w, http.StatusBadRequest, "snooze needs a positive number of minutes")
		return
	}

	rec, err := p.service.Snooze(userIDFrom(r), mux.Vars(r)["id"], time.Duration(body.Minutes)*time.Minute)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p.toPayload(rec))
}

func (p *Plugin) handleComplete(w http.ResponseWriter, r *http.Request) {
	rec, err := p.service.Complete(userIDFrom(r), mux.Vars(r)["id"])
	if err != nil {
		p.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p.toPayload(rec))
}

// --- Interactive buttons ----------------------------------------------------

// handleActionSnooze backs the snooze buttons on a delivered reminder.
//
// The acting user is taken from the request the server built, never from the
// button's own context, so a replayed or hand-crafted payload cannot snooze
// somebody else's reminder.
func (p *Plugin) handleActionSnooze(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)

	request, ok := decodeAction(w, r)
	if !ok {
		return
	}

	reminderID, ok := request.Context[contextReminderID].(string)
	if !ok || reminderID == "" {
		writeError(w, http.StatusBadRequest, "malformed action")
		return
	}

	minutes, _ := request.Context[contextMinutes].(float64)
	duration := time.Duration(minutes) * time.Minute
	if minutes <= 0 {
		// The "Tomorrow" preset carries no fixed length, because what tomorrow
		// morning means depends on the user's timezone at the moment they press
		// it rather than when the reminder was built.
		loc := p.service.Timezone(userID)
		now := p.service.Now().In(loc)
		tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, reminder.DefaultHour, 0, 0, 0, loc)
		duration = tomorrow.Sub(now)
	}

	rec, err := p.service.Snooze(userID, reminderID, duration)
	if err != nil {
		p.respondToAction(w, r, err, "")
		return
	}

	p.respondToAction(w, r, nil, fmt.Sprintf(":alarm_clock: Postponed to %s.", formatTime(rec.DueTime())))
}

// handleActionComplete backs the done button on a delivered reminder.
func (p *Plugin) handleActionComplete(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)

	request, ok := decodeAction(w, r)
	if !ok {
		return
	}

	reminderID, ok := request.Context[contextReminderID].(string)
	if !ok || reminderID == "" {
		writeError(w, http.StatusBadRequest, "malformed action")
		return
	}

	if _, err := p.service.Complete(userID, reminderID); err != nil {
		p.respondToAction(w, r, err, "")
		return
	}
	p.respondToAction(w, r, nil, ":white_check_mark: Done.")
}

// respondToAction replaces the buttons on the delivered post with the outcome,
// so that a reminder cannot be snoozed twice by pressing the same button again.
func (p *Plugin) respondToAction(w http.ResponseWriter, r *http.Request, err error, text string) {
	if err != nil {
		message := "Something went wrong."
		if IsUserFacing(err) {
			message = err.Error()
		} else {
			p.client.Log.Error("Interactive action failed", "path", r.URL.Path, "error", err.Error())
		}
		writeJSON(w, http.StatusOK, &model.PostActionIntegrationResponse{EphemeralText: ":warning: " + message})
		return
	}

	post := &model.Post{}
	post.AddProp(model.PostPropsAttachments, []*model.MessageAttachment{{Footer: text}})

	writeJSON(w, http.StatusOK, &model.PostActionIntegrationResponse{
		Update:        post,
		EphemeralText: text,
	})
}

// --- Plumbing ---------------------------------------------------------------

// fail turns an error into a response, showing the user only what was written
// for them and logging the rest.
func (p *Plugin) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "reminder not found")
	case IsUserFacing(err):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		p.client.Log.Error("Request failed", "path", r.URL.Path, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "something went wrong")
	}
}

// decodeAction reads an interactive button payload. Unlike the plugin's own
// endpoints it tolerates unknown fields, because the shape of this payload is
// the server's to change and a future addition to it must not break a button.
//
// Only the Context is read from the body. The acting user comes from the
// authenticated header, never from the payload.
func decodeAction(w http.ResponseWriter, r *http.Request) (*model.PostActionIntegrationRequest, bool) {
	var request model.PostActionIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "malformed action")
		return nil, false
	}
	return &request, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A failure here means the client went away mid-response; there is nothing
	// useful left to say on a connection that is already gone.
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

// --- Middleware -------------------------------------------------------------

// logRequests records each request at debug level, since the server does not
// log plugin requests itself.
//
// Neither the query string nor the body is logged: both can carry reminder text.
func (p *Plugin) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		p.logDebug("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(start).String(),
			"user_id", userIDFrom(r),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wroteHeader {
		s.status = status
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// recoverPanic keeps a panicking handler from taking the plugin process with
// it; the server deactivates a plugin that crashes repeatedly.
func (p *Plugin) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				p.client.Log.Error("Recovered from a panic in an HTTP handler",
					"path", r.URL.Path,
					"panic", fmt.Sprint(panicValue),
					"stack", string(debug.Stack()),
				)
				if !recorder.wroteHeader {
					writeError(w, http.StatusInternalServerError, "something went wrong")
				}
			}
		}()
		next.ServeHTTP(recorder, r)
	})
}

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func setSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
