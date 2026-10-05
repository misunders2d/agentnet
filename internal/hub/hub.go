// Package hub is the AgentNet Hub service: enrollment, directory, and an
// end-to-end-encrypted store-and-forward mailbox with push delivery.
package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/tlscert"
	"github.com/misunders2d/agentnet/internal/ui/static"
)

// BootstrapFile holds the first admin invite inside the data directory.
const BootstrapFile = "bootstrap-invite.txt"

const bootstrapTTL = 7 * 24 * time.Hour

// Config configures a Hub.
type Config struct {
	DataDir    string
	PublicURL  string // https URL clients use; its host goes into the certificate
	AdminLabel string // person label for the bootstrap admin invite
	Logf       func(format string, args ...any)

	MaxFileSize  int64         // plaintext bytes per attachment (default 100 MiB)
	StorageQuota int64         // total ciphertext bytes held (default 1 GiB)
	UploadTTL    time.Duration // idle time before an incomplete upload is reclaimed (default 24h)

	Heartbeat    time.Duration // ping interval on idle push streams (default protocol.HeartbeatInterval)
	SessionGrace time.Duration // how long a disconnected session may reconnect before it ends (default 30s)

	// PlatformTLS serves plain HTTP for a platform (Railway, a load balancer)
	// that terminates HTTPS for PublicURL with a publicly trusted
	// certificate. Invites then carry no certificate pin and clients verify
	// the platform's certificate with their system CAs. The listener must be
	// reachable only through that platform.
	PlatformTLS bool

	// Web serves the browser messenger on this Hub's origin. The default is
	// API-only. Browsers need publicly trusted HTTPS (normally PlatformTLS).
	Web bool

	// BrowserOrigins are the explicit HTTPS origins of browser workspaces
	// (another relay's page) allowed to call this relay's API and to be
	// named in the page's connect-src; each request still passes every
	// signature and membership check. Empty: same-origin only, as before.
	// No wildcard (browserorigins.go, static/workspaces_origins.go).
	BrowserOrigins []string

	// PushHosts are push services this Hub sends Web Push to besides
	// protocol.DefaultPushHosts (each a host name; a subscription's host
	// must be one of them or a subdomain of one).
	PushHosts []string
}

// Hub serves the AgentNet Hub API.
type Hub struct {
	cfg     Config
	store   *store
	realmID string // loaded once from the database; independent of endpoint/TLS
	unlock  func()
	cert    tls.Certificate
	certPEM string
	streams streams
	// release is the operator's client recommendation; releaseGen changes
	// with it, under releaseMu.
	release    atomic.Pointer[protocol.Release]
	releaseMu  sync.Mutex
	releaseGen int64
	presence   presence
	membersGen atomic.Int64  // changes with the member list (see members.go)
	linksGen   atomic.Int64  // changes when a device joins to be linked (persons.go)
	push       pushKeys      // VAPID key pair (push.go)
	notifier   *notifier     // sends due notification alerts (notify.go)
	signals    signalRuntime // live typing signals, memory only (signals.go)
	waiters    waiters
	stats      Stats
	heartbeat  time.Duration
	blobMu     sync.Mutex // serialises blob file writes, finalisation and reclamation
	syncDir    func(dir string) error
	done       chan struct{}
	closeOnce  sync.Once

	// workspace is the admin's workspace name ("" when none), changed under
	// workspaceMu (workspace.go).
	workspace   atomic.Pointer[string]
	workspaceMu sync.Mutex
}

// Open prepares the data directory, database, TLS certificate, and — for a
// Hub with no agents yet — a fresh bootstrap admin invite.
func Open(cfg Config) (*Hub, error) {
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.AdminLabel == "" {
		cfg.AdminLabel = "admin"
	}
	if cfg.MaxFileSize <= 0 {
		cfg.MaxFileSize = protocol.DefaultMaxFileSize
	}
	if cfg.StorageQuota <= 0 {
		cfg.StorageQuota = protocol.DefaultStorageQuota
	}
	if cfg.UploadTTL <= 0 {
		cfg.UploadTTL = 24 * time.Hour
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = protocol.HeartbeatInterval
	}
	if cfg.SessionGrace <= 0 {
		cfg.SessionGrace = 30 * time.Second
	}
	if !protocol.ValidName(cfg.AdminLabel) {
		return nil, fmt.Errorf("invalid admin label %q", cfg.AdminLabel)
	}
	public, err := protocol.NormalizeHubURL(cfg.PublicURL)
	if err != nil {
		return nil, err
	}
	for _, h := range cfg.PushHosts {
		if !protocol.ValidPushHost(h) {
			return nil, fmt.Errorf("invalid push host %q (a host name such as push.example.com)", h)
		}
	}
	if approved, err := static.ValidateBrowserOrigins(cfg.BrowserOrigins); err != nil {
		return nil, fmt.Errorf("browser origins: %w", err)
	} else {
		cfg.BrowserOrigins = approved
	}
	cfg.PublicURL = public
	u, _ := url.Parse(public)
	if err := secfile.EnsureDir(cfg.DataDir); err != nil {
		return nil, err
	}
	unlock, err := lockData(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(cfg.DataDir, "hub.db"))
	if err != nil {
		unlock()
		return nil, err
	}
	h := &Hub{cfg: cfg, store: st, unlock: unlock, heartbeat: cfg.Heartbeat, syncDir: secfile.SyncDir, done: make(chan struct{})}
	h.presence = presence{grace: cfg.SessionGrace, onEnd: func(agent, session string) {
		senders, err := st.expireSession(agent, session)
		if err != nil {
			cfg.Logf("expire session %s#%s: %v", agent, session, err)
		}
		for _, sender := range senders {
			h.streams.notify(sender)
		}
		h.waiters.notifyAll() // some waited-for messages may have expired
	}, onChange: func(string) { h.membersChanged() }}
	err = h.loadRealm()
	if err == nil && !cfg.PlatformTLS {
		err = h.loadOrCreateCert(u.Hostname())
	}
	if err == nil {
		err = h.bootstrap()
	}
	if err == nil {
		err = h.loadRelease()
	}
	if err == nil {
		err = h.loadWorkspaceName()
	}
	if err == nil {
		err = h.prepareBlobs()
	}
	if err == nil {
		h.push, err = loadOrCreatePushKey(cfg.DataDir)
	}
	if err != nil {
		h.Close()
		return nil, err
	}
	h.notifier = newNotifier(h)
	h.notifier.send = webPusher(h.push, cfg.PublicURL, newPushClient())
	return h, nil
}

func (h *Hub) bootstrap() error {
	n, err := h.store.agentCount()
	if err != nil || n > 0 {
		return err
	}
	if err := h.store.dropBootstrapInvites(); err != nil {
		return err
	}
	secret := protocol.NewID() + protocol.NewID()
	if err := h.store.createInvite(secret, h.cfg.AdminLabel, true, bootstrapTTL, ""); err != nil {
		return err
	}
	path := filepath.Join(h.cfg.DataDir, BootstrapFile)
	code := protocol.Invite{Hub: h.cfg.PublicURL, Label: h.cfg.AdminLabel, Secret: secret, CertPEM: h.certPEM}.Encode()
	if err := secfile.Write(path, []byte(code+"\n")); err != nil {
		return err
	}
	h.cfg.Logf("no agents enrolled; bootstrap admin invite written to %s", path)
	return nil
}

func (h *Hub) loadOrCreateCert(host string) error {
	certPath := filepath.Join(h.cfg.DataDir, "tls.crt")
	keyPath := filepath.Join(h.cfg.DataDir, "tls.key")
	if keyPEM, err := secfile.Read(keyPath); err == nil {
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			return err
		}
		h.cert, err = tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return err
		}
		leaf, err := x509.ParseCertificate(h.cert.Certificate[0])
		if err != nil {
			return err
		}
		if leaf.VerifyHostname(host) != nil {
			return fmt.Errorf("existing certificate does not cover %s; remove %s and %s to regenerate", host, certPath, keyPath)
		}
		h.certPEM = string(certPEM)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	certPEM, keyPEM, err := tlscert.Generate(host, "AgentNet Hub "+host, 10*365*24*time.Hour)
	if err != nil {
		return err
	}
	if err := secfile.Write(keyPath, keyPEM); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	h.certPEM = string(certPEM)
	h.cert, err = tls.X509KeyPair(certPEM, keyPEM)
	return err
}

// Serve serves HTTPS on ln until ctx is cancelled.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           h.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{h.cert}, MinVersion: tls.VersionTLS13},
		ErrorLog:          log.New(logWriter{h.cfg.Logf}, "", 0),
		ConnState:         (&lateConns{stopping: h.done, starting: map[net.Conn]bool{}}).state,
	}
	defer ln.Close()
	notifyCtx, stopNotify := context.WithCancel(ctx)
	notifyDone := make(chan struct{})
	go func() { defer close(notifyDone); h.notifier.run(notifyCtx) }()
	defer func() { stopNotify(); <-notifyDone }()
	errc := make(chan error, 1)
	go func() {
		if h.cfg.PlatformTLS {
			errc <- srv.Serve(ln)
		} else {
			errc <- srv.ServeTLS(ln, "", "")
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	h.closeOnce.Do(func() { close(h.done) })
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	<-errc // ServeTLS has returned, so the listener is closed
	return err
}

// lateConns closes an HTTP/2 connection that begins serving only after the
// Hub started to stop. Shutdown tells the HTTP/2 connections being served to
// finish, but one whose TLS handshake was still running at that moment joins
// afterwards, is never told, and keeps Shutdown waiting until its deadline
// fails it. No request on it has been read yet, so closing it cuts no
// answer short. Shutdown closes HTTP/1 connections itself.
type lateConns struct {
	stopping <-chan struct{}
	mu       sync.Mutex
	starting map[net.Conn]bool // accepted, not yet serving
}

func (l *lateConns) state(c net.Conn, s http.ConnState) {
	l.mu.Lock()
	if s == http.StateNew {
		l.starting[c] = true
		l.mu.Unlock()
		return
	}
	first := l.starting[c]
	delete(l.starting, c)
	l.mu.Unlock()
	if !first || s != http.StateActive {
		return
	}
	select {
	case <-l.stopping:
	default:
		return
	}
	if tc, ok := c.(*tls.Conn); ok && tc.ConnectionState().NegotiatedProtocol == "h2" {
		c.Close()
	}
}

// Close releases the database. Call after Serve returns.
func (h *Hub) Close() error {
	h.presence.close()
	err := h.store.db.Close()
	h.unlock()
	return err
}

// lockData ensures only one process (a running Hub or a maintenance command)
// uses a data directory at a time.
func lockData(dir string) (func(), error) {
	unlock, err := lockfile.Acquire(filepath.Join(dir, "hub.lock"))
	if errors.Is(err, lockfile.ErrLocked) {
		return nil, fmt.Errorf("%s is in use by a running Hub or maintenance command; stop it first", dir)
	}
	return unlock, err
}

type logWriter struct{ logf func(string, ...any) }

func (l logWriter) Write(p []byte) (int, error) {
	l.logf("%s", p)
	return len(p), nil
}
