package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A person who signed in again keeps one chat here, shown under the first
// DM's id. Bringing an agent into that chat must reach the person's current
// DM, with or without shared messages, and the chat must then list it.
func TestMEL736InviteThroughContinuityView(t *testing.T) {
	w := newMEL736OneWorld(t)
	first := mel736OneJoin(t, w, "lost")
	peer := w.admin
	if _, e := peer.CreatePerson(w.ctx, "Colleague"); e != nil {
		t.Fatal(e)
	}
	stopOld := runAgent(t, first)
	runAgent(t, peer)
	waitNamedAgentCaps(t, first)
	waitNamedAgentCaps(t, peer)
	old, _, _ := first.Person()
	conv, e := peer.CreateDM(w.ctx, first.Address)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = peer.SendConv(w.ctx, conv, ConvOutgoing{Body: "before recovery"}); e != nil {
		t.Fatal(e)
	}
	eventually(t, "old DM delivered", func() bool { ms, _ := first.ConversationMessages(conv); return len(ms) > 0 })
	stopOld()
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email}); e != nil {
		t.Fatal(e)
	}
	fresh := mel736OneJoin(t, w, "new")
	runAgent(t, fresh)
	waitNamedAgentCaps(t, fresh)
	next, _, _ := fresh.Person()
	if _, e = peer.refreshPerson(w.ctx, next.Person, false); e != nil {
		t.Fatal(e)
	}
	after, e := peer.SendConv(w.ctx, conv, ConvOutgoing{Body: "after recovery"})
	if e != nil {
		t.Fatal(e)
	}
	// The chat may be opened under the earlier DM's id (the view id is the
	// first DM created); act through that id.
	view := conv
	current, e := peer.MessagingTarget(view)
	if e != nil || current == view {
		t.Fatal("current DM", current, view, e)
	}
	for _, grant := range [][]string{nil, {after.LID}} {
		info, e := peer.InviteAgentInScope(w.ctx, view, peer.Address, "", grant, nil, "", nil)
		if e != nil {
			t.Fatalf("invite through the chat (grant %v): %v", grant, e)
		}
		if info.Conv != current {
			t.Fatalf("invited into %s, current DM %s", info.Conv, current)
		}
		infos, e := peer.MessagingParticipations(view)
		if e != nil {
			t.Fatal(e)
		}
		listed := false
		for _, p := range infos {
			listed = listed || p.PID == info.PID
		}
		if !listed {
			t.Fatal("the chat does not list the invited agent")
		}
		if _, e = peer.DismissParticipation(w.ctx, info.PID); e != nil {
			t.Fatal(e)
		}
	}
	ms, e := peer.MessagingConversationMessages(view)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range ms {
		if m.Body == "before recovery" && !m.OtherDM || m.Body == "after recovery" && m.OtherDM {
			t.Fatalf("other_dm %q = %v", m.Body, m.OtherDM)
		}
	}
}
