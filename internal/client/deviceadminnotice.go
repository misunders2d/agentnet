package client

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

const deviceAdminNoticeSchema = `
CREATE TABLE device_admin_notices(
 id TEXT PRIMARY KEY,
 notice TEXT NOT NULL,
 dismissed INTEGER NOT NULL DEFAULT 0,
 notified INTEGER NOT NULL DEFAULT 0);
`

func DeviceAdminNoticeText(n protocol.DeviceAdminNotice) string {
	change, action := "can no longer", "withdrawn"
	if n.Admin {
		change, action = "can now", "granted"
	}
	return fmt.Sprintf("Your %s %s change company settings — %s from %s at %s.", DeviceWords(n.Device), change, action, DeviceWords(n.By), time.Unix(n.At, 0).Local().Format("15:04"))
}

// Only the authenticated Hub stream dispatches this role metadata. It is
// never admitted as a message, approval, job or execution permission.
func (a *Agent) onDeviceAdminNotice(data []byte) error {
	var n protocol.DeviceAdminNotice
	if err := decodeStrict(data, &n); err != nil || n.Valid() != nil {
		return nil
	}
	me, ok, err := a.Person()
	if err != nil {
		return err
	}
	if !ok || me.Person != n.Person {
		return nil
	}
	raw, _ := json.Marshal(n)
	res, err := a.store.db.Exec(`INSERT OR IGNORE INTO device_admin_notices(id,notice) VALUES(?,?)`, n.ID, string(raw))
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count != 0 {
		a.NoteChange()
		a.wakeWorker()
	}
	return nil
}

func (a *Agent) DeviceAdminNotices() ([]protocol.DeviceAdminNotice, error) {
	rows, err := a.store.db.Query(`SELECT notice FROM device_admin_notices WHERE dismissed=0 ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.DeviceAdminNotice{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var n protocol.DeviceAdminNotice
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (a *Agent) dismissDeviceAdminNotice(id string) (bool, error) {
	res, err := a.store.db.Exec(`UPDATE device_admin_notices SET dismissed=1 WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	count, _ := res.RowsAffected()
	if count > 0 {
		a.NoteChange()
	}
	return count > 0, nil
}

func (a *Agent) notifyDeviceAdminNotices() {
	rows, err := a.store.db.Query(`SELECT notice FROM device_admin_notices WHERE dismissed=0 AND notified=0 ORDER BY rowid`)
	if err != nil {
		a.Logf("device admin notices: %v", err)
		return
	}
	var pending []protocol.DeviceAdminNotice
	for rows.Next() {
		var raw string
		var n protocol.DeviceAdminNotice
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return
		}
		if json.Unmarshal([]byte(raw), &n) == nil {
			pending = append(pending, n)
		}
	}
	rows.Close()
	if a.notifyTried == nil {
		a.notifyTried = map[string]bool{}
	}
	for _, n := range pending {
		key := "device-admin:" + n.ID
		if a.notifyTried[key] {
			continue
		}
		a.notifyTried[key] = true
		argv, onClick := a.convClick("")
		if err := a.notify("AgentNet — company settings access", DeviceAdminNoticeText(n), argv, onClick); err != nil {
			a.Logf("device admin notification not shown (%v); notice remains in OKs", err)
			continue
		}
		if _, err := a.store.db.Exec(`UPDATE device_admin_notices SET notified=1 WHERE id=?`, n.ID); err != nil {
			a.Logf("device admin notification state: %v", err)
		}
	}
}
