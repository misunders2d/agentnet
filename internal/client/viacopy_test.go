package client

import (
	"bytes"
	"io"
	"testing"
)

// BUG-12: a message sent from the person's other device is shown here as
// sent (out, Via) and stored here as received; its file opens, and it can
// be reacted to and edited, when named the way the view shows it.
func TestMessageFromOtherDeviceOpensAndTakesControls(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.alice, w.bob)
	path, content := writeFile(t, t.TempDir(), "plan.txt", 300)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "from the laptop", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	var shown ConvMessage
	eventually(t, "the phone to show it as sent from the laptop", func() bool {
		msgs, _ := phone.ConversationMessages(conv)
		for _, m := range msgs {
			if m.LID == sent.LID {
				shown = m
				return true
			}
		}
		return false
	})
	if shown.Dir != "out" || shown.Via != w.alice.Address || len(shown.Attachments) != 1 || !shown.Attachments[0].Openable {
		t.Fatalf("shown %+v", shown)
	}
	r, _, err := phone.OpenFileFrom(tctx(t), shown.Dir, shown.ID, 0)
	if err != nil {
		t.Fatalf("open as shown: %v", err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, content) {
		t.Fatal("opened other bytes")
	}
	ref, err := phone.RefOf(conv, shown.ID, shown.Dir)
	if err != nil {
		t.Fatalf("ref as shown: %v", err)
	}
	if ref.ID != sent.LID || ref.Fingerprint != w.alice.Self().Fingerprint() {
		t.Fatalf("ref %+v", ref)
	}
	if _, err := phone.React(tctx(t), ref, "👍", false); err != nil {
		t.Fatalf("react: %v", err)
	}
	if _, err := phone.Revise(tctx(t), ref, "edited on the phone"); err != nil {
		t.Fatalf("edit: %v", err)
	}
}
