package notify

// Show posts a notification with osascript. The text is passed as script
// arguments, never spliced into the script.
// Each call shows a new notification; macOS has no replace-by-id here.
func Show(title, body string) error {
	_, err := run("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body)
	return err
}
