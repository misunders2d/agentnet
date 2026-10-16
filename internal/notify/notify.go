// Package notify shows desktop notifications with locally masked, bounded
// display text chosen by the person's local preview preference. Callers must
// apply that mask before passing content: the OS may show it on a locked screen
// or keep it in notification history. No display text goes to a push provider.
package notify

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// ErrUnsupported means this platform has no notifier yet.
var ErrUnsupported = errors.New("desktop notifications are not supported on this platform yet")

const timeout = 10 * time.Second

// run starts a notifier with argv only (no shell) and returns its standard
// output, or its failure including the start of its error output.
func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg != "" {
			return "", errors.New(name + ": " + err.Error() + ": " + msg)
		}
		return "", errors.New(name + ": " + err.Error())
	}
	return stdout.String(), nil
}
