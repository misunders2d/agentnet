package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

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
