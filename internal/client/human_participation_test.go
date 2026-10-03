package client

import (
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// humanTestCaps signs hgp1 for a's live sessions ahead of their own publish.
func humanTestCaps(t *testing.T, a *Agent) {
	t.Helper()
	waitNamedAgentCaps(t, a)
	label, name, _ := protocol.SplitAddress(a.Address)
	var profile protocol.Profile
	if err := a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile); err != nil {
		t.Fatal(err)
	}
	caps := withCap(ownCaps, protocol.CapHumanParticipation)
	for _, session := range profile.Sessions {
		rec := protocol.CapsRecord{Address: a.Address, Session: session, Caps: caps, TS: time.Now().Unix() + 100}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
}
func humanWorld(t *testing.T) (*world, *Agent, string, []string, *agentStub) {
	t.Helper()
	stub := installAgentStub(t)
	w, conv, lids := dmWithHistory(t)
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "guest")
	runAgent(t, carol)
	persons(t, carol)
	if err := carol.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		fakeNotify(a)
		humanTestCaps(t, a)
	}
	return w, carol, conv, lids, stub
}
func humanBodyCount(t *testing.T, a *Agent, conv, body string) int {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.Body == body {
			n++
		}
	}
	return n
}

// BUG-40e: an accepted guest sends without naming its participation (as the
// CLI does): the device's own accepted guest participation is its author
// scope, exactly as when the page names it. A device that is neither a
// member nor an accepted guest still sends nothing.
func TestHumanGuestSendsWithoutNamingItsParticipation(t *testing.T) {
	w, carol, conv, _, _ := humanWorld(t)
	if _, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "before any invitation"}); err == nil {
		t.Fatal("a device outside the DM sent")
	}
	p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "human invite", func() bool { return stateAt(t, carol, p.PID).State == PartInvited })
	if _, err := carol.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "inviter observes human acceptance", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
	sent, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "guest without a pid"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the guest's turn at both members", func() bool {
		return humanBodyCount(t, w.alice, conv, "guest without a pid") == 1 && humanBodyCount(t, w.bob, conv, "guest without a pid") == 1
	})
	msgs, _ := w.alice.ConversationMessages(conv)
	for _, m := range msgs {
		if m.LID == sent.LID && (m.Human == nil || m.Human.AuthorPID != p.PID) {
			t.Fatalf("the guest's turn is not authored under its participation: %+v", m.Human)
		}
	}
}

func TestHumanGuestNativeConsentMessagesFileAndLeave(t *testing.T) {
	w, carol, conv, lids, stub := humanWorld(t)
	rootBefore, rawBefore := rootOf(t, w.alice, conv)
	file := filepath.Join(t.TempDir(), "selected.txt")
	os.WriteFile(file, []byte("SYNTHETIC_SELECTED_BYTES"), 0600)
	selected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "selected file", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, []string{lids[0], selected.LID}, "help in this same DM")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "human invite", func() bool { return stateAt(t, carol, p.PID).State == PartInvited })
	var excerpts int
	carol.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND sub=?`, conv, envelope.SubExcerpt).Scan(&excerpts)
	if excerpts != 0 {
		t.Fatal("human received selected context before acceptance")
	}
	if _, err := carol.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Body: "pre-consent"}); err == nil {
		t.Fatal("pending human sent")
	}
	if _, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "execute"); err == nil {
		t.Fatal("human became executor")
	}
	if _, err := carol.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "inviter observes human acceptance", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
	eventually(t, "accepted exact selected context", func() bool {
		return humanBodyCount(t, carol, conv, "deploy failed at step 3") == 1 && humanBodyCount(t, carol, conv, "selected file") == 1
	})
	if humanBodyCount(t, carol, conv, "unrelated: lunch?") != 0 || humanBodyCount(t, carol, conv, "logs are in the ticket") != 0 {
		t.Fatal("unselected context leaked")
	}
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "shared current turn"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member ordinary message reaches guest and peer", func() bool {
		return humanBodyCount(t, carol, conv, "shared current turn") == 1 && humanBodyCount(t, w.bob, conv, "shared current turn") == 1
	})
	var reply string
	msgs, _ := carol.ConversationMessages(conv)
	for _, m := range msgs {
		if m.LID == sent.LID {
			reply = m.ID
		}
	}
	if _, err := carol.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Body: "human reply", ReplyTo: reply, Files: []OutgoingFile{{Path: file}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "human ordinary reply", func() bool {
		return humanBodyCount(t, w.alice, conv, "human reply") == 1 && humanBodyCount(t, w.bob, conv, "human reply") == 1
	})
	if stateAt(t, carol, p.PID).Claimable() || stub.runs() != 0 {
		t.Fatal("human acquired worker authority")
	}
	if _, err := carol.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both originals observe leave", func() bool {
		return stateAt(t, w.alice, p.PID).State == PartDismissed && stateAt(t, w.bob, p.PID).State == PartDismissed
	})
	if _, err := carol.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Body: "post-leave"}); err == nil {
		t.Fatal("ended human sent")
	}
	private, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "private after leave", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range private.Copies {
		if copy.To == carol.Address {
			t.Fatal("post-end guest recipient")
		}
	}
	eventually(t, "original private follow-up", func() bool { return humanBodyCount(t, w.bob, conv, "private after leave") == 1 })
	if humanBodyCount(t, carol, conv, "private after leave") != 0 {
		t.Fatal("private follow-up leaked")
	}
	rootAfter, rawAfter := rootOf(t, w.alice, conv)
	if rootAfter.ID() != rootBefore.ID() || string(rawBefore) != string(rawAfter) {
		t.Fatal("guest changed original root")
	}
	if !strings.Contains(conv, rootAfter.ID()) {
		t.Fatal("conversation changed")
	}
}
