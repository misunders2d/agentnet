//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/misunders2d/agentnet/internal/client"
)

// canSwitchInPlace: on Unix the updated program takes this process's place.
func canSwitchInPlace() (bool, string) { return true, "" }

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
