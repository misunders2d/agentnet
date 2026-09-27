package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Blob files live in <data>/blobs as <id>.part while uploading and <id>.blob
// once verified. The Hub only ever holds ciphertext.

func (h *Hub) blobPath(id string, complete bool) string {
	ext := ".part"
	if complete {
		ext = ".blob"
	}
	return filepath.Join(h.cfg.DataDir, "blobs", id+ext)
}

func (h *Hub) removeBlobFiles(ids []string) {
	for _, id := range ids {
		os.Remove(h.blobPath(id, false))
		os.Remove(h.blobPath(id, true))
	}
	if len(ids) > 0 {
		h.cfg.Logf("reclaimed %d abandoned upload(s)", len(ids))
	}
}

// validHex reports whether s is n lowercase hex digits.
func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func blobStatus(id string, b blobRow) protocol.BlobStatus {
	return protocol.BlobStatus{ID: id, Size: b.Size, Received: b.Received, State: b.State}
}

// ownBlob loads a blob the caller uploads; others get 404 so ids stay private.
func (h *Hub) ownBlob(w http.ResponseWriter, id, caller string) (blobRow, bool) {
	b, err := h.store.blob(id)
	if err != nil || b.Owner != caller {
		writeError(w, http.StatusNotFound, "", "unknown upload")
		return b, false
	}
	return b, true
}

func (h *Hub) handleBlobReserve(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.BlobReserve
	if err := decodeStrict(body, &req); err != nil || !validHex(req.ID, 32) || !validHex(req.SHA256, 64) || req.Size <= 0 {
		writeError(w, http.StatusBadRequest, "", "malformed upload reservation")
		return
	}
	if req.Size > protocol.CiphertextBound(h.cfg.MaxFileSize) {
		writeError(w, http.StatusRequestEntityTooLarge, "", "file exceeds the Hub's size limit")
		return
	}
	if rcpt, err := h.store.agent(req.Recipient); err != nil || rcpt.Revoked {
		writeError(w, http.StatusNotFound, "", "unknown or revoked recipient")
		return
	}
	b, stale, err := h.store.reserveBlob(caller, req, h.cfg.StorageQuota, time.Now().Add(-h.cfg.UploadTTL))
	switch {
	case errors.Is(err, errQuota):
		writeError(w, http.StatusRequestEntityTooLarge, "", err.Error())
		return
	case errors.Is(err, errBlobConflict):
		writeError(w, http.StatusConflict, "", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.removeBlobFiles(stale)
	writeJSON(w, http.StatusOK, blobStatus(req.ID, b))
}

func (h *Hub) handleBlobStatus(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if b, ok := h.ownBlob(w, r.PathValue("id"), caller); ok {
		writeJSON(w, http.StatusOK, blobStatus(r.PathValue("id"), b))
	}
}

// handleBlobChunk appends one chunk at exactly the current received offset.
// A chunk is durable (fsynced) before the offset advances.
func (h *Hub) handleBlobChunk(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || len(body) == 0 || len(body) > protocol.ChunkSize {
		writeError(w, http.StatusBadRequest, "", "chunk needs an offset and 1..ChunkSize bytes")
		return
	}
	h.blobMu.Lock()
	defer h.blobMu.Unlock()
	b, ok := h.ownBlob(w, id, caller)
	if !ok {
		return
	}
	if b.State != protocol.BlobUploading || offset != b.Received || offset+int64(len(body)) > b.Size {
		writeError(w, http.StatusConflict, "", "chunk does not continue the upload; fetch its status")
		return
	}
	f, err := os.OpenFile(h.blobPath(id, false), os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		if err = f.Truncate(offset); err == nil { // drop bytes written before a crash
			if _, err = f.WriteAt(body, offset); err == nil {
				err = f.Sync()
			}
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = h.store.setReceived(id, offset+int64(len(body)))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	b.Received = offset + int64(len(body))
	writeJSON(w, http.StatusOK, blobStatus(id, b))
}

// handleBlobComplete verifies the full ciphertext against the reserved size
// and digest, then makes it durable. Repeating it after success is harmless.
func (h *Hub) handleBlobComplete(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	h.blobMu.Lock()
	defer h.blobMu.Unlock()
	b, ok := h.ownBlob(w, id, caller)
	if !ok {
		return
	}
	if b.State == protocol.BlobStored {
		writeJSON(w, http.StatusOK, blobStatus(id, b))
		return
	}
	src := h.blobPath(id, false)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		src = h.blobPath(id, true) // crashed after rename, before recording
	}
	size, sum, err := digestFile(src)
	if err != nil || size != b.Size {
		writeError(w, http.StatusConflict, "", "upload incomplete; fetch its status")
		return
	}
	if sum != b.SHA256 {
		os.Remove(src)
		h.store.setBlobState(id, protocol.BlobUploading)
		writeError(w, http.StatusUnprocessableEntity, "", "uploaded bytes do not match the reserved digest")
		return
	}
	if err := os.Rename(src, h.blobPath(id, true)); err != nil && src != h.blobPath(id, true) {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	syncDir(filepath.Dir(src))
	if err := h.store.setBlobState(id, protocol.BlobStored); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	b.State, b.Received = protocol.BlobStored, b.Size
	writeJSON(w, http.StatusOK, blobStatus(id, b))
}

// handleBlobData serves stored ciphertext, with Range support, only to the
// recipient of a message that carries it.
func (h *Hub) handleBlobData(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	b, err := h.store.blob(id)
	if err != nil || b.Recipient != caller || b.State != protocol.BlobStored || !b.Attached {
		writeError(w, http.StatusNotFound, "", "unknown attachment")
		return
	}
	f, err := os.Open(h.blobPath(id, true))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, f)
}

func digestFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// syncDir makes a rename durable where the platform allows it.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

func (h *Hub) prepareBlobs() error {
	if err := secfile.EnsureDir(filepath.Join(h.cfg.DataDir, "blobs")); err != nil {
		return err
	}
	stale, err := h.store.reclaimStale(time.Now().Add(-h.cfg.UploadTTL))
	h.removeBlobFiles(stale)
	return err
}
