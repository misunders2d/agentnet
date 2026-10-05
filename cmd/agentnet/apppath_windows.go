package main

// addLoginShellPath changes nothing on Windows: programs started from the
// Start menu get the person's own PATH.
func addLoginShellPath(logf func(string, ...any)) {}
