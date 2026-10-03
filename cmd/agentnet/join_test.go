package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
			// Pending linking must not skip the local responder setup reminder.
			out, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			prior := os.Stderr
			os.Stderr = out
			joinErr := runJoin(ctx, home, []string{"--agent", form, code})
			os.Stderr = prior
			if _, err := out.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			text, err := io.ReadAll(out)
			out.Close()
			if err != nil {
				t.Fatal(err)
			}
			if joinErr != nil {
				t.Fatal(joinErr)
			}
			if !strings.Contains(string(text), "person's chosen responder") {
				t.Fatalf("missing responder setup: %s", text)
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

// A device waiting for its link approval is told so, not to create a
// person; person create and person service are refused there.
func TestPendingDevicePersonHint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	laptop, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, hub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer laptop.Close()
	if _, err := laptop.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	offer, err := laptop.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := client.JoinAndLink(ctx, t.TempDir(), offer.Code, "watch")
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	var out bytes.Buffer
	err = runPerson(ctx, watch, nil, &out)
	if err == nil || strings.Contains(err.Error(), "person create") || !strings.Contains(err.Error(), "waits for approval on "+laptop.Address) {
		t.Fatalf("pending device's person hint: %v", err)
	}
	for _, args := range [][]string{{"create", "Watch"}, {"service"}} {
		if err := runPerson(ctx, watch, args, &out); !errors.Is(err, client.ErrLinkWaiting) {
			t.Fatalf("person %s on a pending device: %v", strings.Join(args, " "), err)
		}
	}
	if _, ok, err := watch.Person(); err != nil || ok {
		t.Fatalf("pending device's person: %v %v", ok, err)
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
