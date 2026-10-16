package client

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A standard desktop host has no content callback. Its existing built-in
// notifier must still honor the local explicit preview choice exactly once.
func TestBuiltinNotificationHonorsPreview(t *testing.T) {
	w, conv, n, stop := alertWorld(t, time.Hour)
	stop()
	allowAliceAt(t, w)
	p, err := w.bob.AlertPrefs()
	if err != nil {
		t.Fatal(err)
	}
	p.Preview = NotificationPreviewFull
	if err = w.bob.SetAlertPrefs(p); err != nil {
		t.Fatal(err)
	}
	stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true, OpenPage: func(fragment string) []string { return []string{"fake-app", protocol.AppOpenURL(fragment)} }})
	id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Exact built-in private preview"})
	stop()
	if _, err = w.bob.showDueAlerts(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	person, _, _ := w.alice.Person()
	if n.count() != 1 || n.last() != person.Label+": Exact built-in private preview" {
		t.Fatalf("built-in explicit full ignored: %q", n.last())
	}
	n.mu.Lock()
	args := append([]string{}, n.argvs[0]...)
	n.mu.Unlock()
	if len(args) != 2 || args[1] != protocol.AppOpenURL("msg="+id+"&conv="+conv+"&dir=in") {
		t.Fatal(args)
	}
	if _, err = w.bob.showDueAlerts(time.Now().Add(2 * time.Hour)); err != nil || n.count() != 1 {
		t.Fatal("repeat", err, n.count())
	}
}

func TestBuiltinNotificationHostPreviewMatrix(t *testing.T) {
	w, conv, n, stop := alertWorld(t, time.Hour)
	stop()
	stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true})
	id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Exact host preview"})
	stop()
	person, _, _ := w.alice.Person()
	for _, host := range []struct {
		name, workspace string
		page            bool
	}{
		{"owned-default", "default", true}, {"child-workspace", strings.Repeat("b", 32), true},
		{"standalone-or-attached", strings.Repeat("c", 32), false},
	} {
		w.bob.openPage, w.bob.openConv = nil, nil
		if host.page {
			w.bob.openPage = func(fragment string) []string {
				return []string{"fake-app", protocol.AppOpenURL(fragment + "&workspace=" + host.workspace)}
			}
		} else {
			w.bob.openConv = func(string) []string {
				return []string{"fake-app", protocol.AppOpenURL("conv=&workspace=" + host.workspace)}
			}
		}
		for _, mode := range []NotificationPreview{"", NotificationPreviewFull, NotificationPreviewSender, NotificationPreviewHidden, "invalid"} {
			p := AlertPrefs{Enabled: true, Preview: mode}
			if mode == "invalid" {
				p.Preview = ""
			}
			if err := w.bob.SetAlertPrefs(p); err != nil {
				t.Fatal(err)
			}
			if mode == "invalid" {
				if _, err := w.bob.store.db.Exec(`UPDATE config SET v='invalid' WHERE k='notification-preview'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.bob.store.db.Exec(`INSERT INTO alerts(conv,sender,last_id,count,due_ms) VALUES(?,?,?,1,1)`, conv, w.alice.Address, id); err != nil {
				t.Fatal(err)
			}
			before := n.count()
			if _, err := w.bob.showDueAlerts(time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := w.bob.showDueAlerts(time.Now()); err != nil {
				t.Fatal(err)
			}
			want := "AgentNet: New activity"
			if mode == NotificationPreviewFull {
				want = person.Label + ": Exact host preview"
			}
			if mode == NotificationPreviewSender {
				want = person.Label + ": New message"
			}
			if n.count() != before+1 || n.last() != want {
				t.Fatalf("%s mode%q: %q count%d", host.name, mode, n.last(), n.count()-before)
			}
			n.mu.Lock()
			args := append([]string{}, n.argvs[before]...)
			n.mu.Unlock()
			wantURL := protocol.AppOpenURL("msg=" + id + "&conv=" + conv + "&dir=in&workspace=" + host.workspace)
			if len(args) != 2 || args[1] != wantURL {
				t.Fatalf("%s route: %v want%s", host.name, args, wantURL)
			}
		}
	}
	if err := w.bob.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	got := w.bob.nativeDisplay("msg="+id+"&dir=in", id, "Reminder", "stale caller text", true)
	if got.Title != "AgentNet" || got.Body != "A reminder is due" || got.Fragment != "review" {
		t.Fatal("failed reads leaked content or stale target", got)
	}
}

func TestBuiltinReminderSentDirectionAndMultiple(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.alice)
	stopAlice, _ := runWith(t, w, w.alice, RunOptions{HumanOnly: true, OpenPage: func(fragment string) []string {
		return []string{"fake-app", protocol.AppOpenURL(fragment + "&workspace=default")}
	}})
	stop, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true})
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	first := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "First own sent reminder"})
	second := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Second own sent reminder"})
	stop()
	stopAlice()
	// Alice's exact sent copy has the same physical ID as Bob's received copy.
	a := w.alice
	person, _, _ := a.Person()
	for _, mode := range []NotificationPreview{"", NotificationPreviewFull, NotificationPreviewSender, NotificationPreviewHidden} {
		if err := a.SetAlertPrefs(AlertPrefs{Preview: mode}); err != nil {
			t.Fatal(err)
		}
		due := time.Now().Add(time.Hour)
		if _, err := a.SetReminder(first, due); err != nil {
			t.Fatal(err)
		}
		before := n.count()
		if _, err := a.remindDue(due.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.remindDue(due.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		want := "AgentNet: A reminder is due"
		if mode == NotificationPreviewFull {
			want = "Reminder · " + person.Label + ": First own sent reminder"
		}
		if mode == NotificationPreviewSender {
			want = person.Label + ": New message"
		}
		if n.count() != before+1 || n.last() != want {
			t.Fatalf("reminder%q: %q count%d", mode, n.last(), n.count()-before)
		}
		n.mu.Lock()
		args := append([]string{}, n.argvs[before]...)
		n.mu.Unlock()
		if len(args) != 2 || args[1] != protocol.AppOpenURL("msg="+first+"&conv="+conv+"&dir=out&workspace=default") {
			t.Fatal("sent reminder direction", args)
		}
	}
	if err := a.SetAlertPrefs(AlertPrefs{Preview: NotificationPreviewFull}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(time.Hour)
	for _, id := range []string{first, second} {
		if _, err := a.SetReminder(id, due); err != nil {
			t.Fatal(err)
		}
	}
	before := n.count()
	if _, err := a.remindDue(due.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.remindDue(due.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	args := append([]string{}, n.argvs[before]...)
	n.mu.Unlock()
	if n.count() != before+1 || n.last() != "Reminder: 2 reminders are due" || len(args) != 2 || args[1] != protocol.AppOpenURL("review&workspace=default") {
		t.Fatalf("multi reminder %q %v", n.last(), args)
	}
}

func TestBuiltinReviewPrivacyExactTargetOnce(t *testing.T) {
	w, conv, _, stop := alertWorld(t, time.Hour)
	// Receive a real signed DM so the stored peer key and person are verified.
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Establish verified local sender"})
	stop()
	w.bob.openPage = func(fragment string) []string {
		return []string{"fake-app", protocol.AppOpenURL(fragment + "&workspace=default")}
	}
	id := protocol.NewID()
	if _, err := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, w.alice.Address, 1, envelope.KindQuestion, "Exact private decision", 1, stateHeld, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	n := fakeNotify(w.bob)
	person, _, _ := w.alice.Person()
	for _, mode := range []NotificationPreview{"", NotificationPreviewFull, NotificationPreviewSender, NotificationPreviewHidden} {
		if err := w.bob.SetAlertPrefs(AlertPrefs{Preview: mode}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.bob.store.db.Exec(`UPDATE inbox SET notified=0 WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		w.bob.notifyTried = nil
		before := n.count()
		w.bob.notifyReview()
		w.bob.notifyReview()
		want := "AgentNet: A request needs your decision"
		if mode == NotificationPreviewFull {
			want = "Decision needed · " + person.Label + ": Exact private decision"
		}
		if mode == NotificationPreviewSender {
			want = person.Label + ": New message"
		}
		if n.count() != before+1 || n.last() != want {
			t.Fatalf("review%q %q count%d", mode, n.last(), n.count()-before)
		}
		n.mu.Lock()
		args := append([]string{}, n.argvs[before]...)
		n.mu.Unlock()
		if len(args) != 2 || args[1] != protocol.AppOpenURL("msg="+id+"&dir=in&workspace=default") {
			t.Fatal(args)
		}
		if row := inboxRow(t, w.bob, id); row.State != stateHeld {
			t.Fatal("privacy/click changed request", row.State)
		}
	}
}

func TestNativeNotificationHostDefaultDuringStartup(t *testing.T) {
	w := newWorld(t, "")
	if got := w.bob.NativeNotificationPreviewDefault(); got != NotificationPreviewHidden {
		t.Fatal(got)
	}
	readDone := make(chan struct{})
	finish := make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
		<-readDone
	}()
	go func() {
		defer close(readDone)
		for {
			select {
			case <-finish:
				return
			default:
			}
			got := w.bob.NativeNotificationPreviewDefault()
			if got != NotificationPreviewHidden && got != NotificationPreviewSender {
				panic("invalid host default")
			}
		}
	}()
	stop, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyContent: func(NativeNotification) {}, NotifyPreviewDefault: NotificationPreviewSender})
	got := w.bob.NativeNotificationPreviewDefault()
	stop()
	close(finish)
	<-readDone
	if got != NotificationPreviewSender {
		t.Fatal(got)
	}
	for _, v := range []struct {
		opts RunOptions
		want NotificationPreview
	}{
		{RunOptions{}, NotificationPreviewHidden},
		{RunOptions{Notify: func(string) {}}, ""},
		{RunOptions{NotifyContent: func(NativeNotification) {}}, NotificationPreviewFull},
		{RunOptions{NotifyContent: func(NativeNotification) {}, NotifyPreviewDefault: "invalid"}, NotificationPreviewHidden},
	} {
		w.bob.publishNativePreviewHost(v.opts)
		if got := w.bob.NativeNotificationPreviewDefault(); got != v.want {
			t.Fatalf("%+v got%q", v.opts, got)
		}
	}
}

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
	for _, v := range []struct {
		mode NotificationPreview
		body string
	}{{NotificationPreviewFull, "Message deleted."}, {NotificationPreviewSender, "New message"}, {NotificationPreviewHidden, "A reminder is due"}} {
		if err := w.bob.SetAlertPrefs(AlertPrefs{Preview: v.mode}); err != nil {
			t.Fatal(err)
		}
		w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "Private obsolete contents")
		if n := notices[len(notices)-1]; n.Body != v.body || strings.Contains(n.Body, "obsolete") {
			t.Fatal("retracted text escaped privacy mask", v.mode, n)
		}
	}
	got := notificationText("\x00\u202eFake title\n"+strings.Repeat("🙂", 300), 240)
	if utf8.RuneCountInString(got) != 240 || strings.ContainsAny(got, "\x00\u202e\n") || !strings.HasSuffix(got, "…") {
		t.Fatalf("unsafe/unbounded preview %q", got)
	}
}

func TestNativeNotificationPrivacyMaskExactFixture(t *testing.T) {
	w, conv, _, stop := alertWorld(t, time.Hour)
	stop()
	var notice NativeNotification
	stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyContent: func(n NativeNotification) { notice = n }})
	id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Private exact message"})
	stop()
	person, _, err := w.alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	fragment := "msg=" + id + "&conv=" + conv + "&dir=in"
	for _, v := range []struct{ preview, title, body string }{
		{"hidden", "AgentNet", "A reminder is due"},
		{"sender", person.Label, "New message"},
		{"full", "Reminder · " + person.Label, "Private exact message"},
		{"", "Reminder · " + person.Label, "Private exact message"},
		{"unexpected", "AgentNet", "A reminder is due"},
	} {
		if _, err := w.bob.store.db.Exec(`INSERT OR REPLACE INTO config(k,v) VALUES('notification-preview',?)`, v.preview); err != nil {
			t.Fatal(err)
		}
		w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "A reminder is due")
		if notice.Title != v.title || notice.Body != v.body || notice.Fragment != fragment {
			t.Fatalf("preview %q: %+v", v.preview, notice)
		}
	}
}

func TestNativeNotificationPreviewDefaultsAndFailures(t *testing.T) {
	w, conv, _, stop := alertWorld(t, time.Hour)
	stop()
	var notice NativeNotification
	callback := func(n NativeNotification) { notice = n }
	stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyContent: callback})
	id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Exact private target"})
	stop()
	person, _, _ := w.alice.Person()
	for _, v := range []struct {
		pref, hint  NotificationPreview
		title, body string
	}{
		{"", "", person.Label, "Exact private target"},
		{"", NotificationPreviewHidden, "AgentNet", "New activity"},
		{NotificationPreviewFull, NotificationPreviewHidden, person.Label, "Exact private target"},
		{NotificationPreviewSender, NotificationPreviewHidden, person.Label, "New message"},
		{NotificationPreviewHidden, NotificationPreviewFull, "AgentNet", "New activity"},
		{"", "invalid", "AgentNet", "New activity"},
	} {
		if err := w.bob.SetAlertPrefs(AlertPrefs{Preview: v.pref}); err != nil {
			t.Fatal(err)
		}
		stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyContent: callback, NotifyPreviewDefault: v.hint})
		stop()
		w.bob.nativeNotification("msg="+id+"&dir=in", id, "AgentNet", "Caller contents must not replace exact target")
		if notice.Title != v.title || notice.Body != v.body {
			t.Fatalf("pref=%q hint=%q: %+v", v.pref, v.hint, notice)
		}
	}
	if err := w.bob.SetAlertPrefs(AlertPrefs{Preview: NotificationPreviewSender}); err != nil {
		t.Fatal(err)
	}
	// A bad key cannot become a claimed person label in sender mode.
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET verified_by=? WHERE id=?`, "unverified", id); err != nil {
		t.Fatal(err)
	}
	w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "Private stale original")
	if notice.Title != "AgentNet" || notice.Body != "A reminder is due" {
		t.Fatal("unverified address masqueraded as sender", notice)
	}
	// Missing exact targets and failed config/control reads never release caller text.
	w.bob.nativeNotification("msg="+protocol.NewID()+"&dir=in", protocol.NewID(), "Reminder", "Private stale original")
	if notice.Title != "AgentNet" || notice.Body != "A reminder is due" {
		t.Fatal("missing target leaked sender content", notice)
	}
	if err := w.bob.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "Private stale original")
	if notice.Title != "AgentNet" || notice.Body != "A reminder is due" {
		t.Fatal("privacy read error leaked content", notice)
	}
}

func TestNativeNotificationSenderOmitsGroupAndMalformedAttribution(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	sent, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "Private group preview"})
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, copy := range sent.Copies {
		if copy.To == w.bob.Address {
			id = copy.ID
		}
	}
	eventually(t, "group notification admitted", func() bool { return inboxHas(t, w.bob, id) })
	stops[w.bob]() // notification callback fields are immutable during Run
	topic := protocol.NewID()
	if _, err := w.bob.store.db.Exec(`INSERT INTO topic_state(peer,topic,title,updated_at) VALUES(?,?,?,1)`, packet.State.Conv, topic, "Private topic title"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET topic=? WHERE id=?`, topic, id); err != nil {
		t.Fatal(err)
	}
	var notice NativeNotification
	w.bob.nativeNotifyContent = func(n NativeNotification) { notice = n }
	if err := w.bob.SetAlertPrefs(AlertPrefs{Preview: NotificationPreviewFull}); err != nil {
		t.Fatal(err)
	}
	w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "A reminder is due")
	if !strings.Contains(notice.Title, packet.State.Title) || !strings.Contains(notice.Title, "Private topic title") {
		t.Fatal("full fixture did not contain group context", notice)
	}
	if err := w.bob.SetAlertPrefs(AlertPrefs{Preview: NotificationPreviewSender}); err != nil {
		t.Fatal(err)
	}
	w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "A reminder is due")
	person, _, _ := w.alice.Person()
	if notice.Title != person.Label || notice.Body != "New message" {
		t.Fatal("group/reminder title leaked in sender mode", notice)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET human='malformed' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	w.bob.nativeNotification("msg="+id+"&dir=in", id, "Reminder", "Private stale original")
	if notice.Title != "AgentNet" || notice.Body != "A reminder is due" {
		t.Fatal("malformed attribution leaked sender content", notice)
	}
}

func TestNativeNotificationOwnReminderPreviewMatrix(t *testing.T) {
	w := newWorld(t, "")
	notices := []NativeNotification{}
	stopAlice, _ := runWith(t, w, w.alice, RunOptions{HumanOnly: true, NotifyContent: func(n NativeNotification) { notices = append(notices, n) }})
	stopBob, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true})
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Exact own reminder contents"})
	stopAlice()
	stopBob()
	person, _, _ := w.alice.Person()
	for _, v := range []struct {
		mode        NotificationPreview
		title, body string
	}{
		{NotificationPreviewFull, "Reminder · " + person.Label, "Exact own reminder contents"},
		{NotificationPreviewSender, person.Label, "New message"},
		{NotificationPreviewHidden, "AgentNet", "A reminder is due"},
	} {
		if err := w.alice.SetAlertPrefs(AlertPrefs{Preview: v.mode}); err != nil {
			t.Fatal(err)
		}
		due := time.Now().Add(time.Minute)
		if _, err := w.alice.SetReminder(id, due); err != nil {
			t.Fatal(err)
		}
		before := len(notices)
		if _, err := w.alice.remindDue(due.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.alice.remindDue(due.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if len(notices) != before+1 {
			t.Fatal("privacy replayed an already shown reminder", len(notices), before)
		}
		n := notices[len(notices)-1]
		if n.Title != v.title || n.Body != v.body || n.Fragment != "msg="+id+"&conv="+conv+"&dir=out" {
			t.Fatal("wrong own reminder mask/target", v.mode, n)
		}
	}
}

func TestNativeNotificationPreviewDefaultPublishedWithCallback(t *testing.T) {
	w, conv, _, stop := alertWorld(t, time.Hour)
	stop()
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	notices := make(chan NativeNotification, 2)
	stop, _ = runWith(t, w, w.bob, RunOptions{HumanOnly: true, NotifyPreviewDefault: NotificationPreviewHidden, NotifyContent: func(n NativeNotification) { notices <- n }})
	id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "Private alert contents"})
	if _, err := w.bob.showDueAlerts(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notices:
		if n.Title != "AgentNet" || n.Body != "New activity" || n.Fragment != "msg="+id+"&conv="+conv+"&dir=in" {
			t.Fatal("published desktop default lost", n)
		}
	case <-time.After(time.Second):
		t.Fatal("no native alert callback")
	}
	stop()
}

// Actual remote copy contains a private host label. None may reach Hidden or
// Sender-without-author; classification uses the original literal title only.
func TestNativeNotificationFixedSignalPrivacy(t *testing.T) {
	a := &Agent{store: securityStore(t)}
	private := "PRIVATE_REMOTE_LABEL"
	body := "A remote agent run seems stuck. PRIVATE_STALLED_DETAIL. " + remoteReviewCopy(map[string]int{private: 7}, 3) + " PRIVATE_MESSAGE_TEXT"
	for _, mode := range []NotificationPreview{NotificationPreviewHidden, NotificationPreviewSender} {
		if err := a.SetAlertPrefs(AlertPrefs{Preview: mode}); err != nil {
			t.Fatal(err)
		}
		for _, v := range []struct{ title, wantTitle, wantBody string }{
			{"AgentNet", "AgentNet", "New activity"},
			{"Reminder", "AgentNet", "A reminder is due"},
			{"Decision needed", "AgentNet", "A request needs your decision"},
			{"AgentNet — company settings access", "AgentNet — company settings access", "Open AgentNet to review"},
			{"Reminder · " + private, "AgentNet", "New activity"},
			{private, "AgentNet", "New activity"},
		} {
			got := a.nativeDisplay("review", "", v.title, body, true)
			if got.Title != v.wantTitle || got.Body != v.wantBody || got.Fragment != "review" {
				t.Fatalf("mode%q title%q got%+v", mode, v.title, got)
			}
			if strings.Contains(got.Title+got.Body, "PRIVATE") || strings.Contains(got.Body, "7") {
				t.Fatal("private caller/remote copy escaped", got)
			}
		}
	}
	if err := a.SetAlertPrefs(AlertPrefs{Preview: NotificationPreviewFull}); err != nil {
		t.Fatal(err)
	}
	got := a.nativeDisplay("review", "", "Decision needed", body, true)
	if got.Body != notificationText(body, 240) || got.Title != "Decision needed" {
		t.Fatal("Full changed", got)
	}
	if err := a.SetAlertPrefs(AlertPrefs{Preview: NotificationPreviewSender}); err != nil {
		t.Fatal(err)
	}
	got = a.maskNativeNotification(NativeNotification{Fragment: "exact", Title: "Reminder", Body: body}, "Verified author")
	if got.Title != "Verified author" || got.Body != "New message" || got.Fragment != "exact" {
		t.Fatal("verified Sender changed", got)
	}
}
