package hub

import (
	"database/sql"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Append after the release's steps. Recipient keys are captured with the
// role transition; replacement or newly linked devices cannot read it.
const deviceAdminNoticeSchema = `
CREATE TABLE device_admin_notices(
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL,
 recipient TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 person TEXT NOT NULL,
 device TEXT NOT NULL,
 by_device TEXT NOT NULL,
 admin INTEGER NOT NULL,
 at INTEGER NOT NULL,
 UNIQUE(id, recipient));
CREATE INDEX device_admin_notice_recipient ON device_admin_notices(recipient, seq);
`
const deviceAdminPushSender = "@device-admin"

// One durable notice per current device, in the role-change transaction.
// Push uses the existing subscription/scheduler and global opt-in; this
// security notice is independent of DM sender permissions and chat mutes.
func addDeviceAdminNotice(tx *sql.Tx, person, device, by string, admin bool) error {
	head, ok, err := personHeadIn(tx, person)
	if err != nil || !ok {
		return err
	}
	r, err := protocol.ParsePersonRoster(head.record)
	if err != nil {
		return err
	}
	if r.Person != person || r.Seq != head.seq || r.Hash() != head.hash {
		return errors.New("conflicting person record")
	}
	id, now := protocol.NewID(), time.Now()
	for _, d := range r.Devices {
		a, err := rawAgentIn(tx, d.Address)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if a.Revoked || a.Pending || a.Person != person || a.Public.Fingerprint() != d.Fingerprint() {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO device_admin_notices(id,recipient,fingerprint,person,device,by_device,admin,at) VALUES(?,?,?,?,?,?,?,?)`, id, d.Address, d.Fingerprint(), person, device, by, admin, now.Unix()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO notify_pending(address,channel,sender,gen,count,last_msg,due_ms,expires_ms)
 SELECT ?, '', ?, 1, 1, ?, ?, ? WHERE EXISTS(SELECT 1 FROM notify_prefs WHERE address=? AND enabled=1) AND EXISTS(SELECT 1 FROM push_subs WHERE address=?)
 ON CONFLICT(address,channel,sender) DO UPDATE SET gen=gen+1,count=count+1,last_msg=excluded.last_msg,expires_ms=excluded.expires_ms`, d.Address, deviceAdminPushSender, id, now.Add(notifyGrace).UnixMilli(), now.Add(notifyLifetime).UnixMilli(), d.Address, d.Address); err != nil {
			return err
		}
	}
	return nil
}

// Each connection replays bounded batches. Clients durably deduplicate by
// id, including dismissed rows, so reconnect/restart never restores a note.
func (s *store) deviceAdminNotices(address string, after int64) ([]protocol.DeviceAdminNotice, error) {
	a, err := rawAgentIn(s.db, address)
	if err != nil {
		return nil, err
	}
	_, current, err := currentPersonDevice(s.db, a)
	if err != nil || !current {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT seq,id,person,device,by_device,admin,at FROM device_admin_notices WHERE recipient=? AND fingerprint=? AND person=? AND seq>? ORDER BY seq LIMIT 100`, address, a.Public.Fingerprint(), a.Person, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.DeviceAdminNotice{}
	for rows.Next() {
		var n protocol.DeviceAdminNotice
		if err := rows.Scan(&n.Seq, &n.ID, &n.Person, &n.Device, &n.By, &n.Admin, &n.At); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
