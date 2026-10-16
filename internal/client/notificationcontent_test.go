package client

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestNativeNotificationExactMessagePreview(t *testing.T) {
	w, conv, _, stop := alertWorld(t, 30*time.Second)
	stop()
	allowAliceAt(t, w)
	var notices []NativeNotification
	show := func(n NativeNotification) { notices = append(notices, n) }
	receive := func(body string) string {
		stop, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyContent: show})
		id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: body})
		stop()
		return id
	}
	_ = receive("Older unrelated content")
	latest := receive("Exact new message\nwith a second line")
	person, _, err := w.alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.showDueAlerts(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Fragment != "msg="+latest+"&conv="+conv+"&dir=in" || notices[0].Body != "Exact new message with a second line" || notices[0].Title != person.Label {
		t.Fatalf("notices: %+v", notices)
	}
	if _, err := w.bob.showDueAlerts(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatal("notification repeated after due bookkeeping")
	}
	var own NativeNotification
	w.alice.nativeNotifyContent = func(n NativeNotification) { own = n }
	w.alice.nativeNotification("msg="+latest+"&dir=out", latest, "Reminder", "A reminder is due")
	if own.Fragment != "msg="+latest+"&conv="+conv+"&dir=out" || own.Body != "Exact new message with a second line" || !strings.HasPrefix(own.Title, "Reminder · ") {
		t.Fatalf("own sent reminder target: %+v", own)
	}
}

func TestNativeNotificationLinkedOwnerHostAttribution(t *testing.T) {
	w, _, _ := selfConsentWorld(t)
	stub := installAgentStub(t)
	setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
	phone, awaited, _ := linkPhone(t, w.alice, "notification-phone")
	if err := w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); err != nil {
		t.Fatal(err)
	}
	if outcome := <-awaited; outcome.err != nil {
		t.Fatal(outcome.err)
	}
	var notice NativeNotification
	stop, _ := runWith(t, w, phone, RunOptions{HumanOnly: true, NotifyContent: func(n NativeNotification) { notice = n }})
	human, err := w.alice.SendMessage(tctx(t), Outgoing{To: phone.Address, Body: "Human sender"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "linked human message", func() bool { return inboxHas(t, phone, human.ID) })
	stop()
	phone.nativeNotification("msg="+human.ID+"&dir=in", human.ID, "AgentNet", "New activity")
	person, _, err := w.alice.Person()
	if err != nil || notice.Title != person.Label || notice.Body != "Human sender" {
		t.Fatalf("human notice: %+v %v", notice, err)
	}
	request, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "Answer on this host"})
	if err != nil {
		t.Fatal(err)
	}
	stop, _ = runWith(t, w, phone, RunOptions{HumanOnly: true, NotifyContent: func(n NativeNotification) { notice = n }})
	eventually(t, "host answer received", func() bool { _, ok := findReply(phone, request.ID); return ok })
	stop()
	answer, _ := findReply(phone, request.ID)
	phone.nativeNotification("msg="+answer.ID+"&dir=in", answer.ID, "AgentNet", "New activity")
	if notice.Title != "Agent on "+DeviceWords(w.alice.Address) || notice.Body != notificationText(answer.Body, 240) {
		t.Fatalf("agent mislabeled as human owner: %+v", notice)
	}
}

func TestNativeNotificationProactiveAgentAttribution(t *testing.T) {
	w, conv, lids, _ := agentWorld(t)
	stub := installAgentStub(t)
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	pid := participate(t, w, conv, lids[:1], nil)
	sent, err := w.bob.SendAgentMessage(tctx(t), pid, "", "Ordinary proactive agent update")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	eventually(t, "proactive notification target", func() bool {
		m, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
		id = m.ID
		return n == 1 && m.VerifiedAgent
	})
	var notice NativeNotification
	w.alice.nativeNotifyContent = func(n NativeNotification) { notice = n }
	w.alice.nativeNotification("msg="+id+"&dir=in", id, "AgentNet", "New activity")
	if notice.Title != "Agent on "+DeviceWords(w.bob.Address) || notice.Body != "Ordinary proactive agent update" {
		t.Fatalf("proactive agent mislabeled: %+v", notice)
	}
}

func TestNativeNotificationEditedDeletedAndBoundedPreview(t *testing.T) {
	w, conv, _, stop := alertWorld(t, time.Hour)
	stop()
	allowAliceAt(t, w)
	var notices []NativeNotification
	show := func(n NativeNotification) { notices = append(notices, n) }
	stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyContent: show})
	id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Private obsolete contents"})
	ref, err := w.alice.RefOf(conv, id, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.Revise(tctx(t), ref, "Current shown contents"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "current revision received", func() bool {
		r, e := w.bob.notificationControls(ControlRef{Conv: conv, ID: ref.ID, Fingerprint: ref.Fingerprint}, w.alice.Address)
		return e == nil && r.Edited
	})
	stop()
	if _, err = w.bob.showDueAlerts(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Body != "Current shown contents" {
		t.Fatalf("edited preview: %+v", notices)
	}
	stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyContent: show})
	if _, err = w.alice.Retract(tctx(t), ref, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "deletion received", func() bool {
		r, e := w.bob.notificationControls(ControlRef{Conv: conv, ID: ref.ID, Fingerprint: ref.Fingerprint}, w.alice.Address)
		return e == nil && r.Deleted
	})
	stop()
	w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "A reminder is due")
	if notices[len(notices)-1].Body != "Message deleted." {
		t.Fatalf("deleted text leaked: %+v", notices)
	}
	got := notificationText("\x00\u202eFake title\n"+strings.Repeat("🙂", 300), 240)
	if utf8.RuneCountInString(got) != 240 || strings.ContainsAny(got, "\x00\u202e\n") || !strings.HasSuffix(got, "…") {
		t.Fatalf("unsafe/unbounded preview %q", got)
	}
}
