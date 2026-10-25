package reminder

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every time expression promised in README.md and in the /remind help text.
//
// Documentation that is merely written down drifts away from the code. These
// are the literal strings from the docs, so a form that stops working, or that
// is documented but never implemented, fails the build rather than disappointing
// somebody later.
var documentedTimes = []string{
	// Relative
	"in 30 minutes", "in 2 hours", "in 3 days", "in 2 weeks", "in an hour",
	"in half an hour", "in 1 month",

	// Short units
	"in 30m", "in 2h", "in 3d", "in 1w",

	// Named days
	"today at 6pm", "tonight", "tomorrow", "tomorrow at 9am",

	// Weekdays
	"Monday", "Friday at 10am", "next Tuesday at 10:00",

	// Ahead
	"next week", "next month",

	// Dates
	"2026-11-03 at 14:30", "Nov 3", "Nov 3rd at 2pm", "3 November", "Jan 5 2027",

	// Clock only
	"at 9am", "at 18:00", "at noon", "at midnight",
}

// Every repeating form promised in the documentation.
var documentedRepeats = []string{
	"every day at 9am",
	"every weekday at 9:15",
	"every Monday and Friday at 10am",
	"every other Monday at 10am",
	"every 3 days at 8am",
	"every month on the 15th at 9am",
	"every 2 hours",
	"every weekend at 11am",
	"every mon,wed,fri at 10am",
}

// The complete commands written out in the README and the help text.
var documentedCommands = []string{
	"me in 30 minutes to check the build",
	"me tomorrow at 9am to review the deployment",
	"me every Monday at 10am to check the sprint board",
	"me to call the vendor back on Friday at 2pm",
	"me every day at 9am to stand up",
	"me every weekday at 9:15 to post the update",
	"me every Monday and Friday at 10am to sync",
	"me every other Monday at 10am to sync",
	"me every 3 days at 8am to water the plants",
	"me every month on the 15th at 9am to file the report",
	"me every 2 hours to drink water",
	"me every weekend at 11am to call home",
}

func TestEveryDocumentedTimeParses(t *testing.T) {
	now := reference(t)
	loc := newYork(t)

	for _, phrase := range documentedTimes {
		t.Run(phrase, func(t *testing.T) {
			parsed, err := Parse("me "+phrase+" to do the thing", now, loc)
			require.NoError(t, err)
			assert.Equal(t, "do the thing", parsed.Message)
			assert.True(t, parsed.DueAt.After(now), "%q resolved to %s, which is not in the future", phrase, parsed.DueAt)
			assert.Nil(t, parsed.Recurrence, "%q is not a repeating form", phrase)
		})
	}
}

func TestEveryDocumentedRepeatParses(t *testing.T) {
	now := reference(t)
	loc := newYork(t)

	for _, phrase := range documentedRepeats {
		t.Run(phrase, func(t *testing.T) {
			parsed, err := Parse("me "+phrase+" to do the thing", now, loc)
			require.NoError(t, err)
			assert.Equal(t, "do the thing", parsed.Message)

			require.NotNil(t, parsed.Recurrence, "%q should repeat", phrase)
			require.NoError(t, parsed.Recurrence.Validate())
			assert.NotEmpty(t, parsed.Recurrence.Describe(), "a repeating rule must be describable back to the user")
			assert.True(t, parsed.DueAt.After(now), "the first occurrence of %q is not in the future", phrase)

			// A rule the scheduler cannot advance would fire once and stop.
			next := parsed.Recurrence.Next(parsed.DueAt, parsed.DueAt, loc)
			assert.False(t, next.IsZero(), "%q cannot be advanced", phrase)
			assert.True(t, next.After(parsed.DueAt), "%q does not move forward", phrase)
		})
	}
}

func TestEveryDocumentedCommandParses(t *testing.T) {
	now := reference(t)
	loc := newYork(t)

	for _, command := range documentedCommands {
		t.Run(command, func(t *testing.T) {
			parsed, err := Parse(command, now, loc)
			require.NoError(t, err)
			assert.NotEmpty(t, parsed.Message)
			assert.True(t, parsed.DueAt.After(now))

			// The reminder the command describes must be storable as written.
			rec := &Reminder{
				SchemaVersion: SchemaVersion,
				ID:            "abcdefghijklmnopqrstuvwxyz",
				UserID:        "user",
				Message:       parsed.Message,
				Timezone:      loc.String(),
				CreateAt:      now.UnixMilli(),
				UpdateAt:      now.UnixMilli(),
				DueAt:         parsed.DueAt.UnixMilli(),
				Recurrence:    parsed.Recurrence,
				Status:        StatusScheduled,
			}
			assert.NoError(t, rec.Validate())
		})
	}
}

// The documented defaults have to match the code, because the README states
// them as fact.
func TestDocumentedDefaults(t *testing.T) {
	assert.Equal(t, 9, DefaultHour, "the README says a date with no time means 09:00")
	assert.Equal(t, 500, MaxMessageRunes, "the README and the settings help both state 500 characters")
	assert.Equal(t, 5*time.Minute, ClaimTimeout, "the architecture notes say five minutes")
	assert.Equal(t, 3, MaxAttempts, "the architecture notes say three attempts")
}
