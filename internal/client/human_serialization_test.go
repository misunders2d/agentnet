package client

import (
	"bytes"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

func TestHumanHistoryReceiverSerializationAndLegacyAbsence(t *testing.T) {
	_, _, h := humanVector()
	orig := envelope.Inner{V: 2, ID: protocol.NewID(), LID: protocol.NewID(), From: "fixture/carol", To: "fixture/alice", TS: 1, Conv: h.Proof[0].Conv, Kind: envelope.KindMessage, Body: "unchanged captured turn", PID: h.AuthorPID, Human: h}
	item := itemOf(orig, h.Proof[0].Host.Fingerprint, 1000)
	round := item.inner(orig.Conv)
	if humanJSON(round.Human) != humanJSON(h) || contentHash(round) != contentHash(orig) {
		t.Fatal("history descriptor/hash changed")
	}
	request := envelope.ReceiverRequest{ID: orig.ID, LID: orig.LID, From: orig.From, FromKey: h.Proof[0].Host.Fingerprint, TS: orig.TS, Conv: orig.Conv, Kind: orig.Kind, Body: orig.Body, PID: orig.PID, Human: h}
	rr := receiverRequestInner(request)
	if humanJSON(rr.Human) != humanJSON(h) || contentHash(rr) != contentHash(orig) {
		t.Fatal("receiver descriptor/hash changed")
	}
	for _, value := range []any{item, request} {
		raw, err := json.Marshal(value)
		if err != nil || !bytes.Contains(raw, []byte(`"human":`)) {
			t.Fatal("wire descriptor omitted")
		}
	}
	item.Human = nil
	request.Human = nil
	for _, value := range []any{item, request} {
		raw, _ := json.Marshal(value)
		if bytes.Contains(raw, []byte(`"human"`)) {
			t.Fatal("nil extension changes legacy DTO bytes")
		}
	}
	if agentRequirement(orig) != protocol.CapHumanParticipation {
		t.Fatal("human queue capability lost")
	}
	body, _ := json.Marshal(itemOf(orig, h.Proof[0].Host.Fingerprint, 1000))
	carrier := envelope.Inner{Sub: envelope.SubHistory, Body: string(body)}
	if agentRequirement(carrier) != protocol.CapHumanParticipation {
		t.Fatal("human history queue capability lost")
	}
	for _, c := range []string{protocol.CapHumanParticipation, protocol.CapAgentReaction, protocol.CapConvClear} {
		if !containsCap(ownCaps, c) {
			t.Fatalf("qualified capability %s not advertised", c)
		}
	}
}
func containsCap(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}
