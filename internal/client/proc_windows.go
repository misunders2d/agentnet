//go:build windows

package client

import "os/exec"

// ownProcessGroup: on Windows only the harness process itself is killed on
// cancellation; processes it started may keep running (no job object here).
func ownProcessGroup(cmd *exec.Cmd) {}
