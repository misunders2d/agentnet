//go:build windows

package client

import "os/exec"

// ownProcessGroup: on Windows only the harness process itself is killed on
// cancellation; processes it started may keep running (no job object here).
func ownProcessGroup(cmd *exec.Cmd) {}

// stopGroup: no process group is kept on Windows (and runs there get no
// outbox).
func stopGroup(cmd *exec.Cmd) {}
