package ui

import (
	"os"
	"os/exec"
	"regexp"
	"sort"
	"testing"
)

func runNodeCheck(t *testing.T, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, script).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestComicAssistantSetupModel runs Comic's setup rules (which tools start
// ticked, the agent each tool gets, apply before any agent change, reuse on
// a rerun, stop on a changed agent, "ready" only when the server says so,
// no agent list where nothing can be set up, folder entries, and a way out
// of a folder that can't be read) in node.
func TestComicAssistantSetupModel(t *testing.T) {
	runNodeCheck(t, "testdata/assistant_setup_model_check.mjs")
}

// TestSkinAssistantSetupFolders drives Classic's and Zoom's setup: the
// working folder is browsed through /api/folders, never typed.
func TestSkinAssistantSetupFolders(t *testing.T) {
	runNodeCheck(t, "testdata/assistant_setup_skin_check.mjs")
}

// TestComicAssistantSetupStatesWorded pins every tool state the native
// setup reports (cmd/agentnet/assistantsetup.go) to words in Comic, and
// keeps the folders on these screens browsed, not typed (owner, MEL-534).
func TestComicAssistantSetupStatesWorded(t *testing.T) {
	goSrc, err := os.ReadFile("../../cmd/agentnet/assistantsetup.go")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]bool{}
	for _, m := range regexp.MustCompile(`State(?:\s*=|:)\s*"([a-z_]+)"`).FindAllSubmatch(goSrc, -1) {
		states[string(m[1])] = true
	}
	if len(states) < 5 {
		t.Fatalf("found only %d states in assistantsetup.go: the pattern no longer matches", len(states))
	}
	model, err := os.ReadFile("web/src/features/AssistantSetup.model.ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range []string{"STATE_WORDS", "STATE_SENTENCE"} {
		m := regexp.MustCompile(`(?s)export const ` + block + `: Record<string, string> = \{(.*?)\n\};`).FindSubmatch(model)
		if m == nil {
			t.Fatalf("no %s block in AssistantSetup.model.ts", block)
		}
		var missing []string
		for s := range states {
			if !regexp.MustCompile(`(?m)^\s*` + s + `:`).Match(m[1]) {
				missing = append(missing, s)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s has no words for setup states %v", block, missing)
		}
	}
	typed := regexp.MustCompile(`<input[^>]*value=\{[^}]*\bdir\b`)
	for _, f := range []string{"web/src/features/AssistantSetup.tsx", "web/src/features/AssistantSetup.folders.tsx", "web/src/features/Settings.assistant.tsx"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if typed.Match(src) {
			t.Errorf("%s has a typed folder field; folders are chosen with FolderField", f)
		}
	}
}
