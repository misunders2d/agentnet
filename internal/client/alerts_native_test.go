package client

import (
	"fmt"
	"github.com/misunders2d/agentnet/internal/envelope"
	"testing"
	"time"
)

func TestNativeChatAlertUsesCurrentReadState(t *testing.T) {
	for _, linkedRead := range []bool{false, true} {
		for _, laterUnread := range []bool{false, true} {
			t.Run(fmt.Sprintf("linked=%t/later-unread=%t", linkedRead, laterUnread), func(t *testing.T) {
				w, conv, n, stop := alertWorld(t, time.Hour)
				reader := w.bob
				if linkedRead {
					reader = linked(t, w.bob)
				}
				allowAliceAt(t, w)
				const body = "read before the native alert is due"
				sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: body})
				eventually(t, "reader receives original", func() bool {
					messages, err := reader.ConversationMessages(conv)
					if err != nil {
						return false
					}
					for _, m := range messages {
						if m.Body == body {
							return true
						}
					}
					return false
				})
				id, _ := readState(t, reader, conv, body)
				if err := reader.MarkRead([]string{id}); err != nil {
					t.Fatal(err)
				}
				eventually(t, "read state reaches notifier", func() bool {
					_, read := readState(t, w.bob, conv, body)
					return read
				})
				if laterUnread {
					sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "newer unseen message"})
				}
				stop()
				before := n.count()
				if before != 0 {
					t.Fatal("notification appeared before controlled due processing")
				}
				if _, err := w.bob.showDueAlerts(time.Now().Add(2 * time.Hour)); err != nil {
					t.Fatal(err)
				}
				want := before
				if laterUnread {
					want++
				}
				if got := n.count(); got != want {
					t.Fatalf("notifications=%d, want %d", got, want)
				}
			})
		}
	}
}

func TestNativeChatAlertPreservesSuppression(t *testing.T) {
	w, conv, n, stop := alertWorld(t, 30*time.Second)
	stop() // deterministic due processing without another alert loop
	var fragments []string
	w.bob.nativeNotify = func(fragment string) { fragments = append(fragments, fragment) }
	// Use the already established direct fixture admission path by restarting
	// transport only when receiving, then stop before processing due alerts.
	receive := func(body string) string {
		stop, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true, Notify: w.bob.nativeNotify})
		id := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: body})
		stop()
		return id
	}
	receive("alerts disabled")
	if _, e := w.bob.showDueAlerts(time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(fragments) != 0 {
		t.Fatal("alert while disabled")
	}
	allowAliceAt(t, w)
	id := receive("presented")
	if e := w.bob.AlertPresented(conv, []string{id}); e != nil {
		t.Fatal(e)
	}
	if _, e := w.bob.showDueAlerts(time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(fragments) != 0 {
		t.Fatal("presented turn alerted")
	}
	receive("unseen")
	if _, e := w.bob.showDueAlerts(time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(fragments) != 1 || fragments[0] != "conv="+conv {
		t.Fatalf("destinations %v", fragments)
	}
	if n.count() != 0 {
		t.Fatal("desktop notification used for native callback")
	}
	if _, e := w.bob.showDueAlerts(time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(fragments) != 1 {
		t.Fatal("notification repeated")
	}
}

// Real direct-device traffic uses the same alert queue as person chats. An
// answer is actionable arrival, not an execution request or a history replay.
func TestNativeDirectAgentReplyAlert(t *testing.T) {
	w, _, _, stop := alertWorld(t, time.Hour)
	stop()
	var got []string
	notify := func(fragment string) { got = append(got, fragment) }
	w.bob.nativeNotify = notify
	request, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "isolated notification fixture"})
	if err != nil {
		t.Fatal(err)
	}
	receive := func(status, body string) string {
		t.Helper()
		stop, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true, Notify: notify})
		sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: func() string {
			if status == envelope.StatusProgress {
				return envelope.KindMessage
			}
			return envelope.KindAnswer
		}(), ReplyTo: request.ID, Status: status, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "direct reply stored", func() bool { return inboxHas(t, w.bob, sent.ID) })
		stop()
		return sent.ID
	}
	due := func() {
		t.Helper()
		if _, err := w.bob.showDueAlerts(time.Now().Add(2 * time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	receive("", "disabled reply")
	due()
	if len(got) != 0 {
		t.Fatal("notifications were disabled", got)
	}
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	receive(envelope.StatusProgress, "still working")
	due()
	if len(got) != 0 {
		t.Fatal("progress must stay quiet", got)
	}
	seen := receive("", "already in view")
	if err := w.bob.AlertPresented(request.ID, []string{seen}); err != nil {
		t.Fatal(err)
	}
	due()
	if len(got) != 0 {
		t.Fatal("presented reply alerted", got)
	}
	unseen := receive("", "terminal reply")
	due()
	due()
	if len(got) != 1 || got[0] != "msg="+unseen+"&dir=in" {
		t.Fatalf("direct reply destination/dedup %v", got)
	}
	// Re-admission of identical content cannot requeue a notification.
	row := envelope.Inner{V: envelope.Version, ID: unseen, From: w.alice.Address, To: w.bob.Address, Kind: envelope.KindAnswer, ReplyTo: request.ID, Body: "terminal reply"}
	if err := w.bob.store.addInbox(row, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	due()
	if len(got) != 1 {
		t.Fatal("duplicate re-notified", got)
	}
}
