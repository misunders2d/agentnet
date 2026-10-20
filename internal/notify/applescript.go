package notify

// showAppleScript uses Notification Center's own sound handling, so macOS
// notification permissions, Focus and sound settings remain authoritative.
// Keep display text and the fixed system sound in argv, outside the script.
func showAppleScript(title, body string) error {
	_, err := run("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv) sound name (item 3 of argv)",
		"-e", "end run",
		title, body, "Glass")
	return err
}
