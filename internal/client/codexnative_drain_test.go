package client

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type codexDrainFixture struct {
	Route codexNativeRoute
	File  string
	SID   string
}

// The test binary stands in for the native CLI. The captured process, binary
// digest, daemon record and socket are real; no installed harness is invoked.
func runCodexDrainCLI() error {
	home := os.Getenv("CODEX_HOME")
	raw, err := os.ReadFile(filepath.Join(home, "drain-fixture.json"))
	if err != nil {
		return err
	}
	var f codexDrainFixture
	if err = json.Unmarshal(raw, &f); err != nil {
		return err
	}
	args := os.Args[1:]
	command := ""
	if len(args) == 3 && strings.Join(args, " ") == "app-server daemon version" {
		command = "version"
	} else if len(args) == 7 && args[0] == "queue" && args[1] == "--remote" && args[2] == f.Route.Endpoint && args[3] == "--thread" && args[4] == f.SID && args[5] == "--message" {
		command = "queue"
	} else {
		return fmt.Errorf("unexpected synthetic native command")
	}
	log, err := os.OpenFile(filepath.Join(home, "commands"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(log, command)
	log.Close()
	if err != nil {
		return err
	}
	if command == "version" {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"status": "running", "backend": "pid", "socketPath": strings.TrimPrefix(f.Route.Endpoint, "unix://"),
			"managedCodexPath": f.Route.Binary, "managedCodexVersion": "0.160.0", "cliVersion": "0.160.0",
		})
	}
	rollout, err := os.OpenFile(f.File, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer rollout.Close()
	return json.NewEncoder(rollout).Encode(codexReceiptRow(f.SID, args[6]))
}

func TestCodexReplyDrainOnlyPendingInput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Codex native route uses Linux process identity")
	}
	w := newWorld(t, "")
	sid, file, route := codexOriginFixture(t, w.alice)
	route.PID = os.Getpid()
	var err error
	_, route.Ticks, route.Binary, err = codexProcess(route.PID)
	if err != nil {
		t.Fatal(err)
	}
	if route.SHA256, err = codexBinaryDigest(route.Binary); err != nil {
		t.Fatal(err)
	}
	// Shared CI build/temp roots can exceed the Unix socket pathname limit.
	socketDir, err := os.MkdirTemp("/tmp", "agentnet-codex-drain-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	route.Endpoint = "unix://" + socket
	writeCodexDaemonRecord(t, route.Home, route.PID, route.Boot, route.Ticks)
	raw, err := json.Marshal(codexDrainFixture{Route: route, File: file, SID: sid})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(route.Home, "drain-fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_TEST_CODEX_DRAIN", "1")
	handle, err := w.alice.registerCodexReplySession("SessionStart", sid, file, route)
	if err != nil {
		t.Fatal(err)
	}
	if err = route.verify(tctx(t), false); err != nil {
		t.Fatalf("synthetic native route must verify: %v", err)
	}
	commands := func() string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(route.Home, "commands"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if commands() != "version\n" {
		t.Fatal("route verification did not reach synthetic native CLI")
	}
	quiet := func(label string) {
		t.Helper()
		before := commands()
		started := time.Now()
		for range 8 {
			// Unrelated store changes wake the worker and its receiver drain.
			_, changed := w.alice.Changed()
			w.alice.store.changed()
			<-changed
			w.alice.drainCodexReplyInputs()
		}
		added := strings.TrimPrefix(commands(), before)
		t.Logf("%s: 8 drains, %d native commands, %s", label, strings.Count(added, "\n"), time.Since(started))
		if added != "" {
			t.Errorf("%s: no eligible input, but native CLI was probed: %q", label, added)
		}
	}
	quiet("empty inbox")
	binding, input := nativeInput(t, w.alice, w.bob, handle)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := w.alice.store.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE reply_receivers SET receiver=json_set(receiver,'$.session_handle','other') WHERE id=?`, binding)
	quiet("other session input")
	exec(`UPDATE reply_receivers SET receiver=json_set(receiver,'$.session_handle',?),canceled_at=1 WHERE id=?`, handle, binding)
	quiet("canceled binding")
	exec(`UPDATE reply_receivers SET canceled_at=NULL WHERE id=?`, binding)
	// The cheap candidate filter never replaces native identity verification.
	exec(`UPDATE reply_sessions SET record=json_set(record,'$.codex.sha256',?) WHERE handle=?`, strings.Repeat("0", 64), handle)
	w.alice.drainCodexReplyInputs()
	var claim *string
	if err = w.alice.store.db.QueryRow(`SELECT live_claim FROM reply_receiver_inputs WHERE inbox_id=?`, input).Scan(&claim); err != nil || claim != nil {
		t.Fatalf("changed executable claimed input: %v %v", claim, err)
	}
	exec(`UPDATE reply_sessions SET record=json_set(record,'$.codex.sha256',?) WHERE handle=?`, route.SHA256, handle)
	w.alice.drainCodexReplyInputs()
	var state string
	if err = w.alice.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE inbox_id=?`, input).Scan(&state); err != nil || state != "accepted" {
		t.Fatalf("exact input did not receive its native receipt: %s %v", state, err)
	}
	if n := strings.Count(commands(), "queue\n"); n != 1 {
		t.Fatalf("exact input queued %d times", n)
	}
	quiet("accepted input")
	// Model a crash after the native receipt but before the local ACK commit:
	// the retained pending claim must reconcile, never queue the text again.
	exec(`UPDATE reply_receiver_inputs SET state='pending',accepted_at=NULL WHERE inbox_id=?`, input)
	w.alice.drainCodexReplyInputs()
	if err = w.alice.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE inbox_id=?`, input).Scan(&state); err != nil || state != "accepted" {
		t.Fatalf("pending native claim did not reconcile: %s %v", state, err)
	}
	if n := strings.Count(commands(), "queue\n"); n != 1 {
		t.Fatalf("receipt reconciliation queued %d copies", n)
	}
}
