package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func claudeTestNode(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Claude channel Linux qualification")
	}
	bin := t.TempDir()
	if e := os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\nexit 99\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin)
}

func TestClaudeChannelAssetsShowInstallRemove(t *testing.T) {
	claudeTestNode(t)
	home := filepath.Join(t.TempDir(), "agent-home")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "native-profile-not-copied"))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "native-value-not-copied")
	config, e := applyClaudeChannelAssets(home, "show")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(home); !os.IsNotExist(e) {
		t.Fatal("show mutated home")
	}
	var parsed struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if e = json.Unmarshal(config, &parsed); e != nil {
		t.Fatal(e)
	}
	server := parsed.Servers["agentnet"]
	if len(parsed.Servers) != 1 || len(server.Args) != 1 || len(server.Env) != 2 || server.Env["AGENTNET_HOME"] != home || server.Env["AGENTNET_BIN"] == "" {
		t.Fatal("not one standard local SDK config fragment")
	}
	if strings.Contains(string(config), "CLAUDE_CONFIG_DIR") || strings.Contains(string(config), "SESSION_ID") || strings.Contains(string(config), "--tools") {
		t.Fatal("native profile/permissions were overridden")
	}
	installed, e := applyClaudeChannelAssets(home, "install")
	if e != nil || !bytes.Equal(config, installed) {
		t.Fatalf("install changed fragment %v", e)
	}
	if _, e = applyClaudeChannelAssets(home, "install"); e != nil {
		t.Fatal(e)
	}
	files, e := claudeChannelFiles()
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Dir(server.Args[0])
	for name, expected := range files {
		actual, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || !bytes.Equal(expected, actual) {
			t.Fatalf("asset %s %v", name, e)
		}
	}
	for _, name := range []string{"agentnet.bundle.mjs", "LICENSES.txt"} {
		if !strings.Contains(string(files["SHA256SUMS"]), fmt.Sprintf("%x  %s", sha256.Sum256(files[name]), name)) {
			t.Fatal("installed asset checksum mismatch")
		}
	}
	foreign := filepath.Join(dir, "keep-user-file")
	os.WriteFile(foreign, []byte("not AgentNet"), 0600)
	t.Setenv("PATH", "") // removing assets must not require Node
	if _, e = applyClaudeChannelAssets(home, "remove"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(foreign); e != nil {
		t.Fatal("remove changed foreign file")
	}
	for name := range files {
		if _, e = os.Stat(filepath.Join(dir, name)); !os.IsNotExist(e) {
			t.Fatal("owned asset retained")
		}
	}
}

func TestClaudeChannelAssetsRefuseClobberAndMissingNode(t *testing.T) {
	claudeTestNode(t)
	for _, symlink := range []bool{false, true} {
		home := t.TempDir()
		dir := filepath.Join(home, "hooks", "claude-channel")
		os.MkdirAll(dir, 0700)
		file := filepath.Join(dir, "LICENSES.txt")
		if symlink {
			target := filepath.Join(t.TempDir(), "foreign")
			os.WriteFile(target, []byte("private user data"), 0600)
			if e := os.Symlink(target, file); e != nil {
				t.Fatal(e)
			}
		} else {
			os.WriteFile(file, []byte("private user data"), 0600)
		}
		for _, action := range []string{"install", "remove"} {
			if _, e := applyClaudeChannelAssets(home, action); e == nil {
				t.Fatalf("unowned file/symlink %s allowed", action)
			}
		}
		got, _ := os.ReadFile(file)
		if string(got) != "private user data" {
			t.Fatal("unowned file changed")
		}
		if _, e := os.Stat(filepath.Join(dir, "agentnet.bundle.mjs")); !os.IsNotExist(e) {
			t.Fatal("partial assets written before ownership refusal")
		}
	}
	t.Setenv("PATH", "")
	home := filepath.Join(t.TempDir(), "no-node-no-writes")
	if _, e := applyClaudeChannelAssets(home, "install"); e == nil {
		t.Fatal("missing Node triggered automatic install or accepted")
	}
	if _, e := os.Stat(home); !os.IsNotExist(e) {
		t.Fatal("missing Node still mutated home")
	}
}

func TestClaudeChannelEmbeddedSDKStandalone(t *testing.T) {
	modules := os.Getenv("AGENTNET_CLAUDE_SDK_TEST_MODULES")
	if modules == "" {
		t.Skip("opt-in: exact qualified SDK cache needed for fixture CLIENT, not bundled server")
	}
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	home := t.TempDir()
	if _, e = applyClaudeChannelAssets(home, "install"); e != nil {
		t.Fatal(e)
	}
	fixture := filepath.Join("..", "..", "internal", "client", "testdata", "claude_channel_sdk.mjs")
	cmd := exec.Command(node, fixture, filepath.Join(home, "hooks", "claude-channel", "agentnet.bundle.mjs"), modules, "standalone")
	output, e := cmd.CombinedOutput()
	if e != nil || !bytes.Contains(output, []byte(`"status":"PASS"`)) {
		t.Fatalf("installed bundle official SDK fixture: %v %s", e, output)
	}
}
