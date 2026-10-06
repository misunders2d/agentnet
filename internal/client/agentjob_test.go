package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// agentScript is a fake harness for a participation's agent: it keeps the
// prompt of its last run and appends every run to $AGENT_LOG, and answers
// as the file $AGENT_LOG.mode says: emotion (a reply with its emotion
// line), bare (no emotion line), bad (a malformed one), sleep (until
// stopped, then a late reply).
const agentScript = `#!/bin/sh
cat > "$AGENT_LOG.last"
{ echo "=== run $*"; cat "$AGENT_LOG.last"; } >> "$AGENT_LOG"
mode=$(cat "$AGENT_LOG.mode" 2>/dev/null || echo emotion)
case "$mode" in
emotion) printf 'the deploy failed at step 3\n\nemotion: concerned\n' ;;
closed) printf 'Finished the work\ntopic: done\nemotion: calm\n' ;;
bare) printf 'the deploy failed at step 3\n' ;;
bad) printf 'the deploy failed at step 3\nemotion: Very Sad\n' ;;
sleep) touch "$AGENT_LOG.started"; sleep 30 & wait; printf 'late reply\nemotion: calm\n' ;;
propose) printf 'AGENTNET: PROPOSE-TASK\nRestart the deploy from step 3.\nemotion: calm\n' ;;
esac
`

type agentStub struct{ log, dir string }

func installAgentStub(t *testing.T) *agentStub {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent.sh")
	os.WriteFile(bin, []byte(agentScript), 0o700)
	s := &agentStub{log: filepath.Join(dir, "log"), dir: t.TempDir()}
	t.Setenv("AGENT_LOG", s.log)
	Harnesses["agentstub"] = harness{bin: bin, question: []string{"--question-mode"}, task: []string{"--task-mode"}, stdin: true}
	t.Cleanup(func() { delete(Harnesses, "agentstub") })
	return s
}

func (s *agentStub) runs() int {
	data, _ := os.ReadFile(s.log)
	return strings.Count(string(data), "=== run")
}

func (s *agentStub) last() string {
	data, _ := os.ReadFile(s.log + ".last")
	return string(data)
}

func (s *agentStub) mode(m string) { os.WriteFile(s.log+".mode", []byte(m), 0o600) }

// agentWorld is dmWithHistory with bob's daemon stop kept.
func agentWorld(t *testing.T) (w *world, conv string, lids []string, stopBob func()) {
	t.Helper()
	w = newWorld(t, "")
	runAgent(t, w.alice)
	stopBob = runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv = newDM(t, w.alice, w.bob)
	for _, body := range []string{"deploy failed at step 3", "logs are in the ticket", "unrelated: lunch?"} {
		if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "bob to hold the history", func() bool { return len(convBodies(t, w.bob, conv)) == 3 })
	msgs, _ := w.alice.ConversationMessages(conv)
	for _, m := range msgs {
		lids = append(lids, m.LID)
	}
	return w, conv, lids, stopBob
}

// participate: alice invites bob's agent with grant and task keys, bob
// accepts, and both see it active.
func participate(t *testing.T, w *world, conv string, grant, taskKeys []string) string {
	t.Helper()
	p, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, grant, taskKeys, "please look")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the invite", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	if _, err := w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to see it active", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	return p.PID
}

func convMsg(t *testing.T, a *Agent, conv string, match func(ConvMessage) bool) (ConvMessage, int) {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var found ConvMessage
	n := 0
	for _, m := range msgs {
		if match(m) {
			found, n = m, n+1
		}
	}
	return found, n
}

// replyAt waits for the reply to request id in a's copy of conv.
func replyAt(t *testing.T, a *Agent, conv, id string) ConvMessage {
	t.Helper()
	var r ConvMessage
	eventually(t, "the reply to "+id+" at "+a.Address, func() bool {
		var n int
		r, n = convMsg(t, a, conv, func(m ConvMessage) bool { return m.ReplyTo == id })
		return n == 1
	})
	return r
}

func jobState(t *testing.T, a *Agent, id string) string {
	t.Helper()
	s, err := a.store.jobState(id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return s
}

func noReply(t *testing.T, a *Agent, conv, id string) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	if _, n := convMsg(t, a, conv, func(m ConvMessage) bool { return m.ReplyTo == id }); n != 0 {
		t.Fatalf("%s got a reply to %s", a.Address, id)
	}
}

// E1, E2, E6: an active participation's agent answers in the conversation,
// with exactly the granted messages and the request once, a fresh prompt
// per request, the emotion it emitted, and follow-ups from either member;
// the host's own request is one message and one job.
func TestAgentAnswersInTheConversation(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:2], nil)

	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "what failed?")
	if err != nil {
		t.Fatal(err)
	}
	ans := replyAt(t, w.alice, conv, q.ID)
	bob, _, _ := w.bob.store.selfPerson(w.bob.Address)
	if ans.Kind != envelope.KindAnswer || ans.PID != pid || ans.Origin != "agent:agentstub" || ans.Emotion != "concerned" ||
		ans.Body != "the deploy failed at step 3" || ans.From != w.bob.Address || ans.Key != bob.info.Fingerprint || ans.Target != nil {
		t.Fatalf("answer at alice: %+v", ans)
	}
	prompt := st.last()
	for _, want := range []string{"deploy failed at step 3", "logs are in the ticket", "please look", "emotion: WORD", "## Question from another person, who calls themselves \"Person of " + w.alice.Address + "\", writing from their device Alice (" + w.alice.Address + ", key ", "reply with your question for them"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "lunch") || strings.Count(prompt, "what failed?") != 1 {
		t.Fatalf("prompt has an ungranted message or the request not once:\n%s", prompt)
	}
	if req, _ := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.ID == q.ID }); req.Job != stateAnswered || req.State != stateAnswered {
		t.Fatalf("bob's request row: %+v", req)
	}
	if req, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.ID == q.ID }); req.Job != "" {
		t.Fatalf("alice runs nothing, yet her request shows a job: %+v", req)
	}

	// The host's own person asks: one message in the conversation, one job,
	// answered once; the other member holds it as history only.
	own, err := w.bob.AskAgent(tctx(t), pid, envelope.KindQuestion, "and the fix?")
	if err != nil {
		t.Fatal(err)
	}
	ownAns := replyAt(t, w.alice, conv, own.ID)
	if ownAns.Emotion != "concerned" || ownAns.PID != pid {
		t.Fatalf("answer to bob's own question: %+v", ownAns)
	}
	if m, n := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == own.LID }); n != 1 || m.Dir != "out" || m.Job != stateAnswered {
		t.Fatalf("bob lists his own request %d times: %+v", n, m)
	}
	if inboxCount(t, w.bob, `id = ? AND local = 1 AND state = ?`, own.ID, stateAnswered) != 1 {
		t.Fatal("no local job for bob's own request")
	}
	if atAlice, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.ID == own.ID }); atAlice.State != "" || atAlice.Job != "" {
		t.Fatalf("bob's request to his agent is held at alice: %+v", atAlice)
	}
	if msgs, _ := w.bob.Inbox(false, false); len(msgs) != 0 {
		for _, m := range msgs {
			if m.ID == own.ID {
				t.Fatal("the local job is listed as received")
			}
		}
	}
	prompt = st.last()
	if !strings.Contains(prompt, "what failed?") || strings.Count(prompt, "and the fix?") != 1 ||
		!strings.Contains(prompt, "You (the agent), answer: the deploy failed") || !strings.Contains(prompt, "Your owner, \"Person of "+w.bob.Address+"\", on this device asks you the question below") {
		t.Fatalf("follow-up prompt:\n%s", prompt)
	}

	// A follow-up from alice again, without a new invite.
	q3, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "anything else?")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, q3.ID)
	if n := st.runs(); n != 3 {
		t.Fatalf("the agent ran %d times, want 3", n)
	}
	if !strings.Contains(mustRead(t, st.log), "=== run --question-mode") {
		t.Fatal("not run in question mode")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The context given to the agent is bounded as rendered, newest kept,
// and says what it left out.
func TestAgentContextBound(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:2], nil)
	old := agentContextBytes
	agentContextBytes = 90 // one line naming its speaker as a person on a device
	t.Cleanup(func() { agentContextBytes = old })
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "what failed?")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, q.ID)
	prompt := st.last()
	if !strings.Contains(prompt, "(1 earlier message(s) left out to stay within 90 bytes.)") ||
		strings.Contains(prompt, "deploy failed at step 3\n") || !strings.Contains(prompt, "logs are in the ticket") {
		t.Fatalf("bounded prompt:\n%s", prompt)
	}
	info := stateAt(t, w.bob, pid)
	c, err := w.bob.agentContext(info, "", 90)
	if err != nil || c.Bytes > 90 || c.Omitted == 0 || len(c.lines) != len(c.Messages) {
		t.Fatalf("context: %+v %v", c, err)
	}
}

// E3: a task runs only with authority: the host's person accepting it once,
// the invite's task keys naming the asker's key, or a task grant for that
// exact key; a request verified by any other key never runs.
func TestAgentTaskAuthority(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	aliceFP := w.alice.id.Public(w.alice.Address).Fingerprint()

	pid := participate(t, w, conv, nil, nil)
	task, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "restart the deploy")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the task to wait for bob", func() bool { return jobState(t, w.bob, task.ID) == stateAwaiting })
	review, _ := w.bob.Review()
	if st.runs() != 0 || len(review) != 1 || review[0].ID != task.ID {
		t.Fatalf("an unaccepted task ran (%d) or is not in review (%+v)", st.runs(), review)
	}
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	if res := replyAt(t, w.alice, conv, task.ID); res.Kind != envelope.KindResult || !strings.Contains(mustRead(t, st.log), "=== run --task-mode") {
		t.Fatalf("result: %+v", res)
	}

	pid2 := participate(t, w, conv, nil, []string{aliceFP})
	task2, err := w.alice.AskAgent(tctx(t), pid2, envelope.KindTask, "rotate the logs")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, task2.ID)

	pid3 := participate(t, w, conv, nil, nil)
	if _, err := w.bob.GrantTasks(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	task3, err := w.alice.AskAgent(tctx(t), pid3, envelope.KindTask, "clear the cache")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, task3.ID)
	if st.runs() != 3 {
		t.Fatalf("runs %d, want 3", st.runs())
	}

	// The same requests under another key (alice's replaced, say) wait:
	// the task keys and the grant are for her key, and it is no member
	// device key here.
	other := "0000beef-0000beef-0000beef-0000beef"
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		in := agentRequest(t, w, conv, pid2, kind)
		if _, err := w.bob.store.addConvInbox(in, other, stateAgentWaiting, false, nil); err != nil {
			t.Fatal(err)
		}
		notifyDaemon(w.bob.home)
		time.Sleep(300 * time.Millisecond)
		if s := jobState(t, w.bob, in.ID); s != stateAgentWaiting || st.runs() != 3 {
			t.Fatalf("a %s under another key: %s, runs %d", kind, s, st.runs())
		}
	}
}

// agentRequest is a request from alice to bob's agent in pid, as bob would
// hold it (not sent).
func agentRequest(t *testing.T, w *world, conv, pid, kind string) envelope.Inner {
	t.Helper()
	_, raw, _, _ := w.bob.store.conversation(conv)
	bobFP := w.bob.id.Public(w.bob.Address).Fingerprint()
	return envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(),
		Kind: kind, Body: "forged " + kind, Conv: conv, LID: protocol.NewID(), Root: raw, PID: pid,
		Target: &envelope.Target{Address: w.bob.Address, Fingerprint: bobFP}}
}

// E4: a dismissal stops everything not yet handed over: a request not yet
// run never runs, a running one is stopped and its output kept here, an
// output finished late is not stored for sending, and one queued is held
// back.
func TestAgentDismissal(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, stopBob := agentWorld(t)

	pid := participate(t, w, conv, nil, nil)
	before, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "before")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ? AND state = ?`, before.ID, stateAgentWaiting) == 1 })
	if _, err := w.alice.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the dismissal", func() bool { return stateAt(t, w.bob, pid).State == PartDismissed })
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	notifyDaemon(w.bob.home)
	eventually(t, "not run", func() bool { return jobState(t, w.bob, before.ID) == stateNotRun })
	if st.runs() != 0 {
		t.Fatal("a dismissed participation's request ran")
	}

	st.mode("sleep")
	pid2 := participate(t, w, conv, nil, nil)
	during, err := w.alice.AskAgent(tctx(t), pid2, envelope.KindQuestion, "slow")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent to start", func() bool { _, err := os.Stat(st.log + ".started"); return err == nil })
	if _, err := w.alice.DismissParticipation(tctx(t), pid2); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run to stop", func() bool { return jobState(t, w.bob, during.ID) == stateNotDelivered })
	noReply(t, w.alice, conv, during.ID)

	// Finished late: the output is decided on where it would be stored.
	st.mode("emotion")
	if err := w.bob.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	pid3 := participate(t, w, conv, nil, nil)
	late, err := w.alice.AskAgent(tctx(t), pid3, envelope.KindQuestion, "late")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ? AND state = ?`, late.ID, stateAgentWaiting) == 1 })
	j := claimAt(t, w.bob, late.ID)
	if _, err := w.alice.DismissParticipation(tctx(t), pid3); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the dismissal", func() bool { return stateAt(t, w.bob, pid3).State == PartDismissed })
	w.bob.finishAgent(tctx(t), j, &Responder{Harness: "agentstub"}, envelope.StatusDone, "late reply\nemotion: calm")
	if m, _ := w.bob.store.inboxMessage(late.ID); m.State != stateNotDelivered || !strings.Contains(m.Detail, "late reply") {
		t.Fatalf("late output: %+v", m)
	}
	if n := count(t, w.bob, "outbox WHERE reply_to = '"+late.ID+"'"); n != 0 {
		t.Fatal("a late output was stored for sending")
	}
	noReply(t, w.alice, conv, late.ID)

	// Queued, then the participation ends: held back, kept here.
	pid4 := participate(t, w, conv, nil, nil)
	queued, err := w.alice.AskAgent(tctx(t), pid4, envelope.KindQuestion, "queued")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ? AND state = ?`, queued.ID, stateAgentWaiting) == 1 })
	stopBob()
	injectFaults(w.bob).add("POST", "/v1/messages", 1, false)
	j = claimAt(t, w.bob, queued.ID)
	w.bob.finishAgent(tctx(t), j, &Responder{Harness: "agentstub"}, envelope.StatusDone, "queued reply\nemotion: calm")
	var out, outState string
	if err := w.bob.store.db.QueryRow(`SELECT id, state FROM outbox WHERE reply_to = ?`, queued.ID).Scan(&out, &outState); err != nil || outState != stateQueued {
		t.Fatalf("the output is %q (%v), want queued", outState, err)
	}
	if _, err := w.bob.DismissParticipation(tctx(t), pid4); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var errText string
	w.bob.store.db.QueryRow(`SELECT state, coalesce(error, '') FROM outbox WHERE id = ?`, out).Scan(&outState, &errText)
	if outState != stateNotDelivered || !strings.Contains(errText, "dismissed") || jobState(t, w.bob, queued.ID) != stateNotDelivered {
		t.Fatalf("queued output after dismissal: %s %q, job %s", outState, errText, jobState(t, w.bob, queued.ID))
	}
	noReply(t, w.alice, conv, queued.ID)
}

// claimAt claims request id at a as its worker would (a has no responder,
// so its own worker takes nothing).
func claimAt(t *testing.T, a *Agent, id string) job {
	t.Helper()
	j, ok, _, _, err := a.store.claimAgentPage("agentstub", a.Address, a.id.Public(a.Address).Fingerprint(), 0, agentPage)
	if err != nil || !ok || j.ID != id {
		t.Fatalf("claim: %+v %v %v", j, ok, err)
	}
	return j
}

// A running request stops when its asker's person is frozen here, as for
// a dismissal: any local change is looked at.
func TestAgentStopsWhenAMemberFreezes(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	st.mode("sleep")
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "slow")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent to start", func() bool { _, err := os.Stat(st.log + ".started"); return err == nil })
	freezeAlice(t, w)
	eventually(t, "the run to stop", func() bool { return jobState(t, w.bob, q.ID) == stateNotDelivered })
	noReply(t, w.alice, conv, q.ID)
}

// freezeAlice shows bob a different person record from alice's device.
func freezeAlice(t *testing.T, w *world) {
	t.Helper()
	freeze(t, w.bob, w.alice)
}

// E5, E7: nothing runs while a record of the participation is held (a
// dismissal following something not held here); a reply without a valid
// emotion line is not sent, kept for the host's person, and runs again only
// when they say so.
func TestAgentHeldAndEmotion(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)

	stop := protocol.ParticipationEvent{V: 1, Conv: conv, PID: pid, Type: protocol.EventDismiss, Prev: strings.Repeat("ab", 32), TS: time.Now().Unix(),
		Author: stateAuthor(t, w.alice)}
	stop.Sign(w.alice.id.Sign)
	raw, _ := json.Marshal(stop)
	if err := w.bob.store.addParticipationEvent(stop, raw); err != nil {
		t.Fatal(err)
	}
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "held?")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ?`, q.ID) == 1 })
	time.Sleep(300 * time.Millisecond)
	if s := jobState(t, w.bob, q.ID); s != stateAgentWaiting || st.runs() != 0 {
		t.Fatalf("with a record held: %s, runs %d", s, st.runs())
	}

	// A reply without an emotion line, or with one that cannot be read, is
	// delivered all the same, shown neutral (MEL-434): a formatting slip is
	// not a decision for a person, and nothing runs again for it.
	pid2 := participate(t, w, conv, nil, nil)
	for _, mode := range []string{"bare", "bad"} {
		st.mode(mode)
		runs := st.runs()
		q, err := w.alice.AskAgent(tctx(t), pid2, envelope.KindQuestion, "feel? "+mode)
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "answered", func() bool { return jobState(t, w.bob, q.ID) == stateAnswered })
		if a := replyAt(t, w.alice, conv, q.ID); a.Emotion != "neutral" || a.Body != "the deploy failed at step 3" {
			t.Fatalf("%s: %+v", mode, a)
		}
		time.Sleep(200 * time.Millisecond)
		if st.runs() != runs+1 {
			t.Fatalf("%s: ran %d time(s)", mode, st.runs()-runs)
		}
		if review, _ := w.bob.Review(); len(review) != 0 {
			t.Fatalf("%s: a missing emotion became a decision: %+v", mode, review)
		}
	}
}

func stateAuthor(t *testing.T, a *Agent) protocol.EventAuthor {
	t.Helper()
	p, _, _ := a.store.selfPerson(a.Address)
	return protocol.EventAuthor{Person: p.info.Person, Roster: p.info.Roster, Address: a.Address, Fingerprint: p.info.Fingerprint}
}

// E8: address approval never runs a DM request: without a participation
// id it is the person's; the legacy claim takes no conversation row.
func TestAgentLegacyIsolation(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	q, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "for bob himself"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ? AND state = ?`, q.ID, stateConvHeld) == 1 })
	pid := participate(t, w, conv, nil, nil)
	in := agentRequest(t, w, conv, pid, envelope.KindQuestion)
	if _, err := w.bob.store.addConvInbox(in, w.alice.id.Public(w.alice.Address).Fingerprint(), stateAccepted, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := w.bob.store.claimJob("legacy"); ok {
		t.Fatal("the legacy claim took a conversation row")
	}
	time.Sleep(300 * time.Millisecond)
	if jobState(t, w.bob, q.ID) != stateConvHeld {
		t.Fatal("a DM question for the person ran")
	}
	notifyDaemon(w.bob.home)
	eventually(t, "only the participation's request to run", func() bool { return jobState(t, w.bob, in.ID) == stateAnswered })
	if st.runs() != 1 {
		t.Fatalf("runs %d, want 1", st.runs())
	}
}

// E9: a restart interrupts a running request (never rerun on its own) and
// then runs what waited, once.
func TestAgentRestart(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, stopBob := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	st.mode("sleep")
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "slow")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent to start", func() bool { _, err := os.Stat(st.log + ".started"); return err == nil })
	stopBob()
	if s := jobState(t, w.bob, q.ID); s != stateInterrupt {
		t.Fatalf("after stopping: %s", s)
	}
	st.mode("emotion")
	q2, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "while down")
	if err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	replyAt(t, w.alice, conv, q2.ID)
	time.Sleep(300 * time.Millisecond)
	if jobState(t, w.bob, q.ID) != stateInterrupt || st.runs() != 2 {
		t.Fatalf("interrupted %s, runs %d (want 2)", jobState(t, w.bob, q.ID), st.runs())
	}
}

// Head of line: requests that cannot run (a participation only invited, one
// not held here, tasks without authority) never keep a later one from
// running, however many: each page is one bounded transaction, and the
// worker goes on to the next at once.
func TestAgentHeadOfLine(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, stopBob := agentWorld(t)
	invited, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the invite", func() bool { return stateAt(t, w.bob, invited.PID).State == PartInvited })
	pid := participate(t, w, conv, nil, nil)
	aliceFP := w.alice.id.Public(w.alice.Address).Fingerprint()
	for i := range 2*agentPage + 5 {
		p := invited.PID
		if i%2 == 1 {
			p = protocol.NewID() // no event of it here
		}
		in := agentRequest(t, w, conv, p, envelope.KindQuestion)
		if _, err := w.bob.store.addConvInbox(in, aliceFP, stateAgentWaiting, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	var tasks []string
	for range 3 {
		task, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "unauthorized")
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task.ID)
	}
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "last in line")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ?`, q.ID) == 1 })
	stopBob()
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	// Fixture delivery may already have advanced the daemon's sweep. Start
	// this bounded-page check from the first request, with the worker stopped.
	w.bob.agentSweep = agentSweep{}
	// The first page holds only requests of the invited participation: the
	// worker reports that it goes on, without running anything.
	if !w.bob.runNext(tctx(t), nil) || st.runs() != 0 {
		t.Fatalf("after one page: runs %d", st.runs())
	}
	if !w.bob.runNext(tctx(t), nil) || jobState(t, w.bob, q.ID) != stateAnswered || st.runs() != 1 {
		t.Fatalf("after the next page: %s, runs %d", jobState(t, w.bob, q.ID), st.runs())
	}
	for _, id := range tasks {
		if s := jobState(t, w.bob, id); s != stateAwaiting {
			t.Fatalf("task %s: %s", id, s)
		}
	}
	if n := inboxCount(t, w.bob, `state = ?`, stateAgentWaiting); n != 2*agentPage+5 {
		t.Fatalf("%d still waiting", n)
	}
	// A complete look that found nothing is not repeated until something
	// changes here.
	for w.bob.runNext(tctx(t), nil) {
	}
	if !w.bob.agentSweep.idle {
		t.Fatal("the look did not settle")
	}
	replyAt(t, w.alice, conv, q.ID)
}

// Race: a claim decides and marks running in one write transaction, so a
// dismissal or a freeze written meanwhile (here: by another connection, as
// another process would) cannot land between the decision and the write;
// once written, the running job is told to stop.
func TestAgentClaimIsAtomic(t *testing.T) {
	w, conv, _, stopBob := agentWorld(t)
	_, dbPath := paths(w.bob.home)
	other, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(100)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	alicePerson, _, _ := w.alice.store.selfPerson(w.alice.Address)
	author := stateAuthor(t, w.alice)
	// Each write is made ready first: the hook runs within bob's claim
	// transaction, where bob's own store cannot be read.
	for _, c := range []struct {
		name  string
		write func(pid string) func() error
	}{
		{"dismissal", func(pid string) func() error {
			info := stateAt(t, w.bob, pid)
			ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: pid, Type: protocol.EventDismiss, Prev: info.Decision, TS: time.Now().Unix(), Author: author}
			ev.Sign(w.alice.id.Sign)
			raw, _ := json.Marshal(ev)
			return func() error {
				_, err := other.Exec(`INSERT INTO participation_events(hash, conv, pid, type, author, event, received_at, prev) VALUES(?, ?, ?, ?, ?, ?, 0, ?)`,
					ev.Hash(), conv, pid, ev.Type, ev.Author.Fingerprint, string(raw), ev.Prev)
				return err
			}
		}},
		{"freeze", func(string) func() error { // the last case: it ends the DM
			return func() error {
				_, err := other.Exec(`UPDATE persons SET state = ? WHERE person = ?`, personConflict, alicePerson.info.Person)
				return err
			}
		}},
	} {
		name := c.name
		t.Run(name, func(t *testing.T) {
			pid := participate(t, w, conv, nil, nil)
			q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, name)
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ? AND state = ?`, q.ID, stateAgentWaiting) == 1 })
			if name == "freeze" {
				stopBob() // the last case: bob's daemon is not needed after this
			}
			write := c.write(pid)
			var during error
			beforeAgentClaim = func() { during = write() }
			t.Cleanup(func() { beforeAgentClaim = func() {} })
			j := claimAt(t, w.bob, q.ID)
			beforeAgentClaim = func() {}
			if during == nil {
				t.Fatal("a write landed between the claim's decision and its write")
			}
			if err := write(); err != nil {
				t.Fatalf("the write itself fails: %v", err)
			}
			if why := w.bob.agentStop(j); why == "" {
				t.Fatal("the running job is not told to stop")
			}
		})
	}
}

// postHook runs after each message a daemon hands to the Hub.
type postHook struct {
	base  http.RoundTripper
	after func(env envelope.Envelope)
}

func (h postHook) RoundTrip(r *http.Request) (*http.Response, error) {
	var env envelope.Envelope
	if r.Method == "POST" && r.URL.Path == "/v1/messages" && r.GetBody != nil {
		if b, err := r.GetBody(); err == nil {
			json.NewDecoder(b).Decode(&env)
			b.Close()
		}
	}
	resp, err := h.base.RoundTrip(r)
	if err == nil && env.ID != "" {
		h.after(env)
	}
	return resp, err
}

// Every hand-over is decided on from what is stored just before it: once a
// dismissal, or a member frozen, is stored after one output went out, the
// next queued output is held back (and stays so), and a plain message to a
// frozen person stays queued.
func TestAgentOutputRecheckedAtEachHandOver(t *testing.T) {
	t.Parallel()
	for _, stop := range []string{"dismissal", "freeze"} {
		t.Run(stop, func(t *testing.T) {
			w, conv, _, stopBob := agentWorld(t)
			pid := participate(t, w, conv, nil, nil)
			var ids []string
			for _, body := range []string{"first", "second"} {
				q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, body)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, q.ID)
			}
			eventually(t, "bob to hold both", func() bool { return inboxCount(t, w.bob, `pid = ? AND state = ?`, pid, stateAgentWaiting) == 2 })
			stopBob()
			injectFaults(w.bob).add("POST", "/v1/messages", 3, false)
			for _, id := range ids {
				w.bob.finishAgent(tctx(t), claimAt(t, w.bob, id), &Responder{Harness: "agentstub"}, envelope.StatusDone, "reply\nemotion: calm")
			}
			plain, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "a plain message"})
			if err != nil || plain.State != stateQueued {
				t.Fatalf("plain message: %+v %v", plain, err)
			}
			var outputs []string
			w.bob.hub.http.Transport = postHook{w.bob.hub.http.Transport, func(env envelope.Envelope) {
				if env.Kind != envelope.KindAnswer {
					return
				}
				if outputs = append(outputs, env.ID); len(outputs) > 1 {
					return
				}
				if stop == "dismissal" {
					if _, err := w.bob.DismissParticipation(tctx(t), pid); err != nil {
						t.Error(err)
					}
				} else {
					freezeAlice(t, w)
				}
			}}
			if err := w.bob.FlushOutbox(tctx(t)); err != nil {
				t.Fatal(err)
			}
			if len(outputs) != 1 {
				t.Fatalf("%d outputs handed over, the last after the %s was stored", len(outputs), stop)
			}
			var held, reqState string
			w.bob.store.db.QueryRow(`SELECT o.state, i.state FROM outbox o JOIN inbox i ON i.result_id = o.id WHERE o.pid = ? AND o.id != ?`,
				pid, outputs[0]).Scan(&held, &reqState)
			if held != stateNotDelivered || reqState != stateNotDelivered {
				t.Fatalf("the second output: %s, its request %s", held, reqState)
			}
			if stop == "freeze" {
				if s, _, _, _ := w.bob.store.outboxState(plain.ID); s != stateQueued {
					t.Fatalf("a plain message to a frozen person: %s", s)
				}
			}
			// A held-back output is never revived by a later attempt's outcome.
			var second string
			w.bob.store.db.QueryRow(`SELECT id FROM outbox WHERE pid = ? AND state = ?`, pid, stateNotDelivered).Scan(&second)
			for _, s := range []string{stateQueued, stateConvWaiting, stateFailed} {
				w.bob.store.setOutboxState(second, s, "", "")
				if now, _, _, _ := w.bob.store.outboxState(second); now != stateNotDelivered {
					t.Fatalf("revived as %s", now)
				}
			}
			if err := w.bob.FlushOutbox(tctx(t)); err != nil || len(outputs) != 1 {
				t.Fatalf("a later flush sent it (%d, %v)", len(outputs), err)
			}
		})
	}
}

// A group's or DM's participant answers in the request's selected topic.
// An ordinary answer leaves it active; only the explicit trailer closes it.
func TestChatTopicAgentReply(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:2], nil)
	q, err := w.alice.AskAgentInTopic(tctx(t), pid, envelope.KindQuestion, "What failed?", "new", nil)
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, w.alice, conv, q.ID)
	if answer.Topic == "" || answer.TopicDone {
		t.Fatalf("ordinary answer: %+v", answer)
	}
	if !strings.Contains(st.last(), topicClosurePromptText) || strings.Contains(st.last(), "Close a finished topic only on purpose") {
		t.Fatal("participant prompt still infers topic closure from finishing an answer")
	}

	topics, err := w.alice.ChatTopics(conv)
	if err != nil || len(topics) != 1 || topics[0].State != TopicActive || topics[0].Pending {
		t.Fatalf("ordinary answer topics: %+v %v", topics, err)
	}
	st.mode("closed")
	next, err := w.alice.AskAgentInTopic(tctx(t), pid, envelope.KindQuestion, "Please close this topic.", answer.Topic, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == next.LID })
	if n != 1 || request.ReplyTo != answer.LID {
		t.Fatalf("follow-up lost its logical topic parent: %+v (previous answer %s)", request, answer.LID)
	}
	done := replyAt(t, w.alice, conv, next.ID)
	if done.Topic != answer.Topic || !done.TopicDone || done.Body != "Finished the work" {
		t.Fatalf("explicit close: %+v", done)
	}
	topics, err = w.alice.ChatTopics(conv)
	if err != nil || len(topics) != 1 || topics[0].State != TopicDone || topics[0].DoneBy != DoneByAgent {
		t.Fatalf("closed topics: %+v %v", topics, err)
	}
}
