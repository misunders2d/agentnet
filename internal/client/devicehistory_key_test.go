package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The browser's duplicate handling must preserve the same original key as the
// native sidecar. A current pin is not evidence of an unknown legacy recipient.
func TestDeviceHistoryPreservesOriginalRecipientKey(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	own, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	for _, firstKey := range []string{"", w.bob.Self().Fingerprint()} {
		request, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "original recipient identity"})
		if err != nil {
			t.Fatal(err)
		}
		r, err := a.deviceHistorySource(a.store.db, "out", request.ID)
		if err != nil {
			t.Fatal(err)
		}
		item, err := json.Marshal(r.item)
		if err != nil {
			t.Fatal(err)
		}
		for _, incomingKey := range []string{firstKey, "", w.bob.Self().Fingerprint(), phone.Self().Fingerprint()} {
			body, err := json.Marshal(protocol.DeviceHistory{V: 1, Person: own.info.Person, Roster: own.info.Roster, Recipient: r.to, RecipientKey: incomingKey, Item: item})
			if err != nil {
				t.Fatal(err)
			}
			env := sealTo(t, a, phone, envelope.Inner{V: envelope.Version2, Kind: envelope.KindMessage, Sub: envelope.SubDeviceHistory, Replica: true, Body: string(body)})
			groupGovernanceDeliver(t, a, phone, env)
			stored, err := phone.deviceHistoryOriginal(phone.store.db, request.ID)
			if err != nil || stored.toKey != firstKey {
				t.Fatalf("original key changed: first=%q duplicate=%q stored=%q err=%v", firstKey, incomingKey, stored.toKey, err)
			}
			var executable int
			if err = phone.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND (replica!=1 OR state!='' OR attempts!=0)`, request.ID).Scan(&executable); err != nil || executable != 0 {
				t.Fatalf("duplicate gained execution state: %d %v", executable, err)
			}
		}
	}
}
