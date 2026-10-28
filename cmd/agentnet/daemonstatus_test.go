package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func TestDaemonStatusStoppedAndForegroundHelp(t *testing.T) {
	a, home := diagnosticAgent(t)
	var out bytes.Buffer
	if err := runDaemonStatus(context.Background(), home, []string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var v struct {
		State  string                  `json:"state"`
		Daemon *client.DaemonReadiness `json:"daemon"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.State != "stopped" || v.Daemon != nil {
		t.Fatalf("stopped daemon status: %s %v", out.String(), err)
	}
	if _, err := a.ReadDaemonReadiness(context.Background()); err != client.ErrDaemonNotRunning {
		t.Fatalf("read-only status started something: %v", err)
	}
	for _, word := range []string{"daemon status", "foreground", "existing installed service"} {
		if !strings.Contains(topics["daemon"], word) {
			t.Fatalf("daemon help omits %q", word)
		}
	}
}

func TestDaemonStatusUsesRunningProcessAndDoesNotChangeCallerPath(t *testing.T) {
	a, home := diagnosticAgent(t)
	ctx, cancel := context.WithCancel(context.Background())
	readinessCtx, stopReadiness := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopReadiness()
	owned := make(chan struct{})
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = a.Run(ctx, client.RunOptions{HumanOnly: true, Owned: func() (func(), error) {
			close(owned)
			return func() {}, nil
		}})
		close(done)
	}()
	defer func() {
		cancel()
		<-done
		if runErr != nil {
			t.Error(runErr)
		}
	}()
	// Status probes take the daemon lock briefly. Establish this fixture's
	// running-owner precondition before probing, so the probe cannot win the
	// startup lock and make Run refuse to start. Both phases share ten seconds.
	select {
	case <-owned:
	case <-done:
		t.Fatalf("fixture daemon exited before ownership: %v", runErr)
	case <-readinessCtx.Done():
		t.Fatal("fixture daemon ownership did not start")
	}
	for {
		select {
		case <-done:
			t.Fatalf("fixture daemon exited before readiness: %v", runErr)
		default:
		}
		_, err := a.ReadDaemonReadiness(readinessCtx)
		if err == nil {
			break
		}
		if readinessCtx.Err() != nil {
			t.Fatalf("daemon readiness did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := os.Getenv("PATH")
	out, err := diagnosticOutput(t, func() error { return run([]string{"--home", home, "daemon", "status", "--json"}) })
	var v struct {
		State  string                  `json:"state"`
		Daemon *client.DaemonReadiness `json:"daemon"`
	}
	if json.Unmarshal([]byte(out), &v) != nil || err != nil || v.Daemon == nil || !v.Daemon.HumanOnly || v.Daemon.PID != os.Getpid() || v.State == "stopped" {
		t.Fatalf("actual process missing from status: %s %v", out, err)
	}
	if os.Getenv("PATH") != before {
		t.Fatal("status changed the caller launch environment")
	}
	var unused bytes.Buffer
	if err := runDaemonStatus(context.Background(), home, []string{"--agent", "missing", "--json"}, &unused); err == nil {
		t.Fatalf("unknown exact named agent was accepted: %s", unused.String())
	}
}

func TestDaemonStatusDoesNotOpenOrMigrateOlderHome(t *testing.T) {
	home := t.TempDir()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Save(filepath.Join(home, "identity.json")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "agent.db")
	db, err := sqlitedb.Open(path, []string{`CREATE TABLE config(k TEXT PRIMARY KEY, v TEXT); INSERT INTO config VALUES('responder', 'retained-old-setting');`})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	out, err := diagnosticOutput(t, func() error { return run([]string{"--home", home, "daemon", "status", "--json"}) })
	if err != nil || !strings.Contains(out, `"state":"stopped"`) {
		t.Fatalf("older home status: %s %v", out, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatalf("status changed the older database: %v", err)
	}
	if afterEntries, err := os.ReadDir(home); err != nil || len(afterEntries) != len(entries) {
		t.Fatalf("status created files in a stopped home: %v %v", afterEntries, err)
	}
}
