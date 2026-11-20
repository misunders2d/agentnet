//go:build linux

package client

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func askRunOriginV2(t *testing.T, a *Agent, pid int, start uint64) (runOriginAnswer, string) {
	t.Helper()
	raw := askRunOrigin(t, a, `{"v":2,"pid":`+strconv.Itoa(pid)+`,"start":`+strconv.FormatUint(start, 10)+`}`)
	var ans runOriginAnswer
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ans); err != nil || !strings.HasSuffix(raw, "\n") || ans.V != 2 {
		t.Fatalf("v2 answer %q: %v", raw, err)
	}
	if ans.Error != "" && (ans.Run != "" || ans.Kind != "" || ans.Chain != "" || ans.Reply != "") {
		t.Fatalf("v2 answer mixes fields: %q", raw)
	}
	return ans, raw
}

// The owner's phone asks the laptop's agent in the My agents room; that
// run tasks the hub's agent. On the hub, version 2 says: an agent's task,
// own chain, the laptop agent and device by name, and a reply handle that
// sends into the phone request's topic, after that request has finished,
// once per client id. Version 1 is unchanged. A guest, another person or a
// lost upstream makes the chain mixed, with the handle at the hub's own
// request; a changed chain is refused at the re-check; a handle works only
// on its own device, for its own person.
func TestRunOriginV2ChainAndReplyHandle(t *testing.T) {
	stub := installAgentStub(t)
	Harnesses["roomasker"] = Harnesses["agentstub"] // one enabled agent per harness per device
	t.Cleanup(func() { delete(Harnesses, "roomasker") })
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	hub := w.alice
	laptop := linkedVia(t, hub, "laptop", approveLink(hub))
	phone := linkedVia(t, hub, "phone", approveLink(hub))
	for _, d := range []*Agent{hub, laptop, phone} {
		publishGroupFixtureCaps(t, d, true)
	}
	stop, err := hub.listenRunOrigin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	pa, err := phone.CreateLocalAgent("pa", Responder{Harness: "roomasker", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	lap, err := laptop.CreateLocalAgent("lap", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	desk, err := hub.CreateLocalAgent("desk", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*Agent{hub, laptop, phone} {
		if err = d.PublishAgentCatalog(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	self, _, err := hub.store.selfPerson(hub.Address)
	if err != nil || self.info.Person == "" {
		t.Fatal(self, err)
	}
	person := self.info.Person
	stored := func(d *Agent, conv, lid string) string {
		var id string
		eventually(t, "request stored on "+d.Address, func() bool {
			return d.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, conv, lid).Scan(&id) == nil
		})
		waitState(t, d, id, stateAnswered)
		return id
	}

	// The phone's agent opens the topic with the laptop's agent; the person
	// then types the task to that agent there.
	pfrom, err := phone.SessionAgent("", pa.ID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := phone.SendAgentRoomAsk(ctx, AgentRoomAsk{From: pfrom, SessionKey: "agentstub/v2", Host: laptop.Address, AgentID: lap.ID, Kind: envelope.KindQuestion, Body: "are you there?"})
	if err != nil {
		t.Fatal(err)
	}
	stored(laptop, opened.Conv, opened.LID)
	typed, err := phone.AskAgentInTopic(ctx, opened.To, envelope.KindTask, "fix the nightly build", opened.Topic, nil)
	if err != nil {
		t.Fatal(err)
	}
	cause := stored(laptop, opened.Conv, typed.LID)
	setRunning(t, laptop, cause)
	chained, err := laptop.RunAgentRoomAsk(ctx, cause, AgentRoomAsk{Host: hub.Address, AgentID: desk.ID, Kind: envelope.KindTask, Body: "track the nightly fix"})
	if err != nil {
		t.Fatal(err)
	}
	if chained.Topic != opened.Topic {
		t.Fatalf("the run's ask left its cause's topic: %s vs %s", chained.Topic, opened.Topic)
	}
	hubID := stored(hub, opened.Conv, chained.LID)
	setRunning(t, hub, hubID)
	hubJob := storedJob(t, hub, hubID)
	child := startSleeper(t)
	t.Cleanup(hub.vouchRun(hubJob, child.Process.Pid))
	pid, start := child.Process.Pid, liveStart(t, child.Process.Pid)

	// Version 1: byte for byte as before.
	raw := askRunOrigin(t, hub, `{"v":1,"pid":`+strconv.Itoa(pid)+`,"start":`+strconv.FormatUint(start, 10)+`}`)
	if want := `{"v":1,"run":"` + hubID + `/1","person":"` + person + `","author":"agent"}` + "\n"; raw != want {
		t.Fatalf("v1 answer changed:\n got %q\nwant %q", raw, want)
	}
	ans, raw := askRunOriginV2(t, hub, pid, start)
	if ans.Run != hubID+"/1" || ans.Person != person || ans.Author != runOriginAgent || ans.Kind != envelope.KindTask || ans.Chain != runOriginOwn ||
		ans.Agent != "agentstub" || ans.Device != DeviceWords(laptop.Address) || !strings.HasPrefix(ans.Reply, replyHandlePrefix) {
		t.Fatalf("laptop agent -> hub: %s", raw)
	}
	if want := `{"v":2,"run":"` + hubID + `/1","person":"` + person + `","author":"agent","kind":"task","chain":"own","reply":"` + ans.Reply + `","agent":"agentstub","device":"` + DeviceWords(laptop.Address) + `"}` + "\n"; raw != want {
		t.Fatalf("v2 shape:\n got %q\nwant %q", raw, want)
	}
	if again, _ := askRunOriginV2(t, hub, pid, start); again != ans {
		t.Fatalf("v2 answer not stable: %+v vs %+v", again, ans)
	}
	var conv, lid, topic string
	if err := hub.store.db.QueryRow(`SELECT conv,lid,topic FROM reply_handles WHERE handle=?`, ans.Reply).Scan(&conv, &lid, &topic); err != nil || conv != opened.Conv || lid != typed.LID || topic != opened.Topic {
		t.Fatalf("handle is not the phone request's: %s %s %s %v", conv, lid, topic, err)
	}

	// The reply goes into the phone request's topic, though that request is
	// finished, once per client id.
	sent, err := hub.SendReplyHandle(ctx, ans.Reply, desk.ID, "ISSUE-1/1/accepted", "Accepted: tracking the nightly fix", nil)
	if err != nil || sent.Repeated || sent.ID == "" || sent.Conv != opened.Conv || sent.Topic != opened.Topic {
		t.Fatalf("reply: %+v %v", sent, err)
	}
	if again, err := hub.SendReplyHandle(ctx, ans.Reply, desk.ID, "ISSUE-1/1/accepted", "Accepted: tracking the nightly fix", nil); err != nil || !again.Repeated || again.ID != sent.ID || again.LID != sent.LID {
		t.Fatalf("repeated client id: %+v %v", again, err)
	}
	var messages int
	if err := hub.store.db.QueryRow(`SELECT count(DISTINCT lid) FROM outbox WHERE conv=? AND body=?`, opened.Conv, "Accepted: tracking the nightly fix").Scan(&messages); err != nil || messages != 1 {
		t.Fatalf("one client id, %d messages %v", messages, err)
	}
	eventually(t, "the reply on the phone, in the request's topic", func() bool {
		msgs, err := phone.ConversationMessages(opened.Conv)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.LID == sent.LID {
				return m.Topic == opened.Topic && m.Body == "Accepted: tracking the nightly fix" && envelope.AgentOrigin(m.Origin)
			}
		}
		return false
	})

	// Handles: only this device's, only for its own person.
	for name, h := range map[string]string{"unknown": replyHandlePrefix + protocol.NewID(), "garbage": "not-a-handle"} {
		if _, err := hub.SendReplyHandle(ctx, h, desk.ID, "", "x", nil); !errors.Is(err, ErrReplyHandle) {
			t.Fatalf("%s handle: %v", name, err)
		}
	}
	if _, err := phone.SendReplyHandle(ctx, ans.Reply, pa.ID, "", "x", nil); !errors.Is(err, ErrReplyHandle) {
		t.Fatalf("another device's handle: %v", err)
	}
	other := replyHandlePrefix + protocol.NewID()
	if _, err := hub.store.db.Exec(`INSERT INTO reply_handles(handle,request,person,conv,lid,topic,created_ms) VALUES(?,?,?,?,?,?,0)`, other, "bob-request", "bob-person", opened.Conv, typed.LID, opened.Topic); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.SendReplyHandle(ctx, other, desk.ID, "", "x", nil); !errors.Is(err, ErrReplyHandle) {
		t.Fatalf("another person's handle: %v", err)
	}

	// Mixed: the chain as this device holds it, the handle at its own request.
	chainNow := func() runOriginChain {
		c, err := hub.runOriginChainOf(hubJob, person, runOriginAgent)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := chainNow(); c.chain != runOriginOwn || c.target.lid != typed.LID {
		t.Fatalf("own chain: %+v", c)
	}
	var upID, upSender, upKey, upHuman string
	if err := hub.store.db.QueryRow(`SELECT id,sender,verified_by,coalesce(human,'') FROM inbox WHERE conv=? AND lid=?`, opened.Conv, typed.LID).Scan(&upID, &upSender, &upKey, &upHuman); err != nil {
		t.Fatalf("the hub holds no copy of the phone's request: %v", err)
	}
	guestPID := strings.Repeat("b", 32)
	guest := humanJSON(&envelope.HumanTurn{AuthorPID: guestPID, Audience: []envelope.HumanScope{{PID: guestPID}}, Proof: []protocol.ParticipationEvent{{Type: protocol.EventScope, PID: guestPID, Role: protocol.RoleHuman}}})
	for name, set := range map[string]string{
		"a guest wrote the phone's request": `UPDATE inbox SET human=` + sqlQuote(guest) + ` WHERE id=` + sqlQuote(upID),
		"another person sent it":            `UPDATE inbox SET sender=` + sqlQuote(w.bob.Address) + `, verified_by=` + sqlQuote(w.bob.Self().Fingerprint()) + ` WHERE id=` + sqlQuote(upID),
		"its upstream is not held here":     `UPDATE inbox SET reply_to=NULL WHERE id=` + sqlQuote(hubID),
	} {
		if _, err := hub.store.db.Exec(set); err != nil {
			t.Fatal(err)
		}
		if c := chainNow(); c.chain != runOriginMixed || c.target.conv != opened.Conv || c.target.lid != chained.LID {
			t.Fatalf("%s: %+v", name, c)
		}
		// The vouched run said own: its chain changed, so version 2 refuses
		// while version 1 still answers.
		if a, raw := askRunOriginV2(t, hub, pid, start); a.Error == "" {
			t.Fatalf("%s: changed chain vouched: %s", name, raw)
		}
		if a := askRunOriginPID(t, hub, pid, start); a.Error != "" || a.Author != runOriginAgent {
			t.Fatalf("%s: v1 %+v", name, a)
		}
		if _, err := hub.store.db.Exec(`UPDATE inbox SET sender=?, verified_by=?, human=nullif(?,'') WHERE id=?`, upSender, upKey, upHuman, upID); err != nil {
			t.Fatal(err)
		}
		if _, err := hub.store.db.Exec(`UPDATE inbox SET reply_to=? WHERE id=?`, typed.LID, hubID); err != nil {
			t.Fatal(err)
		}
		if a, _ := askRunOriginV2(t, hub, pid, start); a != ans {
			t.Fatalf("%s: restored chain: %+v", name, a)
		}
	}

	// A guest's turn that a member device carried counts "human" in
	// version 1 (runOriginAgentWritten), but never as the person's own chain.
	direct := ownPendingTask(t, hub, phone, "list my schedules")
	if _, err := hub.store.db.Exec(`UPDATE inbox SET human=? WHERE id=?`, guest, direct); err != nil {
		t.Fatal(err)
	}
	dj := storedJob(t, hub, direct)
	if p, author, err := hub.runOriginIdentity(dj); err != nil || p != person || author != runOriginHuman {
		t.Fatalf("premise: %q %q %v", p, author, err)
	}
	if c, err := hub.runOriginChainOf(dj, person, runOriginHuman); err != nil || c.chain != runOriginMixed {
		t.Fatalf("guest-written request: %+v %v", c, err)
	}

	// A mixed run's handle is its own request's: the reply goes to the
	// laptop agent's ask, in the same topic.
	mixedID := "mixed-" + hubID
	mixed, err := hub.replyHandleFor(mixedID, person, replyTarget{opened.Conv, chained.LID, opened.Topic})
	if err != nil || mixed == ans.Reply {
		t.Fatalf("mixed handle %q %v", mixed, err)
	}
	if got, err := hub.replyHandleFor(mixedID, person, replyTarget{opened.Conv, chained.LID, opened.Topic}); err != nil || got != mixed {
		t.Fatalf("handle not stable: %q %v", got, err)
	}
	if _, err := hub.replyHandleFor(mixedID, person, replyTarget{opened.Conv, typed.LID, opened.Topic}); err == nil {
		t.Fatal("a handle moved to another reply")
	}
}

func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
