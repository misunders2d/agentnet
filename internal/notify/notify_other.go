//go:build !linux && !darwin && !windows

package notify

// Show is not implemented on this platform: items stay pending and the
// daemon logs that no notification was shown.
func Show(title, body string) error { return ErrUnsupported }

// Close has nothing to release here.
func Close() {}
