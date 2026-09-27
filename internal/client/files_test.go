package client

import (
	"bytes"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func receive(t *testing.T, w *world) Message {
	t.Helper()
	stop := runAgent(t, w.bob)
	defer stop()
	var msgs []Message
	eventually(t, "bob to receive", func() bool {
		msgs, _ = w.bob.Inbox(false, false)
		return len(msgs) > 0
	})
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
	_, err := w.alice.Send(tctx(t), w.bob.Address, "large", "", path)
	var msg Message
	if err == nil {
		msg = receive(t, w)
		_, err = w.bob.Download(tctx(t), msg.ID, t.TempDir(), false)
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
