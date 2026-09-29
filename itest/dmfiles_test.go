package itest

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCLIDMFiles: through the real binaries, a DM turn with a file and a
// file-only turn reach the other person, are listed with their (quoted)
// names, and are saved by agentnet download with the exact content.
func TestCLIDMFiles(t *testing.T) {
	c := buildCLI(t)
	c.setup(t, "hub")
	c.start("bob.log", "--home", "bob", "daemon")
	c.start("alice.log", "--home", "alice", "daemon")
	c.run("--home", "alice", "person", "create", "Alice")
	c.run("--home", "bob", "person", "create", "Bob")
	var conv string
	waitFor(t, "a DM with bob", func() bool {
		out, err := c.try("--home", "alice", "dm", "new", "bob/desk")
		conv = out
		return err == nil
	})
	img := make([]byte, 90000)
	rand.Read(img)
	os.WriteFile(filepath.Join(c.dir, "chart.png"), img, 0o600)
	os.WriteFile(filepath.Join(c.dir, "notes.txt"), []byte("minutes"), 0o600)
	withText := strings.Fields(c.run("--home", "alice", "dm", "send", "--file", "chart.png", conv, "the chart"))[0]
	fileOnly := strings.Fields(c.run("--home", "alice", "dm", "send", "--file", "notes.txt", "--file", "chart.png", conv))[0]
	if out, err := c.try("--home", "alice", "dm", "send", conv); err == nil {
		t.Fatalf("an empty send: %s", out)
	}
	waitFor(t, "bob to list both", func() bool {
		show := c.run("--home", "bob", "dm", "show", conv)
		return strings.Contains(show, withText) && strings.Contains(show, fileOnly)
	})
	show := c.run("--home", "bob", "dm", "show", conv)
	if !regexp.MustCompile(`the chart\n  \[file\] "chart.png" 90000 bytes`).MatchString(show) ||
		!regexp.MustCompile(`\[file\] "notes.txt" 7 bytes\n  \[file\] "chart.png" 90000 bytes`).MatchString(show) {
		t.Fatalf("dm show:\n%s", show)
	}
	os.MkdirAll(filepath.Join(c.dir, "got"), 0o700)
	saved := strings.Fields(c.run("--home", "bob", "download", "--dir", "got", fileOnly))
	if len(saved) != 2 {
		t.Fatalf("download: %v", saved)
	}
	if data, _ := os.ReadFile(filepath.Join(c.dir, "got", "chart.png")); !bytes.Equal(data, img) {
		t.Fatal("the saved chart differs")
	}
	if data, _ := os.ReadFile(filepath.Join(c.dir, "got", "notes.txt")); string(data) != "minutes" {
		t.Fatal("the saved notes differ")
	}
	if !strings.Contains(c.run("--home", "bob", "dm", "show", conv), `"notes.txt" 7 bytes saved `) {
		t.Fatal("dm show does not say where it was saved")
	}
}
