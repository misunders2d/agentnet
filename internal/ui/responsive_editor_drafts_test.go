package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResponsiveEditorDraftsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT")
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	conv, err := f.NewDM("vitalii/laptop")
	if err != nil {
		t.Fatal(err)
	}
	overview, _ := f.Overview()
	view, _ := f.DM(conv)
	seed, err := json.Marshal(map[string]any{"overview": overview, "view": view})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(file, seed, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(browserCheck(t, "testdata/responsive_editor_drafts_rendered.cjs", file))
}
