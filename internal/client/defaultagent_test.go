package client

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Seed signed configurations that shipped before enabled-harness uniqueness.
// Current configuration APIs must never be used to create this legacy state.
func legacyLocalAgentEntry(a *Agent, label string, r Responder) LocalAgentInfo {
	record := protocol.AgentRecord{V: 1, ID: protocol.NewID(), Host: a.Address, HostKey: a.Self().Fingerprint(), Label: label, Harness: r.Harness, TS: time.Now().Unix()}
	record.Sign(a.id.Sign)
	value := copyResponder(r)
	return LocalAgentInfo{Record: record, Responder: &value}
}

func seedLegacyAgentCatalog(t *testing.T, a *Agent, entries []LocalAgentInfo) {
	t.Helper()
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.setConfig(map[string]string{agentCatalogConfig: string(raw)}); err != nil {
		t.Fatal(err)
	}
}

func TestConcreteDefaultProgramCyclesReuseHarnessIdentity(t *testing.T) {
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
	if piID != named.ID {
		t.Fatal("automatic default duplicated the existing harness identity", piID)
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
	// Retained disabled identities still occupy the unchanged 32-entry bound.
	entries, err := w.bob.LocalAgents()
	if err != nil {
		t.Fatal(err)
	}
	for len(entries) < protocol.MaxAgentCatalog {
		retired := legacyLocalAgentEntry(w.bob, "Retired Pi", pi)
		retired.Responder = nil
		entries = append(entries, retired)
	}
	seedLegacyAgentCatalog(t, w.bob, entries)
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
	entries, err = w.bob.LocalAgents()
	if err != nil || len(entries) != protocol.MaxAgentCatalog {
		t.Fatal("cycles changed catalog size", len(entries), err)
	}
	enabled := map[string]int{}
	for _, entry := range entries {
		if entry.Responder != nil {
			enabled[entry.Responder.Harness]++
		}
	}
	if enabled["pi"] != 1 || enabled["claude"] != 1 || len(enabled) != 2 {
		t.Fatal("cycles duplicated enabled harnesses", enabled)
	}
	// Returning with config edits updates future admissions in that program's
	// existing identity, as a direct config edit would.
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
	if id, err := w.bob.DefaultAgentID(); err != nil || id != "" {
		t.Fatal("Manual retained an automatic default", id, err)
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, piID, nil); err != nil {
		t.Fatal("Manual disabled the connected harness", err)
	}
	if err = w.bob.SetLocalAgentResponder(claudeID, nil); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SetResponder(&claude); err != nil {
		t.Fatal("returning program lost its disabled automatic identity", err)
	}
	if id, _ := w.bob.DefaultAgentID(); id != claudeID {
		t.Fatal("disabled automatic identity was reminted", id)
	}
	if err = w.bob.SetResponder(&different); err != nil {
		t.Fatal("manual transition lost connected Pi identity", err)
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
	w := newWorld(t, "")
	r := Responder{Harness: "pi", Dir: t.TempDir()}
	var legacy []LocalAgentInfo
	for i := 0; i < protocol.MaxAgentCatalog; i++ {
		legacy = append(legacy, legacyLocalAgentEntry(w.bob, "Same name", r))
	}
	seedLegacyAgentCatalog(t, w.bob, legacy)
	first := legacy[0].Record.ID
	history := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: 123, Kind: envelope.KindAnswer, AgentID: legacy[1].Record.ID, Body: "retained historical answer"}
	if err := w.bob.store.addInbox(history, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	const historyQuery = `SELECT json_array(id,sender,ts,body,kind,agent_id) FROM inbox WHERE id=?`
	var beforeHistory string
	if err := w.bob.store.db.QueryRow(historyQuery, history.ID).Scan(&beforeHistory); err != nil {
		t.Fatal(err)
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
	if err = reopened.SetResponder(&r); !errors.Is(err, ErrAgentHarnessAmbiguous) {
		t.Fatal("ambiguous legacy harness was selected without an exact identity", err)
	}
	public, err := reopened.PublicAgentCatalog()
	if err != nil || len(public) != 0 {
		t.Fatal("ambiguous harness was published as a new execution choice", err)
	}
	for _, entry := range legacy {
		if _, err = reopened.ResolveExecutorIn(reopened.store.db, entry.Record.ID, &r); !errors.Is(err, ErrAgentHarnessAmbiguous) {
			t.Fatal("ambiguous legacy identity admitted new work", err)
		}
	}
	if _, err = reopened.ResolveExecutorIn(reopened.store.db, "", &r); !errors.Is(err, ErrAgentHarnessAmbiguous) {
		t.Fatal("ambiguous untargeted request guessed an executor", err)
	}
	if err = reopened.SetDefaultAgent(first); err != nil {
		t.Fatal("explicit recovery failed", err)
	}
	id, _ = reopened.DefaultAgentID()
	if id != first {
		t.Fatal("explicit selection not saved")
	}
	entries, err = reopened.LocalAgents()
	if err != nil || len(entries) != len(legacy) {
		t.Fatal("canonical selection removed historical identities", err)
	}
	enabled := 0
	for i, entry := range entries {
		beforeRecord, _ := json.Marshal(legacy[i].Record)
		afterRecord, _ := json.Marshal(entry.Record)
		if string(beforeRecord) != string(afterRecord) {
			t.Fatal("canonical selection rewrote historical identity")
		}
		if entry.Responder != nil {
			enabled++
			if entry.Record.ID != first {
				t.Fatal("noncanonical harness remained enabled")
			}
		}
	}
	if enabled != 1 {
		t.Fatal("explicit selection did not resolve enabled duplicates", enabled)
	}
	public, err = reopened.PublicAgentCatalog()
	if err != nil || len(public) != 1 || public[0].ID != first {
		t.Fatal("explicit canonical identity not published", err)
	}
	var afterHistory string
	if err = reopened.store.db.QueryRow(historyQuery, history.ID).Scan(&afterHistory); err != nil || afterHistory != beforeHistory {
		t.Fatal("legacy canonical selection changed original history", err)
	}
	if _, err = reopened.ResolveExecutorIn(reopened.store.db, legacy[1].Record.ID, nil); !errors.Is(err, ErrUnknownAgent) {
		t.Fatal("retired historical identity fell back to the canonical harness", err)
	}
}

func TestConcreteDefaultMigrationRestartAndBindings(t *testing.T) {
	w := newWorld(t, "")
	r := Responder{Harness: "pi", Dir: t.TempDir()}
	existing, err := w.bob.CreateLocalAgent("stub", r)
	if err != nil {
		t.Fatal(err)
	}
	// An exact saved configuration proves which existing identity to reuse.
	raw, _ := json.Marshal(r)
	if err = w.bob.store.setConfig(map[string]string{"responder": string(raw)}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.ensureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	id, err := w.bob.DefaultAgentID()
	if err != nil || id != existing.ID {
		t.Fatal("default migration duplicated the exact existing configuration", id, err)
	}
	entries, _ := w.bob.LocalAgents()
	if len(entries) != 1 {
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
	if after != id || len(entries) != 1 {
		t.Fatal("restart reminted default", after, len(entries))
	}
	if err = w.bob.SetResponder(&Responder{Harness: "claude", Dir: r.Dir}); err != nil {
		t.Fatal(err)
	}
	next, _ := w.bob.DefaultAgentID()
	if next == id {
		t.Fatal("default change reused historical identity")
	}
	if old.AgentID != id || old.Responder.Harness != "pi" || old.Record.Harness != "pi" {
		t.Fatal("old admitted binding changed")
	}
	if err = w.bob.SetLocalAgentResponder(existing.ID, &Responder{Harness: "claude", Dir: r.Dir}); !errors.Is(err, ErrAgentHarnessConflict) {
		t.Fatal("mapping change did not report the other enabled harness", err)
	}
	if err = w.bob.SetLocalAgentResponder(existing.ID, &Responder{Harness: "codex", Dir: r.Dir}); err != nil {
		t.Fatal(err)
	}
	stamp, err := w.bob.ResolveExecutorIn(w.bob.store.db, existing.ID, nil)
	if err != nil || stamp.Record.Harness != "codex" || stamp.Responder.Harness != "codex" || stamp.Record.Verify(w.bob.Self()) != nil {
		t.Fatal("new stamp contains old harness", err)
	}
	if err = w.bob.SetDefaultAgent(existing.ID); err != nil {
		t.Fatal(err)
	}
	selected, _ := w.bob.DefaultAgentID()
	if selected != existing.ID {
		t.Fatal("explicit default selection not saved")
	}
	if err = w.bob.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	selected, _ = w.bob.DefaultAgentID()
	if selected != "" {
		t.Fatal("manual has a default recipient")
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, existing.ID, nil); err != nil {
		t.Fatal("Manual disabled the explicitly configured harness", err)
	}
	legacy, err := w.bob.ResolveExecutorIn(w.bob.store.db, "", &r)
	if err != nil || legacy.AgentID != "" || legacy.Record != nil {
		t.Fatal("legacy traffic rebound", err)
	}
}

func TestConcreteDefaultExistingPointerReconcilesLegacyHarness(t *testing.T) {
	w := newWorld(t, "")
	r := Responder{Harness: "pi", Dir: t.TempDir(), Timeout: 37 * time.Second}
	canonical := legacyLocalAgentEntry(w.bob, "Current Pi", r)
	canonical.DefaultResponder = true
	sameConfig := legacyLocalAgentEntry(w.bob, "Earlier Pi", r)
	otherConfig := legacyLocalAgentEntry(w.bob, "Other directory Pi", Responder{Harness: "pi", Dir: t.TempDir()})
	claude := legacyLocalAgentEntry(w.bob, "Claude", Responder{Harness: "claude", Dir: r.Dir})
	legacy := []LocalAgentInfo{sameConfig, otherConfig, canonical, claude}
	seedLegacyAgentCatalog(t, w.bob, legacy)
	raw, _ := json.Marshal(r)
	if err := w.bob.store.setConfig(map[string]string{"responder": string(raw), defaultAgentConfig: canonical.Record.ID}); err != nil {
		t.Fatal(err)
	}
	// This is a retained pre-upgrade admission, not a new claim through an
	// ambiguous catalog. Its selected identity and executor bytes are frozen.
	request := namedExecutorRequest(t, w, otherConfig.Record.ID, envelope.KindQuestion, "")
	stamp := ExecutorStamp{AgentID: otherConfig.Record.ID, Record: &otherConfig.Record, Responder: copyResponder(*otherConfig.Responder)}
	executor, _ := json.Marshal(stamp)
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=?,attempts=3,executor=? WHERE id=?`, stateRunning, string(executor), request); err != nil {
		t.Fatal(err)
	}
	const admissionQuery = `SELECT json_array(id,sender,verified_by,body,ts,reply_to,executor,attempts,target) FROM inbox WHERE id=?`
	var beforeAdmission string
	if err := w.bob.store.db.QueryRow(admissionQuery, request).Scan(&beforeAdmission); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.ensureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	id, err := w.bob.DefaultAgentID()
	if err != nil || id != canonical.Record.ID {
		t.Fatal("migration ignored the proven current identity", id, err)
	}
	entries, err := w.bob.LocalAgents()
	if err != nil || len(entries) != len(legacy) {
		t.Fatal("migration removed legacy identities", err)
	}
	for i, entry := range entries {
		beforeRecord, _ := json.Marshal(legacy[i].Record)
		afterRecord, _ := json.Marshal(entry.Record)
		if string(beforeRecord) != string(afterRecord) {
			t.Fatal("migration rewrote an original signed identity")
		}
		wantEnabled := entry.Record.ID == canonical.Record.ID || entry.Record.ID == claude.Record.ID
		if (entry.Responder != nil) != wantEnabled {
			t.Fatal("migration disabled the wrong harness identity", entry.Record.ID)
		}
	}
	for _, retired := range []string{sameConfig.Record.ID, otherConfig.Record.ID} {
		if _, err := w.bob.ResolveExecutorIn(w.bob.store.db, retired, &r); !errors.Is(err, ErrUnknownAgent) {
			t.Fatal("retired explicit identity fell back to the current harness", err)
		}
	}
	afterCatalog, _ := w.bob.store.config(agentCatalogConfig)
	if err = w.bob.ensureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	if repeated, _ := w.bob.store.config(agentCatalogConfig); repeated != afterCatalog {
		t.Fatal("repeated reconciliation rewrote the catalog")
	}
	if err = w.bob.Close(); err != nil {
		t.Fatal(err)
	}
	w.bob, err = Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer w.bob.Close()
	var afterAdmission string
	if err = w.bob.store.db.QueryRow(admissionQuery, request).Scan(&afterAdmission); err != nil || afterAdmission != beforeAdmission {
		t.Fatal("reconciliation or restart changed the admitted original snapshot", err)
	}
}
