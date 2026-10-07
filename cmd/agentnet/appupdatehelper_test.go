package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/secfile"
)

func helperUpdateFixture(t *testing.T, asset []byte) (string, string) {
	t.Helper()
	home := t.TempDir()
	dir, err := os.MkdirTemp(home, "app-update-")
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(home, "AgentNet.AppImage")
	if runtime.GOOS == "windows" {
		app += ".exe"
	}
	if err = os.WriteFile(app, []byte("old app"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "download")
	if err = os.WriteFile(path, asset, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(asset)
	b, err := json.Marshal(appUpdatePlan{App: app, Asset: path, Kind: "appimage", Sum: hex.EncodeToString(sum[:]), Version: "v0.8.3"})
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "plan.json")
	if err = secfile.Write(plan, b); err != nil {
		t.Fatal(err)
	}
	return home, plan
}

func TestAppUpdateHelperFailedLaunchNeverReportsComplete(t *testing.T) {
	home, plan := helperUpdateFixture(t, []byte("not an executable package"))
	if err := runAppUpdateHelper(home, []string{plan}, strings.NewReader("")); err == nil {
		t.Fatal("invalid app unexpectedly started")
	}
	result, err := readAppUpdateResult(home)
	if err != nil || result.State != "failed" || !strings.Contains(result.Problem, "restart") {
		t.Fatalf("launch failure result = %+v, %v", result, err)
	}
}

func TestAppUpdateHelperLaunchWaitsForAppVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture; failure case runs on every OS")
	}
	home, plan := helperUpdateFixture(t, []byte("#!/bin/sh\nprintf '%s' \"$AGENTNET_HOME\" > \"$AGENTNET_HOME/launched\"\n"))
	t.Setenv("AGENTNET_HOME", filepath.Join(t.TempDir(), "wrong-home"))
	if err := runAppUpdateHelper(home, []string{plan}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(home, "launched"))
		if err == nil && string(b) == home {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted app did not receive exact home: %q, %v", b, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := readAppUpdateResult(home)
	if err != nil || result.State != "pending" {
		t.Fatalf("launch was mistaken for verified completion: %+v, %v", result, err)
	}
}
