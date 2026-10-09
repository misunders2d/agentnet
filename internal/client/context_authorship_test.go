package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestContextSpeakerSeparatesAgentFromOwner(t *testing.T) {
	names := map[string]string{"owner/laptop|key": `your owner, "Sergey", on this device`}
	claims := map[string]string{"owner/laptop|key": `"Sergey" on Laptop`}
	for _, tc := range []struct {
		name string
		msg  ConvMessage
		want string
	}{
		{"human", ConvMessage{From: "owner/laptop", Key: "key", Kind: "message"}, `your owner, "Sergey"`},
		{"agent", ConvMessage{From: "owner/laptop", Key: "key", Kind: "answer", Origin: "agent:codex", AgentID: "codex", VerifiedAgent: true}, `Agent "codex", answer; host person: your owner, "Sergey"`},
		{"named direct agent", ConvMessage{From: "owner/laptop", Key: "key", Kind: "answer", AgentID: "named-executor"}, `Claimed agent "named-executor", answer; host person: your owner, "Sergey"`},
		{"forwarded agent", ConvMessage{From: "owner/laptop", Claimed: "key", Kind: "answer", Origin: "agent:codex", VerifiedAgent: true, History: true, SyncedFrom: "owner/phone"}, `Agent "codex", answer; host person: "Sergey" on Laptop (as shared by owner/phone, not verified here)`},
		{"unverified excerpt", ConvMessage{From: "owner/laptop", Claimed: "key", Kind: "answer", Origin: "agent:codex", ExcerptPID: "excerpt", SyncedFrom: "other/mac"}, `Claimed agent "codex", answer; host person: "Sergey" on Laptop (as shared by other/mac, not verified here)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextSpeaker(tc.msg, names, claims); !strings.HasPrefix(got, tc.want) {
				t.Fatalf("speaker = %q; want prefix %q", got, tc.want)
			}
		})
	}
}

// Reproduce sharing an old agent reply after dismissing and reinviting it.
// The human request remains present; the answer is no longer attributed to
// the host person, and an unselected ambient message remains private.
func TestContextReinvitePreservesAgentAuthorshipAndRequest(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:1], nil)
	const request = "Create a Linear issue for the Mac launch blocker"
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, request)
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, w.bob, conv, q.ID)
	if _, err = w.bob.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	next, err := w.bob.InviteAgent(tctx(t), conv, w.bob.Address, []string{q.LID, answer.LID}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.bob.ParticipationContext(next.PID, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(c.lines, "\n")
	if len(c.Messages) != 2 || !strings.Contains(got, request) || !strings.Contains(got, `Agent "agentstub", answer; host person:`) || strings.Contains(got, "lunch") {
		t.Fatalf("shared context lost request/provenance or widened scope: %s", got)
	}
}
