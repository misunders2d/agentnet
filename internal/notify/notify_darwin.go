package notify

// Show posts a notification with osascript. The text is passed as script
// arguments, never spliced into the script.
// Each call shows a new notification; macOS has no replace-by-id here.
func Show(title, body string) error { return showAppleScript(title, body) }

// Close has nothing to release here.
func Close() {}
