package ui

import (
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

// One person on two devices through the daemon's page (MEL-433): a link
// made on the page, a new device joining with it, approved (or refused)
// on the page; its devices listed, one removed; a service never has a
// person; the chats before the link come to the new device as history,
// and a message sent from either device shows on the other as yours.
func TestLiveIdentity(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	join := func(code, name string) *client.Agent {
		a, err := client.Join(ctx, filepath.Join(t.TempDir(), name), code, name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		runDaemon(t, a)
		return a
	}
	laptop := join(testhub.BootstrapCode(t, dir), "laptop")
	code, _ := laptop.Invite(ctx, "bob", time.Hour, false)
	bob := join(code, "desk")
	code, _ = laptop.Invite(ctx, "svc", time.Hour, false)
	svc := join(code, "bot")
	pl, pb, ps := NewLive(laptop), NewLive(bob), NewLive(svc)
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	over := func(p *Live) Overview {
		t.Helper()
		o, err := p.Overview()
		if err != nil {
			t.Fatal(err)
		}
		return o
	}

	// A service: no person, and none can be made later.
	if o := over(ps); o.Role != RoleUnset {
		t.Fatalf("a new installation's role: %q", o.Role)
	}
	if _, err := ps.SetService(); err != nil || over(ps).Role != RoleService {
		t.Fatalf("service: %v %q", err, over(ps).Role)
	}
	if _, _, err := ps.CreatePerson("Bot"); !errors.Is(err, ErrRefused) {
		t.Fatalf("a service made a person: %v", err)
	}

	if _, _, err := pl.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	pb.CreatePerson("Bob")
	// A DM from before the phone: it reaches the phone as history.
	var before string
	eventually("a DM before the phone", func() bool {
		var err error
		before, err = pl.NewDM(bob.Address)
		return err == nil
	})
	if _, err := pl.SendDM(DMDraft{Conv: before, Body: "before the phone"}); err != nil {
		t.Fatal(err)
	}
	// Bob's file in it, which the phone asks the laptop for later.
	eventually("Bob has the DM", func() bool { ms, _ := bob.ConversationMessages(before); return len(ms) == 1 })
	filePath := filepath.Join(t.TempDir(), "plan.txt")
	os.WriteFile(filePath, []byte("the plan, from bob"), 0o600)
	if _, err := bob.SendConv(ctx, before, client.ConvOutgoing{Body: "a file", Files: []client.OutgoingFile{{Path: filePath}}}); err != nil {
		t.Fatal(err)
	}
	eventually("the laptop has Bob's file", func() bool { ms, _ := laptop.ConversationMessages(before); return len(ms) == 2 })
	o := over(pl)
	if o.Role != RolePerson || o.Person == nil || len(o.Person.Devices) != 1 || !o.Person.Devices[0].This || o.Person.Devices[0].Name != "laptop" {
		t.Fatalf("your person on one device: %q %+v", o.Role, o.Person)
	}
	var link DeviceLink
	eventually("a link once the person is published", func() bool {
		var err error
		link, err = pl.NewDeviceLink()
		return err == nil
	})
	// This test Hub pins its own certificate, so no browser can open it:
	// the link is the code alone, for the command line.
	if !strings.HasPrefix(link.URL, "agentnet-link-v2:") || !link.Expires.After(time.Now()) {
		t.Fatalf("the link: %q %v", link.URL, link.Expires)
	}
	phoneHome := filepath.Join(t.TempDir(), "phone")
	phone, err := client.JoinAndLink(ctx, phoneHome, link.URL, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	runDaemon(t, phone)
	pp := NewLive(phone)
	eventually("the phone waiting", func() bool { o := over(pp); return o.Link != nil && o.Link.State == "pending" })
	var req LinkRequest
	eventually("the request on the laptop's page", func() bool {
		o := over(pl)
		if len(o.Links) == 1 {
			req = o.Links[0]
		}
		return req.ID != ""
	})
	if req.Name != "phone" || req.Fingerprint != phone.Self().Fingerprint() {
		t.Fatalf("the request says: %+v", req)
	}
	if note, err := pl.DecideLink(req.ID, true); err != nil || !strings.Contains(note, "phone") {
		t.Fatalf("approve: %q %v", note, err)
	}
	eventually("the phone linked", func() bool {
		o := over(pp)
		return o.Link == nil && o.Person != nil && o.Person.Label == "Alice" && len(o.Person.Devices) == 2
	})
	if o := over(pl); len(o.Links) != 0 || len(o.Person.Devices) != 2 {
		t.Fatalf("the laptop after approving: %+v %+v", o.Links, o.Person)
	}
	if _, err := pl.DecideLink(req.ID, true); !errors.Is(err, ErrRefused) {
		t.Fatalf("a request decided twice: %v", err)
	}

	eventually("the old DM on the phone, as history from the laptop", func() bool {
		d, err := pp.DM(before)
		return err == nil && len(d.Messages) == 2 && d.Messages[0].Body == "before the phone" && d.Messages[0].SyncedFrom == laptop.Address &&
			len(d.Messages[1].Attachments) == 1 && d.Messages[1].Attachments[0].Availability == "requestable"
	})
	d, _ := pp.DM(before)
	if err := pp.RequestFile(ctx, d.Messages[1].ID, 0); err != nil {
		t.Fatal(err)
	}
	eventually("the laptop's answer", func() bool {
		d, _ := pp.DM(before)
		return d.Messages[1].Attachments[0].Availability == ""
	})
	rc, name, err := pp.OpenFile(ctx, d.Messages[1].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close() // the opened copy goes with it (an open one cannot be removed on Windows)
	if string(got) != "the plan, from bob" || name != "plan.txt" {
		t.Fatalf("the file: %q %q", got, name)
	}
	if left, _ := os.ReadDir(filepath.Join(phoneHome, "opened")); len(left) != 0 {
		t.Fatalf("an opened file left %d copies", len(left))
	}
	eventually("the laptop's copy of it done", func() bool {
		o := over(pl)
		return len(o.History) == 1 && o.History[0].Name == "phone" && o.History[0].State == "done" && o.History[0].Done == o.History[0].Total
	})

	// Bob's page shows one Alice with two devices.
	eventually("Alice on two devices at Bob", func() bool {
		for _, p := range over(pb).People {
			if p.Label == "Alice" && len(p.Devices) == 2 {
				return true
			}
		}
		return false
	})
	// A DM the phone starts is the laptop's too, shown as sent from the
	// phone with one copy per other device; the laptop's reply shows on
	// the phone as sent from the laptop.
	var conv string
	eventually("a DM from the phone", func() bool { conv, err = pp.NewDM(bob.Address); return err == nil })
	if _, err := pp.SendDM(DMDraft{Conv: conv, Body: "from my phone"}); err != nil {
		t.Fatal(err)
	}
	fromOther := func(p *Live, body, via string) func() bool {
		return func() bool {
			d, err := p.DM(conv)
			if err != nil {
				return false
			}
			for _, m := range d.Messages {
				if m.Body == body && m.Dir == "out" && m.Via == via {
					return true
				}
			}
			return false
		}
	}
	eventually("the phone's message on the laptop, from the phone", fromOther(pl, "from my phone", phone.Address))
	if d, _ := pp.DM(conv); len(d.Messages) != 1 || len(d.Messages[0].Copies) != 2 || d.Messages[0].Via != "" {
		t.Fatalf("the phone's own view (two copies: Bob's device and the laptop): %+v", d.Messages)
	}
	if _, err := pl.SendDM(DMDraft{Conv: conv, Body: "and from my laptop"}); err != nil {
		t.Fatal(err)
	}
	eventually("the laptop's reply on the phone, from the laptop", fromOther(pp, "and from my laptop", laptop.Address))
	eventually("both at Bob", func() bool { d, err := pb.DM(conv); return err == nil && len(d.Messages) == 2 })

	// Removing the phone; the last device stays.
	if _, err := pl.RemoveDevice(phone.Address); err != nil {
		t.Fatal(err)
	}
	if o := over(pl); len(o.Person.Devices) != 1 {
		t.Fatalf("after removing the phone: %+v", o.Person.Devices)
	}
	if _, err := pl.RemoveDevice(laptop.Address); !errors.Is(err, ErrRefused) {
		t.Fatalf("the last device removed: %v", err)
	}

	// A refused request: that device never joins as Alice.
	eventually("another link", func() bool { link, err = pl.NewDeviceLink(); return err == nil })
	tablet, err := client.JoinAndLink(ctx, filepath.Join(t.TempDir(), "tablet"), link.URL, "tablet")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tablet.Close() })
	runDaemon(t, tablet)
	pt := NewLive(tablet)
	eventually("the tablet's request", func() bool { o := over(pl); return len(o.Links) == 1 && o.Links[0].Name == "tablet" })
	if _, err := pl.DecideLink(over(pl).Links[0].ID, false); err != nil {
		t.Fatal(err)
	}
	eventually("the tablet refused", func() bool { o := over(pt); return o.Link != nil && o.Link.State == "refused" && o.Person == nil })
}
