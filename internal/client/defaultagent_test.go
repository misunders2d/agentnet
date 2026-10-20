package client

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestConcreteDefaultProgramCyclesReuseOnlyGeneratedLineage(t *testing.T) {
	w := newWorld(t, "")
	pi := Responder{Harness: "pi", Dir: t.TempDir(), Timeout: 37 * time.Second}
	claude := Responder{Harness: "claude", Dir: pi.Dir, Timeout: pi.Timeout}
	named, err := w.bob.CreateLocalAgent("Pi", pi)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SetResponder(&pi); err != nil {
		t.Fatal(err)
	}
	piID, _ := w.bob.DefaultAgentID()
	if piID == "" || piID == named.ID {
		t.Fatal("automatic default selected named twin", piID)
	}
	admitted, err := w.bob.ResolveExecutorIn(w.bob.store.db, piID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SetResponder(&claude); err != nil {
		t.Fatal(err)
	}
	claudeID, _ := w.bob.DefaultAgentID()
	if claudeID == "" || claudeID == piID || claudeID == named.ID {
		t.Fatal("program change joined distinct identity", claudeID)
	}
	// The first Pi -> Claude -> Pi transition must return to the original ID.
	// Repeated transitions must also work at the unchanged 32-entry bound.
	for i := 3; i < protocol.MaxAgentCatalog; i++ {
		if _, err = w.bob.CreateLocalAgent("Named twin", pi); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2*protocol.MaxAgentCatalog; i++ {
		r, want := &pi, piID
		if i%2 == 1 {
			r, want = &claude, claudeID
		}
		if err = w.bob.SetResponder(r); err != nil {
			t.Fatalf("cycle %d at catalog limit: %v", i, err)
		}
		if got, err := w.bob.DefaultAgentID(); err != nil || got != want {
			t.Fatalf("cycle %d reminted default: %s, want %s (%v)", i, got, want, err)
		}
	}
	entries, err := w.bob.LocalAgents()
	if err != nil || len(entries) != protocol.MaxAgentCatalog {
		t.Fatal("cycles changed catalog size", len(entries), err)
	}
	generated := 0
	for _, entry := range entries {
		if entry.DefaultResponder {
			generated++
		}
	}
	if generated != 2 {
		t.Fatal("cycles duplicated automatic identities", generated)
	}
	// Returning with config edits updates future admissions in that program's
	// existing automatic lineage, as a direct config edit would.
	different := pi
	different.Dir = t.TempDir()
	different.Timeout++
	if err = w.bob.SetResponder(&different); err != nil {
		t.Fatal("returning program config edit exhausted catalog", err)
	}
	if got, _ := w.bob.DefaultAgentID(); got != piID {
		t.Fatal("config edit reminted returning automatic lineage", got)
	}
	future, err := w.bob.ResolveExecutorIn(w.bob.store.db, piID, nil)
	if err != nil || future.Responder.Dir != different.Dir || future.Responder.Timeout != different.Timeout {
		t.Fatal("returning program config edit missing from future admission", err)
	}
	// An actually new program still respects the unchanged catalog bound.
	newProgram := Responder{Harness: "codex", Dir: pi.Dir}
	if err = w.bob.SetResponder(&newProgram); !errors.Is(err, ErrAgentCatalogFull) {
		t.Fatal("new automatic program bypassed catalog bound", err)
	}
	if got, _ := w.bob.DefaultAgentID(); got != piID {
		t.Fatal("failed full-catalog change moved current default", got)
	}
	if admitted.AgentID != piID || admitted.Record.Harness != "pi" || admitted.Responder.Harness != "pi" || admitted.Responder.Dir != pi.Dir {
		t.Fatal("cycles changed admitted historical executor")
	}
	if err = w.bob.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SetResponder(&claude); err != nil {
		t.Fatal("manual transition lost Claude lineage", err)
	}
	if err = w.bob.SetResponder(&different); err != nil {
		t.Fatal("manual transition lost disabled Pi lineage", err)
	}
	if got, _ := w.bob.DefaultAgentID(); got != piID {
		t.Fatal("manual transition reminted automatic Pi lineage", got)
	}
	if err = w.bob.Close(); err != nil {
		t.Fatal(err)
	}
	w.bob, err = Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer w.bob.Close()
	if err = w.bob.SetResponder(&pi); err != nil {
		t.Fatal("restart lost reusable automatic lineage", err)
	}
	if got, _ := w.bob.DefaultAgentID(); got != piID {
		t.Fatal("restart reminted automatic lineage", got)
	}
}

func TestConcreteDefaultFullLegacyCatalogRemainsReadable(t *testing.T) {
	stub := installStub(t, "answer")
	w := newWorld(t, "")
	r := Responder{Harness: "stub", Dir: stub.dir}
	var first string
	for i := 0; i < protocol.MaxAgentCatalog; i++ {
		record, err := w.bob.CreateLocalAgent("Same name", r)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = record.ID
		}
	}
	raw, _ := json.Marshal(r)
	if err := w.bob.store.setConfig(map[string]string{"responder": string(raw)}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(w.bobHome)
	if err != nil {
		t.Fatal("full legacy profile became unreadable", err)
	}
	defer reopened.Close()
	entries, err := reopened.LocalAgents()
	if err != nil || len(entries) != protocol.MaxAgentCatalog {
		t.Fatal("catalog changed", len(entries), err)
	}
	id, err := reopened.DefaultAgentID()
	if err != nil || id != "" {
		t.Fatal("migration guessed a named twin", id, err)
	}
	if current, err := reopened.Responder(); err != nil || current == nil || current.Harness != r.Harness || current.Dir != r.Dir {
		t.Fatal("old responder lost", err)
	}
	if err = reopened.SetDefaultAgent(first); err != nil {
		t.Fatal("explicit recovery failed", err)
	}
	id, _ = reopened.DefaultAgentID()
	if id != first {
		t.Fatal("explicit selection not saved")
	}
}

func TestConcreteDefaultMigrationRestartAndBindings(t *testing.T) {
	stub := installStub(t, "answer")
	w := newWorld(t, "")
	r := Responder{Harness: "stub", Dir: stub.dir}
	twin, err := w.bob.CreateLocalAgent("stub", r)
	if err != nil {
		t.Fatal(err)
	}
	// A shipped unnamed default plus an identical named instance: migration
	// creates its own identity and never joins their historical traffic.
	raw, _ := json.Marshal(r)
	if err = w.bob.store.setConfig(map[string]string{"responder": string(raw)}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.ensureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	id, err := w.bob.DefaultAgentID()
	if err != nil || id == "" || id == twin.ID {
		t.Fatal("default merged with named twin", id, err)
	}
	entries, _ := w.bob.LocalAgents()
	if len(entries) != 2 {
		t.Fatal("migration entry count", len(entries))
	}
	old, err := w.bob.ResolveExecutorIn(w.bob.store.db, id, &r)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.ensureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.Close(); err != nil {
		t.Fatal(err)
	}
	w.bob, err = Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer w.bob.Close()
	after, _ := w.bob.DefaultAgentID()
	entries, _ = w.bob.LocalAgents()
	if after != id || len(entries) != 2 {
		t.Fatal("restart reminted default", after, len(entries))
	}
	if err = w.bob.SetResponder(&Responder{Harness: "stub2", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	next, _ := w.bob.DefaultAgentID()
	if next == id || next == twin.ID {
		t.Fatal("default change reused historical identity")
	}
	if old.AgentID != id || old.Responder.Harness != "stub" || old.Record.Harness != "stub" {
		t.Fatal("old admitted binding changed")
	}
	if err = w.bob.SetLocalAgentResponder(twin.ID, &Responder{Harness: "stub2", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	stamp, err := w.bob.ResolveExecutorIn(w.bob.store.db, twin.ID, nil)
	if err != nil || stamp.Record.Harness != "stub2" || stamp.Responder.Harness != "stub2" || stamp.Record.Verify(w.bob.Self()) != nil {
		t.Fatal("new stamp contains old harness", err)
	}
	if err = w.bob.SetDefaultAgent(twin.ID); err != nil {
		t.Fatal(err)
	}
	selected, _ := w.bob.DefaultAgentID()
	if selected != twin.ID {
		t.Fatal("explicit default selection not saved")
	}
	if err = w.bob.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	selected, _ = w.bob.DefaultAgentID()
	if selected != "" {
		t.Fatal("manual has a default recipient")
	}
	legacy, err := w.bob.ResolveExecutorIn(w.bob.store.db, "", &r)
	if err != nil || legacy.AgentID != "" || legacy.Record != nil {
		t.Fatal("legacy traffic rebound", err)
	}
}
