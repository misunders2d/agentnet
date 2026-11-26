package client

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// MEL-828, opt-in (AGENTNET_LIVE_NATIVE=agy): the installed agy answers two
// questions of one conversation from one warm process. The second continues
// the same agy conversation and is answered faster than the cold first.
func TestLiveAgyWarm(t *testing.T) {
	if os.Getenv("AGENTNET_LIVE_NATIVE") != "agy" {
		t.Skip("opt-in: AGENTNET_LIVE_NATIVE=agy")
	}
	w := newWorld(t, "")
	setResponder(t, w.bob, "agy", t.TempDir(), 3*time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	pool := warmPoolOf(t, w.bob)
	send := func(body, replyTo string) (string, Message, time.Duration) {
		start := time.Now()
		r, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: body, Kind: envelope.KindQuestion, ReplyTo: replyTo})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if ans, ok := findReply(w.alice, r.ID); ok {
				return r.ID, ans, time.Since(start)
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("no answer to %s", r.ID)
		return "", Message{}, 0
	}
	q1, a1, cold := send("Reply with exactly the word AMBER and nothing else. Use no tools.", "")
	ref := sessionOf(t, w.bob, q1)
	if ref == nil || len(ref.ID) != 36 {
		t.Fatalf("session=%+v", ref)
	}
	eventually(t, "agy kept warm", func() bool { return pool.idleCount() == 1 })
	q2, a2, warm := send("Which word did you reply with in your previous answer? Reply with only that word. Use no tools.", a1.ID)
	if pool.spawns.Load() != 1 {
		t.Fatalf("the second question started another agy process (spawns %d)", pool.spawns.Load())
	}
	if r2 := sessionOf(t, w.bob, q2); r2 == nil || r2.ID != ref.ID {
		t.Fatalf("second session %+v, want %s", r2, ref.ID)
	}
	if !strings.Contains(strings.ToUpper(a2.Body), "AMBER") {
		t.Fatalf("the warm turn lost the conversation: %q", a2.Body)
	}
	t.Logf("real agy: cold answer %v, warm answer %v, conversation %s, answers %q / %q", cold.Round(time.Millisecond), warm.Round(time.Millisecond), ref.ID, a1.Body, a2.Body)
}
