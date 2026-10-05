package client

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func groupReceiverReply(t *testing.T, from, to *Agent, conv, ref string, files ...OutgoingFile) (string, envelope.Envelope) {
	t.Helper()
	sent, err := from.SendConv(tctx(t), conv, ConvOutgoing{Body: "verified return data", ReplyTo: ref, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = from.store.db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND lid=? AND recipient=?`, conv, sent.LID, to.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	groupGovernanceDeliver(t, from, to, env)
	return env.ID, env
}

func groupReceiverBound(t *testing.T, a *Agent, ref string) ReplyReceiverBinding {
	t.Helper()
	for _, b := range receiverBindings(t, a) {
		if b.RequestRef == ref {
			return b
		}
	}
	t.Fatal("missing exact group receiver binding")
	return ReplyReceiverBinding{}
}

func TestGroupReplyReceiverOrdinaryManagedFilesOnce(t *testing.T) {
	stub := installAgentStub(t)
	w, _, p, _ := groupTurnsFixture(t)
	local, err := w.alice.CreateLocalAgent("chosen local continuation", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	r := &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "Continue original local review using only its selected return data.", Mode: envelope.KindQuestion}
	sent, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "original local group request", ReplyReceiver: r})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "original group request at Bob", func() bool { return len(groupTurns(t, w.bob, p.State.Conv)) == 1 })
	path := filepath.Join(t.TempDir(), "return.txt")
	if err = os.WriteFile(path, []byte("GROUP_RECEIVER_FILE_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	id, env := groupReceiverReply(t, w.bob, w.alice, p.State.Conv, sent.LID, OutgoingFile{Path: path})
	t.Cleanup(func() {
		var state, detail string
		w.alice.store.db.QueryRow(`SELECT state,coalesce(detail,'') FROM inbox WHERE id=?`, id).Scan(&state, &detail)
		t.Logf("return state=%s detail=%s runs=%d", state, detail, stub.runs())
	})
	waitState(t, w.alice, id, stateContinued)
	groupGovernanceDeliver(t, w.bob, w.alice, env)
	b := groupReceiverBound(t, w.alice, sent.LID)
	if len(b.Inputs) != 1 || b.Inputs[0].State != "completed" || stub.runs() != 1 {
		t.Fatalf("logical input ran more than once: %+v runs%d", b, stub.runs())
	}
	if !strings.Contains(stub.last(), r.Instructions) || !strings.Contains(stub.last(), "original local group request") || !strings.Contains(stub.last(), "Verified attachment") {
		t.Fatal("continuation lost original authority or verified file")
	}
	var runs int
	if err = w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, id).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("attempts%d %v", runs, err)
	}
	if _, ok, e := w.alice.store.claimJob("agentstub"); e != nil || ok {
		t.Fatalf("default stole group return: %v %v", ok, e)
	}
	if _, e := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "unbound human group chatter"}); e != nil {
		t.Fatal(e)
	}
	eventually(t, "unbound human chatter visible", func() bool {
		for _, v := range groupTurns(t, w.alice, p.State.Conv) {
			if v.Body == "unbound human group chatter" {
				return true
			}
		}
		return false
	})
	if _, ok, e := w.alice.claimReplyReceiverJob(); e != nil || ok {
		t.Fatalf("unbound group message claimed selected work %v %v", ok, e)
	}
	if _, ok, e := w.alice.store.claimJob("agentstub"); e != nil || ok {
		t.Fatalf("unbound group chatter created default work %v %v", ok, e)
	}
	if stub.runs() != 1 {
		t.Fatal("unbound chatter ran model")
	}
	f, _, e := w.alice.OpenFileFrom(tctx(t), "in", id, 0)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(f)
	f.Close()
	if e != nil || string(data) != "GROUP_RECEIVER_FILE_BYTES" {
		t.Fatalf("selected return bytes %q %v", data, e)
	}
}

func TestGroupReplyReceiverMemberAndVisitorPIDNative(t *testing.T) {
	for _, visitor := range []bool{false, true} {
		t.Run(map[bool]string{false: "member", true: "visitor"}[visitor], func(t *testing.T) {
			stub := installAgentStub(t)
			w, _, p, stops := groupTurnsFixture(t)
			host := w.bob
			if visitor {
				host = proofReader(t, w, "receiver-visitor")
				runAgent(t, host)
				publishGroupFixtureCaps(t, host, true)
			}
			fakeNotify(host)
			remote, e := host.CreateLocalAgent("remote named executor", Responder{Harness: "agentstub", Dir: stub.dir})
			if e != nil {
				t.Fatal(e)
			}
			if e = host.PublishAgentCatalog(tctx(t)); e != nil {
				t.Fatal(e)
			}
			part, e := w.alice.InviteNamedAgent(tctx(t), p.State.Conv, host.Address, remote.ID, nil, nil, "selected local receiver test")
			if e != nil {
				t.Fatal(e)
			}
			eventually(t, "exact host invitation", func() bool { v, e := host.Participation(part.PID); return e == nil && v.State == PartInvited })
			if _, e = host.AcceptParticipation(tctx(t), part.PID); e != nil {
				t.Fatal(e)
			}
			eventually(t, "active selected participation", func() bool { v, e := w.alice.Participation(part.PID); return e == nil && v.Claimable() })
			if e = host.Approve(w.alice.Address); e != nil {
				t.Fatal(e)
			}
			owner, call := nativeReceiverFixture(t, w.alice, "pi")
			sent, e := w.alice.AskAgentWithReceiver(tctx(t), part.PID, envelope.KindQuestion, "original exact PID request", &ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle})
			if e != nil {
				t.Fatal(e)
			}
			answer := replyAt(t, w.alice, p.State.Conv, sent.ID)
			b := groupReceiverBound(t, w.alice, sent.LID)
			if len(b.Inputs) != 1 || stub.runs() != 1 {
				t.Fatalf("PID output correlation %+v runs%d", b, stub.runs())
			}
			d, e := w.alice.TakeReplyReceiverInput(call)
			if e != nil || d == nil || d.InputID != answer.ID {
				t.Fatalf("native group take %+v %v", d, e)
			}
			marker := map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "details": map[string]string{"handle": owner.Handle}}
			nativeWrite(t, call.File, call.SessionID, marker, nativeEntry(d, "accepted", "marker"))
			ack := ReplyReceiverAck{ReplySessionCall: call, BindingID: d.BindingID, InputID: d.InputID, ClaimID: d.ClaimID, InputToken: d.InputToken}
			ack.Leaf = "accepted"
			for range 2 {
				if ok, e := w.alice.AckReplyReceiverInput(ack); e != nil || !ok {
					t.Fatalf("exact native ACK %v %v", ok, e)
				}
			}
			call.Leaf = "accepted"
			if next, e := w.alice.TakeReplyReceiverInput(call); e != nil || next != nil {
				t.Fatalf("duplicate native delivery %+v %v", next, e)
			}
			if _, ok, e := w.alice.store.claimJob("agentstub"); e != nil || ok {
				t.Fatalf("default stole PID return %v %v", ok, e)
			}
			local, e := w.alice.CreateLocalAgent("chosen local receiver", Responder{Harness: "agentstub", Dir: stub.dir})
			if e != nil {
				t.Fatal(e)
			}
			managed, e := w.alice.AskAgentWithReceiver(tctx(t), part.PID, envelope.KindQuestion, "second exact PID request", &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "LOCAL GROUP PID CONTINUATION", Mode: envelope.KindQuestion})
			if e != nil {
				t.Fatal(e)
			}
			returned := replyAt(t, w.alice, p.State.Conv, managed.ID)
			waitState(t, w.alice, returned.ID, stateContinued)
			if stub.runs() != 3 || !strings.Contains(stub.last(), "LOCAL GROUP PID CONTINUATION") {
				t.Fatalf("chosen managed PID did not continue: runs%d", stub.runs())
			}
			// Persist an unclaimed output, then revoke its exact participation.
			stops[w.alice]()
			pending, e := w.alice.AskAgentWithReceiver(tctx(t), part.PID, envelope.KindQuestion, "pending exact PID request", &ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle})
			if e != nil {
				t.Fatal(e)
			}
			var raw []byte
			eventually(t, "pending PID output at host", func() bool {
				return host.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND reply_to IN (?,?) AND kind=?`, w.alice.Address, pending.ID, pending.LID, envelope.KindAnswer).Scan(&raw) == nil
			})
			var env envelope.Envelope
			if e = json.Unmarshal(raw, &env); e != nil {
				t.Fatal(e)
			}
			groupGovernanceDeliver(t, host, w.alice, env)
			if _, e = w.alice.DismissParticipation(tctx(t), part.PID); e != nil {
				t.Fatal(e)
			}
			if next, e := w.alice.TakeReplyReceiverInput(call); e == nil || next != nil {
				t.Fatalf("dismissed PID taken %+v %v", next, e)
			}
			if _, ok, e := w.alice.store.claimJob("agentstub"); e != nil || ok {
				t.Fatalf("dismissed PID default takeover %v %v", ok, e)
			}
		})
	}
}

func TestGroupReplyReceiverCurrentAdmissionClaimFences(t *testing.T) {
	for _, change := range []string{"remove", "rejoin", "withdraw"} {
		t.Run(change, func(t *testing.T) {
			stub := installAgentStub(t)
			w, _, p, stops := groupTurnsFixture(t)
			stops[w.alice]()
			local, e := w.alice.CreateLocalAgent("selected", Responder{Harness: "agentstub", Dir: stub.dir})
			if e != nil {
				t.Fatal(e)
			}
			owner, call := nativeReceiverFixture(t, w.alice, "pi")
			refs := []string{}
			for _, r := range []*ReplyReceiver{{Kind: "managed_agent", AgentID: local.ID, Instructions: "original local review", Mode: envelope.KindQuestion}, {Kind: "live_session", SessionHandle: owner.Handle}} {
				sent, e := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "original epoch request", ReplyReceiver: r})
				if e != nil {
					t.Fatal(e)
				}
				refs = append(refs, sent.LID)
				eventually(t, "epoch request at Bob", func() bool {
					for _, v := range groupTurns(t, w.bob, p.State.Conv) {
						if v.LID == sent.LID {
							return true
						}
					}
					return false
				})
				groupReceiverReply(t, w.bob, w.alice, p.State.Conv, sent.LID)
			}
			if change == "withdraw" {
				if _, e = w.bob.LeaveGroup(tctx(t), p.State.Conv); e != nil {
					t.Fatal(e)
				}
				groupGovernanceDeliver(t, w.bob, w.alice, groupGovernanceDeparture(t, w.bob, w.alice))
			} else {
				person, _, _ := w.bob.store.selfPerson(w.bob.Address)
				next, e := w.alice.RemoveGroupMember(tctx(t), p.State.Conv, person.roster.Person)
				if e != nil {
					t.Fatal(e)
				}
				if change == "rejoin" {
					next = groupInteractionRejoin(t, w.alice, w.bob, next)
					if e = w.alice.FlushOutbox(tctx(t)); e != nil {
						t.Fatal(e)
					}
					groupGovernanceAwait(t, next, w.bob)
				}
			}
			if _, ok, e := w.alice.claimReplyReceiverJob(); e != nil || ok {
				t.Fatalf("obsolete epoch claimed managed work %v %v", ok, e)
			}
			if d, e := w.alice.TakeReplyReceiverInput(call); e == nil || d != nil {
				t.Fatalf("obsolete epoch taken %+v %v", d, e)
			}
			if _, ok, e := w.alice.store.claimJob("agentstub"); e != nil || ok {
				t.Fatalf("obsolete epoch default takeover %v %v", ok, e)
			}
			b := groupReceiverBound(t, w.alice, refs[0])
			if len(b.Inputs) != 1 || b.Inputs[0].State != "pending" {
				t.Fatalf("refusal lost durable input %+v", b)
			}
			if stub.runs() != 0 {
				t.Fatal("refused local continuation ran")
			}
			if change == "rejoin" {
				late, _ := groupReceiverReply(t, w.bob, w.alice, p.State.Conv, refs[0])
				state, e := w.alice.store.jobState(late)
				if e != nil || state != stateNotRun {
					t.Fatalf("rejoined source revived selected old request: %s %v", state, e)
				}
				b = groupReceiverBound(t, w.alice, refs[0])
				if len(b.Inputs) != 2 {
					t.Fatalf("late refused input lost default fence %+v", b)
				}
			}
		})
	}
}

func TestGroupReplyReceiverRestartAndOwnHistoryRefusal(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	owner, call := nativeReceiverFixture(t, w.alice, "pi")
	sent, e := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "persist exact local receiver", ReplyReceiver: &ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle}})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "restart original at Bob", func() bool { return len(groupTurns(t, w.bob, p.State.Conv)) == 1 })
	id, env := groupReceiverReply(t, w.bob, w.alice, p.State.Conv, sent.LID)
	key := w.alice.Self()
	home := w.alice.home
	w.alice.Close()
	a, e := Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b := groupReceiverBound(t, a, sent.LID)
	if len(b.Inputs) != 1 {
		t.Fatalf("restart lost input %+v", b)
	}
	groupGovernanceDeliver(t, w.bob, a, env)
	in, e := envelope.Open(env, a.id, a.Address, w.bob.Self())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.store.addHistoryInbox(in, 0, w.bob.Self().Fingerprint(), key.Fingerprint(), "synthetic-history-carrier", false, nil); e != nil {
		t.Fatal(e)
	}
	d, e := a.TakeReplyReceiverInput(call)
	if e != nil || d == nil || d.InputID != id {
		t.Fatalf("restarted group take %+v %v", d, e)
	}
	b = groupReceiverBound(t, a, sent.LID)
	if len(b.Inputs) != 1 {
		t.Fatalf("fan/history duplicate input %+v", b)
	}
	if _, ok, e := a.store.claimJob("agentstub"); e != nil || ok {
		t.Fatalf("restart default takeover %v %v", ok, e)
	}
}

func TestGroupReplyReceiverRequesterWithdrawal(t *testing.T) {
	stub := installAgentStub(t)
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.bob]()
	local, e := w.bob.CreateLocalAgent("chosen requester", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	owner, call := nativeReceiverFixture(t, w.bob, "pi")
	refs := []string{}
	for _, r := range []*ReplyReceiver{{Kind: "managed_agent", AgentID: local.ID, Instructions: "original local review", Mode: envelope.KindQuestion}, {Kind: "live_session", SessionHandle: owner.Handle}} {
		sent, e := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "requester epoch request", ReplyReceiver: r})
		if e != nil {
			t.Fatal(e)
		}
		refs = append(refs, sent.LID)
		eventually(t, "requester original at Alice", func() bool {
			for _, v := range groupTurns(t, w.alice, p.State.Conv) {
				if v.LID == sent.LID {
					return true
				}
			}
			return false
		})
		groupReceiverReply(t, w.alice, w.bob, p.State.Conv, sent.LID)
	}
	if _, e = w.bob.LeaveGroup(tctx(t), p.State.Conv); e != nil {
		t.Fatal(e)
	}
	if _, ok, e := w.bob.claimReplyReceiverJob(); e != nil || ok {
		t.Fatalf("withdrawn requester claimed %v %v", ok, e)
	}
	if d, e := w.bob.TakeReplyReceiverInput(call); e == nil || d != nil {
		t.Fatalf("withdrawn requester took %+v %v", d, e)
	}
	b := groupReceiverBound(t, w.bob, refs[0])
	if b.State != "refused" || len(b.Inputs) != 1 {
		t.Fatalf("withdrawal lost original binding %+v", b)
	}
	if _, ok, e := w.bob.store.claimJob("agentstub"); e != nil || ok {
		t.Fatalf("withdrawn requester default takeover %v %v", ok, e)
	}
	if stub.runs() != 0 {
		t.Fatal("withdrawn requester ran selected agent")
	}
}
