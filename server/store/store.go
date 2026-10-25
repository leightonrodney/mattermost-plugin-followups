// Package store is the persistence layer for follow-ups. It owns every key the
// plugin writes and is the only place that knows the shape of the key-value
// records.
//
// Two things shape the design. First, the Mattermost plugin key-value store has
// no transactions and no relational queries, so anything that must change
// together has to live under a single key and be written with a compare-and-set
// loop. Second, the scheduler must find due reminders without reading every
// user's data on every tick.
//
// That gives two kinds of record:
//
//   - One aggregate per user, holding all of that user's reminders. A user's
//     reminders only ever change as a unit, so a single compare-and-set makes
//     quota checks, edits and delivery claims race-free without a lock.
//
//   - A due index bucketed by hour and sharded across a fixed number of keys.
//     The scheduler reads the buckets between its cursor and now, which is a
//     handful of keys per tick regardless of how many users the server has.
package store

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
)

// keyPrefix namespaces every key this plugin writes, and carries the schema
// version so that a future format can be introduced alongside the old one.
const keyPrefix = "v1:"

const (
	// userKeyPrefix is followed by a user ID.
	userKeyPrefix = keyPrefix + "u:"

	// dueKeyPrefix is followed by an hour stamp and a shard number.
	dueKeyPrefix = keyPrefix + "d:"

	// cursorKey records how far the scheduler has swept.
	cursorKey = keyPrefix + "cursor"
)

const (
	// dueShards is how many keys each hour bucket is split across. Sharding
	// spreads the compare-and-set contention of many people scheduling
	// reminders for the same popular hour, at the cost of a few more reads per
	// scheduler tick.
	dueShards = 8

	// dueBucketTTL is how long an hour bucket survives. It bounds how long a
	// disabled or offline plugin can be away and still catch up from its
	// cursor; see docs/ARCHITECTURE.md for what happens beyond it.
	dueBucketTTL = 90 * 24 * time.Hour

	// maxUserRecordBytes is a hard ceiling on one user's encoded record. The
	// per-user quota and the message length limit normally keep records far
	// below this; the ceiling is the backstop that keeps a single account from
	// writing an unbounded row.
	maxUserRecordBytes = 1 << 20
)

// KV is the subset of the plugin key-value API this package needs. Both
// pluginapi.KVService and pluginapi.MemoryStore satisfy it, which is what lets
// the tests exercise the real compare-and-set semantics without a server.
type KV interface {
	Get(key string, out any) error
	Set(key string, value any, options ...pluginapi.KVSetOption) (bool, error)
	SetAtomicWithRetries(key string, valueFunc func(oldValue []byte) (any, error)) error
	Delete(key string) error
}

// Store reads and writes the plugin's key-value records.
type Store struct {
	kv KV
}

// New returns a Store backed by the given key-value client.
func New(kv KV) *Store {
	return &Store{kv: kv}
}

// UserRecord is everything the plugin stores for one user. It is written as a
// single key so that every change to a user's reminders is atomic.
type UserRecord struct {
	// SchemaVersion is the version of this record's layout.
	SchemaVersion int `json:"schema_version"`

	// Reminders is keyed by reminder ID.
	Reminders map[string]*reminder.Reminder `json:"reminders"`
}

// ErrRecordTooLarge is returned when a write would exceed maxUserRecordBytes.
var ErrRecordTooLarge = errors.New("you have too much reminder data stored; delete some reminders and try again")

// ErrNotFound is returned when a reminder ID does not belong to the user, or
// does not exist at all. The two cases are deliberately indistinguishable so
// that the API cannot be used to probe for other people's reminder IDs.
var ErrNotFound = errors.New("reminder not found")

func userKey(userID string) string {
	return userKeyPrefix + userID
}

// GetUser returns the user's record, or an empty one if they have none yet.
func (s *Store) GetUser(userID string) (*UserRecord, error) {
	if userID == "" {
		return nil, errors.New("missing user ID")
	}

	var record UserRecord
	if err := s.kv.Get(userKey(userID), &record); err != nil {
		return nil, errors.Wrap(err, "failed to read reminders")
	}
	if record.Reminders == nil {
		record.Reminders = map[string]*reminder.Reminder{}
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = reminder.SchemaVersion
	}
	return &record, nil
}

// UpdateUser applies mutate to the user's record under compare-and-set, so that
// two concurrent callers cannot lose each other's changes. mutate may be called
// more than once and must be free of side effects outside the record it is
// given.
//
// Returning an error from mutate abandons the write and surfaces that error
// unwrapped, so quota and ownership checks belong inside it where they are
// evaluated against the record that is actually about to be written.
func (s *Store) UpdateUser(userID string, mutate func(*UserRecord) error) error {
	if userID == "" {
		return errors.New("missing user ID")
	}
	key := userKey(userID)

	return retryCAS("reminders for a user", func() (bool, error) {
		var current []byte
		if err := s.kv.Get(key, &current); err != nil {
			return false, errors.Wrap(err, "failed to read reminders")
		}

		record := &UserRecord{}
		if len(current) > 0 {
			if err := json.Unmarshal(current, record); err != nil {
				return false, errors.Wrap(err, "stored reminders are corrupt")
			}
		}
		if record.Reminders == nil {
			record.Reminders = map[string]*reminder.Reminder{}
		}
		record.SchemaVersion = reminder.SchemaVersion

		if err := mutate(record); err != nil {
			return false, err
		}

		encoded, err := json.Marshal(record)
		if err != nil {
			return false, errors.Wrap(err, "failed to encode reminders")
		}
		if len(encoded) > maxUserRecordBytes {
			return false, ErrRecordTooLarge
		}

		written, err := s.kv.Set(key, encoded, pluginapi.SetAtomic(current))
		if err != nil {
			return false, errors.Wrap(err, "failed to save reminders")
		}
		return written, nil
	})
}

// DeleteUser removes everything stored for a user. It is used when a user is
// deleted from the server, so that the plugin does not retain their data.
func (s *Store) DeleteUser(userID string) error {
	if userID == "" {
		return errors.New("missing user ID")
	}
	return errors.Wrap(s.kv.Delete(userKey(userID)), "failed to delete reminders")
}

// --- Due index -------------------------------------------------------------

// DueEntry points the scheduler at one reminder. The field names are short
// because an index bucket may hold many thousands of them.
type DueEntry struct {
	UserID     string `json:"u"`
	ReminderID string `json:"r"`
}

// BucketHour truncates an instant to the UTC hour its index bucket covers.
func BucketHour(t time.Time) time.Time {
	return t.UTC().Truncate(time.Hour)
}

func dueKey(hour time.Time, shard int) string {
	return fmt.Sprintf("%s%s:%d", dueKeyPrefix, hour.UTC().Format("2006010215"), shard)
}

// shardFor picks a bucket shard from the user ID, so that one user's entries
// always land in the same shard and the scheduler reads each shard once.
func shardFor(userID string) int {
	h := fnv.New32a()
	// Hash writes never fail.
	_, _ = h.Write([]byte(userID))
	return int(h.Sum32() % dueShards)
}

// Index records that a reminder is due at dueAt, so the scheduler will find it.
//
// Indexing is idempotent: adding an entry that is already present is a no-op,
// which means a retried write cannot cause a reminder to be delivered twice.
func (s *Store) Index(userID, reminderID string, dueAt time.Time) error {
	hour := BucketHour(dueAt)
	key := dueKey(hour, shardFor(userID))
	entry := DueEntry{UserID: userID, ReminderID: reminderID}

	// The bucket expires on its own once it is far enough in the past, so the
	// scheduler never has to clean up after itself. The expiry is recomputed on
	// every write because setting a value replaces its previous expiry.
	ttl := time.Until(hour.Add(time.Hour + dueBucketTTL))
	if ttl <= 0 {
		// The bucket is already past its retention window; nothing would read
		// it even if it were written.
		return nil
	}

	return retryCAS("the schedule index", func() (bool, error) {
		var current []byte
		if err := s.kv.Get(key, &current); err != nil {
			return false, errors.Wrap(err, "failed to read the schedule index")
		}

		entries, err := decodeEntries(current)
		if err != nil {
			return false, err
		}
		if slices.Contains(entries, entry) {
			// Already indexed. Reporting success without a write is what makes
			// re-indexing free, and keeps a retried create from giving the
			// scheduler two chances to deliver the same reminder.
			return true, nil
		}
		entries = append(entries, entry)

		encoded, err := json.Marshal(entries)
		if err != nil {
			return false, errors.Wrap(err, "failed to encode the schedule index")
		}

		// The expiry has to be set on every write, because setting a value
		// replaces the previous one's expiry.
		written, err := s.kv.Set(key, encoded, pluginapi.SetAtomic(current), pluginapi.SetExpiry(ttl))
		if err != nil {
			return false, errors.Wrap(err, "failed to write the schedule index")
		}
		return written, nil
	})
}

const (
	// casBudget is how long a contended compare-and-set keeps trying before it
	// gives up. A budget rather than a fixed attempt count is what makes the
	// loop correct under contention: each round lets exactly one writer through,
	// so the attempts needed grow with the number of competing writers, and a
	// fixed count would start failing writes as soon as a key became popular.
	casBudget = 5 * time.Second

	// casMinBackoff and casMaxBackoff bound the wait between attempts.
	casMinBackoff = 2 * time.Millisecond
	casMaxBackoff = 50 * time.Millisecond
)

// retryCAS runs a compare-and-set attempt until it succeeds, fails outright, or
// runs out of time.
//
// attempt returns (true, nil) when the write landed, (false, nil) when another
// writer won the race and it should be tried again, and an error when the
// operation should be abandoned. The backoff is jittered so that competing
// plugin instances do not line up and collide on every round.
func retryCAS(what string, attempt func() (bool, error)) error {
	deadline := time.Now().Add(casBudget)
	backoff := casMinBackoff

	for tries := 1; ; tries++ {
		done, err := attempt()
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		if time.Now().After(deadline) {
			return errors.Errorf("gave up updating %s after %d attempts: too many concurrent changes", what, tries)
		}

		//nolint:gosec // Jitter only needs to be uneven, not unpredictable.
		time.Sleep(backoff + time.Duration(rand.Int64N(int64(backoff))))
		backoff = min(backoff*2, casMaxBackoff)
	}
}

// DueIn returns every entry indexed for the given hour, across all shards.
func (s *Store) DueIn(hour time.Time) ([]DueEntry, error) {
	var all []DueEntry
	for shard := range dueShards {
		var raw []byte
		if err := s.kv.Get(dueKey(hour, shard), &raw); err != nil {
			return nil, errors.Wrapf(err, "failed to read the schedule index for %s", hour.Format(time.RFC3339))
		}
		entries, err := decodeEntries(raw)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	return all, nil
}

func decodeEntries(raw []byte) ([]DueEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var entries []DueEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errors.Wrap(err, "the schedule index is corrupt")
	}
	return entries, nil
}

// --- Scheduler cursor ------------------------------------------------------

// cursor records the last hour bucket the scheduler finished with. It is
// persisted so that a restarted plugin resumes where it left off rather than
// losing the reminders that came due while it was down.
type cursor struct {
	// LastCompletedHour is an RFC3339 UTC hour. Buckets up to and including it
	// have been fully processed.
	LastCompletedHour string `json:"last_completed_hour"`
}

// Cursor returns the last fully processed hour bucket. The zero time means the
// scheduler has never run.
func (s *Store) Cursor() (time.Time, error) {
	var c cursor
	if err := s.kv.Get(cursorKey, &c); err != nil {
		return time.Time{}, errors.Wrap(err, "failed to read the scheduler cursor")
	}
	if c.LastCompletedHour == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, c.LastCompletedHour)
	if err != nil {
		// A cursor we cannot read is treated as absent rather than fatal; the
		// scheduler will re-establish it on this tick.
		return time.Time{}, nil
	}
	return t.UTC(), nil
}

// SetCursor records the last fully processed hour bucket.
//
// The scheduler holds a cluster-wide lock while it runs, so this does not need
// compare-and-set; the lock is what serialises cursor advancement.
func (s *Store) SetCursor(hour time.Time) error {
	_, err := s.kv.Set(cursorKey, cursor{LastCompletedHour: BucketHour(hour).Format(time.RFC3339)})
	return errors.Wrap(err, "failed to save the scheduler cursor")
}

// BucketRetention is how far back the scheduler can usefully catch up, exported
// so the scheduler can decide when a gap is too large to recover from.
const BucketRetention = dueBucketTTL
