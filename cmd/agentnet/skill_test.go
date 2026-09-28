package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// frontMatter returns the name and description in SKILL.md front matter.
// Lines may end in LF or CRLF: a Windows checkout converts the file, and
// the export is that file byte for byte.
func frontMatter(b []byte) (name, description string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", "", false
	}
	for _, l := range lines[1:] {
		if l == "---" {
			return name, description, name != "" && description != ""
		}
		if v, found := strings.CutPrefix(l, "name: "); found {
			name = v
		}
		if v, found := strings.CutPrefix(l, "description: "); found {
			description = v
		}
	}
	return "", "", false
}

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
	lf := bytes.ReplaceAll(canonical, []byte("\r\n"), []byte("\n"))
	crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
	for what, b := range map[string][]byte{"export": out.Bytes(), "LF": lf, "CRLF": crlf} {
		if name, desc, ok := frontMatter(b); !ok || name != "agentnet-ops" || strings.TrimSpace(desc) == "" {
			t.Fatalf("%s: no SKILL.md front matter naming agentnet-ops (%q)", what, name)
		}
	}
	if _, _, ok := frontMatter(bytes.TrimPrefix(lf, []byte("---\n"))); ok {
		t.Fatal("front matter found without its opening line")
	}
	if err := runSkill(&out, []string{"extra"}); err == nil {
		t.Fatal("arguments accepted")
	}
}
