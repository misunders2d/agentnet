//go:build !linux && !darwin

package notify

// Show is not implemented here yet (Windows included): items stay pending
// and the daemon logs that no notification was shown.
func Show(title, body string) error { return ErrUnsupported }
