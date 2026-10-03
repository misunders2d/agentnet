package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// BUG-10: status takes the logical id `dm show` prints and reports each
// copy, naming the device it went to; a copy's own id still reports that
// copy alone, as before.
func TestStatusOfLogicalIDReportsEachCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	home := t.TempDir()
	alice, err := client.Join(ctx, home, testhub.BootstrapCode(t, hub), "alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
		run, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(run, client.RunOptions{}); close(done) }()
		t.Cleanup(func() { stop(); <-done })
	}
	var conv string
	for conv == "" {
		if conv, err = alice.CreateDM(ctx, bob.Address); err != nil && ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	sent, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "status by lid"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := diagnosticOutput(t, func() error { return run([]string{"--home", home, "status", "--wait", "10s", sent.LID}) })
	if want := sent.ID + " delivered relay to " + bob.Address + "\n"; err != nil || out != want {
		t.Fatalf("status of the logical id: %q %v, want %q", out, err, want)
	}
	out, err = diagnosticOutput(t, func() error { return run([]string{"--home", home, "status", sent.ID}) })
	if want := sent.ID + " delivered relay\n"; err != nil || out != want {
		t.Fatalf("status of the copy: %q %v, want %q", out, err, want)
	}
}
