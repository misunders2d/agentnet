package client

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTopicCopyLegacyAndScopedHistory(t *testing.T) {
	main, named := "", protocol.NewID()
	encode := func(v any) string {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, tt := range []struct {
		name, body  string
		needed, bad bool
	}{
		{name: "legacy omitted local body"},
		{name: "ordinary retained item", body: encode(HistoryItem{Body: "ordinary history"})},
		{name: "malformed retained item", body: "{", bad: true},
		{name: "explicit Main scope", body: encode(HistoryItem{Sub: envelope.SubEvent, Body: encode(protocol.ParticipationEvent{Topic: &main})}), needed: true},
		{name: "named scope", body: encode(HistoryItem{Sub: envelope.SubEvent, Body: encode(protocol.ParticipationEvent{Topic: &named})}), needed: true},
		{name: "retained human proof", body: encode(HistoryItem{Human: &envelope.HumanTurn{Proof: []protocol.ParticipationEvent{{Topic: &named}}}}), needed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// These decisions depend only on retained typed metadata; ordinary
			// pre-scope copies intentionally have no local plaintext body.
			needed, err := topicCopy(nil, "", "", envelope.SubHistory, tt.body, "")
			if needed != tt.needed || (err != nil) != tt.bad {
				t.Fatalf("topic capability: needed=%v err=%v; want needed=%v malformed=%v", needed, err, tt.needed, tt.bad)
			}
		})
	}
}

func TestTopicHistoryRequirementSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.db.Close() }()
	main, named := "", protocol.NewID()
	var copies []outCopy
	for _, scope := range []*string{nil, &main, &named} {
		event, _ := json.Marshal(protocol.ParticipationEvent{Type: protocol.EventInvite, PID: protocol.NewID(), Audience: protocol.AudienceConversation, Topic: scope})
		item, _ := json.Marshal(HistoryItem{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), Sub: envelope.SubEvent, Body: string(event)})
		copies = append(copies, outCopy{env: envelope.Envelope{ID: protocol.NewID(), To: "person/phone"}, in: envelope.Inner{Conv: "conv", LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Body: string(item)}, state: stateQueued})
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = insertCopies(tx, copies); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range copies {
		var body, sealed string
		if err = s.db.QueryRow(`SELECT body,envelope FROM outbox WHERE id=?`, c.env.ID).Scan(&body, &sealed); err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(c.env)
		if sealed != string(want) {
			t.Fatal("restart changed sealed history bytes")
		}
		needed, err := topicCopy(s.db, c.in.Conv, "", c.in.Sub, body, "")
		if err != nil || needed != (i > 0) || i == 0 && body != "" || i > 0 && body != c.in.Body {
			t.Fatalf("copy %d lost scoped requirement or changed ordinary retention: needed=%v retained=%v err=%v", i, needed, body != "", err)
		}
	}
}
