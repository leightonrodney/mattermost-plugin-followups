// Package reminder holds the domain model for follow-ups: the reminder record
// itself, the deterministic parser for `/remind` arguments, and the recurrence
// engine that decides when a repeating reminder fires next.
//
// Nothing in this package talks to Mattermost or to storage, which keeps the
// scheduling rules testable on their own.
package reminder

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// SchemaVersion is the version of the persisted reminder representation. It is
// written into every record so that a future release can migrate old data; see
// docs/ARCHITECTURE.md for the migration procedure.
const SchemaVersion = 1

// MaxMessageRunes bounds the user-supplied reminder text. The limit keeps the
// per-user storage blob small enough to stay comfortably within the plugin KV
// value size, and bounds what a single user can store.
const MaxMessageRunes = 500

// Status is the lifecycle state of a reminder.
type Status string

const (
	// StatusScheduled means the reminder is waiting for its due time.
	StatusScheduled Status = "scheduled"

	// StatusDelivering means a scheduler worker has claimed the reminder and is
	// attempting delivery. A claim older than ClaimTimeout is reclaimable.
	StatusDelivering Status = "delivering"

	// StatusDelivered means the reminder was delivered and will not fire again.
	StatusDelivered Status = "delivered"

	// StatusCancelled means the owner cancelled the reminder before it fired.
	StatusCancelled Status = "cancelled"

	// StatusFailed means delivery was attempted MaxAttempts times without success.
	StatusFailed Status = "failed"
)

// IsTerminal reports whether the status is an end state that the scheduler will
// never advance out of.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusDelivered, StatusCancelled, StatusFailed:
		return true
	case StatusScheduled, StatusDelivering:
		return false
	default:
		return true
	}
}

// Valid reports whether s is a status this version knows about.
func (s Status) Valid() bool {
	switch s {
	case StatusScheduled, StatusDelivering, StatusDelivered, StatusCancelled, StatusFailed:
		return true
	default:
		return false
	}
}

// ClaimTimeout is how long a StatusDelivering claim is honoured before another
// worker may reclaim the reminder. It must comfortably exceed the time a single
// delivery takes; a worker that dies mid-delivery is what this recovers from.
const ClaimTimeout = 5 * time.Minute

// MaxAttempts bounds delivery retries before a reminder is marked failed.
const MaxAttempts = 3

// Reminder is one follow-up owned by exactly one user.
//
// Times are stored as Unix milliseconds in UTC. Timezone holds the IANA name
// the reminder was created in, which is what recurrence is computed against so
// that "every weekday at 9am" stays at 9am across daylight-saving transitions.
type Reminder struct {
	// SchemaVersion is the version of this record's representation.
	SchemaVersion int `json:"schema_version"`

	// ID is a 26-character Mattermost-style ID, unique within the plugin.
	ID string `json:"id"`

	// UserID is the owner. Every read and write path checks it.
	UserID string `json:"user_id"`

	// Message is the user-supplied reminder text, already length-bounded.
	Message string `json:"message"`

	// Timezone is the IANA name used for recurrence arithmetic, e.g.
	// "Europe/London". Empty means UTC.
	Timezone string `json:"timezone"`

	// CreateAt is when the reminder was created, Unix milliseconds UTC.
	CreateAt int64 `json:"create_at"`

	// UpdateAt is when the record last changed, Unix milliseconds UTC.
	UpdateAt int64 `json:"update_at"`

	// DueAt is when this occurrence should fire, Unix milliseconds UTC.
	DueAt int64 `json:"due_at"`

	// Recurrence is nil for a one-off reminder.
	Recurrence *Recurrence `json:"recurrence,omitempty"`

	// ChannelID and PostID link the reminder back to the message it was created
	// from, when it was created from a message. Only the IDs are stored; the
	// message text is never copied into the plugin's storage.
	ChannelID string `json:"channel_id,omitempty"`
	PostID    string `json:"post_id,omitempty"`

	// TeamID is used to build a permalink for PostID. It may be empty for a
	// reminder created in a direct or group message, which have no team.
	TeamID string `json:"team_id,omitempty"`

	// Status is the lifecycle state.
	Status Status `json:"status"`

	// Attempts counts delivery attempts made for the current occurrence.
	Attempts int `json:"attempts,omitempty"`

	// NextAttemptAt is the earliest time a failed delivery may be retried,
	// Unix milliseconds UTC. Zero means "no backoff pending".
	NextAttemptAt int64 `json:"next_attempt_at,omitempty"`

	// ClaimedAt is when a worker claimed this reminder for delivery, Unix
	// milliseconds UTC. Zero when not claimed.
	ClaimedAt int64 `json:"claimed_at,omitempty"`

	// LastDeliveredAt is when delivery last succeeded, Unix milliseconds UTC.
	LastDeliveredAt int64 `json:"last_delivered_at,omitempty"`

	// Occurrences counts how many times this reminder has been delivered. It is
	// what distinguishes the fifth firing of a daily reminder from the first.
	Occurrences int `json:"occurrences,omitempty"`

	// SnoozedFrom records the DueAt a snooze moved away from, so the UI can
	// show that an occurrence was postponed rather than originally scheduled.
	SnoozedFrom int64 `json:"snoozed_from,omitempty"`
}

// Location resolves the reminder's timezone, falling back to UTC when the name
// is empty or unknown to the host's zone database.
func (r *Reminder) Location() *time.Location {
	if r.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// DueTime returns DueAt as a time.Time in the reminder's own timezone.
func (r *Reminder) DueTime() time.Time {
	return time.UnixMilli(r.DueAt).In(r.Location())
}

// IsRecurring reports whether the reminder repeats.
func (r *Reminder) IsRecurring() bool {
	return r.Recurrence != nil && r.Recurrence.Type != RecurrenceNone
}

// Clone returns a deep copy, so that a caller can mutate a candidate record
// without disturbing the one it read.
func (r *Reminder) Clone() *Reminder {
	if r == nil {
		return nil
	}
	clone := *r
	clone.Recurrence = r.Recurrence.Clone()
	return &clone
}

var (
	// ErrEmptyMessage is returned when a reminder has no text.
	ErrEmptyMessage = errors.New("a reminder needs something to say: add text after `to`")

	// ErrMessageTooLong is returned when the text exceeds MaxMessageRunes.
	ErrMessageTooLong = fmt.Errorf("reminder text is limited to %d characters", MaxMessageRunes)
)

// NormalizeMessage trims surrounding whitespace and collapses the interior runs
// of it, so that stored text is predictable. It returns an error for text that
// is empty or over the length limit.
func NormalizeMessage(s string) (string, error) {
	normalized := strings.Join(strings.Fields(s), " ")
	if normalized == "" {
		return "", ErrEmptyMessage
	}
	if utf8.RuneCountInString(normalized) > MaxMessageRunes {
		return "", ErrMessageTooLong
	}
	return normalized, nil
}

// Validate checks the invariants that every stored reminder must hold. It is
// called on every write path, including writes that originate from the plugin's
// own HTTP API, so that a malformed record can never reach storage.
func (r *Reminder) Validate() error {
	if r.ID == "" {
		return errors.New("reminder is missing an ID")
	}
	if r.UserID == "" {
		return errors.New("reminder is missing an owner")
	}
	if !r.Status.Valid() {
		return fmt.Errorf("unknown reminder status %q", r.Status)
	}
	if _, err := NormalizeMessage(r.Message); err != nil {
		return err
	}
	if r.DueAt <= 0 {
		return errors.New("reminder has no due time")
	}
	if r.Timezone != "" {
		if _, err := time.LoadLocation(r.Timezone); err != nil {
			return fmt.Errorf("unknown timezone %q", r.Timezone)
		}
	}
	if r.Recurrence != nil {
		if err := r.Recurrence.Validate(); err != nil {
			return err
		}
	}
	// A post reference without a channel cannot be turned into a permalink, and
	// would leave a dangling ID in storage.
	if r.PostID != "" && r.ChannelID == "" {
		return errors.New("reminder references a message but not its channel")
	}
	return nil
}
