package hub

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func adminLinkedDevice(t *testing.T, h *Hub, owner member, roster *protocol.PersonRoster, address string) member {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	d := member{id, address}
	offer := protocol.NewID()
	if c, e := joinLinked(t, h, deviceInvite(t, h, owner, offer), address, offer, *roster, id); c != http.StatusCreated {
		t.Fatalf("join: %d %+v", c, e)
	}
	next := step(*roster, owner, &d, append(append([]identity.Public{}, roster.Devices...), id.Public(address))...)
	if c, e := put(t, h, owner, next); c != http.StatusNoContent {
		t.Fatalf("link: %d %+v", c, e)
	}
	*roster = next
	return d
}

func TestDeviceAdminGrantChainEndsWithGranter(t *testing.T) {
	for _, end := range []string{"demote", "revoke", "unlink"} {
		t.Run(end, func(t *testing.T) {
			h, _, _ := testHub(t)
			boss := enrollAdmin(t, h, "boss")
			r := personOf(t, h, boss)
			phone := adminLinkedDevice(t, h, boss, &r, "boss/phone")
			tablet := adminLinkedDevice(t, h, boss, &r, "boss/tablet")
			if err := h.store.setDeviceAdmin(boss.addr, phone.addr, true); err != nil {
				t.Fatal(err)
			}
			if err := h.store.setDeviceAdmin(phone.addr, tablet.addr, true); err != nil {
				t.Fatal(err)
			}
			switch end {
			case "demote":
				if err := h.store.setDeviceAdmin(boss.addr, phone.addr, false); err != nil {
					t.Fatal(err)
				}
			case "revoke":
				if err := h.store.revoke(phone.addr); err != nil {
					t.Fatal(err)
				}
			case "unlink":
				if c, e := put(t, h, boss, step(r, boss, nil, boss.id.Public(boss.addr), tablet.id.Public(tablet.addr))); c != http.StatusNoContent {
					t.Fatalf("unlink: %d %+v", c, e)
				}
			}
			a, err := h.store.agent(tablet.addr)
			if err != nil || a.Admin {
				t.Fatalf("orphaned admin: %+v %v", a, err)
			}
			if end == "demote" {
				if err := h.store.setDeviceAdmin(boss.addr, phone.addr, true); err != nil {
					t.Fatal(err)
				}
				if a, _ := h.store.agent(tablet.addr); a.Admin {
					t.Fatal("re-grant resurrected descendant")
				}
			}
		})
	}
}

func TestDeviceAdminGrantChecksCurrentExactRoster(t *testing.T) {
	for _, invalid := range []string{"caller-not-admin", "caller-revoked", "target-revoked", "caller-no-person", "caller-not-current", "target-key", "target-person", "target-pending", "conflicting-head"} {
		t.Run(invalid, func(t *testing.T) {
			h, _, _ := testHub(t)
			boss := enrollAdmin(t, h, "boss")
			r := personOf(t, h, boss)
			phone := adminLinkedDevice(t, h, boss, &r, "boss/phone")
			var err error
			switch invalid {
			case "caller-not-admin":
				_, err = h.store.db.Exec(`UPDATE agents SET admin=0 WHERE address=?`, boss.addr)
			case "caller-revoked":
				_, err = h.store.db.Exec(`UPDATE agents SET revoked_at=1 WHERE address=?`, boss.addr)
			case "target-revoked":
				_, err = h.store.db.Exec(`UPDATE agents SET revoked_at=1 WHERE address=?`, phone.addr)
			case "caller-no-person":
				_, err = h.store.db.Exec(`UPDATE agents SET person_id=NULL WHERE address=?`, boss.addr)
			case "target-person":
				_, err = h.store.db.Exec(`UPDATE agents SET person_id='different' WHERE address=?`, phone.addr)
			case "target-pending":
				_, err = h.store.db.Exec(`UPDATE agents SET pending_person=? WHERE address=?`, r.Person, phone.addr)
			case "target-key":
				id, _ := identity.Generate()
				pub, _ := json.Marshal(id.Public(phone.addr))
				_, err = h.store.db.Exec(`UPDATE agents SET public=? WHERE address=?`, string(pub), phone.addr)
			case "caller-not-current":
				r.Devices = []identity.Public{phone.id.Public(phone.addr)}
				raw, _ := json.Marshal(r)
				_, err = h.store.db.Exec(`UPDATE persons SET record=?, hash=? WHERE person=?`, string(raw), r.Hash(), r.Person)
			case "conflicting-head":
				_, err = h.store.db.Exec(`UPDATE persons SET hash='conflicting' WHERE person=?`, r.Person)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.setDeviceAdmin(boss.addr, phone.addr, true); err == nil {
				t.Fatal("invalid grant succeeded")
			}
		})
	}
}

func TestDeviceAdminCannotRemoveInviteAdmin(t *testing.T) {
	h, _, _ := testHub(t)
	boss := enrollAdmin(t, h, "boss")
	r := personOf(t, h, boss)
	phone := adminLinkedDevice(t, h, boss, &r, "boss/phone")
	if err := h.store.setDeviceAdmin(boss.addr, phone.addr, true); err != nil {
		t.Fatal(err)
	}
	if c, _ := phone.call(t, h, "POST", "/v1/admin/revoke", protocol.RevokeRequest{Address: boss.addr}); c != http.StatusForbidden {
		t.Fatalf("invite admin revoke: %d", c)
	}
	if err := h.store.setDeviceAdmin(phone.addr, boss.addr, false); err == nil {
		t.Fatal("invite admin demoted")
	}
	if err := h.store.setDeviceAdmin(phone.addr, phone.addr, true); err == nil {
		t.Fatal("self grant")
	}
}

func TestDeviceAdminRoleFailsClosedWithoutLiveSource(t *testing.T) {
	h, _, _ := testHub(t)
	boss := enrollAdmin(t, h, "boss")
	r := personOf(t, h, boss)
	phone := adminLinkedDevice(t, h, boss, &r, "boss/phone")
	if err := h.store.setDeviceAdmin(boss.addr, phone.addr, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`UPDATE agents SET admin=0 WHERE address=?`, boss.addr); err != nil {
		t.Fatal(err)
	}
	if a, err := h.store.agent(phone.addr); err != nil || a.Admin {
		t.Fatalf("stale source retained role: %+v %v", a, err)
	}
}
