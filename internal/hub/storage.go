package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// handleStorage is registered as GET /v1/storage by the owning route file.
// Authentication consumes its usual nonce; storage inspection changes no
// blob, message, cleanup state or file and never runs reclamation.
func (h *Hub) handleStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	self, err := h.store.agent(caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage summary unavailable")
		return
	}
	own, err := h.storageUsage(r.Context(), caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage summary unavailable")
		return
	}
	view := protocol.HubStorage{
		Scope: "caller-owned-ciphertext", QuotaScope: "hub-global", Own: own,
		QuotaBytes: h.cfg.StorageQuota, MaxFileBytes: h.cfg.MaxFileSize,
		UploadIdleTTLSeconds: h.cfg.UploadTTL.Seconds(), Location: "Hub data directory / blobs",
		Policy: protocol.HubStoragePolicy{
			DeliveredAttachments:              "Manual operator cleanup only; no automatic expiry. The age is chosen when cleanup is invoked.",
			UnattachedAttachments:             "Completed uploads never attached to a message are removed only by manual operator cleanup.",
			UndeliveredAttachments:            "No automatic deletion; undelivered attachments are kept and excluded from manual delivered cleanup.",
			MessageEnvelopes:                  "No automatic deletion or message-envelope expiry today.",
			IncompleteUploads:                 "Idle unfinished uploads are reclaimed opportunistically at Hub startup and upload reservation, not at a promised deadline; manual cleanup can also reclaim them.",
			ManualDeliveredAgeDefaultSeconds:  (30 * 24 * time.Hour).Seconds(),
			ManualUnattachedAgeDefaultSeconds: (24 * time.Hour).Seconds(),
		},
	}
	if self.Admin {
		global, err := h.storageUsage(r.Context(), "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "", "storage summary unavailable")
			return
		}
		view.Global = &global
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Hub) storageUsage(ctx context.Context, owner string) (protocol.BlobStorageUsage, error) {
	query := `SELECT state, count(*), coalesce(sum(size), 0), coalesce(sum(received), 0) FROM blobs`
	var args []any
	if owner != "" {
		query += ` WHERE owner = ?`
		args = append(args, owner)
	}
	query += ` GROUP BY state`
	rows, err := h.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.BlobStorageUsage{}, err
	}
	defer rows.Close()
	var usage protocol.BlobStorageUsage
	for rows.Next() {
		var state string
		var bucket protocol.StorageBucket
		if err := rows.Scan(&state, &bucket.Files, &bucket.ReservedBytes, &bucket.RecordedReceivedBytes); err != nil {
			return protocol.BlobStorageUsage{}, err
		}
		dst := &usage.Incomplete
		if state == protocol.BlobStored {
			dst = &usage.Stored
		}
		dst.Files += bucket.Files
		dst.ReservedBytes += bucket.ReservedBytes
		dst.RecordedReceivedBytes += bucket.RecordedReceivedBytes
	}
	return usage, rows.Err()
}
