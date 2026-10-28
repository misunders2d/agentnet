package ui

import (
	"strings"
	"testing"
)

func TestFileDropRendered(t *testing.T) {
	out := browserCheck(t, "testdata/file_drop_rendered.cjs")
	if strings.Count(out, "FILE DROP PASS comic ") != 2 {
		t.Fatalf("missing rendered file-drop cases:\n%s", out)
	}
	t.Log(out)
}
