package clock

import (
	"testing"
	"time"
)

func TestBeijingBoundaryPreservesInstant(t *testing.T) {
	original := time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)
	local := In(original)
	if !local.Equal(original) || local.Day() != 2 || local.Hour() != 0 {
		t.Fatalf("incorrect conversion: %s", local)
	}
	if local.Format(time.RFC3339) != "2026-11-02T00:00:00+08:00" {
		t.Fatalf("wrong output timezone: %s", local)
	}
}
