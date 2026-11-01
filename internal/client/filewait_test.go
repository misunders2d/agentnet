package client

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestKeptAutomatically(t *testing.T) {
	for _, c := range []struct {
		name string
		size int64
		want bool
	}{
		{"clip.mp4", 10 << 20, true},
		{"clip.MOV", 10<<20 + 1, false},
		{"notes.pdf", 3 << 20, true},
		{"notes.pdf", 3<<20 + 1, false},
		{"archive", 3<<20 + 1, false},
		{"photo.JPG", 50 << 20, true}, // pictures as before
		{"photo.webp", 3<<20 + 1, true},
	} {
		if got := keptAutomatically(c.name, c.size); got != c.want {
			t.Errorf("keptAutomatically(%q, %d) = %v", c.name, c.size, got)
		}
	}
}

// Owner, November 1, 2026: big received files are offered for download, as
// on Telegram, instead of being kept in the background. A picture and a small
// file are kept as before; a file over 3 MiB waits, downloads on request with
// its progress reported, and one the relay no longer has shows as such
// instead of offering a download forever.
func TestBigReceivedFilesWaitForATap(t *testing.T) {
	if testing.Short() {
		t.Skip("large files")
	}
	w, conv, stopBob := dmFiles(t)
	dir := t.TempDir()
	big, bigData := writeFile(t, dir, "notes.bin", 3<<20+1)
	gone, _ := writeFile(t, dir, "old.bin", 3<<20+1)
	photo, _ := writeFile(t, dir, "photo.png", 3<<20+1)
	small, _ := writeFile(t, dir, "small.mp4", 4096)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Files: []OutgoingFile{{Path: big}, {Path: gone}, {Path: photo}, {Path: small}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "received files", func() bool { return inboxCount(t, w.bob, "id=?", sent.ID) == 1 })
	files := func() []FileInfo { return convMsgByID(t, w.bob, conv, sent.ID).Attachments }
	here := func(i int) bool {
		_, err := os.Stat(w.bob.downloadPath(files()[i].BlobID))
		return err == nil
	}
	eventually(t, "picture and small file kept", func() bool { return here(2) && here(3) })
	stopBob()
	if w.bob.prefetchFiles(tctx(t)) || here(0) || here(1) {
		t.Fatal("a big file was kept in the background")
	}
	for i, want := range []string{FetchWait, FetchWait, "", ""} {
		if f := files()[i]; f.Fetch != want || !f.Openable {
			t.Fatalf("file %d: fetch %q openable %v, want %q", i, f.Fetch, f.Openable, want)
		}
	}

	p, err := w.bob.FetchFile("in", sent.ID, 0)
	if err != nil || p.Total != files()[0].ctSize || p.Done {
		t.Fatal("fetch did not start", p, err)
	}
	var steps int
	for !p.Done {
		if p.Error != "" {
			t.Fatal(p.Error)
		}
		after := p.Have
		if p, err = w.bob.FileProgress(tctx(t), "in", sent.ID, 0, after, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if !p.Done && p.Have <= after {
			t.Fatal("progress answered without moving", after, p)
		}
		steps++
	}
	if steps < 2 || p.Have != p.Total {
		t.Fatal("progress reported", steps, "steps, ending at", p)
	}
	if f := files()[0]; f.Fetch != "" || !here(0) {
		t.Fatal("downloaded file still waits", f.Fetch)
	}
	r, _, err := w.bob.OpenAttachment(tctx(t), sent.ID, 0)
	if err != nil || !bytes.Equal(readAll(t, r), bigData) {
		t.Fatal("downloaded file changed", err)
	}

	// An admin's cleanup removed the other one from the relay.
	base := w.bob.hub.http.Transport
	w.bob.hub.http.Transport = storageTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/v1/blobs/") {
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"unknown attachment"}`)), Request: r}, nil
		}
		return base.RoundTrip(r)
	})
	defer func() { w.bob.hub.http.Transport = base }()
	if _, err = w.bob.FetchFile("in", sent.ID, 1); err != nil {
		t.Fatal(err)
	}
	if p, err = w.bob.FileProgress(tctx(t), "in", sent.ID, 1, 1<<62, 10*time.Second); err != nil || !p.Gone {
		t.Fatal("removed file not reported gone", p, err)
	}
	if f := files()[1]; f.Openable || f.Availability != "unavailable" || f.Fetch != "" {
		t.Fatal("removed file still offered", f.Openable, f.Availability, f.Fetch)
	}
	if _, _, err = w.bob.OpenAttachment(tctx(t), sent.ID, 1); !errors.Is(err, errFileGone) || retryable(err) {
		t.Fatal("removed file opens as a transient failure", err)
	}
}
