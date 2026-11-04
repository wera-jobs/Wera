package config

import (
	"testing"
	"time"
)

func TestScheduleNext(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSchedule("13:00, 09:00,11:00,09:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Times) != 3 {
		t.Fatalf("want 3 deduped times, got %v", s.Times)
	}
	at := func(y int, m time.Month, d, h, minute int) time.Time {
		return time.Date(y, m, d, h, minute, 0, 0, loc)
	}
	tests := []struct {
		now, want time.Time
	}{
		{at(2026, 10, 9, 8, 0), at(2026, 10, 9, 9, 0)},
		{at(2026, 10, 9, 9, 0), at(2026, 10, 9, 11, 0)}, // strictly after
		{at(2026, 10, 9, 12, 59), at(2026, 10, 9, 13, 0)},
		{at(2026, 10, 9, 22, 0), at(2026, 10, 10, 9, 0)},
		// DST ends 2026-11-01 in Chicago; the next run is still 09:00 local.
		{at(2026, 10, 31, 20, 0), at(2026, 11, 1, 9, 0)},
	}
	for _, tc := range tests {
		if got := s.Next(tc.now); !got.Equal(tc.want) {
			t.Errorf("Next(%s) = %s, want %s", tc.now, got, tc.want)
		}
	}
}

func TestScheduleWeekdays(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSchedule("16:30,11:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	if s.Days, err = ParseDays("mon-FRI"); err != nil {
		t.Fatal(err)
	}
	at := func(m time.Month, d, h, minute int) time.Time {
		return time.Date(2026, m, d, h, minute, 0, 0, loc)
	}
	tests := []struct {
		now, want time.Time
	}{
		{at(11, 4, 9, 0), at(11, 4, 11, 0)},   // Wednesday morning
		{at(11, 4, 11, 0), at(11, 4, 16, 30)}, // strictly after
		{at(11, 6, 17, 0), at(11, 9, 11, 0)},  // Friday evening: Monday
		{at(11, 7, 10, 0), at(11, 9, 11, 0)},  // Saturday
		{at(11, 8, 23, 59), at(11, 9, 11, 0)}, // Sunday night
	}
	for _, tc := range tests {
		if got := s.Next(tc.now); !got.Equal(tc.want) {
			t.Errorf("Next(%s) = %s, want %s", tc.now, got, tc.want)
		}
	}
	if got, want := s.String(), "11:00, 16:30 Mon,Tue,Wed,Thu,Fri America/Chicago"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParseDays(t *testing.T) {
	if d, err := ParseDays(" "); d != nil || err != nil {
		t.Fatalf("empty: got %v, %v", d, err)
	}
	d, err := ParseDays("Fri-Mon, Wed")
	if err != nil {
		t.Fatal(err)
	}
	want := [7]bool{true, true, false, true, false, true, true} // Sun..Sat
	if *d != want {
		t.Errorf("Fri-Mon, Wed = %v, want %v", *d, want)
	}
	for _, bad := range []string{"Monday", "Mon-", "Mon,,Fri", "weekdays"} {
		if _, err := ParseDays(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestParseScheduleErrors(t *testing.T) {
	if s, err := ParseSchedule("", time.UTC); s != nil || err != nil {
		t.Fatalf("empty spec: got %v, %v", s, err)
	}
	for _, bad := range []string{"9am", "25:00", "09:00,"} {
		if _, err := ParseSchedule(bad, time.UTC); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
