package hub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type member struct {
	id   *identity.Identity
	addr string
}

func enroll(t *testing.T, h *Hub, label string) member {
	t.Helper()
	id, _ := identity.Generate()
	addr := label + "/x"
	secret := protocol.NewID()
	if err := h.store.createInvite(secret, label, false, time.Hour, "admin/test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.enroll(secret, id.Public(addr), label, nil); err != nil {
		t.Fatal(err)
	}
	return member{id, addr}
}

func (m member) call(t *testing.T, h *Hub, method, path string, in any) (int, []byte) {
	t.Helper()
	var body []byte
	if b, ok := in.([]byte); ok {
		body = b
	} else if in != nil {
		body, _ = json.Marshal(in)
	}
	w := serve(h, signed(t, m.id, m.addr, method, path, body))
	return w.Code, w.Body.Bytes()
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// upload stores data as a complete blob owned by m for recipient.
func (m member) upload(t *testing.T, h *Hub, recipient string, data []byte) string {
	t.Helper()
	id := protocol.NewID()
	if c, b := m.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: id, Recipient: recipient, Size: int64(len(data)), SHA256: digest(data)}); c != 200 {
		t.Fatalf("reserve: %d %s", c, b)
	}
	if c, b := m.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=0", data); c != 200 {
		t.Fatalf("chunk: %d %s", c, b)
	}
	if c, b := m.call(t, h, "POST", "/v1/blobs/"+id+"/complete", nil); c != 200 {
		t.Fatalf("complete: %d %s", c, b)
	}
	return id
}

func blobHub(t *testing.T, quota int64) (*Hub, member, member, member) {
	t.Helper()
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1",
		Logf: t.Logf, StorageQuota: quota, MaxFileSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, enroll(t, h, "alice"), enroll(t, h, "bob"), enroll(t, h, "carol")
}

func TestQuotaHoldsUnderConcurrentReservations(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<20)
	codes := make(chan int, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{
				ID: protocol.NewID(), Recipient: bob.addr, Size: 400 << 10, SHA256: digest(nil)})
			codes <- c
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == http.StatusOK {
			ok++
		} else if c != http.StatusRequestEntityTooLarge {
			t.Fatalf("unexpected status %d", c)
		}
	}
	if ok != 2 { // 2 × 400 KiB fit in 1 MiB, a third does not
		t.Fatalf("%d reservations accepted, want 2", ok)
	}
	big := protocol.BlobReserve{ID: protocol.NewID(), Recipient: bob.addr, Size: protocol.CiphertextBound(1<<20) + 1, SHA256: digest(nil)}
	if c, _ := alice.call(t, h, "POST", "/v1/blobs", big); c != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized file: %d", c)
	}
}

func TestMessageNeedsOwnCompleteBlob(t *testing.T) {
	h, alice, bob, carol := blobHub(t, 1<<30)
	data := []byte("ciphertext")
	post := func(from member, to string, blob envelope.Blob) int {
		r, _ := identity.Generate()
		pub, _ := r.Public(to).Recipient()
		in := envelope.Inner{ID: protocol.NewID(), From: from.addr, To: to, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Attachments: []envelope.Attachment{{Blob: blob, Name: "f", SHA256: digest(nil)}}}
		env, err := envelope.Seal(in, from.id.Sign, pub)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := from.call(t, h, "POST", "/v1/messages", env)
		return c
	}
	ref := func(id string) envelope.Blob {
		return envelope.Blob{ID: id, Size: int64(len(data)), SHA256: digest(data)}
	}

	carolsBlob := carol.upload(t, h, bob.addr, data)
	if c := post(alice, bob.addr, ref(carolsBlob)); c != http.StatusConflict {
		t.Fatalf("someone else's blob: %d", c)
	}
	forCarol := alice.upload(t, h, carol.addr, data)
	if c := post(alice, bob.addr, ref(forCarol)); c != http.StatusConflict {
		t.Fatalf("blob for another recipient: %d", c)
	}
	partial := protocol.NewID()
	alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: partial, Recipient: bob.addr, Size: 10, SHA256: digest(data)})
	if c := post(alice, bob.addr, ref(partial)); c != http.StatusConflict {
		t.Fatalf("incomplete blob: %d", c)
	}
	good := alice.upload(t, h, bob.addr, data)
	wrong := ref(good)
	wrong.SHA256 = digest([]byte("other"))
	if c := post(alice, bob.addr, wrong); c != http.StatusConflict {
		t.Fatalf("wrong digest reference: %d", c)
	}
	if c := post(alice, bob.addr, ref(good)); c != http.StatusAccepted {
		t.Fatalf("valid attachment: %d", c)
	}
	if c, _ := carol.call(t, h, "GET", "/v1/blobs/"+good+"/data", nil); c != http.StatusNotFound {
		t.Fatalf("third agent download: %d", c)
	}
}

func TestChunksMustContinueAndDigestMustMatch(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<30)
	data := bytes.Repeat([]byte("x"), 100)
	id := protocol.NewID()
	alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: id, Recipient: bob.addr, Size: 100, SHA256: digest(data)})
	if c, _ := alice.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=50", data[50:]); c != http.StatusConflict {
		t.Fatalf("gap accepted: %d", c)
	}
	if c, _ := bob.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=0", data); c != http.StatusNotFound {
		t.Fatalf("non-owner chunk: %d", c)
	}
	bad := bytes.Repeat([]byte("y"), 100)
	alice.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=0", bad)
	if c, _ := alice.call(t, h, "POST", "/v1/blobs/"+id+"/complete", nil); c != http.StatusUnprocessableEntity {
		t.Fatalf("wrong bytes finalised: %d", c)
	}
	// The upload restarts from zero with the right bytes.
	if c, b := alice.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=0", data); c != 200 {
		t.Fatalf("re-upload: %d %s", c, b)
	}
	if c, _ := alice.call(t, h, "POST", "/v1/blobs/"+id+"/complete", nil); c != 200 {
		t.Fatalf("complete: %d", c)
	}
	if c, _ := alice.call(t, h, "POST", "/v1/blobs/"+id+"/complete", nil); c != 200 {
		t.Fatalf("repeated complete: %d", c)
	}
}

func TestAbandonedUploadsReclaimedCompletedKept(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<30)
	kept := alice.upload(t, h, bob.addr, []byte("done"))
	stale := protocol.NewID()
	alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: stale, Recipient: bob.addr, Size: 10, SHA256: digest(nil)})
	alice.call(t, h, "PUT", "/v1/blobs/"+stale+"?offset=0", []byte("12345"))
	old := time.Now().Add(-25 * time.Hour).Unix()
	h.store.db.Exec(`UPDATE blobs SET updated_at = ?`, old) // both idle for 25h
	alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: protocol.NewID(), Recipient: bob.addr, Size: 1, SHA256: digest(nil)})
	if _, err := h.store.blob(stale); err != errNotFound {
		t.Fatalf("stale upload kept: %v", err)
	}
	if _, err := os.Stat(h.blobPath(stale, false)); !os.IsNotExist(err) {
		t.Fatal("stale upload file kept")
	}
	if b, err := h.store.blob(kept); err != nil || b.State != protocol.BlobStored {
		t.Fatalf("completed blob reclaimed: %+v %v", b, err)
	}
	if _, err := os.Stat(h.blobPath(kept, true)); err != nil {
		t.Fatal(fmt.Errorf("completed blob file removed: %w", err))
	}
}

func TestReclaimRetriesFailedDeleteAndKeepsAccounting(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 100)
	stale := protocol.NewID()
	alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: stale, Recipient: bob.addr, Size: 60, SHA256: digest(nil)})
	alice.call(t, h, "PUT", "/v1/blobs/"+stale+"?offset=0", []byte("12345"))
	h.store.db.Exec(`UPDATE blobs SET updated_at = ?`, time.Now().Add(-25*time.Hour).Unix())
	// Make the partial file undeletable: a non-empty directory in its place.
	part := h.blobPath(stale, false)
	os.Remove(part)
	os.MkdirAll(filepath.Join(part, "x"), 0o700)

	reserve := func(size int64) int {
		c, _ := alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: protocol.NewID(), Recipient: bob.addr, Size: size, SHA256: digest(nil)})
		return c
	}
	if c := reserve(50); c != http.StatusRequestEntityTooLarge {
		t.Fatalf("undeleted upload stopped counting toward quota: %d", c)
	}
	if b, err := h.store.blob(stale); err != nil || b.State != blobReclaiming {
		t.Fatalf("stale row = %+v, %v", b, err)
	}
	os.RemoveAll(part)
	if c := reserve(50); c != http.StatusOK {
		t.Fatalf("reservation after cleanup: %d", c)
	}
	if _, err := h.store.blob(stale); err != errNotFound {
		t.Fatalf("reclaimed row kept: %v", err)
	}
}

func TestUpdatesOfMissingUploadsFail(t *testing.T) {
	h, _, _, _ := blobHub(t, 1<<30)
	if err := h.store.setReceived(protocol.NewID(), 1); err != errNotFound {
		t.Fatalf("setReceived on missing row: %v", err)
	}
	if err := h.store.setBlobState(protocol.NewID(), protocol.BlobStored); err != errNotFound {
		t.Fatalf("setBlobState on missing row: %v", err)
	}
}

// Reclamation and chunk writes race only through blobMu; whatever order they
// run in, a surviving row matches its file and a removed row leaves no file.
func TestReclaimAndChunksStayConsistent(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<30)
	data := bytes.Repeat([]byte("z"), 4000)
	for round := 0; round < 20; round++ {
		id := protocol.NewID()
		alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: id, Recipient: bob.addr, Size: 4000, SHA256: digest(data)})
		alice.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=0", data[:1000])
		h.store.db.Exec(`UPDATE blobs SET updated_at = ? WHERE id = ?`, time.Now().Add(-25*time.Hour).Unix(), id)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			alice.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=1000", data[1000:2000])
		}()
		go func() {
			defer wg.Done()
			alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: protocol.NewID(), Recipient: bob.addr, Size: 1, SHA256: digest(nil)})
		}()
		wg.Wait()
		info, statErr := os.Stat(h.blobPath(id, false))
		b, err := h.store.blob(id)
		switch {
		case err == errNotFound:
			if statErr == nil {
				t.Fatalf("round %d: reclaimed upload left its file", round)
			}
		case err != nil:
			t.Fatal(err)
		default:
			if statErr != nil || info.Size() != b.Received {
				t.Fatalf("round %d: row received %d, file %v %v", round, b.Received, info, statErr)
			}
		}
	}
}

func TestFinaliseWaitsForDurableRename(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<30)
	data := []byte("ciphertext")
	id := protocol.NewID()
	alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: id, Recipient: bob.addr, Size: int64(len(data)), SHA256: digest(data)})
	alice.call(t, h, "PUT", "/v1/blobs/"+id+"?offset=0", data)
	h.syncDir = func(string) error { return fmt.Errorf("injected fsync failure") }
	if c, _ := alice.call(t, h, "POST", "/v1/blobs/"+id+"/complete", nil); c != http.StatusInternalServerError {
		t.Fatalf("complete with failed fsync: %d", c)
	}
	if b, _ := h.store.blob(id); b.State != protocol.BlobUploading {
		t.Fatalf("stored claimed before durable rename: %s", b.State)
	}
	h.syncDir = func(string) error { return nil }
	if c, b := alice.call(t, h, "POST", "/v1/blobs/"+id+"/complete", nil); c != 200 {
		t.Fatalf("repeated complete: %d %s", c, b)
	}
	if b, _ := h.store.blob(id); b.State != protocol.BlobStored {
		t.Fatalf("state after recovery: %s", b.State)
	}
}

func TestReclaimKeepsRowUntilDirectorySynced(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<30)
	stale := protocol.NewID()
	alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: stale, Recipient: bob.addr, Size: 10, SHA256: digest(nil)})
	alice.call(t, h, "PUT", "/v1/blobs/"+stale+"?offset=0", []byte("12345"))
	h.store.db.Exec(`UPDATE blobs SET updated_at = ?`, time.Now().Add(-25*time.Hour).Unix())
	reserve := func() {
		alice.call(t, h, "POST", "/v1/blobs", protocol.BlobReserve{ID: protocol.NewID(), Recipient: bob.addr, Size: 1, SHA256: digest(nil)})
	}
	h.syncDir = func(string) error { return fmt.Errorf("injected fsync failure") }
	reserve()
	if b, err := h.store.blob(stale); err != nil || b.State != blobReclaiming {
		t.Fatalf("row dropped before directory sync: %+v %v", b, err)
	}
	h.syncDir = func(string) error { return nil }
	reserve()
	if _, err := h.store.blob(stale); err != errNotFound {
		t.Fatalf("row kept after successful reclaim: %v", err)
	}
}

func TestPresenceKeepsNewerConnection(t *testing.T) {
	ended := make(chan string, 4)
	p := presence{grace: 50 * time.Millisecond, onEnd: func(agent, s string) { ended <- s }}
	sid := protocol.NewID()
	p.connect("bob/x", protocol.SessionAd{Session: sid, Endpoint: "https://old.example"})
	p.connect("bob/x", protocol.SessionAd{Session: sid, Endpoint: "https://new.example"})
	p.disconnect("bob/x", sid) // the old, half-open connection finally closes
	time.Sleep(100 * time.Millisecond)
	if !p.live("bob/x", sid) || p.list("bob/x")[0].Ad.Endpoint != "https://new.example" {
		t.Fatal("old connection's exit removed the newer registration")
	}
	p.disconnect("bob/x", sid)
	if !p.live("bob/x", sid) {
		t.Fatal("session ended before its grace period")
	}
	select {
	case s := <-ended:
		if s != sid || p.live("bob/x", sid) {
			t.Fatal("wrong session ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session never ended")
	}
	// Reconnecting within grace keeps the session.
	p.connect("bob/x", protocol.SessionAd{Session: sid})
	p.disconnect("bob/x", sid)
	p.connect("bob/x", protocol.SessionAd{Session: sid})
	time.Sleep(100 * time.Millisecond)
	if !p.live("bob/x", sid) || len(ended) != 0 {
		t.Fatal("reconnect within grace ended the session")
	}
}

func TestRestoreRejectsUnsafeEntries(t *testing.T) {
	for _, name := range []string{"../evil", "/abs", "blobs/../../x", "other/dir/file", `..\outside`,
		`blobs\..\..\x`, `C:\x`, "c:/x", `blobs\0123456789abcdef0123456789abcdef.blob`, "blobs/NOTHEX.blob", "hub.db/.."} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 1})
		tw.Write([]byte("x"))
		tw.Close()
		gz.Close()
		if _, err := Restore(&buf, filepath.Join(t.TempDir(), "d")); err == nil {
			t.Fatalf("entry %q accepted", name)
		}
	}
}

func TestRestoreDetectsSameSizeCorruption(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<30)
	id := alice.upload(t, h, bob.addr, []byte("original ciphertext"))
	dir := h.cfg.DataDir
	h.Close()
	path := h.blobPath(id, true)
	data, _ := os.ReadFile(path)
	data[0] ^= 1 // same size, different bytes
	os.WriteFile(path, data, 0o600)
	m, err := OpenMaintenance(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := m.Backup(&buf); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if _, err := Restore(&buf, filepath.Join(t.TempDir(), "r")); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("corrupted attachment restored: %v", err)
	}
}
