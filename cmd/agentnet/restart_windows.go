package main

import (
	"errors"

	"github.com/misunders2d/agentnet/internal/client"
)

// canSwitchInPlace: not yet on Windows, where a process cannot put another
// program in its place and a replacement must stay under the scheduled task.
func canSwitchInPlace() (bool, string) {
	return false, "switching a running daemon is not supported on Windows yet; restart it (schtasks /end, then /run /tn agentnet) when no job runs"
}

// restartForUpdate is not reached on Windows: the daemon does not switch
// there yet (canSwitchInPlace), and agentnet update says so.
func restartForUpdate(home, exe string, r client.UpdateRequest) error {
	return errors.New("switching a running daemon is not supported on Windows yet; start the daemon again")
}
