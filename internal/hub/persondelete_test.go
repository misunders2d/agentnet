package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A person deletes their own account: every device is revoked and marked
// deleted, undelivered messages and stored files are erased, the Google
// mapping and invitations go, and the signed chain stays.
func TestPersonDeleteErasesOwnAccount(t *testing.T) {
	h, boss := adminHub(t)
	owner := joinMember(t, h, "owner")
	person := personOf(t, h, owner)
	phone := adminLinkedDevice(t, h, owner, &person, "owner/phone")
	blob := owner.upload(t, h, boss.addr, []byte("a file only the owner sent"))
	if _, err := h.store.db.Exec(`INSERT INTO messages(id,sender,recipient,envelope,state,created_at) VALUES(?,?,?,?,?,?)`, protocol.NewID(), owner.addr, boss.addr, []byte("ciphertext"), protocol.StateCustody, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`INSERT INTO google_people(email,subject,person) VALUES('owner@example.test','s',?)`, person.Person); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`INSERT INTO invites(secret_hash,label,admin,expires_at,created_by) VALUES('owner-made','guest',0,?,?)`, time.Now().Add(time.Hour).Unix(), owner.addr); err != nil {
		t.Fatal(err)
	}
	if code, body := owner.call(t, h, "POST", "/v1/person/delete", protocol.DeletePersonRequest{Person: protocol.NewID()}); code != 403 {
		t.Fatalf("another person: %d %s", code, body)
	}
	if code, body := boss.call(t, h, "POST", "/v1/person/delete", protocol.DeletePersonRequest{Person: person.Person}); code != 403 {
		t.Fatalf("someone else's device deleted the person: %d %s", code, body)
	}
	code, body := owner.call(t, h, "POST", "/v1/person/delete", protocol.DeletePersonRequest{Person: person.Person})
	var response protocol.DeletePersonResponse
	if code != 200 || json.Unmarshal(body, &response) != nil || !slices.Equal(response.Deleted, slices.Sorted(slices.Values([]string{owner.addr, phone.addr}))) {
		t.Fatalf("delete: %d %s", code, body)
	}
	for _, d := range []member{owner, phone} {
		a, err := h.store.agent(d.addr)
		if err != nil || !a.Revoked || a.RevokedReason != "deleted" {
			t.Fatalf("device %s not deleted: %+v %v", d.addr, a, err)
		}
	}
	if code, body := owner.call(t, h, "POST", "/v1/person/delete", protocol.DeletePersonRequest{Person: person.Person}); code != 403 || !strings.Contains(string(body), "this account was deleted") {
		t.Fatalf("a deleted device still answered: %d %s", code, body)
	}
	var custody, kept int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM messages WHERE sender=? AND state=?`, owner.addr, protocol.StateCustody).Scan(&custody); err != nil || custody != 0 {
		t.Fatalf("undelivered message still in custody: %d %v", custody, err)
	}
	if err := h.store.db.QueryRow(`SELECT count(*) FROM messages WHERE (sender=? OR recipient=?) AND length(envelope)>0`, owner.addr, owner.addr).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("message ciphertext kept: %d %v", kept, err)
	}
	var google int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM google_people WHERE person=?`, person.Person).Scan(&google); err != nil || google != 0 {
		t.Fatalf("Google mapping kept: %d %v", google, err)
	}
	var invite int64
	if err := h.store.db.QueryRow(`SELECT expires_at FROM invites WHERE secret_hash='owner-made'`).Scan(&invite); err != nil || invite != 0 {
		t.Fatalf("an unused invitation they created stays valid: %d %v", invite, err)
	}
	if _, ok, err := personHeadIn(h.store.db, person.Person); err != nil || !ok {
		t.Fatalf("the signed chain must stay: %v %v", ok, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		h.store.db.QueryRow(`SELECT count(*) FROM blobs WHERE id=?`, blob).Scan(&n)
		_, statErr := os.Stat(filepath.Join(h.cfg.DataDir, "blobs", blob+".blob"))
		if n == 0 && os.IsNotExist(statErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored file not erased: row=%d file=%v", n, statErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The last admin hands admin over before deleting their account.
func TestPersonDeleteRefusesLastAdmin(t *testing.T) {
	h, boss := adminHub(t)
	person := personOf(t, h, boss)
	if code, body := boss.call(t, h, "POST", "/v1/person/delete", protocol.DeletePersonRequest{Person: person.Person}); code != 409 || !strings.Contains(string(body), "last admin") {
		t.Fatalf("last admin deleted: %d %s", code, body)
	}
	if a, err := h.store.agent(boss.addr); err != nil || a.Revoked {
		t.Fatalf("refused deletion changed the admin: %+v %v", a, err)
	}
}

func TestUpgradeBackupsAgeOut(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	for name, age := range map[string]time.Duration{"hub.db.v3.bak": 31 * 24 * time.Hour, "hub.db.v4.bak": time.Hour, "hub.db": 90 * 24 * time.Hour} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	ageOutUpgradeBackups(dir, now, t.Logf)
	for name, want := range map[string]bool{"hub.db.v3.bak": false, "hub.db.v4.bak": true, "hub.db": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Fatalf("%s present=%v, want %v", name, err == nil, want)
		}
	}
}

// An account deleted while one of its files is uploading to Drive leaves no
// Drive copy behind: the upload commits only while the file is still stored
// and removes its own copy otherwise (the mapping row cascades away).
func TestPersonDeleteDuringDriveUploadLeavesNoCopy(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, true)
	h.attachmentDrive.close() // drive the copy by hand
	id := a.upload(t, h, b.addr, []byte("a file whose owner deletes the account mid-upload"))
	p.mu.Lock()
	p.blockPath = "/files"
	p.blocked = make(chan struct{}, 1)
	p.release = make(chan struct{})
	blocked, release := p.blocked, p.release
	p.mu.Unlock()
	copied := make(chan error, 1)
	go func() { _, err := h.copyAttachmentDrive(t.Context(), id); copied <- err }()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach the provider")
	}
	if _, err := h.store.db.Exec(`UPDATE blobs SET state=? WHERE id=?`, blobDeleted, id); err != nil {
		t.Fatal(err)
	}
	h.eraseDeletedFiles(t.Context())
	p.mu.Lock()
	p.blockPath = ""
	p.mu.Unlock()
	close(release)
	if err := <-copied; err == nil {
		t.Fatal("an upload of an erased file committed")
	}
	if _, err := h.store.blob(id); err == nil {
		t.Fatal("the erased file's row is still there")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for fid := range p.files {
		if fid != "fixture-folder" {
			t.Fatalf("Drive copy %s of an erased file remains", fid)
		}
	}
}

// With the Drive worker running, a file erased mid-upload neither pauses
// Drive nor stops other people's transfers, and its copy does not remain.
func TestPersonDeleteMidUploadKeepsDriveRunning(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, false)
	connectRelayDriveFixture(t, h, a)
	p.mu.Lock()
	p.blockPath = "/files"
	p.blocked = make(chan struct{}, 1)
	p.release = make(chan struct{})
	blocked, release := p.blocked, p.release
	p.mu.Unlock()
	erased := a.upload(t, h, b.addr, []byte("a file whose owner deletes the account mid-upload"))
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach the provider")
	}
	if _, err := h.store.db.Exec(`UPDATE blobs SET state=? WHERE id=?`, blobDeleted, erased); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.blockPath = ""
	p.mu.Unlock()
	close(release)
	// The pass that carried the erased file ends without pausing Drive.
	status := awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool { return !s.Migration.Running })
	if status.State != "connected" || status.Notice != "" {
		t.Fatalf("an erased file paused Drive: %+v", status)
	}
	other := b.upload(t, h, a.addr, []byte("someone else's file"))
	awaitAttachmentState(t, h, other, "remote")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := h.store.blob(erased); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the erased file was not erased")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var otherID string
	if err := h.store.db.QueryRow(`SELECT file_id FROM relay_drive_blobs WHERE blob_id=?`, other).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	c, err := h.store.relayDriveConnection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for fid := range p.files {
		if fid != c.FolderID && fid != otherID {
			t.Fatalf("Drive copy %s of an erased file remains", fid)
		}
	}
}
