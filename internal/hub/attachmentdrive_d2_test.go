package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/gdrive"
)

func awaitRelayDriveStatus(t *testing.T, h *Hub, match func(drivecontract.RelayStorageStatus) bool) drivecontract.RelayStorageStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := h.attachmentStorageStatus(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		if match(status) {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected attachment status: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func connectRelayDriveFixture(t *testing.T, h *Hub, admin member) {
	t.Helper()
	link, err := h.beginAttachmentConsent(t.Context(), admin.addr)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	w := serve(h, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code=fixture", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool { return s.Connected && !s.Migration.Running })
}

func TestRelayAttachmentDriveMissingChecksumRetainsLocalUntilMove(t *testing.T) {
	p := newRelayDriveProvider(t)
	p.omitChecksum = true
	h, a, b, _ := attachmentDriveHub(t, p, true)
	data := []byte("encrypted bytes whose provider checksum is unavailable")
	id := a.upload(t, h, b.addr, data)
	awaitAttachmentState(t, h, id, "failed")
	awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool {
		return s.State == "offline" && strings.Contains(s.Notice, "checksum")
	})
	if err := blobfile.Check(h.blobPath(id, true), int64(len(data)), digest(data)); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	calls, creates := p.metadataCalls, p.creates
	if p.mediaBytes != 0 || p.workerDeadline {
		t.Fatal("eviction downloaded bytes or imposed a whole-upload deadline")
	}
	p.mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	p.mu.Lock()
	if p.metadataCalls != calls {
		t.Error("missing checksum caused idle provider retries")
	}
	p.omitChecksum = false
	p.mu.Unlock()
	newID := a.upload(t, h, b.addr, []byte("an independent valid upload"))
	awaitAttachmentState(t, h, newID, "remote")
	awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool {
		return s.State == "offline" && s.Pending.Files == 1 && s.Notice != ""
	})
	p.mu.Lock()
	creates = p.creates
	p.mu.Unlock()
	if err := h.moveExistingAttachments(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitAttachmentState(t, h, id, "remote")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.creates != creates || p.mediaBytes != 0 {
		t.Fatal("retry duplicated or downloaded the metadata-verified object")
	}
}

func TestRelayAttachmentDriveRateLimitsRetryButQuotaWaitsForMove(t *testing.T) {
	for _, reason := range []string{"rateLimitExceeded", "userRateLimitExceeded", "storageQuotaExceeded"} {
		t.Run(reason, func(t *testing.T) {
			p := newRelayDriveProvider(t)
			p.uploadReason = reason
			h, a, b, _ := attachmentDriveHub(t, p, true)
			id := a.upload(t, h, b.addr, []byte("encrypted quota fixture"))
			if reason != "storageQuotaExceeded" {
				awaitAttachmentState(t, h, id, "retry")
				awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool { return s.State == "offline" })
				p.mu.Lock()
				p.uploadReason = ""
				p.mu.Unlock()
				awaitAttachmentState(t, h, id, "remote") // no new admin action or upload
				return
			}
			awaitAttachmentState(t, h, id, "failed")
			awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool {
				return s.State == "offline" && strings.Contains(s.Notice, "storage is full")
			})
			p.mu.Lock()
			p.uploadReason = ""
			calls := p.uploadCalls
			p.mu.Unlock()
			id2 := a.upload(t, h, b.addr, []byte("local custody while Drive is full"))
			time.Sleep(80 * time.Millisecond)
			p.mu.Lock()
			after := p.uploadCalls
			p.mu.Unlock()
			if calls != after {
				t.Fatal("full Drive spun or new upload bypassed the pause")
			}
			if _, err := os.Stat(h.blobPath(id2, true)); err != nil {
				t.Fatal("full Drive lost local custody", err)
			}
			if err := h.moveExistingAttachments(t.Context()); err != nil {
				t.Fatal(err)
			}
			awaitAttachmentState(t, h, id, "remote")
			awaitAttachmentState(t, h, id2, "remote")
		})
	}
}

func TestRelayAttachmentDriveCleanupContinuesAfterRemoteFailure(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, closeHub := attachmentDriveHub(t, p, false)
	local := a.upload(t, h, b.addr, []byte("existing local ciphertext"))
	connectRelayDriveFixture(t, h, a)
	blocked := a.upload(t, h, b.addr, []byte("remote ciphertext retained on provider error"))
	other := a.upload(t, h, b.addr, []byte("independent remote ciphertext"))
	awaitAttachmentState(t, h, blocked, "remote")
	awaitAttachmentState(t, h, other, "remote")
	var fileID string
	if err := h.store.db.QueryRow(`SELECT file_id FROM relay_drive_blobs WHERE blob_id=?`, blocked).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`UPDATE blobs SET updated_at=?`, time.Now().Add(-48*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.deleteFailures = map[string]bool{fileID: true}
	p.mu.Unlock()
	closeHub()
	m, err := OpenMaintenance(h.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.attachmentHTTP = p.http()
	removed, err := m.Cleanup(time.Hour, time.Hour, time.Hour)
	if removed.Files != 2 || err == nil || !strings.Contains(err.Error(), "retained 1 Drive attachment") {
		t.Fatal(removed, err)
	}
	for _, id := range []string{local, other} {
		if _, err := m.store.blob(id); err == nil {
			t.Fatal("independent cleanup was blocked", id)
		}
	}
	if _, err := m.store.blob(blocked); err != nil {
		t.Fatal("failed remote victim row was removed", err)
	}
	p.mu.Lock()
	p.deleteFailures[fileID] = false
	p.mu.Unlock()
	removed, err = m.Cleanup(time.Hour, time.Hour, time.Hour)
	if err != nil || removed.Files != 1 {
		t.Fatal(removed, err)
	}
}

func TestRelayAttachmentDriveBackupRejectsChangedRemoteBytes(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, closeHub := attachmentDriveHub(t, p, true)
	id := a.upload(t, h, b.addr, []byte("encrypted bytes with valid provider metadata"))
	awaitAttachmentState(t, h, id, "remote")
	p.mu.Lock()
	p.corruptRead = true
	p.mu.Unlock()
	closeHub()
	m, err := OpenMaintenance(h.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.attachmentHTTP = p.http()
	if err := m.Backup(io.Discard); !errors.Is(err, gdrive.ErrCiphertext) {
		t.Fatal("portable backup accepted ciphertext that disagreed with its SHA-256", err)
	}
}

func TestRelayAttachmentDriveInvalidResumedRemoteDoesNotStarveQueue(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "missing"}[missing], func(t *testing.T) {
			p := newRelayDriveProvider(t)
			h, a, b, _ := attachmentDriveHub(t, p, false)
			data := []byte("verified local ciphertext survives bad remote state")
			bad := a.upload(t, h, b.addr, data)
			good := a.upload(t, h, b.addr, data)
			if good < bad {
				bad, good = good, bad
			}
			connectRelayDriveFixture(t, h, a)
			const fileID = "retained-original-file-id"
			if !missing {
				p.mu.Lock()
				p.files[fileID] = relayFakeFile{data: data, meta: map[string]any{"id": fileID, "size": strconv.Itoa(len(data)), "mimeType": "application/octet-stream", "sha256Checksum": strings.Repeat("0", 64)}}
				p.mu.Unlock()
			}
			h.attachmentDrive.mu.Lock()
			_, err := h.store.db.Exec(`INSERT INTO relay_drive_blobs(blob_id,file_id,state,local_present) VALUES(?,?,'remote',1),(?,'','pending',1)`, bad, fileID, good)
			h.attachmentDrive.signal()
			h.attachmentDrive.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			awaitAttachmentState(t, h, good, "remote")
			awaitAttachmentState(t, h, bad, "failed")
			if err := blobfile.Check(h.blobPath(bad, true), int64(len(data)), digest(data)); err != nil {
				t.Fatal(err)
			}
			var retained string
			if err := h.store.db.QueryRow(`SELECT file_id FROM relay_drive_blobs WHERE blob_id=?`, bad).Scan(&retained); err != nil || retained != fileID {
				t.Fatal("repair discarded account/cleanup binding", retained, err)
			}
		})
	}
}

func TestRelayAttachmentDriveMembersCannotSeeLocalUsage(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, false)
	a.upload(t, h, b.addr, bytes.Repeat([]byte("ciphertext"), 50))
	admin, err := h.attachmentStorageStatus(t.Context(), true)
	if err != nil || admin.Local.Files != 1 || admin.Local.Bytes == 0 {
		t.Fatal(admin, err)
	}
	code, raw := b.call(t, h, "GET", "/v1/storage/attachments", nil)
	var member drivecontract.RelayStorageStatus
	if code != 200 || json.Unmarshal(raw, &member) != nil || member.Local != (drivecontract.RelayAmount{}) || member.Remote != (drivecontract.RelayAmount{}) || member.Pending != (drivecontract.RelayAmount{}) || member.RemoteOnlyFiles != 0 || member.Migration != (drivecontract.RelayMigration{}) {
		t.Fatal("relay-wide usage exposed", code, string(raw))
	}
}

func TestRelayAttachmentDriveTransientResumeRetriesQueue(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, false)
	data := []byte("verified local copy after crash before eviction")
	resumed := a.upload(t, h, b.addr, data)
	queued := a.upload(t, h, b.addr, data)
	if queued < resumed {
		resumed, queued = queued, resumed
	}
	connectRelayDriveFixture(t, h, a)
	const fileID = "original-resumed-file"
	p.mu.Lock()
	p.files[fileID] = relayFakeFile{data: data, meta: map[string]any{"id": fileID, "size": strconv.Itoa(len(data)), "mimeType": "application/octet-stream", "sha256Checksum": digest(data), "appProperties": map[string]string{"agentnet_realm": h.realmID, "agentnet_blob": resumed, "agentnet_sha256": digest(data)}}}
	p.metadataFailures = map[string]bool{fileID: true}
	p.mu.Unlock()
	h.attachmentDrive.mu.Lock()
	_, err := h.store.db.Exec(`INSERT INTO relay_drive_blobs(blob_id,file_id,state,local_present) VALUES(?,?,'remote',1),(?,'','pending',1)`, resumed, fileID, queued)
	h.attachmentDrive.signal()
	h.attachmentDrive.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	awaitAttachmentState(t, h, resumed, "retry")
	if err := blobfile.Check(h.blobPath(resumed, true), int64(len(data)), digest(data)); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.metadataFailures[fileID] = false
	p.mu.Unlock()
	// Recovery supplies no fresh upload, reconnect, Move or process restart.
	awaitAttachmentState(t, h, resumed, "remote")
	awaitAttachmentState(t, h, queued, "remote")
	var retained string
	if err := h.store.db.QueryRow(`SELECT file_id FROM relay_drive_blobs WHERE blob_id=?`, resumed).Scan(&retained); err != nil || retained != fileID {
		t.Fatal("retry changed the stable provider ID", retained, err)
	}
}

func TestRelayAttachmentDriveConsentNetworkDoesNotBlockLocalCustody(t *testing.T) {
	for _, path := range []string{"/files/generateIds", "/files"} {
		t.Run(map[string]string{"/files/generateIds": "generate_id", "/files": "ensure_folder"}[path], func(t *testing.T) {
			p := newRelayDriveProvider(t)
			h, a, b, _ := attachmentDriveHub(t, p, false)
			p.mu.Lock()
			p.blockPath = path
			p.blocked = make(chan struct{}, 1)
			p.release = make(chan struct{})
			blocked, release := p.blocked, p.release
			p.mu.Unlock()
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			link, err := h.beginAttachmentConsent(t.Context(), a.addr)
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(link)
			callback := make(chan int, 1)
			go func() {
				w := serve(h, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code=fixture", nil))
				callback <- w.Code
			}()
			select {
			case <-blocked:
			case <-time.After(time.Second):
				t.Fatal("consent did not reach provider gate")
			}
			uploaded := make(chan string, 1)
			data := []byte("local custody during slow OAuth folder setup")
			go func() { uploaded <- a.upload(t, h, b.addr, data) }()
			var id string
			select {
			case id = <-uploaded:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("OAuth network held the local custody lock")
			}
			if err := blobfile.Check(h.blobPath(id, true), int64(len(data)), digest(data)); err != nil {
				t.Fatal(err)
			}
			if path == "/files/generateIds" {
				if _, err := h.store.db.Exec(`UPDATE agents SET revoked_at=? WHERE address=?`, time.Now().Unix(), a.addr); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			select {
			case code := <-callback:
				want := 200
				if path == "/files/generateIds" {
					want = 400
				}
				if code != want {
					t.Fatal("authority changed during provider work", code, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("consent did not finish after provider recovery")
			}
		})
	}
}
