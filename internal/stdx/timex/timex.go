// Package timex centralizes date-string formats and time helpers shared with
// mobile/web clients. Stick to stdlib time — third-party date libraries pay
// back nothing vs. the readability cost.
package timex

import (
	"fmt"
	"time"
)

// Format constants — share these with the React/mobile client. Adding a new
// format here is a wire commitment.
const (
	// DateFormat — ISO 8601 calendar date, e.g. "2026-05-14".
	DateFormat = "2006-01-02"

	// DateTimeFormat — RFC 3339, the canonical wire format for timestamps.
	DateTimeFormat = time.RFC3339

	// DateTimeFormatMS — RFC 3339 with millisecond precision, for clients
	// that pin to JS Date semantics.
	DateTimeFormatMS = "2006-01-02T15:04:05.000Z07:00"

	// MonthDayFormat — e.g. "01-14" for short labels.
	MonthDayFormat = "01-02"

	// YearMonthFormat — e.g. "2026-05".
	YearMonthFormat = "2006-01"
)

// Now returns the current UTC time. Tests can override by injecting a clock
// via constructor options at the call site — never replace this function
// itself in tests, mutating package state breaks parallelism.
func Now() time.Time { return time.Now().UTC() }

// Today returns the current date as a DateFormat string in UTC.
func Today() string { return Now().Format(DateFormat) }

// StartOfDay returns midnight of t in loc. If loc is nil, time.UTC is used.
func StartOfDay(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// EndOfDay returns the last representable nanosecond before midnight of the
// NEXT day in loc — i.e. 23:59:59.999999999.
func EndOfDay(t time.Time, loc *time.Location) time.Time {
	return StartOfDay(t, loc).Add(24*time.Hour - time.Nanosecond)
}

// ParseDay parses a DateFormat string in loc. If loc is nil, time.UTC is used.
func ParseDay(s string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	return time.ParseInLocation(DateFormat, s, loc)
}

// DaysBetween returns floor((b-a) / 24h) using calendar days in loc (not
// 24-hour blocks — DST safe). Negative when b is before a.
func DaysBetween(a, b time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	return int(StartOfDay(b, loc).Sub(StartOfDay(a, loc)) / (24 * time.Hour))
}

// FormatMMSS formats a seconds count as a zero-padded `MM:SS` string.
// Negative values clamp to 0; values >= 60 minutes still render as e.g.
// `61:05` (no hour rollover). Used for resend / lockout countdowns on
// the wire and in logs.
//
//	FormatMMSS(90)   // "01:30"
//	FormatMMSS(5)    // "00:05"
//	FormatMMSS(0)    // "00:00"
//	FormatMMSS(3665) // "61:05"
func FormatMMSS(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}
