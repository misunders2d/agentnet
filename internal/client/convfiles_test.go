package client

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// dmFiles: alice and bob with a DM, both daemons running.
func dmFiles(t *testing.T) (*world, string, func()) {
	t.Helper()
	w := newWorld(t, "")
	runAgent(t, w.alice)
	stopBob := runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	return w, newDM(t, w.alice, w.bob), stopBob
}

func convMsgByID(t *testing.T, a *Agent, conv, id string) ConvMessage {
	t.Helper()
	m, n := convMsg(t, a, conv, func(m ConvMessage) bool { return m.ID == id })
	if n != 1 {
		t.Fatalf("%s holds %d copies of %s", a.Address, n, id)
	}
	return m
}

func readAll(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Files go with a DM turn, encrypted to the other device: text and files,
// a file-only turn, several files, an empty file. Both sides list them; the
// recipient saves them (names made safe) or opens one without saving; the
// sender's spool is released once the Hub holds the message; the Hub never
// holds plaintext.
func TestDMFilesRoundTrip(t *testing.T) {
	w, conv, _ := dmFiles(t)
	dir := t.TempDir()
	img, imgData := writeFile(t, dir, "photo.png", 70000)
	doc, docData := writeFile(t, dir, "notes.txt", 1200)
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, nil, 0o600)

	withText, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "the chart", Files: []OutgoingFile{{Path: img}}})
	if err != nil {
		t.Fatal(err)
	}
	fileOnly, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Files: []OutgoingFile{{Path: doc, Name: "../../etc/passwd"}, {Path: empty}, {Path: img, Name: "second copy.png"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{}); err == nil {
		t.Fatal("a turn with neither text nor files was sent")
	}
	eventually(t, "bob to hold both", func() bool {
		return inboxCount(t, w.bob, `id IN (?, ?)`, withText.ID, fileOnly.ID) == 2
	})
	got := convMsgByID(t, w.bob, conv, fileOnly.ID)
	if got.Body != "" || len(got.Attachments) != 3 || got.Attachments[0].Name != "../../etc/passwd" || got.Attachments[1].Size != 0 ||
		got.Attachments[2].Name != "second copy.png" {
		t.Fatalf("bob's file-only turn: %+v", got)
	}
	if sent := convMsgByID(t, w.alice, conv, fileOnly.ID); len(sent.Attachments) != 3 || sent.Attachments[0].Name != "../../etc/passwd" {
		t.Fatalf("alice's view of what she sent: %+v", sent)
	}
	eventually(t, "alice's spool released", func() bool {
		entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool"))
		return len(entries) == 0
	})
	assertNoLeak(t, w.hub.Dir, marker)

	// Opened without saving: checked, and nothing recorded.
	r, f, err := w.bob.OpenAttachment(tctx(t), withText.ID, 0)
	if err != nil || f.Name != "photo.png" || !bytes.Equal(readAll(t, r), imgData) {
		t.Fatalf("open: %+v %v", f, err)
	}
	if m := convMsgByID(t, w.bob, conv, withText.ID); m.Attachments[0].SavedPath != "" {
		t.Fatal("opening recorded a saved path")
	}
	if entries, _ := os.ReadDir(filepath.Join(w.bob.home, "opened")); len(entries) != 0 {
		t.Fatalf("the opened copy stayed: %v", entries)
	}
	if _, _, err := w.bob.OpenAttachment(tctx(t), withText.ID, 1); err == nil {
		t.Fatal("opened a file that is not there")
	}
	if _, _, err := w.alice.OpenAttachment(tctx(t), withText.ID, 0); err == nil {
		t.Fatal("the sender opened her own sent file")
	}

	// Saved: safe, unique names inside the folder.
	out := t.TempDir()
	saved, err := w.bob.Download(tctx(t), fileOnly.ID, out, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range saved {
		if filepath.Dir(p) != out {
			t.Fatalf("saved outside the folder: %s", p)
		}
		want := [][]byte{docData, nil, imgData}[i]
		if data, _ := os.ReadFile(p); !bytes.Equal(data, want) {
			t.Fatalf("%s differs", p)
		}
	}
	if m := convMsgByID(t, w.bob, conv, fileOnly.ID); m.Attachments[0].SavedPath != saved[0] {
		t.Fatalf("saved path not shown: %+v", m.Attachments[0])
	}
}

// Refused before anything is stored or spooled: too many files, one over
// the size limit, a name with control characters, files on a request to an
// agent; nothing is left in the spool.
func TestDMFilesRefused(t *testing.T) {
	w, conv, _ := dmFiles(t)
	dir := t.TempDir()
	small, _ := writeFile(t, dir, "small", 100)
	big, _ := writeFile(t, dir, "big", 5000)
	old := MaxFileSize
	MaxFileSize = 4096
	t.Cleanup(func() { MaxFileSize = old })
	nine := make([]OutgoingFile, envelope.MaxAttachments+1)
	for i := range nine {
		nine[i] = OutgoingFile{Path: small}
	}
	for name, m := range map[string]ConvOutgoing{
		"too many":      {Body: "x", Files: nine},
		"too big":       {Body: "x", Files: []OutgoingFile{{Path: small}, {Path: big}}},
		"control chars": {Body: "x", Files: []OutgoingFile{{Path: small, Name: "a\x1b[31mb"}}},
		"a directory":   {Body: "x", Files: []OutgoingFile{{Path: dir}}},
		"to an agent":   {Kind: envelope.KindQuestion, Body: "x", PID: strings.Repeat("a", 32), Files: []OutgoingFile{{Path: small}}},
	} {
		if _, err := w.alice.SendConv(tctx(t), conv, m); err == nil {
			t.Errorf("%s: sent", name)
		}
	}
	if n := count(t, w.alice, "outbox WHERE conv IS NOT NULL"); n != 0 {
		t.Fatalf("%d refused messages stored", n)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 0 {
		t.Fatalf("spool not cleaned: %v", entries)
	}
}

// A file whose ciphertext was damaged is refused, whether opened or saved,
// and nothing unchecked is handed over.
func TestDMFileDamagedRefused(t *testing.T) {
	w, conv, _ := dmFiles(t)
	path, _ := writeFile(t, t.TempDir(), "x.bin", 70000)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "x", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ?`, sent.ID) == 1 })
	blobID := convMsgByID(t, w.bob, conv, sent.ID).Attachments[0].BlobID
	blob := filepath.Join(w.hub.Dir, "blobs", blobID+".blob")
	data, _ := os.ReadFile(blob)
	data[len(data)/2] ^= 1
	os.WriteFile(blob, data, 0o600)
	if _, _, err := w.bob.OpenAttachment(tctx(t), sent.ID, 0); err == nil || !strings.Contains(err.Error(), "signed digest") {
		t.Fatalf("damaged file opened: %v", err)
	}
	out := t.TempDir()
	if _, err := w.bob.Download(tctx(t), sent.ID, out, false); err == nil {
		t.Fatal("damaged file saved")
	}
	assertOnlyFiles(t, out)
}

// A turn with files survives a failed upload and a restart as one message:
// it stays queued with the reason, then goes out once, files and all.
func TestDMFilesQueuedAcrossRestart(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	stopAlice := runAgent(t, w.alice)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	stopAlice()
	injectFaults(w.alice).add("POST", "/v1/blobs", 1, false)
	path, data := writeFile(t, t.TempDir(), "report.pdf", 70000)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "report", Files: []OutgoingFile{{Path: path}}})
	if err != nil || sent.State != stateQueued || !strings.Contains(sent.Detail, "attachment upload") {
		t.Fatalf("send while the upload fails: %+v %v", sent, err)
	}
	os.Remove(path) // the spool holds what is sent
	runAgent(t, w.alice)
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `conv = ?`, conv) == 1 })
	m := convMsgByID(t, w.bob, conv, sent.ID)
	r, _, err := w.bob.OpenAttachment(tctx(t), m.ID, 0)
	if err != nil || !bytes.Equal(readAll(t, r), data) {
		t.Fatalf("the queued file: %v", err)
	}
	if n := inboxCount(t, w.bob, `conv = ?`, conv); n != 1 {
		t.Fatalf("%d messages at bob", n)
	}
}

// An agent is told that a shared message had files, never given them.
func TestAgentContextNamesFilesOnly(t *testing.T) {
	w, conv, _ := dmFiles(t)
	path, _ := writeFile(t, t.TempDir(), "secret.txt", 500)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "see attached", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ?`, sent.ID) == 1 })
	pid := participate(t, w, conv, []string{sent.LID}, nil)
	c, err := w.bob.ParticipationContext(pid, 0)
	if err != nil || len(c.lines) != 1 || !strings.Contains(c.lines[0], "see attached (1 attached file(s), not given to you)") ||
		strings.Contains(strings.Join(c.lines, "\n"), marker) {
		t.Fatalf("context: %q %v", c.lines, err)
	}
}

// A page's upload is staged privately and bounded; cleanup removes it.
func TestStageUpload(t *testing.T) {
	w := newWorld(t, "")
	old := MaxFileSize
	MaxFileSize = 1000
	t.Cleanup(func() { MaxFileSize = old })
	path, cleanup, err := w.alice.StageUpload("photo.png", strings.NewReader(strings.Repeat("x", 1000)))
	if err != nil {
		t.Fatal(err)
	}
	// secfile.Read refuses a file other users can reach (mode bits on Unix,
	// the ACL on Windows).
	if data, err := secfile.Read(path); err != nil || len(data) != 1000 || filepath.Dir(path) != filepath.Join(w.alice.home, "staging") {
		t.Fatalf("staged %s: %d bytes, %v", path, len(data), err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cleanup kept the staged file")
	}
	if _, _, err := w.alice.StageUpload("big", strings.NewReader(strings.Repeat("x", 1001))); err == nil {
		t.Fatal("staged more than the limit")
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "staging")); len(entries) != 0 {
		t.Fatalf("a refused upload stayed: %v", entries)
	}
}

// fileGate counts message posts and runs after once a file upload
// completes.
type fileGate struct {
	base     http.RoundTripper
	after    func()
	messages int
}

func (h *fileGate) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "POST" && r.URL.Path == "/v1/messages" {
		h.messages++
	}
	resp, err := h.base.RoundTrip(r)
	if err == nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/complete") {
		h.after()
	}
	return resp, err
}

// The hand-over is decided again after the files are uploaded, just before
// the message goes: the recipient's person frozen during the upload keeps
// it queued (its files uploaded, its spool kept), never posted.
func TestDMFilesFreezeDuringUpload(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	path, _ := writeFile(t, t.TempDir(), "report.txt", 1000)
	h := &fileGate{base: w.alice.hub.http.Transport}
	h.after = func() {
		person, raw := forkedStep(t, w.bob, "changed claim")
		if _, err := w.alice.store.pinChain(person, [][]byte{raw}, w.alice.Self(), false); !errors.Is(err, errPersonConflict) {
			t.Errorf("freeze: %v", err)
		}
	}
	w.alice.hub.http.Transport = h
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "file", Files: []OutgoingFile{{Path: path}}})
	if err != nil || h.messages != 0 || sent.State != stateQueued {
		t.Fatalf("after a freeze during the upload: %d posts, %+v %v", h.messages, sent, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 1 {
		t.Fatalf("spool: %v", entries)
	}
	if err := w.alice.FlushOutbox(tctx(t)); err != nil || h.messages != 0 {
		t.Fatalf("a flush posted it: %d %v", h.messages, err)
	}
}

// A direct (v1) message takes named files too (a page's staged uploads),
// shown under their chosen names, within the same limit; the staging left
// by an earlier run is cleaned, and only StageUpload's files.
func TestV1NamedFilesAndStagingCleanup(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	staged, cleanup, err := w.alice.StageUpload("budget.xlsx", strings.NewReader("numbers"))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := writeFile(t, t.TempDir(), "notes.txt", 100)
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "files", Files: []string{plain},
		Named: []OutgoingFile{{Name: "budget.xlsx", Path: staged}}})
	cleanup()
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ?`, sent.ID) == 1 })
	m := inboxRow(t, w.bob, sent.ID)
	if len(m.Attachments) != 2 || m.Attachments[0].Name != "notes.txt" || m.Attachments[1].Name != "budget.xlsx" {
		t.Fatalf("received: %+v", m.Attachments)
	}
	r, _, err := w.bob.OpenAttachment(tctx(t), sent.ID, 1)
	if err != nil || string(readAll(t, r)) != "numbers" {
		t.Fatalf("open: %v", err)
	}
	nine := make([]OutgoingFile, envelope.MaxAttachments)
	for i := range nine {
		nine[i] = OutgoingFile{Path: plain}
	}
	if _, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Files: []string{plain}, Named: nine}); err == nil {
		t.Fatal("more than the limit, counted together, was sent")
	}

	left, _, _ := w.alice.StageUpload("left over", strings.NewReader("x"))
	other := filepath.Join(w.alice.home, "staging", "keep-me")
	os.WriteFile(other, nil, 0o600)
	if err := w.alice.CleanStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Fatal("a staged upload stayed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("CleanStaging removed a file that is not a staged upload")
	}
}
