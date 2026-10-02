package client

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func remoteGroupWorld(t *testing.T) (*world, *Agent, GroupContext, func()) {
	t.Helper()
	w, _, packet, _ := groupTurnsFixture(t)
	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	if e := w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); e != nil {
		t.Fatal(e)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	stop := runAgent(t, phone)
	// Existing explicit verified current-context import isolates receiver
	// acceptance from the separately owned automatic link-carrier producer.
	if e := phone.SyncGroupFromRoot(tctx(t), packet.Root); e != nil {
		t.Fatal(e)
	}
	if e := phone.AcceptGroupContext(tctx(t), packet); e != nil {
		t.Fatal(e)
	}
	publishGroupFixtureCaps(t, phone, true)
	if _, e := w.alice.sendKey(tctx(t), phone.Address); e != nil {
		t.Fatal(e)
	}
	if _, e := w.alice.GrantTasks(phone.Address); e != nil {
		t.Fatal(e)
	}
	return w, phone, packet, stop
}

func TestReceiverRemoteGroupOrdinaryManagedFiles(t *testing.T) {
	stub := installAgentStub(t)
	w, phone, packet, stop := remoteGroupWorld(t)
	chosen, e := w.alice.CreateLocalAgent("selected group A", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	r := ReplyReceiver{Kind: "managed_agent", AgentID: chosen.ID, Instructions: "EXPLICIT GROUP CONTINUATION", Mode: envelope.KindQuestion, Host: &ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}}
	sent, e := phone.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "ORIGINAL GROUP PHONE REQUEST", ReplyReceiver: &r})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "group ready releases exact original", func() bool {
		for _, v := range groupTurns(t, w.bob, packet.State.Conv) {
			if v.LID == sent.LID {
				return true
			}
		}
		return false
	})
	stop()
	path, _ := writeFile(t, t.TempDir(), "group-return.txt", 94)
	id, env := groupReceiverReply(t, w.bob, w.alice, packet.State.Conv, sent.LID, OutgoingFile{Path: path})
	waitState(t, w.alice, id, stateContinued)
	groupGovernanceDeliver(t, w.bob, w.alice, env)
	b := groupReceiverBound(t, w.alice, sent.LID)
	if len(b.Inputs) != 1 || stub.runs() != 1 || b.Executor == nil || b.Executor.AgentID != chosen.ID {
		t.Fatalf("selected group duplicate/default %+v runs%d", b, stub.runs())
	}
	if !strings.Contains(stub.last(), r.Instructions) || !strings.Contains(stub.last(), "ORIGINAL GROUP PHONE REQUEST") || !strings.Contains(stub.last(), "Verified attachment") {
		t.Fatal("group selected continuation lost authority/files")
	}
	if j, ok, e := w.alice.store.claimJob("default"); e != nil || ok {
		t.Fatalf("group default %+v %t %v", j, ok, e)
	}
}

func TestReceiverRemoteGroupNamedNativeMemberVisitor(t *testing.T) {
	for _, visitor := range []bool{false, true} {
		t.Run(map[bool]string{false: "member", true: "visitor"}[visitor], func(t *testing.T) {
			stub := installAgentStub(t)
			w, phone, packet, stop := remoteGroupWorld(t)
			host := w.bob
			if visitor {
				host = proofReader(t, w, "remote-visitor")
				runAgent(t, host)
				publishGroupFixtureCaps(t, host, true)
			}
			fakeNotify(host)
			remote, e := host.CreateLocalAgent("remote B", Responder{Harness: "agentstub", Dir: stub.dir})
			if e != nil {
				t.Fatal(e)
			}
			if e = host.PublishAgentCatalog(tctx(t)); e != nil {
				t.Fatal(e)
			}
			if _, e = host.sendKey(tctx(t), phone.Address); e != nil {
				t.Fatal(e)
			}
			if e = host.Approve(phone.Address); e != nil {
				t.Fatal(e)
			}
			part, e := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, remote.ID, nil, nil, "exact selected native group return")
			if e != nil {
				t.Fatal(e)
			}
			eventually(t, "remote invitation", func() bool { p, e := host.Participation(part.PID); return e == nil && p.State == PartInvited })
			if _, e = host.AcceptParticipation(tctx(t), part.PID); e != nil {
				t.Fatal(e)
			}
			eventually(t, "phone verified current PID", func() bool { p, e := phone.Participation(part.PID); return e == nil && p.Claimable() })
			owner, call := nativeReceiverFixture(t, w.alice, "pi")
			r := ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle, Host: &ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}}
			sent, e := phone.AskAgentWithReceiver(tctx(t), part.PID, envelope.KindQuestion, "ORIGINAL PHONE PID QUESTION", &r)
			if e != nil {
				t.Fatal(e)
			}
			b := groupReceiverBound(t, phone, sent.LID)
			full, e := replyReceiverIn(phone.store.db, b.ID)
			if e != nil {
				t.Fatal(e)
			}
			setup := full.remote.Route.DelegationID
			eventually(t, "selected laptop named output", func() bool {
				var n int
				w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE binding=?`, setup).Scan(&n)
				return n == 1
			})
			stop()
			d, e := w.alice.TakeReplyReceiverInput(call)
			if e != nil || d == nil || d.RequestBody != "ORIGINAL PHONE PID QUESTION" {
				t.Fatalf("native PID %+v %v", d, e)
			}
			if stub.runs() != 1 {
				t.Fatalf("remote/selected default runs%d", stub.runs())
			}
			if _, e = w.alice.DismissParticipation(tctx(t), part.PID); e != nil {
				t.Fatal(e)
			}
			if again, e := w.alice.TakeReplyReceiverInput(call); e == nil || again != nil {
				t.Fatalf("withdrawn uncertain PID must stay held %+v %v", again, e)
			}
			if j, ok, e := w.alice.store.claimJob("default"); e != nil || ok {
				t.Fatalf("PID default %+v %t %v", j, ok, e)
			}
		})
	}
}
