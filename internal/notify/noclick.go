//go:build !linux

package notify

// ShowAction is Show: clicks are not handled on this platform yet (macOS
// osascript notifications have no callback; the Windows balloon's click
// message is not handled), so argv and onClick are unused.
func ShowAction(title, body string, argv []string, onClick func()) error { return Show(title, body) }
