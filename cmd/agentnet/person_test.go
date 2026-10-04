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

// Fail closed (review finding 1): person approve ID, the flow a browser
// is linked with too, trusts nothing; only person approve --native ID,
// which says it is never for a browser, adds the device to the self-consent
// trust set. Nothing else adds one (there is no person trust); untrust
// removes one, and person marks trusted devices.
func TestPersonApproveTrustsOnlyNative(t *testing.T) {
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
	link := func(name string, approve ...string) (client.TrustedDevice, string) {
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
		return client.TrustedDevice{Address: dev.Address, Fingerprint: dev.Self().Fingerprint()}, out.String()
	}
	trusted := func(d client.TrustedDevice) bool {
		t.Helper()
		set, err := a.SelfConsentTrust()
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(set, d)
	}
	phone, said := link("phone")
	if trusted(phone) || strings.Contains(said, "need no accept") {
		t.Fatalf("person approve ID trusted the device (it may be a browser): %q", said)
	}
	desk, said := link("desk", "--native")
	if !trusted(desk) || !strings.Contains(said, "never") || !strings.Contains(said, "browser") {
		t.Fatalf("approve --native: trusted %v, said %q", trusted(desk), said)
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
	for _, bad := range [][]string{{"trust", phone.Address}, {"untrust", a.Address}, {"approve", "--browser", "x"}, {"approve", "--native"}} {
		if err := runPerson(ctx, a, bad, &out); err == nil {
			t.Fatalf("person %v accepted", bad)
		}
	}
	if trusted(phone) {
		t.Fatal("the phone became trusted")
	}
	if err := runPerson(ctx, a, []string{"untrust", desk.Address}, &out); err != nil || trusted(desk) {
		t.Fatalf("untrust: %v", err)
	}
}

// BUG-39a: after a Hub admin revokes one device of a person, the person's
// own device list says so (with what to do), and fingerprint says the
// address is revoked instead of showing it like any other.
func TestPersonAndFingerprintShowRevokedDevice(t *testing.T) {
	alice, bob, _, _, eventually := dmWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	offer, err := bob.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := client.JoinAndLink(ctx, filepath.Join(t.TempDir(), "phone"), offer.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	linked := make(chan error, 1)
	go func() { _, e := phone.AwaitLink(ctx); linked <- e }()
	var request string
	eventually("the phone's link request at bob", func() bool {
		rows, e := bob.PendingLinks()
		if e != nil || len(rows) == 0 {
			return false
		}
		request = rows[0].ID
		return true
	})
	if err = bob.DecideLink(ctx, request, true); err != nil {
		t.Fatal(err)
	}
	if err = <-linked; err != nil {
		t.Fatal(err)
	}
	if err = alice.Revoke(ctx, phone.Address); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = runPerson(ctx, bob, nil, &out); err != nil {
		t.Fatal(err)
	}
	line := ""
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, phone.Address) {
			line = l
		}
	}
	if !strings.Contains(line, "revoked") || !strings.Contains(line, "person remove "+phone.Address) {
		t.Fatalf("person does not show the revoked device:\n%s", out.String())
	}
	if strings.Contains(strings.Replace(out.String(), line, "", 1), "revoked") {
		t.Fatalf("a current device shown revoked:\n%s", out.String())
	}
	out.Reset()
	if err = runFingerprint(ctx, alice, phone.Address, &out); err != nil || !strings.Contains(out.String(), "revoked") {
		t.Fatalf("fingerprint of a revoked device: %v\n%s", err, out.String())
	}
}
