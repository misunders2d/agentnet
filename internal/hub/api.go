package hub

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (h *Hub) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/join", h.handleJoin)
	mux.HandleFunc("GET /v1/agents/{label}/{agent}", h.handleDirectory)
	mux.HandleFunc("POST /v1/messages", h.handlePostMessage)
	mux.HandleFunc("GET /v1/messages/{id}", h.handleMessageState)
	mux.HandleFunc("POST /v1/messages/{id}/ack", h.handleAck)
	mux.HandleFunc("GET /v1/stream", h.handleStream)
	mux.HandleFunc("POST /v1/blobs", h.handleBlobReserve)
	mux.HandleFunc("GET /v1/blobs/{id}", h.handleBlobStatus)
	mux.HandleFunc("PUT /v1/blobs/{id}", h.handleBlobChunk)
	mux.HandleFunc("POST /v1/blobs/{id}/complete", h.handleBlobComplete)
	mux.HandleFunc("GET /v1/blobs/{id}/data", h.handleBlobData)
	mux.HandleFunc("POST /v1/admin/invites", h.handleInvite)
	mux.HandleFunc("POST /v1/admin/revoke", h.handleRevoke)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, protocol.Error{Code: code, Error: msg})
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// authenticate verifies the request signature, freshness and nonce, and that
// the caller is an active agent. On failure it writes the response.
func (h *Hub) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	caller, _, ok := h.authenticateBody(w, r)
	return caller, ok
}

// authenticateBody is authenticate that also returns the verified body.
func (h *Hub) authenticateBody(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	revoked := false
	sr, err := protocol.ReadSignedRequest(r, func(address string) (ed25519.PublicKey, error) {
		a, err := h.store.agent(address)
		if err != nil {
			return nil, errors.New("unknown agent")
		}
		revoked = a.Revoked
		return a.Public.SignKey, nil
	})
	if err != nil {
		writeError(w, http.StatusUnauthorized, "", err.Error())
		return "", nil, false
	}
	if revoked {
		writeError(w, http.StatusForbidden, protocol.CodeRevoked, "agent revoked")
		return "", nil, false
	}
	fresh, err := h.store.useNonce(sr.Agent, sr.Nonce, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return "", nil, false
	}
	if !fresh {
		writeError(w, http.StatusUnauthorized, "", "replayed request")
		return "", nil, false
	}
	return sr.Agent, sr.Body, true
}

func (h *Hub) requireAdmin(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return "", nil, false
	}
	a, err := h.store.agent(caller)
	if err != nil || !a.Admin {
		writeError(w, http.StatusForbidden, "", "admin only")
		return "", nil, false
	}
	return caller, body, true
}

func (h *Hub) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req protocol.JoinRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxBody))
	if err == nil {
		err = decodeStrict(body, &req)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "", "malformed join request")
		return
	}
	label, _, err := protocol.SplitAddress(req.Public.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	if err := protocol.VerifyJoin(req); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	bootstrap, err := h.store.enroll(req.Secret, req.Public, label)
	switch {
	case errors.Is(err, errInviteInvalid):
		writeError(w, http.StatusForbidden, "", err.Error())
		return
	case errors.Is(err, errAddressTaken):
		writeError(w, http.StatusConflict, "", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if bootstrap {
		os.Remove(filepath.Join(h.cfg.DataDir, BootstrapFile))
	}
	h.cfg.Logf("enrolled %s", req.Public.Address)
	writeJSON(w, http.StatusCreated, protocol.DirectoryEntry{Public: req.Public})
}

func (h *Hub) handleDirectory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	a, err := h.store.agent(protocol.Address(r.PathValue("label"), r.PathValue("agent")))
	if err != nil {
		writeError(w, http.StatusNotFound, "", "unknown agent")
		return
	}
	writeJSON(w, http.StatusOK, protocol.DirectoryEntry{Public: a.Public, Revoked: a.Revoked})
}

func (h *Hub) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var env envelope.Envelope
	if err := decodeStrict(body, &env); err != nil {
		writeError(w, http.StatusBadRequest, "", "malformed envelope")
		return
	}
	if env.From != caller {
		writeError(w, http.StatusForbidden, "", "envelope sender must be the caller")
		return
	}
	sender, err := h.store.agent(caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if err := env.VerifySig(sender.Public.SignKey); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	recipient, err := h.store.agent(env.To)
	if err != nil {
		writeError(w, http.StatusNotFound, "", "unknown recipient")
		return
	}
	if recipient.Revoked {
		writeError(w, http.StatusForbidden, protocol.CodeRecipientRevoked, "recipient revoked")
		return
	}
	// Store a canonical re-encoding so identical retries compare equal.
	canonical, _ := json.Marshal(env)
	state, err := h.store.putMessage(env, canonical)
	if errors.Is(err, errIDConflict) || errors.Is(err, errBlobNotReady) {
		writeError(w, http.StatusConflict, "", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.streams.notify(env.To)
	writeJSON(w, http.StatusAccepted, protocol.Receipt{ID: env.ID, State: state})
}

func (h *Hub) handleMessageState(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	_, _, state, err := h.store.messageState(r.PathValue("id"), caller)
	if err != nil {
		writeError(w, http.StatusNotFound, "", "unknown message")
		return
	}
	writeJSON(w, http.StatusOK, protocol.Receipt{ID: r.PathValue("id"), State: state})
}

func (h *Hub) handleAck(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.AckRequest
	if err := decodeStrict(body, &req); err != nil || (req.State != protocol.StateDelivered && req.State != protocol.StateQuarantined) {
		writeError(w, http.StatusBadRequest, "", "ack state must be delivered or quarantined")
		return
	}
	state, err := h.store.setDisposition(r.PathValue("id"), caller, req.State)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "", "unknown message")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	writeJSON(w, http.StatusOK, protocol.Receipt{ID: r.PathValue("id"), State: state})
}

func (h *Hub) handleInvite(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.InviteRequest
	if err := decodeStrict(body, &req); err != nil || !protocol.ValidName(req.Label) {
		writeError(w, http.StatusBadRequest, "", "invite needs a valid person label")
		return
	}
	if req.TTL <= 0 || req.TTL > 30*24*time.Hour {
		req.TTL = 7 * 24 * time.Hour
	}
	secret := protocol.NewID() + protocol.NewID()
	if err := h.store.createInvite(secret, req.Label, req.Admin, req.TTL, caller); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	code := protocol.Invite{Hub: h.cfg.PublicURL, Label: req.Label, Secret: secret, CertPEM: h.certPEM}.Encode()
	writeJSON(w, http.StatusCreated, map[string]string{"code": code})
}

func (h *Hub) handleRevoke(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.RevokeRequest
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "", "malformed revoke request")
		return
	}
	if req.Address == caller {
		writeError(w, http.StatusBadRequest, "", "an admin cannot revoke itself")
		return
	}
	if err := h.store.revoke(req.Address); err != nil {
		writeError(w, http.StatusNotFound, "", "unknown or already revoked agent")
		return
	}
	h.streams.disconnect(req.Address)
	h.cfg.Logf("revoked %s by %s", req.Address, caller)
	writeJSON(w, http.StatusOK, map[string]string{"revoked": req.Address})
}
