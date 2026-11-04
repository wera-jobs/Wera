package config

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Schedule is a list of daily run times in one time zone, e.g.
// RUN_SCHEDULE="09:00,13:00" with RUN_TIMEZONE="America/Chicago", on the
// days RUN_DAYS names (every day when it is empty).
type Schedule struct {
	Times    []time.Duration // offsets from local midnight, sorted
	Location *time.Location
	Days     *[7]bool // indexed by time.Weekday; nil means every day
}

// ParseSchedule parses a comma-separated list of HH:MM times in loc.
// An empty spec returns nil (no schedule: the worker uses RUN_INTERVAL).
func ParseSchedule(spec string, loc *time.Location) (*Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	s := &Schedule{Location: loc}
	seen := map[time.Duration]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		t, err := time.Parse("15:04", part)
		if err != nil {
			return nil, fmt.Errorf("bad time %q (want HH:MM, 24-hour)", part)
		}
		off := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
		if !seen[off] {
			seen[off] = true
			s.Times = append(s.Times, off)
		}
	}
	sort.Slice(s.Times, func(i, j int) bool { return s.Times[i] < s.Times[j] })
	return s, nil
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseDays parses the days to run on, e.g. "Mon-Fri" or "Mon,Wed,Fri"
// (three-letter names, any case; a range may wrap, like "Fri-Mon"). An
// empty spec returns nil: every day.
func ParseDays(spec string) (*[7]bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	day := func(s string) (time.Weekday, error) {
		d, ok := weekdays[strings.ToLower(strings.TrimSpace(s))]
		if !ok {
			return 0, fmt.Errorf("bad day %q (want Mon, Tue, ... Sun)", strings.TrimSpace(s))
		}
		return d, nil
	}
	var days [7]bool
	for _, part := range strings.Split(spec, ",") {
		from, to, isRange := strings.Cut(part, "-")
		first, err := day(from)
		if err != nil {
			return nil, err
		}
		last := first
		if isRange {
			if last, err = day(to); err != nil {
				return nil, err
			}
		}
		for d := first; ; d = (d + 1) % 7 {
			days[d] = true
			if d == last {
				break
			}
		}
	}
	return &days, nil
}

// Next returns the first scheduled time strictly after now. Wall-clock
// times are resolved per day, so DST changes keep runs at 09:00 local.
func (s *Schedule) Next(now time.Time) time.Time {
	local := now.In(s.Location)
	for day := range 8 {
		y, m, d := local.AddDate(0, 0, day).Date()
		if s.Days != nil && !s.Days[time.Date(y, m, d, 12, 0, 0, 0, s.Location).Weekday()] {
			continue
		}
		for _, off := range s.Times {
			h, minute := int(off/time.Hour), int(off%time.Hour/time.Minute)
			t := time.Date(y, m, d, h, minute, 0, 0, s.Location)
			if t.After(now) {
				return t
			}
		}
	}
	// Unreachable with at least one time and one day, but stay safe.
	return now.Add(24 * time.Hour)
}

// String renders the schedule like "09:00, 13:00 America/Chicago", with
// its days when it skips some ("11:00 Mon-Fri America/Chicago").
func (s *Schedule) String() string {
	parts := make([]string, len(s.Times))
	for i, off := range s.Times {
		parts[i] = fmt.Sprintf("%02d:%02d", int(off/time.Hour), int(off%time.Hour/time.Minute))
	}
	out := strings.Join(parts, ", ")
	if s.Days != nil {
		var names []string
		for d := time.Sunday; d <= time.Saturday; d++ {
			if s.Days[d] {
				names = append(names, d.String()[:3])
			}
		}
		out += " " + strings.Join(names, ",")
	}
	return out + " " + s.Location.String()
}
