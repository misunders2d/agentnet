package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Owner, 2026-11-09: an agent addressed another as plain text ("Bezos Pi,
// …") because its prompt held only its requester's exact mention, and two
// agents of one person were both "Your Pi". Every run now gets the exact
// mention of each member and each present agent, labeled owner · device.
func TestAgentPromptListsExactMentionsOfEveryoneHere(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
	own, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "own agent")
	if err != nil {
		t.Fatal(err)
	}
	if !own.Claimable() {
		if _, err = w.alice.AcceptParticipation(tctx(t), own.PID); err != nil {
			t.Fatal(err)
		}
	}
	other := participate(t, w, conv, nil, nil) // bob's agent
	eventually(t, "both agents active at alice", func() bool {
		return stateAt(t, w.alice, own.PID).Claimable() && stateAt(t, w.alice, other).Claimable()
	})
	q, err := w.alice.AskAgent(tctx(t), own.PID, envelope.KindQuestion, "who is here?")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, q.ID)
	prompt := stub.last()
	alice, _, _ := w.alice.Person()
	bob := stateAt(t, w.alice, other).Host
	for _, want := range []string{
		"(agentnet:person/" + alice.Person + ")",
		"(agentnet:person/" + bob.Person + ")",
		"you (this agent)",
		"(agentnet:agent/" + own.PID + ")",
		"another person's agent",
		"(agentnet:agent/" + other + ")",
		" · " + DeviceWords(w.bob.Address) + "](agentnet:agent/" + other + ")",
		" · " + DeviceWords(w.alice.Address) + "](agentnet:agent/" + own.PID + ")",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt[strings.Index(prompt, "## Who is here"):])
		}
	}
	// Owner, 2026-11-10: an agent posted "[@Sergey\x27s agent](…)". Every
	// label this prompt gives is "owner's agent · device" with a literal
	// apostrophe, never a shell or JS escape of it.
	roster := prompt[strings.Index(prompt, "## Who is here"):]
	if want := "[@" + alice.Label + "'s "; !strings.Contains(roster, want) {
		t.Errorf("roster lacks the literal apostrophe label %q:\n%s", want, roster)
	}
	for _, escaped := range []string{`\x27`, `\'`, `\u0027`, `&#39;`} {
		if strings.Contains(roster, escaped) {
			t.Errorf("roster escapes an apostrophe as %s:\n%s", escaped, roster)
		}
	}
}

func TestAgentMentionLabelNamesOwnerAgentAndDevice(t *testing.T) {
	host := PersonInfo{Label: "Sergey", Address: "admin/linux-laptop"}
	if got := agentMentionLabel(host, "Pi"); got != "Sergey's Pi · Linux laptop" {
		t.Fatalf("label %q", got)
	}
	if got := agentMentionLabel(PersonInfo{Address: "admin/bezos"}, "name unavailable; use verified PID"); got != "Someone's agent · Bezos" {
		t.Fatalf("fallback label %q", got)
	}
}
