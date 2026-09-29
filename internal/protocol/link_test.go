package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func vecOffer() LinkOffer {
	r := vecRoster()
	_, desk := vecDesk()
	inv := Invite{Hub: "https://hub.example", Label: "vitalii", Secret: strings.Repeat("s", 32)}
	return LinkOffer{V: 2, Invite: inv.Encode(), Offer: vecSession, Expires: 1790000600, Person: vecPerson, Seq: 0, Roster: r.Hash(),
		Approver: LinkApprover{Address: desk.Address, Fingerprint: desk.Fingerprint()}, Secret: bytes.Repeat([]byte{7}, LinkSecretSize)}
}

// The offer round-trips; the MAC covers exactly the transcript (vector),
// and any change to it fails the constant-time check.
func TestLinkVectors(t *testing.T) {
	o := vecOffer()
	if got, err := DecodeLinkOffer("https://hub.example/#" + o.Encode()); err != nil || got.Offer != o.Offer {
		t.Fatalf("from a URL: %+v %v", got, err)
	}
	got, err := DecodeLinkOffer("#" + o.Encode() + "\n")
	if err != nil || got.Offer != o.Offer || !bytes.Equal(got.Secret, o.Secret) || got.Approver != o.Approver {
		t.Fatalf("decode: %+v %v", got, err)
	}
	phone, pub := vecPhone()
	join := ed25519.Sign(phone.Sign, JoinBytes(o.Person, o.Seq+1, o.Roster, pub))
	mac := LinkMAC(o, pub, join)
	if hex.EncodeToString(mac) != VecLinkMAC {
		t.Errorf("link mac %x", mac)
	}
	if !strings.HasPrefix(string(LinkTranscript(o, pub, join)), LinkDomain+`{"offer":"00112233445566778899aabbccddeeff","expires":1790000600,"person":"0123456789abcdef0123456789abcdef","seq":0,"roster":"`+o.Roster+`","approver":"`+o.Approver.Fingerprint+`","device":{"address":"vitalii/phone"`) {
		t.Errorf("transcript: %s", LinkTranscript(o, pub, join))
	}
	if !CheckLinkMAC(o, pub, join, mac) {
		t.Fatal("the MAC does not check")
	}
	_, other := vecDevice(3, vecBoxPhone, "vitalii/phone")
	for what, bad := range map[string]func() bool{
		"another key":    func() bool { return CheckLinkMAC(o, other, join, mac) },
		"another secret": func() bool { p := o; p.Secret = bytes.Repeat([]byte{8}, 32); return CheckLinkMAC(p, pub, join, mac) },
		"another offer":  func() bool { p := o; p.Offer = vecOther; return CheckLinkMAC(p, pub, join, mac) },
		"later expiry":   func() bool { p := o; p.Expires++; return CheckLinkMAC(p, pub, join, mac) },
		"another step":   func() bool { p := o; p.Seq = 1; return CheckLinkMAC(p, pub, join, mac) },
		"another join":   func() bool { return CheckLinkMAC(o, pub, append([]byte{1}, join[1:]...), mac) },
	} {
		if bad() {
			t.Errorf("%s: MAC accepted", what)
		}
	}
	for _, s := range []string{"", "agentnet-link-v2:", "agentnet-link-v2:!!", LinkPrefix + "e30", "agentnet-invite:x"} {
		if _, err := DecodeLinkOffer(s); err == nil {
			t.Errorf("decoded %q", s)
		}
	}
	short := o
	short.Secret = short.Secret[:16]
	if _, err := DecodeLinkOffer(short.Encode()); err == nil {
		t.Error("a short secret decoded")
	}
}
