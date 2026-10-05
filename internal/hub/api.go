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
	"github.com/misunders2d/agentnet/internal/ui/static"
)

func (h *Hub) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, protocol.VersionInfo{Version: protocol.Version, Protocol: protocol.ProtocolVersion, Features: features, RealmID: h.RealmID()})
	})
	mux.HandleFunc("POST /v1/join", h.handleJoin)
	mux.HandleFunc("GET /v1/agents", h.handleMembers)
	mux.HandleFunc("GET /v1/agents/{label}/{agent}", h.handleDirectory)
	mux.HandleFunc("GET /v1/agents/{label}/{agent}/sessions", h.handleSessions)
	mux.HandleFunc("GET /v1/agents/{label}/{agent}/profile", h.handleProfile)
	mux.HandleFunc("PUT /v1/person", h.handlePutPerson)
	mux.HandleFunc("POST /v1/person/device-invite", h.handleDeviceInvite)
	mux.HandleFunc("POST /v1/person/device-refuse", h.handleDeviceRefuse)
	mux.HandleFunc("GET /v1/persons/{id}/chain", h.handleChain)
	mux.HandleFunc("PUT /v1/caps", h.handlePutCaps)
	mux.HandleFunc("POST /v1/messages", h.handlePostMessage)
	mux.HandleFunc("POST /v1/signal", h.handleSignal)
	mux.HandleFunc("PUT /v1/agent-catalog", h.handleAgentCatalogPut)
	mux.HandleFunc("GET /v1/agents/{label}/{agent}/agent-catalog", h.handleAgentCatalogGet)
	mux.HandleFunc("GET /v1/messages/{id}", h.handleMessageState)
	mux.HandleFunc("POST /v1/messages/{id}/ack", h.handleAck)
	mux.HandleFunc("GET /v1/messages/{id}/wait", h.handleReceiptWait)
	mux.HandleFunc("GET /v1/stream", h.handleStream)
	mux.HandleFunc("POST /v1/stream/ack", h.handleStreamAck)
	mux.HandleFunc("POST /v1/blobs", h.handleBlobReserve)
	mux.HandleFunc("GET /v1/blobs/{id}", h.handleBlobStatus)
	mux.HandleFunc("PUT /v1/blobs/{id}", h.handleBlobChunk)
	mux.HandleFunc("POST /v1/blobs/{id}/complete", h.handleBlobComplete)
	mux.HandleFunc("GET /v1/blobs/{id}/data", h.handleBlobData)
	mux.HandleFunc("GET /v1/storage", h.handleStorage)                     // hub/storage.go: the caller's own ciphertext usage, read-only
	mux.HandleFunc("GET /v1/storage/drive", h.handleDriveStorageGet)       // hub/drivestorage.go: public Drive storage config (no tokens)
	mux.HandleFunc("PUT /v1/admin/storage/drive", h.handleDriveStoragePut) // admin only, compare-and-swap
	mux.HandleFunc("GET /v1/teams", h.handleTeams)                         // hub/teams.go: signed team directory
	mux.HandleFunc("PUT /v1/team", h.handlePutTeam)
	mux.HandleFunc("GET /v1/teams/{id}/chain", h.handleTeamChain)
	mux.HandleFunc("POST /v1/groups/{id}/chain", h.handleGroupCommit) // hub/groups.go: encrypted signed group journal (admins)
	mux.HandleFunc("GET /v1/groups/{id}/chain", h.handleGroupChain)
	mux.HandleFunc("POST /v1/admin/invites", h.handleInvite)
	mux.HandleFunc("GET /v1/admin/invites", h.handleInvites) // hub/invites.go: any member; the list for admins only
	mux.HandleFunc("POST /v1/admin/invites/revoke", h.handleInviteRevoke)
	mux.HandleFunc("POST /v1/admin/revoke", h.handleRevoke)
	mux.HandleFunc("POST /v1/admin/release", h.handleRelease)
	mux.HandleFunc("GET /v1/release", h.handleReleaseGet)
	mux.HandleFunc("GET /v1/notify", h.handleNotifyInfo)
	mux.HandleFunc("GET /v1/notify/prefs", h.handleNotifyPrefsGet)
	mux.HandleFunc("PUT /v1/notify/prefs", h.handleNotifyPrefsPut)
	mux.HandleFunc("PUT /v1/notify/subscription", h.handlePushSubscribe)
	mux.HandleFunc("DELETE /v1/notify/subscription", h.handlePushUnsubscribe)
	mux.HandleFunc("POST /v1/notify/seen", h.handleNotifySeen)
	if h.cfg.Web {
		relay := http.Handler(static.Relay(filepath.Join(h.cfg.DataDir, "skins")))
		if wrapped, err := static.WithConnectOrigins(relay, h.cfg.BrowserOrigins); err == nil { // validated in Open
			relay = wrapped
		}
		mux.Handle("/", relay)
	}
	var handler http.Handler = mux
	if wrapped, err := WithBrowserOrigins(mux, h.cfg.PublicURL, h.cfg.BrowserOrigins); err == nil { // validated in Open; empty = same-origin only
		handler = wrapped
	}
	return h.countRequests(handler)
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
	var a agent
	sr, err := protocol.ReadSignedRequest(r, func(address string) (ed25519.PublicKey, error) {
		var err error
		if a, err = h.store.agent(address); err != nil {
			return nil, errors.New("unknown agent")
		}
		return a.Public.SignKey, nil
	})
	if err != nil {
		writeError(w, http.StatusUnauthorized, "", err.Error())
		return "", nil, false
	}
	switch {
	case a.Revoked && a.RevokedReason == "refused":
		writeError(w, http.StatusForbidden, protocol.CodeLinkRefused, "the device link was refused")
		return "", nil, false
	case a.Revoked && a.RevokedReason == "expired":
		writeError(w, http.StatusForbidden, protocol.CodeLinkExpired, "nobody approved the device link in time")
		return "", nil, false
	case a.Revoked:
		writeError(w, http.StatusForbidden, protocol.CodeRevoked, "agent revoked")
		return "", nil, false
	case a.Pending && a.PendingUntil <= time.Now().Unix():
		// Nobody approved it in time: it is revoked on its next request.
		if _, err := h.store.endPending(`address = ?`, "expired", a.Public.Address); err != nil {
			writeError(w, http.StatusInternalServerError, "", "storage error")
			return "", nil, false
		}
		writeError(w, http.StatusForbidden, protocol.CodeLinkExpired, "nobody approved the device link in time")
		return "", nil, false
	case a.Pending && r.Pattern != "GET /v1/stream" && r.Pattern != "POST /v1/stream/ack":
		// A device waiting for its person's approval holds only its stream.
		writeError(w, http.StatusForbidden, protocol.CodeLinkPending, "this device waits for approval on its person's other device")
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
	bootstrap, inviter, err := h.store.enroll(req.Secret, req.Public, label, req.Link)
	switch {
	case errors.Is(err, errInviteInvalid):
		writeError(w, http.StatusForbidden, "", err.Error())
		return
	case errors.Is(err, errRosterStale):
		writeError(w, http.StatusConflict, protocol.CodeRosterStale, err.Error())
		return
	case errors.Is(err, errTooManyDevices):
		writeError(w, http.StatusConflict, "", err.Error())
		return
	case errors.Is(err, errAddressTaken):
		writeError(w, http.StatusConflict, protocol.CodeAddressTaken, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if bootstrap {
		os.Remove(filepath.Join(h.cfg.DataDir, BootstrapFile))
	}
	if req.Link != nil {
		h.cfg.Logf("%s joined to be linked; waiting for %s", req.Public.Address, inviter)
		h.linksGen.Add(1)
		h.streams.notify(inviter)
	} else {
		h.cfg.Logf("enrolled %s", req.Public.Address)
		h.membersChanged()
	}
	writeJSON(w, http.StatusCreated, protocol.DirectoryEntry{Public: req.Public})
}

func (h *Hub) handleDirectory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	a, err := h.store.agent(protocol.Address(r.PathValue("label"), r.PathValue("agent")))
	if err != nil || a.Pending {
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
	if err != nil || recipient.Pending {
		writeError(w, http.StatusNotFound, "", "unknown recipient")
		return
	}
	if recipient.Revoked {
		writeError(w, http.StatusForbidden, protocol.CodeRecipientRevoked, "recipient revoked")
		return
	}
	if env.Session != "" && !env.Fallback && !h.presence.live(env.To, env.Session) {
		writeError(w, http.StatusConflict, protocol.CodeSessionExpired, "addressed session is not live")
		return
	}
	// Store a canonical re-encoding so identical retries compare equal.
	canonical, _ := json.Marshal(env)
	state, err := h.store.putMessage(env, canonical, sender.Public.Fingerprint(), time.Now())
	if errors.Is(err, errIDConflict) || errors.Is(err, errBlobNotReady) {
		writeError(w, http.StatusConflict, "", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.stats.Messages.Add(1)
	h.streams.notify(env.To)
	if env.Attn {
		h.notifier.wake()
	}
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
	state, sender, changed, err := h.store.setDisposition(r.PathValue("id"), caller, req.State)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "", "unknown message")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if changed {
		h.streams.notify(sender)
	}
	h.waiters.notify(r.PathValue("id"))
	writeJSON(w, http.StatusOK, protocol.Receipt{ID: r.PathValue("id"), State: state})
}

func (h *Hub) handleInvite(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.InviteRequest
	if err := decodeStrict(body, &req); err != nil || (req.Label == "" && req.Name == "") || (req.Label != "" && !protocol.ValidName(req.Label)) {
		writeError(w, http.StatusBadRequest, "", "invite needs a valid person label or the person's name")
		return
	}
	if !protocol.ValidInviteHint(req.Name, protocol.MaxInviteHint) || !protocol.ValidInviteHint(req.From, protocol.MaxInviteHint) ||
		!protocol.ValidInviteHint(req.Workspace, protocol.MaxWorkspaceHint) {
		writeError(w, http.StatusBadRequest, "", "names on an invitation must be short readable text")
		return
	}
	if req.Browser && (h.certPEM != "" || !h.cfg.Web) {
		writeError(w, http.StatusConflict, "", "this Hub cannot serve browser invitations: that needs hub serve --web and HTTPS that browsers trust (not a certificate pin)")
		return
	}
	if req.TTL <= 0 || req.TTL > protocol.MaxInviteTTL {
		req.TTL = 7 * 24 * time.Hour
	}
	now := time.Now()
	label := req.Label
	if label == "" { // made from the name, never shared with another person (hub/invites.go)
		var err error
		if label, err = h.store.freeInviteLabel(labelFromName(req.Name), now); err != nil {
			writeError(w, http.StatusInternalServerError, "", "storage error")
			return
		}
	}
	secret := protocol.NewID() + protocol.NewID()
	if err := h.store.createNamedInvite(secret, label, req.Name, req.Admin, req.TTL, caller, now); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	code := protocol.Invite{Hub: h.cfg.PublicURL, Label: label, Secret: secret, CertPEM: h.certPEM,
		Name: req.Name, From: req.From, Workspace: req.Workspace}.Encode()
	writeJSON(w, http.StatusCreated, protocol.InviteCreated{Code: code, Label: label})
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
	h.presence.drop(req.Address)
	h.membersChanged()
	h.waiters.notifyAll() // a revoked agent's receipt waits end without a state
	h.cfg.Logf("revoked %s by %s", req.Address, caller)
	writeJSON(w, http.StatusOK, map[string]string{"revoked": req.Address})
}

func (h *Hub) handleSessions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	address := protocol.Address(r.PathValue("label"), r.PathValue("agent"))
	if a, err := h.store.agent(address); err != nil || a.Revoked || a.Pending {
		writeError(w, http.StatusNotFound, "", "unknown agent")
		return
	}
	writeJSON(w, http.StatusOK, h.presence.list(address))
}
