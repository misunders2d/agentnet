package client

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestSplitReaction(t *testing.T) {
	for _, c := range []struct {
		in, rest, emoji string
		remove, ok      bool
	}{
		{"the answer\nreaction: 👍", "the answer", "👍", false, true},
		{"the answer\n  Reaction:  -🎉  \n", "the answer", "🎉", true, true},
		{"the answer\nreaction: Very Happy", "the answer\nreaction: Very Happy", "", false, false},
		{"reaction: 👍", "reaction: 👍", "", false, false}, // nothing would be left to answer
		{"reaction: 👍\nthe answer", "reaction: 👍\nthe answer", "", false, false},
		{"plain answer", "plain answer", "", false, false},
	} {
		rest, choice := splitReaction(c.in)
		if rest != c.rest || (choice != nil) != c.ok || choice != nil && (choice.emoji != c.emoji || choice.remove != c.remove) {
			t.Errorf("%q: %q %+v", c.in, rest, choice)
		}
	}
}

// withAgentReaction signs the agr1 capability for a's live sessions at a
// current timestamp.
func withAgentReaction(t *testing.T, agents ...*Agent) {
	t.Helper()
	caps := withCap(ownCaps, protocol.CapAgentReaction)
	for _, a := range agents {
		signCapsNow(t, a, caps)
	}
}

func reactorsOn(msgs []ConvMessage, lid, emoji string) []Reactor {
	for _, m := range msgs {
		if m.LID == lid {
			for _, r := range m.Reactions {
				if r.Emoji == emoji {
					return r.By
				}
			}
		}
	}
	return nil
}

func v1Reactors(t *testing.T, a *Agent, id, emoji string) []Reactor {
	t.Helper()
	c, err := a.Conversation(id, 0, 0)
	if err != nil {
		return nil
	}
	for _, m := range c.Messages {
		if m.ID == id {
			for _, r := range m.Reactions {
				if r.Emoji == emoji {
					return r.By
				}
			}
		}
	}
	return nil
}

func reactorIDs(rs []Reactor) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	slices.Sort(out)
	return out
}

// A device's default responder and named executor react as themselves,
// distinct from that device's person; only the request sent to them takes
// it; a reader without agr1 makes it wait, then gets it once.
func TestAssistantReactionDeviceThread(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	progressReader(t, w.alice, w.bob)
	waitNamedAgentCaps(t, w.bob)
	withAgentReaction(t, w.alice, w.bob)
	aliceFP := w.alice.Self().Fingerprint()
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "default request"})
	if err != nil {
		t.Fatal(err)
	}
	agent := protocol.NewID()
	n, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "named request",
		Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: agent}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "requests at bob", func() bool { return inboxCount(t, w.bob, "id IN (?, ?)", q.ID, n.ID) == 2 })

	if _, err = w.bob.reactAsAssistant(tctx(t), q.ID, "claude", "👍", false); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.React(tctx(t), ControlRef{ID: q.ID, Fingerprint: aliceFP}, "👍", false); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.reactAsAssistant(tctx(t), n.ID, "claude", "👍", false); err != nil {
		t.Fatal(err)
	}
	def, named := "assistant:"+w.bob.Address+"/default", "assistant:"+w.bob.Address+"/"+agent
	eventually(t, "owner and default assistant distinct", func() bool {
		want := []string{def, w.bob.Address}
		slices.Sort(want)
		return slices.Equal(reactorIDs(v1Reactors(t, w.alice, q.ID, "👍")), want)
	})
	eventually(t, "named assistant distinct", func() bool {
		rs := v1Reactors(t, w.alice, n.ID, "👍")
		return len(rs) == 1 && rs[0].ID == named && rs[0].Assistant
	})
	// Removing the assistant's mark leaves the owner's.
	if _, err = w.bob.reactAsAssistant(tctx(t), q.ID, "claude", "👍", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "only the assistant's mark removed", func() bool {
		return slices.Equal(reactorIDs(v1Reactors(t, w.alice, q.ID, "👍")), []string{w.bob.Address})
	})
	// A harness change keeps the default responder's reactor.
	if _, err = w.bob.reactAsAssistant(tctx(t), q.ID, "codex", "🎉", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "same default reactor", func() bool { return slices.Equal(reactorIDs(v1Reactors(t, w.alice, q.ID, "🎉")), []string{def}) })

	// Forged provenance is never stored.
	recipient, _ := w.alice.Self().Recipient()
	forge := func(ref, agentID, origin string, fp string) string {
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Sub: envelope.SubReaction, Body: `{"emoji":"🔥","op":"add","n":99}`, Ref: &envelope.Ref{ID: ref, Fingerprint: fp}, AgentID: agentID, Origin: origin}
		env, err := envelope.Seal(in, w.bob.id.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.alice.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	for _, id := range []string{
		forge(n.ID, "", envelope.OriginAgentPrefix+"claude", aliceFP),                    // default responder on a named request
		forge(n.ID, protocol.NewID(), "", aliceFP),                                       // another agent
		forge(q.ID, agent, "", aliceFP),                                                  // named agent on a default request
		forge(q.ID, "", envelope.OriginAgentPrefix+"claude", w.bob.Self().Fingerprint()), // not alice's request
	} {
		if inboxCount(t, w.alice, "id = ?", id) != 0 {
			t.Fatal("forged assistant reaction stored")
		}
	}

	// An older reader: the reaction waits, then arrives once.
	signCapsNow(t, w.alice, without(ownCaps, protocol.CapAgentReaction))
	held, err := w.bob.reactAsAssistant(tctx(t), q.ID, "claude", "✅", false)
	if err != nil || held.State != stateConvWaiting {
		t.Fatalf("reaction to an older reader: %+v %v", held, err)
	}
	withAgentReaction(t, w.alice)
	feats, _ := w.bob.relayFeatures(tctx(t))
	w.bob.releaseConv(tctx(t), feats)
	if err = w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "held reaction released", func() bool { return slices.Equal(reactorIDs(v1Reactors(t, w.alice, q.ID, "✅")), []string{def}) })
	w.bob.releaseConv(tctx(t), feats)
	w.bob.FlushOutbox(tctx(t))
	time.Sleep(200 * time.Millisecond)
	if n := inboxCount(t, w.alice, "sub = ? AND body LIKE ?", envelope.SubReaction, `%✅%`); n != 1 {
		t.Fatalf("released copies %d", n)
	}
}

// reactHarness registers a stand-in responder that answers with text, an
// optional reaction line and an emotion line when asked for one.
func reactHarness(t *testing.T, name, out string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	os.WriteFile(bin, []byte("#!/bin/sh\necho run >> \"$0.runs\"\ncat > \"$0.prompt\"\nprintf '"+out+"'\n"), 0o700)
	Harnesses[name] = harness{bin: bin, stdin: true}
	t.Cleanup(func() { delete(Harnesses, name) })
	return bin
}

// A default responder chooses a reaction in its final output: one answer
// (without the reaction line) and one assistant reaction; no other job.
func TestAssistantReactionChosenByResponder(t *testing.T) {
	bin := reactHarness(t, "reactstub", `the answer\nreaction: 👍\n`)
	w := newWorld(t, "")
	setResponder(t, w.bob, "reactstub", t.TempDir(), time.Minute)
	w.bob.Approve(w.alice.Address)
	runAgent(t, w.bob)
	progressReader(t, w.alice, w.bob)
	withAgentReaction(t, w.alice, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "what is up?"})
	if err != nil {
		t.Fatal(err)
	}
	var ans Message
	eventually(t, "answer", func() bool { var ok bool; ans, ok = findReply(w.alice, q.ID); return ok })
	if ans.Body != "the answer" {
		t.Fatalf("answer %q", ans.Body)
	}
	eventually(t, "chosen reaction", func() bool {
		rs := v1Reactors(t, w.alice, q.ID, "👍")
		return len(rs) == 1 && rs[0].ID == "assistant:"+w.bob.Address+"/default" && rs[0].Assistant
	})
	time.Sleep(300 * time.Millisecond)
	runs, _ := os.ReadFile(bin + ".runs")
	prompt, _ := os.ReadFile(bin + ".prompt")
	if strings.Count(string(runs), "run") != 1 || inboxCount(t, w.alice, "reply_to = ? AND kind = ?", q.ID, envelope.KindAnswer) != 1 || !strings.Contains(string(prompt), "reaction: EMOJI") {
		t.Fatalf("runs %q answers %d prompt has reaction offer %v", runs, inboxCount(t, w.alice, "reply_to = ? AND kind = ?", q.ID, envelope.KindAnswer), strings.Contains(string(prompt), "reaction: EMOJI"))
	}
}

// An outside participation's assistant chooses a reaction to its request:
// both members see it as the assistant's, beside a member's own mark;
// forged agent/PID never stored; after the end nothing more goes.
func TestAssistantReactionParticipation(t *testing.T) {
	w, host, conv, _, _, records, _ := externalAgentWorld(t)
	bin := reactHarness(t, "reactagent", `looked\nreaction: 🎉\nemotion: calm\n`)
	record, err := host.CreateLocalAgent("Reactor", Responder{Harness: "reactagent", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	withAgentReaction(t, w.alice, w.bob, host)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invite at host", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "active", func() bool { return stateAt(t, w.alice, p.PID).Claimable() && stateAt(t, w.bob, p.PID).Claimable() })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "react if you like")
	if err != nil {
		t.Fatal(err)
	}
	if reply := replyAt(t, w.alice, conv, q.ID); reply.Body != "looked" {
		t.Fatalf("reply %q", reply.Body)
	}
	if _, err = w.alice.React(tctx(t), ControlRef{Conv: conv, ID: q.LID, Fingerprint: w.alice.Self().Fingerprint()}, "🎉", false); err != nil {
		t.Fatal(err)
	}
	alicePerson, _, _ := w.alice.Person()
	for _, a := range []*Agent{w.alice, w.bob} {
		eventually(t, "assistant and member reactions at "+a.Address, func() bool {
			msgs, _ := a.ConversationMessages(conv)
			rs := reactorsOn(msgs, q.LID, "🎉")
			want := []string{"assistant:" + p.PID, alicePerson.Person}
			slices.Sort(want)
			return slices.Equal(reactorIDs(rs), want)
		})
	}
	prompt, _ := os.ReadFile(bin + ".prompt")
	if !strings.Contains(string(prompt), "just before the emotion line") {
		t.Fatal("conversation prompt lacks the reaction offer")
	}
	// Forged agent or PID from the host: held, never stored.
	_, root, _, _ := host.store.conversation(conv)
	recipient, _ := w.bob.Self().Recipient()
	for _, f := range [][2]string{{p.PID, records[1].ID}, {protocol.NewID(), record.ID}} {
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: host.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Sub: envelope.SubReaction, Body: `{"emoji":"🔥","op":"add","n":99}`, Ref: &envelope.Ref{ID: q.LID, Fingerprint: w.alice.Self().Fingerprint()},
			Conv: conv, LID: protocol.NewID(), PID: f[0], AgentID: f[1]}
		_ = root
		env, err := envelope.Seal(in, host.id.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.bob.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if inboxCount(t, w.bob, "id = ?", in.ID) != 0 {
			t.Fatalf("forged assistant reaction %v stored", f)
		}
	}
	// After the end: refused at the host.
	if _, err = w.bob.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "end at host", func() bool { return stateAt(t, host, p.PID).State == PartDismissed })
	if _, err = host.reactAsAssistant(tctx(t), q.ID, "reactagent", "✅", false); err == nil {
		t.Fatal("assistant reacted after its participation ended")
	}
}

// A group visitor assistant's reaction reaches the group's members as the
// assistant's.
func TestAssistantReactionGroupParticipation(t *testing.T) {
	stub := installAgentStub(t)
	stub.mode("sleep")
	w, producer, packet, stops := groupTurnsFixture(t)
	host := proofReader(t, w, "reaction-visitor")
	runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	fakeNotify(host)
	members := []*Agent{}
	for a := range stops {
		if a != host {
			members = append(members, a)
		}
	}
	for _, a := range append(members, host) {
		addCapSuccessor(t, a, protocol.CapAgentReaction)
	}
	record, err := host.CreateLocalAgent("Group reactor", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	p, err := producer.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor invitation", func() bool { v, e := host.Participation(p.PID); return e == nil && v.State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor active", func() bool { v, e := producer.Participation(p.PID); return e == nil && v.Claimable() })
	q, err := producer.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "group react")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor runs", func() bool { _, err := os.Stat(stub.log + ".started"); return err == nil })
	if _, err = host.reactAsAssistant(tctx(t), q.ID, "agentstub", "👀", false); err != nil {
		t.Fatal(err)
	}
	for _, a := range members {
		eventually(t, "group assistant reaction at "+a.Address, func() bool {
			msgs, _ := a.ConversationMessages(conv)
			rs := reactorsOn(msgs, q.LID, "👀")
			return len(rs) == 1 && rs[0].ID == "assistant:"+p.PID && rs[0].Assistant
		})
	}
}

// addCapSuccessor adds cap to each live session's latest signed record, as
// that record's immediate successor. The group fixture signs its records
// ahead of the clock; a current-time record would only wait for them.
func addCapSuccessor(t *testing.T, a *Agent, cap string) {
	t.Helper()
	label, name, _ := protocol.SplitAddress(a.Address)
	var prof protocol.Profile
	if err := a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		t.Fatal(err)
	}
	for _, session := range prof.Sessions {
		var latest protocol.CapsRecord
		for _, raw := range prof.Caps {
			if r, err := protocol.ParseCapsRecord(raw); err == nil && r.Session == session && r.TS >= latest.TS {
				latest = r
			}
		}
		caps := append(slices.Clone(latest.Caps), cap)
		slices.Sort(caps)
		caps = slices.Compact(caps)
		rec := protocol.CapsRecord{Address: a.Address, Session: session, Caps: caps, TS: max(latest.TS+1, time.Now().Unix())}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
}
