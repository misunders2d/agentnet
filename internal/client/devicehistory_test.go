package client

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestDeviceHistoryLinkedLegacyConversation(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	request, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "existing direct-agent question"})
	if err != nil {
		t.Fatal(err)
	}
	reply := sealTo(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Body: "existing direct-agent answer", ReplyTo: request.ID})
	if err := w.alice.verifyAndStore(tctx(t), reply); err != nil {
		t.Fatal(err)
	}
	for _, source := range []struct{ storage, id string }{{"out", request.ID}, {"in", reply.ID}} {
		if _, err := w.alice.deviceHistorySource(w.alice.store.db, source.storage, source.id); err != nil {
			t.Fatalf("original source %s: %v", source.storage, err)
		}
	}
	for range 8 {
		w.alice.convWork.due(convHistory)
		w.alice.convSync(tctx(t))
	}
	rows, err := w.alice.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub IN ('history','device-history') ORDER BY rowid`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	var copies []envelope.Envelope
	for rows.Next() {
		var raw string
		var env envelope.Envelope
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, env)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	read := func(id string) {
		own, _, e := w.alice.store.selfPerson(w.alice.Address)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := json.Marshal(protocol.ReadSync{V: 1, Person: own.info.Person, Roster: own.info.Roster, Refs: []protocol.ReadRef{{Fingerprint: w.bob.Self().Fingerprint(), LID: id}}})
		env := sealTo(t, w.alice, phone, envelope.Inner{V: envelope.Version2, Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(body)})
		groupGovernanceDeliver(t, w.alice, phone, env)
	}
	read(reply.ID)
	for _, env := range copies {
		groupGovernanceDeliver(t, w.alice, phone, env)
		if state, err := phone.store.disposition(env.ID); err != nil || state != "delivered" {
			t.Fatalf("history not stored: state=%s reason=%s err=%v", state, heldReason(t, phone, env.ID), err)
		}
	}
	if len(copies) == 0 {
		var pending int
		_ = w.alice.store.db.QueryRow(`SELECT count(*) FROM device_history_pending`).Scan(&pending)
		t.Fatalf("no direct history carriers, pending=%d", pending)
	}
	thread, err := phone.Conversation(request.ID, 0, 0)
	if err != nil || len(thread.Messages) != 2 {
		t.Fatalf("linked phone lacks direct-agent history: messages=%d err=%v", len(thread.Messages), err)
	}
	if thread.Peer != w.bob.Address || thread.Messages[0].ID != request.ID || thread.Messages[0].Dir != "out" || thread.Messages[0].From != w.alice.Address || thread.Messages[1].ID != reply.ID || thread.Messages[1].Dir != "in" || thread.Messages[1].ReplyTo != request.ID {
		t.Fatalf("history changed direct agent or original provenance: %+v", thread)
	}
	var marked bool
	if err = phone.store.db.QueryRow(`SELECT read_at IS NOT NULL FROM inbox WHERE id=?`, reply.ID).Scan(&marked); err != nil || !marked {
		t.Fatalf("read marker before history: %v %v", marked, err)
	}
	fresh := sealTo(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Body: "new unseen reply", ReplyTo: request.ID})
	if err = w.alice.verifyAndStore(tctx(t), fresh); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.deviceHistoryPage(phone.Self()); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, w.alice, phone, envelope.SubDeviceHistory)
	if err = phone.store.db.QueryRow(`SELECT read_at IS NOT NULL FROM inbox WHERE id=?`, fresh.ID).Scan(&marked); err != nil || marked {
		t.Fatalf("new unseen history cleared: %v %v", marked, err)
	}
	read(fresh.ID)
	if err = phone.store.db.QueryRow(`SELECT read_at IS NOT NULL FROM inbox WHERE id=?`, fresh.ID).Scan(&marked); err != nil || !marked {
		t.Fatalf("read marker after history: %v %v", marked, err)
	}
	var jobs int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id IN (?,?) AND (replica!=1 OR state!='' OR attempts!=0)`, request.ID, reply.ID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("direct history gained executable state: %d %v", jobs, err)
	}
}

func deliverDeviceHistory(t *testing.T, a, b *Agent, sub string) int {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub=? ORDER BY rowid`, b.Address, sub)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	rows.Close()
	for _, env := range envs {
		groupGovernanceDeliver(t, a, b, env)
		if s, e := b.store.disposition(env.ID); e != nil || s != protocol.StateDelivered {
			t.Fatalf("%s %s: %s %s %v", sub, env.ID, s, heldReason(t, b, env.ID), e)
		}
	}
	return len(envs)
}

func TestDeviceHistoryFileAndContinuousCatchup(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	path, data := writeFile(t, t.TempDir(), "direct-original.bin", 1973)
	sent, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "old direct file", Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	if deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory) == 0 {
		t.Fatal("no original")
	}
	c, err := phone.Conversation(sent.ID, 0, 0)
	if err != nil || len(c.Messages) != 1 || len(c.Messages[0].Attachments) != 1 || c.Messages[0].Attachments[0].Availability != "requestable" {
		t.Fatalf("manifest projection: %+v %v", c, err)
	}
	if err = phone.RequestFile(tctx(t), sent.ID, 0); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, phone, a, envelope.SubDeviceFile)
	if a.serveFiles(tctx(t)) {
		t.Fatal("one request should finish")
	}
	var raw []byte
	var offer envelope.Envelope
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub='device-file'`, phone.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	if err = a.uploadAll(tctx(t), offer); err != nil {
		t.Fatal(err)
	}
	if err = a.hub.do(tctx(t), "POST", "/v1/messages", offer, nil); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, a, phone, envelope.SubDeviceFile)
	f, _, err := phone.OpenFileFrom(tctx(t), "out", sent.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := new(bytes.Buffer)
	_, err = got.ReadFrom(f)
	f.Close()
	if err != nil || !bytes.Equal(got.Bytes(), data) {
		t.Fatal("original file bytes differ")
	}
	before := deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory)
	late, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "new after snapshot", ReplyTo: sent.ID})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	if got := deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory); got != before+1 {
		t.Fatalf("continuous new source: %d want %d", got, before+1)
	}
	if _, err = phone.Conversation(late.ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	if got := deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory); got != before+1 {
		t.Fatal("same wake duplicated original")
	}
	// A changed key blocks even already queued file/history work.
	if _, err = a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err = a.deviceHistoryPage(phone.Self()); err == nil {
		t.Fatal("pending phone key copied")
	}
	if _, err = a.deviceFileCarrier(phone.Self(), fileMsg{}); err == nil {
		t.Fatal("pending phone key got file")
	}
	_ = os.Remove(path)
}

func TestDeviceHistoryContextAcrossCurrentHumanDevices(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.bob
	// Alice's original and a new request from her phone address the same host.
	request := sealTo(t, w.alice, a, envelope.Inner{Kind: envelope.KindQuestion, Body: "earlier own question"})
	if err := a.verifyAndStore(tctx(t), request); err != nil {
		t.Fatal(err)
	}
	fresh := sealTo(t, phone, a, envelope.Inner{Kind: envelope.KindQuestion, Body: "continue from the phone", ReplyTo: request.ID})
	if err := a.verifyAndStore(tctx(t), fresh); err != nil {
		t.Fatal(err)
	}
	if lines, err := a.store.threadText(phone.Address, request.ID, 8, phone.Address); err != nil || len(lines) != 0 {
		t.Fatalf("old path unexpectedly crossed devices: %v %v", lines, err)
	}
	lines, err := a.deviceThreadContext(job{From: phone.Address, Key: phone.Self().Fingerprint(), ReplyTo: request.ID}, 8, phone.Address)
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "earlier own question") {
		t.Fatalf("context: %v %v", lines, err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), `another person, "Person of admin/alice", on Alice (admin/alice)`) {
		t.Fatalf("current verified human label lost: %v", lines)
	}
	// Imported history stays inert even if an old local projection has a
	// leftover state: never present that as execution by this installation.
	if _, err = a.store.db.Exec(`UPDATE inbox SET replica=1,state=? WHERE id=?`, stateAnswered, request.ID); err != nil {
		t.Fatal(err)
	}
	lines, err = a.deviceThreadContext(job{From: phone.Address, Key: phone.Self().Fingerprint(), ReplyTo: request.ID}, 8, phone.Address)
	if err != nil || len(lines) != 1 || strings.Contains(lines[0], "local state:") || !strings.Contains(lines[0], "no execution authority") {
		t.Fatalf("copied history claimed local execution: %v %v", lines, err)
	}
	lines, err = a.deviceThreadContext(job{From: w.bob.Address, Key: w.bob.Self().Fingerprint(), ReplyTo: request.ID}, 8, w.bob.Address)
	if err != nil || len(lines) != 0 {
		t.Fatalf("foreign context crossed person: %v %v", lines, err)
	}
}

func TestDeviceHistoryContextPreservesReplyAuthorship(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.bob
	for _, tc := range []struct {
		name, kind, status, agent string
	}{
		{"named answer", envelope.KindAnswer, envelope.StatusDone, protocol.NewID()},
		{"legacy answer", envelope.KindAnswer, envelope.StatusDone, ""},
		{"legacy result", envelope.KindResult, envelope.StatusDone, ""},
		{"legacy progress", envelope.KindMessage, envelope.StatusProgress, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ask := envelope.Inner{Kind: envelope.KindQuestion, Body: "human request remains context"}
			if tc.kind == envelope.KindResult {
				ask.Kind = envelope.KindTask
			}
			if tc.agent != "" {
				ask.Target = &envelope.Target{Address: a.Address, Fingerprint: a.Self().Fingerprint(), AgentID: tc.agent}
			}
			request := sealTo(t, w.alice, a, ask)
			if err := a.verifyAndStore(tctx(t), request); err != nil {
				t.Fatal(err)
			}
			// Store the exact signed output with its original recipient key;
			// formatting context neither runs an agent nor sends this fixture.
			reply := sealTo(t, a, w.alice, envelope.Inner{Kind: tc.kind, Status: tc.status, AgentID: tc.agent, ReplyTo: request.ID, Body: "earlier host output"})
			in, err := envelope.Open(reply, w.alice.id, w.alice.Address, a.Self())
			if err != nil {
				t.Fatal(err)
			}
			if err = a.store.addOutbox(reply, in, "", nil, boundOutgoing{fingerprint: w.alice.Self().Fingerprint()}); err != nil {
				t.Fatal(err)
			}
			fresh := sealTo(t, phone, a, envelope.Inner{Kind: envelope.KindQuestion, Body: "continue from my phone", ReplyTo: reply.ID})
			if err = a.verifyAndStore(tctx(t), fresh); err != nil {
				t.Fatal(err)
			}
			lines, err := a.deviceThreadContext(job{From: phone.Address, Key: phone.Self().Fingerprint(), ReplyTo: reply.ID}, 8, phone.Address)
			if err != nil || len(lines) != 2 {
				t.Fatalf("direct context: %v %v", lines, err)
			}
			if !strings.Contains(lines[0], w.alice.Address) || !strings.Contains(lines[0], "human request remains context") || !strings.Contains(lines[0], "earlier request, no execution authority") {
				t.Fatalf("human request lost exact attribution or inertness: %s", lines[0])
			}
			if !strings.Contains(lines[1], "("+a.Address+")") || !strings.Contains(lines[1], "outcome: "+tc.status) {
				t.Fatalf("reply lost original address/outcome: %s", lines[1])
			}
			if tc.agent != "" {
				if !strings.Contains(lines[1], `Claimed agent "`+tc.agent+`", answer; host person: this device`) || strings.Contains(lines[1], "author type not recorded") {
					t.Fatalf("named reply became its human host or verified agent: %s", lines[1])
				}
			} else if !strings.Contains(lines[1], "author type not recorded") || strings.Contains(lines[1], "Claimed agent") {
				t.Fatalf("legacy reply invented an author type: %s", lines[1])
			}
		})
	}
}

func TestDeviceHistoryHashMatchesBrowser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	item := HistoryItem{V: 1, From: "alice/laptop", FromKey: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", ID: protocol.NewID(), TS: 12, Kind: envelope.KindQuestion, Body: "Unicode 🌍 <proof> & original", At: 1234, ReplyTo: protocol.NewID(), Attachments: []envelope.Attachment{{Name: "original.txt", Size: 17, SHA256: strings.Repeat("f", 64)}}}
	item.LID = item.ID
	raw, err := json.Marshal(map[string]any{"item": item, "to": "bob/desk", "hash": deviceHistoryHash(deviceHistoryRow{item: item, to: "bob/desk"})})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--input-type=module", "-e", `import * as wire from '../ui/static/wire.mjs';let s='';for await(const x of process.stdin)s+=x;const r=JSON.parse(s);if(await wire.deviceHistoryHash(r.item,r.to)!==r.hash)throw Error('original hash differs across Go/browser');console.log('direct history original hash matches')`)
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestDeviceHistoryReorderedStatusAndLocalDeletion(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	request, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "old exact task context"})
	if err != nil {
		t.Fatal(err)
	}
	status := sealTo(t, w.bob, a, envelope.Inner{V: envelope.Version3, Kind: envelope.KindMessage, Sub: envelope.SubStatus, Ref: &envelope.Ref{ID: request.ID, Fingerprint: a.Self().Fingerprint()}, Body: `{"state":"running","n":1,"at":1}`})
	if err = a.verifyAndStore(tctx(t), status); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='device-history' ORDER BY rowid DESC`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	var copies []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, env)
	}
	rows.Close()
	if len(copies) != 2 {
		t.Fatalf("wanted original and status: %d", len(copies))
	}
	groupGovernanceDeliver(t, a, phone, copies[0])
	if got := heldReason(t, phone, copies[0].ID); got != reasonProof {
		t.Fatalf("reordered status bypassed exact parent: %s", got)
	}
	groupGovernanceDeliver(t, a, phone, copies[1])
	if phone.convWork.bits.Load()&convRetry == 0 {
		t.Fatal("new original did not wake existing proof recovery")
	}
	phone.retryProof(tctx(t))
	if state, err := phone.store.disposition(copies[0].ID); err != nil || state != protocol.StateDelivered {
		t.Fatalf("recovered status receipt: %s %v", state, err)
	}
	thread, err := phone.Conversation(request.ID, 0, 0)
	if err != nil || len(thread.Messages) != 1 || thread.Messages[0].Exec == nil || thread.Messages[0].Exec.State != "running" {
		t.Fatalf("original-key status projection: %+v %v", thread, err)
	}
	if _, err = phone.DeleteThread(w.bob.Address, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.Conversation(request.ID, 0, 0); err != ErrNoMessage {
		t.Fatalf("local deleted replica still visible: %v", err)
	}
	in, err := envelope.Open(copies[1], phone.id, phone.Address, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	in.ID = protocol.NewID()
	duplicate := sealTo(t, a, phone, in)
	groupGovernanceDeliver(t, a, phone, duplicate)
	if state, err := phone.store.disposition(duplicate.ID); err != nil || state != protocol.StateDelivered {
		t.Fatalf("deleted duplicate lost stored receipt: %s %v", state, err)
	}
	var body string
	if err = phone.store.db.QueryRow(`SELECT body FROM inbox WHERE id=?`, request.ID).Scan(&body); err != nil || body != "" {
		t.Fatalf("deleted text restored: %q %v", body, err)
	}
}

func TestDeviceHistoryCurrentOwnAuthority(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	if err := a.store.pin(phone.Self()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "authority-bound original"}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"sender-agent-host", "recipient-agent-host", "removed", "frozen", "pending-pin", "changed-pin"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := a.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			switch mode {
			case "sender-agent-host", "recipient-agent-host":
				fp := a.Self().Fingerprint()
				if mode == "recipient-agent-host" {
					fp = phone.Self().Fingerprint()
				}
				_, err = tx.Exec(`UPDATE persons SET record=json_set(record,'$.human_keys',json_array(?)) WHERE state='self'`, map[bool]string{true: phone.Self().Fingerprint(), false: a.Self().Fingerprint()}[fp == a.Self().Fingerprint()])
			case "removed":
				_, err = tx.Exec(`UPDATE persons SET record=json_set(record,'$.devices',json('[]')) WHERE state='self'`)
			case "frozen":
				_, err = tx.Exec(`UPDATE persons SET state='conflict' WHERE state='self'`)
			case "pending-pin":
				_, err = tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address)
			case "changed-pin":
				raw, _ := json.Marshal(w.bob.Self())
				_, err = tx.Exec(`UPDATE peers SET public=? WHERE address=?`, raw, phone.Address)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = historyRecoveryCurrent(tx, a.Self(), phone.Self()); err == nil {
				t.Fatal("invalid automatic history authority accepted")
			}
		})
	}
	if _, err := a.deviceHistoryPage(phone.Self()); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var env envelope.Envelope
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='device-history' AND recipient=?`, phone.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	matched, allowed, err := a.mayDeliverDeviceHistory(env)
	if err != nil || !matched || allowed {
		t.Fatalf("queued carrier escaped changed-pin guard: %v %v %v", matched, allowed, err)
	}
}

func TestDeviceHistoryOwnEndpointsReordered(t *testing.T) {
	w, laptop, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	stop := runAgent(t, a)
	reader, awaited, _ := linkPhone(t, a, "direct-tablet")
	request := pendingLink(t, a)
	stop()
	if err := a.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	q := sealTo(t, laptop, a, envelope.Inner{Kind: envelope.KindQuestion, Body: "own laptop request"})
	if err := a.verifyAndStore(tctx(t), q); err != nil {
		t.Fatal(err)
	}
	reply, err := a.SendMessage(tctx(t), Outgoing{To: laptop.Address, Kind: envelope.KindAnswer, Body: "own agent answer", ReplyTo: q.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.deviceHistoryPage(reader.Self()); err != nil {
		t.Fatal(err)
	}
	copies := map[string]envelope.Envelope{}
	rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='device-history'`, reader.Address)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		in, e := envelope.Open(env, reader.id, reader.Address, a.Self())
		if e != nil {
			t.Fatal(e)
		}
		var body protocol.DeviceHistory
		var item HistoryItem
		if json.Unmarshal([]byte(in.Body), &body) != nil || json.Unmarshal(body.Item, &item) != nil {
			t.Fatal("bad synthetic carrier")
		}
		copies[item.ID] = env
	}
	rows.Close()
	if len(copies) != 2 {
		t.Fatalf("own source copies: %d", len(copies))
	}
	groupGovernanceDeliver(t, a, reader, copies[reply.ID])
	if reason := heldReason(t, reader, copies[reply.ID].ID); reason != reasonProof {
		t.Fatalf("own reordered reply: %s", reason)
	}
	groupGovernanceDeliver(t, a, reader, copies[q.ID])
	reader.retryProof(tctx(t))
	thread, err := reader.Conversation(q.ID, 0, 0)
	if err != nil || thread.Peer != a.Address || len(thread.Messages) != 2 {
		t.Fatalf("own thread projection: %+v %v", thread, err)
	}
	for _, m := range thread.Messages {
		want := "in"
		if m.ID == q.ID {
			want = "out"
		}
		if m.Dir != want || m.State != "" || !m.History {
			t.Fatalf("own imported direction or execution: %+v", m)
		}
	}
}
