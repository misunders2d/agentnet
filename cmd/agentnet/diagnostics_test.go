package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func diagnosticOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	previous := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = previous }()
	runErr := fn()
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), runErr
}

func TestBuildDescription(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name     string
		info     *debug.BuildInfo
		expected string
	}{
		{"missing", nil, "build revision unknown, working tree unknown"},
		{"clean", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}}}, "build revision " + revision + ", working tree clean"},
		{"dirty", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "true"}}}, "build revision " + revision + ", working tree dirty"},
		{"unknown state", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}}}, "build revision " + revision + ", working tree unknown"},
		{"invalid state", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "invalid"}}}, "build revision unknown, working tree unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildDescription(tc.info); got != tc.expected {
				t.Fatalf("got %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestVersionDiagnosticsPreserveUpdateLines(t *testing.T) {
	previousInfo, previousVersion := readBuildInfo, protocol.Version
	t.Cleanup(func() { readBuildInfo, protocol.Version = previousInfo, previousVersion })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}}, true
	}
	home := filepath.Join(t.TempDir(), "absent")
	for _, version := range []string{"dev", "v0.5.0", "v0.5.0-3-gabcdef-dirty"} {
		protocol.Version = version
		out, err := diagnosticOutput(t, func() error { return run([]string{"--home", home, "version"}) })
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 2 || lines[0] != versionLine(version) || !strings.Contains(lines[1], "0123456789abcdef, working tree dirty") {
			t.Fatalf("version/update identity changed: %q", out)
		}
		out, err = diagnosticOutput(t, func() error { return run([]string{"--home", home, "version", "--schema"}) })
		if err != nil {
			t.Fatal(err)
		}
		want := versionLine(version) + fmt.Sprintf("\nschema %d\n", client.SchemaSteps())
		if out != want {
			t.Fatalf("schema parser contract changed: %q, want %q", out, want)
		}
	}
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
	if got := currentBuildDescription(); got != "build revision unknown, working tree unknown" {
		t.Fatal(got)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("version touched the absent home: %v", err)
	}
}

func diagnosticAgent(t *testing.T) (*client.Agent, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	home := t.TempDir()
	a, err := client.Join(ctx, home, testhub.BootstrapCode(t, hub), "diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, home
}

func TestInboxPeekPreservesReadAndReviewState(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const unreadID = "11111111111111111111111111111111"
	const readID = "22222222222222222222222222222222"
	if _, err := db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, read_at, state) VALUES
		(?, 'peer/device', 1, 'task', 'synthetic awaiting task', 1, NULL, 'awaiting'),
		(?, 'peer/device', 2, 'message', 'synthetic read message', 2, 123, '')`, unreadID, readID); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--peek"}, {"--peek", "--unread"}, {"--peek", "--json"}, {"--peek", "--unread", "--json"}, {"--peek", "--review"}} {
		out, err := diagnosticOutput(t, func() error { return runInbox(a, args) })
		if err != nil || !strings.Contains(out, unreadID) {
			t.Fatalf("peek %v: %q, %v", args, out, err)
		}
		var unreadAt, readAt sql.NullInt64
		var state string
		if err := db.QueryRow(`SELECT read_at, state FROM inbox WHERE id = ?`, unreadID).Scan(&unreadAt, &state); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT read_at FROM inbox WHERE id = ?`, readID).Scan(&readAt); err != nil {
			t.Fatal(err)
		}
		if unreadAt.Valid || !readAt.Valid || readAt.Int64 != 123 || state != "awaiting" {
			t.Fatalf("peek %v changed persisted state: unread=%v read=%v state=%s", args, unreadAt, readAt, state)
		}
	}
	if _, err := diagnosticOutput(t, func() error { return runInbox(a, nil) }); err != nil {
		t.Fatal(err)
	}
	var unread int
	if err := db.QueryRow(`SELECT count(*) FROM inbox WHERE read_at IS NULL`).Scan(&unread); err != nil || unread != 0 {
		t.Fatalf("default inbox stopped marking read: unread=%d, %v", unread, err)
	}
}

func TestHubRoleDiagnosticsIgnoreAddressLabels(t *testing.T) {
	admin, _ := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, err := admin.Invite(ctx, "admin", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	memberHome := t.TempDir()
	member, err := client.Join(ctx, memberHome, code, "zenbook")
	if err != nil {
		t.Fatal(err)
	}
	defer member.Close()
	for _, tc := range []struct {
		agent *client.Agent
		role  string
	}{{admin, "admin"}, {member, "member"}} {
		var out bytes.Buffer
		if err := runWhoami(ctx, tc.agent, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), tc.agent.Address+"\nfingerprint ") || !strings.Contains(out.String(), "hub role "+tc.role+" (reported by the Hub)") {
			t.Fatalf("role inferred from label or identity lines changed: %q", out.String())
		}
	}
	out, doctorErr := diagnosticOutput(t, func() error { return run([]string{"--home", memberHome, "doctor"}) })
	if doctorErr == nil || doctorErr.Error() != "some checks failed" {
		t.Fatalf("fixture has no daemon, but doctor returned %v", doctorErr)
	}
	if !strings.Contains(out, "hub-role") || !strings.Contains(out, "member (reported by the Hub)") || !strings.Contains(out, "build revision") {
		t.Fatalf("doctor omitted role/build diagnostics: %q", out)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	var offline bytes.Buffer
	if err := runWhoami(cancelled, member, &offline); err != nil {
		t.Fatalf("unreachable role broke local identity: %v", err)
	}
	if !strings.Contains(offline.String(), "hub role unknown (could not verify with the Hub)") {
		t.Fatalf("unverifiable role guessed: %q", offline.String())
	}
}
