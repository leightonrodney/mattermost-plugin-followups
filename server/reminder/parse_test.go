package reminder

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newYork is used throughout because it has both daylight-saving transitions
// within the test year, which UTC would hide.
func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err, "the host needs a timezone database")
	return loc
}

// reference is a Tuesday afternoon, chosen so that "tomorrow" and "Monday"
// resolve to different days and neither is a weekend.
func reference(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, time.March, 10, 14, 30, 0, 0, newYork(t))
}

func TestParseRelative(t *testing.T) {
	now := reference(t)
	loc := newYork(t)

	tests := []struct {
		name  string
		input string
		want  time.Time
		text  string
	}{
		{"minutes", "me in 30 minutes to check the build", now.Add(30 * time.Minute), "check the build"},
		{"abbreviated unit", "me in 30m to check the build", now.Add(30 * time.Minute), "check the build"},
		{"hours", "me in 2 hours to stretch", now.Add(2 * time.Hour), "stretch"},
		{"article", "me in an hour to stretch", now.Add(time.Hour), "stretch"},
		{"half an hour", "me in half an hour to stretch", now.Add(30 * time.Minute), "stretch"},
		{"days", "me in 3 days to follow up", now.AddDate(0, 0, 3), "follow up"},
		{"weeks", "me in 2 weeks to follow up", now.AddDate(0, 0, 14), "follow up"},
		{"calendar month", "me in 1 month to renew", now.AddDate(0, 1, 0), "renew"},
		{"text first", "me to check the build in 30 minutes", now.Add(30 * time.Minute), "check the build"},
		{"text first without to", "me check the build in 30 minutes", now.Add(30 * time.Minute), "check the build"},
		{"no subject", "in 30 minutes to check the build", now.Add(30 * time.Minute), "check the build"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.input, now, loc)
			require.NoError(t, err)
			assert.True(t, got.DueAt.Equal(test.want), "due %s, want %s", got.DueAt, test.want)
			assert.Equal(t, test.text, got.Message)
			assert.Nil(t, got.Recurrence)
		})
	}
}

func TestParseAbsolute(t *testing.T) {
	now := reference(t) // Tuesday 2026-03-10 14:30 America/New_York
	loc := newYork(t)

	at := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, loc)
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
		text  string
	}{
		{"tomorrow defaults to morning", "me tomorrow to review the deployment", at(2026, time.March, 11, 9, 0), "review the deployment"},
		{"tomorrow at a time", "me tomorrow at 9am to review the deployment", at(2026, time.March, 11, 9, 0), "review the deployment"},
		{"tonight", "me tonight to water the plants", at(2026, time.March, 10, 20, 0), "water the plants"},
		{"today still ahead", "me today at 6pm to leave", at(2026, time.March, 10, 18, 0), "leave"},
		{"today already past rolls over", "me today at 9am to leave", at(2026, time.March, 11, 9, 0), "leave"},
		{"bare time rolls to tomorrow", "me at 9am to leave", at(2026, time.March, 11, 9, 0), "leave"},
		{"bare time still ahead", "me at 18:00 to leave", at(2026, time.March, 10, 18, 0), "leave"},
		{"noon", "me at noon to eat", at(2026, time.March, 11, 12, 0), "eat"},
		{"midnight", "me at midnight to sleep", at(2026, time.March, 11, 0, 0), "sleep"},
		{"weekday later this week", "me friday at 10am to send the report", at(2026, time.March, 13, 10, 0), "send the report"},
		{"weekday today still ahead", "me tuesday at 6pm to send the report", at(2026, time.March, 10, 18, 0), "send the report"},
		{"next weekday skips today", "me next tuesday at 10am to send the report", at(2026, time.March, 17, 10, 0), "send the report"},
		{"next monday", "me next monday to send the report", at(2026, time.March, 16, 9, 0), "send the report"},
		{"next week", "me next week to send the report", at(2026, time.March, 17, 9, 0), "send the report"},
		{"next month", "me next month to send the report", at(2026, time.April, 10, 9, 0), "send the report"},
		{"iso date", "me on 2026-11-03 at 14:30 to vote", at(2026, time.November, 3, 14, 30), "vote"},
		{"iso date without on", "me 2026-11-03 to vote", at(2026, time.November, 3, 9, 0), "vote"},
		{"month name day", "me Nov 3 at 2pm to vote", at(2026, time.November, 3, 14, 0), "vote"},
		{"day month name", "me 3 November at 2pm to vote", at(2026, time.November, 3, 14, 0), "vote"},
		{"month name with year", "me Jan 5 2027 to plan", at(2027, time.January, 5, 9, 0), "plan"},
		{"ordinal day", "me Nov 3rd to vote", at(2026, time.November, 3, 9, 0), "vote"},
		{"past month rolls to next year", "me Jan 5 to plan", at(2027, time.January, 5, 9, 0), "plan"},
		{"space separated meridiem", "me tomorrow at 9 am to review", at(2026, time.March, 11, 9, 0), "review"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.input, now, loc)
			require.NoError(t, err)
			assert.True(t, got.DueAt.Equal(test.want), "due %s, want %s", got.DueAt, test.want)
			assert.Equal(t, test.text, got.Message)
		})
	}
}

func TestParseRecurring(t *testing.T) {
	now := reference(t) // Tuesday 2026-03-10 14:30
	loc := newYork(t)

	at := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, loc)
	}

	tests := []struct {
		name     string
		input    string
		wantDue  time.Time
		wantRec  Recurrence
		wantText string
	}{
		{
			name:     "every monday",
			input:    "me every Monday at 10am to check the sprint board",
			wantDue:  at(2026, time.March, 16, 10, 0),
			wantRec:  Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Monday}, Hour: 10},
			wantText: "check the sprint board",
		},
		{
			name:     "every day",
			input:    "me every day at 9am to stand up",
			wantDue:  at(2026, time.March, 11, 9, 0),
			wantRec:  Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 9},
			wantText: "stand up",
		},
		{
			name:     "every day defaults to morning",
			input:    "me every day to stand up",
			wantDue:  at(2026, time.March, 11, 9, 0),
			wantRec:  Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 9},
			wantText: "stand up",
		},
		{
			// The series is anchored on the day it was created, so the first
			// firing is a full three days on from that day's 8am, not the
			// next morning.
			name:     "every three days",
			input:    "me every 3 days at 8am to water the plants",
			wantDue:  at(2026, time.March, 13, 8, 0),
			wantRec:  Recurrence{Type: RecurrenceDaily, Interval: 3, Hour: 8},
			wantText: "water the plants",
		},
		{
			name:     "every weekday",
			input:    "me every weekday at 9:15 to post the update",
			wantDue:  at(2026, time.March, 11, 9, 15),
			wantRec:  Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}, Hour: 9, Minute: 15},
			wantText: "post the update",
		},
		{
			name:     "every weekend",
			input:    "me every weekend at 11am to call home",
			wantDue:  at(2026, time.March, 14, 11, 0),
			wantRec:  Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Saturday, time.Sunday}, Hour: 11},
			wantText: "call home",
		},
		{
			name:     "weekday list with and",
			input:    "me every Monday and Friday at 10am to sync",
			wantDue:  at(2026, time.March, 13, 10, 0),
			wantRec:  Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Monday, time.Friday}, Hour: 10},
			wantText: "sync",
		},
		{
			name:     "comma separated weekdays",
			input:    "me every mon,wed,fri at 10am to sync",
			wantDue:  at(2026, time.March, 11, 10, 0),
			wantRec:  Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday}, Hour: 10},
			wantText: "sync",
		},
		{
			name:     "every other monday",
			input:    "me every other Monday at 10am to sync",
			wantRec:  Recurrence{Type: RecurrenceWeekly, Interval: 2, Weekdays: []time.Weekday{time.Monday}, Hour: 10},
			wantText: "sync",
		},
		{
			name:     "every month on a day",
			input:    "me every month on the 15th at 9am to file the report",
			wantDue:  at(2026, time.March, 15, 9, 0),
			wantRec:  Recurrence{Type: RecurrenceMonthly, Interval: 1, DayOfMonth: 15, Hour: 9},
			wantText: "file the report",
		},
		{
			name:     "every two hours is a fixed interval",
			input:    "me every 2 hours to drink water",
			wantDue:  now.Add(2 * time.Hour),
			wantRec:  Recurrence{Type: RecurrenceInterval, EverySeconds: 7200},
			wantText: "drink water",
		},
		{
			name:     "every 30 minutes",
			input:    "me every 30 minutes to check the queue",
			wantDue:  now.Add(30 * time.Minute),
			wantRec:  Recurrence{Type: RecurrenceInterval, EverySeconds: 1800},
			wantText: "check the queue",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.input, now, loc)
			require.NoError(t, err)
			require.NotNil(t, got.Recurrence)
			assert.Equal(t, test.wantText, got.Message)
			assert.Equal(t, test.wantRec.Type, got.Recurrence.Type)
			assert.Equal(t, test.wantRec.Interval, got.Recurrence.Interval)
			assert.Equal(t, test.wantRec.Weekdays, got.Recurrence.Weekdays)
			assert.Equal(t, test.wantRec.DayOfMonth, got.Recurrence.DayOfMonth)
			assert.Equal(t, test.wantRec.Hour, got.Recurrence.Hour)
			assert.Equal(t, test.wantRec.Minute, got.Recurrence.Minute)
			assert.Equal(t, test.wantRec.EverySeconds, got.Recurrence.EverySeconds)
			if !test.wantDue.IsZero() {
				assert.True(t, got.DueAt.Equal(test.wantDue), "due %s, want %s", got.DueAt, test.wantDue)
			}
			assert.True(t, got.DueAt.After(now), "the first occurrence must be in the future")
		})
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	now := reference(t)
	loc := newYork(t)

	tests := []struct {
		name        string
		input       string
		wantMessage string
	}{
		{"empty", "me", "tell me what to remind you about"},
		{"no time", "me to do the thing", "couldn't find a time"},
		{"no text", "me in 30 minutes", "needs something to say"},
		{"ambiguous slash date", "me on 11/03 to vote", "day/month or month/day"},
		{"impossible date", "me on 2026-02-30 to vote", "couldn't find a time"},
		{"impossible month day", "me Feb 30 to vote", "couldn't find a time"},
		{"impossible clock time", "me at 25:00 to vote", "couldn't find a time"},
		{"impossible minute", "me at 10:75 to vote", "couldn't find a time"},
		{"zero interval", "me every 0 days to sync", "couldn't find a time"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(test.input, now, loc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantMessage)
		})
	}
}

func TestParseRespectsTimezone(t *testing.T) {
	// The same wall-clock request in two zones must land on two different
	// absolute instants, eight hours apart.
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)

	now := time.Date(2026, time.June, 1, 6, 0, 0, 0, time.UTC)

	inTokyo, err := Parse("me tomorrow at 9am to call", now.In(tokyo), tokyo)
	require.NoError(t, err)
	inBerlin, err := Parse("me tomorrow at 9am to call", now.In(berlin), berlin)
	require.NoError(t, err)

	assert.Equal(t, 9, inTokyo.DueAt.In(tokyo).Hour())
	assert.Equal(t, 9, inBerlin.DueAt.In(berlin).Hour())
	assert.NotEqual(t, inTokyo.DueAt.UTC(), inBerlin.DueAt.UTC())
}

func TestParseMessageLimits(t *testing.T) {
	now := reference(t)
	loc := newYork(t)

	t.Run("rejects an over-long message", func(t *testing.T) {
		long := ""
		for range MaxMessageRunes + 1 {
			long += "x"
		}
		_, err := Parse("me in 5 minutes to "+long, now, loc)
		require.ErrorIs(t, err, ErrMessageTooLong)
	})

	t.Run("collapses whitespace", func(t *testing.T) {
		got, err := Parse("me in 5 minutes to   check    the build  ", now, loc)
		require.NoError(t, err)
		assert.Equal(t, "check the build", got.Message)
	})

	t.Run("keeps multibyte text intact", func(t *testing.T) {
		got, err := Parse("me in 5 minutes to 締め切りを確認する", now, loc)
		require.NoError(t, err)
		assert.Equal(t, "締め切りを確認する", got.Message)
	})
}
