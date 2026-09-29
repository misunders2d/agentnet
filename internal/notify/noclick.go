//go:build !linux && !windows

package notify

// ShowAction is Show: clicks are not handled on this platform (macOS
// osascript notifications have no click callback; a real one needs an app
// bundle with a UNUserNotificationCenter delegate), so argv and onClick are
// unused.
func ShowAction(title, body string, argv []string, onClick func()) error { return Show(title, body) }
