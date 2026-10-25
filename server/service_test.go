package main

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
	"github.com/leightonrodney/mattermost-plugin-followups/server/store"
)

// fakeUsers stands in for the Mattermost user API.
type fakeUsers struct {
	timezone string
}

func (f fakeUsers) Get(userID string) (*model.User, error) {
	user := &model.User{Id: userID}
	if f.timezone != "" {
		user.Timezone = map[string]string{"useAutomaticTimezone": "false", "manualTimezone": f.timezone}
	}
	return user, nil
}

// testService wires a service to the SDK's in-memory key-value store, so the
// compare-and-set behaviour under test is the real thing.
type testService struct {
	*service
	store *store.Store
	clock *fakeClock
	conf  *configuration
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestService(t *testing.T, tweak ...func(*configuration)) *testService {
	t.Helper()

	conf := defaultConfiguration()
	for _, apply := range tweak {
		apply(conf)
	}

	st := store.New(&pluginapi.MemoryStore{})
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}

	svc := newService(st, fakeUsers{timezone: "America/New_York"}, func() *configuration { return conf })
	svc.now = clock.Now

	return &testService{service: svc, store: st, clock: clock, conf: conf}
}

func (ts *testService) mustCreate(t *testing.T, userID string, in time.Duration, message string) *reminder.Reminder {
	t.Helper()
	rec, err := ts.Create(userID, createInput{
		Message:  message,
		DueAt:    ts.clock.Now().Add(in),
		Timezone: "UTC",
	})
	require.NoError(t, err)
	return rec
}

// --- Creation --------------------------------------------------------------

func TestCreateStoresAndIndexes(t *testing.T) {
	ts := newTestService(t)

	rec := ts.mustCreate(t, "user1", time.Hour, "check the build")

	assert.Equal(t, "user1", rec.UserID)
	assert.Equal(t, reminder.StatusScheduled, rec.Status)
	assert.Equal(t, reminder.SchemaVersion, rec.SchemaVersion)

	stored, err := ts.Get("user1", rec.ID)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, stored.ID)

	entries, err := ts.store.DueIn(store.BucketHour(rec.DueTime()))
	require.NoError(t, err)
	assert.Contains(t, entries, store.DueEntry{UserID: "user1", ReminderID: rec.ID},
		"a reminder that is not indexed would never fire")
}

func TestCreateRejectsTooSoonAndTooFar(t *testing.T) {
	ts := newTestService(t)
	now := ts.clock.Now()

	_, err := ts.Create("user1", createInput{Message: "x", DueAt: now.Add(5 * time.Second)})
	require.Error(t, err)
	assert.True(t, IsUserFacing(err))
	assert.Contains(t, err.Error(), "at least")

	_, err = ts.Create("user1", createInput{Message: "x", DueAt: now.AddDate(10, 0, 0)})
	require.Error(t, err)
	assert.True(t, IsUserFacing(err))
	assert.Contains(t, err.Error(), "ahead")
}

func TestCreateRejectsBadText(t *testing.T) {
	ts := newTestService(t)
	now := ts.clock.Now()

	_, err := ts.Create("user1", createInput{Message: "   ", DueAt: now.Add(time.Hour)})
	assert.ErrorIs(t, err, reminder.ErrEmptyMessage)

	long := make([]rune, reminder.MaxMessageRunes+1)
	for i := range long {
		long[i] = 'x'
	}
	_, err = ts.Create("user1", createInput{Message: string(long), DueAt: now.Add(time.Hour)})
	assert.ErrorIs(t, err, reminder.ErrMessageTooLong)
}

func TestCreateEnforcesTheReminderQuota(t *testing.T) {
	ts := newTestService(t, func(c *configuration) { c.MaxRemindersPerUser = 3 })

	for i := range 3 {
		ts.mustCreate(t, "user1", time.Hour, fmt.Sprintf("reminder %d", i))
	}

	_, err := ts.Create("user1", createInput{Message: "one too many", DueAt: ts.clock.Now().Add(time.Hour)})
	require.Error(t, err)
	assert.True(t, IsUserFacing(err))
	assert.Contains(t, err.Error(), "limit")

	// Another user is unaffected by the first one's quota.
	ts.mustCreate(t, "user2", time.Hour, "mine")
}

func TestCreateQuotaIsRaceFree(t *testing.T) {
	const limit = 5
	ts := newTestService(t, func(c *configuration) { c.MaxRemindersPerUser = limit })

	// Twenty simultaneous creations against a limit of five. The check runs
	// inside the compare-and-set block, so exactly five may succeed.
	const attempts = 20
	var wg sync.WaitGroup
	results := make([]error, attempts)

	for i := range attempts {
		wg.Go(func() {
			_, results[i] = ts.Create("user1", createInput{
				Message: fmt.Sprintf("reminder %d", i),
				DueAt:   ts.clock.Now().Add(time.Hour),
			})
		})
	}
	wg.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		}
	}
	assert.Equal(t, limit, succeeded, "the quota must hold exactly, not approximately")

	list, err := ts.List("user1")
	require.NoError(t, err)
	assert.Len(t, list, limit)
}

func TestCreateEnforcesTheRecurringQuota(t *testing.T) {
	ts := newTestService(t, func(c *configuration) { c.MaxRecurringRemindersPerUser = 1 })
	daily := &reminder.Recurrence{Type: reminder.RecurrenceDaily, Interval: 1, Hour: 9}

	_, err := ts.Create("user1", createInput{Message: "first", DueAt: ts.clock.Now().Add(time.Hour), Recurrence: daily})
	require.NoError(t, err)

	_, err = ts.Create("user1", createInput{Message: "second", DueAt: ts.clock.Now().Add(time.Hour), Recurrence: daily})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repeating")

	// A one-off still fits under the overall limit.
	ts.mustCreate(t, "user1", time.Hour, "one-off")
}

func TestCreateRejectsTooFrequentARepeat(t *testing.T) {
	ts := newTestService(t, func(c *configuration) { c.MinIntervalSeconds = 300 })

	_, err := ts.Create("user1", createInput{
		Message:    "too often",
		DueAt:      ts.clock.Now().Add(time.Hour),
		Recurrence: &reminder.Recurrence{Type: reminder.RecurrenceInterval, EverySeconds: 60},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apart")
}

// --- Ownership -------------------------------------------------------------

func TestOneUserCannotReachAnother(t *testing.T) {
	ts := newTestService(t)
	theirs := ts.mustCreate(t, "owner", time.Hour, "private business")

	t.Run("get", func(t *testing.T) {
		_, err := ts.Get("intruder", theirs.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
	t.Run("cancel", func(t *testing.T) {
		_, err := ts.Cancel("intruder", theirs.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
	t.Run("delete", func(t *testing.T) {
		assert.ErrorIs(t, ts.Delete("intruder", theirs.ID), store.ErrNotFound)
	})
	t.Run("snooze", func(t *testing.T) {
		_, err := ts.Snooze("intruder", theirs.ID, time.Hour)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
	t.Run("complete", func(t *testing.T) {
		_, err := ts.Complete("intruder", theirs.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
	t.Run("reschedule", func(t *testing.T) {
		_, err := ts.Reschedule("intruder", theirs.ID, ts.clock.Now().Add(2*time.Hour), nil)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
	t.Run("retext", func(t *testing.T) {
		_, err := ts.Retext("intruder", theirs.ID, "mine now")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("the owner's reminder is untouched", func(t *testing.T) {
		still, err := ts.Get("owner", theirs.ID)
		require.NoError(t, err)
		assert.Equal(t, reminder.StatusScheduled, still.Status)
		assert.Equal(t, "private business", still.Message)
	})

	t.Run("the intruder's own list stays empty", func(t *testing.T) {
		list, err := ts.List("intruder")
		require.NoError(t, err)
		assert.Empty(t, list)
	})
}

// --- Delivery protocol -----------------------------------------------------

func TestClaimOnlySucceedsOnce(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "fire me")

	ts.clock.Advance(2 * time.Hour)
	now := ts.clock.Now()

	first, err := ts.Claim("user1", rec.ID, now)
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusDelivering, first.Status)
	assert.Equal(t, 1, first.Attempts)

	_, err = ts.Claim("user1", rec.ID, now)
	assert.ErrorIs(t, err, errSkip, "a second worker must not get the same occurrence")
}

func TestClaimIsRaceFree(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "fire me")
	ts.clock.Advance(2 * time.Hour)
	now := ts.clock.Now()

	// Ten workers going for the same reminder at the same instant, which is the
	// situation a cluster lock expiring mid-run would produce.
	const workers = 10
	var wg sync.WaitGroup
	results := make([]error, workers)

	for i := range workers {
		wg.Go(func() {
			_, results[i] = ts.Claim("user1", rec.ID, now)
		})
	}
	wg.Wait()

	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, errSkip):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, won, "exactly one worker may claim an occurrence")
}

func TestClaimSkipsWhatIsNotDue(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "later")

	_, err := ts.Claim("user1", rec.ID, ts.clock.Now())
	assert.ErrorIs(t, err, errSkip)
}

func TestClaimSkipsTerminalReminders(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "cancel me")
	_, err := ts.Cancel("user1", rec.ID)
	require.NoError(t, err)

	ts.clock.Advance(2 * time.Hour)
	_, err = ts.Claim("user1", rec.ID, ts.clock.Now())
	assert.ErrorIs(t, err, errSkip, "a cancelled reminder must never be delivered")
}

func TestClaimReclaimsAStalledDelivery(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "fire me")
	ts.clock.Advance(2 * time.Hour)

	_, err := ts.Claim("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)

	// Still within the claim window: another worker must keep its hands off.
	ts.clock.Advance(reminder.ClaimTimeout / 2)
	_, err = ts.Claim("user1", rec.ID, ts.clock.Now())
	assert.ErrorIs(t, err, errSkip)

	// Past it: the worker that held the claim is presumed dead.
	ts.clock.Advance(reminder.ClaimTimeout)
	reclaimed, err := ts.Claim("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)
	assert.Equal(t, 2, reclaimed.Attempts)
}

func TestDeliveredFinishesAOneOff(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "once")
	ts.clock.Advance(2 * time.Hour)

	_, err := ts.Claim("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)

	done, err := ts.Delivered("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusDelivered, done.Status)
	assert.Equal(t, 1, done.Occurrences)
	assert.NotZero(t, done.LastDeliveredAt)

	_, err = ts.Claim("user1", rec.ID, ts.clock.Now())
	assert.ErrorIs(t, err, errSkip, "a delivered one-off must not fire again")
}

func TestDeliveredAdvancesARecurringSeries(t *testing.T) {
	ts := newTestService(t)

	firstDue := ts.clock.Now().Add(time.Hour)
	rec, err := ts.Create("user1", createInput{
		Message:    "stand up",
		DueAt:      firstDue,
		Timezone:   "UTC",
		Recurrence: &reminder.Recurrence{Type: reminder.RecurrenceInterval, EverySeconds: 3600},
	})
	require.NoError(t, err)

	ts.clock.Advance(90 * time.Minute)
	_, err = ts.Claim("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)

	advanced, err := ts.Delivered("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)

	assert.Equal(t, reminder.StatusScheduled, advanced.Status, "the series must keep going")
	assert.Equal(t, 1, advanced.Occurrences)
	assert.Greater(t, advanced.DueAt, ts.clock.Now().UnixMilli(), "the next occurrence must be in the future")

	entries, err := ts.store.DueIn(store.BucketHour(advanced.DueTime()))
	require.NoError(t, err)
	assert.Contains(t, entries, store.DueEntry{UserID: "user1", ReminderID: rec.ID},
		"the next occurrence must be indexed before the scheduler forgets about it")
}

func TestFailedRetriesThenGivesUp(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "flaky")
	ts.clock.Advance(2 * time.Hour)

	for attempt := 1; attempt < reminder.MaxAttempts; attempt++ {
		claimed, err := ts.Claim("user1", rec.ID, ts.clock.Now())
		require.NoError(t, err, "attempt %d", attempt)
		assert.Equal(t, attempt, claimed.Attempts)

		failed, err := ts.Failed("user1", rec.ID, ts.clock.Now())
		require.NoError(t, err)
		assert.Equal(t, reminder.StatusScheduled, failed.Status, "it should still be retryable")
		assert.Greater(t, failed.NextAttemptAt, ts.clock.Now().UnixMilli(), "the retry must be backed off")

		// The backoff is honoured: claiming before it elapses is refused.
		_, err = ts.Claim("user1", rec.ID, ts.clock.Now())
		assert.ErrorIs(t, err, errSkip)

		ts.clock.Advance(20 * time.Minute)
	}

	claimed, err := ts.Claim("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)
	assert.Equal(t, reminder.MaxAttempts, claimed.Attempts)

	exhausted, err := ts.Failed("user1", rec.ID, ts.clock.Now())
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusFailed, exhausted.Status, "it must stop after the attempt limit")

	ts.clock.Advance(time.Hour)
	_, err = ts.Claim("user1", rec.ID, ts.clock.Now())
	assert.ErrorIs(t, err, errSkip)
}

// --- Management ------------------------------------------------------------

func TestSnoozeMovesAOneOff(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "later please")

	moved, err := ts.Snooze("user1", rec.ID, 2*time.Hour)
	require.NoError(t, err)

	assert.Equal(t, rec.ID, moved.ID, "a one-off is moved, not copied")
	assert.Equal(t, rec.DueAt, moved.SnoozedFrom)
	assert.Equal(t, ts.clock.Now().Add(2*time.Hour).UnixMilli(), moved.DueAt)

	entries, err := ts.store.DueIn(store.BucketHour(moved.DueTime()))
	require.NoError(t, err)
	assert.Contains(t, entries, store.DueEntry{UserID: "user1", ReminderID: rec.ID})
}

func TestSnoozeOfARecurringReminderLeavesTheSeriesAlone(t *testing.T) {
	ts := newTestService(t)
	original, err := ts.Create("user1", createInput{
		Message:    "stand up",
		DueAt:      ts.clock.Now().Add(time.Hour),
		Timezone:   "UTC",
		Recurrence: &reminder.Recurrence{Type: reminder.RecurrenceDaily, Interval: 1, Hour: 9},
	})
	require.NoError(t, err)

	copied, err := ts.Snooze("user1", original.ID, 30*time.Minute)
	require.NoError(t, err)

	assert.NotEqual(t, original.ID, copied.ID, "the series must not be dragged along by a snooze")
	assert.Nil(t, copied.Recurrence, "the snoozed copy is a one-off")
	assert.Equal(t, original.Message, copied.Message)

	unchanged, err := ts.Get("user1", original.ID)
	require.NoError(t, err)
	assert.Equal(t, original.DueAt, unchanged.DueAt)
	assert.True(t, unchanged.IsRecurring())
}

func TestCancelAndDelete(t *testing.T) {
	ts := newTestService(t)

	cancelled := ts.mustCreate(t, "user1", time.Hour, "stop me")
	_, err := ts.Cancel("user1", cancelled.ID)
	require.NoError(t, err)

	after, err := ts.Get("user1", cancelled.ID)
	require.NoError(t, err)
	assert.Equal(t, reminder.StatusCancelled, after.Status, "cancelling keeps it in the history")

	_, err = ts.Cancel("user1", cancelled.ID)
	assert.Error(t, err, "cancelling twice is refused")

	require.NoError(t, ts.Delete("user1", cancelled.ID))
	_, err = ts.Get("user1", cancelled.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestRescheduleAndRetext(t *testing.T) {
	ts := newTestService(t)
	rec := ts.mustCreate(t, "user1", time.Hour, "original")

	weekly := &reminder.Recurrence{Type: reminder.RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Monday}, Hour: 9}
	newDue := ts.clock.Now().Add(48 * time.Hour)

	moved, err := ts.Reschedule("user1", rec.ID, newDue, weekly)
	require.NoError(t, err)
	assert.Equal(t, newDue.UnixMilli(), moved.DueAt)
	assert.True(t, moved.IsRecurring())

	retexted, err := ts.Retext("user1", rec.ID, "  updated   text  ")
	require.NoError(t, err)
	assert.Equal(t, "updated text", retexted.Message)

	_, err = ts.Retext("user1", rec.ID, "   ")
	assert.ErrorIs(t, err, reminder.ErrEmptyMessage)
}

func TestPurgeAppliesRetention(t *testing.T) {
	ts := newTestService(t, func(c *configuration) { c.CompletedRetentionDays = 7 })

	old := ts.mustCreate(t, "user1", time.Hour, "ancient")
	_, err := ts.Cancel("user1", old.ID)
	require.NoError(t, err)

	kept := ts.mustCreate(t, "user1", time.Hour, "still pending")

	// Nothing is old enough yet.
	removed, err := ts.Purge("user1", ts.clock.Now())
	require.NoError(t, err)
	assert.Zero(t, removed)

	ts.clock.Advance(8 * 24 * time.Hour)
	removed, err = ts.Purge("user1", ts.clock.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	_, err = ts.Get("user1", old.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	_, err = ts.Get("user1", kept.ID)
	assert.NoError(t, err, "a pending reminder is never purged, however old")
}

func TestListIsOrdered(t *testing.T) {
	ts := newTestService(t)

	late := ts.mustCreate(t, "user1", 3*time.Hour, "late")
	early := ts.mustCreate(t, "user1", time.Hour, "early")
	done := ts.mustCreate(t, "user1", 2*time.Hour, "finished")
	_, err := ts.Cancel("user1", done.ID)
	require.NoError(t, err)

	list, err := ts.List("user1")
	require.NoError(t, err)
	require.Len(t, list, 3)

	assert.Equal(t, early.ID, list[0].ID, "pending reminders come first, soonest first")
	assert.Equal(t, late.ID, list[1].ID)
	assert.Equal(t, done.ID, list[2].ID, "finished reminders come last")
}

func TestTimezoneFallsBackToUTC(t *testing.T) {
	st := store.New(&pluginapi.MemoryStore{})
	conf := defaultConfiguration()

	t.Run("no timezone set", func(t *testing.T) {
		svc := newService(st, fakeUsers{}, func() *configuration { return conf })
		assert.Equal(t, time.UTC, svc.Timezone("user1"))
	})

	t.Run("unknown timezone", func(t *testing.T) {
		svc := newService(st, fakeUsers{timezone: "Mars/Olympus_Mons"}, func() *configuration { return conf })
		assert.Equal(t, time.UTC, svc.Timezone("user1"))
	})

	t.Run("known timezone", func(t *testing.T) {
		svc := newService(st, fakeUsers{timezone: "Europe/Berlin"}, func() *configuration { return conf })
		assert.Equal(t, "Europe/Berlin", svc.Timezone("user1").String())
	})
}
