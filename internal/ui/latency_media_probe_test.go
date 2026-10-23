package ui

import "testing"

func TestLatencyMediaProbeGuards(t *testing.T) {
	runNodeCheck(t, "testdata/latency_media_probe_check.cjs")
}
