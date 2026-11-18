package awsapi

import (
	"math"
	"testing"
	"time"
)

func TestTrafficWindowStartsAtPreviousUTCMonth(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	// 2026-03-01 05:00 in Shanghai is still 2026-02-28 in UTC.
	start, end := trafficWindow(time.Date(2026, 3, 1, 5, 0, 30, 0, shanghai))
	if !start.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 2, 28, 21, 0, 0, 0, time.UTC)) {
		t.Fatal(start, end)
	}
	if day := utcDay(time.Date(2026, 3, 1, 5, 0, 0, 0, shanghai)); !day.Equal(time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(day)
	}
}

func TestBytesValueRejectsImpossibleMetrics(t *testing.T) {
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1), 1 << 63} {
		if _, err := bytesValue(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	if v, err := bytesValue(1023.6); err != nil || v != 1024 {
		t.Fatal(v, err)
	}
}

func TestParseQueryID(t *testing.T) {
	cases := map[string]struct {
		index   int
		inbound bool
		ok      bool
	}{"m0in": {0, true, true}, "m12out": {12, false, true}, "m3up": {0, false, false}, "m250in": {0, false, false}, "x1in": {0, false, false}}
	for id, want := range cases {
		index, inbound, ok := parseQueryID(id, 250)
		if ok != want.ok || ok && (index != want.index || inbound != want.inbound) {
			t.Fatal(id, index, inbound, ok)
		}
	}
}
