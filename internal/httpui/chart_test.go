package httpui

import "testing"

func TestSparkline(t *testing.T) {
	if got := sparkline(nil); got != "" {
		t.Fatalf("empty = %q, want \"\"", got)
	}
	// All zero renders a flat baseline (lowest block).
	if got := sparkline([]int{0, 0, 0}); got != "▁▁▁" {
		t.Fatalf("all-zero = %q, want ▁▁▁", got)
	}
	// Min and max scale to the lowest and highest blocks.
	if got := sparkline([]int{0, 8}); got != "▁█" {
		t.Fatalf("[0,8] = %q, want ▁█", got)
	}
	// A monotonic ramp produces non-decreasing block heights ending at the peak.
	ramp := []rune(sparkline([]int{0, 1, 2, 3, 4, 5, 6, 7}))
	if len(ramp) != 8 {
		t.Fatalf("ramp length = %d, want 8", len(ramp))
	}
	if ramp[0] != '▁' || ramp[7] != '█' {
		t.Fatalf("ramp ends = %q..%q, want ▁..█", ramp[0], ramp[7])
	}
	for i := 1; i < len(ramp); i++ {
		if ramp[i] < ramp[i-1] {
			t.Fatalf("ramp not non-decreasing: %q", string(ramp))
		}
	}
}
