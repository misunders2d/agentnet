package client

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func TestStorageRetryProofUpgradePreservesLegacyUncertainty(t *testing.T) {
	index := slices.Index(schema, sendRetryProofSchema)
	if index < 0 {
		t.Fatal("retry proof migration missing")
	}
	path := filepath.Join(t.TempDir(), "client.db")
	db, err := sqlitedb.Open(path, schema[:index])
	if err != nil {
		t.Fatal(err)
	}
	oldID, newID := protocol.NewID(), protocol.NewID()
	if _, err = db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at) VALUES(?,'bob/laptop','old','{}','failed',1)`, oldID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	upgraded, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.db.Close()
	if _, err = upgraded.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at) VALUES(?,'bob/laptop','new','{}','queued',2)`, newID); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{oldID: false, newID: true} {
		var proven bool
		if err = upgraded.db.QueryRow(`SELECT handover_proven FROM outbox WHERE id=?`, id).Scan(&proven); err != nil || proven != want {
			t.Fatalf("proof after upgrade id=%s got=%v want=%v error=%v", id, proven, want, err)
		}
	}
}

func TestStorageSendRetryExactCiphertextRestartAndLegacyUncertainty(t *testing.T) {
	w := newWorld(t, "")
	base := w.alice.hub.http.Transport
	var full atomic.Bool
	full.Store(true)
	var posts atomic.Int32
	transport := storageTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/v1/blobs" && full.Load() {
			return &http.Response{StatusCode: 413, Status: "413 Request Entity Too Large", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":"relay_quota","error":"private provider detail"}`))}, nil
		}
		if r.Method == "POST" && r.URL.Path == "/v1/messages" {
			posts.Add(1)
		}
		return base.RoundTrip(r)
	})
	w.alice.hub.http.Transport = transport
	path, _ := writeFile(t, t.TempDir(), "picture.jpg", 6400)
	sent, err := w.alice.Send(tctx(t), w.bob.Address, "keep this exact text", "", path)
	if err == nil || sent.State != stateFailed || !strings.HasPrefix(sent.Detail, "send_error:relay_quota:") {
		t.Fatalf("typed stored refusal: %+v %v", sent, err)
	}
	var before, blob string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, sent.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.db.QueryRow(`SELECT blob_id FROM uploads WHERE message_id=?`, sent.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	ct, err := os.ReadFile(w.alice.spoolPath(blob))
	if err != nil {
		t.Fatal(err)
	}
	home := w.alice.home
	w.alice.Close()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.hub.http.Transport = transport
	retry := StorageSendRetry{ID: sent.ID}
	for _, guard := range []string{"handover_proven=0", "handover_proven=1,handover_started=1"} {
		if _, err = a.store.db.Exec(`UPDATE outbox SET `+guard+` WHERE id=?`, sent.ID); err != nil {
			t.Fatal(err)
		}
		v, err := a.RetryStorageSend(tctx(t), retry)
		if err != nil || v.Retried != 0 {
			t.Fatal("legacy/uncertain send retried", v, err)
		}
	}
	if _, err = a.store.db.Exec(`UPDATE outbox SET handover_proven=1,handover_started=0 WHERE id=?`, sent.ID); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(a.spoolPath(blob), []byte("damaged ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := a.RetryStorageSend(tctx(t), retry)
	if err != nil || v.Retried != 0 {
		t.Fatal("damaged ciphertext was retried", v, err)
	}
	if err = os.WriteFile(a.spoolPath(blob), ct, 0600); err != nil {
		t.Fatal(err)
	}
	full.Store(false)
	results := make(chan StorageSendRetried, 2)
	for range 2 {
		go func() { v, _ := a.RetryStorageSend(tctx(t), retry); results <- v }()
	}
	n := (<-results).Retried + (<-results).Retried
	if n != 1 {
		t.Fatal("duplicate button retry count", n)
	}
	eventually(t, "same stored message in relay custody", func() bool { return outboxState(t, a, sent.ID) == protocol.StateCustody })
	var after string
	a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, sent.ID).Scan(&after)
	if after != before || posts.Load() != 1 {
		t.Fatal("retry changed or duplicated signed ciphertext", posts.Load())
	}
	v, err = a.RetryStorageSend(tctx(t), retry)
	if err != nil || v.Retried != 0 {
		t.Fatal("custody message retried", v, err)
	}
}

func TestStorageCacheConfirmedRemoteProofRetainsOfflineAndReopens(t *testing.T) {
	w, conv, stopBob := dmFiles(t)
	path, data := writeFile(t, t.TempDir(), "cache.dat", 6500)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "received file", func() bool { return inboxCount(t, w.bob, "id=?", sent.ID) == 1 })
	stopBob()
	r, _, err := w.bob.OpenAttachment(tctx(t), sent.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, r), data) {
		t.Fatal("initial received bytes changed")
	}
	selection, err := w.bob.StorageCache(tctx(t))
	if err != nil || len(selection.Files) != 1 {
		t.Fatal(selection, err)
	}
	change := StorageCacheClear{Files: selection.Files}
	if _, err = w.bob.ClearStorageCache(tctx(t), change); err == nil {
		t.Fatal("cache cleared without confirmation")
	}
	change.Confirm = true
	spool := filepath.Join(w.bob.home, "spool", "failed-retry.age")
	if err = os.MkdirAll(filepath.Dir(spool), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(spool, []byte("failed pending ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	base := w.bob.hub.http.Transport
	w.bob.hub.http.Transport = storageTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("fixture offline") })
	v, err := w.bob.ClearStorageCache(tctx(t), change)
	if err != nil || v.Removed.Files != 0 || v.Retained.Files != 1 {
		t.Fatal("offline removed last known copy", v, err)
	}
	w.bob.hub.http.Transport = base
	v, err = w.bob.ClearStorageCache(tctx(t), change)
	if err != nil || v.Removed.Files != 1 {
		t.Fatal(v, err)
	}
	if _, err = os.Stat(spool); err != nil {
		t.Fatal("failed retry spool removed", err)
	}
	if w.bob.prefetchFiles(tctx(t)) {
		t.Fatal("manual cache clear was requeued for prefetch")
	}
	if _, err = os.Stat(w.bob.downloadPath(selection.Files[0].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("prefetch refilled cleared cache", err)
	}
	home := w.bob.home
	w.bob.Close()
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.prefetchFiles(tctx(t)) {
		t.Fatal("restart refilled manually cleared cache")
	}
	r, _, err = reopened.OpenAttachment(tctx(t), sent.ID, 0)
	if err != nil {
		t.Fatal("normal attachment retrieval after cleanup", err)
	}
	if !bytes.Equal(readAll(t, r), data) {
		t.Fatal("retrieval after cleanup changed original bytes")
	}
}
