package client

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestAgentHarnessUniqueness(t *testing.T) {
	for _, operation := range []string{"create", "update", "reenable"} {
		t.Run(operation, func(t *testing.T) {
			w := newWorld(t, "")
			pi := Responder{Harness: "pi", Dir: t.TempDir()}
			claude := Responder{Harness: "claude", Dir: t.TempDir()}
			first, err := w.bob.CreateLocalAgent("First", pi)
			if err != nil {
				t.Fatal(err)
			}
			var other protocol.AgentRecord
			if operation != "create" {
				other, err = w.bob.CreateLocalAgent("Other", claude)
				if err != nil {
					t.Fatal(err)
				}
				if operation == "reenable" {
					if err = w.bob.SetLocalAgentResponder(other.ID, nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := w.bob.store.config(agentCatalogConfig)
			if err != nil {
				t.Fatal(err)
			}
			duplicate := pi
			duplicate.Dir = t.TempDir() // A different label or directory is still Pi.
			if operation == "create" {
				_, err = w.bob.CreateLocalAgent("Different label", duplicate)
			} else {
				err = w.bob.SetLocalAgentResponder(other.ID, &duplicate)
			}
			if !errors.Is(err, ErrAgentHarnessConflict) {
				t.Fatal("second enabled instance did not report a harness conflict", err)
			}
			after, readErr := w.bob.store.config(agentCatalogConfig)
			if readErr != nil || after != before {
				t.Fatal("refused harness conflict changed the catalog", readErr)
			}
			if _, err = w.alice.CreateLocalAgent("Pi on another device", pi); err != nil {
				t.Fatal("same harness on another device refused", err)
			}
			if err = w.bob.SetLocalAgentResponder(first.ID, nil); err != nil {
				t.Fatal("conflict prevented disabling the old identity", err)
			}
			if operation != "create" {
				if err = w.bob.SetLocalAgentResponder(other.ID, &duplicate); err != nil {
					t.Fatal("freed harness could not be enabled", err)
				}
			}
		})
	}
}

func TestAgentHarnessConcurrentCreate(t *testing.T) {
	w := newWorld(t, "")
	r := Responder{Harness: "pi", Dir: t.TempDir()}
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, label := range []string{"First", "Second"} {
		go func() {
			ready.Done()
			<-start
			_, err := w.bob.CreateLocalAgent(label, r)
			outcomes <- err
		}()
	}
	ready.Wait()
	close(start)
	succeeded := 0
	for range 2 {
		if err := <-outcomes; err == nil {
			succeeded++
		} else if !errors.Is(err, ErrAgentHarnessConflict) {
			t.Fatal("concurrent refusal was not a harness conflict", err)
		}
	}
	entries, err := w.bob.LocalAgents()
	if err != nil || succeeded != 1 || len(entries) != 1 || entries[0].Responder == nil {
		t.Fatalf("concurrent creates: successes=%d entries=%d error=%v", succeeded, len(entries), err)
	}
}

func TestAgentHarnessAmbiguousRequestDoesNotBlockIndependentWork(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	// The selected executor authors a named answer, so the requester must
	// advertise the real reader capability before its reply can be stored.
	runAgent(t, w.alice)
	waitNamedAgentCaps(t, w.alice)
	w.bob.Logf = t.Logf
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	valid, err := w.bob.CreateLocalAgent("Independent stub", Responder{Harness: "stub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := w.bob.LocalAgents()
	if err != nil {
		t.Fatal(err)
	}
	pi := Responder{Harness: "pi", Dir: st.dir}
	first := legacyLocalAgentEntry(w.bob, "Earlier Pi", pi)
	second := legacyLocalAgentEntry(w.bob, "Other Pi", pi)
	seedLegacyAgentCatalog(t, w.bob, append(entries, first, second))
	beforeCatalog, err := w.bob.store.config(agentCatalogConfig)
	if err != nil {
		t.Fatal(err)
	}
	// A retained, already answered pre-upgrade admission is never rebound by
	// scanning new requests through the ambiguous legacy configurations.
	history := namedExecutorRequest(t, w, second.Record.ID, envelope.KindQuestion, "")
	executor, err := json.Marshal(ExecutorStamp{AgentID: second.Record.ID, Record: &second.Record, Responder: pi})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,executor=?,agent_id=? WHERE id=?`, stateAnswered, string(executor), second.Record.ID, history); err != nil {
		t.Fatal(err)
	}
	blocked := namedExecutorRequest(t, w, first.Record.ID, envelope.KindQuestion, "")
	ready := namedExecutorRequest(t, w, valid.ID, envelope.KindQuestion, "")
	for position, id := range []string{blocked, ready} {
		if _, err = w.bob.store.db.Exec(`UPDATE inbox SET received_at=? WHERE id=?`, position+1, id); err != nil {
			t.Fatal(err)
		}
	}
	const originalQuery = `SELECT json_array(id,sender,verified_by,body,ts,reply_to,target,state,attempts,executor,agent_id) FROM inbox WHERE id=?`
	originals := map[string]string{}
	for _, id := range []string{history, blocked} {
		var raw string
		if err = w.bob.store.db.QueryRow(originalQuery, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		originals[id] = raw
	}
	if !w.bob.runNext(tctx(t), nil) {
		t.Fatal("ambiguous first request prevented the independent selected harness from running")
	}
	if state, _ := w.bob.store.jobState(ready); state != stateAnswered || st.count() != 1 {
		var detail string
		if err := w.bob.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, ready).Scan(&detail); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("independent request: state=%s executions=%d detail=%s", state, st.count(), detail)
	}
	var raw, detail string
	if err = w.bob.store.db.QueryRow(`SELECT executor FROM inbox WHERE id=?`, ready).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var admitted ExecutorStamp
	if err = json.Unmarshal([]byte(raw), &admitted); err != nil || admitted.AgentID != valid.ID || admitted.Responder.Harness != "stub" {
		t.Fatal("independent request did not retain its exact executor", err)
	}
	if err = w.bob.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, blocked).Scan(&detail); err != nil || !strings.Contains(detail, ErrAgentHarnessAmbiguous.Error()) {
		t.Fatal("ambiguous request lacks its actionable configuration reason", detail, err)
	}
	if w.bob.runNext(tctx(t), nil) || st.count() != 1 {
		t.Fatal("ambiguous request ran or completed request repeated")
	}
	for id, before := range originals {
		var after string
		if err = w.bob.store.db.QueryRow(originalQuery, id).Scan(&after); err != nil || after != before {
			t.Fatal("waiting admission or retained history/stamp was rewritten", id, err)
		}
	}
	if after, err := w.bob.store.config(agentCatalogConfig); err != nil || after != beforeCatalog {
		t.Fatal("worker chose or rewrote an ambiguous configuration", err)
	}
}
