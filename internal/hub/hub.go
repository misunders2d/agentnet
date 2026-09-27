// Package hub is the AgentNet Hub service: enrollment, directory, and an
// end-to-end-encrypted store-and-forward mailbox with push delivery.
package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
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
}

// Hub serves the AgentNet Hub API.
type Hub struct {
	cfg       Config
	store     *store
	cert      tls.Certificate
	certPEM   string
	streams   streams
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
	h := &Hub{cfg: cfg, store: st, heartbeat: protocol.HeartbeatInterval, syncDir: secfile.SyncDir, done: make(chan struct{})}
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
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "AgentNet Hub " + host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
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
func (h *Hub) Close() error { return h.store.db.Close() }

type logWriter struct{ logf func(string, ...any) }

func (l logWriter) Write(p []byte) (int, error) {
	l.logf("%s", p)
	return len(p), nil
}
