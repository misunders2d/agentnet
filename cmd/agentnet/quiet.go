package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// quietAttr adjusts a short-lived helper command for the platform (on
// Windows: no console window). Nil elsewhere.
var quietAttr func(*exec.Cmd)

// capped keeps at most max bytes and remembers whether more came.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// runQuiet runs a short external command (schtasks, PowerShell, a program's
// `version`) bounded in time and in the output it keeps: it never waits past
// timeout, and more than max bytes of output is an error. It returns
// standard output; the error names the command and carries its standard
// error.
func runQuiet(timeout time.Duration, max int, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if quietAttr != nil {
		quietAttr(cmd)
	}
	out, errOut := &capped{max: max}, &capped{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.WaitDelay = 2 * time.Second // do not wait on pipes a killed command's children still hold
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s did not finish within %s", name, timeout)
	case out.over:
		return nil, fmt.Errorf("%s printed more than %d bytes", name, max)
	case err != nil:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out.buf.Bytes(), fmt.Errorf("%s: %v: %s", name, err, bytes.TrimSpace(errOut.buf.Bytes()))
		}
		return out.buf.Bytes(), fmt.Errorf("%s: %w", name, err)
	}
	return out.buf.Bytes(), nil
}
