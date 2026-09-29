package timex

import (
	"testing"
	"time"
)

func TestStartOfDay_UTC(t *testing.T) {
	in := time.Date(2026, 5, 14, 15, 30, 45, 123, time.UTC)
	got := StartOfDay(in, time.UTC)
	want := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEndOfDay_LastNanosecond(t *testing.T) {
	in := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	got := EndOfDay(in, time.UTC)
	want := time.Date(2026, 5, 14, 23, 59, 59, 999999999, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDay(t *testing.T) {
	got, err := ParseDay("2026-05-14", time.UTC)
	if err != nil {
		t.Fatalf("ParseDay: %v", err)
	}
	want := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDay_NilLocDefaultsUTC(t *testing.T) {
	got, err := ParseDay("2026-05-14", nil)
	if err != nil {
		t.Fatalf("ParseDay: %v", err)
	}
	if got.Location() != time.UTC {
		t.Errorf("loc = %v, want UTC", got.Location())
	}
}

func TestDaysBetween(t *testing.T) {
	a := time.Date(2026, 5, 14, 23, 0, 0, 0, time.UTC)
	b := time.Date(2026, 5, 16, 1, 0, 0, 0, time.UTC)
	if got := DaysBetween(a, b, time.UTC); got != 2 {
		t.Errorf("got %d, want 2", got)
	}
	if got := DaysBetween(b, a, time.UTC); got != -2 {
		t.Errorf("reverse got %d, want -2", got)
	}
	if got := DaysBetween(a, a, time.UTC); got != 0 {
		t.Errorf("same day got %d, want 0", got)
	}
}

func TestToday_ParsesBack(t *testing.T) {
	s := Today()
	if _, err := ParseDay(s, time.UTC); err != nil {
		t.Errorf("Today() = %q, not parseable: %v", s, err)
	}
}

func TestFormatMMSS(t *testing.T) {
	cases := []struct {
		secs int
		want string
	}{
		{0, "00:00"},
		{5, "00:05"},
		{59, "00:59"},
		{60, "01:00"},
		{90, "01:30"},
		{600, "10:00"},
		{900, "15:00"},
		{3665, "61:05"}, // no hour rollover — minutes overflow naturally
		{-5, "00:00"},   // negative clamps
	}
	for _, tc := range cases {
		got := FormatMMSS(tc.secs)
		if got != tc.want {
			t.Errorf("FormatMMSS(%d) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}
