package client

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestRequestFollowupRetainedIntent(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	fp := strings.Repeat("a", 64)
	ref := &envelope.Ref{ID: protocol.NewID(), Fingerprint: fp}
	in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: "alice/phone", To: "alice/laptop", Kind: envelope.KindTask, TS: 1, Body: "Use English", ReplyTo: ref.ID, Followup: ref}
	env := envelope.Envelope{V: envelope.Version, ID: in.ID, From: in.From, To: in.To, Kind: in.Kind, TS: in.TS}
	if err = s.addOutbox(env, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := storedRequestFollowup(s.db, "out", in.ID); err != nil || got == nil || *got != *ref {
		t.Fatalf("durable intent: %+v %v", got, err)
	}
	if needs, err := requestFollowupCopy(s.db, in.ID, "", in.Body); err != nil || !needs {
		t.Fatalf("queued reader gate: %v %v", needs, err)
	}
	item := itemOf(in, fp, 1)
	round := item.inner(strings.Repeat("b", 64))
	in.Conv, in.LID = round.Conv, round.LID
	if round.Followup == nil || *round.Followup != *ref || contentHash(in) != contentHash(round) {
		t.Fatal("history changed explicit intent or hash")
	}
	plain := in
	plain.Followup = nil
	if contentHash(plain) == contentHash(in) {
		t.Fatal("explicit intent omitted from conflict identity")
	}
	body, _ := json.Marshal(item)
	if needs, err := requestFollowupCopy(s.db, "", envelope.SubHistory, string(body)); err != nil || !needs {
		t.Fatalf("history reader gate: %v %v", needs, err)
	}
	item.Followup = nil
	body, _ = json.Marshal(item)
	if strings.Contains(string(body), "followup") {
		t.Fatal("nil field changed old history bytes")
	}
}
