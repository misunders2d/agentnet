package ui

import (
	"os"
	"testing"
)

func TestComicPanelLayout(t *testing.T) {
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("installed pinned frontend compiler required")
	}
	runNodeCheck(t, "testdata/panel_layout_check.cjs")
}
