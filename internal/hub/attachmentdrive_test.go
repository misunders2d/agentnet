package hub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/gdrive"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

type relayFakeFile struct {
	meta map[string]any
	data []byte
}
type relayDriveProvider struct {
	mu                                  sync.Mutex
	server                              *httptest.Server
	files                               map[string]relayFakeFile
	next, creates, deletes, tokenCalls  int
	uploadCalls                         int
	metadataCalls                       int
	mediaBytes                          int64
	account, challenge                  string
	failUpload, denyUpload, corruptRead bool
	omitChecksum, badChecksum           bool
	uploadReason                        string
	blockPath                           string
	blocked, release                    chan struct{}
	workerDeadline                      bool
	deleteFailures                      map[string]bool
	metadataFailures                    map[string]bool
}

func TestRelayAttachmentDriveOAuthConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"downloaded web", `{"web":{"client_id":"fixture-client","client_secret":"fixture-secret","project_id":"fixture-project","auth_uri":"https://untrusted.invalid/auth","token_uri":"https://untrusted.invalid/token","auth_provider_x509_cert_url":"https://untrusted.invalid/certs","redirect_uris":["https://untrusted.invalid/callback"],"javascript_origins":["https://untrusted.invalid"]}}`, true},
		{"flat", `{"client_id":"fixture-client","client_secret":"fixture-secret"}`, true},
		{"desktop", `{"installed":{"client_id":"fixture-client","client_secret":"fixture-secret"}}`, false},
		{"ambiguous", `{"web":{"client_id":"fixture-client","client_secret":"fixture-secret"},"client_id":"other","client_secret":"other"}`, false},
		{"missing secret", `{"web":{"client_id":"fixture-client"}}`, false},
		{"trailing data", `{"client_id":"fixture-client","client_secret":"fixture-secret"} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := secfile.Write(path, []byte(tc.raw)); err != nil {
				t.Fatal(err)
			}
			cfg, configured, err := readAttachmentOAuth(path, nil)
			if configured != tc.valid || (err == nil) != tc.valid {
				t.Fatalf("configured=%v error=%v, want valid=%v", configured, err, tc.valid)
			}
			if !tc.valid {
				if strings.Contains(err.Error(), "fixture-secret") {
					t.Fatal("configuration error exposed secret")
				}
				return
			}
			if cfg.ClientID != "fixture-client" || cfg.ClientSecret != "fixture-secret" || cfg.AuthURL != "" || cfg.TokenURL != "" || cfg.RevokeURL != "" {
				t.Fatal("configuration must consume only the Web client credentials")
			}
			link, _, err := cfg.WebConsent("https://relay.example.test"+attachmentDriveCallback, "fixture-state")
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(link)
			if u.Scheme != "https" || u.Host != "accounts.google.com" || u.Query().Get("redirect_uri") != "https://relay.example.test"+attachmentDriveCallback {
				t.Fatal("downloaded endpoints or redirect URI overrode fixed relay consent")
			}
		})
	}
}

type relayDriveTransport struct {
	target  *url.URL
	base    http.RoundTripper
	observe func(*http.Request)
}
type relayProviderCounter struct {
	http.ResponseWriter
	bytes int64
}

func (w *relayProviderCounter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (t relayDriveTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.observe != nil {
		t.observe(r)
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	copy.URL = &u
	return t.base.RoundTrip(copy)
}
func newRelayDriveProvider(t *testing.T) *relayDriveProvider {
	t.Helper()
	p := &relayDriveProvider{files: map[string]relayFakeFile{}, account: "fixture-account"}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/upload/drive/v3"), "/drive/v3")
		p.mu.Lock()
		blockPath, blocked, release := p.blockPath, p.blocked, p.release
		p.mu.Unlock()
		if path == blockPath && blocked != nil {
			select {
			case blocked <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/token":
			p.tokenCalls++
			r.ParseForm()
			if p.challenge != "" {
				s := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(s[:]) != p.challenge {
					t.Error("Web PKCE verification failed")
				}
			}
			if r.Form.Get("client_secret") != "fixture-server-secret" || r.Form.Get("redirect_uri") != "https://127.0.0.1:1"+attachmentDriveCallback {
				t.Error("server OAuth configuration changed")
			}
			fmt.Fprintf(w, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","token_type":"Bearer","expires_in":3600,"scope":%q}`, gdrive.FileScope)
		case path == "/revoke":
			w.WriteHeader(200)
		case path == "/about":
			fmt.Fprintf(w, `{"user":{"permissionId":%q,"emailAddress":"admin@example.test","displayName":"Fixture admin"}}`, p.account)
		case path == "/files/generateIds":
			p.next++
			fmt.Fprintf(w, `{"ids":["drive-id-%d"]}`, p.next)
		case path == "/files" && r.Method == "POST":
			p.uploadCalls++
			if p.uploadReason != "" {
				w.WriteHeader(403)
				fmt.Fprintf(w, `{"error":{"code":403,"errors":[{"reason":%q}],"message":"PRIVATE_PROVIDER_BODY"}}`, p.uploadReason)
				return
			}
			if p.denyUpload {
				http.Error(w, `{"error":{"code":403,"message":"PRIVATE_PROVIDER_BODY"}}`, 403)
				return
			}
			if p.failUpload {
				http.Error(w, `{"error":{"code":503,"message":"PRIVATE_PROVIDER_BODY"}}`, 503)
				return
			}
			var meta map[string]any
			var data []byte
			if r.URL.Query().Get("uploadType") == "multipart" {
				_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				parts := multipart.NewReader(r.Body, params["boundary"])
				part, err := parts.NextPart()
				if err != nil {
					t.Error(err)
					return
				}
				json.NewDecoder(part).Decode(&meta)
				part, err = parts.NextPart()
				if err != nil {
					t.Error(err)
					return
				}
				data, _ = io.ReadAll(part)
			} else {
				json.NewDecoder(r.Body).Decode(&meta)
			}
			id, _ := meta["id"].(string)
			if _, exists := p.files[id]; exists {
				http.Error(w, `{"error":{"code":409}}`, 409)
				return
			}
			if meta["mimeType"] != "application/vnd.google-apps.folder" {
				meta["size"] = strconv.Itoa(len(data))
				meta["sha256Checksum"] = digest(data)
			}
			p.files[id] = relayFakeFile{meta: meta, data: data}
			p.creates++
			json.NewEncoder(w).Encode(meta)
		case strings.HasPrefix(path, "/files/"):
			id := strings.TrimPrefix(path, "/files/")
			if r.Method == "GET" && r.URL.Query().Get("alt") != "media" && p.metadataFailures[id] {
				http.Error(w, `{"error":{"code":503}}`, 503)
				return
			}
			file, ok := p.files[id]
			if !ok {
				http.Error(w, `{"error":{"code":404}}`, 404)
				return
			}
			if r.Method == "DELETE" {
				if p.deleteFailures[id] {
					http.Error(w, `{"error":{"code":503}}`, 503)
					return
				}
				delete(p.files, id)
				p.deletes++
				w.WriteHeader(204)
				return
			}
			if r.URL.Query().Get("alt") == "media" {
				data := append([]byte(nil), file.data...)
				if p.corruptRead && len(data) > 0 {
					data[0] ^= 1
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				counter := &relayProviderCounter{ResponseWriter: w}
				http.ServeContent(counter, r, "", time.Time{}, bytes.NewReader(data))
				p.mediaBytes += counter.bytes
				return
			}
			p.metadataCalls++
			meta := make(map[string]any, len(file.meta))
			for key, value := range file.meta {
				meta[key] = value
			}
			if p.omitChecksum {
				delete(meta, "sha256Checksum")
			}
			if p.badChecksum {
				meta["sha256Checksum"] = strings.Repeat("0", 64)
			}
			json.NewEncoder(w).Encode(meta)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}
func (p *relayDriveProvider) http() *http.Client {
	u, _ := url.Parse(p.server.URL)
	return &http.Client{Transport: relayDriveTransport{target: u, base: http.DefaultTransport, observe: func(r *http.Request) {
		if strings.Contains(r.URL.Path, "/files") {
			if _, hasDeadline := r.Context().Deadline(); hasDeadline {
				p.mu.Lock()
				p.workerDeadline = true
				p.mu.Unlock()
			}
		}
	}}, Timeout: 5 * time.Second}
}
func attachmentDriveHub(t *testing.T, p *relayDriveProvider, connected bool) (*Hub, member, member, func()) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hub")
	if err := secfile.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := secfile.Write(filepath.Join(dir, AttachmentDriveOAuthFile), []byte(`{"client_id":"fixture-web-client","client_secret":"fixture-server-secret"}`)); err != nil {
		t.Fatal(err)
	}
	h, err := Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf, AttachmentDriveHTTP: p.http()})
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	closeHub := func() { once.Do(func() { h.Close() }) }
	t.Cleanup(closeHub)
	a, b := enroll(t, h, "alice"), enroll(t, h, "bob")
	if _, err = h.store.db.Exec(`UPDATE agents SET admin=1 WHERE address=?`, a.addr); err != nil {
		t.Fatal(err)
	}
	if connected {
		p.mu.Lock()
		p.files["fixture-folder"] = relayFakeFile{meta: map[string]any{"id": "fixture-folder", "name": "Encrypted attachments", "mimeType": gdrive.FolderMIME, "appProperties": map[string]string{"agentnet_realm": h.realmID, "agentnet_kind": "relay_ciphertext"}}}
		p.mu.Unlock()
		c := relayDriveConnection{ClientID: "fixture-web-client", Token: gdrive.Token{Access: "fixture-access", Refresh: "fixture-refresh", Scope: gdrive.FileScope, Expiry: time.Now().Add(time.Hour)}, Account: gdrive.Account{PermissionID: "fixture-account", Email: "admin@example.test"}, FolderID: "fixture-folder", FolderName: "Encrypted attachments"}
		if err = h.store.saveRelayDriveConnection(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	return h, a, b, closeHub
}
func awaitAttachmentState(t *testing.T, h *Hub, id, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var got string
		var local int
		err := h.store.db.QueryRow(`SELECT state,local_present FROM relay_drive_blobs WHERE blob_id=?`, id).Scan(&got, &local)
		if err == nil && got == state && (state != "remote" || local == 0) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("attachment did not reach %s", state)
}

func TestRelayAttachmentDriveMoveChecksumsACLAndPortableBackup(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, closeHub := attachmentDriveHub(t, p, true)
	data := []byte("synthetic encrypted attachment")
	id := a.upload(t, h, b.addr, data)
	awaitAttachmentState(t, h, id, "remote")
	if _, err := os.Stat(h.blobPath(id, true)); !os.IsNotExist(err) {
		t.Fatal("verified remote blob left a local persistent copy", err)
	}
	if _, err := h.store.db.Exec(`INSERT INTO messages(id,sender,recipient,envelope,state,created_at) VALUES(?,?,?,?,?,?)`, "message", a.addr, b.addr, []byte("ciphertext"), protocol.StateCustody, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`UPDATE blobs SET message_id='message' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if code, _ := a.call(t, h, "GET", "/v1/blobs/"+id+"/data", nil); code != 404 {
		t.Fatal("owner bypassed recipient ACL", code)
	}
	r := signed(t, b.id, b.addr, "GET", "/v1/blobs/"+id+"/data", nil)
	r.Header.Set("Range", "bytes=2-8")
	w := serve(h, r)
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), data[2:9]) {
		t.Fatal("remote Range delivery changed", w.Code, w.Body.String())
	}
	code, body := b.call(t, h, "GET", "/v1/storage/attachments", nil)
	if code != 200 || bytes.Contains(body, []byte("admin@example.test")) || bytes.Contains(body, []byte("fixture-refresh")) {
		t.Fatal("member status leaked account or credentials", code, string(body))
	}
	var memberStatus drivecontract.RelayStorageStatus
	if err := json.Unmarshal(body, &memberStatus); err != nil || memberStatus.Local != (drivecontract.RelayAmount{}) || memberStatus.Remote != (drivecontract.RelayAmount{}) || memberStatus.Pending != (drivecontract.RelayAmount{}) || memberStatus.RemoteOnlyFiles != 0 || memberStatus.Migration != (drivecontract.RelayMigration{}) {
		t.Fatal("member status leaked relay-wide usage or progress", string(body), err)
	}
	code, body = a.call(t, h, "POST", "/v1/admin/storage/attachments", drivecontract.RelayStorageAction{Action: "disconnect", Confirm: true})
	if code != 409 || !bytes.Contains(body, []byte("1 attachment")) {
		t.Fatal("remote-only disconnect was not blocked", code, string(body))
	}
	closeHub()
	m, err := OpenMaintenance(h.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	m.attachmentHTTP = p.http()
	var archive bytes.Buffer
	if err = m.Backup(&archive); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == AttachmentDriveOAuthFile {
			t.Fatal("server OAuth secret included in portable archive")
		}
		if header.Name == "blobs/"+id+".blob" {
			got, _ := io.ReadAll(tr)
			if !bytes.Equal(got, data) {
				t.Fatal("archive is a thin reference or changed ciphertext")
			}
			found = true
		}
	}
	gz.Close()
	if !found {
		t.Fatal("remote bytes absent from existing archive layout")
	}
	// No provider or original owner-only config is needed to restore/read it.
	p.server.Close()
	os.Remove(filepath.Join(h.cfg.DataDir, AttachmentDriveOAuthFile))
	restored := filepath.Join(t.TempDir(), "restored")
	summary, err := Restore(bytes.NewReader(archive.Bytes()), restored)
	if err != nil || summary.Blobs != 1 {
		t.Fatal(summary, err)
	}
	if err = blobfile.Check(filepath.Join(restored, "blobs", id+".blob"), int64(len(data)), digest(data)); err != nil {
		t.Fatal(err)
	}
}

func TestRelayAttachmentDriveBadRemoteMetadataRetainLocalAndStableID(t *testing.T) {
	p := newRelayDriveProvider(t)
	p.badChecksum = true
	h, a, b, _ := attachmentDriveHub(t, p, true)
	data := []byte("synthetic encrypted bytes")
	id := a.upload(t, h, b.addr, data)
	awaitAttachmentState(t, h, id, "failed")
	if err := blobfile.Check(h.blobPath(id, true), int64(len(data)), digest(data)); err != nil {
		t.Fatal("local ciphertext lost before remote checksum verified", err)
	}
	p.mu.Lock()
	p.badChecksum = false
	creates := p.creates
	p.mu.Unlock()
	if err := h.moveExistingAttachments(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitAttachmentState(t, h, id, "remote")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.creates != creates {
		t.Fatal("retry created a duplicate Drive object", creates, p.creates)
	}
}

func TestRelayAttachmentDriveConsentPKCEAndCurrentAdminKey(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, false)
	if code, _ := b.call(t, h, "POST", "/v1/admin/storage/attachments", drivecontract.RelayStorageAction{Action: "connect"}); code != 403 {
		t.Fatal("member started relay OAuth", code)
	}
	code, body := a.call(t, h, "POST", "/v1/admin/storage/attachments", drivecontract.RelayStorageAction{Action: "connect"})
	var status drivecontract.RelayStorageStatus
	if code != 200 || json.Unmarshal(body, &status) != nil {
		t.Fatal(code, string(body))
	}
	u, err := url.Parse(status.ConsentURL)
	if err != nil || u.Scheme != "https" || u.Host != "accounts.google.com" {
		t.Fatal("untrusted or missing consent URL")
	}
	q := u.Query()
	if q.Get("redirect_uri") != h.cfg.PublicURL+attachmentDriveCallback || q.Get("code_challenge_method") != "S256" || q.Get("scope") != gdrive.FileScope {
		t.Fatal("wrong callback/PKCE/scope")
	}
	p.mu.Lock()
	p.challenge = q.Get("code_challenge")
	p.mu.Unlock()
	w := httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state=wrong&code=fixture", nil))
	if w.Code != 400 {
		t.Fatal("bad state accepted")
	}
	w = httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+q.Get("state")+"&code=fixture", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+q.Get("state")+"&code=fixture", nil))
	if w.Code != 400 {
		t.Fatal("callback replay accepted")
	}
	// A current admin must still own the exact signing key after consent started.
	link, err := h.beginAttachmentConsent(t.Context(), a.addr)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := url.Parse(link)
	if _, err = h.store.db.Exec(`UPDATE agents SET revoked_at=? WHERE address=?`, time.Now().Unix(), a.addr); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+next.Query().Get("state")+"&code=fixture", nil))
	if w.Code != 403 {
		t.Fatal("removed admin finished consent", w.Code)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tokenCalls != 1 {
		t.Fatal("invalid callback reached token endpoint", p.tokenCalls)
	}
}

func TestRelayAttachmentDriveReconnectMustKeepAccountAndExplicitMove(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, true)
	id := a.upload(t, h, b.addr, []byte("encrypted fixture"))
	awaitAttachmentState(t, h, id, "remote")
	link, err := h.beginAttachmentConsent(t.Context(), a.addr)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	p.mu.Lock()
	p.account = "different-account"
	p.challenge = u.Query().Get("code_challenge")
	p.mu.Unlock()
	w := httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+u.Query().Get("state")+"&code=fixture", nil))
	if w.Code != 400 {
		t.Fatal("different account stole remote-only storage", w.Code)
	}
	c, err := h.store.relayDriveConnection(context.Background())
	if err != nil || c.Account.PermissionID != "fixture-account" {
		t.Fatal("failed reconnect replaced working account", c.Account.PermissionID, err)
	}
	if code, _ := a.call(t, h, "POST", "/v1/admin/storage/attachments", drivecontract.RelayStorageAction{Action: "move"}); code != 400 {
		t.Fatal("move lacked explicit confirmation")
	}
}

func TestRelayAttachmentDriveExistingLocalNeedsMoveAndRestartResumes(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, closeHub := attachmentDriveHub(t, p, false)
	data := []byte("existing encrypted attachment")
	id := a.upload(t, h, b.addr, data)
	link, err := h.beginAttachmentConsent(t.Context(), a.addr)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	p.mu.Lock()
	p.challenge = u.Query().Get("code_challenge")
	p.mu.Unlock()
	w := httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+u.Query().Get("state")+"&code=fixture", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var pending int
	if err = h.store.db.QueryRow(`SELECT count(*) FROM relay_drive_blobs WHERE blob_id=?`, id).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("connect silently migrated existing attachments", pending, err)
	}
	p.mu.Lock()
	p.badChecksum = true
	p.mu.Unlock()
	if err = h.moveExistingAttachments(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitAttachmentState(t, h, id, "failed")
	p.mu.Lock()
	p.badChecksum = false
	before := p.creates
	p.mu.Unlock()
	closeHub()
	resumed, err := Open(Config{DataDir: h.cfg.DataDir, PublicURL: h.cfg.PublicURL, Logf: t.Logf, AttachmentDriveHTTP: p.http()})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	awaitAttachmentState(t, resumed, id, "remote")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.creates != before {
		t.Fatal("restart duplicated a stored Drive object", before, p.creates)
	}
}

func TestRelayAttachmentDriveConsentChangedKeyAndExpiry(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, _, _ := attachmentDriveHub(t, p, false)
	link, err := h.beginAttachmentConsent(t.Context(), a.addr)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	replacement, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(replacement.Public(a.addr))
	if _, err = h.store.db.Exec(`UPDATE agents SET public=? WHERE address=?`, string(raw), a.addr); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+u.Query().Get("state")+"&code=fixture", nil))
	if w.Code != 403 {
		t.Fatal("changed exact admin key completed OAuth", w.Code)
	}
	link, err = h.beginAttachmentConsent(t.Context(), a.addr)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(link)
	h.attachmentDrive.mu.Lock()
	h.attachmentDrive.consent.expires = time.Now().Add(-time.Second)
	h.attachmentDrive.mu.Unlock()
	w = httptest.NewRecorder()
	h.handleAttachmentDriveCallback(w, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+u.Query().Get("state")+"&code=fixture", nil))
	if w.Code != 400 {
		t.Fatal("expired OAuth state accepted")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tokenCalls != 0 {
		t.Fatal("invalid admin/state called Google token endpoint")
	}
}

func TestRelayAttachmentDriveCleanupKeepsUndeliveredAndRetriesDelete(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, closeHub := attachmentDriveHub(t, p, true)
	old := time.Now().Add(-48 * time.Hour).Unix()
	ids := []string{a.upload(t, h, b.addr, []byte("delivered encrypted fixture")), a.upload(t, h, b.addr, []byte("undelivered encrypted fixture"))}
	for i, id := range ids {
		awaitAttachmentState(t, h, id, "remote")
		state := protocol.StateDelivered
		if i == 1 {
			state = protocol.StateCustody
		}
		message := fmt.Sprintf("message-%d", i)
		if _, err := h.store.db.Exec(`INSERT INTO messages(id,sender,recipient,envelope,state,created_at) VALUES(?,?,?,?,?,?)`, message, a.addr, b.addr, []byte("ciphertext"), state, old); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.db.Exec(`UPDATE blobs SET message_id=? WHERE id=?`, message, id); err != nil {
			t.Fatal(err)
		}
	}
	var remoteID string
	if err := h.store.db.QueryRow(`SELECT file_id FROM relay_drive_blobs WHERE blob_id=?`, ids[0]).Scan(&remoteID); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after remote deletion but before the local row commit.
	p.mu.Lock()
	delete(p.files, remoteID)
	p.mu.Unlock()
	closeHub()
	m, err := OpenMaintenance(h.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.attachmentHTTP = p.http()
	removed, err := m.Cleanup(time.Hour, time.Hour, time.Hour)
	if err != nil || removed.Files != 1 {
		t.Fatal(removed, err)
	}
	if _, err = m.store.blob(ids[0]); !errors.Is(err, errNotFound) {
		t.Fatal("deleted attachment row retained", err)
	}
	if _, err = m.store.blob(ids[1]); err != nil {
		t.Fatal("undelivered ciphertext was removed", err)
	}
}

func TestRelayAttachmentDriveInterleavedNativeRangesStayBounded(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, true)
	const size = 9 << 20
	data := [][]byte{bytes.Repeat([]byte{0xa1}, size), bytes.Repeat([]byte{0xb2}, size)}
	ids := []string{protocol.NewID(), protocol.NewID()}
	for i, id := range ids {
		fileID := fmt.Sprintf("range-fixture-%d", i)
		sha := digest(data[i])
		p.mu.Lock()
		p.files[fileID] = relayFakeFile{data: data[i], meta: map[string]any{"id": fileID, "mimeType": "application/octet-stream", "size": strconv.Itoa(size), "sha256Checksum": sha, "appProperties": map[string]string{"agentnet_realm": h.realmID, "agentnet_blob": id, "agentnet_sha256": sha}}}
		p.mu.Unlock()
		message := fmt.Sprintf("range-message-%d", i)
		if _, err := h.store.db.Exec(`INSERT INTO messages(id,sender,recipient,envelope,state,created_at) VALUES(?,?,?,?,?,?)`, message, a.addr, b.addr, []byte("ciphertext"), protocol.StateCustody, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.db.Exec(`INSERT INTO blobs(id,owner,recipient,size,sha256,received,state,message_id,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, a.addr, b.addr, size, sha, size, protocol.BlobStored, message, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.db.Exec(`INSERT INTO relay_drive_blobs(blob_id,file_id,state,local_present) VALUES(?,?,'remote',0)`, id, fileID); err != nil {
			t.Fatal(err)
		}
	}
	var assembled [2]bytes.Buffer
	for offset := 0; offset < size; offset += 4 << 20 {
		end := min(offset+(4<<20)-1, size-1)
		for i, id := range ids {
			r := signed(t, b.id, b.addr, "GET", "/v1/blobs/"+id+"/data", nil)
			r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
			w := serve(h, r)
			if w.Code != 206 || w.Header().Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, end, size) || !bytes.Equal(w.Body.Bytes(), data[i][offset:end+1]) {
				t.Fatalf("native range changed: file%d offset%d HTTP%d", i, offset, w.Code)
			}
			assembled[i].Write(w.Body.Bytes())
		}
	}
	for i := range data {
		if digest(assembled[i].Bytes()) != digest(data[i]) {
			t.Fatal("native assembly changed ciphertext")
		}
	}
	p.mu.Lock()
	received := p.mediaBytes
	p.mu.Unlock()
	if received != 2*size {
		t.Fatalf("interleaved 4MiB ranges fetched %d provider bytes; want exactly %d", received, 2*size)
	}
	// HTTP errors, HEAD and unauthorized peers must not cause media downloads.
	if code, _ := a.call(t, h, "GET", "/v1/blobs/"+ids[0]+"/data", nil); code != 404 {
		t.Fatal("range path bypassed recipient ACL")
	}
	r := signed(t, b.id, b.addr, "GET", "/v1/blobs/"+ids[0]+"/data", nil)
	r.Header.Set("Range", fmt.Sprintf("bytes=%d-", size+1))
	if w := serve(h, r); w.Code != 416 {
		t.Fatal("unsatisfiable range semantics changed", w.Code)
	}
	if code, body := b.call(t, h, "HEAD", "/v1/blobs/"+ids[0]+"/data", nil); code != 200 || len(body) != 0 {
		t.Fatal("HEAD semantics changed", code, len(body))
	}
	p.mu.Lock()
	after := p.mediaBytes
	p.mu.Unlock()
	if after != received {
		t.Fatal("HEAD/error/unauthorized request downloaded ciphertext")
	}
	// Existing suffix and multipart behavior is still owned by ServeContent.
	r = signed(t, b.id, b.addr, "GET", "/v1/blobs/"+ids[0]+"/data", nil)
	r.Header.Set("Range", "bytes=-64")
	if w := serve(h, r); w.Code != 206 || !bytes.Equal(w.Body.Bytes(), data[0][size-64:]) {
		t.Fatal("suffix range changed")
	}
	r = signed(t, b.id, b.addr, "GET", "/v1/blobs/"+ids[0]+"/data", nil)
	r.Header.Set("Range", "bytes=0-63,128-191")
	w := serve(h, r)
	_, params, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
	if err != nil || w.Code != 206 {
		t.Fatal("multipart range changed", w.Code, err)
	}
	parts := multipart.NewReader(w.Body, params["boundary"])
	for _, offset := range []int{0, 128} {
		part, err := parts.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(part)
		if !bytes.Equal(got, data[0][offset:offset+64]) {
			t.Fatal("multipart bytes changed")
		}
	}
	p.mu.Lock()
	after = p.mediaBytes
	p.mu.Unlock()
	if after != received+192 {
		t.Fatalf("suffix/multipart amplified provider reads: %d", after-received)
	}
}

func TestRelayAttachmentDriveTransientFailureRetriesWithoutNewAction(t *testing.T) {
	p := newRelayDriveProvider(t)
	p.failUpload = true
	h, a, b, _ := attachmentDriveHub(t, p, true)
	data := []byte("queued encrypted bytes during provider outage")
	id := a.upload(t, h, b.addr, data)
	awaitAttachmentState(t, h, id, "retry")
	if err := blobfile.Check(h.blobPath(id, true), int64(len(data)), digest(data)); err != nil {
		t.Fatal("provider outage lost local custody", err)
	}
	p.mu.Lock()
	p.failUpload = false
	p.mu.Unlock()
	// No upload, reconnect, Move, or restart supplies a wake after recovery.
	awaitAttachmentState(t, h, id, "remote")
	var count int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM relay_drive_blobs WHERE blob_id=?`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry changed blob identity", count, err)
	}
}

func TestRelayAttachmentDriveDeniedConsentPausesUntilReconnect(t *testing.T) {
	p := newRelayDriveProvider(t)
	p.denyUpload = true
	h, a, b, _ := attachmentDriveHub(t, p, true)
	id := a.upload(t, h, b.addr, []byte("encrypted before consent loss"))
	awaitAttachmentState(t, h, id, "failed")
	deadline := time.Now().Add(time.Second)
	for {
		status, err := h.attachmentStorageStatus(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "needs_reconnect" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("permission failure did not pause for reconnect")
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	p.denyUpload = false
	calls := p.uploadCalls
	p.mu.Unlock()
	id2 := a.upload(t, h, b.addr, []byte("encrypted while consent paused"))
	// A fresh upload wakes the queue, but must not retry a consent failure.
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	after := p.uploadCalls
	p.mu.Unlock()
	if after != calls {
		t.Fatal("new upload bypassed the reconnect pause")
	}
	for _, id := range []string{id, id2} {
		if _, err := os.Stat(h.blobPath(id, true)); err != nil {
			t.Fatal("consent pause lost local custody", err)
		}
	}
	code, body := a.call(t, h, "POST", "/v1/admin/storage/attachments", []byte(`{"action":"connect"}`))
	if code != 200 {
		t.Fatal(code, string(body))
	}
	var status drivecontract.RelayStorageStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(status.ConsentURL)
	if err != nil {
		t.Fatal(err)
	}
	w := serve(h, httptest.NewRequest("GET", attachmentDriveCallback+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code=fixture-code", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitAttachmentState(t, h, id, "remote")
	awaitAttachmentState(t, h, id2, "remote")
}
