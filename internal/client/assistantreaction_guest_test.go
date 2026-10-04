package client

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func profileOf(t *testing.T, a *Agent) protocol.Profile {
	t.Helper()
	label, name, _ := protocol.SplitAddress(a.Address)
	var prof protocol.Profile
	if err := a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		t.Fatal(err)
	}
	return prof
}

// dropCapSuccessor removes cap from each live session's latest signed
// record, as that record's immediate successor (addCapSuccessor's inverse).
func dropCapSuccessor(t *testing.T, a *Agent, cap string) {
	t.Helper()
	prof := profileOf(t, a)
	for _, session := range prof.Sessions {
		var latest protocol.CapsRecord
		for _, raw := range prof.Caps {
			if r, err := protocol.ParseCapsRecord(raw); err == nil && r.Session == session && r.TS >= latest.TS {
				latest = r
			}
		}
		caps := without(latest.Caps, cap) // an older reader: no rm1 either
		rec := protocol.CapsRecord{Address: a.Address, Session: session, Caps: caps, TS: max(latest.TS+1, time.Now().Unix())}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
}

type reactionCopy struct {
	state  string
	detail string
	pids   []string // the captured audience's scopes
	human  *envelope.HumanTurn
	env    envelope.Envelope
}

// reactionCopies are a's copies of its assistant's reaction emoji in conv,
// by recipient.
func reactionCopies(t *testing.T, a *Agent, conv, emoji string) map[string]reactionCopy {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT recipient, state, coalesce(error,''), coalesce(human,''), envelope FROM outbox WHERE conv=? AND sub=? AND pid IS NOT NULL AND instr(body, ?) > 0`,
		conv, envelope.SubReaction, `"emoji":"`+emoji+`"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]reactionCopy{}
	for rows.Next() {
		var to, human, raw string
		var c reactionCopy
		if err := rows.Scan(&to, &c.state, &c.detail, &human, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &c.env); err != nil {
			t.Fatal(err)
		}
		if human != "" {
			c.human = &envelope.HumanTurn{}
			if err := json.Unmarshal([]byte(human), c.human); err != nil {
				t.Fatal(err)
			}
			for _, s := range c.human.Audience {
				c.pids = append(c.pids, s.PID)
			}
			slices.Sort(c.pids)
		}
		out[to] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// requestAt is a's own stored copy of request lid in conv.
func requestAt(t *testing.T, a *Agent, conv, lid string) string {
	t.Helper()
	var id string
	eventually(t, "request "+lid+" at "+a.Address, func() bool {
		return a.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND ref_id IS NULL AND local=0 AND replica=0`, conv, lid).Scan(&id) == nil
	})
	return id
}

func assistantMarkAt(t *testing.T, a *Agent, conv, lid, emoji, pid string) bool {
	t.Helper()
	msgs, _ := a.ConversationMessages(conv)
	rs := reactorsOn(msgs, lid, emoji)
	return len(rs) == 1 && rs[0].ID == "assistant:"+pid && rs[0].Assistant
}

// An assistant's reaction to a request with guests goes to that request's
// captured audience still active, as its output does: the guest requester
// and the members see the assistant's own mark. A guest who joined after
// the request never gets it; a guest who ends while their copy waits gets
// nothing. A reaction naming more than its request's audience, from another
// actor or agent, or about another key's request is never stored; one ahead
// of its request waits for it, then is stored once.
func TestAssistantReactionGuestAudience(t *testing.T) {
	w, carol, conv, _, _, ap, hp := guestAssistant(t)
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		addCapSuccessor(t, a, protocol.CapAgentReaction)
	}
	if err := w.bob.Approve(carol.Address); err != nil {
		t.Fatal(err)
	}
	carolFP := carol.Self().Fingerprint()
	q1, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "guest asks for a look")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, carol, conv, q1.LID)

	// Dave joins after Carol's request.
	dave := mustJoin(t, filepath.Join(t.TempDir(), "dave"), w.aliceInvites("dave"), "guest2")
	stopDave := runAgent(t, dave)
	persons(t, dave)
	fakeNotify(dave)
	humanTestCaps(t, dave)
	addCapSuccessor(t, dave, protocol.CapAgentReaction)
	dp, err := w.alice.InviteHuman(tctx(t), conv, dave.Address, nil, "PRIVATE_GUEST_NOTE_D")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "Dave invited", func() bool { return stateAt(t, dave, dp.PID).State == PartInvited })
	if _, err = dave.AcceptParticipation(tctx(t), dp.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Dave active at both originals", func() bool {
		return stateAt(t, w.alice, dp.PID).HumanActive() && stateAt(t, w.bob, dp.PID).HumanActive()
	})
	eventually(t, "Dave learns the assistant", func() bool { p, err := dave.Participation(ap.PID); return err == nil && p.Claimable() })

	// The assistant's mark on Carol's request: Carol and Alice see it as the
	// assistant's; its copies go to them only, with the request's scope.
	if _, err = w.bob.reactAsAssistant(tctx(t), requestAt(t, w.bob, conv, q1.LID), "agentstub", "👀", false); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{carol, w.alice} {
		eventually(t, "assistant mark at "+a.Address, func() bool { return assistantMarkAt(t, a, conv, q1.LID, "👀", ap.PID) })
	}
	looked := reactionCopies(t, w.bob, conv, "👀")
	if len(looked) != 2 || !slices.Equal(looked[carol.Address].pids, []string{hp.PID}) || !slices.Equal(looked[w.alice.Address].pids, []string{hp.PID}) {
		t.Fatalf("copies of the reaction to Carol's request: %+v", looked)
	}
	time.Sleep(300 * time.Millisecond)
	if n := inboxCount(t, dave, "sub = ?", envelope.SubReaction); n != 0 {
		t.Fatalf("a guest who joined after the request got %d reactions", n)
	}

	// Never stored: a broader audience than the request's, another actor or
	// agent; a reaction about another key's request waits for proof.
	genuine := looked[w.alice.Address].human
	broad, err := w.bob.humanPlan(tctx(t), conv, "")
	if err != nil || broad == nil || len(broad.Audience) != 2 {
		t.Fatalf("Bob's current audience: %+v %v", broad, err)
	}
	pinfo, err := w.alice.Participation(ap.PID)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := w.alice.Self().Recipient()
	forge := func(by *Agent, h *envelope.HumanTurn, agentID, refFP string) string {
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: by.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Sub: envelope.SubReaction, Body: `{"emoji":"🔥","op":"add","n":99}`, Ref: &envelope.Ref{ID: q1.LID, Fingerprint: refFP},
			Conv: conv, LID: protocol.NewID(), PID: ap.PID, AgentID: agentID, Human: h}
		env, err := envelope.Seal(in, by.id.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.alice.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	for _, c := range []struct{ name, id, reason string }{
		{"a broader audience than the request's", forge(w.bob, broad, pinfo.AgentID, carolFP), reasonInvalid},
		{"another actor", forge(carol, genuine, pinfo.AgentID, carolFP), reasonInvalid},
		{"another agent", forge(w.bob, genuine, protocol.NewID(), carolFP), reasonInvalid},
		{"another key's request", forge(w.bob, genuine, pinfo.AgentID, w.bob.Self().Fingerprint()), reasonProof},
	} {
		if n, why := inboxCount(t, w.alice, "id = ?", c.id), heldReason(t, w.alice, c.id); n != 0 || why != c.reason {
			t.Fatalf("%s: stored %d, held %q", c.name, n, why)
		}
	}

	// A member's request beside both guests: Dave is in its audience. His
	// copy arriving ahead of the request waits for it, then is stored once.
	sessions := profileOf(t, dave).Sessions
	stopDave()
	q2, err := w.alice.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "member asks beside guests")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "reply to Alice", func() bool {
		_, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.ReplyTo == q2.ID || m.ReplyTo == q2.LID })
		return n == 1
	})
	bobQ2 := requestAt(t, w.bob, conv, q2.LID)
	if _, err = w.bob.reactAsAssistant(tctx(t), bobQ2, "agentstub", "🎉", false); err != nil {
		t.Fatal(err)
	}
	party := reactionCopies(t, w.bob, conv, "🎉")
	both := []string{hp.PID, dp.PID}
	slices.Sort(both)
	if len(party) != 3 || !slices.Equal(party[dave.Address].pids, both) || !slices.Equal(party[carol.Address].pids, both) {
		t.Fatalf("copies of the reaction to Alice's request: %+v", party)
	}
	early := party[dave.Address].env
	if err = dave.accept(tctx(t), early); err != nil {
		t.Fatal(err)
	}
	if n, why := inboxCount(t, dave, "id = ?", early.ID), heldReason(t, dave, early.ID); n != 0 || why != reasonProof {
		t.Fatalf("reaction ahead of its request: stored %d, held %q", n, why)
	}
	runAgent(t, dave)
	// The restart's new session advertises production capabilities: sign the
	// qualification ones again once it has.
	eventually(t, "Dave's new session published", func() bool {
		for _, raw := range profileOf(t, dave).Caps {
			if r, err := protocol.ParseCapsRecord(raw); err == nil && !slices.Contains(sessions, r.Session) {
				return true
			}
		}
		return false
	})
	humanTestCaps(t, dave)
	addCapSuccessor(t, dave, protocol.CapAgentReaction)
	for _, a := range []*Agent{dave, carol, w.alice} {
		eventually(t, "assistant mark on Alice's request at "+a.Address, func() bool { return assistantMarkAt(t, a, conv, q2.LID, "🎉", ap.PID) })
	}
	if err = dave.accept(tctx(t), early); err != nil { // a replay
		t.Fatal(err)
	}
	if n := inboxCount(t, dave, "id = ?", early.ID); n != 1 {
		t.Fatalf("replayed reaction stored %d times", n)
	}

	// Dave's copy waits (no agr1); he ends; his capability comes back: the
	// release's delivery fence finds him outside the audience.
	dropCapSuccessor(t, dave, protocol.CapAgentReaction)
	if _, err = w.bob.reactAsAssistant(tctx(t), bobQ2, "agentstub", "✅", false); err != nil {
		t.Fatal(err)
	}
	if s := reactionCopies(t, w.bob, conv, "✅")[dave.Address].state; s != stateConvWaiting {
		t.Fatalf("copy to a reader without agr1: %q", s)
	}
	if _, err = dave.DismissParticipation(tctx(t), dp.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Bob observes Dave's end", func() bool { return stateAt(t, w.bob, dp.PID).State == PartDismissed })
	addCapSuccessor(t, dave, protocol.CapAgentReaction)
	feats, err := w.bob.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	w.bob.releaseConv(tctx(t), feats)
	w.bob.FlushOutbox(tctx(t))
	var last reactionCopy
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Dave's waiting copy: %q %q", last.state, last.detail)
		}
	})
	eventually(t, "ended guest's waiting copy not delivered", func() bool {
		last = reactionCopies(t, w.bob, conv, "✅")[dave.Address]
		return last.state == stateNotDelivered
	})
	for _, a := range []*Agent{carol, w.alice} {
		eventually(t, "second mark at "+a.Address, func() bool { return assistantMarkAt(t, a, conv, q2.LID, "✅", ap.PID) })
	}
	time.Sleep(300 * time.Millisecond)
	if n := inboxCount(t, dave, "sub = ? AND instr(body, ?) > 0", envelope.SubReaction, "✅"); n != 0 {
		t.Fatalf("ended guest got %d waiting reactions", n)
	}
}

// The wire shape of an assistant's reaction to a captured audience: a
// conversation reaction naming its participation, with the audience (no
// author, no root). Every other control, a person's reaction, a device
// thread's, an author, a root or proof from elsewhere is refused at seal.
func TestHumanReactionWireVectors(t *testing.T) {
	_, _, human := humanVector()
	audience := *human
	audience.AuthorPID = "" // the assistant's host authors its reaction, as its output
	conv := human.Proof[0].Conv
	sender, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	r, err := reader.Public("fixture/bob").Recipient()
	if err != nil {
		t.Fatal(err)
	}
	valid := envelope.Inner{V: envelope.Version3, ID: strings.Repeat("8", 32), From: "fixture/host", To: "fixture/bob", TS: 1790000200, Kind: envelope.KindMessage,
		Sub: envelope.SubReaction, Body: `{"emoji":"👀","op":"add","n":1}`, Ref: &envelope.Ref{ID: strings.Repeat("5", 32), Fingerprint: "01234567-89abcdef-01234567-89abcdef"},
		Conv: conv, LID: strings.Repeat("9", 32), PID: strings.Repeat("7", 32), Fan: []envelope.Fan{{Person: strings.Repeat("2", 32), Roster: strings.Repeat("b", 64)}}, Human: &audience}
	if _, err := envelope.Seal(valid, sender.Sign, r); err != nil {
		t.Fatalf("assistant reaction to a captured audience: %v", err)
	}
	unproven := audience
	unproven.Audience = append(slices.Clone(audience.Audience), envelope.HumanScope{PID: strings.Repeat("6", 32), Invite: strings.Repeat("d", 64), Decision: strings.Repeat("e", 64)})
	change := func(f func(*envelope.Inner)) envelope.Inner { in := valid; f(&in); return in }
	invalid := []struct {
		name  string
		in    envelope.Inner
		plain bool // valid without its audience: the audience alone is refused
	}{
		{"a person's reaction", change(func(in *envelope.Inner) { in.PID = "" }), true},
		{"a device thread's assistant reaction", change(func(in *envelope.Inner) {
			in.Conv, in.LID, in.Fan, in.PID, in.Origin = "", "", nil, "", envelope.OriginAgentPrefix+"claude"
		}), true},
		{"a revision naming a participation", change(func(in *envelope.Inner) { in.Sub, in.Body = envelope.SubRevision, `{"rev":1,"text":"edited"}` }), false},
		{"a retraction naming a participation", change(func(in *envelope.Inner) { in.Sub, in.Body = envelope.SubRetraction, `{}` }), false},
		{"an author", change(func(in *envelope.Inner) { in.Human = human }), true},
		{"a root", change(func(in *envelope.Inner) { in.Root = []byte(`{}`) }), false},
		{"another conversation's proof", change(func(in *envelope.Inner) { in.Conv = strings.Repeat("c", 64) }), true},
		{"a scope without proof", change(func(in *envelope.Inner) { in.Human = &unproven }), true},
	}
	vectors := map[string]envelope.Inner{}
	for _, c := range invalid {
		if _, err := envelope.Seal(c.in, sender.Sign, r); err == nil {
			t.Errorf("%s with an audience: sealed", c.name)
		}
		if c.plain {
			bare := c.in
			bare.Human = nil
			if _, err := envelope.Seal(bare, sender.Sign, r); err != nil {
				t.Errorf("%s without an audience: %v", c.name, err)
			}
		}
		vectors[c.name] = c.in
	}
	// An edit carries its turn's captured audience (ROOM_V1 §2.3): it names
	// no participation, and its reader checks it against the edited turn.
	for _, sub := range []string{envelope.SubRevision, envelope.SubRetraction} {
		edit := change(func(in *envelope.Inner) {
			in.Sub, in.PID, in.Body = sub, "", map[string]string{envelope.SubRevision: `{"rev":1,"text":"edited"}`, envelope.SubRetraction: `{}`}[sub]
		})
		if _, err := envelope.Seal(edit, sender.Sign, r); err != nil {
			t.Errorf("an edit (%s) carrying its turn's audience: %v", sub, err)
		}
	}
	data, _ := json.Marshal(map[string]any{"human": audience, "human_json": humanJSON(&audience), "valid": valid, "invalid": vectors})
	t.Log("HUMAN_REACTION_VECTOR_JSON " + string(data))
}
