package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestDeviceAdminNoticesEveryOwnDevice(t *testing.T) {
	h, _, _ := testHub(t)
	boss := enrollAdmin(t, h, "boss")
	r := personOf(t, h, boss)
	phone := adminLinkedDevice(t, h, boss, &r, "boss/pixel")
	tablet := adminLinkedDevice(t, h, boss, &r, "boss/tablet")
	other := enroll(t, h, "other")
	personOf(t, h, other)
	for _, change := range []struct {
		by, to member
		admin  bool
	}{{boss, phone, true}, {phone, tablet, true}, {boss, phone, false}} {
		if c, b := change.by.call(t, h, "POST", "/v1/person/device-admin", protocol.DeviceAdminRequest{Address: change.to.addr, Admin: change.admin}); c != http.StatusNoContent {
			t.Fatalf("change: %d %s", c, b)
		}
	}
	for _, m := range []member{boss, phone, tablet} {
		n, err := h.store.deviceAdminNotices(m.addr, 0)
		if err != nil || len(n) != 4 {
			t.Fatalf("%s notices: %+v %v", m.addr, n, err)
		}
		for _, event := range n {
			if event.Valid() != nil || event.Person != r.Person {
				t.Fatalf("invalid notice: %+v", event)
			}
		}
		if !n[0].Admin || n[0].Device != phone.addr || n[0].By != boss.addr || n[2].Admin || n[3].Admin {
			t.Fatalf("wrong changes: %+v", n)
		}
		if tail, err := h.store.deviceAdminNotices(m.addr, n[1].Seq); err != nil || len(tail) != 2 {
			t.Fatalf("cursor: %+v %v", tail, err)
		}
	}
	if n, err := h.store.deviceAdminNotices(other.addr, 0); err != nil || len(n) != 0 {
		t.Fatalf("cross-person leak: %+v %v", n, err)
	}
	// Repeating a withdrawal creates no duplicate and cannot restore descendants.
	if err := h.store.setDeviceAdmin(boss.addr, phone.addr, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.store.deviceAdminNotices(boss.addr, 0); len(n) != 4 {
		t.Fatalf("duplicate withdrawal: %d", len(n))
	}
	// New/replacement keys receive no earlier security history.
	fresh := adminLinkedDevice(t, h, boss, &r, "boss/new")
	if n, err := h.store.deviceAdminNotices(fresh.addr, 0); err != nil || len(n) != 0 {
		t.Fatalf("new device history: %+v %v", n, err)
	}
	id, _ := identity.Generate()
	pub, _ := json.Marshal(id.Public(phone.addr))
	if _, err := h.store.db.Exec(`UPDATE agents SET public=? WHERE address=?`, string(pub), phone.addr); err != nil {
		t.Fatal(err)
	}
	if n, err := h.store.deviceAdminNotices(phone.addr, 0); err != nil || len(n) != 0 {
		t.Fatalf("replacement key: %+v %v", n, err)
	}
}

func TestDeviceAdminNoticeAtomicWithGrant(t *testing.T) {
	h, _, _ := testHub(t)
	boss := enrollAdmin(t, h, "boss")
	r := personOf(t, h, boss)
	phone := adminLinkedDevice(t, h, boss, &r, "boss/phone")
	if _, err := h.store.db.Exec(`DROP TABLE device_admin_notices`); err != nil {
		t.Fatal(err)
	}
	if err := h.store.setDeviceAdmin(boss.addr, phone.addr, true); err == nil {
		t.Fatal("grant without durable notice")
	}
	if a, err := h.store.agent(phone.addr); err != nil || a.Admin {
		t.Fatalf("unreported grant committed: %+v %v", a, err)
	}
}

func TestDeviceAdminNoticeOptedInPush(t *testing.T) {
	h, _, _ := testHub(t)
	boss := enrollAdmin(t, h, "boss")
	r := personOf(t, h, boss)
	phone := adminLinkedDevice(t, h, boss, &r, "boss/phone")
	w := &notifyWorld{h: h}
	w.subscribe(t, phone, "https://fcm.googleapis.com/fcm/send/security-phone")
	w.prefs(t, phone, protocol.NotifyPrefs{Enabled: true}) // no DM sender grant
	push := &fakePush{status: http.StatusCreated}
	h.notifier.send = push.send
	if err := h.store.setDeviceAdmin(boss.addr, phone.addr, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(2 * notifyGrace)
	if _, err := h.notifier.round(context.Background()); err != nil {
		t.Fatal(err)
	} // future deadline stays
	if err := h.notifier.dispatch(context.Background(), phone.addr, now); err != nil {
		t.Fatal(err)
	}
	if calls := push.calls(); len(calls) != 1 || calls[0].payload.Notice != "device_admin" || calls[0].payload.Channel != "" {
		t.Fatalf("security push: %+v", calls)
	}
	var count int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM notify_pending WHERE address=?`, boss.addr).Scan(&count); err != nil || count != 0 {
		t.Fatalf("push without opt-in: %d %v", count, err)
	}
}
