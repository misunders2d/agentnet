package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestAppUpdateResultRequiresFreshProof(t *testing.T) {
	for _, tc := range []struct{ name, running, cli, want string }{
		{"matching", "v0.8.3", "installed", "complete"},
		{"old app", "v0.8.2", "installed", "partial"},
		{"old CLI", "v0.8.3", "custom", "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := writeAppUpdateResult(home, "v0.8.3", "pending", ""); err != nil {
				t.Fatal(err)
			}
			text, err := appUpdateResultText(home)
			if err != nil || !strings.Contains(text, "pending verification") || strings.Contains(text, "Updated app") {
				t.Fatalf("pending: %q %v", text, err)
			}
			if err := reconcileAppUpdateResult(home, tc.running, appCommandStatus{State: tc.cli, Problem: "protected CLI"}); err != nil {
				t.Fatal(err)
			}
			r, err := readAppUpdateResult(home)
			if err != nil || r.State != tc.want {
				t.Fatalf("result: %+v %v", r, err)
			}
			text, err = appUpdateResultText(home)
			if err != nil || strings.Contains(text, "{\"") {
				t.Fatalf("not human text: %q %v", text, err)
			}
		})
	}
}

func TestAppUpdateResultLegacyAndFailure(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, appUpdateResultFile)
	if err := reconcileAppUpdateResult(home, "v0.8.3", appCommandStatus{State: "installed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created absent record: %v", err)
	}
	if err := secfile.Write(path, []byte("Updated to v0.8.3")); err != nil {
		t.Fatal(err)
	}
	text, err := appUpdateResultText(home)
	if err != nil || !strings.Contains(text, "pending verification") {
		t.Fatalf("legacy trusted: %q %v", text, err)
	}
	if err := reconcileAppUpdateResult(home, "v0.8.3", appCommandStatus{State: "missing"}); err != nil {
		t.Fatal(err)
	}
	r, _ := readAppUpdateResult(home)
	if r.State != "partial" {
		t.Fatalf("legacy missing CLI: %+v", r)
	}
	if err := reconcileAppUpdateResult(home, "v0.8.3", appCommandStatus{State: "installed"}); err != nil {
		t.Fatal(err)
	}
	r, _ = readAppUpdateResult(home)
	if r.State != "complete" {
		t.Fatalf("repair: %+v", r)
	}
	if err := reconcileAppUpdateResult(home, "v0.8.2", appCommandStatus{State: "installed"}); err != nil {
		t.Fatal(err)
	}
	r, _ = readAppUpdateResult(home)
	if r.State != "partial" {
		t.Fatalf("cached completion trusted: %+v", r)
	}
	for _, legacy := range []bool{false, true} {
		if legacy {
			err = secfile.Write(path, []byte("Launch failed: permission denied"))
		} else {
			err = writeAppUpdateResult(home, "v0.8.3", "failed", "Launch failed: permission denied")
		}
		if err != nil {
			t.Fatal(err)
		}
		before, _ := secfile.Read(path)
		if err := reconcileAppUpdateResult(home, "v0.8.3", appCommandStatus{State: "installed"}); err != nil {
			t.Fatal(err)
		}
		after, _ := secfile.Read(path)
		if string(before) != string(after) {
			t.Fatal("failed launch overwritten")
		}
		text, err = appUpdateResultText(home)
		if err != nil || !strings.Contains(text, "permission denied") {
			t.Fatalf("failure lost: %q %v", text, err)
		}
	}
	if err := secfile.Write(path, []byte(`{"state":"unknown"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := appUpdateResultText(home); err == nil || strings.Contains(err.Error(), "{\"") {
		t.Fatalf("malformed result: %v", err)
	}
}

func TestAppUpdateRestartFailureRecoversOnlyFromFreshProof(t *testing.T) {
	for _, tc := range []struct{ name, requested, problem, running, cli, want string }{
		{"verified restart", "v0.8.5", "App could not restart: exit status 127", "v0.8.5", "installed", "complete"},
		{"old app", "v0.8.5", "App could not restart: exit status 127", "v0.8.4", "installed", "failed"},
		{"custom CLI", "v0.8.5", "App could not restart: exit status 127", "v0.8.5", "custom", "failed"},
		{"unverified CLI", "v0.8.5", "App could not restart: exit status 127", "v0.8.5", "error", "failed"},
		{"install failure", "v0.8.5", "Your previous app is still available: install failed", "v0.8.5", "installed", "failed"},
		{"shutdown failure", "v0.8.5", "App shutdown handoff failed: closed pipe", "v0.8.5", "installed", "failed"},
		{"legacy failure", "", "App could not restart: exit status 127", "v0.8.5", "installed", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := writeAppUpdateResult(home, tc.requested, "failed", tc.problem); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(home, appUpdateResultFile))
			if err := reconcileAppUpdateResult(home, tc.running, appCommandStatus{State: tc.cli}); err != nil {
				t.Fatal(err)
			}
			result, err := readAppUpdateResult(home)
			if err != nil || result.State != tc.want {
				t.Fatalf("recovery = %+v, %v; want %s", result, err, tc.want)
			}
			if tc.want == "failed" {
				after, _ := os.ReadFile(filepath.Join(home, appUpdateResultFile))
				if string(before) != string(after) {
					t.Fatal("unverified or unrelated failure overwritten")
				}
			}
		})
	}
}
