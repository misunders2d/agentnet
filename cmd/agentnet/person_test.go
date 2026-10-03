package main

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// person approve ID trusts the device it links (a native one) for invites
// of the person's own agents; approve --browser links without trusting;
// trust and untrust change it later, and person marks trusted devices.
func TestPersonApproveTrustsNativeDevicesOnly(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off") // its daemon shows nothing on this desktop
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { a.Run(runCtx, client.RunOptions{}); close(done) }()
	defer func() { stop(); <-done }()
	link := func(name string, approve ...string) client.TrustedDevice {
		t.Helper()
		o, err := a.NewDeviceLink(ctx)
		if err != nil {
			t.Fatal(err)
		}
		dev, err := client.JoinAndLink(ctx, t.TempDir(), o.Code, name)
		if err != nil {
			t.Fatal(err)
		}
		defer dev.Close()
		id := ""
		for deadline := time.Now().Add(15 * time.Second); id == ""; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("no link request from %s", dev.Address)
			}
			links, _ := a.PendingLinks()
			for _, l := range links {
				if l.State == client.LinkPending && l.Address == dev.Address {
					id = l.ID
				}
			}
		}
		var out bytes.Buffer
		if err := runPerson(ctx, a, append(append([]string{"approve"}, approve...), id), &out); err != nil {
			t.Fatalf("approve %v: %v", approve, err)
		}
		return client.TrustedDevice{Address: dev.Address, Fingerprint: dev.Self().Fingerprint()}
	}
	trusted := func(d client.TrustedDevice) bool {
		t.Helper()
		set, err := a.SelfConsentTrust()
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(set, d)
	}
	desk := link("desk")
	phone := link("phone", "--browser")
	if !trusted(desk) || trusted(phone) {
		t.Fatalf("after approve: desk trusted %v, browser phone trusted %v", trusted(desk), trusted(phone))
	}
	var out bytes.Buffer
	if err := runPerson(ctx, a, nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if marked := strings.Contains(line, "(trusted:"); marked != strings.Contains(line, desk.Address) {
			t.Fatalf("person marks the wrong devices: %s", &out)
		}
	}
	if err := runPerson(ctx, a, []string{"trust", phone.Address}, &out); err != nil || !trusted(phone) {
		t.Fatalf("trust: %v", err)
	}
	if err := runPerson(ctx, a, []string{"untrust", phone.Address}, &out); err != nil || trusted(phone) {
		t.Fatalf("untrust: %v", err)
	}
	for _, bad := range [][]string{{"untrust", a.Address}, {"trust", "admin/nobody"}, {"approve", "--native", "x"}} {
		if err := runPerson(ctx, a, bad, &out); err == nil {
			t.Fatalf("person %v accepted", bad)
		}
	}
}
