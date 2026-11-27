package client

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Ruslan's report (2026-11-27): one person, one device, a group of their
// own with their own agent on that same device. Nobody else needs a copy;
// the group must still talk and the own agent must still be reachable.
func TestSoloOwnAgentGroupOnOneDevice(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	g, err := w.alice.CreateGroup(tctx(t), "My agents")
	if err != nil {
		t.Fatal(err)
	}
	conv := g.Root.ID()
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "note to self"}); err != nil {
		t.Errorf("plain message in a solo group: %v", err)
	}
	// A harness that never starts: this test is about sending, on every OS,
	// and must not launch a real agent program.
	Harnesses["solonone"] = harness{bin: filepath.Join(t.TempDir(), "missing"), question: []string{"--question-mode"}, task: []string{"--task-mode"}, stdin: true}
	t.Cleanup(func() { delete(Harnesses, "solonone") })
	named, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "solonone", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil {
		t.Fatalf("invite own agent into a solo group: %v", err)
	}
	if !p.Claimable() {
		t.Fatalf("own agent not active: %+v", p)
	}
	if _, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "hello"); err != nil {
		t.Errorf("ask own agent in a solo group: %v", err)
	}
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "after the agent joined"}); err != nil {
		t.Errorf("plain message with own agent present: %v", err)
	}
}

// MEL-840 end to end on one device: the own agent's job runs here and its
// answer shows in the group; a file goes with the ask. Then, as Ruslan did,
// the agent is removed and "Chat with Pi" (AgentChat.tsx: a topic of "My
// agents", ownAgentTopic's invitation, then askAgent) brings it back: the
// first message reaches the local agent again.
func TestSoloOwnAgentGroupAnswersAndRejoins(t *testing.T) {
	stub := installAgentStub(t)
	stub.mode("emotion")
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	g, err := w.alice.CreateGroup(tctx(t), "My agents")
	if err != nil {
		t.Fatal(err)
	}
	conv := g.Root.ID()
	named, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil || !p.Claimable() {
		t.Fatalf("invite own agent: %+v %v", p, err)
	}
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err = os.WriteFile(file, []byte("solo notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "hello", OutgoingFile{Path: file, Name: "notes.txt"})
	if err != nil {
		t.Fatalf("ask with a file: %v", err)
	}
	if r := replyAt(t, w.alice, conv, ask.ID); !strings.Contains(r.Body, "the deploy failed at step 3") {
		t.Fatalf("own agent's answer: %+v", r)
	}
	if !strings.Contains(stub.last(), "hello") || !strings.Contains(stub.last(), "solo notes") {
		t.Fatalf("the local agent was not asked: %q", stub.last())
	}
	if m, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == ask.LID && m.Kind == envelope.KindQuestion }); n != 1 || len(m.Attachments) != 1 || m.Attachments[0].Name != "notes.txt" {
		t.Fatalf("the ask and its file in the group: %d %+v", n, m.Attachments)
	} else if m.Job == "" {
		t.Fatalf("the ask shows no local job: %+v", m)
	}
	var local, posted int
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=? AND state=?`, ask.LID, w.alice.Address, protocol.StateDelivered).Scan(&local); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND state!=?`, ask.LID, protocol.StateDelivered).Scan(&posted); err != nil {
		t.Fatal(err)
	}
	if local != 1 || posted != 0 {
		t.Fatalf("the ask's copies: %d local-only, %d to post", local, posted)
	}
	if _, err = w.alice.DismissParticipation(WithQueuedSend(tctx(t), ""), p.PID); err != nil {
		t.Fatalf("remove own agent: %v", err)
	}
	eventually(t, "the agent removed", func() bool { return !stateAt(t, w.alice, p.PID).Claimable() })

	// "Chat with Pi": a new topic, the invitation the UI makes, the ask.
	topic := protocol.NewID()
	q, err := w.alice.InviteAgentInScope(WithQueuedSend(tctx(t), ""), conv, w.alice.Address, named.ID, nil, []string{w.alice.Self().Fingerprint()}, "", &topic)
	if err != nil {
		t.Fatalf("Chat with: invite own agent again: %v", err)
	}
	eventually(t, "own agent active in its topic", func() bool { return stateAt(t, w.alice, q.PID).Claimable() })
	id := protocol.NewID()
	sent, _, err := w.alice.AskAgentInTopicQueued(tctx(t), id, q.PID, envelope.KindQuestion, "Хай", topic, nil, "", 0)
	if err != nil {
		t.Fatalf("Chat with: first message: %v", err)
	}
	if r := replyAt(t, w.alice, conv, sent.ID); !strings.Contains(r.Body, "the deploy failed at step 3") {
		t.Fatalf("own agent's answer in its topic: %+v", r)
	}
	if !strings.Contains(stub.last(), "Хай") {
		t.Fatalf("the local agent was not asked in its topic: %q", stub.last())
	}
}

// soloAgentGroup is MEL-840's setup: alice's group of her own, her agent Pi
// (the shell stub) invited into it on this one device.
func soloAgentGroup(t *testing.T, w *world, stub *agentStub) (string, ParticipationInfo) {
	t.Helper()
	publishGroupFixtureCaps(t, w.alice, true)
	g, err := w.alice.CreateGroup(tctx(t), "My agents")
	if err != nil {
		t.Fatal(err)
	}
	named, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), g.Root.ID(), w.alice.Address, named.ID, nil, nil, "")
	if err != nil || !p.Claimable() {
		t.Fatalf("invite own agent: %+v %v", p, err)
	}
	return g.Root.ID(), p
}

func localOnlyCopies(t *testing.T, a *Agent, lid string) (local, posted int) {
	t.Helper()
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=? AND state=?`, lid, a.Address, protocol.StateDelivered).Scan(&local); err != nil {
		t.Fatal(err)
	}
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND NOT (recipient=? AND state=?)`, lid, a.Address, protocol.StateDelivered).Scan(&posted); err != nil {
		t.Fatal(err)
	}
	return local, posted
}

// The own agent's run started, then the Hub went away: its reply in a group
// that is this device's alone needs nobody's current roster, so it is kept
// (answered, one local-only copy), not lost as not delivered.
func TestSoloOwnAgentGroupReplyWithHubDown(t *testing.T) {
	stub := installAgentStub(t)
	stub.mode("gate")
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	conv, p := soloAgentGroup(t, w, stub)
	ask, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "while offline")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent to start", func() bool { _, err := os.Stat(stub.log + ".started"); return err == nil })
	w.hub.Stop()
	if err = os.WriteFile(stub.log+".go", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run to end", func() bool {
		s := jobState(t, w.alice, ask.ID)
		return s != stateRunning && s != "" && s != statePending
	})
	if s := jobState(t, w.alice, ask.ID); s != stateAnswered {
		var detail string
		w.alice.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, ask.ID).Scan(&detail)
		t.Fatalf("the reply with the Hub down: %s %q", s, detail)
	}
	r := replyAt(t, w.alice, conv, ask.ID)
	if !strings.Contains(r.Body, "the deploy failed at step 3") {
		t.Fatalf("the reply: %+v", r)
	}
	if local, posted := localOnlyCopies(t, w.alice, r.LID); local != 1 || posted != 0 {
		t.Fatalf("the reply's copies: %d local-only, %d to post", local, posted)
	}
	if n := stub.runs(); n != 1 {
		t.Fatalf("the agent ran %d times", n)
	}
}

// bob joins between the choice of the self-only copy and its write: the
// write sees it, the send is built again, and bob gets his copy. The own
// agent's ask and an ordinary turn alike; the job runs once.
func TestSoloOwnAgentGroupMemberJoinsMidSend(t *testing.T) {
	for _, ask := range []bool{true, false} {
		t.Run(map[bool]string{true: "ask", false: "turn"}[ask], func(t *testing.T) {
			stub := installAgentStub(t)
			stub.mode("emotion")
			w := newWorld(t, "")
			persons(t, w.alice, w.bob)
			runAgent(t, w.alice)
			runAgent(t, w.bob)
			publishGroupFixtureCaps(t, w.bob, true)
			conv, p := soloAgentGroup(t, w, stub)
			bob, _, err := w.bob.store.selfPerson(w.bob.Address)
			if err != nil {
				t.Fatal(err)
			}
			joins := 0
			t.Cleanup(func() { beforeOutbox = func() {} })
			beforeOutbox = func() {
				beforeOutbox = func() {}
				joins++
				inv, err := w.alice.InviteGroup(tctx(t), conv, bob.roster.Person, nil)
				if err != nil {
					t.Fatal(err)
				}
				awaitGroupInvitation(t, w.bob, inv.ID, "pending")
				if err = w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
					t.Fatal(err)
				}
				eventually(t, "bob a member at alice", func() bool {
					g, e := w.alice.GroupContext(conv)
					return e == nil && len(g.State.Members) == 2
				})
			}
			var sent ConvSent
			if ask {
				sent, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "racing a join")
			} else {
				sent, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "racing a join"})
			}
			if err != nil || joins != 1 {
				t.Fatalf("send while bob joins (%d joins): %v", joins, err)
			}
			var toBob int
			if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=?`, sent.LID, w.bob.Address).Scan(&toBob); err != nil {
				t.Fatal(err)
			}
			if local, _ := localOnlyCopies(t, w.alice, sent.LID); local != 0 || toBob != 1 {
				t.Fatalf("after bob joined: %d local-only, %d for bob", local, toBob)
			}
			eventually(t, "bob to see it", func() bool {
				_, n := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == sent.LID && m.Body == "racing a join" })
				return n == 1
			})
			if ask {
				if r := replyAt(t, w.alice, conv, sent.ID); !strings.Contains(r.Body, "the deploy failed at step 3") {
					t.Fatalf("own agent's answer: %+v", r)
				}
				if n := stub.runs(); n != 1 {
					t.Fatalf("the agent ran %d times", n)
				}
			} else if n := stub.runs(); n != 0 {
				t.Fatalf("an ordinary turn ran the agent %d times", n)
			}
		})
	}
}

// A file sent in a group that is this device's alone (each of the three
// solo paths: plain turn, the own agent's ask, a turn with the agent
// present) leaves no spooled ciphertext and no upload to do: its local-only
// copy is never posted. The kept copy still opens it here.
func TestSoloGroupFileLeavesNoSpool(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	g, err := w.alice.CreateGroup(tctx(t), "My agents")
	if err != nil {
		t.Fatal(err)
	}
	conv := g.Root.ID()
	file := func(body string) OutgoingFile {
		path := filepath.Join(t.TempDir(), body+".txt")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return OutgoingFile{Path: path, Name: body + ".txt"}
	}
	check := func(what string, sent ConvSent, body string) {
		t.Helper()
		if local, posted := localOnlyCopies(t, w.alice, sent.LID); local != 1 || posted != 0 {
			t.Fatalf("%s: %d local-only, %d to post", what, local, posted)
		}
		entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool"))
		var uploads int
		if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM uploads`).Scan(&uploads); err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 || uploads != 0 {
			t.Fatalf("%s: %d spool files, %d uploads left", what, len(entries), uploads)
		}
		r, _, err := w.alice.OpenFileFrom(tctx(t), "out", sent.ID, 0)
		if err != nil {
			t.Fatalf("%s: open the file here: %v", what, err)
		}
		defer r.Close()
		if data, _ := io.ReadAll(r); string(data) != body {
			t.Fatalf("%s: the file opened as %q", what, data)
		}
	}
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "plain", Files: []OutgoingFile{file("plain")}})
	if err != nil {
		t.Fatal(err)
	}
	check("plain turn", sent, "plain")
	Harnesses["solonone"] = harness{bin: filepath.Join(t.TempDir(), "missing"), question: []string{"--question-mode"}, task: []string{"--task-mode"}, stdin: true}
	t.Cleanup(func() { delete(Harnesses, "solonone") })
	named, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "solonone", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil || !p.Claimable() {
		t.Fatalf("invite own agent: %+v %v", p, err)
	}
	if sent, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "ask", file("ask")); err != nil {
		t.Fatal(err)
	}
	check("the own agent's ask", sent, "ask")
	if sent, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "with Pi", Files: []OutgoingFile{file("with Pi")}}); err != nil {
		t.Fatal(err)
	}
	check("a turn with the agent present", sent, "with Pi")
}

// The own agent answers only with a reaction (agentjob.go's prompt allows
// a whole reply of "reaction: EMOJI"). In a group that is this device's
// alone it is one local-only copy, online and with the Hub down: the
// request is answered and shows the agent's reaction, nothing is posted.
func TestSoloOwnAgentGroupReactionOnly(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "online", true: "offline"}[offline], func(t *testing.T) {
			stub := installAgentStub(t)
			stub.mode("reactgate")
			w := newWorld(t, "")
			persons(t, w.alice)
			runAgent(t, w.alice)
			conv, p := soloAgentGroup(t, w, stub)
			if !offline {
				if err := os.WriteFile(stub.log+".go", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ask, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "thumbs?")
			if err != nil {
				t.Fatal(err)
			}
			if offline {
				eventually(t, "the agent to start", func() bool { _, err := os.Stat(stub.log + ".started"); return err == nil })
				w.hub.Stop()
				if err = os.WriteFile(stub.log+".go", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			eventually(t, "the run to end", func() bool {
				return slices.Contains([]string{stateAnswered, stateNotDelivered, stateJobFailed, stateCancelled}, jobState(t, w.alice, ask.ID))
			})
			if s := jobState(t, w.alice, ask.ID); s != stateAnswered {
				var detail string
				w.alice.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, ask.ID).Scan(&detail)
				t.Fatalf("a reaction-only answer: %s %q", s, detail)
			}
			msgs, err := w.alice.ConversationMessages(conv)
			if err != nil {
				t.Fatal(err)
			}
			if rs := reactorsOn(msgs, ask.LID, "👍"); len(rs) != 1 || !rs[0].Assistant {
				t.Fatalf("the agent's reaction on the ask: %+v", rs)
			}
			var local, posted int
			if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND recipient=? AND state=?`, envelope.SubReaction, w.alice.Address, protocol.StateDelivered).Scan(&local); err != nil {
				t.Fatal(err)
			}
			if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND NOT (recipient=? AND state=?)`, envelope.SubReaction, w.alice.Address, protocol.StateDelivered).Scan(&posted); err != nil {
				t.Fatal(err)
			}
			if local != 1 || posted != 0 {
				t.Fatalf("the reaction's copies: %d local-only, %d to post", local, posted)
			}
			if n := stub.runs(); n != 1 {
				t.Fatalf("the agent ran %d times", n)
			}
		})
	}
}
