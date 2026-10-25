package reminder

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RecurrenceType names the family of repetition rules.
type RecurrenceType string

const (
	// RecurrenceNone is a one-off reminder.
	RecurrenceNone RecurrenceType = ""

	// RecurrenceDaily fires every Interval days at a fixed wall-clock time.
	RecurrenceDaily RecurrenceType = "daily"

	// RecurrenceWeekly fires on the listed weekdays, every Interval weeks, at a
	// fixed wall-clock time.
	RecurrenceWeekly RecurrenceType = "weekly"

	// RecurrenceMonthly fires on DayOfMonth, every Interval months, at a fixed
	// wall-clock time. A DayOfMonth that a month does not have is clamped to
	// that month's last day.
	RecurrenceMonthly RecurrenceType = "monthly"

	// RecurrenceInterval fires every EverySeconds of elapsed time. Unlike the
	// wall-clock rules, it is unaffected by daylight-saving transitions: "every
	// 12 hours" always means 12 real hours.
	RecurrenceInterval RecurrenceType = "interval"
)

// Recurrence describes how a reminder repeats.
//
// Two different notions of "every" are deliberately kept apart. The wall-clock
// rules (daily, weekly, monthly) pin an hour and minute in the user's timezone
// and let the real elapsed interval stretch or shrink across a daylight-saving
// transition, which is what someone means by "every weekday at 9am". The
// interval rule adds a fixed duration instead, which is what someone means by
// "every 12 hours".
type Recurrence struct {
	Type RecurrenceType `json:"type"`

	// Interval is how many days, weeks, or months to skip between firings. It
	// is at least 1 for the wall-clock rules and unused for RecurrenceInterval.
	Interval int `json:"interval,omitempty"`

	// Weekdays lists the days a weekly rule fires on, sorted and deduplicated.
	Weekdays []time.Weekday `json:"weekdays,omitempty"`

	// DayOfMonth is the 1-31 day a monthly rule targets.
	DayOfMonth int `json:"day_of_month,omitempty"`

	// Hour and Minute are the wall-clock time, in the reminder's timezone.
	Hour   int `json:"hour"`
	Minute int `json:"minute"`

	// EverySeconds is the fixed gap used by RecurrenceInterval.
	EverySeconds int64 `json:"every_seconds,omitempty"`
}

// Clone returns a deep copy, including the Weekdays slice.
func (rc *Recurrence) Clone() *Recurrence {
	if rc == nil {
		return nil
	}
	clone := *rc
	clone.Weekdays = slices.Clone(rc.Weekdays)
	return &clone
}

// Validate checks that the rule is one the scheduler can actually advance. An
// invalid rule would otherwise become a reminder that never fires again.
func (rc *Recurrence) Validate() error {
	switch rc.Type {
	case RecurrenceNone:
		return nil

	case RecurrenceInterval:
		if rc.EverySeconds <= 0 {
			return errors.New("a repeating interval must be greater than zero")
		}

	case RecurrenceDaily, RecurrenceWeekly, RecurrenceMonthly:
		if rc.Interval < 1 {
			return errors.New("a repeat interval must be at least 1")
		}
		if rc.Hour < 0 || rc.Hour > 23 {
			return fmt.Errorf("hour %d is outside 0-23", rc.Hour)
		}
		if rc.Minute < 0 || rc.Minute > 59 {
			return fmt.Errorf("minute %d is outside 0-59", rc.Minute)
		}
		if rc.Type == RecurrenceWeekly && len(rc.Weekdays) == 0 {
			return errors.New("a weekly reminder needs at least one weekday")
		}
		for _, wd := range rc.Weekdays {
			if wd < time.Sunday || wd > time.Saturday {
				return fmt.Errorf("weekday %d is outside 0-6", int(wd))
			}
		}
		if rc.Type == RecurrenceMonthly && (rc.DayOfMonth < 1 || rc.DayOfMonth > 31) {
			return fmt.Errorf("day of month %d is outside 1-31", rc.DayOfMonth)
		}

	default:
		return fmt.Errorf("unknown recurrence type %q", rc.Type)
	}
	return nil
}

// Next returns the first firing strictly after `after`, computed in loc.
//
// `previous` is the occurrence the caller is advancing from; it anchors the
// interval rule so that "every 12 hours" counts from the last scheduled firing
// rather than from the moment the scheduler happened to wake up.
//
// When occurrences were missed — because the server was down, or because
// delivery was disabled — Next skips over them and returns the next future
// firing rather than replaying each one. A recurring reminder is a standing
// instruction, not a queue of backlogged messages.
//
// The zero time is returned when the rule cannot be advanced, which Validate
// is responsible for making impossible.
func (rc *Recurrence) Next(previous, after time.Time, loc *time.Location) time.Time {
	if rc == nil || rc.Type == RecurrenceNone {
		return time.Time{}
	}
	if loc == nil {
		loc = time.UTC
	}

	switch rc.Type {
	case RecurrenceInterval:
		return rc.nextInterval(previous, after)
	case RecurrenceDaily:
		return rc.nextDaily(previous, after, loc)
	case RecurrenceWeekly:
		return rc.nextWeekly(after, loc)
	case RecurrenceMonthly:
		return rc.nextMonthly(previous, after, loc)
	case RecurrenceNone:
		return time.Time{}
	default:
		return time.Time{}
	}
}

// nextInterval advances the anchor by whole intervals until it is past `after`.
// It is computed on absolute instants, so a daylight-saving transition in the
// middle of the gap does not change its length.
func (rc *Recurrence) nextInterval(previous, after time.Time) time.Time {
	step := time.Duration(rc.EverySeconds) * time.Second
	if step <= 0 {
		return time.Time{}
	}

	anchor := previous
	if anchor.IsZero() {
		anchor = after
	}

	// Jump straight to the right multiple rather than looping, so that a long
	// outage costs one division instead of one iteration per missed firing.
	if !anchor.After(after) {
		gap := after.Sub(anchor)
		steps := gap/step + 1
		return anchor.Add(steps * step)
	}
	return anchor.Add(step)
}

// nextDaily advances in whole Interval-day steps from the previous firing, so
// that an every-N-days series keeps its original alignment even when the
// scheduler has to catch up over several missed days.
func (rc *Recurrence) nextDaily(previous, after time.Time, loc *time.Location) time.Time {
	interval := max(rc.Interval, 1)

	anchor := previous
	if anchor.IsZero() {
		anchor = after
	}
	ay, am, ad := anchor.In(loc).Date()
	start := atWallClock(loc, ay, am, ad, rc.Hour, rc.Minute)

	// Estimate the number of whole steps between the anchor and `after` so a
	// long outage costs a division rather than one iteration per missed day.
	// Integer division rounds down, so the estimate is never too far ahead and
	// the short loop below settles on the exact step.
	steps := max(int(after.Sub(start).Hours()/24)/interval, 0)

	for i := steps; i < steps+maxDayScan; i++ {
		candidate := atWallClock(loc, ay, am, ad+i*interval, rc.Hour, rc.Minute)
		if candidate.After(after) {
			return candidate
		}
	}
	return time.Time{}
}

// nextWeekly scans forward day by day for the next listed weekday. Scanning is
// bounded by maxDayScan, which covers the largest interval the parser accepts.
func (rc *Recurrence) nextWeekly(after time.Time, loc *time.Location) time.Time {
	local := after.In(loc)
	y, m, d := local.Date()

	for i := range maxDayScan {
		candidate := atWallClock(loc, y, m, d+i, rc.Hour, rc.Minute)
		if !candidate.After(after) {
			continue
		}
		if !slices.Contains(rc.Weekdays, candidate.Weekday()) {
			continue
		}
		// With an interval above 1, only every Nth week counts. Weeks are
		// measured from the Unix epoch's Monday so that the answer does not
		// depend on when the scheduler happened to ask.
		if rc.Interval > 1 && weekIndex(candidate)%rc.Interval != 0 {
			continue
		}
		return candidate
	}
	return time.Time{}
}

// nextMonthly advances in whole Interval-month steps from the previous firing,
// clamping the target day to the length of each month so that "the 31st" still
// fires in February.
func (rc *Recurrence) nextMonthly(previous, after time.Time, loc *time.Location) time.Time {
	interval := max(rc.Interval, 1)

	anchor := previous
	if anchor.IsZero() {
		anchor = after
	}
	ay, am, _ := anchor.In(loc).Date()
	afterLocal := after.In(loc)

	// As with nextDaily, jump most of the way in one step so that catching up
	// after a long outage does not loop once per missed month.
	gapMonths := (afterLocal.Year()*12 + int(afterLocal.Month())) - (ay*12 + int(am))
	steps := max(gapMonths/interval, 0)

	for i := steps; i < steps+maxMonthScan; i++ {
		year, month := addMonths(ay, am, i*interval)
		day := min(rc.DayOfMonth, daysInMonth(year, month))

		candidate := atWallClock(loc, year, month, day, rc.Hour, rc.Minute)
		if candidate.After(after) {
			return candidate
		}
	}
	return time.Time{}
}

const (
	// maxDayScan bounds the day-by-day scans. 400 days covers a yearly weekly
	// interval with room to spare and guarantees the loops terminate.
	maxDayScan = 400

	// maxMonthScan bounds the monthly scan to just over ten years.
	maxMonthScan = 130
)

// atWallClock builds the instant for a wall-clock time in loc, allowing day and
// month to be out of range so that callers can do arithmetic inline; time.Date
// normalises them.
//
// Daylight-saving transitions make two wall-clock times awkward, and both are
// resolved here so that a repeating reminder fires exactly once per period:
//
//   - A time skipped by a spring-forward transition (02:30 where the clocks
//     jump from 02:00 to 03:00) does not exist. time.Date resolves it to the
//     equivalent instant *before* the jump, which is earlier than the previous
//     day's firing and would make the scheduler skip the day entirely. Instead
//     this reconstructs the instant the time would have been had the clocks not
//     moved — 03:30 in that example — so the reminder fires once, shortly after
//     the jump, a whole number of days on from the last one.
//
//   - A time that happens twice because of a fall-back transition resolves to
//     the first of the two, which is time.Date's own behaviour, so the reminder
//     fires once rather than twice.
func atWallClock(loc *time.Location, year int, month time.Month, day, hour, minute int) time.Time {
	t := time.Date(year, month, day, hour, minute, 0, 0, loc)
	if t.Hour() == hour && t.Minute() == minute {
		return t
	}

	// Reaching here means the wall clock came back different from the one asked
	// for, which only happens when the transition swallowed it. Rebuild the
	// instant from the offset that was in force on the far side of the jump.
	_, offset := t.Zone()
	asUTC := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	return asUTC.Add(-time.Duration(offset) * time.Second).In(loc)
}

// weekIndex numbers the weeks since the Unix epoch, counting from Monday, so
// that an every-N-weeks rule lands on a stable set of weeks.
func weekIndex(t time.Time) int {
	// 1970-01-01 was a Thursday; shifting by 3 days puts week 0 on a Monday.
	days := int(t.Unix() / 86400)
	return (days + 3) / 7
}

// addMonths adds n months to a year/month pair, carrying into the year.
func addMonths(year int, month time.Month, n int) (int, time.Month) {
	total := (year*12 + int(month) - 1) + n
	return total / 12, time.Month(total%12 + 1)
}

// daysInMonth returns the number of days in the given month, leap years included.
func daysInMonth(year int, month time.Month) int {
	// Day zero of the following month is the last day of this one.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Describe renders the rule as the phrase a user would recognise, for command
// responses and for the reminder list.
func (rc *Recurrence) Describe() string {
	if rc == nil || rc.Type == RecurrenceNone {
		return ""
	}

	at := fmt.Sprintf(" at %02d:%02d", rc.Hour, rc.Minute)

	switch rc.Type {
	case RecurrenceInterval:
		return "every " + humanizeSeconds(rc.EverySeconds)

	case RecurrenceDaily:
		if rc.Interval == 1 {
			return "every day" + at
		}
		return fmt.Sprintf("every %d days%s", rc.Interval, at)

	case RecurrenceWeekly:
		days := make([]string, 0, len(rc.Weekdays))
		for _, wd := range rc.Weekdays {
			days = append(days, weekdayNames[wd])
		}
		every := "every "
		if rc.Interval > 1 {
			every = fmt.Sprintf("every %d weeks on ", rc.Interval)
		}
		return every + joinWithAnd(days) + at

	case RecurrenceMonthly:
		every := "every month"
		if rc.Interval > 1 {
			every = fmt.Sprintf("every %d months", rc.Interval)
		}
		return fmt.Sprintf("%s on the %s%s", every, ordinal(rc.DayOfMonth), at)

	case RecurrenceNone:
		return ""
	}
	return ""
}

// humanizeSeconds renders a duration in the largest whole unit that divides it,
// so 7200 reads as "2 hours" rather than "120 minutes".
func humanizeSeconds(secs int64) string {
	units := []struct {
		size int64
		name string
	}{
		{86400 * 7, "week"},
		{86400, "day"},
		{3600, "hour"},
		{60, "minute"},
		{1, "second"},
	}
	for _, u := range units {
		if secs >= u.size && secs%u.size == 0 {
			n := secs / u.size
			if n == 1 {
				return u.name
			}
			return fmt.Sprintf("%d %ss", n, u.name)
		}
	}
	return fmt.Sprintf("%d seconds", secs)
}

// joinWithAnd renders a list the way English does: "a, b and c".
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// ordinal renders 1 as "1st", 2 as "2nd", and so on.
func ordinal(n int) string {
	suffix := "th"
	// 11th, 12th and 13th are the exceptions to the digit rule.
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
