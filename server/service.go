package main

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
	"github.com/leightonrodney/mattermost-plugin-followups/server/store"
)

// userLookup is the slice of the Mattermost user API the service needs. Keeping
// it narrow is what lets the service be tested without a server.
type userLookup interface {
	Get(userID string) (*model.User, error)
}

// service holds every rule about reminders that is not about how they are
// stored or how they are delivered: quotas, ownership, the delivery claim
// protocol, and advancing a recurring series.
//
// Every method takes the acting user's ID and will only ever touch that user's
// record. There is no code path that reads one user's reminders on behalf of
// another, which is what makes cross-user exposure impossible rather than
// merely unlikely.
type service struct {
	store  *store.Store
	users  userLookup
	config func() *configuration

	// now is the clock, injected so that tests can control time.
	now func() time.Time
}

func newService(st *store.Store, users userLookup, config func() *configuration) *service {
	return &service{store: st, users: users, config: config, now: time.Now}
}

// Now is the service's clock. Handlers read the time through it rather than
// calling time.Now themselves, so that a duration worked out in a handler and
// the instant the service adds it to are measured against the same clock.
func (s *service) Now() time.Time {
	return s.now()
}

// errSkip means a reminder was not eligible for the operation and the caller
// should move on. It is not a failure.
var errSkip = errors.New("reminder not eligible")

// userError marks an error whose message was written for the person who caused
// it, and so may be shown to them word for word. Everything else is an internal
// failure: it is logged, and the user sees a generic message.
//
// The distinction is a security boundary as much as a usability one. Internal
// errors can carry key names, user IDs and storage details that have no
// business reaching a channel.
type userError struct{ err error }

func (e userError) Error() string { return e.err.Error() }
func (e userError) Unwrap() error { return e.err }

// userErrorf builds an error that is safe to show to the user.
func userErrorf(format string, a ...any) error {
	return userError{fmt.Errorf(format, a...)}
}

// IsUserFacing reports whether an error carries a message meant for the user.
func IsUserFacing(err error) bool {
	var target userError
	if errors.As(err, &target) {
		return true
	}
	return errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrRecordTooLarge) ||
		errors.Is(err, reminder.ErrEmptyMessage) ||
		errors.Is(err, reminder.ErrMessageTooLong)
}

// createInput is a validated request to create a reminder.
type createInput struct {
	Message    string
	DueAt      time.Time
	Recurrence *reminder.Recurrence

	// Timezone is the IANA name recurrence is computed in.
	Timezone string

	// ChannelID, PostID and TeamID link the reminder to the message it came
	// from. Only the identifiers are kept; the message text is never copied.
	ChannelID string
	PostID    string
	TeamID    string
}

// Timezone returns the user's configured timezone, falling back to UTC when the
// server has none for them or the name is one the host does not know.
func (s *service) Timezone(userID string) *time.Location {
	user, err := s.users.Get(userID)
	if err != nil || user == nil {
		return time.UTC
	}
	name := user.GetPreferredTimezone()
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Create validates and stores a new reminder for the given user.
//
// The due-time index is written before the reminder record. The orderings fail
// differently and this is the harmless one: an index entry with no reminder
// behind it is ignored by the scheduler, whereas a reminder with no index entry
// would silently never fire.
func (s *service) Create(userID string, input createInput) (*reminder.Reminder, error) {
	config := s.config()
	now := s.now()

	message, err := reminder.NormalizeMessage(input.Message)
	if err != nil {
		return nil, err
	}

	if scheduleErr := s.validateSchedule(now, input.DueAt, input.Recurrence, config); scheduleErr != nil {
		return nil, scheduleErr
	}

	rec := &reminder.Reminder{
		SchemaVersion: reminder.SchemaVersion,
		ID:            model.NewId(),
		UserID:        userID,
		Message:       message,
		Timezone:      input.Timezone,
		CreateAt:      now.UnixMilli(),
		UpdateAt:      now.UnixMilli(),
		DueAt:         input.DueAt.UnixMilli(),
		Recurrence:    input.Recurrence,
		ChannelID:     input.ChannelID,
		PostID:        input.PostID,
		TeamID:        input.TeamID,
		Status:        reminder.StatusScheduled,
	}

	if validationErr := rec.Validate(); validationErr != nil {
		return nil, userError{validationErr}
	}

	if indexErr := s.store.Index(userID, rec.ID, input.DueAt); indexErr != nil {
		return nil, indexErr
	}

	err = s.store.UpdateUser(userID, func(record *store.UserRecord) error {
		if quotaErr := checkQuota(record, rec, config); quotaErr != nil {
			return quotaErr
		}
		record.Reminders[rec.ID] = rec
		return nil
	})
	if err != nil {
		return nil, err
	}

	return rec, nil
}

// validateSchedule enforces the administrator's bounds on when a reminder may
// fire and how often it may repeat.
func (s *service) validateSchedule(now, dueAt time.Time, rec *reminder.Recurrence, config *configuration) error {
	minInterval := config.MinInterval()

	// A little slack absorbs the time between parsing the command and getting
	// here, so that "in 1 minute" is not rejected for being a second short.
	if dueAt.Before(now.Add(minInterval - 5*time.Second)) {
		return userErrorf("reminders have to be at least %s from now on this server", humanDuration(minInterval))
	}
	if dueAt.After(now.Add(config.MaxHorizon())) {
		return userErrorf("reminders can only be set up to %s ahead on this server", humanDuration(config.MaxHorizon()))
	}

	if rec == nil || rec.Type == reminder.RecurrenceNone {
		return nil
	}
	if err := rec.Validate(); err != nil {
		return userError{err}
	}
	if rec.Type == reminder.RecurrenceInterval && time.Duration(rec.EverySeconds)*time.Second < minInterval {
		return userErrorf("repeating reminders have to be at least %s apart on this server", humanDuration(minInterval))
	}
	return nil
}

// checkQuota rejects a creation that would take the user past the configured
// limits. It runs inside the compare-and-set block so that two reminders
// created at the same instant cannot both see room for one more.
func checkQuota(record *store.UserRecord, candidate *reminder.Reminder, config *configuration) error {
	var active, recurring int
	for _, existing := range record.Reminders {
		if existing.Status.IsTerminal() {
			continue
		}
		active++
		if existing.IsRecurring() {
			recurring++
		}
	}

	if active >= config.ReminderLimit() {
		return userErrorf("you already have %d reminders set, which is the limit on this server; cancel one first", config.ReminderLimit())
	}
	if candidate.IsRecurring() && recurring >= config.RecurringLimit() {
		if config.RecurringLimit() == 0 {
			return userErrorf("repeating reminders are turned off on this server")
		}
		return userErrorf("you already have %d repeating reminders, which is the limit on this server", config.RecurringLimit())
	}
	return nil
}

// List returns the user's reminders, soonest first, with finished ones after
// the pending ones.
func (s *service) List(userID string) ([]*reminder.Reminder, error) {
	record, err := s.store.GetUser(userID)
	if err != nil {
		return nil, err
	}

	list := make([]*reminder.Reminder, 0, len(record.Reminders))
	for _, rec := range record.Reminders {
		list = append(list, rec)
	}
	sortReminders(list)
	return list, nil
}

// sortReminders puts pending reminders first in due order, then finished ones
// most recent first, which is the order both the command and the UI want.
func sortReminders(list []*reminder.Reminder) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Status.IsTerminal() != b.Status.IsTerminal() {
			return !a.Status.IsTerminal()
		}
		if a.Status.IsTerminal() {
			return a.UpdateAt > b.UpdateAt
		}
		return a.DueAt < b.DueAt
	})
}

// Get returns one of the user's reminders.
func (s *service) Get(userID, reminderID string) (*reminder.Reminder, error) {
	record, err := s.store.GetUser(userID)
	if err != nil {
		return nil, err
	}
	rec, ok := record.Reminders[reminderID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return rec, nil
}

// mutateOne applies a change to one of the user's reminders under
// compare-and-set, refusing anything the user does not own.
//
// The lookup is scoped to the acting user's record, so a reminder ID belonging
// to somebody else is indistinguishable from one that does not exist.
func (s *service) mutateOne(userID, reminderID string, mutate func(*reminder.Reminder) error) (*reminder.Reminder, error) {
	var result *reminder.Reminder

	err := s.store.UpdateUser(userID, func(record *store.UserRecord) error {
		rec, ok := record.Reminders[reminderID]
		if !ok {
			return store.ErrNotFound
		}
		// Defence in depth: the record is keyed by user, so this can only fail
		// if the stored data was tampered with or a bug crossed the wires.
		if rec.UserID != userID {
			return store.ErrNotFound
		}

		candidate := rec.Clone()
		if err := mutate(candidate); err != nil {
			return err
		}
		candidate.UpdateAt = s.now().UnixMilli()

		if err := candidate.Validate(); err != nil {
			return userError{err}
		}

		record.Reminders[reminderID] = candidate
		result = candidate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Cancel stops a reminder without erasing it, so that it stays visible in the
// user's history until retention removes it.
func (s *service) Cancel(userID, reminderID string) (*reminder.Reminder, error) {
	return s.mutateOne(userID, reminderID, func(rec *reminder.Reminder) error {
		if rec.Status.IsTerminal() {
			return userErrorf("that reminder has already finished")
		}
		rec.Status = reminder.StatusCancelled
		rec.ClaimedAt = 0
		return nil
	})
}

// Delete removes a reminder outright.
func (s *service) Delete(userID, reminderID string) error {
	return s.store.UpdateUser(userID, func(record *store.UserRecord) error {
		if _, ok := record.Reminders[reminderID]; !ok {
			return store.ErrNotFound
		}
		delete(record.Reminders, reminderID)
		return nil
	})
}

// Reschedule moves a reminder, optionally replacing its repeat rule.
func (s *service) Reschedule(userID, reminderID string, dueAt time.Time, rec *reminder.Recurrence) (*reminder.Reminder, error) {
	config := s.config()
	if err := s.validateSchedule(s.now(), dueAt, rec, config); err != nil {
		return nil, err
	}

	updated, err := s.mutateOne(userID, reminderID, func(existing *reminder.Reminder) error {
		// Reviving a cancelled or delivered reminder has to respect the quota
		// the same way creating one does.
		if existing.Status.IsTerminal() {
			record, err := s.store.GetUser(userID)
			if err != nil {
				return err
			}
			candidate := existing.Clone()
			candidate.Recurrence = rec
			if err := checkQuota(record, candidate, config); err != nil {
				return err
			}
		}
		existing.DueAt = dueAt.UnixMilli()
		existing.Recurrence = rec
		existing.Status = reminder.StatusScheduled
		existing.ClaimedAt = 0
		existing.Attempts = 0
		existing.NextAttemptAt = 0
		existing.SnoozedFrom = 0
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := s.store.Index(userID, reminderID, dueAt); err != nil {
		return nil, err
	}
	return updated, nil
}

// Retext replaces a reminder's message.
func (s *service) Retext(userID, reminderID, message string) (*reminder.Reminder, error) {
	normalized, err := reminder.NormalizeMessage(message)
	if err != nil {
		return nil, err
	}
	return s.mutateOne(userID, reminderID, func(rec *reminder.Reminder) error {
		rec.Message = normalized
		return nil
	})
}

// Snooze postpones a reminder by d.
//
// A one-off reminder is simply moved, and is revived if it had already been
// delivered. A repeating reminder is left alone and a one-off copy is created
// instead, because postponing one occurrence of a series should not drag the
// whole series along with it.
func (s *service) Snooze(userID, reminderID string, d time.Duration) (*reminder.Reminder, error) {
	existing, err := s.Get(userID, reminderID)
	if err != nil {
		return nil, err
	}

	dueAt := s.now().Add(d)

	if existing.IsRecurring() {
		return s.Create(userID, createInput{
			Message:   existing.Message,
			DueAt:     dueAt,
			Timezone:  existing.Timezone,
			ChannelID: existing.ChannelID,
			PostID:    existing.PostID,
			TeamID:    existing.TeamID,
		})
	}

	if scheduleErr := s.validateSchedule(s.now(), dueAt, nil, s.config()); scheduleErr != nil {
		return nil, scheduleErr
	}

	updated, err := s.mutateOne(userID, reminderID, func(rec *reminder.Reminder) error {
		if rec.Status == reminder.StatusCancelled {
			return userErrorf("that reminder was cancelled; set a new one instead")
		}
		rec.SnoozedFrom = rec.DueAt
		rec.DueAt = dueAt.UnixMilli()
		rec.Status = reminder.StatusScheduled
		rec.ClaimedAt = 0
		rec.Attempts = 0
		rec.NextAttemptAt = 0
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := s.store.Index(userID, reminderID, dueAt); err != nil {
		return nil, err
	}
	return updated, nil
}

// Complete marks a one-off reminder as done. A repeating reminder is already
// scheduled for its next occurrence by the time the user sees it, so completing
// one acknowledges the occurrence without touching the series.
func (s *service) Complete(userID, reminderID string) (*reminder.Reminder, error) {
	return s.mutateOne(userID, reminderID, func(rec *reminder.Reminder) error {
		if rec.IsRecurring() {
			return nil
		}
		rec.Status = reminder.StatusDelivered
		rec.ClaimedAt = 0
		return nil
	})
}

// --- Delivery protocol -----------------------------------------------------
//
// Delivery is a three-step handshake so that two scheduler workers, or one
// worker running twice after a crash, cannot deliver the same occurrence twice
// in the normal case:
//
//  1. Claim moves the reminder from scheduled to delivering under
//     compare-and-set. Exactly one caller can win.
//  2. The caller posts the message.
//  3. Delivered or Failed moves it out of delivering.
//
// A worker that dies between steps 1 and 3 leaves the reminder claimed. After
// ClaimTimeout another worker reclaims it and tries again, which is why the
// guarantee is at-least-once rather than exactly-once. See docs/ARCHITECTURE.md.

// Claim takes ownership of a due reminder for delivery. It returns errSkip when
// the reminder is not eligible, which is the common case for an index entry
// that has already been handled.
func (s *service) Claim(userID, reminderID string, now time.Time) (*reminder.Reminder, error) {
	return s.mutateOne(userID, reminderID, func(rec *reminder.Reminder) error {
		switch rec.Status {
		case reminder.StatusScheduled:
			// Ready only once both the due time and any retry backoff have passed.
			if rec.DueAt > now.UnixMilli() || rec.NextAttemptAt > now.UnixMilli() {
				return errSkip
			}
		case reminder.StatusDelivering:
			// Reclaim a stalled delivery, but not one still in progress.
			if now.UnixMilli()-rec.ClaimedAt < reminder.ClaimTimeout.Milliseconds() {
				return errSkip
			}
		case reminder.StatusDelivered, reminder.StatusCancelled, reminder.StatusFailed:
			return errSkip
		default:
			return errSkip
		}

		rec.Status = reminder.StatusDelivering
		rec.ClaimedAt = now.UnixMilli()
		rec.Attempts++
		return nil
	})
}

// Delivered records a successful delivery. A one-off reminder finishes here; a
// repeating one is advanced to its next occurrence and re-indexed, so that the
// series survives even if the scheduler stops immediately afterwards.
func (s *service) Delivered(userID, reminderID string, now time.Time) (*reminder.Reminder, error) {
	var nextDue time.Time

	updated, err := s.mutateOne(userID, reminderID, func(rec *reminder.Reminder) error {
		rec.LastDeliveredAt = now.UnixMilli()
		rec.Occurrences++
		rec.ClaimedAt = 0
		rec.Attempts = 0
		rec.NextAttemptAt = 0
		rec.SnoozedFrom = 0

		if !rec.IsRecurring() {
			rec.Status = reminder.StatusDelivered
			return nil
		}

		next := rec.Recurrence.Next(time.UnixMilli(rec.DueAt), now, rec.Location())
		if next.IsZero() {
			// A rule that cannot be advanced would otherwise sit in delivering
			// forever; stop it and leave it visible to the user.
			rec.Status = reminder.StatusFailed
			return nil
		}

		// A horizon change can leave an existing series reaching past the new
		// limit. Retire it rather than scheduling beyond what is allowed.
		if next.After(now.Add(s.config().MaxHorizon())) {
			rec.Status = reminder.StatusDelivered
			return nil
		}

		rec.DueAt = next.UnixMilli()
		rec.Status = reminder.StatusScheduled
		nextDue = next
		return nil
	})
	if err != nil {
		return nil, err
	}

	if !nextDue.IsZero() {
		if err := s.store.Index(userID, reminderID, nextDue); err != nil {
			return updated, err
		}
	}
	return updated, nil
}

// Failed records an unsuccessful delivery, scheduling a backoff retry until the
// attempt limit is reached.
func (s *service) Failed(userID, reminderID string, now time.Time) (*reminder.Reminder, error) {
	var retryAt time.Time

	updated, err := s.mutateOne(userID, reminderID, func(rec *reminder.Reminder) error {
		rec.ClaimedAt = 0

		if rec.Attempts >= reminder.MaxAttempts {
			rec.Status = reminder.StatusFailed
			return nil
		}

		retryAt = now.Add(retryBackoff(rec.Attempts))
		rec.Status = reminder.StatusScheduled
		rec.NextAttemptAt = retryAt.UnixMilli()
		return nil
	})
	if err != nil {
		return nil, err
	}

	if !retryAt.IsZero() {
		if err := s.store.Index(userID, reminderID, retryAt); err != nil {
			return updated, err
		}
	}
	return updated, nil
}

// retryBackoff spaces out delivery retries so that a server having a bad minute
// is not hammered by every pending reminder at once.
func retryBackoff(attempts int) time.Duration {
	switch attempts {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// Purge removes finished reminders that are past the retention period, and
// trims the oldest when a user has accumulated more than the cap. It returns
// how many were removed.
//
// The candidates are worked out from a plain read first, so that the common
// case — nothing to remove — costs one read and no write at all.
func (s *service) Purge(userID string, now time.Time) (int, error) {
	record, err := s.store.GetUser(userID)
	if err != nil {
		return 0, err
	}

	doomed := expiredIDs(record, now, s.config().Retention())
	if len(doomed) == 0 {
		return 0, nil
	}

	removed := 0
	err = s.store.UpdateUser(userID, func(record *store.UserRecord) error {
		// Recomputed inside the compare-and-set block because the record may
		// have changed since the read above.
		removed = 0
		for _, id := range expiredIDs(record, now, s.config().Retention()) {
			delete(record.Reminders, id)
			removed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// expiredIDs lists the finished reminders that retention and the history cap
// say should go.
func expiredIDs(record *store.UserRecord, now time.Time, retention time.Duration) []string {
	finished := make([]*reminder.Reminder, 0, len(record.Reminders))
	for _, rec := range record.Reminders {
		if rec.Status.IsTerminal() {
			finished = append(finished, rec)
		}
	}

	cutoff := now.Add(-retention).UnixMilli()
	var doomed []string
	kept := make([]*reminder.Reminder, 0, len(finished))

	for _, rec := range finished {
		if rec.UpdateAt <= cutoff {
			doomed = append(doomed, rec.ID)
			continue
		}
		kept = append(kept, rec)
	}

	// Whatever survived the cutoff is still capped, so that a frequent
	// recurring reminder cannot grow the record without bound.
	if len(kept) > maxFinishedPerUser {
		slices.SortFunc(kept, func(a, b *reminder.Reminder) int {
			return int(b.UpdateAt - a.UpdateAt)
		})
		for _, rec := range kept[maxFinishedPerUser:] {
			doomed = append(doomed, rec.ID)
		}
	}

	return doomed
}

// maxFinishedPerUser caps retained history independently of the retention
// period.
const maxFinishedPerUser = 100

// humanDuration renders a duration the way the command responses phrase it.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		days := int(d / (24 * time.Hour))
		return pluralize(days, "day")
	case d >= time.Hour && d%time.Hour == 0:
		return pluralize(int(d/time.Hour), "hour")
	case d >= time.Minute && d%time.Minute == 0:
		return pluralize(int(d/time.Minute), "minute")
	default:
		return pluralize(int(d/time.Second), "second")
	}
}

func pluralize(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
