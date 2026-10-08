package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A member keeps its own history when an assistant hosted on that same
// device leaves. The ended assistant must not become a new history reader.
func TestGroupHistoryMemberReaderAfterOwnAssistantDismissed(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	a := w.alice
	conv := packet.State.Conv
	p := p6Member(t, a, a, conv)
	eventually(t, "member sees the accepted assistant", func() bool {
		return stateAt(t, w.bob, p.PID).Claimable()
	})
	request, err := w.bob.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "retained historical request")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "original member holds the request", func() bool {
		return inboxCount(t, a, `conv=? AND lid=? AND human IS NOT NULL`, conv, request.LID) == 1
	})
	var originalHuman string
	if err = a.store.db.QueryRow(`SELECT human FROM inbox WHERE conv=? AND lid=?`, conv, request.LID).Scan(&originalHuman); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	if state := stateAt(t, a, p.PID); state.State != PartDismissed || state.Held != 0 {
		t.Fatalf("fixture assistant did not end cleanly: %+v", state)
	}
	var captured envelope.HumanTurn
	if err = json.Unmarshal([]byte(originalHuman), &captured); err != nil {
		t.Fatal(err)
	}
	// Member read authority applies to inert history only. The same ended
	// host must not regain live participation or accept a changed reader key.
	if err = humanAuthority(a.store.db, conv, &captured, w.bob.Address, w.bob.Self().Fingerprint(), a.Address, a.Self().Fingerprint(), false, false, false); err == nil {
		t.Fatal("ended assistant retained live reader authority")
	}
	if err = humanAuthority(a.store.db, conv, &captured, w.bob.Address, w.bob.Self().Fingerprint(), a.Address, strings.Repeat("f", 64), true, false, false); err == nil {
		t.Fatal("historical member reader accepted a different key")
	}
	var altered envelope.HumanTurn
	if err = json.Unmarshal([]byte(originalHuman), &altered); err != nil {
		t.Fatal(err)
	}
	if len(altered.Audience) == 0 {
		t.Fatal("fixture has no captured consent")
	}
	altered.Audience[0].Decision = strings.Repeat("f", 64)
	if err = humanAuthority(a.store.db, conv, &altered, w.bob.Address, w.bob.Self().Fingerprint(), a.Address, a.Self().Fingerprint(), true, false, false); err == nil {
		t.Fatal("historical member reader bypassed exact captured consent")
	}
	phone, await, _ := linkPhone(t, a, "after-assistant-dismissal")
	link := pendingLink(t, a)
	stops[a]()
	if err = a.DecideLink(tctx(t), link.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	if _, err = a.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatalf("ended assistant blocked original member history: %v", err)
	}
	rows, err := a.store.db.Query(`SELECT body FROM outbox WHERE recipient=? AND conv=? AND sub='history'`, phone.Address, conv)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var body string
		var item HistoryItem
		if err = rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(body), &item); err != nil {
			t.Fatal(err)
		}
		if item.LID == request.LID {
			got, err := json.Marshal(item.Human)
			if err != nil || string(got) != originalHuman || item.PID != p.PID || item.Kind != envelope.KindQuestion {
				t.Fatalf("history changed the captured request: %+v, %v", item, err)
			}
			found = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("history silently skipped the retained request")
	}
}

func TestGroupHistoryReplyAfterAskingAssistantDismissed(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, stops := groupTurnsFixture(t)
	a, conv := w.alice, packet.State.Conv
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(a.Address); err != nil {
		t.Fatal(err)
	}
	from := p6Member(t, a, a, conv)
	to := p6Member(t, a, w.bob, conv)
	eventually(t, "asking assistant scope reaches target", func() bool { return stateAt(t, w.bob, from.PID).Claimable() })
	root, err := a.AskAgent(tctx(t), from.PID, envelope.KindTask, "ask another assistant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); err != nil {
		t.Fatal(err)
	}
	ask, err := a.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindQuestion, "retained correlated question")
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, a, conv, ask.LID)
	if _, err = a.DismissParticipation(tctx(t), from.PID); err != nil {
		t.Fatal(err)
	}
	phone, await, _ := linkPhone(t, a, "after-asking-assistant")
	link := pendingLink(t, a)
	stops[a]()
	if err = a.DecideLink(tctx(t), link.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	if _, err = a.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatalf("ended asking assistant blocked retained reply: %v", err)
	}
	var copied int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history' AND json_extract(body,'$.id')=?`, phone.Address, answer.ID).Scan(&copied); err != nil || copied != 1 {
		t.Fatalf("retained reply copies: %d %v", copied, err)
	}
	m, err := a.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	output := envelope.Inner{Conv: conv, PID: to.PID, Kind: envelope.KindAnswer, ReplyTo: ask.LID, AgentID: to.AgentID}
	if _, err = externalOutputRequest(a.store.db, output, stateAt(t, a, to.PID), m, a.Address, a.Self().Fingerprint()); err == nil {
		t.Fatal("historical retention restored live asking-agent authority")
	}
	if stub.runs() != 1 {
		t.Fatal("history replayed the original request")
	}
}
