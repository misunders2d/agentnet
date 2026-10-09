package ui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Removing an exact-device grant must leave the current roster and inherited
// person permission visible. The inventory does not create any grants itself.
func TestOwnPermissionInventory(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	desk, err := client.Join(ctx, filepath.Join(t.TempDir(), "desk"), testhub.BootstrapCode(t, hub), "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { desk.Close() })
	runDaemon(t, desk)
	live := NewLive(desk)
	if _, _, err = live.CreatePerson("Owner"); err != nil {
		t.Fatal(err)
	}
	var link DeviceLink
	waitFor(t, "device link", func() bool { link, err = live.NewDeviceLink(); return err == nil })
	phone, err := client.JoinAndLink(ctx, filepath.Join(t.TempDir(), "phone"), link.URL, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	runDaemon(t, phone)
	var request string
	waitFor(t, "link request", func() bool {
		o, e := live.Overview()
		if e == nil && len(o.Links) == 1 {
			request = o.Links[0].ID
		}
		return request != ""
	})
	if _, err = live.DecideLink(request, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "linked phone", func() bool { p, ok, e := phone.Person(); return e == nil && ok && len(p.Devices) == 2 })
	if _, err = phone.SendMessage(ctx, client.Outgoing{To: desk.Address, Kind: envelope.KindMessage, Body: "linked device pin"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "verified phone key", func() bool {
		k, e := desk.PeerKeyOf(phone.Address)
		return e == nil && k.Pinned == phone.Self().Fingerprint()
	})
	me, _, err := desk.Person()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{{Do: DoApprove, ID: me.Person}, {Do: DoGrantTasks, ID: me.Person}, {Do: DoApprove, ID: phone.Address}, {Do: DoGrantTasks, ID: phone.Address}} {
		if _, err := live.Act(action); err != nil {
			t.Fatal(err)
		}
	}
	check := func(questions, tasks string) {
		t.Helper()
		v, e := live.Approvals()
		if e != nil {
			t.Fatal(e)
		}
		if len(v.OwnDevices) != 2 {
			t.Fatalf("device disappeared: %+v", v.OwnDevices)
		}
		for _, d := range v.OwnDevices {
			if d.Address == phone.Address {
				if d.Questions != questions || d.Tasks != tasks || d.This {
					t.Fatalf("phone permission: %+v", d)
				}
				return
			}
		}
		t.Fatal("phone missing from current roster")
	}
	check("person", "person")
	for _, kind := range []string{"question", "task"} {
		if _, err = live.RevokeApproval(ApprovalRevoke{Kind: kind, Address: phone.Address}); err != nil {
			t.Fatal(err)
		}
	}
	check("person", "person")
	for _, kind := range []string{"question", "task"} {
		if _, err = live.RevokeApproval(ApprovalRevoke{Kind: kind, Person: me.Person}); err != nil {
			t.Fatal(err)
		}
	}
	check("approval", "approval")
	if _, err = live.RemoveDevice(phone.Address); err != nil {
		t.Fatal(err)
	}
	v, err := live.Approvals()
	if err != nil || len(v.OwnDevices) != 1 || !v.OwnDevices[0].This {
		t.Fatalf("removed device inventory: %+v %v", v.OwnDevices, err)
	}
}
