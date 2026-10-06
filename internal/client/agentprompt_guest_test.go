package client

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// BUG-03: the DM assistant's prompt names the real asker and every reader.
// A guest's question is named as the guest's, an outside person present
// only temporarily, never as the other member's; the reply's readers are
// the two members and the guests the request's audience names that are
// still present. A member's question while a guest is present is the
// member's, and the guest is still named as a reader.
func TestAgentPromptNamesGuestAskerAndReaders(t *testing.T) {
	w, carol, conv, _, stub, ap, _ := guestAssistant(t)
	if err := w.bob.Approve(carol.Address); err != nil {
		t.Fatal(err)
	}
	guest := "a guest whose chosen name is \"Person of " + carol.Address + "\" (" + carol.Address + ")"
	readers := "It also reaches the guests present now, outside people invited only temporarily: " + guest + "."

	q, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "guest asks the assistant")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, carol, conv, q.LID)
	prompt := stub.last()
	if !strings.Contains(prompt, "## Question from "+guest+", an outside person present in this conversation only temporarily, as a guest\n") {
		t.Fatalf("the guest is not named as the asker:\n%s", prompt)
	}
	if strings.Contains(prompt, "## Question from another person") {
		t.Fatalf("the guest's question is said to come from the other member:\n%s", prompt)
	}
	if !strings.Contains(prompt, "your reply is sent to both of them. "+readers) {
		t.Fatalf("the guest is not named as a reader:\n%s", prompt)
	}

	own, err := w.alice.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "member asks beside the guest")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, own.ID)
	prompt = stub.last()
	if !strings.Contains(prompt, "## Question from another person, who calls themselves \"Person of "+w.alice.Address+"\", writing from their device Alice ("+w.alice.Address+", key ") || !strings.Contains(prompt, readers) {
		t.Fatalf("a member's question beside a guest:\n%s", prompt)
	}
}
