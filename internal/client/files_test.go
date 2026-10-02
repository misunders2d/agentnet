package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"github.com/misunders2d/agentnet/internal/testhub"
)

const marker = "PLAINTEXT-MARKER-5d1e"

// writeFile creates a file of size bytes: random data with a plaintext
// marker at the start so storage can be scanned for leaks.
func writeFile(t *testing.T, dir, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	copy(data, marker)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

// receive runs bob's daemon until a message arrived and stops it. The
// daemon keeps received files' ciphertext in the background (prefetchFiles);
// these fixtures test fetching itself (faults, tampering, resume, access),
// so the cache is cleared once the daemon has stopped, and each test fetches
// from the Hub as a device that has not kept the file yet. Prefetching has
// its own test (TestReceivedDeviceMessageFilePrefetched).
func receive(t *testing.T, w *world) Message {
	t.Helper()
	stop := runAgent(t, w.bob)
	var msgs []Message
	eventually(t, "bob to receive", func() bool {
		msgs, _ = w.bob.Inbox(false, false)
		return len(msgs) > 0
	})
	stop()
	if err := os.RemoveAll(filepath.Join(w.bob.home, "downloads")); err != nil {
		t.Fatal(err)
	}
	return msgs[len(msgs)-1]
}

func assertNoLeak(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		for _, s := range secrets {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%q found in Hub file %s", s, path)
			}
		}
		return nil
	})
}

func assertOnlyFiles(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s contains %v, want %v", dir, got, want)
	}
}

func TestAttachmentRoundTripWhileRecipientOffline(t *testing.T) {
	w := newWorld(t, "")
	src := t.TempDir()
	big, bigData := writeFile(t, src, "secret-name-q9.bin", 3<<20+123)
	small, smallData := writeFile(t, src, "notes.txt", 100)
	res, err := w.alice.Send(tctx(t), w.bob.Address, "two files", "", big, small)
	if err != nil || res.State != protocol.StateCustody {
		t.Fatalf("send = %+v, %v", res, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 0 {
		t.Fatalf("spool not released after custody: %d files", len(entries))
	}
	assertNoLeak(t, w.hub.Dir, marker, "secret-name-q9")

	msg := receive(t, w)
	if len(msg.Attachments) != 2 || msg.Attachments[0].Name != "secret-name-q9.bin" {
		t.Fatalf("attachments = %+v", msg.Attachments)
	}
	out := t.TempDir()
	paths, err := w.bob.Download(tctx(t), msg.ID, out, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]byte{bigData, smallData} {
		if got, _ := os.ReadFile(paths[i]); !bytes.Equal(got, want) {
			t.Fatalf("file %d differs", i)
		}
	}
	// Existing output is never replaced without --force.
	os.WriteFile(paths[1], []byte("mine"), 0o600)
	if _, err := w.bob.Download(tctx(t), msg.ID, out, false); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite without force: %v", err)
	}
	if got, _ := os.ReadFile(paths[1]); string(got) != "mine" {
		t.Fatal("existing file changed")
	}
	if _, err := w.bob.Download(tctx(t), msg.ID, out, true); err != nil {
		t.Fatalf("download with force: %v", err)
	}
	assertOnlyFiles(t, out, "notes.txt", "secret-name-q9.bin")
}

// countPuts counts chunk uploads reaching the transport.
type countPuts struct {
	base http.RoundTripper
	n    atomic.Int64
}

func (c *countPuts) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "PUT" {
		c.n.Add(1)
	}
	return c.base.RoundTrip(r)
}

func TestUploadResumesAfterClientAndHubRestart(t *testing.T) {
	w := newWorld(t, "")
	path, data := writeFile(t, t.TempDir(), "report.bin", 3<<20-8192) // ciphertext fits 6 chunks
	f := injectFaults(w.alice)
	f.addAfter("PUT", "/v1/blobs/", 2, 1, false) // third chunk fails
	res, err := w.alice.Send(tctx(t), w.bob.Address, "resume me", "", path)
	if err != nil || res.State != stateQueued {
		t.Fatalf("interrupted send = %+v, %v", res, err)
	}

	// Restart both sides; the new client resumes the same spooled upload.
	aliceHome := w.alice.home
	w.alice.Close()
	w.hub.Stop()
	w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
	alice, err := Open(aliceHome)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	puts := &countPuts{base: alice.hub.http.Transport}
	alice.hub.http.Transport = puts
	f2 := injectFaults(alice)
	f2.add("POST", "/complete", 1, true) // finalize succeeds, its response is lost
	if err := alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, alice, res.ID); s != protocol.StateCustody {
		t.Fatalf("after resume state %s", s)
	}
	if n := puts.n.Load(); n != 4 {
		t.Fatalf("resumed upload sent %d chunks, want 4 (not restarted)", n)
	}
	msg := receive(t, w)
	if msg.ID != res.ID {
		t.Fatalf("got message %s", msg.ID)
	}
	paths, err := w.bob.Download(tctx(t), msg.ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("resumed file differs")
	}
}

func TestDownloadResumesAndNeverExposesPartialFile(t *testing.T) {
	w := newWorld(t, "")
	path, data := writeFile(t, t.TempDir(), "big.bin", 10<<20) // 3 ranges
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "big", "", path); err != nil {
		t.Fatal(err)
	}
	msg := receive(t, w)
	out := t.TempDir()
	f := injectFaults(w.bob)
	f.addAfter("GET", "/data", 1, 1, false)
	if _, err := w.bob.Download(tctx(t), msg.ID, out, false); err == nil {
		t.Fatal("interrupted download succeeded")
	}
	assertOnlyFiles(t, out) // nothing released, no temp left
	part, err := os.Stat(w.bob.downloadPath(msg.Attachments[0].BlobID) + ".part")
	if err != nil || part.Size() != downloadRange {
		t.Fatalf("partial ciphertext: %v %v", part, err)
	}

	w.bob.Close()
	w.hub.Stop()
	w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
	bob, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	paths, err := bob.Download(tctx(t), msg.ID, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("resumed download differs")
	}
	assertOnlyFiles(t, out, "big.bin")
}

func TestTamperedAttachmentRejected(t *testing.T) {
	w := newWorld(t, "")
	path, _ := writeFile(t, t.TempDir(), "x.bin", 70000)
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "x", "", path); err != nil {
		t.Fatal(err)
	}
	msg := receive(t, w)
	blob := filepath.Join(w.hub.Dir, "blobs", msg.Attachments[0].BlobID+".blob")
	data, _ := os.ReadFile(blob)
	data[len(data)/2] ^= 1
	os.WriteFile(blob, data, 0o600)
	out := t.TempDir()
	if _, err := w.bob.Download(tctx(t), msg.ID, out, false); err == nil || !strings.Contains(err.Error(), "signed digest") {
		t.Fatalf("tampered attachment: %v", err)
	}
	assertOnlyFiles(t, out)
}

func TestAttachmentAccessIsRecipientOnly(t *testing.T) {
	w := newWorld(t, "")
	path, _ := writeFile(t, t.TempDir(), "x.bin", 1000)
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "x", "", path); err != nil {
		t.Fatal(err)
	}
	msg := receive(t, w)
	blobPath := "/v1/blobs/" + msg.Attachments[0].BlobID + "/data"
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	var he *HubError
	if err := carol.hub.getRange(tctx(t), blobPath, 0, 10, &bytes.Buffer{}); !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("third agent fetch: %v", err)
	}
	if err := w.alice.hub.getRange(tctx(t), blobPath, 0, 10, &bytes.Buffer{}); !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("sender fetch after custody: %v", err)
	}
	if err := w.alice.Revoke(tctx(t), w.bob.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.Download(tctx(t), msg.ID, t.TempDir(), false); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked download: %v", err)
	}
}

func TestAttachmentMemoryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large file")
	}
	w := newWorld(t, "")
	const size = 48 << 20
	path, _ := writeFile(t, t.TempDir(), "large.bin", size)
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}
	}()
	// Moving 48 MiB through the Hub's disk takes longer than one request's
	// budget on slow CI disks (Windows).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := w.alice.Send(ctx, w.bob.Address, "large", "", path)
	if err == nil && res.State != protocol.StateCustody {
		err = fmt.Errorf("send not in custody: %s (%s)", res.State, res.Detail)
	}
	var msg Message
	if err == nil {
		msg = receive(t, w)
		_, err = w.bob.Download(ctx, msg.ID, t.TempDir(), false)
	}
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	// Client, Hub and recipient all run in this process; a whole-file buffer
	// anywhere would push the heap past the file size.
	if grew := int64(peak.Load()) - int64(base.HeapInuse); grew > size/2 {
		t.Fatalf("heap grew %d MiB while moving a %d MiB file", grew>>20, size>>20)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":       "report.pdf",
		"../../etc/passwd": "_.._etc_passwd",
		`..\..\x.exe`:      `_.._x.exe`,
		"..":               "attachment",
		"":                 "attachment",
		"a\x00b\nc":        "a_b_c",
		"CON.txt":          "_CON.txt",
		"/abs/path":        "_abs_path",
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoreUpgradeFromM1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := sqlitedb.Open(path, schema[:1])
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at) VALUES('m', 'a/b', 1, 'message', 'kept', 1)`)
	db.Close()
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	msgs, err := st.inbox(false)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "kept" {
		t.Fatalf("after upgrade: %+v, %v", msgs, err)
	}
}

// R2: a second file's interruption, a crash after release but before the
// saved path is recorded, colliding names, and a modified existing output.
func TestMultiFileDownloadResumesWithUniqueNames(t *testing.T) {
	w := newWorld(t, "")
	src := t.TempDir()
	os.Mkdir(filepath.Join(src, "a"), 0o700)
	os.Mkdir(filepath.Join(src, "b"), 0o700)
	p1, d1 := writeFile(t, filepath.Join(src, "a"), "x.bin", 1000)
	p2, d2 := writeFile(t, filepath.Join(src, "b"), "x.bin", 5<<20) // two ranges
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "same names", "", p1, p2); err != nil {
		t.Fatal(err)
	}
	msg := receive(t, w)
	out := t.TempDir()
	f := injectFaults(w.bob)
	f.addAfter("GET", "/data", 2, 1, false) // first file ok, second file's 2nd range fails
	if _, err := w.bob.Download(tctx(t), msg.ID, out, false); err == nil {
		t.Fatal("interrupted download succeeded")
	}
	assertOnlyFiles(t, out, "x.bin")

	paths, err := w.bob.Download(tctx(t), msg.ID, out, false) // plain rerun, no --force
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(paths[1]) != "x (2).bin" {
		t.Fatalf("colliding name saved as %s", paths[1])
	}
	for i, want := range [][]byte{d1, d2} {
		if got, _ := os.ReadFile(paths[i]); !bytes.Equal(got, want) {
			t.Fatalf("file %d differs", i)
		}
	}

	// Crash after the final link, before the saved path was recorded.
	w.bob.store.db.Exec(`UPDATE attachments SET saved_path = NULL`)
	if _, err := w.bob.Download(tctx(t), msg.ID, out, false); err != nil {
		t.Fatalf("rerun after unrecorded release: %v", err)
	}

	// A changed file is never mistaken for ours.
	os.WriteFile(paths[0], []byte("edited by the user"), 0o600)
	if _, err := w.bob.Download(tctx(t), msg.ID, out, false); !errors.Is(err, ErrExists) {
		t.Fatalf("modified output: %v", err)
	}
	if _, err := w.bob.Download(tctx(t), msg.ID, out, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, d1) {
		t.Fatal("forced download did not restore the file")
	}
	assertOnlyFiles(t, out, "x (2).bin", "x.bin")
}

func TestFinalNames(t *testing.T) {
	got := finalNames([]FileInfo{{Name: "a.txt"}, {Name: "A.TXT"}, {Name: "../a.txt"}, {Name: "a.txt"}, {Name: ".."}, {Name: ""}})
	want := []string{"a.txt", "A (2).TXT", "_a.txt", "a (3).txt", "attachment", "attachment (2)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("finalNames = %q, want %q", got, want)
	}
}

// staleOffsets answers chunk uploads with 409 as if another process had
// moved the offset. With forward, the chunk really reaches the Hub first.
type staleOffsets struct {
	base    http.RoundTripper
	forward bool
}

func (s staleOffsets) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != "PUT" {
		return s.base.RoundTrip(r)
	}
	if s.forward {
		resp, err := s.base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
	}
	return &http.Response{StatusCode: http.StatusConflict, Status: "409 Conflict", Request: r, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"error":"chunk does not continue the upload"}`))}, nil
}

// R5: conflicts that come with progress never exhaust the retry budget, and
// contention without progress leaves the message queued, not failed.
func TestUploadConflicts(t *testing.T) {
	w := newWorld(t, "")
	path, data := writeFile(t, t.TempDir(), "c.bin", 3<<20) // 7 chunks, each answered 409
	base := w.alice.hub.http.Transport
	w.alice.hub.http.Transport = staleOffsets{base: base, forward: true}
	res, err := w.alice.Send(tctx(t), w.bob.Address, "contended", "", path)
	if err != nil || res.State != protocol.StateCustody {
		t.Fatalf("progressing conflicts: %+v, %v", res, err)
	}

	w.alice.hub.http.Transport = staleOffsets{base: base}
	res, err = w.alice.Send(tctx(t), w.bob.Address, "stuck", "", path)
	if err != nil || res.State != stateQueued {
		t.Fatalf("contention without progress: %+v, %v", res, err)
	}
	w.alice.hub.http.Transport = base
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, w.alice, res.ID); s != protocol.StateCustody {
		t.Fatalf("queued message after contention cleared: %s", s)
	}
	msg := receive(t, w)
	paths, err := w.bob.Download(tctx(t), msg.ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("file differs")
	}
}

// A released file whose directory sync failed is not recorded as saved
// until a later run syncs successfully.
func TestSavedOnlyAfterDirectorySync(t *testing.T) {
	w := newWorld(t, "")
	path, data := writeFile(t, t.TempDir(), "s.bin", 5000)
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "sync", "", path); err != nil {
		t.Fatal(err)
	}
	msg := receive(t, w)
	out := t.TempDir()
	defer func(f func(string) error) { syncDir = f }(syncDir)
	syncDir = func(string) error { return errors.New("injected fsync failure") }
	// Run 0 fails syncing the ciphertext cache, run 1 releases the final file
	// but fails its sync, run 2 finds the released file and fails again.
	for i := 0; i < 3; i++ {
		if _, err := w.bob.Download(tctx(t), msg.ID, out, false); err == nil {
			t.Fatalf("run %d: success despite failed directory sync", i)
		}
		if files, _ := w.bob.store.attachments(msg.ID); files[0].SavedPath != "" {
			t.Fatalf("run %d: recorded saved before sync", i)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "s.bin")); err != nil {
		t.Fatalf("file was not released by the failing runs: %v", err)
	}
	syncDir = func(string) error { return nil }
	paths, err := w.bob.Download(tctx(t), msg.ID, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("file differs")
	}
	if files, _ := w.bob.store.attachments(msg.ID); files[0].SavedPath != paths[0] {
		t.Fatalf("saved path = %q", files[0].SavedPath)
	}
}

func TestLocalCleanupKeepsQueuedSpool(t *testing.T) {
	w := newWorld(t, "")
	path, _ := writeFile(t, t.TempDir(), "q.bin", 1000)
	f := injectFaults(w.alice)
	f.add("POST", "/v1/blobs", 1, false)
	queued, err := w.alice.Send(tctx(t), w.bob.Address, "queued", "", path)
	if err != nil || queued.State != stateQueued {
		t.Fatalf("queued: %+v %v", queued, err)
	}
	stray := filepath.Join(w.alice.home, "spool", protocol.NewID()+".age") // abandoned by a crash
	os.WriteFile(stray, []byte("x"), 0o600)
	release, err := lockfile.Acquire(filepath.Join(w.alice.home, "daemon.lock")) // as a running daemon does
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.Cleanup(false); err == nil {
		t.Fatal("cleanup ran beside the daemon")
	}
	release()
	r, err := w.alice.Cleanup(false)
	if err != nil || r.SpoolFiles != 1 {
		t.Fatalf("cleanup = %+v, %v", r, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 1 {
		t.Fatalf("queued message's spool removed: %d left", len(entries))
	}
}

// R2: a send that has spooled its file but not yet written its outbox row
// holds the spool lock, so cleanup cannot delete its only encrypted copy.
func TestCleanupWaitsForInFlightSpool(t *testing.T) {
	w := newWorld(t, "")
	path, data := writeFile(t, t.TempDir(), "f.bin", 50000)
	paused, resume := make(chan struct{}), make(chan struct{})
	beforeOutbox = func() { close(paused); <-resume }
	defer func() { beforeOutbox = func() {} }()
	done := make(chan error, 1)
	var res SendResult
	go func() {
		var err error
		res, err = w.alice.Send(tctx(t), w.bob.Address, "racing cleanup", "", path)
		done <- err
	}()
	<-paused
	if _, err := w.alice.Cleanup(false); err == nil {
		t.Fatal("cleanup ran while a send was between spooling and its outbox write")
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 1 {
		t.Fatalf("spool changed during the send: %d files", len(entries))
	}
	close(resume)
	if err := <-done; err != nil || res.State != protocol.StateCustody {
		t.Fatalf("send = %+v, %v", res, err)
	}
	if r, err := w.alice.Cleanup(false); err != nil || r.SpoolFiles != 0 {
		t.Fatalf("cleanup after send = %+v, %v", r, err)
	}
	msg := receive(t, w)
	paths, err := w.bob.Download(tctx(t), msg.ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("file differs")
	}
}

// A file sent in a device message opens on the sender later, from the copy
// kept for itself at send time (encrypted to its own key); a sent file
// with no kept copy is honestly not openable, never substituted.
func TestSenderOpensKeptDeviceMessageFile(t *testing.T) {
	w := newWorld(t, "")
	path, data := writeFile(t, t.TempDir(), "plan.txt", 5000)
	res, err := w.alice.Send(tctx(t), w.bob.Address, "the plan", "", path)
	if err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, filepath.Join(w.alice.home, "kept"), marker) // kept copies are ciphertext
	r, f, err := w.alice.OpenFileFrom(tctx(t), "", res.ID, 0)
	if err != nil || f.Name != "plan.txt" || !bytes.Equal(readAll(t, r), data) {
		t.Fatalf("open own sent file: %+v %v", f, err)
	}
	c, err := w.alice.Conversation(res.ID, 0, 0)
	if err != nil || len(c.Messages) != 1 || len(c.Messages[0].Attachments) != 1 || !c.Messages[0].Attachments[0].Openable {
		t.Fatalf("sender's view: %+v %v", c, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "opened")); len(entries) != 0 {
		t.Fatalf("plaintext left after close: %d", len(entries))
	}
	// Without a kept copy (sent before copies were kept): not openable.
	if err := os.Remove(w.alice.keptPath(f.SHA256)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.alice.OpenFileFrom(tctx(t), "", res.ID, 0); !errors.Is(err, errNotKept) {
		t.Fatalf("open without a kept copy: %v", err)
	}
	c, _ = w.alice.Conversation(res.ID, 0, 0)
	if c.Messages[0].Attachments[0].Openable {
		t.Fatal("a sent file with no kept copy is shown openable")
	}
	// The recipient's side is unchanged: it opens what it received.
	msg := receive(t, w)
	r, f, err = w.bob.OpenFileFrom(tctx(t), "in", msg.ID, 0)
	if err != nil || !bytes.Equal(readAll(t, r), data) || f.Name != "plan.txt" {
		t.Fatalf("recipient opens file: %+v %v", f, err)
	}
	assertOnlyFiles(t, filepath.Join(w.bob.home, "opened"))
}

// A file received in a device message is kept here (its ciphertext, after
// the signed digest matched) without anyone opening it, as conversation
// files are, so it survives the Hub dropping the blob later.
func TestReceivedDeviceMessageFilePrefetched(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.bob)
	defer stop()
	path, _ := writeFile(t, t.TempDir(), "report.bin", 200000)
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "report", "", path); err != nil {
		t.Fatal(err)
	}
	var msgs []Message
	eventually(t, "bob to receive", func() bool {
		msgs, _ = w.bob.Inbox(false, false)
		return len(msgs) == 1 && len(msgs[0].Attachments) == 1
	})
	blob := msgs[0].Attachments[0].BlobID
	eventually(t, "ciphertext kept without opening", func() bool {
		_, err := os.Stat(w.bob.downloadPath(blob))
		return err == nil
	})
	if _, err := os.Stat(w.bob.downloadPath(blob) + ".part"); err == nil {
		t.Fatal("a partial file counts as kept")
	}
	if entries, _ := os.ReadDir(filepath.Join(w.bob.home, "opened")); len(entries) != 0 {
		t.Fatal("prefetching wrote plaintext")
	}
}

// Plaintext an earlier run left while viewing a file is removed when the
// daemon starts; files the person saved elsewhere are not touched.
func TestOpenedPlaintextSweptAtDaemonStart(t *testing.T) {
	w := newWorld(t, "")
	opened := filepath.Join(w.bob.home, "opened")
	if err := os.MkdirAll(opened, 0o700); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(opened, ".agentnet-123.part")
	os.WriteFile(leftover, []byte("plaintext"), 0o600)
	saved, _ := writeFile(t, t.TempDir(), "saved.txt", 10)
	stop := runAgent(t, w.bob)
	eventually(t, "leftover removed", func() bool { _, err := os.Stat(leftover); return errors.Is(err, os.ErrNotExist) })
	stop()
	if _, err := os.Stat(saved); err != nil {
		t.Fatal("a saved file was touched")
	}
}

// A peer chooses the ids of what it sends, so a received message can carry
// the id of a message this device sent. The two are different files: an
// open that names the direction gets exactly that file, and one that does
// not is refused rather than served the other direction's bytes.
func TestOpenFileSameIDInBothDirectionsFailsClosed(t *testing.T) {
	w := newWorld(t, "")
	path, mine := writeFile(t, t.TempDir(), "mine.bin", 3000)
	res, err := w.alice.Send(tctx(t), w.bob.Address, "mine", "", path)
	if err != nil {
		t.Fatal(err)
	}
	// A received message with the SAME id and another manifest (as if bob
	// had chosen alice's id), stored the way the daemon stores what verified.
	theirs := []byte("theirs-not-mine")
	sum := sha256.Sum256(theirs)
	in := envelope.Inner{V: 1, ID: res.ID, From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "theirs",
		Attachments: []envelope.Attachment{{Blob: envelope.Blob{ID: protocol.NewID(), Size: 200, SHA256: strings.Repeat("ab", 32)}, Name: "theirs.bin", Size: int64(len(theirs)), SHA256: hex.EncodeToString(sum[:])}}}
	if err := w.alice.store.addInbox(in, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.alice.OpenFileFrom(tctx(t), "", res.ID, 0); !errors.Is(err, ErrAmbiguousMessage) {
		t.Fatalf("ambiguous id: %v", err)
	}
	r, f, err := w.alice.OpenFileFrom(tctx(t), "out", res.ID, 0)
	if err != nil || f.Name != "mine.bin" || !bytes.Equal(readAll(t, r), mine) {
		t.Fatalf("out: %+v %v", f, err)
	}
	r, f, err = w.alice.OpenFileFrom(tctx(t), "in", res.ID, 0)
	if err == nil {
		got := readAll(t, r)
		if bytes.Equal(got, mine) {
			t.Fatal("the received reference served the sent file")
		}
		t.Fatalf("a blob the Hub never held opened: %+v", f)
	}
	if f.Name != "theirs.bin" {
		t.Fatalf("in resolved to %+v", f)
	}
	if _, _, err := w.alice.OpenFileFrom(tctx(t), "sideways", res.ID, 0); err == nil {
		t.Fatal("an unknown direction was accepted")
	}
}
