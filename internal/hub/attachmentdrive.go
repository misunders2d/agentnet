package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/gdrive"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

const attachmentDriveSchema = `
CREATE TABLE relay_attachment_drive(
 id INTEGER PRIMARY KEY CHECK(id=1), connection TEXT NOT NULL DEFAULT '{}', oauth_path TEXT NOT NULL DEFAULT '');
INSERT INTO relay_attachment_drive(id) VALUES(1);
CREATE TABLE relay_drive_blobs(
 blob_id TEXT PRIMARY KEY REFERENCES blobs(id) ON DELETE CASCADE,
 file_id TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'pending', local_present INTEGER NOT NULL DEFAULT 1);
`

// AttachmentDriveOAuthFile is an owner-only Web OAuth configuration. It is
// deliberately absent from public status and portable backups.
const AttachmentDriveOAuthFile = "attachment-drive-oauth.json"
const attachmentDriveCallback = "/oauth/drive/attachments"

type relayDriveConnection struct {
	ClientID   string         `json:"oauth_client_id"`
	Token      gdrive.Token   `json:"token"`
	Account    gdrive.Account `json:"account"`
	FolderID   string         `json:"folder_id"`
	FolderName string         `json:"folder_name"`
}
type relayDriveConsent struct {
	state, verifier, admin string
	key                    []byte
	expires                time.Time
}
type relayDrive struct {
	mu            sync.Mutex
	consentMu     sync.Mutex // serializes consent/disconnect without blocking local custody
	tokenMu       sync.Mutex
	oauth         gdrive.OAuthConfig
	configured    bool
	client        *gdrive.Client
	consent       *relayDriveConsent
	state, notice string
	storageFull   bool
	progress      drivecontract.RelayMigration
	wake          chan struct{}
	cancel        context.CancelFunc
	done          chan struct{}
	closeOnce     sync.Once
}

func readAttachmentOAuth(path string, hc *http.Client) (gdrive.OAuthConfig, bool, error) {
	raw, err := secfile.Read(path)
	if errors.Is(err, os.ErrNotExist) {
		return gdrive.OAuthConfig{}, false, nil
	}
	if err != nil {
		return gdrive.OAuthConfig{}, false, errors.New("relay attachment OAuth configuration must be an owner-only file")
	}
	type credentials struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	var file struct {
		credentials
		Web       json.RawMessage `json:"web"`
		Installed json.RawMessage `json:"installed"`
	}
	invalid := func() (gdrive.OAuthConfig, bool, error) {
		return gdrive.OAuthConfig{}, false, errors.New("relay attachment OAuth configuration needs a Web client_id and client_secret")
	}
	if len(raw) > 16<<10 || !json.Valid(raw) || decodeStrict(raw, &file) != nil || file.Installed != nil {
		return invalid()
	}
	cfg := file.credentials
	if file.Web != nil {
		if cfg.ClientID != "" || cfg.ClientSecret != "" || json.Unmarshal(file.Web, &cfg) != nil {
			return invalid()
		}
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return invalid()
	}
	// Google's downloaded Web-client file also contains endpoints and redirect
	// URIs. Only these two credentials are consumed; our endpoints and the
	// relay's exact HTTPS callback remain fixed by the application.
	return gdrive.OAuthConfig{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, HTTP: hc}, true, nil
}
func (s *store) relayDriveConnection(ctx context.Context) (relayDriveConnection, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT connection FROM relay_attachment_drive WHERE id=1`).Scan(&raw)
	var c relayDriveConnection
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c)
	}
	return c, err
}
func (s *store) saveRelayDriveConnection(ctx context.Context, c relayDriveConnection) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE relay_attachment_drive SET connection=? WHERE id=1`, string(raw))
	return err
}

func (h *Hub) saveAttachmentConsent(ctx context.Context, c relayDriveConnection, pending *relayDriveConsent, activate bool) error {
	d := h.attachmentDrive
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := h.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := agentIn(tx, pending.admin)
	if err != nil || !a.Admin || a.Revoked || a.Pending || !bytes.Equal(a.Public.SignKey, pending.key) {
		return errors.New("initiating admin authority changed")
	}
	var saved string
	if err = tx.QueryRowContext(ctx, `SELECT connection FROM relay_attachment_drive WHERE id=1`).Scan(&saved); err != nil {
		return err
	}
	var old relayDriveConnection
	if err = json.Unmarshal([]byte(saved), &old); err != nil {
		return err
	}
	var references int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM relay_drive_blobs WHERE file_id!=''`).Scan(&references); err != nil {
		return err
	}
	if (references > 0 || d.progress.Running) && old.Account.PermissionID != c.Account.PermissionID {
		return errors.New("reconnect the same Google account while Drive attachments exist")
	}
	if references > 0 && old.ClientID != d.oauth.ClientID {
		return errors.New("restore the original relay OAuth client while Drive attachments exist")
	}
	if !activate {
		c.Token = old.Token // retain any refresh that completed during provider setup
		if old.Account.PermissionID != c.Account.PermissionID {
			c.Token = gdrive.Token{} // never pair the old account's grant with a new folder
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relay_attachment_drive SET connection=? WHERE id=1`, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}
func (h *Hub) initAttachmentDrive() error {
	path := h.cfg.AttachmentDriveOAuthFile
	if path == "" {
		path = filepath.Join(h.cfg.DataDir, AttachmentDriveOAuthFile)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	oauth, configured, err := readAttachmentOAuth(path, h.cfg.AttachmentDriveHTTP)
	if err != nil {
		return err
	}
	if _, err = h.store.db.Exec(`UPDATE relay_attachment_drive SET oauth_path=? WHERE id=1`, path); err != nil {
		return err
	}
	d := &relayDrive{oauth: oauth, configured: configured, wake: make(chan struct{}, 1), done: make(chan struct{})}
	d.client = &gdrive.Client{HTTP: h.cfg.AttachmentDriveHTTP, Token: func(ctx context.Context) (string, error) { return h.attachmentAccess(ctx) }}
	// A stopped transfer retains its stable provider ID and local ciphertext.
	if _, err = h.store.db.Exec(`UPDATE relay_drive_blobs SET state='pending' WHERE state IN ('failed','retry')`); err != nil {
		return err
	}
	h.attachmentDrive = d
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go h.runAttachmentDrive(ctx)
	d.signal()
	return nil
}
func (d *relayDrive) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}
func (d *relayDrive) close() {
	if d == nil || d.cancel == nil {
		return
	}
	d.closeOnce.Do(func() { d.cancel(); <-d.done })
}
func (h *Hub) attachmentAccess(ctx context.Context) (string, error) {
	d := h.attachmentDrive
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	c, err := h.store.relayDriveConnection(ctx)
	if err != nil || !d.configured || c.Token.Refresh == "" || c.ClientID != d.oauth.ClientID {
		return "", gdrive.ErrConsent
	}
	if c.Token.Access != "" && c.Token.Expiry.After(time.Now().Add(time.Minute)) {
		return c.Token.Access, nil
	}
	c.Token, err = d.oauth.Refresh(ctx, c.Token)
	if err == nil {
		err = h.store.saveRelayDriveConnection(ctx, c)
	}
	if err != nil {
		return "", err
	}
	return c.Token.Access, nil
}

func (h *Hub) attachmentStorageStatus(ctx context.Context, admin bool) (drivecontract.RelayStorageStatus, error) {
	d := h.attachmentDrive
	v := drivecontract.RelayStorageStatus{CanAdmin: admin, State: "local"}
	if d == nil {
		return v, errors.New("relay attachment storage unavailable")
	}
	d.mu.Lock()
	v.Configured, v.State, v.Notice, v.Migration = d.configured, d.state, d.notice, d.progress
	d.mu.Unlock()
	c, err := h.store.relayDriveConnection(ctx)
	if err != nil {
		return v, err
	}
	v.Connected = c.Token.Refresh != "" && c.Account.PermissionID != ""
	if v.State == "" {
		v.State = "local"
	}
	if !v.Configured {
		v.State = "not_configured"
	}
	if v.Connected && v.State == "local" {
		v.State = "connected"
	}
	if v.Migration.Running {
		v.State = "migrating"
	}
	if !admin {
		v.Migration = drivecontract.RelayMigration{}
		return v, nil // relay-wide usage and transfer progress are admin-only
	}
	if admin && c.Account.PermissionID != "" {
		v.Account = &drivecontract.RelayAccount{Email: c.Account.Email, Name: c.Account.Name}
		if c.FolderID != "" {
			v.Folder = &drivecontract.RelayFolder{ID: c.FolderID, Name: c.FolderName}
		}
	}
	err = h.store.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN b.state=? THEN b.size ELSE b.received END),0)
 FROM blobs b LEFT JOIN relay_drive_blobs d ON d.blob_id=b.id WHERE coalesce(d.local_present,1)=1`, protocol.BlobStored).Scan(&v.Local.Files, &v.Local.Bytes)
	if err != nil {
		return v, err
	}
	err = h.store.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(b.size),0),coalesce(sum(CASE WHEN d.local_present=0 THEN 1 ELSE 0 END),0)
 FROM blobs b JOIN relay_drive_blobs d ON d.blob_id=b.id WHERE d.state='remote'`).Scan(&v.Remote.Files, &v.Remote.Bytes, &v.RemoteOnlyFiles)
	if err != nil {
		return v, err
	}
	err = h.store.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(b.size),0) FROM blobs b JOIN relay_drive_blobs d ON d.blob_id=b.id WHERE d.state!='remote'`).Scan(&v.Pending.Files, &v.Pending.Bytes)
	return v, err
}
func (h *Hub) handleAttachmentStorageGet(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	a, err := h.store.agent(caller)
	if err != nil {
		writeError(w, 500, "", "attachment storage unavailable")
		return
	}
	v, err := h.attachmentStorageStatus(r.Context(), a.Admin)
	if err != nil {
		writeError(w, 500, "", "attachment storage unavailable")
		return
	}
	writeJSON(w, 200, v)
}
func (h *Hub) handleAttachmentStorageAction(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var action drivecontract.RelayStorageAction
	if len(body) > 1024 || decodeStrict(body, &action) != nil {
		writeError(w, 400, "", "malformed attachment storage action")
		return
	}
	var consentURL string
	var err error
	switch action.Action {
	case "connect":
		consentURL, err = h.beginAttachmentConsent(r.Context(), caller)
	case "move":
		if !action.Confirm {
			writeError(w, 400, "", "confirm moving existing encrypted attachments")
			return
		}
		err = h.moveExistingAttachments(r.Context())
	case "disconnect":
		if !action.Confirm {
			writeError(w, 400, "", "confirm disconnecting relay attachment storage")
			return
		}
		err = h.disconnectAttachmentDrive(r.Context())
	default:
		writeError(w, 400, "", "choose connect, move or disconnect")
		return
	}
	if err != nil {
		writeError(w, 409, "", err.Error())
		return
	}
	v, err := h.attachmentStorageStatus(r.Context(), true)
	if err != nil {
		writeError(w, 500, "", "attachment storage unavailable")
		return
	}
	v.ConsentURL = consentURL
	writeJSON(w, 200, v)
}
func (h *Hub) beginAttachmentConsent(ctx context.Context, caller string) (string, error) {
	d := h.attachmentDrive
	if !d.configured {
		return "", errors.New("the relay operator must configure a Google Web OAuth client and the relay HTTPS callback first")
	}
	a, err := h.store.agent(caller)
	if err != nil || !a.Admin || a.Revoked || a.Pending {
		return "", errors.New("current relay admin required")
	}
	state := protocol.NewID() + protocol.NewID()
	link, verifier, err := d.oauth.WebConsent(h.cfg.PublicURL+attachmentDriveCallback, state)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.consent = &relayDriveConsent{state: state, verifier: verifier, admin: caller, key: append([]byte(nil), a.Public.SignKey...), expires: time.Now().Add(5 * time.Minute)}
	d.mu.Unlock()
	return link, nil
}

func (h *Hub) handleAttachmentDriveCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	d := h.attachmentDrive
	d.mu.Lock()
	pending := d.consent
	if pending == nil || r.URL.Query().Get("state") != pending.state || time.Now().After(pending.expires) {
		d.mu.Unlock()
		http.Error(w, "Invalid or expired Google consent. Return to AgentNet.", 400)
		return
	}
	d.consent = nil // single-use, including provider decline or failed exchange
	d.mu.Unlock()
	a, err := h.store.agent(pending.admin)
	if err != nil || !a.Admin || a.Revoked || a.Pending || !bytes.Equal(a.Public.SignKey, pending.key) {
		http.Error(w, "The initiating admin is no longer authorized. Return to AgentNet.", 403)
		return
	}
	code := r.URL.Query().Get("code")
	if r.URL.Query().Get("error") != "" || code == "" || len(code) > 4096 {
		http.Error(w, "Google consent declined or incomplete. Return to AgentNet.", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	token, err := d.oauth.ExchangeWeb(ctx, h.cfg.PublicURL+attachmentDriveCallback, code, pending.verifier)
	if err == nil && token.Refresh == "" {
		err = gdrive.ErrConsent
	}
	if err == nil {
		err = h.finishAttachmentConsent(ctx, pending, token)
	}
	if err != nil {
		d.mu.Lock()
		d.notice = "Google connection failed. Reconnect with the same account if Drive attachments already exist."
		d.mu.Unlock()
		http.Error(w, "Google connection failed. Return to AgentNet and reconnect with the same account.", 400)
		return
	}
	io.WriteString(w, "Relay attachment storage connected. Return to AgentNet. Existing local attachments move only after an admin confirms Move existing.")
}

func (h *Hub) finishAttachmentConsent(ctx context.Context, pending *relayDriveConsent, token gdrive.Token) error {
	d := h.attachmentDrive
	d.consentMu.Lock()
	defer d.consentMu.Unlock()
	client := *d.client
	client.Token = func(context.Context) (string, error) { return token.Access, nil }
	account, err := client.Account(ctx)
	if err != nil {
		return err
	}
	a, err := h.store.agent(pending.admin)
	if err != nil || !a.Admin || a.Revoked || !bytes.Equal(a.Public.SignKey, pending.key) {
		return errors.New("admin authority changed")
	}
	old, err := h.store.relayDriveConnection(ctx)
	if err != nil {
		return err
	}
	var references int64
	if err = h.store.db.QueryRowContext(ctx, `SELECT count(*) FROM relay_drive_blobs WHERE file_id!=''`).Scan(&references); err != nil {
		return err
	}
	d.mu.Lock()
	running := d.progress.Running
	d.mu.Unlock()
	if (references > 0 || running) && old.Account.PermissionID != account.PermissionID {
		return errors.New("reconnect the same Google account while Drive attachments exist")
	}
	if references > 0 && old.ClientID != d.oauth.ClientID {
		return errors.New("restore the original relay OAuth client while Drive attachments exist")
	}
	if old.Account.PermissionID != account.PermissionID {
		old.FolderID = ""
	}
	if old.FolderID == "" {
		old.FolderID, err = client.GenerateID(ctx)
		if err != nil {
			return err
		}
		old.Account = account
		old.ClientID = d.oauth.ClientID
		// Persist the preallocated folder ID before creation, including crash recovery.
		if err = h.saveAttachmentConsent(ctx, old, pending, false); err != nil {
			return err
		}
	}
	folder, err := client.EnsureCipherFolder(ctx, old.FolderID, h.realmID)
	if err != nil {
		return err
	}
	old.Token, old.Account, old.FolderName, old.ClientID = token, account, folder.Name, d.oauth.ClientID
	if err = h.saveAttachmentConsent(ctx, old, pending, true); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state, d.notice = "connected", ""
	d.storageFull = false
	_, err = h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET state='pending' WHERE state IN ('failed','retry')`)
	if err == nil {
		d.signal()
	}
	return err
}

func (h *Hub) moveExistingAttachments(ctx context.Context) error {
	d := h.attachmentDrive
	d.mu.Lock()
	defer d.mu.Unlock()
	c, err := h.store.relayDriveConnection(ctx)
	if err != nil {
		return err
	}
	if !d.configured || c.Token.Refresh == "" {
		return errors.New("connect the relay's Google Drive account first")
	}
	if _, err = h.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO relay_drive_blobs(blob_id) SELECT id FROM blobs WHERE state=?`, protocol.BlobStored); err != nil {
		return err
	}
	if _, err = h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET state='pending' WHERE state IN ('failed','retry')`); err != nil {
		return err
	}
	d.notice = ""
	d.storageFull = false
	if d.state != "needs_reconnect" {
		d.state = "connected" // explicit Move resumes a checksum/quota pause
	}
	d.signal()
	return nil
}
func (h *Hub) disconnectAttachmentDrive(ctx context.Context) error {
	d := h.attachmentDrive
	d.consentMu.Lock()
	defer d.consentMu.Unlock()
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.progress.Running {
		return errors.New("wait for the attachment transfer to finish before disconnecting")
	}
	var count int64
	if err := h.store.db.QueryRowContext(ctx, `SELECT count(*) FROM relay_drive_blobs WHERE local_present=0`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("cannot disconnect: %d attachment(s) exist only in this Google Drive account", count)
	}
	// Restore creates local verified copies while retaining remote references.
	// Keep their account binding until cleanup removes those remote objects too.
	if err := h.store.db.QueryRowContext(ctx, `SELECT count(*) FROM relay_drive_blobs WHERE file_id!=''`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("cannot disconnect: %d retained Drive attachment reference(s) still require this account for cleanup", count)
	}
	c, err := h.store.relayDriveConnection(ctx)
	if err != nil {
		return err
	}
	if c.Token.Refresh != "" {
		if err = d.oauth.Revoke(ctx, c.Token); err != nil {
			return err
		}
	}
	if err = h.store.saveRelayDriveConnection(ctx, relayDriveConnection{}); err != nil {
		return err
	}
	d.consent, d.state, d.notice = nil, "local", ""
	return nil
}

func (h *Hub) storeCompleteAttachment(id string) error {
	d := h.attachmentDrive
	if d == nil {
		return h.store.setBlobState(id, protocol.BlobStored)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	c, err := h.store.relayDriveConnection(context.Background())
	if err != nil {
		return err
	}
	tx, err := h.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = mustAffectOne(tx.Exec(`UPDATE blobs SET state=?,received=size,updated_at=? WHERE id=? AND state IN (?,?)`, protocol.BlobStored, time.Now().Unix(), id, protocol.BlobUploading, protocol.BlobStored)); err != nil {
		return err
	}
	queue := d.configured && c.Token.Refresh != ""
	if queue {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO relay_drive_blobs(blob_id) VALUES(?)`, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil && queue {
		d.signal()
	}
	return err
}
func (h *Hub) runAttachmentDrive(ctx context.Context) {
	d := h.attachmentDrive
	defer close(d.done)
	var retryTimer *time.Timer
	var retry <-chan time.Time
	delay := time.Second
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			if retry != nil {
				continue // arrivals retain local custody without bypassing provider backoff
			}
		case <-retry:
		}
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer = nil
			retry = nil
		}
		if !d.configured {
			continue
		}
		d.mu.Lock()
		if d.state == "needs_reconnect" || d.storageFull {
			d.mu.Unlock()
			continue // new uploads retain custody without retrying denied consent
		}
		c, err := h.store.relayDriveConnection(ctx)
		if err != nil || c.Token.Refresh == "" {
			d.mu.Unlock()
			continue
		}
		if _, err = h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET state='pending' WHERE state='retry'`); err != nil {
			d.mu.Unlock()
			continue
		}
		d.progress = drivecontract.RelayMigration{Running: true}
		err = h.store.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(b.size),0) FROM relay_drive_blobs d JOIN blobs b ON b.id=d.blob_id WHERE d.state='pending' OR (d.state='remote' AND d.local_present=1)`).Scan(&d.progress.TotalFiles, &d.progress.TotalBytes)
		d.mu.Unlock()
		if err == nil {
			err = h.copyAttachmentQueue(ctx)
		}
		var failed int
		if err == nil {
			err = h.store.db.QueryRowContext(ctx, `SELECT count(*) FROM relay_drive_blobs WHERE state='failed'`).Scan(&failed)
		}
		d.mu.Lock()
		d.progress.Running = false
		if err != nil {
			d.state = "offline"
			if errors.Is(err, gdrive.ErrConsent) || errors.Is(err, gdrive.ErrDenied) {
				d.state = "needs_reconnect"
			}
			d.notice = "Attachment transfer paused; local ciphertext is retained. Temporary provider failures retry automatically; reconnect the same account if consent is needed."
			if errors.Is(err, gdrive.ErrStorageFull) {
				d.storageFull = true
				d.notice = "Google Drive storage is full. Local ciphertext is retained; free Drive space, then confirm Move existing to retry."
			} else if errors.Is(err, gdrive.ErrChecksumUnavailable) || errors.Is(err, gdrive.ErrCiphertext) || errors.Is(err, gdrive.ErrMissing) {
				d.notice = "Drive attachment metadata or SHA-256 checksum is missing or does not match. Local ciphertext is retained; check Drive, then confirm Move existing to retry."
			}
		} else if failed > 0 {
			d.state = "offline"
			if d.notice == "" {
				d.notice = "Some attachment transfers remain paused. Local ciphertext is retained; check Drive, then confirm Move existing to retry."
			}
		} else {
			d.state, d.notice = "connected", ""
		}
		d.mu.Unlock()
		if errors.Is(err, gdrive.ErrOffline) && ctx.Err() == nil {
			var queued int
			if e := h.store.db.QueryRowContext(ctx, `SELECT count(*) FROM relay_drive_blobs WHERE state='retry'`).Scan(&queued); e == nil && queued > 0 {
				retryTimer = time.NewTimer(delay)
				retry = retryTimer.C
				delay = min(delay*2, time.Minute)
			}
		} else if err == nil {
			delay = time.Second
		}
	}
}

const attachmentDriveTransfers = 4

// The existing durable queue owns retries and stable provider IDs. Keep only
// the current bounded transfers in memory, alternating old backlog and fresh
// arrivals so neither can starve the other.
func (h *Hub) copyAttachmentQueue(ctx context.Context) error {
	type result struct {
		id   string
		size int64
		err  error
	}
	results := make(chan result, attachmentDriveTransfers)
	inflight := make(map[string]bool, attachmentDriveTransfers)
	newest, stopped := false, false
	var transferErr error
	for {
		for !stopped && ctx.Err() == nil && len(inflight) < attachmentDriveTransfers {
			id, err := h.nextAttachmentDrive(ctx, inflight, newest)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				transferErr, stopped = err, true
				break
			}
			inflight[id], newest = true, !newest
			go func() {
				size, err := h.copyAttachmentDrive(ctx, id)
				results <- result{id, size, err}
			}()
		}
		if len(inflight) == 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return transferErr
		}
		wake := h.attachmentDrive.wake
		if stopped || len(inflight) == attachmentDriveTransfers {
			wake = nil
		}
		var r result
		select {
		case r = <-results: // drain admitted transfers before status changes or shutdown
		case <-wake:
			continue // fill a free slot when an arrival wakes a partially full batch
		}
		delete(inflight, r.id)
		if r.err == nil {
			h.attachmentDrive.mu.Lock()
			h.attachmentDrive.progress.Files++
			h.attachmentDrive.progress.Bytes += r.size
			h.attachmentDrive.mu.Unlock()
			continue
		}
		state := "failed"
		if errors.Is(r.err, gdrive.ErrOffline) {
			state = "retry"
		}
		marked, err := h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET state=? WHERE blob_id=? AND state!='remote'`, state, r.id)
		var changed int64
		if err == nil {
			changed, err = marked.RowsAffected()
		}
		pause := errors.Is(r.err, gdrive.ErrStorageFull) || errors.Is(r.err, gdrive.ErrConsent) || errors.Is(r.err, gdrive.ErrDenied)
		provider := errors.Is(r.err, gdrive.ErrOffline) || pause
		// A later corrupt blob must not hide the provider cause/backoff already
		// observed. Other corrupt files still allow independent work to progress.
		// An admitted transfer can report a permanent provider pause after a
		// temporary failure. That known quota/consent refusal must win backoff.
		if !stopped || pause {
			transferErr = r.err
			if err != nil {
				transferErr = err
			}
		}
		stopped = stopped || provider || err != nil || changed == 0
	}
}

func (h *Hub) nextAttachmentDrive(ctx context.Context, inflight map[string]bool, newest bool) (string, error) {
	order := "ASC"
	if newest {
		order = "DESC"
	}
	rows, err := h.store.db.QueryContext(ctx, `SELECT blob_id FROM relay_drive_blobs
 WHERE state='pending' OR (state='remote' AND local_present=1) ORDER BY rowid `+order+` LIMIT ?`, attachmentDriveTransfers+1)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		if !inflight[id] {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", sql.ErrNoRows
}

func copyVerifiedCiphertext(dst io.Writer, src io.Reader, size int64, sha string) error {
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(src, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != sha {
		return gdrive.ErrCiphertext
	}
	return nil
}
func (h *Hub) copyAttachmentDrive(ctx context.Context, id string) (int64, error) {
	d := h.attachmentDrive
	b, err := h.store.blob(id)
	if err != nil || b.State != protocol.BlobStored {
		return 0, errors.New("attachment no longer stored")
	}
	var fileID, state string
	if err = h.store.db.QueryRowContext(ctx, `SELECT file_id,state FROM relay_drive_blobs WHERE blob_id=?`, id).Scan(&fileID, &state); err != nil {
		return 0, err
	}
	c, err := h.store.relayDriveConnection(ctx)
	if err != nil {
		return 0, err
	}
	if fileID == "" {
		fileID, err = d.client.GenerateID(ctx)
		if err != nil {
			return 0, err
		}
		if _, err = h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET file_id=? WHERE blob_id=? AND file_id=''`, fileID, id); err != nil {
			return 0, err
		}
	}
	local := h.blobPath(id, true)
	if state != "remote" {
		if err = blobfile.Check(local, b.Size, b.SHA256); err != nil {
			return 0, err
		}
		f, e := os.Open(local)
		if e != nil {
			return 0, e
		}
		err = d.client.PutCiphertext(ctx, fileID, c.FolderID, h.realmID, id, b.SHA256, b.Size, f)
		f.Close()
		if err != nil {
			return 0, err
		}
		// The provider copy is proven before committing its mapping. Neither a
		// lost response nor a crash here can cause another Drive object.
		if _, err = h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET state='remote' WHERE blob_id=?`, id); err != nil {
			return 0, err
		}
	} else {
		// A restart may land after the remote commit but before local removal.
		// Confirm its current provider checksum before deleting that local copy.
		if _, err = d.client.CiphertextDownload(ctx, fileID, h.realmID, id, b.SHA256, b.Size); err != nil {
			if errors.Is(err, gdrive.ErrMissing) || errors.Is(err, gdrive.ErrCiphertext) || errors.Is(err, gdrive.ErrChecksumUnavailable) || errors.Is(err, gdrive.ErrOffline) {
				// Keep the original ID/account binding for cleanup. A damaged
				// provider object must not starve independent queued attachments.
				state := "failed"
				localErr := blobfile.Check(local, b.Size, b.SHA256)
				if localErr == nil && errors.Is(err, gdrive.ErrOffline) {
					state = "retry" // existing active backoff must also resume crash leftovers
				}
				if _, markErr := h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET state=? WHERE blob_id=?`, state, id); markErr != nil {
					return 0, markErr
				}
				if localErr != nil {
					return 0, localErr
				}
			}
			return 0, err
		}
	}
	if err = blobfile.RemoveIfExists(local); err != nil {
		return 0, err
	}
	if err = h.syncDir(filepath.Dir(local)); err != nil {
		return 0, err
	}
	_, err = h.store.db.ExecContext(ctx, `UPDATE relay_drive_blobs SET local_present=0 WHERE blob_id=? AND state='remote'`, id)
	return b.Size, err
}

func (h *Hub) openAttachmentCiphertext(r *http.Request, id string, b blobRow) (io.ReadSeeker, func(), error) {
	f, err := os.Open(h.blobPath(id, true))
	if err == nil {
		return f, func() { f.Close() }, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	var remoteID, state string
	if err = h.store.db.QueryRowContext(r.Context(), `SELECT file_id,state FROM relay_drive_blobs WHERE blob_id=?`, id).Scan(&remoteID, &state); err != nil || state != "remote" {
		return nil, nil, errors.New("attachment unavailable")
	}
	download, err := h.attachmentDrive.client.CiphertextDownload(r.Context(), remoteID, h.realmID, id, b.SHA256, b.Size)
	if err != nil {
		return nil, nil, err
	}
	reader := newAttachmentRangeReader(r, download, b.Size)
	return reader, func() { reader.Close() }, nil
}
