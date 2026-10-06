package protocol

import (
	"crypto/ed25519"
	"github.com/misunders2d/agentnet/internal/identity"
	"testing"
)

func TestP7OnlyHumanDevicesEnroll(t *testing.T) {
	desk, dp := vecDesk()
	host, hp := vecPhone()
	first := vecRoster()
	r := vecLinked()
	r.HumanKeys = []string{dp.Fingerprint()}
	r.Sign(desk.Sign)
	if _, e := r.VerifyNext(first); e != nil {
		t.Fatal(e)
	}
	key, _ := identity.Generate()
	pub := key.Public("vitalii/agent2")
	next := PersonRoster{Person: r.Person, Label: r.Label, Seq: r.Seq + 1, Prev: r.Hash(), Devices: append(append([]identity.Public{}, r.Devices...), pub), HumanKeys: r.Humans(), By: hp.Fingerprint()}
	next.Join = ed25519.Sign(key.Sign, JoinBytes(next.Person, next.Seq, next.Prev, pub))
	next.Sign(host.Sign)
	if _, e := next.VerifyNext(r); e == nil {
		t.Fatal("agent host added device")
	}
	promoted := r
	promoted.Seq++
	promoted.Prev = r.Hash()
	promoted.By = hp.Fingerprint()
	promoted.Join = nil
	promoted.HumanKeys = append(promoted.Humans(), hp.Fingerprint())
	promoted.Sign(host.Sign)
	if _, e := promoted.VerifyNext(r); e == nil {
		t.Fatal("agent host promoted itself")
	}
	next.By = dp.Fingerprint()
	next.Sign(desk.Sign)
	if _, e := next.VerifyNext(r); e != nil {
		t.Fatal("human could not add host", e)
	}
	bad := next
	bad.HumanKeys = []string{dp.Fingerprint(), dp.Fingerprint()}
	bad.Sign(desk.Sign)
	if bad.Validate() == nil {
		t.Fatal("duplicate human key accepted")
	}
	bad.HumanKeys = []string{vecFP}
	if bad.Validate() == nil {
		t.Fatal("unknown human key accepted")
	}
}
