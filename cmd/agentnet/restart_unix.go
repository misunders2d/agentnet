//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/misunders2d/agentnet/internal/client"
)

// switchHooks returns the platform's RunOptions.CanSwitch and PrepareSwitch
// for a daemon of home started from exe. On Unix the updated program takes
// this very process's place (restartForUpdate), so nothing needs checking or
// preparing.
func switchHooks(home, exe string) (func() (bool, string), func(client.UpdateRequest) error) {
	return nil, nil
}

// runUpdateHelper exists only on Windows.
func runUpdateHelper(home string, args []string) error {
	return fmt.Errorf("%s is only used on Windows", updateHelperCmd)
}

// processAlive reports whether process pid exists (known: it could be asked).
func processAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM, true
}

// restartForUpdate puts the updated program file in place of this daemon
// process: same process id, arguments and environment, so a service manager
// or terminal that started the daemon keeps it. The new program records the
// outcome when it starts. It returns only if that could not happen.
func restartForUpdate(home, exe string, r client.UpdateRequest) error {
	env := []string{client.UpdateRestartEnv + "=" + r.ID}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, client.UpdateRestartEnv+"=") {
			env = append(env, kv)
		}
	}
	err := syscall.Exec(exe, os.Args, env)
	client.RecordUpdateActivation(home, client.UpdateActivation{ID: r.ID, To: r.To, Result: client.ActivationFailed,
		Detail: "could not start " + exe + ": " + err.Error() + "; the daemon has stopped, start it again"})
	return fmt.Errorf("stopped to switch to agentnet %s, but could not start %s: %w; start the daemon again", r.To, exe, err)
}
