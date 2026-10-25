package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The parts an overlay skin may style (web/src/skin/README.md, Parts) are a
// contract: every documented part is on an element in Comic's source, and
// every data-part in the source is documented.
func TestSkinKitPartsDocumented(t *testing.T) {
	readme, err := os.ReadFile("web/src/skin/README.md")
	if err != nil {
		t.Fatal(err)
	}
	section := string(readme)
	start := strings.Index(section, "## Parts")
	end := strings.Index(section, "## Build an overlay")
	if start < 0 || end < start {
		t.Fatal("README has no Parts section")
	}
	var documented []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z-]+)` \\|").FindAllStringSubmatch(section[start:end], -1) {
		documented = append(documented, m[1])
	}
	var used []string
	err = filepath.WalkDir("web/src", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tsx") {
			return err
		}
		src, err := os.ReadFile(path)
		for _, m := range regexp.MustCompile(`data-part=(?:"([a-z-]+)"|\{\s*\w+\s*\?\s*undefined\s*:\s*"([a-z-]+)"\s*\})`).FindAllSubmatch(src, -1) {
			part := m[1]
			if len(part) == 0 {
				part = m[2]
			}
			used = append(used, string(part))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(documented)
	slices.Sort(used)
	used = slices.Compact(used)
	if len(documented) < 15 || !slices.Equal(documented, used) {
		t.Fatalf("documented parts %v\nparts in Comic's source %v", documented, used)
	}
}
