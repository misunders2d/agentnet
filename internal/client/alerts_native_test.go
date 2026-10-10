package client

import (
	"testing"
	"time"
)

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
