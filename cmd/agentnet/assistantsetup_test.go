package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/ui"
)

func isolatedSetup(t *testing.T) *assistantSetupController {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native hook installation remains unsupported on Windows")
	}
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	for _, name := range []string{"PI_CODING_AGENT_DIR", "CODEX_HOME", "OMP_PROFILE", "PI_PROFILE", "PI_CONFIG_DIR"} {
		t.Setenv(name, "")
	}
	for _, name := range setupHarnesses {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := &assistantSetupController{home: t.TempDir()}
	c.install = func(h, file string) error { return runHooks(c.home, []string{"install", h, "--file", file}) }
	return c
}
func TestAssistantSetupSelectedInstallPreservesConfigAndRerun(t *testing.T) {
	c := isolatedSetup(t)
	file, _ := setupTarget("claude")
	os.MkdirAll(filepath.Dir(file), 0700)
	original := []byte(`{"model":"fixture-model","mcpServers":{"fixture":{"command":"kept"}},"permissions":{"allow":["Read"]},"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"keep-existing"}]}]}}`)
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := c.run(ctx, ui.AssistantSetupRequest{})
	if err != nil || len(first.Harnesses) != 4 {
		t.Fatalf("detection %v", err)
	}
	request := ui.AssistantSetupRequest{Action: "review", Harnesses: []string{"claude", "omp"}}
	review, err := c.run(ctx, request)
	if err != nil || review.ReviewID == "" {
		t.Fatal(err)
	}
	unchanged, _ := os.ReadFile(file)
	if !bytes.Equal(unchanged, original) {
		t.Fatal("review wrote configuration")
	}
	request.Action = "apply"
	request.ReviewID = review.ReviewID
	result, err := c.run(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Harnesses) != 4 {
		t.Fatal("lost catalog")
	}
	after, _ := os.ReadFile(file)
	for _, part := range []string{"fixture-model", "mcpServers", "keep-existing", "permissions"} {
		if !bytes.Contains(after, []byte(part)) {
			t.Fatalf("unrelated setting lost: %s", part)
		}
	}
	omp, _ := setupTarget("omp")
	if data, err := os.ReadFile(omp); err != nil || !bytes.Contains(data, []byte(`const HARNESS = "omp"`)) {
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"omp"`)) {
			t.Fatal("OMP extension not bound")
		}
	}
	codex, _ := setupTarget("codex")
	pi, _ := setupTarget("pi")
	for _, f := range []string{codex, pi} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatal("unselected tool modified")
		}
	}
	beforeInfo, _ := os.Stat(file)
	request.Action = "review"
	request.ReviewID = ""
	review, err = c.run(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Action = "apply"
	request.ReviewID = review.ReviewID
	result, err = c.run(ctx, request)
	if err != nil || !strings.Contains(result.Note, "No setup changes") {
		t.Fatalf("rerun %v %s", err, result.Note)
	}
	second, _ := os.ReadFile(file)
	afterInfo, _ := os.Stat(file)
	if !bytes.Equal(after, second) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("rerun rewrote configured file")
	}
	backups, _ := filepath.Glob(file + ".agentnet-backup-*")
	if len(backups) != 1 {
		t.Fatal("rerun duplicated backup")
	}
}
func TestAssistantSetupStaleReviewAndMissingExecutableFailClosed(t *testing.T) {
	c := isolatedSetup(t)
	ctx := context.Background()
	request := ui.AssistantSetupRequest{Action: "review", Harnesses: []string{"codex"}}
	review, err := c.run(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := setupTarget("codex")
	os.MkdirAll(filepath.Dir(file), 0700)
	changed := []byte(`{"model":"changed-after-review"}`)
	os.WriteFile(file, changed, 0600)
	request.Action = "apply"
	request.ReviewID = review.ReviewID
	if _, err = c.run(ctx, request); err == nil {
		t.Fatal("stale review applied")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(changed, after) {
		t.Fatal("stale config overwritten")
	}
	if err = os.Remove(filepath.Join(os.Getenv("PATH"), "codex")); err != nil {
		t.Fatal(err)
	}
	if _, err = c.run(ctx, request); err == nil {
		t.Fatal("removed executable accepted")
	}
	view, err := c.run(ctx, ui.AssistantSetupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Harnesses[0].Detected || view.Harnesses[0].State != "not_detected" {
		t.Fatal("removed tool shown ready")
	}
	after, _ = os.ReadFile(file)
	if !bytes.Equal(changed, after) {
		t.Fatal("uninstall erased config")
	}
}
func TestAssistantSetupRefusesUnownedExtensionsAndCollision(t *testing.T) {
	c := isolatedSetup(t)
	file, _ := setupTarget("pi")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, []byte("// user extension"), 0600)
	if _, err := c.run(context.Background(), ui.AssistantSetupRequest{Action: "review", Harnesses: []string{"pi"}}); err == nil {
		t.Fatal("unowned extension accepted")
	}
	os.Remove(file)
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	if _, err := c.run(context.Background(), ui.AssistantSetupRequest{Action: "review", Harnesses: []string{"pi", "omp"}}); err == nil {
		t.Fatal("shared extension destination accepted")
	}
}

func TestAssistantSetupJSONFormattingDoesNotLoseConfiguredHooks(t *testing.T) {
	c := isolatedSetup(t)
	for _, harness := range []string{"codex", "claude"} {
		t.Run(harness, func(t *testing.T) {
			file, _ := setupTarget(harness)
			os.MkdirAll(filepath.Dir(file), 0700)
			command, err := hookCommand(c.home, harness)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := mergeHooks(map[string]any{"unrelated": map[string]any{"preserve": true}}, harness, command)
			if err != nil {
				t.Fatal(err)
			}
			compact, _ := json.Marshal(cfg)
			if err = os.WriteFile(file, compact, 0600); err != nil {
				t.Fatal(err)
			}
			if row := c.inspect(harness, nil).row; !row.Configured || row.State != "needs_activation" {
				t.Fatalf("valid compact config lost setup: %+v", row)
			}
			data, _ := os.ReadFile(file)
			if !bytes.Equal(data, compact) {
				t.Fatal("inspection rewrote live configuration")
			}
			for _, mutation := range []string{"home", "executable", "missing", "duplicate", "matcher"} {
				var changed map[string]any
				json.Unmarshal(compact, &changed)
				hooks := changed["hooks"].(map[string]any)
				event := harnessHookEvents(harness)[0]
				groups := hooks[event].([]any)
				g := groups[0].(map[string]any)
				h := g["hooks"].([]any)[0].(map[string]any)
				switch mutation {
				case "home":
					h["command"] = strings.Replace(command, c.home, c.home+"-old", 1)
				case "executable":
					h["command"] = "'/previous/agentnet' --home '" + c.home + "' hook " + harness
				case "missing":
					delete(hooks, event)
				case "duplicate":
					hooks[event] = append(groups, g)
				case "matcher":
					g["matcher"] = "only-a-subset"
				}
				raw, _ := json.Marshal(changed)
				os.WriteFile(file, raw, 0600)
				if c.inspect(harness, nil).row.Configured {
					t.Fatalf("%s wrongly configured", mutation)
				}
			}
		})
	}
}
