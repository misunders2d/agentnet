package client

import (
	"bytes"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
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
	// Negotiation uses the signed record's Reads contract: rm1 already
	// guarantees its shipped readers, preserving advertisement headroom.
	key, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rec := protocol.CapsRecord{Address: "fixture/desk", Session: protocol.NewID(), TS: 1, Caps: append([]string(nil), ownCaps...)}
	rec.Sign(key.Sign)
	if err = rec.Verify(key.Public(rec.Address).SignKey); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	profile := protocol.Profile{Sessions: []string{rec.Session}, Caps: []json.RawMessage{raw}}
	basic := []string{protocol.CapHumanParticipation, protocol.CapAgentReaction, protocol.CapConvClear, protocol.CapProgress}
	explicit := []string{protocol.CapGroupHumanParticipation, protocol.CapGroupInvitationControl, protocol.CapReadSync}
	for _, c := range append(append([]string(nil), basic...), explicit...) {
		if !profile.Supports(rec.Address, key.Public(rec.Address).SignKey, c) {
			t.Fatalf("signed current profile cannot negotiate %s", c)
		}
	}
	if len(rec.Caps)+1 > protocol.MaxAdvertisedCaps {
		t.Fatal("agent hint exceeds advertisement bound")
	}
	room := protocol.CapsRecord{Address: rec.Address, Session: protocol.NewID(), TS: 1, Caps: []string{protocol.CapRoom}}
	room.Sign(key.Sign)
	oldRaw, err := json.Marshal(room)
	if err != nil {
		t.Fatal(err)
	}
	mixed := protocol.Profile{Sessions: []string{rec.Session, room.Session}, Caps: []json.RawMessage{raw, oldRaw}}
	for _, c := range basic {
		if !mixed.Supports(rec.Address, key.Public(rec.Address).SignKey, c) {
			t.Fatalf("rm1 lost implied support for %s", c)
		}
	}
	for _, c := range explicit {
		if !rec.Has(c) || room.Reads(c) {
			t.Fatalf("new capability %s must remain explicit, never rm1-implied", c)
		}
		if mixed.Supports(rec.Address, key.Public(rec.Address).SignKey, c) {
			t.Fatalf("mixed old/new sessions incorrectly negotiate %s", c)
		}
	}
}
