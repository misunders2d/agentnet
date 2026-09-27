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
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/tlscert"
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
}

// Hub serves the AgentNet Hub API.
type Hub struct {
	cfg       Config
	store     *store
	cert      tls.Certificate
	certPEM   string
	streams   streams
	presence  presence
	stats     Stats
	heartbeat time.Duration
	blobMu    sync.Mutex // serialises blob file writes, finalisation and reclamation
	syncDir   func(dir string) error
	done      chan struct{}
	closeOnce sync.Once
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
	cfg.PublicURL = public
	u, _ := url.Parse(public)
	if err := secfile.EnsureDir(cfg.DataDir); err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(cfg.DataDir, "hub.db"))
	if err != nil {
		return nil, err
	}
	h := &Hub{cfg: cfg, store: st, heartbeat: cfg.Heartbeat, syncDir: secfile.SyncDir, done: make(chan struct{})}
	h.presence = presence{grace: cfg.SessionGrace, onEnd: func(agent, session string) {
		if err := st.expireSession(agent, session); err != nil {
			cfg.Logf("expire session %s#%s: %v", agent, session, err)
		}
	}}
	if err := h.loadOrCreateCert(u.Hostname()); err != nil {
		st.db.Close()
		return nil, err
	}
	if err := h.bootstrap(); err != nil {
		st.db.Close()
		return nil, err
	}
	if err := h.prepareBlobs(); err != nil {
		st.db.Close()
		return nil, err
	}
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
	}
	defer ln.Close()
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()
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

// Close releases the database. Call after Serve returns.
func (h *Hub) Close() error {
	h.presence.close()
	return h.store.db.Close()
}

type logWriter struct{ logf func(string, ...any) }

func (l logWriter) Write(p []byte) (int, error) {
	l.logf("%s", p)
	return len(p), nil
}
