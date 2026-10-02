package client

import (
	"math"
	"testing"
	"time"
)

// The reconnect schedule keeps the bands the loops had: over many
// independent schedules every step's wait is inside its band (the library
// may exceed the top by a nanosecond) and spreads across it; the cap band
// holds for good; Reset returns to the first band.
func TestReconnectBackoffBands(t *testing.T) {
	bands := [][2]time.Duration{
		{500 * time.Millisecond, time.Second}, {time.Second, 2 * time.Second}, {2 * time.Second, 4 * time.Second},
		{4 * time.Second, 8 * time.Second}, {8 * time.Second, 16 * time.Second}, {16 * time.Second, 32 * time.Second},
		{30 * time.Second, 60 * time.Second}, {30 * time.Second, 60 * time.Second}, {30 * time.Second, 60 * time.Second},
	}
	const schedules = 3000
	lo := make([]time.Duration, len(bands))
	hi := make([]time.Duration, len(bands))
	for i := range lo {
		lo[i] = math.MaxInt64
	}
	for range schedules {
		b := newReconnectBackoff()
		b.Reset()
		for step, band := range bands {
			w := b.NextBackOff()
			if w < band[0] || w > band[1]+time.Nanosecond {
				t.Fatalf("step %d: wait %s outside [%s, %s]", step, w, band[0], band[1])
			}
			lo[step], hi[step] = min(lo[step], w), max(hi[step], w)
		}
		b.Reset()
		if w := b.NextBackOff(); w < bands[0][0] || w > bands[0][1]+time.Nanosecond {
			t.Fatalf("after Reset: %s", w)
		}
	}
	for step, band := range bands {
		width := band[1] - band[0]
		if lo[step] > band[0]+width/20 || hi[step] < band[1]-width/20 {
			t.Fatalf("step %d: %d samples span only [%s, %s] of [%s, %s]", step, schedules, lo[step], hi[step], band[0], band[1])
		}
	}
}
