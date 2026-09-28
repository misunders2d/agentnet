package itest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCLISkillExport: the real binary prints the embedded skill exactly,
// with no agent home (none is created) and no Hub to contact.
func TestCLISkillExport(t *testing.T) {
	c := buildCLI(t)
	home := filepath.Join(c.dir, "no-home")
	want, err := os.ReadFile(filepath.Join("..", "cmd", "agentnet", "skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(c.bin, "--home", home, "skill")
	cmd.Dir = c.dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || !bytes.Equal(out, want) || stderr.Len() != 0 {
		t.Fatalf("skill export (%v, stderr %q):\n%s", err, stderr.String(), out)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("skill created the home (%v)", err)
	}
	if _, err := c.try("--home", home, "skill", "extra"); err == nil {
		t.Fatal("skill accepted an argument")
	}
}
