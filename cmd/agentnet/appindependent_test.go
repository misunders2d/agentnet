package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestIndependentQualificationRequiresExactSameVersionProof(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture; native protocol regression is cross-platform")
	}
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "agentnet")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\n' '"+versionLine("v0.8.3")+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSum(exe)
	if err != nil {
		t.Fatal(err)
	}
	target := appCommandTarget{Path: exe, Sum: hex.EncodeToString(sum)}
	version := "v0.8.2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("agentnet_ui")
		if err != nil || cookie.Value != "private" || r.URL.Path != "/api/overview" {
			t.Error("incorrect daemon identity request")
		}
		json.NewEncoder(w).Encode(map[string]string{"version": version})
	}))
	defer server.Close()
	if err := secfile.Write(filepath.Join(home, uiURLFile), []byte(server.URL+"/?t=private")); err != nil {
		t.Fatal(err)
	}
	qualified, err := qualifyIndependentTarget(context.Background(), home, target)
	if err != nil || qualified != nil {
		t.Fatalf("old-version candidate: %+v %v", qualified, err)
	}
	if _, err := os.Stat(filepath.Join(home, "update-request.json")); !os.IsNotExist(err) {
		t.Fatal("mismatched version emitted a switch")
	}
	version = "v0.8.3"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		for {
			b, err := secfile.Read(filepath.Join(home, "update-request.json"))
			if err == nil {
				var request client.UpdateRequest
				if err = json.Unmarshal(b, &request); err != nil {
					done <- err
					return
				}
				if request.Exe != exe || request.From != version || request.To != version {
					done <- os.ErrInvalid
					return
				}
				activation, _ := json.Marshal(client.UpdateActivation{ID: request.ID, To: version, Result: client.ActivationRunning, Running: version, PID: 42})
				done <- secfile.Write(filepath.Join(home, "update-activation.json"), activation)
				return
			}
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	qualified, err = qualifyIndependentTarget(ctx, home, target)
	if writeErr := <-done; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil || qualified == nil || qualified.Exe != exe || qualified.From != version || qualified.PID != 42 {
		t.Fatalf("qualification: %+v %v", qualified, err)
	}
	before, _ := os.ReadFile(filepath.Join(home, "update-request.json"))
	if err := noDaemonUpdateRequest(home); err == nil {
		t.Fatal("existing request ignored")
	}
	after, _ := os.ReadFile(filepath.Join(home, "update-request.json"))
	if string(before) != string(after) {
		t.Fatal("existing request overwritten")
	}
}

func TestIndependentAppRestartFailureResumesVerifiedSwitchOnce(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	f.runner.command = f.runner.installCommand(false)
	if status := f.runner.currentCommandStatus(); status.State != "installed" {
		t.Fatalf("fixture commands not verified: %+v", status)
	}
	record := appUpdateResult{Version: protocol.Version, State: "failed", Problem: "App could not restart: exit status 127", Independent: &appIndependentDaemon{Exe: f.terminal, From: "v1.2.2", PID: 42}}
	if err := saveAppUpdateResult(f.home, record); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.beginIndependentSwitch(); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(f.home, "update-request.json")
	before, err := secfile.Read(requestPath)
	if err != nil {
		t.Fatalf("verified restart recovery did not resume its approved switch: %v", err)
	}
	var request client.UpdateRequest
	if err := json.Unmarshal(before, &request); err != nil || request.Exe != f.terminal || request.From != "v1.2.2" || request.To != protocol.Version {
		t.Fatalf("recovered request = %+v, %v", request, err)
	}
	if err := f.runner.beginIndependentSwitch(); err != nil {
		t.Fatal(err)
	}
	after, _ := secfile.Read(requestPath)
	if string(before) != string(after) {
		t.Fatal("reopen reissued its acknowledged switch")
	}
	if err := reconcileAppUpdateResult(f.home, protocol.Version, f.runner.currentCommandStatus()); err != nil {
		t.Fatal(err)
	}
	result, _ := readAppUpdateResult(f.home)
	if result.State != "pending" || result.SwitchID != request.ID {
		t.Fatalf("busy daemon was reported complete: %+v", result)
	}
	activation := client.UpdateActivation{ID: request.ID, To: protocol.Version, Result: client.ActivationRunning, Running: "v1.2.2", PID: 42}
	data, _ := json.Marshal(activation)
	if err := secfile.Write(filepath.Join(f.home, "update-activation.json"), data); err != nil {
		t.Fatal(err)
	}
	if err := reconcileAppUpdateResult(f.home, protocol.Version, f.runner.currentCommandStatus()); err != nil {
		t.Fatal(err)
	}
	result, _ = readAppUpdateResult(f.home)
	if result.State != "partial" {
		t.Fatalf("old running daemon falsely completed: %+v", result)
	}
	activation.Running = protocol.Version
	data, _ = json.Marshal(activation)
	if err := secfile.Write(filepath.Join(f.home, "update-activation.json"), data); err != nil {
		t.Fatal(err)
	}
	if err := reconcileAppUpdateResult(f.home, protocol.Version, f.runner.currentCommandStatus()); err != nil {
		t.Fatal(err)
	}
	result, _ = readAppUpdateResult(f.home)
	if result.State != "complete" {
		t.Fatalf("matching daemon activation did not complete recovery: %+v", result)
	}
}
