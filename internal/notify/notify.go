// Package notify shows a desktop notification with fixed, content-free
// text. Callers must never pass message content: the text may be shown on a
// locked screen or kept by the notification server.
package notify

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// ErrUnsupported means this platform has no notifier yet.
var ErrUnsupported = errors.New("desktop notifications are not supported on this platform yet")

const timeout = 10 * time.Second

// run starts a notifier with argv only (no shell) and reports its failure,
// including the start of its error output.
func run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg != "" {
			return errors.New(name + ": " + err.Error() + ": " + msg)
		}
		return errors.New(name + ": " + err.Error())
	}
	return nil
}
