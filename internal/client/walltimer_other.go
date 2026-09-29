//go:build !linux

package client

// platformWallTimer is the Go timer: on this platform no suspend-aware
// wall timer is used (not verified), so a sleep can delay a reminder until
// the daemon next wakes for another reason.
func platformWallTimer() wallTimer { return newGoWallTimer() }
