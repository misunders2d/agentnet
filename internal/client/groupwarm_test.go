package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupWarmOriginalReplay(t *testing.T) {
	w, p0, c0 := newGroupPublicationFixture(t)
	p1 := p0
	p1.State.Seq = 1
	p1.State.Prev = c0.Hash
	p1.State.Title = "One"
	p1.State.Members = slices.Clone(p0.State.Members)
	p1.State.Sign(w.alice.id.Sign)
	c1 := sealedCurrentProof(t, w.alice, w.bob, p1)
	p2 := p1
	p2.State.Seq = 2
	p2.State.Prev = c1.Hash
	p2.State.Title = "Two"
	p2.State.Sign(w.alice.id.Sign)
	c2 := sealedCurrentProof(t, w.alice, w.bob, p2)
	if err := w.bob.IngestGroupProofPage(tctx(t), p0.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c0}}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.AcceptGroupContext(tctx(t), p0); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.IngestGroupProofPage(tctx(t), p0.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c1, c2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.GroupMembers(p0.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("stale context became usable: %v", err)
	}
	if err := w.bob.AcceptGroupContext(tctx(t), p2); err != nil {
		t.Fatal(err)
	}
	got, err := w.bob.GroupContext(p0.State.Conv)
	if err != nil || got.State.Hash() != p2.State.Hash() {
		t.Fatalf("recovery failed %v", err)
	}
	home := w.bob.home
	w.bob.Close()
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err = reopened.GroupContext(p0.State.Conv); err != nil || got.State.Seq != 2 {
		t.Fatalf("reload recovery %v", err)
	}
}

func TestGroupWarmFreshSelfConsentBoundary(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fresh", "fresh-at-target", "same-old", "older-different", "known-removed", "invalid-decrypted", "malformed-decrypted", "cipher-corrupt", "join-slot-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			w, p0, c0 := newGroupPublicationFixture(t)
			bob, _, err := w.bob.store.selfPerson(w.bob.Address)
			if err != nil {
				t.Fatal(err)
			}
			makeState := func(prev GroupContext, seq int64, present bool, admission *protocol.GroupAdmission) GroupContext {
				p := prev
				p.Proof = nil
				p.State.Members = slices.Clone(prev.State.Members)
				p.State.Seq = seq
				p.State.Prev = prev.State.Hash()
				p.State.Title = "step" + string(rune('0'+seq))
				p.State.Members = slices.DeleteFunc(p.State.Members, func(m protocol.GroupMember) bool { return m.Person == bob.roster.Person })
				if present {
					m, _ := p0.State.Member(bob.roster.Person)
					if admission != nil {
						m.Admission = *admission
					}
					p.State.Members = append(p.State.Members, m)
					slices.SortFunc(p.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
				}
				p.State.Sign(w.alice.id.Sign)
				return p
			}
			p1 := makeState(p0, 1, false, nil)
			// Legitimate removal ciphertext excludes Bob entirely.
			p1Build := p1
			p1Build.Proof = []protocol.GroupState{p0.State}
			c1, err := w.alice.BuildGroupCommit(tctx(t), p1Build)
			if err != nil {
				t.Fatal(err)
			}
			admission, err := w.bob.SignGroupAdmission(p0.Root, 2, p1.State.Hash(), nil)
			if err != nil {
				t.Fatal(err)
			}
			p2 := makeState(p1, 2, true, &admission)
			c2 := sealedCurrentProof(t, w.alice, w.bob, p2)
			initial := p0
			records := []protocol.GroupCommit{c0}
			target := p2
			if mode == "same-old" {
				target = makeState(p1, 2, true, nil)
				c2 = sealedCurrentProof(t, w.alice, w.bob, target)
			}
			records = append(records, c1, c2)
			switch mode {
			case "fresh", "join-slot-mismatch":
				if mode == "join-slot-mismatch" {
					admission.History = []protocol.GroupHistoryRef{{LID: protocol.NewID(), Author: w.bob.Self().Fingerprint(), Hash: strings.Repeat("e", 64)}}
					admission.Sign(w.bob.id.Sign)
				}
				target = makeState(p2, 3, true, &admission)
				records = append(records, sealedCurrentProof(t, w.alice, w.bob, target))
			case "older-different", "known-removed":
				initial = p2
				p3 := makeState(p2, 3, false, nil)
				p3Build := p3
				p3Build.Proof = []protocol.GroupState{p0.State, p1.State, p2.State}
				c3, e := w.alice.BuildGroupCommit(tctx(t), p3Build)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "known-removed" {
					initial = p3
					c3 = sealedCurrentProof(t, w.alice, w.bob, p3)
				}
				p4 := makeState(p3, 4, false, nil)
				p4Build := p4
				p4Build.Proof = []protocol.GroupState{p0.State, p1.State, p2.State, p3.State}
				c4, e := w.alice.BuildGroupCommit(tctx(t), p4Build)
				if e != nil {
					t.Fatal(e)
				}
				var older *protocol.GroupAdmission
				if mode == "known-removed" {
					older = &admission
				}
				target = makeState(p4, 5, true, older)
				records = append(records, c3, c4, sealedCurrentProof(t, w.alice, w.bob, target))
			case "invalid-decrypted", "malformed-decrypted", "cipher-corrupt":
				// A decryptable invalid transition cannot be hidden by later fresh consent.
				invalidAdmission, e := w.bob.SignGroupAdmission(p0.Root, 1, p0.State.Hash(), nil)
				if e != nil {
					t.Fatal(e)
				}
				bad := makeState(p0, 1, true, &invalidAdmission)
				bad.State.Prev = c0.Hash
				bad.State.Sign(w.alice.id.Sign)
				records[1] = sealedCurrentProof(t, w.alice, w.bob, bad)
				if mode == "malformed-decrypted" {
					raw, e := json.Marshal(bad)
					if e != nil {
						t.Fatal(e)
					}
					raw = append(raw[:len(raw)-1], []byte(`,"unknown":1}`)...)
					var ct bytes.Buffer
					writer, e := age.Encrypt(&ct, w.bob.id.Box.Recipient())
					if e != nil {
						t.Fatal(e)
					}
					if _, e = writer.Write(raw); e != nil {
						t.Fatal(e)
					}
					if e = writer.Close(); e != nil {
						t.Fatal(e)
					}
					records[1].Ciphertext = ct.Bytes()
					records[1].Sign(w.alice.id.Sign)
				}
				if mode == "cipher-corrupt" {
					records[1].Ciphertext[len(records[1].Ciphertext)-1] ^= 1
					records[1].Sign(w.alice.id.Sign)
				}
				p2 = makeState(bad, 2, false, nil)
				records[2] = sealedCurrentProof(t, w.alice, w.bob, p2)
				laterAdmission, e := w.bob.SignGroupAdmission(p0.Root, 3, p2.State.Hash(), nil)
				if e != nil {
					t.Fatal(e)
				}
				target = makeState(p2, 3, true, &laterAdmission)
				records = append(records, sealedCurrentProof(t, w.alice, w.bob, target))
			}
			through := int(initial.State.Seq) + 1
			if err = w.bob.IngestGroupProofPage(tctx(t), p0.Root, protocol.GroupJournalPage{Records: records[:through]}); err != nil {
				t.Fatal(err)
			}
			if initial.State.Seq == 0 {
				err = w.bob.AcceptGroupContext(tctx(t), initial)
			} else {
				err = w.bob.AcceptGroupCommit(tctx(t), records[initial.State.Seq])
			}
			if err != nil {
				t.Fatal("initial", err)
			}
			if err = w.bob.IngestGroupProofPage(tctx(t), p0.Root, protocol.GroupJournalPage{Records: records[through:]}); err != nil {
				t.Fatal(err)
			}
			resolve, e := w.bob.groupResolver(tctx(t), target)
			if e != nil {
				t.Fatal(e)
			}
			if e = target.State.VerifyCurrent(target.Root, records[target.State.Seq], resolve, func(seq int64) (protocol.GroupCommit, bool) {
				if seq < 0 || seq >= int64(len(records)) {
					return protocol.GroupCommit{}, false
				}
				return records[seq], true
			}, nil); e != nil {
				t.Fatalf("test must isolate gap recovery: current snapshot itself invalid: %v", e)
			}
			before, e := w.bob.GroupContext(p0.State.Conv)
			if e != nil {
				t.Fatal(e)
			}
			if e = groupTurnCheck(w.bob.store.db, before, w.bob.Address, w.bob.Self().Fingerprint()); !errors.Is(e, ErrGroupContextPending) {
				t.Fatalf("newer proof enabled stale send: %v", e)
			}
			err = w.bob.AcceptGroupContext(tctx(t), target)
			if mode == "fresh" || mode == "fresh-at-target" {
				if err != nil {
					t.Fatal("fresh join refused", err)
				}
				got, e := w.bob.GroupContext(p0.State.Conv)
				if e != nil || got.State.Seq != target.State.Seq {
					t.Fatal("fresh join not installed", e)
				}
			} else {
				if err == nil {
					t.Fatal("stale or invalid transition accepted")
				}
				if _, e := w.bob.GroupMembers(p0.State.Conv); !errors.Is(e, ErrGroupContextPending) {
					t.Fatalf("partial replay authorized stale state: %v", e)
				}
			}
		})
	}
}

func TestGroupWarmReplayPreservesPinnedWithdrawal(t *testing.T) {
	w, p0, c0 := newGroupPublicationFixture(t)
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	p1 := p0
	p1.State.Members = slices.Clone(p0.State.Members)
	p1.State.Seq = 1
	p1.State.Prev = c0.Hash
	for i := range p1.State.Members {
		if p1.State.Members[i].Person == bob.roster.Person {
			p1.State.Members[i].Admin = false
		}
	}
	p1.State.Sign(w.alice.id.Sign)
	c1 := sealedCurrentProof(t, w.alice, w.bob, p1)
	if err = w.alice.IngestGroupProofPage(tctx(t), p0.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c0, c1}}); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AcceptGroupContext(tctx(t), p1); err != nil {
		t.Fatal(err)
	}
	member, _ := p1.State.Member(bob.roster.Person)
	leave := protocol.GroupWithdrawal{Conv: p1.State.Conv, Realm: p1.State.Realm, Person: member.Person, Admission: member.Admission.Hash(), Roster: bob.roster.Hash(), By: w.bob.Self().Fingerprint()}
	leave.Sign(w.bob.id.Sign)
	p1.Withdrawals = []protocol.GroupWithdrawal{leave}
	if err = w.alice.AcceptGroupContext(tctx(t), p1); err != nil {
		t.Fatal(err)
	}
	p2 := p1
	p2.Withdrawals = nil
	p2.State.Seq = 2
	p2.State.Prev = c1.Hash
	p2.State.Title = "Two"
	p2.State.Sign(w.alice.id.Sign)
	c2 := sealedCurrentProof(t, w.alice, w.bob, p2)
	p3 := p2
	p3.State.Seq = 3
	p3.State.Prev = c2.Hash
	p3.State.Title = "Three"
	p3.State.Sign(w.alice.id.Sign)
	c3 := sealedCurrentProof(t, w.alice, w.bob, p3)
	if err = w.alice.IngestGroupProofPage(tctx(t), p0.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c2, c3}}); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AcceptGroupContext(tctx(t), p3); err != nil {
		t.Fatal(err)
	}
	withdrawals, err := w.alice.groupWithdrawals(p0.State.Conv)
	if err != nil || len(withdrawals) != 1 || withdrawals[0].Admission != leave.Admission {
		t.Fatalf("lost pinned withdrawal %v", err)
	}
	members, err := w.alice.GroupMembers(p0.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.Person == bob.roster.Person {
			t.Fatal("withdrawn Bob became effective during recovery")
		}
	}
}

func TestGroupWarmHeaderMismatchBeforeRecovery(t *testing.T) {
	w, packet, record := newGroupPublicationFixture(t)
	packet.State.Title = "Different encrypted state"
	packet.State.Sign(w.alice.id.Sign)
	body, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	var ct bytes.Buffer
	writer, err := age.Encrypt(&ct, w.bob.id.Box.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	record.Ciphertext = ct.Bytes()
	record.Sign(w.alice.id.Sign)
	called := false
	_, err = w.bob.decodeGroupCommit(tctx(t), record, nil, func(context.Context, GroupContext, protocol.GroupRosterResolver) error { called = true; return nil })
	if called || err == nil || err.Error() != "group: ciphertext state disagrees with signed header" {
		t.Fatalf("mismatched header reached recovery callback: called=%v err=%v", called, err)
	}
}
