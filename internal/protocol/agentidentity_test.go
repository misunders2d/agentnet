package protocol

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

const AgentVectorCanonical = `agentnet-agent-v1
{"v":1,"id":"00112233445566778899aabbccddeeff","host":"vitalii/desk","host_key":"19c77bce-aca933c7-80e1c0e9-e46fc988","label":"Builder \u003c\u0026\u003e Ю","ts":1700000000}`
const AgentVectorHash = "cf26d8a25764b83d020a1bd0eeed51d2ab206078b071f905ab500215ee1a5d5e"
const AgentVectorSignature = "c4f696037567b7b71bd8e370b799dcedac813d59d4b8e36b6e0bd0bc9bdae14b842c1d816edb923a2d85dc4c56ef77148989f194cc8570ec33e7e1c938b4060b"

func TestAgentIdentityVector(t *testing.T) {
	key, pub := vecDesk()
	r := AgentRecord{V: 1, ID: "00112233445566778899aabbccddeeff", Host: pub.Address, HostKey: pub.Fingerprint(), Label: "Builder <&> Ю", TS: 1700000000}
	r.Sign(key.Sign)
	if err := r.Verify(pub); err != nil {
		t.Fatal(err)
	}
	if string(r.Canonical()) != AgentVectorCanonical || r.Hash() != AgentVectorHash || hex.EncodeToString(r.Sig) != AgentVectorSignature {
		t.Fatal("agent canonical vector differs")
	}
	raw, _ := json.Marshal(r)
	parsed, err := ParseAgentRecord(raw)
	if err != nil || parsed.Verify(pub) != nil {
		t.Fatal("roundtrip", err)
	}
}
func TestAgentIdentityExactHostAndShape(t *testing.T) {
	key, pub := vecDesk()
	_, other := vecPhone()
	r := AgentRecord{V: 1, ID: NewID(), Host: pub.Address, HostKey: pub.Fingerprint(), Label: "same label", TS: 1}
	r.Sign(key.Sign)
	if r.Verify(other) == nil {
		t.Fatal("foreign host verified")
	}
	cases := []AgentRecord{r, r, r, r, r}
	cases[0].ID = "short"
	cases[1].ID = "00112233445566778899AABBCCDDEEFF"
	cases[2].Host = other.Address
	cases[3].HostKey = other.Fingerprint()
	cases[4].Label = "label\ncontrol"
	for i, bad := range cases {
		bad.Sign(key.Sign)
		if bad.Verify(pub) == nil {
			t.Fatalf("shape %d", i)
		}
	}
	forged := r
	forged.Sig = append([]byte{}, r.Sig...)
	forged.Sig[0] ^= 1
	if forged.Verify(pub) == nil {
		t.Fatal("forged signature verified")
	}
}
