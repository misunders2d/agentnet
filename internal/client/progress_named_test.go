package client

import (
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// signCapsNow replaces each live session's signed capabilities of a with
// caps, at a current timestamp after the session's own record.
func signCapsNow(t *testing.T, a *Agent, caps []string) {
	t.Helper()
	label, name, _ := protocol.SplitAddress(a.Address)
	var prof protocol.Profile
	if err := a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		t.Fatal(err)
	}
	var last int64
	for _, raw := range prof.Caps {
		if r, err := protocol.ParseCapsRecord(raw); err == nil && r.TS > last {
			last = r.TS
		}
	}
	for time.Now().Unix() <= last {
		time.Sleep(50 * time.Millisecond)
	}
	caps = slices.Clone(caps)
	slices.Sort(caps)
	for _, session := range prof.Sessions {
		rec := protocol.CapsRecord{Address: a.Address, Session: session, Caps: caps, TS: time.Now().Unix()}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// without is caps less drop, as an older program lists them: one that
// does not read drop does not read rm1 either when rm1 implies drop. Keep
// rm1's other effective capabilities even if a current program omits their
// redundant explicit names; each old-reader probe drops only what it tests.
func without(caps []string, drop string) []string {
	out := slices.Clone(caps)
	if slices.Contains(caps, protocol.CapRoom) && (drop == protocol.CapRoom || slices.Contains(protocol.RoomImplies, drop)) {
		out = append(out, protocol.RoomImplies...)
		slices.Sort(out)
		out = slices.Compact(out)
	}
	return slices.DeleteFunc(out, func(c string) bool {
		return c == drop || c == protocol.CapRoom && slices.Contains(protocol.RoomImplies, drop)
	})
}

func TestCapabilityFixtureDropsOnlyRequestedReader(t *testing.T) {
	before := protocol.CapsRecord{Caps: []string{protocol.CapEnv2, protocol.CapRoom, protocol.CapRootSync}}
	for _, drop := range []string{protocol.CapAgentReaction, protocol.CapConvClear, protocol.CapRoom, protocol.CapRootSync} {
		after := protocol.CapsRecord{Caps: without(before.Caps, drop)}
		if after.Reads(drop) {
			t.Fatalf("fixture still reads dropped %s", drop)
		}
		for _, keep := range append(slices.Clone(protocol.RoomImplies), protocol.CapEnv2, protocol.CapRootSync) {
			if keep != drop && before.Reads(keep) && !after.Reads(keep) {
				t.Fatalf("dropping %s also removed effective %s", drop, keep)
			}
		}
	}
}

// withCap is caps plus add, sorted and once, as a signed record requires.
func withCap(caps []string, add string) []string {
	caps = append(slices.Clone(caps), add)
	slices.Sort(caps)
	return slices.Compact(caps)
}

// A named executor's progress carries its exact agent, is admitted only for
// the request that targeted it, and waits until the requester reads both
// prg1 and agi1, across a restart; then it is delivered once.
func TestProgressNamedExecutorProvenanceAndComboCaps(t *testing.T) {
	w := newWorld(t, "")
	stopBob := runAgent(t, w.bob)
	progressReader(t, w.alice, w.bob)
	waitNamedAgentCaps(t, w.bob)
	agent := protocol.NewID()
	req, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "named request",
		Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: agent}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "named request at bob", func() bool { return inboxCount(t, w.bob, "id = ?", req.ID) == 1 })

	sent, err := w.bob.SendProgress(tctx(t), w.alice.Address, req.ID, "NAMED PROGRESS", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "named progress at alice", func() bool {
		var agentID, status string
		return w.alice.store.db.QueryRow(`SELECT coalesce(agent_id,''), coalesce(status,'') FROM inbox WHERE id=?`, sent.ID).Scan(&agentID, &status) == nil &&
			agentID == agent && status == envelope.StatusProgress
	})
	if reply, err := w.alice.store.replyTo(req.ID, w.bob.Address); err != nil || reply != "" {
		t.Fatalf("progress counted as the answer: %q %v", reply, err)
	}

	// Wrong agent or wrong request: refused at the requester, never stored.
	recipient, _ := w.alice.Self().Recipient()
	forge := func(agentID, replyTo string) string {
		in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Body: "forged", ReplyTo: replyTo, Status: envelope.StatusProgress, AgentID: agentID}
		env, err := envelope.Seal(in, w.bob.id.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.alice.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	for _, id := range []string{forge(protocol.NewID(), req.ID), forge(agent, protocol.NewID())} {
		if inboxCount(t, w.alice, "id = ?", id) != 0 {
			t.Fatal("progress with wrong provenance stored")
		}
	}

	// alice's sessions lack agi1, then prg1: each update waits, never downgraded.
	signCapsNow(t, w.alice, without(ownCaps, protocol.CapAgentIdentity))
	noAgent, err := w.bob.SendProgress(tctx(t), w.alice.Address, req.ID, "WAITS FOR AGI1", 0, false)
	if err != nil || noAgent.State != stateConvWaiting {
		t.Fatalf("progress without agi1: %+v %v", noAgent, err)
	}
	signCapsNow(t, w.alice, without(ownCaps, protocol.CapProgress))
	noProgress, err := w.bob.SendProgress(tctx(t), w.alice.Address, req.ID, "WAITS FOR PRG1", 0, false)
	if err != nil || noProgress.State != stateConvWaiting {
		t.Fatalf("progress without prg1: %+v %v", noProgress, err)
	}
	feats, err := w.bob.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	w.bob.releaseConv(tctx(t), feats)
	w.bob.FlushOutbox(tctx(t))
	for _, id := range []string{noAgent.ID, noProgress.ID} {
		if s, _, _, _ := w.bob.store.outboxState(id); s != stateConvWaiting {
			t.Fatalf("released with one capability missing: %s", s)
		}
	}

	// Restart: the held copies stay; both capabilities back release each once.
	stopBob()
	w.bob.Close()
	bob, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	signCapsNow(t, w.alice, ownCaps)
	bob.releaseConv(tctx(t), feats)
	if err = bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{noAgent.ID, noProgress.ID} {
		eventually(t, "released named progress", func() bool { return inboxCount(t, w.alice, "id = ? AND agent_id = ?", id, agent) == 1 })
	}
	bob.releaseConv(tctx(t), feats)
	bob.FlushOutbox(tctx(t))
	time.Sleep(200 * time.Millisecond)
	if n := inboxCount(t, w.alice, "reply_to = ? AND status = ?", req.ID, envelope.StatusProgress); n != 3 {
		t.Fatalf("named progress copies %d, want 3", n)
	}
}
