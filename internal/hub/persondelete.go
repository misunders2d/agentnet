package hub

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Account deletion (Google Play account deletion policy; owner decisions in
// docs/DECISIONS.md, November 25, 2026). A person erases themself from the
// relay from one of their own human devices. All their devices, agent hosts
// included, are revoked and marked deleted; their undelivered messages,
// stored files, profile pictures, Google sign-in mapping, notification data
// and unused invitations are erased. The signed person chain (name, and email
// for Google sign-in) stays: groups verify past history against it, and it is
// disclosed as kept for security. Copies already delivered stay on
// recipients' devices.

var (
	errDeleteNotOwn      = errors.New("only one of the person's own human devices can delete the account")
	errDeleteLastAdmin   = errors.New("you are this workspace's last admin: make someone else an admin first")
	errDeleteWrongPerson = errors.New("name your own person to delete it")
)

func (h *Hub) handlePersonDelete(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.DeletePersonRequest
	if decodeStrict(body, &req) != nil || !protocol.ValidID(req.Person) {
		writeError(w, http.StatusBadRequest, "", "choose your exact person id")
		return
	}
	addresses, pictures, err := h.store.deletePerson(caller, req.Person)
	switch {
	case errors.Is(err, errDeleteNotOwn), errors.Is(err, errDeleteWrongPerson):
		writeError(w, http.StatusForbidden, "", err.Error())
		return
	case errors.Is(err, errDeleteLastAdmin):
		writeError(w, http.StatusConflict, "", err.Error())
		return
	case errors.Is(err, errBadStep):
		writeError(w, http.StatusConflict, "", "person identity records conflict; nothing was deleted")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	// Pictures are files named by hash; one still used by another person stays.
	dir := filepath.Join(h.cfg.DataDir, "pictures")
	for _, hash := range pictures {
		if err := os.Remove(filepath.Join(dir, hash+".png")); err != nil && !errors.Is(err, os.ErrNotExist) {
			h.cfg.Logf("deleting a picture of deleted person %s: %v", req.Person, err)
		}
	}
	for _, address := range addresses {
		h.streams.disconnect(address)
		h.presence.drop(address)
	}
	h.lookAgain()
	h.waiters.notifyAll()
	go h.eraseDeletedFiles(context.Background())
	h.cfg.Logf("person %s deleted their account (%d devices)", req.Person, len(addresses))
	writeJSON(w, http.StatusOK, protocol.DeletePersonResponse{Deleted: addresses})
}

// deletePerson erases person in one transaction and returns its devices and
// the picture files no other person uses. Stored files are left for the
// maintenance cleanup, which also deletes Drive-offloaded copies.
func (s *store) deletePerson(caller, person string) ([]string, []string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	me, err := agentIn(tx, caller)
	if err != nil {
		return nil, nil, err
	}
	if me.Revoked || me.Pending {
		return nil, nil, errDeleteNotOwn
	}
	if me.Person != person {
		return nil, nil, errDeleteWrongPerson
	}
	head, ok, err := personHeadIn(tx, person)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, errDeleteNotOwn
	}
	roster, err := protocol.ParsePersonRoster(head.record)
	if err != nil || roster.Person != person {
		return nil, nil, errBadStep
	}
	if !roster.Human(me.Public.Fingerprint()) {
		return nil, nil, errDeleteNotOwn
	}
	googleLabel := ""
	if roster.Email != "" {
		googleLabel = protocol.GoogleLabel(roster.Email)
	}
	var mapped string
	if err := tx.QueryRow(`SELECT email FROM google_people WHERE person=?`, person).Scan(&mapped); err == nil && mapped != "" {
		googleLabel = protocol.GoogleLabel(mapped)
	}
	rows, err := tx.Query(`SELECT address FROM agents WHERE revoked_at IS NULL AND (person_id=? OR pending_person=? OR (?!='' AND label=?)) ORDER BY address`, person, person, googleLabel, googleLabel)
	if err != nil {
		return nil, nil, err
	}
	addresses := []string{}
	for rows.Next() {
		var address string
		if err = rows.Scan(&address); err != nil {
			rows.Close()
			return nil, nil, err
		}
		addresses = append(addresses, address)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	mine := map[string]bool{}
	admin := false
	for _, address := range addresses {
		mine[address] = true
		a, err := agentIn(tx, address)
		if err != nil {
			return nil, nil, err
		}
		admin = admin || a.Admin
	}
	g, err := googleEmailAdmin(tx, person)
	if err != nil {
		return nil, nil, err
	}
	admin = admin || g
	if admin {
		others, err := otherAdmins(tx, mine, person)
		if err != nil {
			return nil, nil, err
		}
		if !others {
			return nil, nil, errDeleteLastAdmin
		}
	}
	now := time.Now().Unix()
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(addresses)), ",") + ")"
	args := make([]any, len(addresses))
	for i, a := range addresses {
		args[i] = a
	}
	for _, address := range addresses {
		if err := revokeDeviceAdminGrants(tx, address, caller); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(`UPDATE agents SET revoked_at=?,revoked_reason='deleted',admin=0 WHERE address=? AND revoked_at IS NULL`, now, address); err != nil {
			return nil, nil, err
		}
		if err := settleRevokedDevice(tx, address); err != nil {
			return nil, nil, err
		}
		for _, q := range []string{`DELETE FROM caps WHERE address=?`, `DELETE FROM push_subs WHERE address=?`, `DELETE FROM notify_pending WHERE sender=?`} {
			if _, err := tx.Exec(q, address); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(addresses) > 0 {
		// Their undelivered messages are erased, not delivered later; nothing
		// they sent or were sent stays readable here.
		if _, err := expireCustody(tx, "sender IN "+in, args...); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(`UPDATE messages SET envelope=x'' WHERE sender IN `+in+` OR recipient IN `+in, append(args, args...)...); err != nil {
			return nil, nil, err
		}
		// Their stored files are no longer served; eraseDeletedFiles removes
		// them, Drive copies included.
		if _, err := tx.Exec(`UPDATE blobs SET state=?,updated_at=0 WHERE owner IN `+in, append([]any{blobDeleted}, args...)...); err != nil {
			return nil, nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE invites SET expires_at=0 WHERE person=? AND used_by IS NULL`, person); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`DELETE FROM google_people WHERE person=?`, person); err != nil {
		return nil, nil, err
	}
	pictures, err := unsharedPictures(tx, person)
	if err != nil {
		return nil, nil, err
	}
	return addresses, pictures, tx.Commit()
}

// otherAdmins reports whether a current admin remains outside the deleted
// person: an admin device, or a Google sign-in admin with a current device.
func otherAdmins(tx *sql.Tx, mine map[string]bool, person string) (bool, error) {
	rows, err := tx.Query(`SELECT address FROM agents WHERE revoked_at IS NULL AND pending_person IS NULL AND admin=1`)
	if err != nil {
		return false, err
	}
	var candidates []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return false, err
		}
		if !mine[a] {
			candidates = append(candidates, a)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, address := range candidates {
		a, err := agentIn(tx, address)
		if err != nil {
			return false, err
		}
		if a.Admin {
			return true, nil
		}
	}
	var google int
	err = tx.QueryRow(`SELECT count(*) FROM google_people p JOIN google_emails e ON e.email=p.email WHERE p.person<>? AND e.admin=1 AND e.denied=0 AND EXISTS(SELECT 1 FROM agents a WHERE a.person_id=p.person AND a.revoked_at IS NULL)`, person).Scan(&google)
	return google > 0, err
}

// unsharedPictures lists the picture hashes person ever set that no other
// person's record names.
func unsharedPictures(tx *sql.Tx, person string) ([]string, error) {
	rows, err := tx.Query(`SELECT record FROM person_chain WHERE person=?`, person)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var hashes []string
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			rows.Close()
			return nil, err
		}
		if r, err := protocol.ParsePersonRoster([]byte(rec)); err == nil && r.Picture != "" && !seen[r.Picture] {
			seen[r.Picture] = true
			hashes = append(hashes, r.Picture)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := hashes[:0]
	for _, h := range hashes {
		var shared int
		if err := tx.QueryRow(`SELECT count(*) FROM person_chain WHERE person<>? AND instr(record,?)>0`, person, h).Scan(&shared); err != nil {
			return nil, err
		}
		if shared == 0 {
			out = append(out, h)
		}
	}
	return out, nil
}

// blobDeleted marks a stored file of a deleted person: never served again,
// and removed by eraseDeletedFiles.
const blobDeleted = "deleted"

// eraseDeletedFiles removes deleted persons' stored files: the Drive copy
// first when the relay offloaded one, then the local files, then the row. A
// file that cannot be removed yet stays marked for the next pass, at the
// next start or deletion.
func (h *Hub) eraseDeletedFiles(ctx context.Context) {
	rows, err := h.store.db.QueryContext(ctx, `SELECT b.id,b.sha256,b.size,coalesce(d.file_id,'') FROM blobs b LEFT JOIN relay_drive_blobs d ON d.blob_id=b.id WHERE b.state=?`, blobDeleted)
	if err != nil {
		h.cfg.Logf("erasing deleted accounts' files: %v", err)
		return
	}
	type file struct {
		id, sha, remote string
		size            int64
	}
	var files []file
	for rows.Next() {
		var f file
		if err := rows.Scan(&f.id, &f.sha, &f.size, &f.remote); err != nil {
			break
		}
		files = append(files, f)
	}
	rows.Close()
	left := 0
	for _, f := range files {
		if f.remote != "" {
			d := h.attachmentDrive
			if d == nil || d.client == nil {
				left++
				continue
			}
			if err := d.client.DeleteCiphertext(ctx, f.remote, h.realmID, f.id, f.sha, f.size); err != nil {
				h.cfg.Logf("erasing a deleted account's Drive file %s: %v (will retry)", f.id, err)
				left++
				continue
			}
		}
		h.blobMu.Lock()
		err := blobfile.RemoveIfExists(h.blobPath(f.id, false), h.blobPath(f.id, true))
		if err == nil {
			err = h.syncDir(filepath.Join(h.cfg.DataDir, "blobs"))
		}
		if err == nil {
			_, err = h.store.db.ExecContext(ctx, `DELETE FROM blobs WHERE id=? AND state=?`, f.id, blobDeleted)
		}
		h.blobMu.Unlock()
		if err != nil {
			h.cfg.Logf("erasing a deleted account's file %s: %v (will retry)", f.id, err)
			left++
		}
	}
	if len(files) > 0 {
		h.cfg.Logf("erased %d file(s) of deleted accounts; %d left for retry", len(files)-left, left)
	}
}

// upgradeBackupAge is how long the relay keeps its schema-upgrade database
// copies (hub.db.vN.bak): the account deletion page promises that deleted
// data leaves backups within 30 days (owner decision, November 25, 2026).
const upgradeBackupAge = 30 * 24 * time.Hour

// ageOutUpgradeBackups deletes hub.db.vN.bak copies last written before
// upgradeBackupAge. It runs at every start; a failure is logged, never fatal.
func ageOutUpgradeBackups(dir string, now time.Time, logf func(string, ...any)) {
	matches, err := filepath.Glob(filepath.Join(dir, "hub.db.v*.bak"))
	if err != nil {
		return
	}
	for _, path := range matches {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < upgradeBackupAge {
			continue
		}
		if err := os.Remove(path); err != nil {
			logf("removing old upgrade backup %s: %v", filepath.Base(path), err)
			continue
		}
		logf("removed upgrade backup %s, older than 30 days", filepath.Base(path))
	}
}
