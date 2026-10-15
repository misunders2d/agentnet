package ui

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

func TestBrowserFollowupAttemptWireMatchesGo(t *testing.T) {
	w := startWireNode(t)
	setup := w.ok(map[string]any{"op": "setup", "address": "browser/phone"})
	var pub identity.Public
	if err := json.Unmarshal([]byte(setup["public"].(string)), &pub); err != nil {
		t.Fatal(err)
	}
	native, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	nativePub := native.Public("native/desk")
	for _, attempt := range []int64{0, 1, 9007199254740991} {
		id, original := protocol.NewID(), protocol.NewID()
		f := &envelope.FollowupRef{Ref: envelope.Ref{ID: original, Fingerprint: pub.Fingerprint()}, Attempt: attempt}
		message := map[string]any{"id": id, "to": nativePub.Address, "ts": 1, "kind": "question", "body": "correct", "reply_to": original, "followup": f}
		raw := w.ok(map[string]any{"op": "seal", "to": publicJSON(t, nativePub), "message": message})["envelope"].(string)
		var sealed envelope.Envelope
		if err := json.Unmarshal([]byte(raw), &sealed); err != nil {
			t.Fatal(err)
		}
		in, err := envelope.Open(sealed, native, nativePub.Address, pub)
		if err != nil || in.Followup == nil || in.Followup.Attempt != attempt {
			t.Fatal(attempt, err, in.Followup)
		}
		recipient, _ := pub.Recipient()
		in.ID = protocol.NewID()
		in.From = nativePub.Address
		in.To = pub.Address
		sealed, err = envelope.Seal(in, native.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		bytes, _ := json.Marshal(sealed)
		opened := w.ok(map[string]any{"op": "open", "from": publicJSON(t, nativePub), "envelope": string(bytes)})["inner"].(map[string]any)
		got := opened["followup"].(map[string]any)
		if attempt > 0 && got["attempt"] != float64(attempt) || attempt == 0 && got["attempt"] != nil {
			t.Fatal(got)
		}
	}
}
