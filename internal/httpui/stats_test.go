package httpui

import (
	"testing"
	"time"

	"vtunnel/internal/requestlog"
)

func TestComputeStats(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 30, 0, time.UTC)
	created := now.Add(-5 * time.Minute)
	logs := []requestlog.Entry{
		{Time: now.Add(-25 * time.Second), Status: 200, Bytes: 100, Duration: 10 * time.Millisecond},
		{Time: now.Add(-25 * time.Second), Status: 200, Bytes: 200, Duration: 20 * time.Millisecond},
		{Time: now.Add(-2 * time.Second), Status: 404, Bytes: 50, Duration: 5 * time.Millisecond},
		{Time: now.Add(-1 * time.Second), Status: 500, Bytes: 0, Duration: 200 * time.Millisecond},
	}

	s := computeStats(logs, created, now, 30, time.Second)

	if s.Total != 4 {
		t.Fatalf("Total = %d, want 4", s.Total)
	}
	if s.Errors != 2 {
		t.Fatalf("Errors = %d, want 2 (one 4xx, one 5xx)", s.Errors)
	}
	if s.ClassCounts[2] != 2 || s.ClassCounts[4] != 1 || s.ClassCounts[5] != 1 {
		t.Fatalf("ClassCounts = %v, want 2xx:2 4xx:1 5xx:1", s.ClassCounts)
	}
	if s.BytesSent != 350 {
		t.Fatalf("BytesSent = %d, want 350", s.BytesSent)
	}
	if s.Uptime != 5*time.Minute {
		t.Fatalf("Uptime = %s, want 5m", s.Uptime)
	}
	if len(s.Buckets) != 30 {
		t.Fatalf("Buckets len = %d, want 30", len(s.Buckets))
	}
	// Two requests landed ~25s ago (bucket 5), two in the last 2s (buckets 27/28).
	if s.Buckets[5] != 2 {
		t.Fatalf("Buckets[5] = %d, want 2", s.Buckets[5])
	}
	if s.PeakBucket != 2 {
		t.Fatalf("PeakBucket = %d, want 2", s.PeakBucket)
	}
	// p95 should surface the slow 200ms request; p50 should be in the low-ms range.
	if s.P95 != 200*time.Millisecond {
		t.Fatalf("P95 = %s, want 200ms", s.P95)
	}
	if s.P50 > 20*time.Millisecond {
		t.Fatalf("P50 = %s, want <= 20ms", s.P50)
	}
	if got := s.errorRate(); got < 0.49 || got > 0.51 {
		t.Fatalf("errorRate = %.2f, want ~0.5", got)
	}
}

func TestComputeStatsEmpty(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	s := computeStats(nil, time.Time{}, now, 10, time.Second)
	if s.Total != 0 || s.Errors != 0 || s.P50 != 0 || s.P95 != 0 || s.PeakBucket != 0 {
		t.Fatalf("empty stats not zeroed: %+v", s)
	}
	if s.Uptime != 0 {
		t.Fatalf("Uptime = %s, want 0 for zero createdAt", s.Uptime)
	}
	if len(s.Buckets) != 10 {
		t.Fatalf("Buckets len = %d, want 10", len(s.Buckets))
	}
}
