package main

import (
	"context"
	"database/sql"
	"flag"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func originSelection(t *testing.T, args ...string) func() (string, error) {
	t.Helper()
	a, _ := diagnosticAgent(t)
	return func() (string, error) {
		fs := flag.NewFlagSet("ask", flag.ContinueOnError)
		f := receiverFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		r, err := f.selected(a)
		if r == nil {
			return "", err
		}
		return r.Kind, err
	}
}

// A CLI request names its origin session only through the harness's own
// session environment; a named session that cannot be the receiver refuses
// (never the inbox), an explicit receiver or a background job decides alone,
// and a plain command keeps the inbox.
func TestCLIOriginSelection(t *testing.T) {
	for _, k := range []string{"AGENTNET_REPLY_SESSION", "AGENTNET_REPLY_BINDING", "AGENTNET_BACKGROUND", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	plain := originSelection(t)
	if kind, err := plain(); kind != "" || err != nil {
		t.Fatalf("plain command: %q %v", kind, err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	if kind, err := plain(); kind != "" || err == nil || !strings.Contains(err.Error(), "Claude Code session synthetic-claude-session") || !strings.Contains(err.Error(), "--reply-receiver human") {
		t.Fatalf("unregistered Claude origin: %q %v", kind, err)
	}
	if kind, err := originSelection(t, "--reply-receiver", "human")(); kind != "human" || err != nil {
		t.Fatalf("explicit human override: %q %v", kind, err)
	}
	t.Setenv("AGENTNET_BACKGROUND", "1")
	if kind, err := plain(); kind != "" || err != nil {
		t.Fatalf("background job captured an interactive origin: %q %v", kind, err)
	}
	t.Setenv("AGENTNET_BACKGROUND", "")
	t.Setenv("CODEX_THREAD_ID", "synthetic-codex-thread")
	if _, err := plain(); err == nil || !strings.Contains(err.Error(), "cannot tell which assistant") {
		t.Fatalf("both harness names: %v", err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := plain(); err == nil || !strings.Contains(err.Error(), "Codex thread synthetic-codex-thread") {
		t.Fatalf("unregistered Codex origin: %v", err)
	}
}

// A refused origin sends nothing: no outbox row, no binding.
func TestCLIOriginRefusalSendsNothing(t *testing.T) {
	for _, k := range []string{"AGENTNET_REPLY_SESSION", "AGENTNET_BACKGROUND", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	a, home := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSendKind(ctx, a, "ask", []string{"peer/desk", "where is the shipment?"}); err == nil || !strings.Contains(err.Error(), "--reply-receiver human") {
		t.Fatalf("ask from an unregistered Claude session: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&n)
	if n != 0 {
		t.Fatalf("refused request queued %d outbox rows", n)
	}
}
