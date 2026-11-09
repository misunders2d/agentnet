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
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestAdminRevokeExactPerson(t *testing.T) {
	stops := map[*client.Agent]func(){}
	alice, bob, carol, _, _ := dmWorld(t, stops)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, ok, err := bob.Person()
	if err != nil || !ok {
		t.Fatalf("person: %v %v", ok, err)
	}
	var listed bytes.Buffer
	if err := runPerson(ctx, alice, []string{"list"}, &listed); err != nil {
		t.Fatal(err)
	}
	for _, exact := range []string{p.Person, bob.Address, bob.Self().Fingerprint()} {
		if !strings.Contains(listed.String(), exact) {
			t.Fatalf("person lookup omitted exact offboarding target %q: %s", exact, listed.String())
		}
	}
	for _, args := range [][]string{{"revoke", "--person"}, {"revoke", "--person", "Bob"}, {"revoke", "--person", bob.Address}} {
		if err := runAdmin(ctx, alice, args); err == nil {
			t.Fatalf("ambiguous person accepted: %v", args)
		}
	}
	if err := runAdmin(ctx, carol, []string{"revoke", "--person", p.Person}); err == nil {
		t.Fatal("non-admin could remove a person")
	}
	// The removed device's own daemon ends with the Hub's revocation (403) once
	// it reconnects, as it should; it is stopped first, so that expected end
	// is not read as the fixture's failure.
	stops[bob]()
	if err := runAdmin(ctx, alice, []string{"revoke", "--person", p.Person}); err != nil {
		t.Fatal(err)
	}
	if revoked, err := alice.Revoked(ctx, bob.Address); err != nil || !revoked {
		t.Fatalf("target still admitted: %v %v", revoked, err)
	}
	if revoked, err := alice.Revoked(ctx, carol.Address); err != nil || revoked {
		t.Fatalf("unrelated person affected: %v %v", revoked, err)
	}
}

// Current verified human devices all use the same own-agent consent rule.
func TestPersonApproveOwnHumanDevices(t *testing.T) {
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
		// A signed roster does not silently trust a changed directory key.
		// Establish the exact peer pin through the ordinary sender path.
		if _, err := a.SendMessage(ctx, client.Outgoing{To: dev.Address, Kind: envelope.KindMessage, Body: "linked device fixture"}); err != nil {
			t.Fatal(err)
		}
		return client.TrustedDevice{Address: dev.Address, Fingerprint: dev.Self().Fingerprint()}, out.String()
	}
	trusted := func(d client.TrustedDevice) bool {
		t.Helper()
		set, err := a.OwnHumanDevices()
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(set, d)
	}
	phone, said := link("phone")
	if !trusted(phone) || !strings.Contains(said, "without another AgentNet OK") {
		t.Fatalf("ordinary phone approval: trusted %v, said %q", trusted(phone), said)
	}
	desk, said := link("desk", "--native")
	if !trusted(desk) || !strings.Contains(said, "legacy alias") {
		t.Fatalf("legacy native approval: trusted %v, said %q", trusted(desk), said)
	}
	var out bytes.Buffer
	if err := runPerson(ctx, a, nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, d := range []client.TrustedDevice{phone, desk} {
		found := false
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, d.Address) && strings.Contains(line, "your verified device") {
				found = true
			}
		}
		if !found {
			t.Fatalf("own device missing from effective permissions: %s", &out)
		}
	}
	for _, bad := range [][]string{{"trust", phone.Address}, {"untrust", a.Address}, {"untrust", desk.Address}, {"approve", "--browser", "x"}, {"approve", "--native"}} {
		if err := runPerson(ctx, a, bad, &out); err == nil {
			t.Fatalf("person %v accepted", bad)
		}
	}
	if !trusted(phone) || !trusted(desk) {
		t.Fatal("legacy untrust changed own-device authority")
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
