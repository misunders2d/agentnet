package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Explicit opt-in: isolated test Hub/identities, real provider, current user
// harness permissions. Named participant questions keep a fresh ephemeral
// background run; no open or saved user conversation is resumed.
func TestLiveOwnAgentTopicClosure(t *testing.T) {
	name := os.Getenv("AGENTNET_LIVE")
	if name != "codex" && name != "claude" {
		t.Skip("set AGENTNET_LIVE=codex|claude and AGENTNET_LIVE_TOPIC_BINARY to a resolved installed binary")
	}
	binary := os.Getenv("AGENTNET_LIVE_TOPIC_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("AGENTNET_LIVE_TOPIC_BINARY must be an absolute resolved binary, avoiding launchers that modify configuration")
	}
	if st, err := os.Stat(binary); err != nil || st.IsDir() {
		t.Fatal("live harness binary unavailable", err)
	}
	h := Harnesses[name]
	original := h
	h.bin = binary
	Harnesses[name] = h
	t.Cleanup(func() { Harnesses[name] = original })
	w := newWorld(t, "")
	if err := w.alice.SetResponder(&Responder{Harness: name, Dir: t.TempDir(), Timeout: 90 * time.Second}); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "")
	if err != nil || !p.Claimable() {
		t.Fatalf("own agent invite: %+v %v", p, err)
	}
	ask := func(body, topic string) ConvMessage {
		t.Helper()
		q, e := w.alice.AskAgentInTopic(tctx(t), p.PID, envelope.KindQuestion, body, topic, nil)
		if e != nil {
			t.Fatal(e)
		}
		ids := map[string]bool{q.ID: true, q.LID: true}
		copies, e := w.alice.SentCopies(q.LID)
		if e != nil {
			t.Fatal(e)
		}
		for _, copy := range copies {
			ids[copy.ID] = true
		}
		end := time.Now().Add(110 * time.Second)
		for time.Now().Before(end) {
			answer, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return ids[m.ReplyTo] })
			if n == 1 {
				if answer.Status() != envelope.StatusDone || strings.TrimSpace(answer.Body) == "" {
					t.Fatalf("live result: %+v", answer)
				}
				t.Logf("synthetic model result topic=%s closed=%v body=%q", answer.Topic, answer.TopicDone, answer.Body)
				return answer
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("live reply exceeded 110 seconds; no automatic retry or auth changes")
		return ConvMessage{}
	}
	first := ask("Without using any tools: this is a synthetic server. Supplied CPU usage is 12%, RAM usage is 31%, and disk usage is 22%. How is this server doing? Answer in one sentence using only those supplied facts.", "new")
	if first.Topic == "" || first.TopicDone {
		t.Fatalf("ordinary first answer closed topic: %+v", first)
	}
	verify := func(want, by string) {
		t.Helper()
		topics, e := w.alice.ChatTopics(conv)
		if e != nil || len(topics) != 1 || topics[0].ID != first.Topic || topics[0].State != want || topics[0].Pending || topics[0].DoneBy != by {
			t.Fatalf("topic state: %+v %v", topics, e)
		}
	}
	verify(TopicActive, "")
	second := ask("Without using any tools, which of the three supplied usage percentages is highest? Answer in one sentence.", first.Topic)
	if second.Topic != first.Topic || second.TopicDone {
		t.Fatalf("ordinary follow-up closed or replaced topic: %+v", second)
	}
	verify(TopicActive, "")
	closed := ask("Please close this topic now. Without using any tools, give a brief closing summary using only the supplied synthetic server facts.", first.Topic)
	if closed.Topic != first.Topic || !closed.TopicDone {
		t.Fatalf("explicit intentional close not honored: %+v", closed)
	}
	verify(TopicDone, DoneByAgent)
}
