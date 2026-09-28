package client

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/tlscert"
)

// RunOptions configure the daemon.
type RunOptions struct {
	// Listen is the address to accept direct deliveries on (e.g. ":7443").
	// Empty disables direct delivery; everything then goes through the Hub.
	Listen string
	// Advertise is the https://host:port peers should dial. It must be
	// reachable by them; nothing is guessed. Defaults to https://Listen.
	Advertise string
	// Owned, if set, starts local services that need this daemon to own the
	// home (the messenger page). It runs once the home lock is held and the
	// worker is listening; its stop runs when the daemon ends.
	Owned func() (stop func(), err error)
	// Executable is this daemon's program file, resolved when it started,
	// before an update could replace it. Only an update of this exact file
	// switches the daemon (restart.go); empty never switches.
	Executable string
	// CanSwitch, if set, says whether this daemon can be switched in place
	// and why not. A daemon that cannot is never stopped for an update: the
	// request is recorded as not applied, with the reason.
	CanSwitch func() (ok bool, why string)
}

// Direct delivery limits. Variables so tests can shorten them.
var (
	// memberTTL is how long a peer's Hub membership check is reused before
	// the Hub is asked again. A revoked peer can keep delivering directly for
	// at most this long; if the Hub cannot be asked, direct requests fail.
	memberTTL = time.Minute
	// directQuota bounds ciphertext accepted directly (the recipient's copy).
	directQuota int64 = 1 << 30
	// directUploadTTL is the idle time before an unfinished direct upload is reclaimed.
	directUploadTTL = 24 * time.Hour
)

var (
	errAuthorityUnavailable = errors.New("cannot confirm membership with the Hub right now")
	errNotMember            = errors.New("not an active member")
)

// directServer accepts deliveries straight from peers, with the same
// envelope, key and attachment checks as Hub delivery.
type directServer struct {
	a       *Agent
	session string

	blobMu sync.Mutex // serialises direct blob writes, finalisation and reclamation

	authMu  sync.Mutex
	nonces  map[string]time.Time
	members map[string]member
}

type member struct {
	key     ed25519.PublicKey
	checked time.Time
}

// startDirect listens for direct deliveries and returns the endpoint and
// certificate to advertise, plus a function that stops the listener.
func (a *Agent) startDirect(opts RunOptions, session string) (endpoint, certPEM string, stop func(), err error) {
	ln, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return "", "", nil, err
	}
	defer func() {
		if err != nil {
			ln.Close()
		}
	}()
	advertise := opts.Advertise
	if advertise == "" {
		advertise = "https://" + ln.Addr().String()
	}
	endpoint, err = protocol.NormalizeHubURL(advertise)
	if err != nil {
		return "", "", nil, fmt.Errorf("advertised direct address: %w", err)
	}
	u, _ := url.Parse(endpoint)
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsUnspecified() {
		return "", "", nil, errors.New("set --advertise to an address peers can reach (not 0.0.0.0)")
	}
	cp, kp, err := tlscert.Generate(u.Hostname(), "AgentNet direct "+a.Address, 30*24*time.Hour)
	if err != nil {
		return "", "", nil, err
	}
	cert, err := tls.X509KeyPair(cp, kp)
	if err != nil {
		return "", "", nil, err
	}
	s := &directServer{a: a, session: session, nonces: map[string]time.Time{}, members: map[string]member{}}
	if err := secfile.EnsureDir(filepath.Dir(a.downloadPath("x"))); err != nil {
		return "", "", nil, err
	}
	s.blobMu.Lock()
	err = s.reclaim()
	s.blobMu.Unlock()
	if err != nil {
		return "", "", nil, err
	}
	srv := &http.Server{
		Handler:           s.routes(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	done := make(chan struct{})
	go func() { srv.ServeTLS(ln, "", ""); close(done) }()
	stop = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		<-done
	}
	a.Logf("accepting direct deliveries on %s as %s", ln.Addr(), endpoint)
	return endpoint, string(cp), stop, nil
}

func (s *directServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/direct/blobs", s.handleReserve)
	mux.HandleFunc("GET /v1/direct/blobs/{id}", s.handleStatus)
	mux.HandleFunc("PUT /v1/direct/blobs/{id}", s.handleChunk)
	mux.HandleFunc("POST /v1/direct/blobs/{id}/complete", s.handleComplete)
	mux.HandleFunc("POST /v1/direct/messages", s.handleMessage)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, protocol.Error{Code: code, Error: msg})
}

// authenticate checks the request signature against the sender's current
// membership and rejects replays.
func (s *directServer) authenticate(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	sr, err := protocol.ReadSignedRequest(r, func(agent string) (ed25519.PublicKey, error) {
		return s.memberKey(r.Context(), agent)
	})
	switch {
	case errors.Is(err, errAuthorityUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "", err.Error())
		return "", nil, false
	case errors.Is(err, errNotMember):
		writeErr(w, http.StatusForbidden, "", err.Error())
		return "", nil, false
	case err != nil:
		writeErr(w, http.StatusUnauthorized, "", err.Error())
		return "", nil, false
	}
	if !s.useNonce(sr.Agent+"/"+sr.Nonce, sr.Time) {
		writeErr(w, http.StatusUnauthorized, "", "replayed request")
		return "", nil, false
	}
	return sr.Agent, sr.Body, true
}

// memberKey returns agent's signing key after confirming with the Hub, at
// most memberTTL ago, that it is an active member whose key matches the one
// this agent trusts.
func (s *directServer) memberKey(ctx context.Context, agent string) (ed25519.PublicKey, error) {
	s.authMu.Lock()
	m, ok := s.members[agent]
	s.authMu.Unlock()
	if ok && time.Since(m.checked) < memberTTL {
		return m.key, nil
	}
	e, err := s.a.directory(ctx, agent)
	if err != nil {
		if retryable(err) {
			return nil, errAuthorityUnavailable
		}
		return nil, errNotMember
	}
	if e.Revoked {
		return nil, errNotMember
	}
	pinned, _, found, err := s.a.store.peer(agent)
	if err != nil {
		return nil, err
	}
	if !found {
		if err := s.a.store.pin(e.Public); err != nil {
			return nil, err
		}
	} else if !sameKeys(pinned, e.Public) {
		return nil, fmt.Errorf("keys for %s changed; not trusted for direct delivery", agent)
	}
	s.authMu.Lock()
	s.members[agent] = member{key: e.Public.SignKey, checked: time.Now()}
	s.authMu.Unlock()
	return e.Public.SignKey, nil
}

func (s *directServer) useNonce(key string, at time.Time) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	cutoff := time.Now().Add(-2 * protocol.ClockSkew)
	for k, t := range s.nonces {
		if t.Before(cutoff) {
			delete(s.nonces, k)
		}
	}
	if _, seen := s.nonces[key]; seen {
		return false
	}
	s.nonces[key] = at
	return true
}

func (s *directServer) partPath(id string) string { return s.a.downloadPath(id) + ".direct" }

// reclaim removes unfinished direct uploads idle past directUploadTTL, in
// the same two phases as the Hub. Callers hold blobMu.
func (s *directServer) reclaim() error {
	ids, err := s.a.store.markDirectReclaiming(time.Now().Add(-directUploadTTL))
	if err != nil {
		return err
	}
	for _, id := range ids {
		err := blobfile.RemoveIfExists(s.partPath(id), s.a.downloadPath(id))
		if err == nil {
			err = syncDir(filepath.Dir(s.partPath(id)))
		}
		if err == nil {
			err = s.a.store.deleteDirectReclaimed(id)
		}
		if err != nil {
			s.a.Logf("reclaim direct upload %s: %v (will retry)", id, err)
		}
	}
	return nil
}

func status(id string, b directBlob) protocol.BlobStatus {
	return protocol.BlobStatus{ID: id, Size: b.Size, Received: b.Received, State: b.State}
}

// ownUpload loads an upload by the caller; others get 404.
func (s *directServer) ownUpload(w http.ResponseWriter, id, caller string) (directBlob, bool) {
	b, err := s.a.store.directBlob(id)
	if err != nil || b.Owner != caller {
		writeErr(w, http.StatusNotFound, "", "unknown upload")
		return b, false
	}
	return b, true
}

func (s *directServer) handleReserve(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var req protocol.BlobReserve
	if err := decodeStrict(body, &req); err != nil || !protocol.ValidID(req.ID) || len(req.SHA256) != 64 || req.Size <= 0 {
		writeErr(w, http.StatusBadRequest, "", "malformed upload reservation")
		return
	}
	if req.Recipient != s.a.Address {
		writeErr(w, http.StatusConflict, "", "upload is for another agent")
		return
	}
	if req.Size > protocol.CiphertextBound(MaxFileSize) {
		writeErr(w, http.StatusRequestEntityTooLarge, "", "file exceeds the size limit")
		return
	}
	s.blobMu.Lock()
	defer s.blobMu.Unlock()
	if err := s.reclaim(); err != nil {
		writeErr(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	b, err := s.a.store.reserveDirect(caller, req, directQuota)
	switch {
	case errors.Is(err, errDirectQuota):
		writeErr(w, http.StatusRequestEntityTooLarge, "", err.Error())
	case errors.Is(err, errDirectConflict):
		writeErr(w, http.StatusConflict, "", err.Error())
	case errors.Is(err, errDirectBusy):
		writeErr(w, http.StatusServiceUnavailable, "", err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "", "storage error")
	default:
		writeJSON(w, http.StatusOK, status(req.ID, b))
	}
}

func (s *directServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	caller, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if b, ok := s.ownUpload(w, r.PathValue("id"), caller); ok {
		writeJSON(w, http.StatusOK, status(r.PathValue("id"), b))
	}
}

func (s *directServer) handleChunk(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || len(body) == 0 || len(body) > protocol.ChunkSize {
		writeErr(w, http.StatusBadRequest, "", "chunk needs an offset and 1..ChunkSize bytes")
		return
	}
	s.blobMu.Lock()
	defer s.blobMu.Unlock()
	b, ok := s.ownUpload(w, id, caller)
	if !ok {
		return
	}
	if b.State != protocol.BlobUploading || offset != b.Received || offset+int64(len(body)) > b.Size {
		writeErr(w, http.StatusConflict, "", "chunk does not continue the upload; fetch its status")
		return
	}
	err = blobfile.WriteChunk(s.partPath(id), offset, body)
	if err == nil {
		err = s.a.store.setDirectReceived(id, offset+int64(len(body)))
	}
	if errors.Is(err, errNoSuchUpload) {
		writeErr(w, http.StatusConflict, "", "upload no longer active; fetch its status")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	b.Received = offset + int64(len(body))
	writeJSON(w, http.StatusOK, status(id, b))
}

func (s *directServer) handleComplete(w http.ResponseWriter, r *http.Request) {
	caller, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	s.blobMu.Lock()
	defer s.blobMu.Unlock()
	b, ok := s.ownUpload(w, id, caller)
	if !ok {
		return
	}
	if b.State == protocol.BlobStored {
		writeJSON(w, http.StatusOK, status(id, b))
		return
	}
	part, final := s.partPath(id), s.a.downloadPath(id)
	src := part
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		src = final // crashed after rename, before recording
	}
	err := blobfile.Check(src, b.Size, b.SHA256)
	if errors.Is(err, blobfile.ErrDigest) {
		err = blobfile.RemoveIfExists(src)
		if err == nil {
			err = s.a.store.setDirectState(id, protocol.BlobUploading)
		}
		if err == nil {
			writeErr(w, http.StatusUnprocessableEntity, "", blobfile.ErrDigest.Error())
			return
		}
	} else if err != nil {
		writeErr(w, http.StatusConflict, "", "upload incomplete; fetch its status")
		return
	}
	if err == nil {
		err = blobfile.Promote(src, final, syncDir)
	}
	if err == nil {
		err = s.a.store.setDirectState(id, protocol.BlobStored)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	b.State, b.Received = protocol.BlobStored, b.Size
	writeJSON(w, http.StatusOK, status(id, b))
}

// handleMessage stores a directly delivered message once its attachments
// are held here, then answers with the same receipt the Hub would record.
func (s *directServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var env envelope.Envelope
	if err := decodeStrict(body, &env); err != nil {
		writeErr(w, http.StatusBadRequest, "", "malformed envelope")
		return
	}
	switch {
	case env.From != caller:
		writeErr(w, http.StatusForbidden, "", "envelope sender must be the caller")
		return
	case env.To != s.a.Address:
		writeErr(w, http.StatusConflict, "", "envelope is for another agent")
		return
	case env.Session != "" && env.Session != s.session && !env.Fallback:
		writeErr(w, http.StatusConflict, protocol.CodeSessionExpired, "addressed session is not this one")
		return
	}
	for _, b := range env.Blobs {
		if ok, err := s.a.store.directStored(b.ID, caller, b.Size, b.SHA256); err != nil || !ok {
			writeErr(w, http.StatusConflict, "", "attachment "+b.ID+" has not been uploaded here")
			return
		}
	}
	seen, err := s.a.store.seen(env.ID)
	if err == nil && !seen {
		err = s.a.verifyAndStore(r.Context(), env)
	}
	var state string
	if err == nil {
		state, err = s.a.store.disposition(env.ID)
	}
	if err == nil {
		// The Hub never saw this message, so there is no Hub receipt to send.
		err = s.a.store.markAcked(receipt{env.ID, state})
	}
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "", "not stored yet: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, protocol.Receipt{ID: env.ID, State: state, Path: protocol.PathDirect})
}
