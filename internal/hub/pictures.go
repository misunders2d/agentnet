package hub

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func (h *Hub) pictureBytes(hash string) ([]byte, error) {
	if !protocol.ValidHash(hash) {
		return nil, errors.New("invalid hash")
	}
	b, err := os.ReadFile(filepath.Join(h.cfg.DataDir, "pictures", hash+".png"))
	if err != nil {
		return nil, err
	}
	if err = protocol.ValidatePicture(b); err != nil {
		return nil, err
	}
	if protocol.PictureHash(b) != hash {
		return nil, errors.New("picture hash mismatch")
	}
	return b, nil
}
func (h *Hub) handlePicturePut(w http.ResponseWriter, r *http.Request) {
	caller, b, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	a, err := h.store.agent(caller)
	_, current, proofErr := currentPersonDevice(h.store.db, a)
	if err != nil || proofErr != nil || !current {
		writeError(w, http.StatusForbidden, "", "set up your person first")
		return
	}
	hash := r.PathValue("hash")
	if !protocol.ValidHash(hash) || protocol.PictureHash(b) != hash {
		writeError(w, http.StatusBadRequest, "", "picture hash mismatch")
		return
	}
	if err = protocol.ValidatePicture(b); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	dir := filepath.Join(h.cfg.DataDir, "pictures")
	if err = secfile.EnsureDir(dir); err == nil {
		err = secfile.Write(filepath.Join(dir, hash+".png"), b)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "picture storage failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Pictures are public by the owner's explicit product decision, like names.
func (h *Hub) handlePictureGet(w http.ResponseWriter, r *http.Request) {
	b, err := h.pictureBytes(r.PathValue("hash"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(b)
}
