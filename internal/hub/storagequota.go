package hub

import (
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Append a storage setting to the existing relay-storage singleton. Zero
// preserves the operator's configured quota until an admin explicitly changes it.
const storageQuotaSchema = `ALTER TABLE relay_attachment_drive ADD COLUMN quota_bytes INTEGER NOT NULL DEFAULT 0;`

func (h *Hub) loadStorageQuota() error {
	var quota int64
	if err := h.store.db.QueryRow(`SELECT quota_bytes FROM relay_attachment_drive WHERE id=1`).Scan(&quota); err != nil {
		return err
	}
	h.storageQuota.Store(quota)
	return nil
}

func (h *Hub) currentStorageQuota() int64 {
	if quota := h.storageQuota.Load(); quota > 0 {
		return quota
	}
	return h.cfg.StorageQuota
}

func (h *Hub) handleStorageQuota(w http.ResponseWriter, r *http.Request) {
	_, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.StorageQuotaChange
	if decodeStrict(body, &req) != nil || !req.Confirm || req.QuotaBytes <= 0 || req.QuotaBytes > 1<<53-1 {
		writeError(w, http.StatusBadRequest, "", "confirm a positive relay quota in whole bytes")
		return
	}
	h.blobMu.Lock() // the same admission lock as upload reservations
	_, err := h.store.db.ExecContext(r.Context(), `UPDATE relay_attachment_drive SET quota_bytes=? WHERE id=1`, req.QuotaBytes)
	if err == nil {
		h.storageQuota.Store(req.QuotaBytes)
	}
	h.blobMu.Unlock()
	if err != nil {
		writeBlobStorageError(w, err)
		return
	}
	// Lowering a limit only gates new reservations; it never deletes custody.
	h.membersChanged()
	writeJSON(w, http.StatusOK, req)
}
