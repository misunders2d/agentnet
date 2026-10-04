//go:build unix

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A named pipe is refused as an attachment before it is opened: opening one
// waits for a writer, so a send hung for good (ignoring Ctrl+C). Both the
// conversation path (which keeps the sender's own copy first) and a device
// message are covered; nothing is left in the spool.
func TestAttachingNamedPipeRefused(t *testing.T) {
	w, conv, _ := dmFiles(t)
	pipe := filepath.Join(t.TempDir(), "pipe.csv")
	if err := unix.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, send := range map[string]func() error{
		"conversation": func() error {
			_, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "x", Files: []OutgoingFile{{Path: pipe}}})
			return err
		},
		"device": func() error {
			_, err := w.alice.Send(tctx(t), w.bob.Address, "x", "", pipe)
			return err
		},
	} {
		done := make(chan error, 1)
		go func() { done <- send() }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			unblockPipe(pipe, done)
			t.Fatalf("%s: attaching a named pipe hangs", name)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 0 {
		t.Fatalf("spool after refused sends: %d files", len(entries))
	}
}

// unblockPipe lets a reader stuck opening pipe go on (a writer opens and
// closes it) until done reports, so a failing test leaves nothing hanging.
func unblockPipe(pipe string, done <-chan error) {
	deadline := time.After(5 * time.Second)
	for {
		if f, err := os.OpenFile(pipe, os.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		select {
		case <-done:
			return
		case <-deadline:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}
