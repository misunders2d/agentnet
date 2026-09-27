package notify

// Show posts a notification with osascript. The text is passed as script
// arguments, never spliced into the script.
func Show(title, body string) error {
	return run("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body)
}
