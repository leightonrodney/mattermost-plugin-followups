package reminder

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextDaily(t *testing.T) {
	loc := newYork(t)
	at := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, loc)
	}

	rec := &Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 9}

	previous := at(2026, time.June, 1, 9, 0)
	next := rec.Next(previous, previous, loc)
	assert.True(t, next.Equal(at(2026, time.June, 2, 9, 0)), "got %s", next)

	t.Run("every three days keeps its alignment", func(t *testing.T) {
		rec := &Recurrence{Type: RecurrenceDaily, Interval: 3, Hour: 9}
		previous := at(2026, time.June, 1, 9, 0)
		next := rec.Next(previous, previous, loc)
		assert.True(t, next.Equal(at(2026, time.June, 4, 9, 0)), "got %s", next)
	})
}

func TestNextSkipsMissedOccurrences(t *testing.T) {
	loc := newYork(t)
	at := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, loc)
	}

	// The server was down for a fortnight. A standing daily reminder should
	// resume with the next future firing, not replay fourteen of them.
	rec := &Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 9}
	previous := at(2026, time.June, 1, 9, 0)
	now := at(2026, time.June, 15, 14, 0)

	next := rec.Next(previous, now, loc)
	assert.True(t, next.Equal(at(2026, time.June, 16, 9, 0)), "got %s", next)

	t.Run("an every-three-days series stays on its own days", func(t *testing.T) {
		rec := &Recurrence{Type: RecurrenceDaily, Interval: 3, Hour: 9}
		previous := at(2026, time.June, 1, 9, 0) // the series runs 1, 4, 7, 10, 13, 16
		now := at(2026, time.June, 14, 12, 0)

		next := rec.Next(previous, now, loc)
		assert.True(t, next.Equal(at(2026, time.June, 16, 9, 0)), "got %s", next)
	})

	t.Run("a fixed interval jumps whole steps", func(t *testing.T) {
		rec := &Recurrence{Type: RecurrenceInterval, EverySeconds: 3600}
		previous := at(2026, time.June, 1, 9, 0)
		now := at(2026, time.June, 1, 15, 30)

		next := rec.Next(previous, now, loc)
		assert.True(t, next.Equal(at(2026, time.June, 1, 16, 0)), "got %s", next)
	})
}

func TestNextWeekly(t *testing.T) {
	loc := newYork(t)
	at := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, loc)
	}

	// 2026-06-01 is a Monday.
	rec := &Recurrence{
		Type:     RecurrenceWeekly,
		Interval: 1,
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
		Hour:     10,
	}

	monday := at(2026, time.June, 1, 10, 0)
	require.Equal(t, time.Monday, monday.Weekday())

	wednesday := rec.Next(monday, monday, loc)
	assert.True(t, wednesday.Equal(at(2026, time.June, 3, 10, 0)), "got %s", wednesday)

	friday := rec.Next(wednesday, wednesday, loc)
	assert.True(t, friday.Equal(at(2026, time.June, 5, 10, 0)), "got %s", friday)

	nextMonday := rec.Next(friday, friday, loc)
	assert.True(t, nextMonday.Equal(at(2026, time.June, 8, 10, 0)), "got %s", nextMonday)
}

func TestNextWeeklyEveryOtherWeek(t *testing.T) {
	loc := newYork(t)
	rec := &Recurrence{
		Type:     RecurrenceWeekly,
		Interval: 2,
		Weekdays: []time.Weekday{time.Monday},
		Hour:     10,
	}

	previous := time.Date(2026, time.June, 1, 10, 0, 0, 0, loc)
	next := rec.Next(previous, previous, loc)

	assert.Equal(t, time.Monday, next.Weekday())
	gap := next.Sub(previous)
	assert.Equal(t, 14*24*time.Hour, gap, "every other week must skip a week, got %s", gap)
}

func TestNextMonthlyClampsShortMonths(t *testing.T) {
	loc := time.UTC
	rec := &Recurrence{Type: RecurrenceMonthly, Interval: 1, DayOfMonth: 31, Hour: 9}

	// January the 31st, then a month with no 31st, then one that has it again.
	previous := time.Date(2026, time.January, 31, 9, 0, 0, 0, loc)

	february := rec.Next(previous, previous, loc)
	assert.Equal(t, 28, february.Day(), "February 2026 has 28 days")
	assert.Equal(t, time.February, february.Month())

	march := rec.Next(february, february, loc)
	assert.Equal(t, 31, march.Day(), "the series returns to the 31st when the month has one")
	assert.Equal(t, time.March, march.Month())

	t.Run("a leap February gets the 29th", func(t *testing.T) {
		previous := time.Date(2028, time.January, 31, 9, 0, 0, 0, loc)
		february := rec.Next(previous, previous, loc)
		assert.Equal(t, 29, february.Day(), "2028 is a leap year")
	})
}

func TestNextMonthlyEveryOtherMonth(t *testing.T) {
	loc := time.UTC
	rec := &Recurrence{Type: RecurrenceMonthly, Interval: 2, DayOfMonth: 15, Hour: 9}

	previous := time.Date(2026, time.January, 15, 9, 0, 0, 0, loc)
	next := rec.Next(previous, previous, loc)

	assert.Equal(t, time.March, next.Month())
	assert.Equal(t, 15, next.Day())
}

// --- Daylight saving ------------------------------------------------------
//
// America/New_York springs forward on 2026-03-08 (02:00 becomes 03:00) and
// falls back on 2026-11-01 (02:00 becomes 01:00).

func TestDailyKeepsWallClockAcrossSpringForward(t *testing.T) {
	loc := newYork(t)
	rec := &Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 9}

	previous := time.Date(2026, time.March, 7, 9, 0, 0, 0, loc)
	next := rec.Next(previous, previous, loc)

	assert.Equal(t, 9, next.Hour(), "the reminder stays at 9am local")
	assert.Equal(t, 8, next.Day())
	assert.Equal(t, 23*time.Hour, next.Sub(previous), "only 23 real hours pass on the short day")
}

func TestDailyKeepsWallClockAcrossFallBack(t *testing.T) {
	loc := newYork(t)
	rec := &Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 9}

	previous := time.Date(2026, time.October, 31, 9, 0, 0, 0, loc)
	next := rec.Next(previous, previous, loc)

	assert.Equal(t, 9, next.Hour(), "the reminder stays at 9am local")
	assert.Equal(t, time.November, next.Month())
	assert.Equal(t, 1, next.Day())
	assert.Equal(t, 25*time.Hour, next.Sub(previous), "25 real hours pass on the long day")
}

func TestDailySurvivesASkippedWallClockTime(t *testing.T) {
	loc := newYork(t)

	// 02:30 does not exist on 2026-03-08: the clocks jump straight from 01:59
	// to 03:00. The reminder must still fire exactly once that day, and strictly
	// after the previous day's firing, rather than being skipped.
	rec := &Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 2, Minute: 30}

	previous := time.Date(2026, time.March, 7, 2, 30, 0, 0, loc)
	next := rec.Next(previous, previous, loc)

	assert.True(t, next.After(previous), "the next firing must move forward in time")
	assert.Equal(t, 8, next.Day(), "the transition day must not be skipped, got %s", next)
	assert.Equal(t, 24*time.Hour, next.Sub(previous), "a whole day of real time still passes")

	dayAfter := rec.Next(next, next, loc)
	assert.Equal(t, 9, dayAfter.Day())
	assert.Equal(t, 2, dayAfter.Hour(), "the series returns to 02:30 once the transition is past")
	assert.Equal(t, 30, dayAfter.Minute())
}

func TestDailyFiresOnceOnARepeatedWallClockTime(t *testing.T) {
	loc := newYork(t)

	// 01:30 happens twice on 2026-11-01. The reminder must fire once.
	rec := &Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 1, Minute: 30}

	previous := time.Date(2026, time.October, 31, 1, 30, 0, 0, loc)
	next := rec.Next(previous, previous, loc)

	assert.Equal(t, 1, next.Day())
	assert.Equal(t, time.November, next.Month())
	assert.Equal(t, 1, next.Hour())
	assert.Equal(t, 30, next.Minute())

	// The following firing is the next day, not the second 01:30 of the same day.
	following := rec.Next(next, next, loc)
	assert.Equal(t, 2, following.Day(), "got %s", following)
}

func TestIntervalIgnoresDaylightSaving(t *testing.T) {
	loc := newYork(t)

	// A fixed interval counts real elapsed time, so the gap is unchanged by a
	// transition even though the wall clock shifts by an hour.
	rec := &Recurrence{Type: RecurrenceInterval, EverySeconds: 12 * 3600}

	previous := time.Date(2026, time.March, 7, 20, 0, 0, 0, loc)
	next := rec.Next(previous, previous, loc)

	assert.Equal(t, 12*time.Hour, next.Sub(previous))
	assert.Equal(t, 9, next.Hour(), "the wall clock moves because the offset changed")
}

func TestValidateRejectsUnusableRules(t *testing.T) {
	tests := []struct {
		name string
		rec  Recurrence
	}{
		{"unknown type", Recurrence{Type: "yearly"}},
		{"zero interval seconds", Recurrence{Type: RecurrenceInterval}},
		{"zero interval", Recurrence{Type: RecurrenceDaily, Interval: 0}},
		{"weekly without days", Recurrence{Type: RecurrenceWeekly, Interval: 1}},
		{"hour out of range", Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 24}},
		{"minute out of range", Recurrence{Type: RecurrenceDaily, Interval: 1, Minute: 60}},
		{"day of month out of range", Recurrence{Type: RecurrenceMonthly, Interval: 1, DayOfMonth: 32}},
		{"weekday out of range", Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{9}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Error(t, test.rec.Validate())
		})
	}
}

func TestValidateAcceptsUsableRules(t *testing.T) {
	tests := []Recurrence{
		{Type: RecurrenceNone},
		{Type: RecurrenceInterval, EverySeconds: 60},
		{Type: RecurrenceDaily, Interval: 1, Hour: 9},
		{Type: RecurrenceWeekly, Interval: 2, Weekdays: []time.Weekday{time.Monday}, Hour: 9},
		{Type: RecurrenceMonthly, Interval: 1, DayOfMonth: 31, Hour: 23, Minute: 59},
	}
	for _, rec := range tests {
		assert.NoError(t, rec.Validate(), "%+v", rec)
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		rec  Recurrence
		want string
	}{
		{Recurrence{Type: RecurrenceNone}, ""},
		{Recurrence{Type: RecurrenceDaily, Interval: 1, Hour: 9}, "every day at 09:00"},
		{Recurrence{Type: RecurrenceDaily, Interval: 3, Hour: 8, Minute: 30}, "every 3 days at 08:30"},
		{Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Monday}, Hour: 10}, "every Monday at 10:00"},
		{Recurrence{Type: RecurrenceWeekly, Interval: 1, Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday}, Hour: 10}, "every Monday, Wednesday and Friday at 10:00"},
		{Recurrence{Type: RecurrenceWeekly, Interval: 2, Weekdays: []time.Weekday{time.Monday}, Hour: 10}, "every 2 weeks on Monday at 10:00"},
		{Recurrence{Type: RecurrenceMonthly, Interval: 1, DayOfMonth: 1, Hour: 9}, "every month on the 1st at 09:00"},
		{Recurrence{Type: RecurrenceMonthly, Interval: 1, DayOfMonth: 22, Hour: 9}, "every month on the 22nd at 09:00"},
		{Recurrence{Type: RecurrenceMonthly, Interval: 3, DayOfMonth: 13, Hour: 9}, "every 3 months on the 13th at 09:00"},
		{Recurrence{Type: RecurrenceInterval, EverySeconds: 7200}, "every 2 hours"},
		{Recurrence{Type: RecurrenceInterval, EverySeconds: 3600}, "every hour"},
		{Recurrence{Type: RecurrenceInterval, EverySeconds: 1800}, "every 30 minutes"},
	}

	for _, test := range tests {
		assert.Equal(t, test.want, test.rec.Describe())
	}
}

func TestCloneIsDeep(t *testing.T) {
	original := &Reminder{
		ID:         "abc",
		Recurrence: &Recurrence{Type: RecurrenceWeekly, Weekdays: []time.Weekday{time.Monday}},
	}

	clone := original.Clone()
	clone.Recurrence.Weekdays[0] = time.Friday
	clone.ID = "xyz"

	assert.Equal(t, time.Monday, original.Recurrence.Weekdays[0], "the original must not see the change")
	assert.Equal(t, "abc", original.ID)
}
