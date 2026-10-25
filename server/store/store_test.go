package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
)

// newTestStore backs the store with the SDK's in-memory key-value
// implementation, which honours the same compare-and-set semantics as the real
// one. That is the point: these tests exercise the actual concurrency rules
// rather than a hand-written stub that always agrees with them.
func newTestStore() *Store {
	return New(&pluginapi.MemoryStore{})
}

func sampleReminder(userID, id string, dueAt time.Time) *reminder.Reminder {
	return &reminder.Reminder{
		SchemaVersion: reminder.SchemaVersion,
		ID:            id,
		UserID:        userID,
		Message:       "check the build",
		DueAt:         dueAt.UnixMilli(),
		CreateAt:      dueAt.UnixMilli(),
		UpdateAt:      dueAt.UnixMilli(),
		Status:        reminder.StatusScheduled,
	}
}

func TestGetUserReturnsAnEmptyRecord(t *testing.T) {
	st := newTestStore()

	record, err := st.GetUser("user1")
	require.NoError(t, err)
	assert.NotNil(t, record.Reminders)
	assert.Empty(t, record.Reminders)
	assert.Equal(t, reminder.SchemaVersion, record.SchemaVersion)
}

func TestGetUserRejectsAnEmptyUserID(t *testing.T) {
	_, err := newTestStore().GetUser("")
	assert.Error(t, err)
}

func TestUpdateUserRoundTrips(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	require.NoError(t, st.UpdateUser("user1", func(record *UserRecord) error {
		record.Reminders["a"] = sampleReminder("user1", "a", due)
		return nil
	}))

	record, err := st.GetUser("user1")
	require.NoError(t, err)
	require.Len(t, record.Reminders, 1)
	assert.Equal(t, "check the build", record.Reminders["a"].Message)
	assert.Equal(t, reminder.SchemaVersion, record.SchemaVersion)
}

func TestUpdateUserSurfacesTheMutatorError(t *testing.T) {
	st := newTestStore()
	sentinel := fmt.Errorf("quota exceeded")

	err := st.UpdateUser("user1", func(*UserRecord) error { return sentinel })
	assert.ErrorIs(t, err, sentinel, "the caller must see its own error, not a wrapped retry failure")

	record, err := st.GetUser("user1")
	require.NoError(t, err)
	assert.Empty(t, record.Reminders, "a rejected change must not be written")
}

func TestUpdateUserKeepsUsersApart(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	require.NoError(t, st.UpdateUser("user1", func(record *UserRecord) error {
		record.Reminders["a"] = sampleReminder("user1", "a", due)
		return nil
	}))

	other, err := st.GetUser("user2")
	require.NoError(t, err)
	assert.Empty(t, other.Reminders, "one user's reminders must not appear in another's record")
}

func TestUpdateUserIsAtomicUnderConcurrency(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	// Twenty writers racing on the same record. Without compare-and-set, the
	// last writer would clobber the other nineteen.
	const writers = 20
	var wg sync.WaitGroup
	errs := make([]error, writers)

	for i := range writers {
		wg.Go(func() {
			id := fmt.Sprintf("reminder-%02d", i)
			errs[i] = st.UpdateUser("user1", func(record *UserRecord) error {
				record.Reminders[id] = sampleReminder("user1", id, due)
				return nil
			})
		})
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}

	record, err := st.GetUser("user1")
	require.NoError(t, err)
	assert.Len(t, record.Reminders, writers, "every concurrent write must survive")
}

func TestUpdateUserRejectsAnOversizedRecord(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	// Deliberately past the ceiling, which the per-user quota normally keeps
	// callers far away from.
	huge := make([]byte, 0, maxUserRecordBytes)
	for range maxUserRecordBytes {
		huge = append(huge, 'x')
	}

	err := st.UpdateUser("user1", func(record *UserRecord) error {
		rec := sampleReminder("user1", "a", due)
		rec.Message = string(huge)
		record.Reminders["a"] = rec
		return nil
	})
	assert.ErrorIs(t, err, ErrRecordTooLarge)
}

func TestDeleteUserRemovesEverything(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	require.NoError(t, st.UpdateUser("user1", func(record *UserRecord) error {
		record.Reminders["a"] = sampleReminder("user1", "a", due)
		return nil
	}))
	require.NoError(t, st.DeleteUser("user1"))

	record, err := st.GetUser("user1")
	require.NoError(t, err)
	assert.Empty(t, record.Reminders)
}

// --- Due index -------------------------------------------------------------

func TestIndexAndRead(t *testing.T) {
	st := newTestStore()
	due := time.Now().UTC().Add(90 * time.Minute)

	require.NoError(t, st.Index("user1", "a", due))

	entries, err := st.DueIn(BucketHour(due))
	require.NoError(t, err)
	assert.Equal(t, []DueEntry{{UserID: "user1", ReminderID: "a"}}, entries)
}

func TestIndexIsIdempotent(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	require.NoError(t, st.Index("user1", "a", due))
	require.NoError(t, st.Index("user1", "a", due))
	require.NoError(t, st.Index("user1", "a", due))

	entries, err := st.DueIn(BucketHour(due))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "re-indexing must not create a second chance to deliver")
}

func TestIndexSeparatesHours(t *testing.T) {
	st := newTestStore()
	// Anchored on a future hour boundary so that the two entries are certain to
	// land in different buckets.
	base := BucketHour(time.Now().UTC().Add(2 * time.Hour))

	require.NoError(t, st.Index("user1", "a", base.Add(10*time.Minute)))
	require.NoError(t, st.Index("user1", "b", base.Add(70*time.Minute)))

	first, err := st.DueIn(BucketHour(base))
	require.NoError(t, err)
	assert.Len(t, first, 1)
	assert.Equal(t, "a", first[0].ReminderID)

	second, err := st.DueIn(BucketHour(base.Add(time.Hour)))
	require.NoError(t, err)
	assert.Len(t, second, 1)
	assert.Equal(t, "b", second[0].ReminderID)
}

func TestIndexGathersEveryShard(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	// Enough distinct users that more than one shard is certain to be used.
	const users = 50
	for i := range users {
		require.NoError(t, st.Index(fmt.Sprintf("user%02d", i), "a", due))
	}

	entries, err := st.DueIn(BucketHour(due))
	require.NoError(t, err)
	assert.Len(t, entries, users, "DueIn must read every shard, not just one")
}

func TestIndexIsSafeUnderConcurrency(t *testing.T) {
	st := newTestStore()
	due := time.Now().Add(time.Hour)

	// All of these land in the same shard, because the shard is chosen from the
	// user ID. That is the contended case the retry loop exists for.
	const writers = 25
	var wg sync.WaitGroup
	errs := make([]error, writers)

	for i := range writers {
		wg.Go(func() {
			errs[i] = st.Index("user1", fmt.Sprintf("reminder-%02d", i), due)
		})
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}

	entries, err := st.DueIn(BucketHour(due))
	require.NoError(t, err)
	assert.Len(t, entries, writers, "no index entry may be lost to a lost update")
}

func TestIndexIgnoresAnExpiredBucket(t *testing.T) {
	st := newTestStore()

	// Past the retention window, so nothing would ever read the bucket.
	stale := time.Now().Add(-BucketRetention - 48*time.Hour)
	require.NoError(t, st.Index("user1", "a", stale))

	entries, err := st.DueIn(BucketHour(stale))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDueInIsEmptyForAnUnusedHour(t *testing.T) {
	entries, err := newTestStore().DueIn(BucketHour(time.Now()))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// --- Cursor ----------------------------------------------------------------

func TestCursorStartsUnset(t *testing.T) {
	cursor, err := newTestStore().Cursor()
	require.NoError(t, err)
	assert.True(t, cursor.IsZero())
}

func TestCursorRoundTripsOnTheHour(t *testing.T) {
	st := newTestStore()
	moment := time.Date(2026, time.June, 1, 9, 37, 12, 0, time.UTC)

	require.NoError(t, st.SetCursor(moment))

	cursor, err := st.Cursor()
	require.NoError(t, err)
	assert.True(t, cursor.Equal(time.Date(2026, time.June, 1, 9, 0, 0, 0, time.UTC)),
		"the cursor is stored truncated to its hour, got %s", cursor)
}

func TestCursorSurvivesACorruptValue(t *testing.T) {
	kv := &pluginapi.MemoryStore{}
	st := New(kv)

	// A cursor the plugin cannot parse must not stop the scheduler; it is
	// treated as absent and re-established on the next run.
	_, err := kv.Set(cursorKey, cursor{LastCompletedHour: "not a timestamp"})
	require.NoError(t, err)

	got, err := st.Cursor()
	require.NoError(t, err)
	assert.True(t, got.IsZero())
}
