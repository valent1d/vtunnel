package httpui

import (
	"math"
	"sort"
	"time"

	"vtunnel/internal/requestlog"
)

// Stats is the live dashboard summary computed from a route's recent request
// logs. Everything here is derived from vtunnel's own proxy logs, so it stays
// accurate in real time (no dependency on delayed external analytics).
type Stats struct {
	Total       int           // requests in the available log window
	Errors      int           // responses with status >= 400
	ClassCounts [6]int        // counts indexed by status class: [_, 1xx, 2xx, 3xx, 4xx, 5xx]
	P50         time.Duration // median latency
	P95         time.Duration // 95th percentile latency
	BytesSent   int64         // total response bytes
	Uptime      time.Duration // since the route was opened
	Buckets     []int         // request counts per time bucket, oldest -> newest (for the chart)
	BucketDur   time.Duration // width of each bucket
	PeakBucket  int           // highest single-bucket count (chart scaling)
}

// computeStats summarizes logs for a single route over the trailing window
// bucketCount*bucketDur ending at now. logs may arrive in any order.
func computeStats(logs []requestlog.Entry, createdAt, now time.Time, bucketCount int, bucketDur time.Duration) Stats {
	stats := Stats{
		Buckets:   make([]int, bucketCount),
		BucketDur: bucketDur,
	}
	if !createdAt.IsZero() {
		if up := now.Sub(createdAt); up > 0 {
			stats.Uptime = up
		}
	}

	durations := make([]time.Duration, 0, len(logs))
	windowStart := now.Add(-time.Duration(bucketCount) * bucketDur)
	for _, entry := range logs {
		stats.Total++
		stats.BytesSent += entry.Bytes
		if class := entry.Status / 100; class >= 1 && class <= 5 {
			stats.ClassCounts[class]++
		}
		if entry.Status >= 400 {
			stats.Errors++
		}
		durations = append(durations, entry.Duration)

		// Place the request in its time bucket (oldest bucket = index 0).
		if bucketCount > 0 && bucketDur > 0 && !entry.Time.Before(windowStart) && !entry.Time.After(now) {
			idx := int(entry.Time.Sub(windowStart) / bucketDur)
			if idx >= bucketCount {
				idx = bucketCount - 1
			}
			if idx >= 0 {
				stats.Buckets[idx]++
				if stats.Buckets[idx] > stats.PeakBucket {
					stats.PeakBucket = stats.Buckets[idx]
				}
			}
		}
	}

	stats.P50 = percentile(durations, 0.50)
	stats.P95 = percentile(durations, 0.95)
	return stats
}

// percentile returns the p-th percentile (0..1) of the durations using the
// nearest-rank method, or 0 if empty. Nearest-rank surfaces tail latency well
// even with few samples (e.g. p95 of a handful of requests reflects the slow one).
func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(p * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// errorRate returns the share of errored requests in [0,1].
func (s Stats) errorRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Errors) / float64(s.Total)
}
