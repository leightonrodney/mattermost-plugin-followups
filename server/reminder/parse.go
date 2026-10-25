package reminder

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ParseResult is a parsed `/remind` request: when it should first fire, how it
// repeats, and what it should say.
type ParseResult struct {
	// Message is the reminder text, already normalized.
	Message string

	// DueAt is the first firing, as an absolute instant.
	DueAt time.Time

	// Recurrence is nil for a one-off reminder.
	Recurrence *Recurrence
}

// ErrNoTimeExpression means nothing in the input looked like a time.
var ErrNoTimeExpression = errors.New("no time found")

// Parse reads a `/remind` argument string such as
//
//	me in 30 minutes to check the build
//	me every Monday at 10am to review the sprint board
//	me to call the vendor back tomorrow at 9am
//
// and returns when to fire, how to repeat, and what to say. `now` is the
// reference instant and `loc` the user's timezone; both are passed in rather
// than read from the clock so that the grammar is fully testable.
//
// The grammar is deterministic: no statistical model, no network call, and the
// same input with the same `now` and `loc` always yields the same result. The
// full set of accepted forms is listed in the README.
//
// Where the input contains a time expression both before and after the text,
// the trailing one wins, unless the text is introduced by the word `to`, in
// which case the leading expression wins. That makes both of the natural
// orderings — "in 30 minutes to check the build" and "to check the build in 30
// minutes" — mean what they look like.
func Parse(input string, now time.Time, loc *time.Location) (*ParseResult, error) {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)

	tokens := strings.Fields(input)

	// "/remind me ..." is the documented form, but the subject is optional.
	if len(tokens) > 0 && strings.EqualFold(tokens[0], "me") {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return nil, errors.New("tell me what to remind you about, for example `/remind me in 30 minutes to check the build`")
	}

	timeExpr, messageTokens, err := splitTimeAndMessage(tokens, now, loc)
	if err != nil {
		return nil, err
	}

	// A leading "to" introduces the text rather than being part of it.
	if len(messageTokens) > 0 && strings.EqualFold(messageTokens[0], "to") {
		messageTokens = messageTokens[1:]
	}

	message, err := NormalizeMessage(strings.Join(messageTokens, " "))
	if err != nil {
		return nil, err
	}

	return &ParseResult{
		Message:    message,
		DueAt:      timeExpr.due,
		Recurrence: timeExpr.recurrence,
	}, nil
}

// timeExpr is a successfully parsed time expression.
type timeExpr struct {
	due        time.Time
	recurrence *Recurrence
}

// splitTimeAndMessage locates the time expression within the token list and
// returns it alongside the tokens that make up the message.
func splitTimeAndMessage(tokens []string, now time.Time, loc *time.Location) (timeExpr, []string, error) {
	// A leading expression: the longest prefix of tokens that parses.
	prefixExpr, prefixLen, prefixOK := parseLongestPrefix(tokens, now, loc)

	// A trailing expression: the longest suffix of tokens that parses in full.
	var (
		suffixExpr    timeExpr
		suffixStart   = -1
		suffixOK      bool
		firstPossible = 0
	)
	// A suffix that swallows every token would leave no message; start at 1
	// unless the prefix also failed, in which case the caller gets a clearer
	// "no text" error from NormalizeMessage.
	if len(tokens) > 1 {
		firstPossible = 1
	}
	for j := firstPossible; j < len(tokens); j++ {
		if expr, consumed, ok := parseTimeExpression(tokens[j:], now, loc); ok && consumed == len(tokens)-j {
			suffixExpr, suffixStart, suffixOK = expr, j, true
			break
		}
	}

	switch {
	case prefixOK && prefixLen < len(tokens) && strings.EqualFold(tokens[prefixLen], "to"):
		// "in 30 minutes to check the build": the `to` marks the text.
		return prefixExpr, tokens[prefixLen:], nil
	case suffixOK:
		return suffixExpr, tokens[:suffixStart], nil
	case prefixOK:
		return prefixExpr, tokens[prefixLen:], nil
	default:
		// A slash date is the one shape that looks like a date but cannot be
		// read safely: 03/11 is the 3rd of November to half the world and the
		// 11th of March to the other half. Say so rather than guessing.
		if slashDate := findSlashDate(tokens); slashDate != "" {
			return timeExpr{}, nil, fmt.Errorf(
				"%q could be either day/month or month/day, so I won't guess. Write it as `2026-11-03` or `Nov 3` instead", slashDate)
		}
		//nolint:staticcheck // These errors are shown to the user as written, so they read as sentences.
		return timeExpr{}, nil, fmt.Errorf(
			"I couldn't find a time in %q. Try `in 30 minutes`, `tomorrow at 9am`, `Monday at 10:00`, `2026-11-03 at 14:30`, or `every weekday at 9am`",
			strings.Join(tokens, " "))
	}
}

// parseLongestPrefix returns the longest prefix of tokens that parses as a time
// expression.
func parseLongestPrefix(tokens []string, now time.Time, loc *time.Location) (timeExpr, int, bool) {
	expr, consumed, ok := parseTimeExpression(tokens, now, loc)
	if !ok {
		return timeExpr{}, 0, false
	}
	return expr, consumed, true
}

// parseTimeExpression parses a time expression from the start of tokens and
// reports how many tokens it consumed.
func parseTimeExpression(tokens []string, now time.Time, loc *time.Location) (timeExpr, int, bool) {
	if len(tokens) == 0 {
		return timeExpr{}, 0, false
	}

	switch strings.ToLower(strings.TrimSuffix(tokens[0], ",")) {
	case "every", "each":
		return parseRecurring(tokens, now, loc)
	case "in":
		return parseRelative(tokens, now)
	}
	return parseAbsolute(tokens, now, loc)
}

// ---------------------------------------------------------------------------
// Relative times: "in 30 minutes", "in 2 hours", "in a week"
// ---------------------------------------------------------------------------

// unitSeconds maps every accepted spelling of a duration unit to its length.
// Months and years are deliberately absent: they are not fixed durations, and
// "in 2 months" is handled by the calendar path instead.
var unitSeconds = map[string]int64{
	"s": 1, "sec": 1, "secs": 1, "second": 1, "seconds": 1,
	"m": 60, "min": 60, "mins": 60, "minute": 60, "minutes": 60,
	"h": 3600, "hr": 3600, "hrs": 3600, "hour": 3600, "hours": 3600,
	"d": 86400, "day": 86400, "days": 86400,
	"w": 604800, "wk": 604800, "wks": 604800, "week": 604800, "weeks": 604800,
}

// calendarUnits are the durations that have to be added on the calendar rather
// than as a fixed number of seconds, because their length varies.
var calendarUnits = map[string]string{
	"mo": "month", "month": "month", "months": "month",
	"y": "year", "yr": "year", "yrs": "year", "year": "year", "years": "year",
}

// parseRelative handles "in <count> <unit>", including the glued form "in 30m"
// and the article form "in an hour".
func parseRelative(tokens []string, now time.Time) (timeExpr, int, bool) {
	if len(tokens) < 2 {
		return timeExpr{}, 0, false
	}

	count, unit, consumed, ok := parseCountAndUnit(tokens[1:])
	if !ok {
		return timeExpr{}, 0, false
	}

	if secs, isFixed := unitSeconds[unit]; isFixed {
		due := now.Add(time.Duration(count*secs) * time.Second)
		return timeExpr{due: due}, consumed + 1, true
	}

	switch calendarUnits[unit] {
	case "month":
		return timeExpr{due: now.AddDate(0, int(count), 0)}, consumed + 1, true
	case "year":
		return timeExpr{due: now.AddDate(int(count), 0, 0)}, consumed + 1, true
	}
	return timeExpr{}, 0, false
}

// parseCountAndUnit reads "30 minutes", "30minutes", "an hour" or "half an
// hour" from the front of tokens.
func parseCountAndUnit(tokens []string) (count int64, unit string, consumed int, ok bool) {
	if len(tokens) == 0 {
		return 0, "", 0, false
	}

	head := strings.ToLower(strings.Trim(tokens[0], ","))

	// "half an hour" is common enough to be worth accepting literally.
	if head == "half" && len(tokens) >= 3 && isArticle(tokens[1]) {
		if u, found := normalizeUnit(tokens[2]); found && unitSeconds[u] >= 60 {
			return unitSeconds[u] / 120, "minutes", 3, unitSeconds[u]%120 == 0
		}
		return 0, "", 0, false
	}

	// "a day", "an hour"
	if isArticle(head) && len(tokens) >= 2 {
		if u, found := normalizeUnit(tokens[1]); found {
			return 1, u, 2, true
		}
		return 0, "", 0, false
	}

	// "30m" written as one token.
	if n, rest, split := splitLeadingNumber(head); split && rest != "" {
		if u, found := normalizeUnit(rest); found {
			return n, u, 1, true
		}
		return 0, "", 0, false
	}

	// "30 minutes"
	n, err := strconv.ParseInt(head, 10, 64)
	if err != nil || n < 0 || len(tokens) < 2 {
		return 0, "", 0, false
	}
	u, found := normalizeUnit(tokens[1])
	if !found {
		return 0, "", 0, false
	}
	return n, u, 2, true
}

func isArticle(s string) bool {
	switch strings.ToLower(s) {
	case "a", "an", "one":
		return true
	}
	return false
}

// normalizeUnit resolves a unit spelling to its canonical key in unitSeconds or
// calendarUnits.
func normalizeUnit(token string) (string, bool) {
	u := strings.ToLower(strings.Trim(token, ".,"))
	if _, ok := unitSeconds[u]; ok {
		return u, true
	}
	if _, ok := calendarUnits[u]; ok {
		return u, true
	}
	return "", false
}

// splitLeadingNumber splits "30m" into 30 and "m".
func splitLeadingNumber(s string) (int64, string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, "", false
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return n, s[i:], true
}

// ---------------------------------------------------------------------------
// Absolute times: "tomorrow at 9am", "Monday", "2026-11-03 at 14:30"
// ---------------------------------------------------------------------------

const (
	// DefaultHour is the time of day used when a date is given without one, so
	// that "tomorrow" means tomorrow morning rather than midnight. It is
	// exported so that the command's help text states the real value.
	DefaultHour = 9

	// eveningHour is what "tonight" means.
	eveningHour = 20
)

// parseAbsolute handles every form that names a point on the calendar rather
// than an offset from now.
func parseAbsolute(tokens []string, now time.Time, loc *time.Location) (timeExpr, int, bool) {
	consumed := 0

	// "on Monday", "on 2026-11-03": the preposition is optional noise.
	if strings.EqualFold(tokens[0], "on") {
		tokens, consumed = tokens[1:], 1
		if len(tokens) == 0 {
			return timeExpr{}, 0, false
		}
	}

	// A bare "at 9am" means the next time it is 9am.
	if strings.EqualFold(tokens[0], "at") {
		hour, minute, used, ok := parseAtClause(tokens)
		if !ok {
			return timeExpr{}, 0, false
		}
		return timeExpr{due: nextWallClock(now, loc, hour, minute)}, consumed + used, true
	}

	date, used, ok := parseDatePhrase(tokens, now, loc)
	if !ok {
		return timeExpr{}, 0, false
	}
	consumed += used
	tokens = tokens[used:]

	hour, minute, timeUsed, hasTime := parseAtClause(tokens)
	if !hasTime {
		hour, minute = date.defaultHour, 0
	}
	consumed += timeUsed

	due := atWallClock(loc, date.year, date.month, date.day, hour, minute)

	// "at 9am" with no date means the next 9am, not one that has already gone.
	if date.floating && !due.After(now) {
		due = due.AddDate(0, 0, 1)
	}

	return timeExpr{due: due}, consumed, true
}

// datePhrase is a resolved calendar date plus the time of day to assume when
// the input did not give one.
type datePhrase struct {
	year  int
	month time.Month
	day   int

	// defaultHour is the hour to use when no "at" clause follows.
	defaultHour int

	// floating marks a date that was inferred from the current day and may be
	// rolled forward if the resulting instant is already past.
	floating bool
}

// parseDatePhrase resolves the date portion of an absolute expression.
func parseDatePhrase(tokens []string, now time.Time, loc *time.Location) (datePhrase, int, bool) {
	if len(tokens) == 0 {
		return datePhrase{}, 0, false
	}

	head := strings.ToLower(strings.Trim(tokens[0], ","))
	y, m, d := now.Date()

	switch head {
	case "today":
		return datePhrase{year: y, month: m, day: d, defaultHour: DefaultHour, floating: true}, 1, true

	case "tonight":
		return datePhrase{year: y, month: m, day: d, defaultHour: eveningHour, floating: true}, 1, true

	case "tomorrow":
		t := now.AddDate(0, 0, 1)
		ty, tm, td := t.Date()
		return datePhrase{year: ty, month: tm, day: td, defaultHour: DefaultHour}, 1, true

	case "next":
		return parseNextPhrase(tokens, now)
	}

	// A bare weekday: the soonest one, which may be today if its time is still
	// ahead. nextWeekday handles the roll-forward.
	if wd, ok := parseWeekday(head); ok {
		date := nextWeekdayDate(now, wd, false)
		dy, dm, dd := date.Date()
		return datePhrase{year: dy, month: dm, day: dd, defaultHour: DefaultHour, floating: true}, 1, true
	}

	return parseCalendarDate(tokens, now, loc)
}

// parseNextPhrase handles "next Monday", "next week" and "next month".
func parseNextPhrase(tokens []string, now time.Time) (datePhrase, int, bool) {
	if len(tokens) < 2 {
		return datePhrase{}, 0, false
	}

	switch strings.ToLower(strings.Trim(tokens[1], ",")) {
	case "week":
		t := now.AddDate(0, 0, 7)
		y, m, d := t.Date()
		return datePhrase{year: y, month: m, day: d, defaultHour: DefaultHour}, 2, true
	case "month":
		t := now.AddDate(0, 1, 0)
		y, m, d := t.Date()
		return datePhrase{year: y, month: m, day: d, defaultHour: DefaultHour}, 2, true
	}

	wd, ok := parseWeekday(strings.ToLower(strings.Trim(tokens[1], ",")))
	if !ok {
		return datePhrase{}, 0, false
	}
	// "next Monday" never means today, even when today is a Monday.
	date := nextWeekdayDate(now, wd, true)
	y, m, d := date.Date()
	return datePhrase{year: y, month: m, day: d, defaultHour: DefaultHour}, 2, true
}

// parseCalendarDate handles "2026-11-03", "Nov 3", "Nov 3 2026" and "3 Nov".
//
// Purely numeric slash dates such as 11/03 are rejected rather than guessed at,
// because they mean different days either side of the Atlantic.
func parseCalendarDate(tokens []string, now time.Time, _ *time.Location) (datePhrase, int, bool) {
	head := strings.Trim(tokens[0], ",")

	// ISO: unambiguous everywhere, so it is the documented form.
	if y, m, d, ok := parseISODate(head); ok {
		return datePhrase{year: y, month: m, day: d, defaultHour: DefaultHour}, 1, true
	}

	// "Nov 3" / "November 3rd" / "Nov 3 2026"
	if month, ok := parseMonthName(head); ok && len(tokens) >= 2 {
		day, ok := parseDayOfMonth(tokens[1])
		if !ok {
			return datePhrase{}, 0, false
		}
		year, used := now.Year(), 2
		if y, ok := parseYear(tokens, 2); ok {
			year, used = y, 3
		} else if day < now.Day() && month == now.Month() || month < now.Month() {
			// A month and day already behind us means next year.
			year++
		}
		if day > daysInMonth(year, month) {
			return datePhrase{}, 0, false
		}
		return datePhrase{year: year, month: month, day: day, defaultHour: DefaultHour}, used, true
	}

	// "3 Nov" / "3 November 2026"
	if day, ok := parseDayOfMonth(head); ok && len(tokens) >= 2 {
		month, ok := parseMonthName(tokens[1])
		if !ok {
			return datePhrase{}, 0, false
		}
		year, used := now.Year(), 2
		if y, ok := parseYear(tokens, 2); ok {
			year, used = y, 3
		} else if month < now.Month() || (month == now.Month() && day < now.Day()) {
			year++
		}
		if day > daysInMonth(year, month) {
			return datePhrase{}, 0, false
		}
		return datePhrase{year: year, month: month, day: day, defaultHour: DefaultHour}, used, true
	}

	return datePhrase{}, 0, false
}

// parseISODate reads YYYY-MM-DD, rejecting dates that do not exist such as
// 2026-02-30.
func parseISODate(s string) (int, time.Month, int, bool) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 || len(parts[0]) != 4 {
		return 0, 0, 0, false
	}
	y, err1 := strconv.Atoi(parts[0])
	mo, err2 := strconv.Atoi(parts[1])
	d, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	if mo < 1 || mo > 12 || d < 1 || d > daysInMonth(y, time.Month(mo)) {
		return 0, 0, 0, false
	}
	return y, time.Month(mo), d, true
}

var monthNames = map[string]time.Month{
	"jan": time.January, "january": time.January,
	"feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April,
	"may": time.May,
	"jun": time.June, "june": time.June,
	"jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "sept": time.September, "september": time.September,
	"oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November,
	"dec": time.December, "december": time.December,
}

func parseMonthName(token string) (time.Month, bool) {
	m, ok := monthNames[strings.ToLower(strings.Trim(token, ".,"))]
	return m, ok
}

// parseDayOfMonth reads "3", "3rd" or "21st".
func parseDayOfMonth(token string) (int, bool) {
	s := strings.ToLower(strings.Trim(token, ".,"))
	s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s, "st"), "nd"), "rd"), "th")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 31 {
		return 0, false
	}
	return n, true
}

// parseYear reads a four-digit year at the given index, if there is one.
func parseYear(tokens []string, index int) (int, bool) {
	if len(tokens) <= index {
		return 0, false
	}
	s := strings.Trim(tokens[index], ",")
	if len(s) != 4 {
		return 0, false
	}
	y, err := strconv.Atoi(s)
	if err != nil || y < 1970 || y > 9999 {
		return 0, false
	}
	return y, true
}

var weekdayNameLookup = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "weds": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

func parseWeekday(token string) (time.Weekday, bool) {
	wd, ok := weekdayNameLookup[strings.ToLower(strings.Trim(token, ".,"))]
	return wd, ok
}

// nextWeekdayDate returns the date of the next given weekday. When skipToday is
// false and today already is that weekday, today is returned, which lets
// "Monday at 6pm" mean this evening.
func nextWeekdayDate(now time.Time, wd time.Weekday, skipToday bool) time.Time {
	delta := (int(wd) - int(now.Weekday()) + 7) % 7
	if delta == 0 && skipToday {
		delta = 7
	}
	return now.AddDate(0, 0, delta)
}

// nextWallClock returns the next instant at the given wall-clock time, today if
// it is still ahead and tomorrow otherwise.
func nextWallClock(now time.Time, loc *time.Location, hour, minute int) time.Time {
	y, m, d := now.In(loc).Date()
	candidate := atWallClock(loc, y, m, d, hour, minute)
	if !candidate.After(now) {
		candidate = atWallClock(loc, y, m, d+1, hour, minute)
	}
	return candidate
}

// parseAtClause reads an optional "at <time>" or a bare time of day from the
// front of tokens.
func parseAtClause(tokens []string) (hour, minute, consumed int, ok bool) {
	if len(tokens) == 0 {
		return 0, 0, 0, false
	}

	offset := 0
	if strings.EqualFold(tokens[0], "at") || strings.EqualFold(tokens[0], "@") {
		offset = 1
		if len(tokens) == 1 {
			return 0, 0, 0, false
		}
	}

	// "9 am" split across two tokens.
	if len(tokens) > offset+1 {
		if h, m, joined := parseTimeOfDay(tokens[offset] + tokens[offset+1]); joined {
			return h, m, offset + 2, true
		}
	}

	h, m, ok := parseTimeOfDay(tokens[offset])
	if !ok {
		return 0, 0, 0, false
	}
	return h, m, offset + 1, true
}

// parseTimeOfDay reads "9am", "9:30am", "21:00", "noon" or "midnight".
func parseTimeOfDay(token string) (hour, minute int, ok bool) {
	s := strings.ToLower(strings.Trim(token, ".,"))

	switch s {
	case "noon", "midday":
		return 12, 0, true
	case "midnight":
		return 0, 0, true
	}

	meridiem := ""
	switch {
	case strings.HasSuffix(s, "am"):
		meridiem, s = "am", strings.TrimSpace(strings.TrimSuffix(s, "am"))
	case strings.HasSuffix(s, "pm"):
		meridiem, s = "pm", strings.TrimSpace(strings.TrimSuffix(s, "pm"))
	}
	if s == "" {
		return 0, 0, false
	}

	hourPart, minutePart := s, "0"
	if h, m, found := strings.Cut(s, ":"); found {
		hourPart, minutePart = h, m
	}

	h, err := strconv.Atoi(hourPart)
	if err != nil {
		return 0, 0, false
	}
	m, err := strconv.Atoi(minutePart)
	if err != nil || m < 0 || m > 59 {
		return 0, 0, false
	}

	switch meridiem {
	case "am":
		if h < 1 || h > 12 {
			return 0, 0, false
		}
		if h == 12 {
			h = 0
		}
	case "pm":
		if h < 1 || h > 12 {
			return 0, 0, false
		}
		if h != 12 {
			h += 12
		}
	default:
		// A bare number is only a time when it is written as a 24-hour clock;
		// "in 3 days" must not have its 3 read as 03:00.
		if !strings.Contains(s, ":") || h < 0 || h > 23 {
			return 0, 0, false
		}
	}

	return h, m, true
}

// ---------------------------------------------------------------------------
// Recurring times: "every weekday at 9am", "every 2 hours", "every 15th"
// ---------------------------------------------------------------------------

// parseRecurring handles everything introduced by "every" or "each".
func parseRecurring(tokens []string, now time.Time, loc *time.Location) (timeExpr, int, bool) {
	if len(tokens) < 2 {
		return timeExpr{}, 0, false
	}

	cursor := 1
	interval := 1

	// "every other Monday" is twice the base interval.
	if strings.EqualFold(tokens[cursor], "other") {
		interval = 2
		cursor++
		if cursor >= len(tokens) {
			return timeExpr{}, 0, false
		}
	}

	// "every 3 days", "every 2 hours": a count in front of the unit.
	if n, err := strconv.ParseInt(strings.Trim(tokens[cursor], ","), 10, 64); err == nil {
		if n < 1 {
			return timeExpr{}, 0, false
		}
		interval = int(n)
		cursor++
		if cursor >= len(tokens) {
			return timeExpr{}, 0, false
		}
	}

	rec, used, ok := parseRecurrenceUnit(tokens[cursor:], interval, now)
	if !ok {
		return timeExpr{}, 0, false
	}
	cursor += used

	// Monthly rules may name the day: "every month on the 15th".
	if rec.Type == RecurrenceMonthly {
		if day, used := parseOnTheNth(tokens[cursor:]); used > 0 {
			rec.DayOfMonth = day
			cursor += used
		}
	}

	// A wall-clock rule takes its hour from a trailing "at" clause, defaulting
	// to the morning so that "every Monday" is not a midnight reminder.
	if rec.Type != RecurrenceInterval {
		if hour, minute, used, found := parseAtClause(tokens[cursor:]); found {
			rec.Hour, rec.Minute = hour, minute
			cursor += used
		} else {
			rec.Hour, rec.Minute = DefaultHour, 0
		}
	}

	if err := rec.Validate(); err != nil {
		return timeExpr{}, 0, false
	}

	var due time.Time
	if rec.Type == RecurrenceInterval {
		due = now.Add(time.Duration(rec.EverySeconds) * time.Second)
	} else {
		due = rec.Next(time.Time{}, now, loc)
	}
	if due.IsZero() {
		return timeExpr{}, 0, false
	}

	return timeExpr{due: due, recurrence: rec}, cursor, true
}

// parseRecurrenceUnit reads the period of a recurring rule: a unit name, a
// weekday list, or one of the "weekday"/"weekend" shorthands.
func parseRecurrenceUnit(tokens []string, interval int, now time.Time) (*Recurrence, int, bool) {
	if len(tokens) == 0 {
		return nil, 0, false
	}

	switch strings.ToLower(strings.Trim(tokens[0], ",")) {
	case "day", "days", "daily":
		return &Recurrence{Type: RecurrenceDaily, Interval: interval}, 1, true

	case "week", "weeks", "weekly":
		// Anchored to the weekday the rule was created on.
		return &Recurrence{
			Type:     RecurrenceWeekly,
			Interval: interval,
			Weekdays: []time.Weekday{now.Weekday()},
		}, 1, true

	case "month", "months", "monthly":
		return &Recurrence{
			Type:       RecurrenceMonthly,
			Interval:   interval,
			DayOfMonth: now.Day(),
		}, 1, true

	case "weekday", "weekdays":
		return &Recurrence{
			Type:     RecurrenceWeekly,
			Interval: interval,
			Weekdays: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		}, 1, true

	case "weekend", "weekends":
		return &Recurrence{
			Type:     RecurrenceWeekly,
			Interval: interval,
			Weekdays: []time.Weekday{time.Saturday, time.Sunday},
		}, 1, true

	case "hour", "hours", "minute", "minutes", "second", "seconds", "h", "m", "s", "min", "mins", "hr", "hrs":
		unit, ok := normalizeUnit(tokens[0])
		if !ok {
			return nil, 0, false
		}
		return &Recurrence{
			Type:         RecurrenceInterval,
			EverySeconds: int64(interval) * unitSeconds[unit],
		}, 1, true
	}

	if weekdays, used, ok := parseWeekdayList(tokens); ok {
		return &Recurrence{Type: RecurrenceWeekly, Interval: interval, Weekdays: weekdays}, used, true
	}

	return nil, 0, false
}

// parseWeekdayList reads "Monday", "mon,fri" or "Monday, Wednesday and Friday"
// into a sorted, deduplicated set of weekdays.
func parseWeekdayList(tokens []string) ([]time.Weekday, int, bool) {
	var (
		days     []time.Weekday
		consumed int
	)

	for consumed < len(tokens) {
		token := tokens[consumed]

		// Joining words are only skipped when a weekday actually follows, so
		// that "every Monday and then some" does not swallow the text.
		if isListJoiner(token) {
			if consumed+1 >= len(tokens) {
				break
			}
			if _, ok := parseWeekday(strings.Split(tokens[consumed+1], ",")[0]); !ok {
				break
			}
			consumed++
			continue
		}

		// "mon,fri" arrives as one token.
		matched := false
		for part := range strings.SplitSeq(token, ",") {
			if part == "" {
				continue
			}
			wd, ok := parseWeekday(part)
			if !ok {
				matched = false
				break
			}
			days = append(days, wd)
			matched = true
		}
		if !matched {
			break
		}
		consumed++
	}

	if len(days) == 0 {
		return nil, 0, false
	}

	slices.Sort(days)
	return slices.Compact(days), consumed, true
}

func isListJoiner(token string) bool {
	switch strings.ToLower(strings.Trim(token, ",")) {
	case "and", "&", "":
		return true
	}
	return false
}

// parseOnTheNth reads the "on the 15th" suffix of a monthly rule.
func parseOnTheNth(tokens []string) (day int, consumed int) {
	index := 0
	if index < len(tokens) && strings.EqualFold(tokens[index], "on") {
		index++
	}
	if index < len(tokens) && strings.EqualFold(tokens[index], "the") {
		index++
	}
	if index == 0 || index >= len(tokens) {
		return 0, 0
	}
	d, ok := parseDayOfMonth(tokens[index])
	if !ok {
		return 0, 0
	}
	return d, index + 1
}

// slashDatePattern matches the numeric slash dates that parseCalendarDate
// deliberately refuses, such as 11/03 or 11/03/2026.
var slashDatePattern = regexp.MustCompile(`^\d{1,4}/\d{1,2}(/\d{2,4})?$`)

// findSlashDate returns the first token that looks like an ambiguous numeric
// date, so the caller can explain the refusal rather than report a generic
// parse failure.
func findSlashDate(tokens []string) string {
	for _, token := range tokens {
		trimmed := strings.Trim(token, ".,")
		if slashDatePattern.MatchString(trimmed) {
			return trimmed
		}
	}
	return ""
}
