package itest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLIRelease: an admin recommends a version with flags before the
// version; a running member daemon saves it; `version` keeps its stdout line
// and adds the recommendation on stderr; `version` for a missing home
// creates nothing; doctor reports it.
func TestCLIRelease(t *testing.T) {
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	c.start("bob.log", "--home", "bob", "daemon")
	out := c.run("--home", "alice", "admin", "release", "set", "--url", "https://example.test/update", "--note", "for people", "v42")
	if !strings.Contains(out, "recommended client version v42") {
		t.Fatalf("set: %s", out)
	}
	if out, err := c.try("--home", "bob", "admin", "release", "set", "--url", "https://example.test/", "v1"); err == nil {
		t.Fatalf("non-admin set: %s", out)
	}
	waitFor(t, "bob saves the recommendation", func() bool {
		cmd := exec.Command(c.bin, "--home", "bob", "version")
		cmd.Dir = c.dir
		stderr, _ := cmd.StderrPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Start()
		o, _ := readAll(stdout)
		e, _ := readAll(stderr)
		cmd.Wait()
		return strings.HasPrefix(o, "agentnet dev (protocol ") && strings.Count(o, "\n") == 1 &&
			strings.Contains(e, "your Hub recommends agentnet v42 (this is dev)") && strings.Contains(e, "https://example.test/update")
	})
	if out, _ := c.try("--home", "bob", "doctor"); !strings.Contains(out, "the Hub recommends v42; this is dev") {
		t.Fatalf("doctor: %s", out)
	}
	missing := filepath.Join(c.dir, "nobody")
	if out, err := c.try("--home", missing, "version"); err != nil || strings.Contains(out, "recommends") {
		t.Fatalf("version without a home: %v %s", err, out)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("version created a home")
	}
	if out := c.run("--home", "alice", "admin", "release", "clear"); !strings.Contains(out, "no client version recommended") {
		t.Fatalf("clear: %s", out)
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) (string, error) {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String(), nil
		}
	}
}
