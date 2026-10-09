//go:build linux

package ui

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func groupHistoryWitnessVectors(t *testing.T, root protocol.ConvRoot, first protocol.GroupState, alice, bob *identity.Identity, ar, br protocol.PersonRoster, browser identity.Public, original protocol.GroupCommit, parts map[string]any) map[string]any {
	t.Helper()
	states := []protocol.GroupState{first}
	records := []protocol.GroupCommit{original}
	for i := int64(1); i <= 3; i++ {
		s := states[len(states)-1]
		s.Members = slices.Clone(s.Members)
		s.Seq = i
		s.Prev = states[len(states)-1].Hash()
		signer, roster := alice, ar
		if i > 1 {
			signer, roster = bob, br
		}
		s.Actor, s.ActorRoster, s.By = roster.Person, roster.Hash(), roster.Devices[0].Fingerprint()
		if i == 1 {
			for j := range s.Members {
				if s.Members[j].Person == br.Person {
					s.Members[j].Admin = true
				}
			}
		}
		if i == 2 {
			s.Members = slices.DeleteFunc(s.Members, func(m protocol.GroupMember) bool { return m.Person == ar.Person })
		}
		if i == 3 {
			adm := protocol.GroupAdmission{Conv: root.ID(), Realm: root.Realm, Person: ar.Person, Roster: ar.Hash(), Seq: i, Prev: s.Prev, By: ar.Devices[0].Fingerprint()}
			adm.Sign(alice.Sign)
			s.Members = append(s.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: ar.Person, Roster: ar.Hash()}, Admission: adm})
			slices.SortFunc(s.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
		}
		s.Sign(signer.Sign)
		var ct bytes.Buffer
		key, _ := browser.Recipient()
		enc, err := age.Encrypt(&ct, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = enc.Write([]byte(marshal(t, client.GroupContext{Root: root, State: s}))); err != nil {
			t.Fatal(err)
		}
		if err = enc.Close(); err != nil {
			t.Fatal(err)
		}
		c := protocol.GroupCommit{V: 1, Conv: root.ID(), Realm: root.Realm, Bootstrap: root.Creator.Fingerprint, Seq: i, Prev: s.Prev, Hash: s.Hash(), Admins: s.Admins(), Writer: roster.Devices[0].Address, Actor: s.Actor, ActorRoster: s.ActorRoster, Ciphertext: ct.Bytes()}
		c.Sign(signer.Sign)
		states = append(states, s)
		records = append(records, c)
	}
	var h client.HistoryItem
	if err := json.Unmarshal([]byte(parts["history-p6-root"].(string)), &h); err != nil {
		t.Fatal(err)
	}
	p := client.GroupContext{Root: root, State: first}
	for _, name := range []string{"p6-0-invite", "p6-0-scope", "p6-0-accept", "p6-1-invite", "p6-1-scope", "p6-1-accept"} {
		raw := parts[name].(map[string]any)["inner"]
		b, _ := json.Marshal(raw)
		var in struct{ Body string }
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatal(err)
		}
		ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
		if err != nil {
			t.Fatal(err)
		}
		p.Memberships = append(p.Memberships, ev)
	}
	h.GroupHistory = &p
	key, err := x509.MarshalPKCS8PrivateKey(bob.Sign)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"states": states, "records": records, "history": marshal(t, h), "bob_private": base64.StdEncoding.EncodeToString(key)}
}
