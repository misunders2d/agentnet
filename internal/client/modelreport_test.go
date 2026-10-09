package client

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func modelAt(t *testing.T, a *Agent, hostKey, agentID string) protocol.AgentModel {
	t.Helper()
	var raw string
	if err := a.store.db.QueryRow(`SELECT body FROM agent_models WHERE host_key=? AND agent_id=?`, hostKey, agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m protocol.AgentModel
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func seedModelRun(t *testing.T, a, from *Agent, r Responder, id string) job {
	t.Helper()
	recipient, _ := a.Self().Recipient()
	env, err := envelope.Seal(envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: from.Address, To: a.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: "owned report fixture"}, from.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	stamp := &ExecutorStamp{AgentID: id, Responder: r}
	raw, _ := json.Marshal(stamp)
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=?,agent_id=?,executor=? WHERE id=?`, stateRunning, id, string(raw), env.ID); err != nil {
		t.Fatal(err)
	}
	return job{ID: env.ID, AgentID: id, Executor: stamp}
}
func TestModelReportExactOwnedSnapshotAndQuietSync(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	r := Responder{Harness: "codex", Dir: t.TempDir(), Timeout: time.Minute}
	if err := w.alice.SetResponder(&r); err != nil {
		t.Fatal(err)
	}
	named, err := w.alice.CreateLocalAgent("Private fixture", r)
	if err != nil {
		t.Fatal(err)
	}
	fp := w.alice.Self().Fingerprint()
	for _, id := range []string{"", named.ID} {
		j := seedModelRun(t, w.alice, w.bob, r, id)
		if err = w.alice.recordModelReport(j, &r, "gpt-6.1-sol"); err != nil {
			t.Fatal(err)
		}
		previous := modelAt(t, w.alice, fp, id)
		seq, _ := w.alice.Changed()
		if err = w.alice.recordModelReport(j, &r, "gpt-6.1-sol"); err != nil {
			t.Fatal(err)
		}
		next := modelAt(t, w.alice, fp, id)
		after, _ := w.alice.Changed()
		if previous != next || after != seq {
			t.Fatal("identical model churned snapshot/time/revision or change stream")
		}
		mismatch := r
		mismatch.Dir = t.TempDir()
		if err = w.alice.recordModelReport(j, &mismatch, "unbound"); err != nil {
			t.Fatal(err)
		}
		if modelAt(t, w.alice, fp, id) != previous {
			t.Fatal("different executor captured")
		}
		receiver := j
		receiver.Receiver = &ReplyReceiverBinding{}
		if err = w.alice.recordModelReport(receiver, &r, "receiver-metadata"); err != nil {
			t.Fatal(err)
		}
		if modelAt(t, w.alice, fp, id) != previous {
			t.Fatal("receiver metadata captured")
		}
		if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, j.ID); err != nil {
			t.Fatal(err)
		}
		if err = w.alice.recordModelReport(j, &r, "late"); err != nil {
			t.Fatal(err)
		}
		if modelAt(t, w.alice, fp, id) != previous {
			t.Fatal("completed run captured metadata")
		}
	}
	reports, err := w.alice.ModelReports()
	if err != nil || len(reports) != 2 {
		t.Fatalf("local reports %+v %v", reports, err)
	}
	if _, err = w.alice.syncModelReports(); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND recipient=? LIMIT 1`, envelope.SubModelSync, phone.Address).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	before := count(t, w.alice, "outbox WHERE sub='model-sync'")
	if _, err = w.alice.syncModelReports(); err != nil {
		t.Fatal(err)
	}
	if count(t, w.alice, "outbox WHERE sub='model-sync'") != before {
		t.Fatal("unchanged report duplicated carrier")
	}
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(encoded), &env); err != nil {
		t.Fatal(err)
	}
	if env.Attn {
		t.Fatal("model metadata attention")
	}
	if err = phone.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	got, err := phone.ModelReports()
	if err != nil || len(got) != 2 {
		t.Fatalf("remote private reports %+v %v", got, err)
	}
	if count(t, phone, "inbox WHERE sub='model-sync'") != 0 {
		t.Fatal("metadata became runnable/displayed inbox turn")
	}
	old := modelAt(t, phone, fp, "")
	tx, err := phone.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	conflict := old
	conflict.Model = "different"
	if saveModelReport(tx, fp, conflict) == nil {
		t.Fatal("same revision conflict")
	}
	tx.Rollback()
	tx, _ = phone.store.db.Begin()
	older := old
	older.Revision--
	older.Model = "old"
	if err = saveModelReport(tx, fp, older); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if modelAt(t, phone, fp, "") != old {
		t.Fatal("older report won")
	}
	r.Dir = t.TempDir()
	if err = w.alice.SetResponder(&r); err != nil {
		t.Fatal(err)
	}
	reports, err = w.alice.ModelReports()
	if err != nil || len(reports) != 1 || reports[0].AgentID != named.ID {
		t.Fatalf("changed local executor remained reported %+v %v", reports, err)
	}
	// Remote reports remain historical facts; the receiving phone cannot infer host configuration.
	if reports, err = phone.ModelReports(); err != nil || len(reports) != 2 {
		t.Fatal("remote last known report lost", err)
	}
	foreign := protocol.ModelSync{V: 1, Person: phonePerson(t, phone).info.Person, Roster: phonePerson(t, phone).info.Roster, Reports: []protocol.AgentModel{old}}
	raw, _ := json.Marshal(foreign)
	forged := craft(t, w.bob, phone, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubModelSync, Replica: true, Body: string(raw)})
	if err = phone.verifyAndStore(tctx(t), forged); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, phone, forged.ID) == "" {
		t.Fatal("foreign report accepted")
	}
}
func phonePerson(t *testing.T, a *Agent) personRow {
	t.Helper()
	p, ok, e := a.store.selfPerson(a.Address)
	if e != nil || !ok {
		t.Fatal("self person", e)
	}
	return p
}

func TestModelReportOlderReaderFIFOAndRestart(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	me := phonePerson(t, w.alice)
	m := protocol.AgentModel{Model: "reported fixture", Harness: "codex", Executor: strings.Repeat("a", 64), At: 1, Revision: 1}
	tx, _ := w.alice.store.db.Begin()
	if e := saveModelReport(tx, w.alice.Self().Fingerprint(), m); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	if _, e := w.alice.syncModelReports(); e != nil {
		t.Fatal(e)
	}
	var encoded string
	w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND recipient=?`, envelope.SubModelSync, phone.Address).Scan(&encoded)
	var env envelope.Envelope
	json.Unmarshal([]byte(encoded), &env)
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	if e := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); e != nil {
		t.Fatal(e)
	}
	publish := func(ts int64, caps []string) {
		t.Helper()
		r := protocol.CapsRecord{Address: phone.Address, Session: profile.Sessions[0], TS: ts, Caps: caps}
		r.Sign(phone.id.Sign)
		if e := phone.hub.do(tctx(t), "PUT", "/v1/caps", r, nil); e != nil {
			t.Fatal(e)
		}
	}
	publish(time.Now().Unix()+100, []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapRoom})
	result, e := w.alice.deliver(tctx(t), env, nil)
	if e != nil || result.State != stateConvWaiting {
		t.Fatalf("old reader %+v %v", result, e)
	}
	sent, e := w.alice.SendMessage(tctx(t), Outgoing{To: phone.Address, Body: "real human turn after quiet report"})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.store.setOutboxState(sent.ID, stateQueued, "", ""); e != nil {
		t.Fatal(e)
	}
	if e = w.alice.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id=?`, sent.ID).Scan(&state); e != nil || state == stateQueued {
		t.Fatalf("quiet report blocked real turn %s %v", state, e)
	}
	if e = w.alice.Close(); e != nil {
		t.Fatal(e)
	}
	again, e := Open(w.alice.home)
	if e != nil {
		t.Fatal(e)
	}
	defer again.Close()
	if _, e = again.syncModelReports(); e != nil {
		t.Fatal(e)
	}
	if count(t, again, "outbox WHERE sub='model-sync'") != 1 {
		t.Fatal("restart report churn")
	}
	publish(time.Now().Unix()+200, slices.Clone(ownCaps))
	features, e := again.relayFeatures(tctx(t))
	if e != nil {
		t.Fatal(e)
	}
	again.releaseConv(tctx(t), features)
	if e = again.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	var unchanged string
	if e = again.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&unchanged); e != nil || unchanged != encoded {
		t.Fatal("restart resealed report", e)
	}
	if e = again.RemoveDevice(tctx(t), phone.Address); e != nil {
		t.Fatal(e)
	}
	again.store.setOutboxState(env.ID, stateQueued, "", "")
	if _, allowed, e := again.mayDeliverModelSync(env); e != nil || allowed {
		t.Fatalf("removed reader %v %v", allowed, e)
	}
	if e = readSyncAuthority(again.store.db, protocol.ReadSync{Person: me.info.Person, Roster: me.info.Roster}, again.Address, again.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); e == nil {
		t.Fatal("removed reader authority")
	}
}
func TestReportedModelTerminalMarker(t *testing.T) {
	for _, tt := range []struct{ body, answer, model string }{{"Answer\nAGENTNET-MODEL: gpt-6.1-sol", "Answer", "gpt-6.1-sol"}, {"AGENTNET-MODEL: gpt-6.1-sol", "", "gpt-6.1-sol"}, {"AGENTNET-MODEL: gpt-6.1-sol\nAnswer", "AGENTNET-MODEL: gpt-6.1-sol\nAnswer", ""}, {"Answer\nAGENTNET-MODEL: x\tbad", "Answer\nAGENTNET-MODEL: x\tbad", ""}} {
		answer, model := reportedModel(tt.body)
		if answer != tt.answer || model != tt.model {
			t.Fatalf("%q: %q %q", tt.body, answer, model)
		}
	}
}

func TestModelReportCurrentOwnerAndPinGuards(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	if e := phone.store.pin(w.alice.Self()); e != nil {
		t.Fatal(e)
	}
	own := phonePerson(t, phone)
	original, _ := json.Marshal(own.roster)
	m := protocol.AgentModel{Model: "known", Harness: "codex", Executor: strings.Repeat("a", 64), At: 1, Revision: 1}
	tx, _ := phone.store.db.Begin()
	if e := saveModelReport(tx, w.alice.Self().Fingerprint(), m); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	r := protocol.ModelSync{Person: own.info.Person, Roster: own.info.Roster}
	for _, mode := range []string{"agent-host-reader", "removed", "frozen", "pending-pin", "changed-pin"} {
		t.Run(mode, func(t *testing.T) {
			restore := func() {
				phone.store.db.Exec(`UPDATE persons SET record=?,state=? WHERE person=?`, string(original), personSelf, own.info.Person)
				phone.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address)
			}
			defer restore()
			roster := own.roster
			switch mode {
			case "agent-host-reader":
				roster.HumanKeys = []string{w.alice.Self().Fingerprint()}
			case "removed":
				roster.Devices = slices.DeleteFunc(slices.Clone(roster.Devices), func(d identity.Public) bool { return d.Address == w.alice.Address })
			case "frozen":
				phone.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, own.info.Person)
			case "pending-pin":
				phone.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address)
			case "changed-pin":
				var previous string
				phone.store.db.QueryRow(`SELECT public FROM peers WHERE address=?`, w.alice.Address).Scan(&previous)
				bad, _ := json.Marshal(w.bob.Self())
				phone.store.db.Exec(`UPDATE peers SET public=? WHERE address=?`, string(bad), w.alice.Address)
				defer phone.store.db.Exec(`UPDATE peers SET public=? WHERE address=?`, previous, w.alice.Address)
			}
			if mode == "agent-host-reader" || mode == "removed" {
				raw, _ := json.Marshal(roster)
				if _, e := phone.store.db.Exec(`UPDATE persons SET record=? WHERE person=?`, string(raw), own.info.Person); e != nil {
					t.Fatal(e)
				}
			}
			if e := modelSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); e == nil {
				t.Fatal("stale host authority accepted")
			}
			if reports, e := phone.ModelReports(); e != nil || len(reports) != 0 {
				t.Fatalf("stale host private reports %+v %v", reports, e)
			}
		})
	}
}
func TestModelReportEnrolledAgentHostReportsOnlyItself(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	host, awaited, _ := linkPhone(t, w.alice, "agent-server")
	if err := w.alice.ApproveAgentLink(tctx(t), pendingLink(t, w.alice).ID); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	for _, dev := range []identity.Public{w.alice.Self(), phone.Self()} {
		if err := host.store.pin(dev); err != nil {
			t.Fatal(err)
		}
	}
	own := phonePerson(t, host)
	if own.roster.Human(host.Self().Fingerprint()) {
		t.Fatal("fixture did not enroll an agent host")
	}
	m := protocol.AgentModel{Model: "native host model", Harness: "codex", Executor: strings.Repeat("a", 64), At: 1, Revision: 1}
	tx, err := host.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{host.Self().Fingerprint(), w.bob.Self().Fingerprint()} {
		if err = saveModelReport(tx, fp, m); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = host.syncModelReports(); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err = host.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND recipient=?`, envelope.SubModelSync, phone.Address).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(encoded), &env); err != nil {
		t.Fatal(err)
	}
	if _, allowed, e := host.mayDeliverModelSync(env); e != nil || !allowed {
		t.Fatalf("enrolled host delivery %v %v", allowed, e)
	}
	if err = phone.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	got, err := phone.ModelReports()
	if err != nil || len(got) != 1 || got[0].HostKey != host.Self().Fingerprint() || got[0].Host != host.Address || got[0].Model != m.Model {
		t.Fatalf("exact agent-host report %+v %v", got, err)
	}
	if n := count(t, phone, "agent_models WHERE host_key='"+w.bob.Self().Fingerprint()+"'"); n != 0 {
		t.Fatal("host forwarded another host report")
	}
	if got, err = host.ModelReports(); err != nil || len(got) != 0 {
		t.Fatalf("agent host private reader %+v %v", got, err)
	}
	r := protocol.ModelSync{V: 1, Person: own.info.Person, Roster: own.info.Roster, Reports: []protocol.AgentModel{m}}
	raw, _ := json.Marshal(r)
	denied := craft(t, w.alice, host, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubModelSync, Replica: true, Body: string(raw)})
	if err = host.verifyAndStore(tctx(t), denied); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, host, denied.ID) == "" {
		t.Fatal("agent-host reader accepted")
	}
	if err = readSyncAuthority(host.store.db, protocol.ReadSync{Person: own.info.Person, Roster: own.info.Roster}, host.Address, host.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("metadata authorization broadened read-state authority")
	}
}

func TestModelReportWorkerTerminalMetadata(t *testing.T) {
	for _, mode := range []string{"answer", "metadata-only", "failed"} {
		t.Run(mode, func(t *testing.T) {
			st := installStub(t, "answer")
			bin := Harnesses["stub"].bin
			script := "#!/bin/sh\ncat > \"$STUB_LOG.stdin\"\n"
			switch mode {
			case "answer":
				script += "printf 'ordinary answer\\nAGENTNET-MODEL: gpt-6.1-sol\\n'\n"
			case "metadata-only":
				script += "printf 'AGENTNET-MODEL: gpt-6.1-sol\\n'\n"
			case "failed":
				script += "printf 'failed answer\\nAGENTNET-MODEL: gpt-6.1-sol\\n'\nexit 1\n"
			}
			if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			w := newWorld(t, "")
			setResponder(t, w.bob, "stub", st.dir, time.Minute)
			runWith(t, w, w.bob, RunOptions{})
			runAgent(t, w.alice)
			if e := w.bob.Approve(w.alice.Address); e != nil {
				t.Fatal(e)
			}
			q, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "fixture question"})
			if e != nil {
				t.Fatal(e)
			}
			var answer Message
			eventually(t, "owned model answer", func() bool { var ok bool; answer, ok = findReply(w.alice, q.ID); return ok })
			n := count(t, w.bob, "agent_models")
			if mode == "answer" {
				if n != 1 || answer.Body != "ordinary answer" || answer.Status != envelope.StatusDone {
					t.Fatalf("metadata was not privately stripped %+v reports=%d", answer, n)
				}
			} else if n != 0 || answer.Status != envelope.StatusFailed || strings.Contains(answer.Body, "AGENTNET-MODEL") {
				t.Fatalf("invalid success or metadata leak %+v reports=%d", answer, n)
			}
			prompt, _ := os.ReadFile(st.log + ".stdin")
			if !strings.Contains(string(prompt), modelReportPrompt) {
				t.Fatal("owned run did not receive optional self-report prompt")
			}
		})
	}
}
