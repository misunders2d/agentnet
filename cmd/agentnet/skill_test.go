package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// The skill ships as embedded: the export is the canonical file byte for
// byte, starts with SKILL.md front matter naming agentnet-ops, and takes no
// arguments.
func TestSkillExport(t *testing.T) {
	var out bytes.Buffer
	if err := runSkill(&out, nil); err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile("skill/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), canonical) {
		t.Fatal("export differs from skill/SKILL.md")
	}
	if !strings.HasPrefix(string(canonical), "---\nname: agentnet-ops\ndescription: ") {
		t.Fatal("no SKILL.md front matter naming agentnet-ops")
	}
	if err := runSkill(&out, []string{"extra"}); err == nil {
		t.Fatal("arguments accepted")
	}
}
