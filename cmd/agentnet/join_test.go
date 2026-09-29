package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// The command accepts an ordinary invite and all three forms of the
// existing device's link. A link joins pending, never as another person.
func TestJoinAcceptsDeviceLinks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	home := t.TempDir()
	if err := runJoin(ctx, home, []string{"--agent", "laptop", testhub.BootstrapCode(t, hub)}); err != nil {
		t.Fatal(err)
	}
	laptop, err := client.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer laptop.Close()
	if _, ok, err := laptop.Person(); err != nil || ok {
		t.Fatalf("ordinary enrollment created a person: %v", err)
	}
	person, err := laptop.CreatePerson(ctx, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{"code", "fragment", "url"} {
		t.Run(form, func(t *testing.T) {
			offer, err := laptop.NewDeviceLink(ctx)
			if err != nil {
				t.Fatal(err)
			}
			code := offer.Code
			if form == "fragment" {
				code = "#" + code
			} else if form == "url" {
				code = "https://example.test/#" + code
			}
			home := t.TempDir()
			if err := runJoin(ctx, home, []string{"--agent", form, code}); err != nil {
				t.Fatal(err)
			}
			device, err := client.Open(home)
			if err != nil {
				t.Fatal(err)
			}
			defer device.Close()
			link := device.LinkState()
			if link.State != client.LinkPending || link.Person != person.Person || link.Approver != laptop.Address {
				t.Fatalf("new device not pending for the existing person: %+v", link)
			}
			if _, ok, err := device.Person(); err != nil || ok {
				t.Fatalf("pending device already has a person: %v", err)
			}
		})
	}
}

func TestJoinMissingNameCreatesNothing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "unused")
	if err := runJoin(context.Background(), home, []string{"agentnet-link-v2:invalid"}); err == nil {
		t.Fatal("joined without a chosen device name")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("missing name touched the home: %v", err)
	}
}
