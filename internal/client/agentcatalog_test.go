package client

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestLocalAgentIdentityMappingsAndImmutableSelection(t *testing.T) {
	stub := installStub(t, "answer")
	w := newWorld(t, "")
	defaultConfig := &Responder{Harness: "stub", Dir: stub.dir, Timeout: time.Minute}
	if err := w.bob.SetResponder(defaultConfig); err != nil {
		t.Fatal(err)
	}
	before, err := w.bob.Responder()
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.bob.CreateLocalAgent("Builder", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.bob.CreateLocalAgent("Builder", Responder{Harness: "stub2", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Label != b.Label || a.Verify(w.bob.Self()) != nil || b.Verify(w.bob.Self()) != nil {
		t.Fatal("stable identity collapsed or unsigned")
	}
	after, _ := w.bob.Responder()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("agent setup changed device default")
	}
	tx, err := w.bob.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := w.bob.ResolveExecutorIn(tx, a.ID, after)
	tx.Rollback()
	if err != nil || stamp.AgentID != a.ID || stamp.Responder.Harness != "stub" {
		t.Fatal("first executor", err)
	}
	if err := w.bob.SetLocalAgentResponder(a.ID, &Responder{Harness: "stub2", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	if stamp.Responder.Harness != "stub" || stamp.Record.ID != a.ID {
		t.Fatal("running selection changed with catalog")
	}
	next, err := w.bob.ResolveExecutorIn(w.bob.store.db, a.ID, after)
	if err != nil || next.Responder.Harness != "stub2" {
		t.Fatal("next selection not updated", err)
	}
	if err := w.bob.SetLocalAgentResponder(a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, a.ID, after); !errors.Is(err, ErrUnknownAgent) {
		t.Fatal("removed identity fell back", err)
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, protocol.NewID(), after); !errors.Is(err, ErrUnknownAgent) {
		t.Fatal("unknown identity fell back", err)
	}
	foreign, err := w.alice.CreateLocalAgent("Builder", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, foreign.ID, after); !errors.Is(err, ErrUnknownAgent) {
		t.Fatal("foreign identity mapped locally", err)
	}
	public, err := w.bob.PublicAgentCatalog()
	if err != nil || len(public) != 1 || public[0].ID != b.ID {
		t.Fatal("removed record public", err)
	}
	entries, err := w.bob.LocalAgents()
	if err != nil || len(entries) != 2 || entries[0].Record.ID != a.ID {
		t.Fatal("historical identity lost", err)
	}
	legacy, err := w.bob.ResolveExecutorIn(w.bob.store.db, "", after)
	if err != nil || legacy.AgentID != "" || !reflect.DeepEqual(legacy.Responder, *after) {
		t.Fatal("legacy changed", err)
	}
	if err = w.bob.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	manual, err := w.bob.ResolveExecutorIn(w.bob.store.db, "", nil)
	if err != nil || manual != nil {
		t.Fatal("manual default changed", err)
	}
	selected, err := w.bob.ResolveExecutorIn(w.bob.store.db, b.ID, nil)
	if err != nil || selected == nil {
		t.Fatal("named selection depended on default", err)
	}
	raw, _ := w.bob.store.config(agentCatalogConfig)
	if strings.Contains(raw, "session_id") || strings.Contains(raw, "native_session") {
		t.Fatal("catalog contains native session mapping")
	}
}
