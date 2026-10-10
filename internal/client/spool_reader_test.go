package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestSpoolReaderEncryptsAndSyncs(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{home: t.TempDir()}
	data := []byte("archive plaintext must never be written to disk")
	oldLimit, oldSync := MaxFileSize, syncDir
	t.Cleanup(func() { MaxFileSize, syncDir = oldLimit, oldSync })
	MaxFileSize = int64(len(data)) // The exact limit must still succeed.
	synced := false
	syncDir = func(dir string) error {
		if dir != filepath.Join(a.home, "spool") {
			t.Fatalf("synced unexpected directory %q", dir)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".age") {
			t.Fatalf("sync happened before final ciphertext rename: %v, %v", entries, err)
		}
		synced = true
		return oldSync(dir)
	}
	att, err := a.spoolReader("history.age.json", bytes.NewReader(data), identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("ciphertext directory was not synced")
	}
	ciphertext, err := os.ReadFile(a.spoolPath(att.Blob.ID))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, data) {
		t.Fatal("spool contains plaintext")
	}
	plainHash, cipherHash := sha256.Sum256(data), sha256.Sum256(ciphertext)
	if att.Name != "history.age.json" || att.Size != int64(len(data)) || att.SHA256 != hex.EncodeToString(plainHash[:]) ||
		att.Blob.Size != int64(len(ciphertext)) || att.Blob.SHA256 != hex.EncodeToString(cipherHash[:]) {
		t.Fatalf("incorrect attachment manifest: %+v", att)
	}
	decrypted, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(decrypted)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("decrypted data differs: %q, %v", got, err)
	}
	assertOnlyFiles(t, filepath.Join(a.home, "spool"), att.Blob.ID+".age")
}

type spoolReaderFailure struct{ err error }

func (r spoolReaderFailure) Read([]byte) (int, error) { return 0, r.err }

func TestSpoolReaderFailureCleansCiphertext(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	oldLimit := MaxFileSize
	t.Cleanup(func() { MaxFileSize = oldLimit })
	MaxFileSize = 1024
	injected := errors.New("reader failed")
	for _, mode := range []string{"read failure", "over limit"} {
		t.Run(mode, func(t *testing.T) {
			a := &Agent{home: t.TempDir()}
			remaining := bytes.NewReader(bytes.Repeat([]byte("x"), 4096))
			var src io.Reader = remaining
			if mode == "read failure" {
				src = io.MultiReader(bytes.NewReader([]byte("partial plaintext")), spoolReaderFailure{injected})
			}
			att, err := a.spoolReader("history.age.json", src, identity.Recipient())
			if err == nil || att.Blob.ID != "" {
				t.Fatalf("failed read produced attachment: %+v, %v", att, err)
			}
			if mode == "read failure" && !errors.Is(err, injected) {
				t.Fatalf("lost reader error: %v", err)
			}
			if mode == "over limit" && (remaining.Len() != 4096-int(MaxFileSize)-1 || !strings.Contains(err.Error(), "byte limit")) {
				t.Fatalf("size check did not bound reads: %d bytes left, %v", remaining.Len(), err)
			}
			assertOnlyFiles(t, filepath.Join(a.home, "spool"))
		})
	}
}

func TestSpoolNamedStillRejectsInvalidFiles(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{home: t.TempDir()}
	oldLimit := MaxFileSize
	t.Cleanup(func() { MaxFileSize = oldLimit })
	MaxFileSize = 1
	file := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(file, []byte("xx"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		file OutgoingFile
		want string
	}{
		{OutgoingFile{Path: file}, "larger than"},
		{OutgoingFile{Path: t.TempDir()}, "not a regular file"},
		{OutgoingFile{Name: "bad\nname", Path: file}, "file name"},
	} {
		if _, err := a.spoolNamed(test.file, identity.Recipient()); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%+v: got %v, want %q", test.file, err, test.want)
		}
	}
	if _, err := os.Stat(filepath.Join(a.home, "spool")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid input created a spool: %v", err)
	}
	if _, err := a.spoolReader("bad\nname", bytes.NewReader(nil), identity.Recipient()); err == nil {
		t.Fatal("reader path accepted an invalid name")
	}
}
