package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"github.com/misunders2d/agentnet/internal/tlscert"
)

func realmOpen(t *testing.T, cfg Config) *Hub {
	t.Helper()
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRealmStableAcrossHubRestartAndTLSChanges(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), PublicURL: "https://127.0.0.1:1", Logf: t.Logf}
	h := realmOpen(t, cfg)
	id := h.RealmID()
	if !validHex(id, 32) {
		t.Fatalf("not a random 128-bit namespace: %q", id)
	}
	cert, err := os.ReadFile(filepath.Join(cfg.DataDir, "tls.crt"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := secfile.Read(filepath.Join(cfg.DataDir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.PublicURL = "https://127.0.0.1:2"
	h = realmOpen(t, cfg)
	if h.RealmID() != id {
		t.Fatal("endpoint port change regenerated the workspace")
	}
	for name, want := range map[string][]byte{"tls.crt": cert, "tls.key": key} {
		got, err := os.ReadFile(filepath.Join(cfg.DataDir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("restart replaced %s: %v", name, err)
		}
	}
	h.Close()
	// Synthetic certificate renewal and a different hostname are independent
	// of the already initialized database's namespace.
	newCert, newKey, err := tlscert.Generate("renewed.example.test", "synthetic renewal", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"tls.crt": newCert, "tls.key": newKey} {
		if err := secfile.Write(filepath.Join(cfg.DataDir, name), data); err != nil {
			t.Fatal(err)
		}
	}
	cfg.PublicURL = "https://renewed.example.test"
	h = realmOpen(t, cfg)
	if h.RealmID() != id || h.certPEM == string(cert) {
		t.Fatal("certificate renewal changed identity or was not exercised")
	}
	h.Close()
	cfg.PlatformTLS, cfg.PublicURL = true, "https://platform.example.test"
	h = realmOpen(t, cfg)
	if h.RealmID() != id {
		t.Fatal("TLS configuration changed workspace identity")
	}
	h.Close()
	cfg.DataDir = t.TempDir()
	other := realmOpen(t, cfg)
	defer other.Close()
	if other.RealmID() == id || !validHex(other.RealmID(), 32) {
		t.Fatal("independent database reused a workspace identity")
	}
}

func TestRealmUpgradePreservesEnrollmentCiphertextAndKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	// Eight shipped steps precede realm; later steps must not move this fixture.
	db, err := sqlitedb.Open(path, schema[:8])
	if err != nil {
		t.Fatal(err)
	}
	old := &store{db: db}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public := id.Public("legacy/device")
	if err := old.createInvite("synthetic-legacy", "legacy", true, time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := old.enroll("synthetic-legacy", public, "legacy", nil); err != nil {
		t.Fatal(err)
	}
	message := envelope.Envelope{ID: protocol.NewID(), From: public.Address, To: public.Address}
	ciphertext := []byte("synthetic historical ciphertext")
	if _, err := old.putMessage(message, ciphertext, public.Fingerprint(), time.Now()); err != nil {
		t.Fatal(err)
	}
	blobID := protocol.NewID()
	if _, err := old.reserveBlob(public.Address, protocol.BlobReserve{ID: blobID, Recipient: public.Address, Size: 3, SHA256: digest([]byte("age"))}, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE blobs SET state = ?, received = 3, message_id = ? WHERE id = ?`, protocol.BlobStored, message.ID, blobID); err != nil {
		t.Fatal(err)
	}
	blob, err := old.blob(blobID)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := secfile.EnsureDir(filepath.Join(dir, "blobs")); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(dir, "blobs", blobID+".blob")
	if err := secfile.Write(blobPath, []byte("age")); err != nil {
		t.Fatal(err)
	}
	cert, key, err := tlscert.Generate("127.0.0.1", "synthetic old Hub", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"tls.crt": cert, "tls.key": key} {
		if err := secfile.Write(filepath.Join(dir, name), data); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf}
	h := realmOpen(t, cfg)
	realm := h.RealmID()
	member, err := h.store.agent(public.Address)
	if err != nil || !member.Admin || member.Public.Fingerprint() != public.Fingerprint() {
		t.Fatalf("upgrade replaced enrolled keys or membership: %+v %v", member, err)
	}
	var retained []byte
	if err := h.store.db.QueryRow(`SELECT envelope FROM messages WHERE id = ?`, message.ID).Scan(&retained); err != nil || string(retained) != string(ciphertext) {
		t.Fatalf("upgrade lost history: %v", err)
	}
	after, err := h.store.blob(blobID)
	if err != nil || after != blob {
		t.Fatalf("upgrade changed attachment state: %+v %v", after, err)
	}
	for name, want := range map[string][]byte{"tls.crt": cert, "tls.key": key, filepath.Join("blobs", blobID+".blob"): []byte("age")} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("upgrade changed retained file %s: %v", name, err)
		}
	}
	var version, rows int
	if err := h.store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(schema) {
		t.Fatalf("schema step not appended: %d %v", version, err)
	}
	if err := h.store.db.QueryRow(`SELECT count(*) FROM realm`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("not exactly one realm: %d %v", rows, err)
	}
	h.Close()
	h = realmOpen(t, cfg)
	defer h.Close()
	if h.RealmID() != realm {
		t.Fatal("upgraded workspace identity was not stable")
	}
}

func TestRealmCorruptPersistedIdentityRefusesWithoutReplacement(t *testing.T) {
	for _, bad := range []string{"", "invalid", strings.Repeat("A", 32), strings.Repeat("z", 32), "missing-row"} {
		name := bad
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			cfg := Config{DataDir: t.TempDir(), PublicURL: "https://synthetic.example.test", PlatformTLS: true, Logf: t.Logf}
			h := realmOpen(t, cfg)
			var err error
			if bad == "missing-row" {
				_, err = h.store.db.Exec(`DELETE FROM realm`)
			} else {
				_, err = h.store.db.Exec(`UPDATE realm SET realm_id = ? WHERE id = 1`, bad)
			}
			if err != nil {
				t.Fatal(err)
			}
			h.Close()
			if got, err := Open(cfg); err == nil || !strings.Contains(err.Error(), "corrupt Hub workspace identity") {
				if got != nil {
					got.Close()
				}
				t.Fatalf("corruption did not fail explicitly: %v", err)
			}
			st, err := openStore(filepath.Join(cfg.DataDir, "hub.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.db.Close()
			if bad == "missing-row" {
				var n int
				if err := st.db.QueryRow(`SELECT count(*) FROM realm`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("missing identity silently regenerated: %d %v", n, err)
				}
			} else {
				var saved string
				if err := st.db.QueryRow(`SELECT realm_id FROM realm WHERE id = 1`).Scan(&saved); err != nil || saved != bad {
					t.Fatalf("corrupt identity silently replaced: %q %v", saved, err)
				}
			}
		})
	}
}

func TestRealmInitializationRecoversUncommittedFirstRecord(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	abandoned := protocol.NewID()
	if _, err := tx.Exec(`UPDATE realm SET realm_id = ?, initialized = 1 WHERE id = 1`, abandoned); err != nil {
		t.Fatal(err)
	}
	tx.Rollback() // simulates a crash before the first identity commit
	h := &Hub{store: st}
	if err := h.loadRealm(); err != nil || !validHex(h.RealmID(), 32) || h.RealmID() == abandoned {
		t.Fatalf("uncommitted initialization not recovered: %v", err)
	}
	first := h.RealmID()
	if err := h.loadRealm(); err != nil || h.RealmID() != first {
		t.Fatalf("committed initialization regenerated: %v", err)
	}
}

func TestRealmVersionFieldRemainsOptional(t *testing.T) {
	legacy := protocol.VersionInfo{Version: "legacy", Protocol: protocol.ProtocolVersion}
	raw, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(raw), "realm_id") {
		t.Fatalf("legacy wire gained a required identity: %s %v", raw, err)
	}
	var decoded protocol.VersionInfo
	if err := json.Unmarshal([]byte(`{"version":"old","protocol":1}`), &decoded); err != nil || decoded.RealmID != "" {
		t.Fatalf("legacy version no longer decodes as unknown: %+v %v", decoded, err)
	}
	legacy.RealmID = protocol.NewID()
	raw, err = json.Marshal(legacy)
	if err != nil || !strings.Contains(string(raw), `"realm_id":"`+legacy.RealmID+`"`) || legacy.Protocol != 1 {
		t.Fatalf("optional realm changed protocol generation: %s %v", raw, err)
	}
}

func TestRealmVersionResponseUsesPersistedCachedIdentity(t *testing.T) {
	h, _, _ := testHub(t)
	for range 3 {
		w := serve(h, httptest.NewRequest(http.MethodGet, "/v1/version", nil))
		var v protocol.VersionInfo
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Protocol != protocol.ProtocolVersion || v.RealmID != h.RealmID() {
			t.Fatalf("version seam does not publish this workspace: %d %s", w.Code, w.Body)
		}
	}
	var saved string
	var initialized int
	if err := h.store.db.QueryRow(`SELECT realm_id, initialized FROM realm WHERE id = 1`).Scan(&saved, &initialized); err != nil || saved != h.RealmID() || initialized != 1 {
		t.Fatalf("version reads changed the persisted namespace: %q %d %v", saved, initialized, err)
	}
}
