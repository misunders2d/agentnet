package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestStorageQuotaAdminConfirmationPersistenceAndNoDeletion(t *testing.T) {
	h, admin, other, _ := blobHub(t, 4096)
	if _, err := h.store.db.Exec(`UPDATE agents SET admin=1 WHERE address=?`, admin.addr); err != nil {
		t.Fatal(err)
	}
	id := admin.upload(t, h, other.addr, []byte("retained ciphertext"))
	call := func(who member, quota int64, confirm bool) int {
		status, _ := who.call(t, h, "POST", "/v1/admin/storage/quota", map[string]any{"quota_bytes": quota, "confirm": confirm})
		return status
	}
	if got := call(other, 8192, true); got != 403 {
		t.Fatalf("member quota edit: %d", got)
	}
	if got := call(admin, 8192, false); got != 400 {
		t.Fatalf("unconfirmed quota edit: %d", got)
	}
	if got := call(admin, 0, true); got != 400 {
		t.Fatalf("zero quota edit: %d", got)
	}
	if got := call(admin, 8, true); got != 200 {
		t.Fatalf("confirmed quota edit: %d", got)
	}
	status, raw := admin.call(t, h, "GET", "/v1/storage", nil)
	var v protocol.HubStorage
	if status != 200 || json.Unmarshal(raw, &v) != nil || v.QuotaBytes != 8 {
		t.Fatalf("saved quota not reported: %d %s", status, raw)
	}
	if _, err := h.store.blob(id); err != nil {
		t.Fatal("lowering quota deleted existing file", err)
	}
	if status, _ := admin.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: protocol.NewID(), Recipient: other.addr, Size: 1, SHA256: digest(nil)}); status != 413 {
		t.Fatal("lower quota did not gate new reservation", status)
	}
	cfg := h.cfg
	h.Close()
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, raw = admin.call(t, reopened, "GET", "/v1/storage", nil)
	if status != 200 || json.Unmarshal(raw, &v) != nil || v.QuotaBytes != 8 {
		t.Fatalf("quota lost after restart: %d %s", status, raw)
	}
}

func TestStorageQuotaSurvivesDriveConnectionChanges(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, _, _ := attachmentDriveHub(t, p, false)
	if h.currentStorageQuota() != h.cfg.StorageQuota {
		t.Fatal("unsaved setting lost configured quota")
	}
	if code, raw := a.call(t, h, "POST", "/v1/admin/storage/quota", map[string]any{"quota_bytes": 8192, "confirm": true}); code != 200 {
		t.Fatal(code, string(raw))
	}
	connectRelayDriveFixture(t, h, a)
	if h.currentStorageQuota() != 8192 {
		t.Fatal("Drive connect lost saved quota")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := h.disconnectAttachmentDrive(t.Context())
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "wait for the attachment transfer") || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.currentStorageQuota() != 8192 {
		t.Fatal("Drive disconnect lost saved quota")
	}
}
