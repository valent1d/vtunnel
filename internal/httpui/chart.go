package httpui

import (
	"math"
	"strings"
)

// sparkBlocks are the eight vertical block glyphs used to draw sparklines,
// from lowest to highest.
var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// sparkline renders values as a row of block glyphs scaled to the maximum value,
// one glyph per value (oldest -> newest). An empty input yields an empty string;
// an all-zero input renders a flat baseline.
func sparkline(values []int) string {
	if len(values) == 0 {
		return ""
	}
	maxv := 0
	for _, v := range values {
		if v > maxv {
			maxv = v
		}
	}
	var b strings.Builder
	b.Grow(len(values) * 3) // block glyphs are multi-byte
	for _, v := range values {
		idx := 0
		if maxv > 0 {
			idx = int(math.Round(float64(v) / float64(maxv) * float64(len(sparkBlocks)-1)))
		}
		if idx < 0 {
			idx = 0
		}
		if idx > len(sparkBlocks)-1 {
			idx = len(sparkBlocks) - 1
		}
		b.WriteRune(sparkBlocks[idx])
	}
	return b.String()
}
