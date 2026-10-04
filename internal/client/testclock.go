//go:build agentnet_testclock

package client

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// A test-only clock seam, compiled only with -tags agentnet_testclock and
// never into a released program. When AGENTNET_TEST_CLOCK_FILE names a
// file, the times this device stores on device-thread messages and topic
// marks, and the "now" topic states are derived at, are shifted by the
// whole seconds that file holds (read on every use, so a disposable world
// can make some of its history old and the rest current; see
// internal/ui/testdata/topics_seed.sh). Envelopes, signatures and every
// request to the Hub keep the real time.
func init() {
	path := os.Getenv("AGENTNET_TEST_CLOCK_FILE")
	if path == "" {
		return
	}
	storeClock = func() time.Time {
		data, err := os.ReadFile(path)
		if err != nil {
			return time.Now()
		}
		shift, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			return time.Now()
		}
		return time.Now().Add(time.Duration(shift) * time.Second)
	}
}
