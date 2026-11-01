package client

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// quietHook runs the per-call hook events on an unchanged registration.
// Every native tool call runs them, so none may write, announce a change or
// wake the daemon (MEL-731): each wake made the owner's daemon rebuild its
// open views, about a core while agents worked. The returned woken reports a
// later wake: the caller's next changing event must still send one.
func quietHook(t *testing.T, a *Agent, register func(event string) (string, error)) (woken func(time.Duration) bool) {
	t.Helper()
	path := sockPath(a.home)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	// notifyDaemon's dial has connected before the hook returns, so a wake
	// it sent is already waiting here.
	woken = func(wait time.Duration) bool {
		ln.(*net.UnixListener).SetDeadline(time.Now().Add(wait))
		c, err := ln.Accept()
		if err == nil {
			c.Close()
			return true
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
		return false
	}
	records := func() string {
		var raw string
		if err := a.store.db.QueryRow(`SELECT coalesce(group_concat(handle||record),'') FROM (SELECT handle,record FROM reply_sessions ORDER BY handle)`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before, _ := a.ViewChanges()
	saved := records()
	for _, event := range []string{"PostToolUse", "UserPromptSubmit", "PostToolUse", "Stop"} {
		if _, err := register(event); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	if woken(50 * time.Millisecond) {
		t.Fatal("an unchanged registration woke the daemon")
	}
	if after, _ := a.ViewChanges(); after.Content != before.Content {
		t.Fatal("an unchanged registration announced a change")
	}
	if records() != saved {
		t.Fatal("an unchanged registration was rewritten")
	}
	return woken
}
