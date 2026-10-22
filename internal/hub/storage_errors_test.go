package hub

import (
	"encoding/json"
	"net/http"
	"syscall"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestAttachmentStorageErrorCodesAndSameIDRecovery(t *testing.T) {
	h, a, b, _ := blobHub(t, 1024)
	assert := func(status int, raw []byte, wantStatus int, wantCode string) {
		t.Helper()
		var v protocol.Error
		if json.Unmarshal(raw, &v) != nil || status != wantStatus || v.Code != wantCode {
			t.Fatalf("storage error %d %s, want %d %s", status, raw, wantStatus, wantCode)
		}
	}
	tooBig := protocol.BlobReserve{ID: protocol.NewID(), Recipient: b.addr, Size: protocol.CiphertextBound(h.cfg.MaxFileSize) + 1, SHA256: digest(nil)}
	status, raw := a.call(t, h, "POST", "/v1/blobs", tooBig)
	assert(status, raw, http.StatusRequestEntityTooLarge, "file_too_large")
	data := []byte("ciphertext preserved through disk-full recovery")
	req := protocol.BlobReserve{ID: protocol.NewID(), Recipient: b.addr, Size: int64(len(data)), SHA256: digest(data)}
	full := req
	full.ID, full.Size = protocol.NewID(), 2048
	status, raw = a.call(t, h, "POST", "/v1/blobs", full)
	assert(status, raw, http.StatusRequestEntityTooLarge, "relay_quota")
	if status, raw = a.call(t, h, "POST", "/v1/blobs", req); status != 200 {
		t.Fatalf("reserve: %d %s", status, raw)
	}
	if status, raw = a.call(t, h, "PUT", "/v1/blobs/"+req.ID+"?offset=0", data); status != 200 {
		t.Fatalf("chunk: %d %s", status, raw)
	}
	original := h.syncDir
	h.syncDir = func(string) error { return syscall.ENOSPC }
	status, raw = a.call(t, h, "POST", "/v1/blobs/"+req.ID+"/complete", nil)
	assert(status, raw, http.StatusInsufficientStorage, "relay_disk_full")
	h.syncDir = original
	status, raw = a.call(t, h, "POST", "/v1/blobs/"+req.ID+"/complete", nil)
	if status != 200 {
		t.Fatalf("same-ID complete after disk recovery: %d %s", status, raw)
	}
	var count int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM blobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("storage failure minted or lost upload", count, err)
	}
}
