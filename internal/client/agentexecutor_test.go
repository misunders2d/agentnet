package client

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Fixtures inject already-authenticated inbox rows; envelope admission is
// separately covered by the named-device binding regression.
func namedExecutorRequest(t *testing.T, w *world, id, kind, reply string) string {
	t.Helper()
	in := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: kind, Body: "normalized granted request", ReplyTo: reply,
		Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: id}}
	if err := w.alice.store.addOutbox(envelope.Envelope{ID: in.ID, From: in.From, To: in.To}, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	return in.ID
}

func TestNamedExecutorABAAndImmutableClaim(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	runAgent(t, w.alice)
	label, name, _ := protocol.SplitAddress(w.alice.Address)
	var prof protocol.Profile
	eventually(t, "synthetic alice session published", func() bool {
		err := w.bob.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof)
		return err == nil && len(prof.Sessions) == 1
	})
	caps := protocol.CapsRecord{Address: w.alice.Address, Session: prof.Sessions[0], Caps: []string{protocol.CapAgentIdentity}, TS: time.Now().Unix() + 100}
	caps.Sign(w.alice.id.Sign)
	if err := w.alice.hub.do(tctx(t), "PUT", "/v1/caps", caps, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.SetResponder(&Responder{Harness: "stub", Dir: st.dir}); err != nil {
		t.Fatal(err)
	}
	defaultBefore, _ := w.bob.Responder()
	a, err := w.bob.CreateLocalAgent("A", Responder{Harness: "cstyle", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.bob.CreateLocalAgent("B", Responder{Harness: "cstyle2", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for i, rec := range []protocol.AgentRecord{a, b, a} {
		request := namedExecutorRequest(t, w, rec.ID, envelope.KindQuestion, previous)
		if !w.bob.runNext(tctx(t), nil) {
			t.Fatal("job not claimed")
		}
		if state, _ := w.bob.store.jobState(request); state != stateAnswered {
			row := inboxRow(t, w.bob, request)
			t.Fatalf("job %d: %+v", i, row)
		}
		var raw, answerID string
		if err := w.bob.store.db.QueryRow(`SELECT executor,result_id FROM inbox WHERE id=?`, request).Scan(&raw, &answerID); err != nil {
			t.Fatal(err)
		}
		var stamp ExecutorStamp
		if json.Unmarshal([]byte(raw), &stamp) != nil || stamp.AgentID != rec.ID {
			t.Fatalf("stamp %s", raw)
		}
		env, err := w.bob.store.outboxEnvelope(answerID)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.alice.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		var author string
		if err = w.alice.store.db.QueryRow(`SELECT agent_id FROM inbox WHERE id=?`, answerID).Scan(&author); err != nil || author != rec.ID {
			t.Fatalf("author %q %v", author, err)
		}
		if sessionOf(t, w.bob, request) != nil {
			t.Fatal("named request persisted native session")
		}
		previous = answerID
	}
	after, _ := w.bob.Responder()
	if !reflect.DeepEqual(defaultBefore, after) {
		t.Fatal("per-request selection changed default")
	}
	if w.bob.runNext(tctx(t), nil) || st.count() != 3 {
		t.Fatalf("rerun/count %d", st.count())
	}
	log, _ := os.ReadFile(st.log)
	if strings.Count(string(log), "args=--question-mode --no-session-persistence") != 2 || strings.Count(string(log), "args=--other-flags --no-session-persistence") != 1 || strings.Contains(string(log), "--resume") {
		t.Fatalf("A/B/A/session mapping: %s", log)
	}

	// A selected running stamp survives mapping changes and restart storage.
	request := namedExecutorRequest(t, w, a.ID, envelope.KindQuestion, previous)
	resolve := func(q dbq, id string) (*ExecutorStamp, error) { return w.bob.ResolveExecutorIn(q, id, defaultBefore) }
	j, ok, err := w.bob.store.claimJob(defaultBefore.Harness, resolve)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = w.bob.SetLocalAgentResponder(a.ID, &Responder{Harness: "cstyle2", Dir: st.dir}); err != nil {
		t.Fatal(err)
	}
	if j.Executor.Responder.Harness != "cstyle" {
		t.Fatal("running selection mutated")
	}
	// Reopen the actual store before reading the running snapshot.
	_, dbPath := paths(w.bob.home)
	if err = w.bob.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, openErr := openStore(dbPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	w.bob.store = reopened
	var raw string
	if err = w.bob.store.db.QueryRow(`SELECT executor FROM inbox WHERE id=?`, request).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var persisted ExecutorStamp
	if json.Unmarshal([]byte(raw), &persisted) != nil || persisted.Responder.Harness != "cstyle" {
		t.Fatal("immutable stamp not persisted")
	}
	w.bob.runJob(tctx(t), j, &j.Executor.Responder, nil)
	if state, _ := w.bob.store.jobState(request); state != stateAnswered {
		t.Fatal(state)
	}
}

func TestNamedExecutorRefusalRollbackAndManualDefault(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	a, err := w.bob.CreateLocalAgent("A", Responder{Harness: "stub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(q dbq, id string) (*ExecutorStamp, error) { return w.bob.ResolveExecutorIn(q, id, nil) }
	legacy := namedExecutorRequest(t, w, "", envelope.KindQuestion, "")
	unknown := namedExecutorRequest(t, w, protocol.NewID(), envelope.KindQuestion, "")
	valid := namedExecutorRequest(t, w, a.ID, envelope.KindQuestion, "")
	// Deterministic arrival ordering: unknown before valid; legacy stays pending.
	for i, id := range []string{legacy, unknown, valid} {
		if _, err = w.bob.store.db.Exec(`UPDATE inbox SET received_at=? WHERE id=?`, i, id); err != nil {
			t.Fatal(err)
		}
	}
	j, ok, err := w.bob.store.claimJob("", resolve)
	if err != nil || !ok || j.ID != valid {
		t.Fatalf("no fallback/starvation: %s %v %v", j.ID, ok, err)
	}
	if state, _ := w.bob.store.jobState(legacy); state != statePending {
		t.Fatal("manual untargeted changed", state)
	}
	var detail string
	if err = w.bob.store.db.QueryRow(`SELECT detail FROM inbox WHERE id=?`, unknown).Scan(&detail); err != nil || detail != "not run: selected agent unavailable" {
		t.Fatal(detail, err)
	}
	if err = w.bob.SetLocalAgentResponder(a.ID, nil); err != nil {
		t.Fatal(err)
	}
	removed := namedExecutorRequest(t, w, a.ID, envelope.KindQuestion, "")
	foreign, err := w.alice.CreateLocalAgent("foreign", Responder{Harness: "stub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	foreignRequest := namedExecutorRequest(t, w, foreign.ID, envelope.KindQuestion, "")
	if _, ok, err = w.bob.store.claimJob("", resolve); err != nil || ok {
		t.Fatalf("unavailable selected: %v %v", ok, err)
	}
	for _, id := range []string{removed, foreignRequest} {
		if state, _ := w.bob.store.jobState(id); state != stateNotRun {
			t.Fatal(id, state)
		}
	}

	// Operational resolver failure must roll back claim/attempt/status; no text leaks.
	retry := namedExecutorRequest(t, w, protocol.NewID(), envelope.KindQuestion, "")
	sentinel := errors.New("private path operational failure")
	if _, _, err = w.bob.store.claimJob("", func(dbq, string) (*ExecutorStamp, error) { return nil, sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var attempts int
	var state string
	if err = w.bob.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE id=?`, retry).Scan(&state, &attempts); err != nil || state != statePending || attempts != 0 {
		t.Fatalf("rollback %s %d %v", state, attempts, err)
	}
	// Corrupt persisted catalog is operational, never permanent refusal.
	if err = w.bob.store.setConfig(map[string]string{agentCatalogConfig: "broken JSON"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = w.bob.store.claimJob("", resolve); err == nil || errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("corruption masked %v", err)
	}
	if state, _ := w.bob.store.jobState(retry); state != statePending {
		t.Fatal("corrupt catalog retired request", state)
	}

	// Naming an executor does not grant task permission.
	task := namedExecutorRequest(t, w, protocol.NewID(), envelope.KindTask, "")
	calls := 0
	if _, _, err = w.bob.store.claimJob("", func(dbq, string) (*ExecutorStamp, error) { calls++; return nil, sentinel }); err == nil {
		t.Fatal("pending retry must still surface resolver failure")
	}
	if state, _ := w.bob.store.jobState(task); state != stateAwaiting {
		t.Fatal("task permission bypass", state)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestNamedExecutorParticipationResolverRollback(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, stopBob := agentWorld(t)
	pid := participate(t, w, conv, nil, nil)
	stopBob()
	in := agentRequest(t, w, conv, pid, envelope.KindQuestion)
	if _, err := w.bob.store.addConvInbox(in, w.alice.Self().Fingerprint(), stateAgentWaiting, false, nil); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("private DB failure")
	_, ok, _, _, err := w.bob.store.claimAgentPage("agentstub", w.bob.Address, w.bob.Self().Fingerprint(), 0, agentPage, func(dbq, string) (*ExecutorStamp, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) || ok {
		t.Fatalf("resolver failure %v %v", ok, err)
	}
	if state := jobState(t, w.bob, in.ID); state != stateAgentWaiting {
		t.Fatal("page error retired request", state)
	}
	var attempts int
	if err = w.bob.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, in.ID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal(attempts, err)
	}
	_, ok, _, _, err = w.bob.store.claimAgentPage("agentstub", w.bob.Address, w.bob.Self().Fingerprint(), 0, agentPage, func(dbq, string) (*ExecutorStamp, error) { return nil, ErrUnknownAgent })
	if err != nil || ok || jobState(t, w.bob, in.ID) != stateNotRun {
		t.Fatalf("terminal refusal %v %v", ok, err)
	}
	var detail string
	if err = w.bob.store.db.QueryRow(`SELECT detail FROM inbox WHERE id=?`, in.ID).Scan(&detail); err != nil || detail != "not run: selected agent unavailable" {
		t.Fatal(detail, err)
	}
	if st.runs() != 0 {
		t.Fatal("resolver failure executed harness")
	}
}
