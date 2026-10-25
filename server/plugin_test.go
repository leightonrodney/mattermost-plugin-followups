package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
	"github.com/leightonrodney/mattermost-plugin-followups/server/store"
)

// fakeKV is an in-memory key-value store with the same compare-and-set
// behaviour as the server's. Backing the mocked plugin API with it means the
// plugin-level tests exercise the real storage code, not a stub of it.
type fakeKV struct {
	mu   sync.Mutex
	data map[string][]byte

	// failWrites makes every write fail, to exercise the error paths.
	failWrites bool
}

func newFakeKV() *fakeKV {
	return &fakeKV{data: map[string][]byte{}}
}

func (f *fakeKV) get(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data[key]
}

func (f *fakeKV) set(key string, value []byte, options model.PluginKVSetOptions) (bool, *model.AppError) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failWrites {
		return false, model.NewAppError("set", "kv.write.error", nil, "storage is unavailable", http.StatusInternalServerError)
	}

	if options.Atomic && !bytes.Equal(f.data[key], options.OldValue) {
		return false, nil
	}
	if value == nil {
		delete(f.data, key)
		return true, nil
	}
	f.data[key] = append([]byte(nil), value...)
	return true, nil
}

// testPlugin is a Plugin wired to a mocked server API.
type testPlugin struct {
	*Plugin

	api   *plugintest.API
	kv    *fakeKV
	clock *fakeClock

	postsMu sync.Mutex
	posts   []*model.Post
}

const (
	testBotID     = "bot00000000000000000000000"
	testUserID    = "user0000000000000000000000"
	testOtherUser = "other000000000000000000000"
	testChannelID = "chan0000000000000000000000"
	testTeamID    = "team0000000000000000000000"
)

func newTestPlugin(t *testing.T, tweak ...func(*configuration)) *testPlugin {
	t.Helper()

	api := &plugintest.API{}
	kv := newFakeKV()

	tp := &testPlugin{
		Plugin: &Plugin{},
		api:    api,
		kv:     kv,
		clock:  &fakeClock{now: time.Now().UTC().Truncate(time.Second)},
	}

	api.On("KVGet", mock.AnythingOfType("string")).Return(
		func(key string) []byte { return kv.get(key) },
		func(string) *model.AppError { return nil },
	).Maybe()

	api.On("KVSetWithOptions", mock.AnythingOfType("string"), mock.Anything, mock.Anything).Return(
		func(key string, value []byte, options model.PluginKVSetOptions) (bool, *model.AppError) {
			return kv.set(key, value, options)
		},
	).Maybe()

	for _, level := range []string{"LogDebug", "LogInfo", "LogWarn", "LogError"} {
		api.On(level, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
			mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
		api.On(level, mock.Anything).Maybe()
	}

	api.On("GetUser", mock.AnythingOfType("string")).Return(
		func(userID string) *model.User {
			return &model.User{
				Id:       userID,
				Timezone: map[string]string{"useAutomaticTimezone": "false", "manualTimezone": "UTC"},
			}
		},
		func(string) *model.AppError { return nil },
	).Maybe()

	api.On("GetDirectChannel", mock.Anything, mock.Anything).Return(
		&model.Channel{Id: "dmchannel00000000000000000"}, (*model.AppError)(nil)).Maybe()

	api.On("CreatePost", mock.AnythingOfType("*model.Post")).Return(
		func(post *model.Post) *model.Post {
			tp.postsMu.Lock()
			defer tp.postsMu.Unlock()
			stored := post.Clone()
			stored.Id = model.NewId()
			tp.posts = append(tp.posts, stored)
			return stored
		},
		func(*model.Post) *model.AppError { return nil },
	).Maybe()

	siteURL := "https://mattermost.example.com"
	config := &model.Config{}
	config.SetDefaults()
	config.ServiceSettings.SiteURL = &siteURL
	api.On("GetConfig").Return(config).Maybe()

	api.On("HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything).Return(true).Maybe()
	api.On("GetChannel", mock.Anything).Return(
		&model.Channel{Id: testChannelID, TeamId: testTeamID}, (*model.AppError)(nil)).Maybe()
	api.On("GetTeam", mock.Anything).Return(
		&model.Team{Id: testTeamID, Name: "engineering"}, (*model.AppError)(nil)).Maybe()
	api.On("GetTeamsForUser", mock.Anything).Return(
		[]*model.Team{{Id: testTeamID, Name: "engineering"}}, (*model.AppError)(nil)).Maybe()
	api.On("GetPost", mock.Anything).Return(
		&model.Post{Id: "post0000000000000000000000", ChannelId: testChannelID}, (*model.AppError)(nil)).Maybe()

	conf := defaultConfiguration()
	for _, apply := range tweak {
		apply(conf)
	}

	tp.SetAPI(api)
	tp.client = pluginapi.NewClient(api, nil)
	tp.botID = testBotID
	tp.store = store.New(&tp.client.KV)
	tp.service = newService(tp.store, &tp.client.User, tp.getConfiguration)
	tp.service.now = tp.clock.Now
	tp.configuration.set(conf)
	tp.router = tp.newRouter()

	t.Cleanup(func() { api.AssertExpectations(t) })
	return tp
}

func (tp *testPlugin) deliveredPosts() []*model.Post {
	tp.postsMu.Lock()
	defer tp.postsMu.Unlock()
	return append([]*model.Post(nil), tp.posts...)
}

// runSchedulerAt runs one scheduler pass as if it were the given moment.
func (tp *testPlugin) runSchedulerAt(now time.Time) {
	tp.clock.mu.Lock()
	tp.clock.now = now
	tp.clock.mu.Unlock()
	tp.Plugin.runSchedulerAt(now)
}

func (tp *testPlugin) createDue(t *testing.T, userID string, in time.Duration, message string) *reminder.Reminder {
	t.Helper()
	rec, err := tp.service.Create(userID, createInput{
		Message:  message,
		DueAt:    tp.clock.Now().Add(in),
		Timezone: "UTC",
	})
	require.NoError(t, err)
	return rec
}

// --- Scheduler -------------------------------------------------------------

func TestSchedulerDeliversADueReminder(t *testing.T) {
	tp := newTestPlugin(t)
	rec := tp.createDue(t, testUserID, 30*time.Minute, "check the build")

	// Nothing is due yet.
	tp.runSchedulerAt(tp.clock.Now())
	assert.Empty(t, tp.deliveredPosts())

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0].Message, "check the build")
	assert.Equal(t, testBotID, posts[0].UserId)

	after, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusDelivered, after.Status)
	assert.Equal(t, 1, after.Occurrences)
}

func TestSchedulerDeliversOnlyOnce(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "only once")

	at := tp.clock.Now().Add(31 * time.Minute)
	for range 5 {
		tp.runSchedulerAt(at)
	}

	assert.Len(t, tp.deliveredPosts(), 1, "re-running the scheduler must not re-deliver")
}

func TestSchedulerIsSafeWithConcurrentRuns(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "contended")

	at := tp.clock.Now().Add(31 * time.Minute)

	// Two nodes running at once, which is what a cluster lock expiring during a
	// long run would produce.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			tp.Plugin.runSchedulerAt(at)
		})
	}
	wg.Wait()

	assert.Len(t, tp.deliveredPosts(), 1, "per-reminder claiming must survive concurrent scheduler runs")
}

func TestSchedulerRecoversAfterARestart(t *testing.T) {
	tp := newTestPlugin(t)
	tp.createDue(t, testUserID, 30*time.Minute, "survive the restart")

	// The plugin is rebuilt against the same storage, as a restart would do.
	// Nothing is held in memory between the two, so delivery can only work if
	// the index and cursor did their job.
	restarted := &Plugin{}
	restarted.SetAPI(tp.api)
	restarted.client = pluginapi.NewClient(tp.api, nil)
	restarted.botID = testBotID
	restarted.store = store.New(&restarted.client.KV)
	restarted.service = newService(restarted.store, &restarted.client.User, restarted.getConfiguration)
	restarted.configuration.set(defaultConfiguration())

	restarted.runSchedulerAt(tp.clock.Now().Add(2 * time.Hour))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0].Message, "survive the restart")
}

func TestSchedulerDeliversWhileCatchingUpOverSeveralHours(t *testing.T) {
	tp := newTestPlugin(t)

	// Three reminders spread across three different hour buckets, none of which
	// the scheduler has seen.
	tp.createDue(t, testUserID, time.Hour, "first")
	tp.createDue(t, testUserID, 2*time.Hour, "second")
	tp.createDue(t, testUserID, 3*time.Hour, "third")

	tp.runSchedulerAt(tp.clock.Now().Add(4 * time.Hour))

	assert.Len(t, tp.deliveredPosts(), 3, "a catch-up run must sweep every hour it passed over")
}

func TestSchedulerRespectsTheDisabledSetting(t *testing.T) {
	tp := newTestPlugin(t, func(c *configuration) { c.EnableScheduler = false })
	tp.createDue(t, testUserID, 30*time.Minute, "held back")

	tp.runSchedulerAt(tp.clock.Now().Add(time.Hour))
	assert.Empty(t, tp.deliveredPosts(), "delivery is off")

	// Turning it back on delivers the reminder late rather than losing it.
	conf := defaultConfiguration()
	tp.configuration.set(conf)
	tp.runSchedulerAt(tp.clock.Now().Add(2 * time.Hour))
	assert.Len(t, tp.deliveredPosts(), 1)
}

func TestSchedulerAdvancesARecurringReminder(t *testing.T) {
	tp := newTestPlugin(t)

	rec, err := tp.service.Create(testUserID, createInput{
		Message:    "stand up",
		DueAt:      tp.clock.Now().Add(30 * time.Minute),
		Timezone:   "UTC",
		Recurrence: &reminder.Recurrence{Type: reminder.RecurrenceInterval, EverySeconds: 3600},
	})
	require.NoError(t, err)

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))
	require.Len(t, tp.deliveredPosts(), 1)

	tp.runSchedulerAt(tp.clock.Now().Add(95 * time.Minute))
	assert.Len(t, tp.deliveredPosts(), 2, "the series must fire again")

	after, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusScheduled, after.Status)
	assert.Equal(t, 2, after.Occurrences)
	assert.Contains(t, tp.deliveredPosts()[1].Message, "Repeats every hour")
}

func TestSchedulerRetriesAFailedDelivery(t *testing.T) {
	tp := newTestPlugin(t)
	rec := tp.createDue(t, testUserID, 30*time.Minute, "flaky")

	// Posting fails for this run.
	tp.api.ExpectedCalls = filterOutCall(tp.api.ExpectedCalls, "CreatePost")
	tp.api.On("CreatePost", mock.Anything).Return(
		(*model.Post)(nil),
		model.NewAppError("CreatePost", "post.failed", nil, "the server said no", http.StatusInternalServerError),
	).Maybe()

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	after, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusScheduled, after.Status, "a failed delivery must be retried, not dropped")
	assert.Equal(t, 1, after.Attempts)
	assert.NotZero(t, after.NextAttemptAt)
}

func filterOutCall(calls []*mock.Call, method string) []*mock.Call {
	kept := calls[:0]
	for _, call := range calls {
		if call.Method != method {
			kept = append(kept, call)
		}
	}
	return kept
}

func TestDeliveredPostCarriesAPermalinkAndActions(t *testing.T) {
	tp := newTestPlugin(t)

	_, err := tp.service.Create(testUserID, createInput{
		Message:   "follow this up",
		DueAt:     tp.clock.Now().Add(30 * time.Minute),
		Timezone:  "UTC",
		ChannelID: testChannelID,
		PostID:    "post0000000000000000000000",
		TeamID:    testTeamID,
	})
	require.NoError(t, err)

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0].Message, "https://mattermost.example.com/engineering/pl/post0000000000000000000000")

	attachments, ok := posts[0].GetProp(model.PostPropsAttachments).([]*model.MessageAttachment)
	require.True(t, ok, "the delivered post must carry its action buttons")
	require.Len(t, attachments, 1)
	assert.NotEmpty(t, attachments[0].Actions)
}

func TestDeliveredPostOmitsAPermalinkTheUserCannotOpen(t *testing.T) {
	tp := newTestPlugin(t)

	// The user has since lost access to the channel.
	tp.api.ExpectedCalls = filterOutCall(tp.api.ExpectedCalls, "HasPermissionToChannel")
	tp.api.On("HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything).Return(false).Maybe()

	_, err := tp.service.Create(testUserID, createInput{
		Message:   "follow this up",
		DueAt:     tp.clock.Now().Add(30 * time.Minute),
		Timezone:  "UTC",
		ChannelID: testChannelID,
		PostID:    "post0000000000000000000000",
		TeamID:    testTeamID,
	})
	require.NoError(t, err)

	tp.runSchedulerAt(tp.clock.Now().Add(31 * time.Minute))

	posts := tp.deliveredPosts()
	require.Len(t, posts, 1)
	assert.NotContains(t, posts[0].Message, "/pl/", "a permalink must not be offered for a channel the user cannot read")
	assert.Contains(t, posts[0].Message, "no longer available")
}

// --- HTTP API --------------------------------------------------------------

func (tp *testPlugin) request(method, path, userID string, body any) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		reader = strings.NewReader(string(encoded))
	} else {
		reader = strings.NewReader("")
	}

	request := httptest.NewRequest(method, path, reader)
	if userID != "" {
		request.Header.Set("Mattermost-User-ID", userID)
	}

	recorder := httptest.NewRecorder()
	tp.ServeHTTP(nil, recorder, request)
	return recorder
}

func TestAPIRequiresAuthentication(t *testing.T) {
	tp := newTestPlugin(t)

	for _, path := range []string{"/api/v1/reminders", "/api/v1/config"} {
		response := tp.request(http.MethodGet, path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, response.Code, path)
	}

	response := tp.request(http.MethodPost, "/api/v1/reminders", "", map[string]any{"text": "me in 1 hour to x"})
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestAPICreateAndList(t *testing.T) {
	tp := newTestPlugin(t)

	response := tp.request(http.MethodPost, "/api/v1/reminders", testUserID,
		map[string]any{"text": "me in 2 hours to check the build"})
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())

	var created reminderPayload
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	assert.Equal(t, "check the build", created.Message)
	assert.Equal(t, string(reminder.StatusScheduled), created.Status)

	response = tp.request(http.MethodGet, "/api/v1/reminders", testUserID, nil)
	require.Equal(t, http.StatusOK, response.Code)

	var listed struct {
		Reminders []reminderPayload `json:"reminders"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &listed))
	require.Len(t, listed.Reminders, 1)
	assert.Equal(t, created.ID, listed.Reminders[0].ID)
}

func TestAPIDoesNotLeakBetweenUsers(t *testing.T) {
	tp := newTestPlugin(t)

	response := tp.request(http.MethodPost, "/api/v1/reminders", testUserID,
		map[string]any{"text": "me in 2 hours to private business"})
	require.Equal(t, http.StatusCreated, response.Code)

	var created reminderPayload
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))

	t.Run("the other user sees nothing", func(t *testing.T) {
		response := tp.request(http.MethodGet, "/api/v1/reminders", testOtherUser, nil)
		require.Equal(t, http.StatusOK, response.Code)
		assert.NotContains(t, response.Body.String(), "private business")
	})

	for _, probe := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"cancel", http.MethodPost, "/api/v1/reminders/" + created.ID + "/cancel", nil},
		{"snooze", http.MethodPost, "/api/v1/reminders/" + created.ID + "/snooze", map[string]any{"minutes": 30}},
		{"complete", http.MethodPost, "/api/v1/reminders/" + created.ID + "/complete", nil},
		{"delete", http.MethodDelete, "/api/v1/reminders/" + created.ID, nil},
		{"update", http.MethodPut, "/api/v1/reminders/" + created.ID, map[string]any{"message": "mine now"}},
	} {
		t.Run(probe.name+" is refused", func(t *testing.T) {
			response := tp.request(probe.method, probe.path, testOtherUser, probe.body)
			assert.Equal(t, http.StatusNotFound, response.Code,
				"another user's reminder must be indistinguishable from one that does not exist")
		})
	}

	t.Run("the owner's reminder is untouched", func(t *testing.T) {
		still, err := tp.service.Get(testUserID, created.ID)
		require.NoError(t, err)
		assert.Equal(t, reminder.StatusScheduled, still.Status)
		assert.Equal(t, "private business", still.Message)
	})
}

func TestAPIRejectsBadInput(t *testing.T) {
	tp := newTestPlugin(t)

	t.Run("unparseable phrase", func(t *testing.T) {
		response := tp.request(http.MethodPost, "/api/v1/reminders", testUserID, map[string]any{"text": "me to do a thing"})
		assert.Equal(t, http.StatusBadRequest, response.Code)
	})

	t.Run("no time at all", func(t *testing.T) {
		response := tp.request(http.MethodPost, "/api/v1/reminders", testUserID, map[string]any{"message": "no when"})
		assert.Equal(t, http.StatusBadRequest, response.Code)
	})

	t.Run("unknown field", func(t *testing.T) {
		response := tp.request(http.MethodPost, "/api/v1/reminders", testUserID, map[string]any{"nonsense": true})
		assert.Equal(t, http.StatusBadRequest, response.Code)
	})

	t.Run("malformed body", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/reminders", strings.NewReader("{not json"))
		request.Header.Set("Mattermost-User-ID", testUserID)
		recorder := httptest.NewRecorder()
		tp.ServeHTTP(nil, recorder, request)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("unknown route", func(t *testing.T) {
		response := tp.request(http.MethodGet, "/api/v1/nope", testUserID, nil)
		assert.Equal(t, http.StatusNotFound, response.Code)
	})
}

func TestAPISetsSecurityHeaders(t *testing.T) {
	tp := newTestPlugin(t)
	response := tp.request(http.MethodGet, "/api/v1/config", testUserID, nil)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", response.Header().Get("Referrer-Policy"))
}

func TestAPIParsePreview(t *testing.T) {
	tp := newTestPlugin(t)

	response := tp.request(http.MethodPost, "/api/v1/parse", testUserID,
		map[string]any{"text": "me every weekday at 9am to post the update"})
	require.Equal(t, http.StatusOK, response.Code)

	var preview struct {
		OK         bool   `json:"ok"`
		DueAt      int64  `json:"due_at"`
		Message    string `json:"message"`
		RepeatText string `json:"repeat_text"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &preview))
	assert.True(t, preview.OK)
	assert.Equal(t, "post the update", preview.Message)
	assert.Contains(t, preview.RepeatText, "Monday")

	t.Run("a bad phrase reports why rather than failing", func(t *testing.T) {
		response := tp.request(http.MethodPost, "/api/v1/parse", testUserID, map[string]any{"text": "gibberish"})
		require.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), `"ok":false`)
	})
}

func TestAPISnoozeAndComplete(t *testing.T) {
	tp := newTestPlugin(t)

	response := tp.request(http.MethodPost, "/api/v1/reminders", testUserID,
		map[string]any{"text": "me in 2 hours to check the build"})
	require.Equal(t, http.StatusCreated, response.Code)
	var created reminderPayload
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))

	response = tp.request(http.MethodPost, "/api/v1/reminders/"+created.ID+"/snooze", testUserID,
		map[string]any{"minutes": 45})
	require.Equal(t, http.StatusOK, response.Code)

	var snoozed reminderPayload
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &snoozed))
	assert.Equal(t, tp.clock.Now().Add(45*time.Minute).UnixMilli(), snoozed.DueAt,
		"snooze is measured from now, not from the original due time")
	assert.NotEqual(t, created.DueAt, snoozed.DueAt)

	response = tp.request(http.MethodPost, "/api/v1/reminders/"+created.ID+"/complete", testUserID, nil)
	require.Equal(t, http.StatusOK, response.Code)

	after, err := tp.service.Get(testUserID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusDelivered, after.Status)
}

// --- Interactive buttons ---------------------------------------------------

func TestActionUsesTheAuthenticatedUserNotThePayload(t *testing.T) {
	tp := newTestPlugin(t)

	rec := tp.createDue(t, testUserID, 2*time.Hour, "private business")

	// A crafted payload claiming to be the owner, sent by somebody else. The
	// server sets Mattermost-User-ID, so the claim in the body must count for
	// nothing.
	body := map[string]any{
		"user_id": testUserID,
		"context": map[string]any{contextReminderID: rec.ID, contextMinutes: 30},
	}

	response := tp.request(http.MethodPost, "/api/v1/actions/snooze", testOtherUser, body)
	require.Equal(t, http.StatusOK, response.Code, "an action always answers the server with 200")
	assert.Contains(t, response.Body.String(), "not found")

	unchanged, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, rec.DueAt, unchanged.DueAt, "the reminder must not have moved")
}

func TestActionSnoozeMovesTheOwnersReminder(t *testing.T) {
	tp := newTestPlugin(t)
	rec := tp.createDue(t, testUserID, 2*time.Hour, "move me")

	body := map[string]any{"context": map[string]any{contextReminderID: rec.ID, contextMinutes: 30}}
	response := tp.request(http.MethodPost, "/api/v1/actions/snooze", testUserID, body)
	require.Equal(t, http.StatusOK, response.Code)

	moved, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, tp.clock.Now().Add(30*time.Minute).UnixMilli(), moved.DueAt)
}

func TestActionTomorrowPresetUsesTheUsersMorning(t *testing.T) {
	tp := newTestPlugin(t)
	rec := tp.createDue(t, testUserID, 2*time.Hour, "tomorrow please")

	body := map[string]any{"context": map[string]any{contextReminderID: rec.ID, contextMinutes: 0}}
	response := tp.request(http.MethodPost, "/api/v1/actions/snooze", testUserID, body)
	require.Equal(t, http.StatusOK, response.Code)

	moved, err := tp.service.Get(testUserID, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, reminder.DefaultHour, moved.DueTime().UTC().Hour())
}

func TestActionRejectsAMalformedPayload(t *testing.T) {
	tp := newTestPlugin(t)

	response := tp.request(http.MethodPost, "/api/v1/actions/snooze", testUserID, map[string]any{"context": map[string]any{}})
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

// --- Configuration ---------------------------------------------------------

func TestConfigurationIsClamped(t *testing.T) {
	config := &configuration{
		SchedulerIntervalSeconds:     -5,
		MaxRemindersPerUser:          0,
		MaxRecurringRemindersPerUser: -1,
		MinIntervalSeconds:           1,
		MaxHorizonDays:               999999,
		CompletedRetentionDays:       -3,
	}

	assert.Equal(t, time.Duration(minSchedulerIntervalSeconds)*time.Second, config.SchedulerInterval())
	assert.Equal(t, 1, config.ReminderLimit())
	assert.Equal(t, 0, config.RecurringLimit())
	assert.Equal(t, time.Duration(minIntervalFloor)*time.Second, config.MinInterval())
	assert.Equal(t, time.Duration(maxHorizonCeiling)*24*time.Hour, config.MaxHorizon())
	assert.Equal(t, time.Duration(0), config.Retention())
}

func TestConfigurationDefaultsMatchTheManifest(t *testing.T) {
	// A default that drifts from plugin.json would give a fresh install
	// different behaviour from the one the settings page describes.
	defaults := map[string]any{}
	for _, setting := range manifest.SettingsSchema.Settings {
		defaults[setting.Key] = setting.Default
	}

	config := defaultConfiguration()
	assert.Equal(t, defaults["EnableScheduler"], config.EnableScheduler)
	assert.Equal(t, defaults["EnableDebugLogging"], config.EnableDebugLogging)
	assertNumericDefault(t, defaults, "SchedulerIntervalSeconds", config.SchedulerIntervalSeconds)
	assertNumericDefault(t, defaults, "MaxRemindersPerUser", config.MaxRemindersPerUser)
	assertNumericDefault(t, defaults, "MaxRecurringRemindersPerUser", config.MaxRecurringRemindersPerUser)
	assertNumericDefault(t, defaults, "MinIntervalSeconds", config.MinIntervalSeconds)
	assertNumericDefault(t, defaults, "MaxHorizonDays", config.MaxHorizonDays)
	assertNumericDefault(t, defaults, "CompletedRetentionDays", config.CompletedRetentionDays)
}

func assertNumericDefault(t *testing.T, defaults map[string]any, key string, got int) {
	t.Helper()
	raw, ok := defaults[key]
	require.True(t, ok, "plugin.json has no setting %q", key)
	number, ok := raw.(float64)
	require.True(t, ok, "setting %q has a non-numeric default %v", key, raw)
	assert.Equal(t, int(number), got, "default for %s", key)
}
