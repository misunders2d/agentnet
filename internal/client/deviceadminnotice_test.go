package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestDeviceAdminNoticePersistsDismissesAndAlertsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := identity.Generate()
	address := "owner/laptop"
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Owner", Devices: []identity.Public{id.Public(address)}}
	r.Sign(id.Sign)
	raw, _ := json.Marshal(r)
	if _, err := st.pinChain(r.Person, [][]byte{raw}, id.Public(address), true); err != nil {
		t.Fatal(err)
	}
	alerts := []string{}
	a := &Agent{store: st, Address: address, changes: newChangeFeed(), Logf: t.Logf, wakeWorker: func() {}, notify: func(title, body string, _ []string, _ func()) error { alerts = append(alerts, body); return nil }}
	n := protocol.DeviceAdminNotice{Seq: 1, ID: protocol.NewID(), Person: r.Person, Device: "owner/pixel", By: address, Admin: true, At: 1791214800}
	event, _ := json.Marshal(n)
	if err := a.dispatch(context.Background(), "device_admin", string(event)); err != nil {
		t.Fatal(err)
	}
	if err := a.onDeviceAdminNotice(event); err != nil {
		t.Fatal(err)
	}
	listed, err := a.DeviceAdminNotices()
	if err != nil || len(listed) != 1 {
		t.Fatalf("notices: %+v %v", listed, err)
	}
	a.notifyDeviceAdminNotices()
	a.notifyDeviceAdminNotices()
	if len(alerts) != 1 || !strings.HasPrefix(alerts[0], "Your Pixel can now change company settings — granted from Laptop at ") || strings.Contains(alerts[0], "owner/") {
		t.Fatalf("alerts: %+v", alerts)
	}
	// A foreign person's event creates no notice or authority.
	n.ID = protocol.NewID()
	n.Person = protocol.NewID()
	foreign, _ := json.Marshal(n)
	if err := a.onDeviceAdminNotice(foreign); err != nil {
		t.Fatal(err)
	}
	if listed, _ := a.DeviceAdminNotices(); len(listed) != 1 {
		t.Fatal("foreign notice admitted")
	}
	if err := a.Resolve(listed[0].ID); err != nil {
		t.Fatal(err)
	}
	st.db.Close()
	st, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	a.store = st
	a.notifyTried = nil
	if err := a.onDeviceAdminNotice(event); err != nil {
		t.Fatal(err)
	}
	if listed, err := a.DeviceAdminNotices(); err != nil || len(listed) != 0 {
		t.Fatalf("dismissed note restored: %+v %v", listed, err)
	}
	a.notifyDeviceAdminNotices()
	if len(alerts) != 1 {
		t.Fatal("restart alerted again")
	}
	for _, table := range []string{"inbox", "outbox", "approvals", "task_grants"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("notice changed %s authority: %d %v", table, count, err)
		}
	}
}
