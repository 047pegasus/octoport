package db

import (
	"testing"
	"time"
)

// The old code anchored bounds to the current day-of-month
// (time.Now().AddDate(0, i, 0) formatted with day precision), so any run not
// on the 1st produced misaligned partitions. These cases pin the fixed
// month-boundary behavior, including the exact scenario from production
// (a mid-September run creating events_202612).
func TestEventPartitionRange(t *testing.T) {
	cases := []struct {
		desc     string
		now      time.Time
		offset   int
		wantName string
		wantFrom string
		wantTo   string
	}{
		{"first of month", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 0,
			"events_202610", "2026-10-01", "2026-11-01"},
		{"mid-month run still yields month boundaries", time.Date(2026, 9, 15, 17, 28, 10, 0, time.UTC), 3,
			"events_202612", "2026-12-01", "2027-01-01"},
		{"production case: Jan 2027 from an Oct 1 run", time.Date(2026, 10, 1, 17, 28, 10, 0, time.UTC), 3,
			"events_202701", "2027-01-01", "2027-02-01"},
		{"december rolls over to january", time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC), 1,
			"events_202701", "2027-01-01", "2027-02-01"},
		{"jan 31 never skips february (old AddDate overflow)", time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC), 1,
			"events_202602", "2026-02-01", "2026-03-01"},
		{"leap day stays in february", time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC), 0,
			"events_202402", "2024-02-01", "2024-03-01"},
		{"non-UTC input is normalized to UTC first", time.Date(2026, 11, 1, 0, 30, 0, 0, time.FixedZone("UTC+2", 2*3600)), 0,
			"events_202610", "2026-10-01", "2026-11-01"},
	}
	for _, c := range cases {
		name, from, to := eventPartitionRange(c.now, c.offset)
		if name != c.wantName || from != c.wantFrom || to != c.wantTo {
			t.Errorf("%s: got (%s, %s, %s), want (%s, %s, %s)",
				c.desc, name, from, to, c.wantName, c.wantFrom, c.wantTo)
		}
	}
}

func TestCanonicalEventRange(t *testing.T) {
	from, to, ok := canonicalEventRange("events_202612")
	if !ok || !from.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) ||
		!to.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("canonicalEventRange(events_202612) = %v, %v, %v", from, to, ok)
	}
	for _, bad := range []string{"events_202613", "events_202600", "events_123", "events_default", "tunnels", ""} {
		if _, _, ok := canonicalEventRange(bad); ok {
			t.Errorf("canonicalEventRange(%q) unexpectedly ok", bad)
		}
	}
}

func TestParsePartitionBound(t *testing.T) {
	from, to, ok := parsePartitionBound(
		`FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00')`)
	if !ok || !from.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) ||
		!to.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("parsePartitionBound typical = %v, %v, %v", from, to, ok)
	}

	// The production squatter: a "December" partition sprawling into January.
	from, to, ok = parsePartitionBound(
		`FOR VALUES FROM ('2026-12-15 00:00:00+00') TO ('2027-01-15 00:00:00+00')`)
	if !ok || !from.Equal(time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)) ||
		!to.Equal(time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("parsePartitionBound squatter = %v, %v, %v", from, to, ok)
	}

	for _, expr := range []string{"DEFAULT", "", "FOR VALUES WITH (MODULUS 4, REMAINDER 0)"} {
		if _, _, ok := parsePartitionBound(expr); ok {
			t.Errorf("parsePartitionBound(%q) unexpectedly ok", expr)
		}
	}
}
