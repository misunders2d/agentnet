package client

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// MEL-841 (2026-11-27): an administrator invited the same person into a
// group twice and cancelled the first invitation. The cancelled one must not
// be acceptable once the guest knows; an acceptance sent before the guest
// knew must not revive it at the inviter; the remaining invitation works and
// the guest sees the inviter's later turns, nothing left held. These
// journeys pass on this source: regression coverage, not a reproduction.
func TestGroupGuestCancelledInvitationRepro(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	guest := proofReader(t, w, "guest")
	runAgent(t, guest)
	publishGroupFixtureCaps(t, guest, true)
	for _, a := range []*Agent{w.alice, w.bob, guest} {
		roomReader(t, a)
	}
	cancelledGuestJourney(t, w.alice, guest, packet.State.Conv)
}

// The incident's shape: the inviter is the group's only member.
func TestGroupGuestCancelledInvitationSoloGroup(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	for _, a := range []*Agent{w.alice, w.bob} {
		roomReader(t, a)
	}
	cancelledGuestJourney(t, w.alice, w.bob, p.Root.ID())
}

func cancelledGuestJourney(t *testing.T, inviter, guest *Agent, conv string) {
	t.Helper()
	if _, err := inviter.HumanInviteSupport(tctx(t), conv, guest.Address); err != nil {
		t.Fatal(err)
	}
	first, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest first invitation", func() bool { p, e := guest.Participation(first.PID); return e == nil && p.State == PartInvited })
	if _, err = inviter.DismissParticipation(tctx(t), first.PID); err != nil {
		t.Fatal(err)
	}
	if p, e := inviter.Participation(first.PID); e != nil || p.State != PartDismissed {
		t.Fatalf("inviter cancellation: %+v %v", p, e)
	}
	second, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "second")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest second invitation", func() bool { p, e := guest.Participation(second.PID); return e == nil && p.State == PartInvited })
	_ = inviter.FlushOutbox(tctx(t))
	eventually(t, "guest learns cancellation", func() bool { p, e := guest.Participation(first.PID); return e == nil && p.State == PartDismissed })
	if p, err := guest.AcceptParticipation(tctx(t), first.PID); err == nil {
		t.Fatalf("cancelled invitation accepted: %+v", p)
	}
	if _, err = guest.AcceptParticipation(tctx(t), second.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "inviter sees guest active", func() bool { p, e := inviter.Participation(second.PID); return e == nil && p.HumanActive() })
	if _, err = inviter.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest shows inviter turn", func() bool {
		for _, m := range groupTurns(t, guest, conv) {
			if m.Body == "hello" {
				return true
			}
		}
		return false
	})
	assertNothingHeld(t, guest)
}

// assertNothingHeld fails when a has any held (quarantined) copy left.
func assertNothingHeld(t *testing.T, a *Agent) {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT id, sender, reason FROM quarantine`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, sender, reason string
		if err := rows.Scan(&id, &sender, &reason); err != nil {
			t.Fatal(err)
		}
		t.Errorf("%s still holds %s from %s (%s)", a.Address, id, sender, reason)
	}
}

// Two pending invitations at once (Main and a topic: the duplicate guard
// compares topic), through the UI's queued path; the first is cancelled.
func TestGroupGuestCancelledInvitationTwoPending(t *testing.T) {
	for _, unaware := range []bool{false, true} {
		t.Run(map[bool]string{false: "aware", true: "unaware"}[unaware], func(t *testing.T) {
			w, p := groupLifecycleFixture(t, false)
			runAgent(t, w.alice)
			publishGroupFixtureCaps(t, w.alice, true)
			stopB := runAgent(t, w.bob)
			publishGroupFixtureCaps(t, w.bob, true)
			for _, a := range []*Agent{w.alice, w.bob} {
				roomReader(t, a)
			}
			inviter, guest, conv := w.alice, w.bob, p.Root.ID()
			topic := protocol.NewID()
			first, err := inviter.InviteHumanInScope(WithQueuedSend(tctx(t), ""), conv, guest.Address, nil, "first", nil)
			if err != nil {
				t.Fatal(err)
			}
			second, err := inviter.InviteHumanInScope(WithQueuedSend(tctx(t), ""), conv, guest.Address, nil, "second", &topic)
			if err != nil {
				t.Fatal(err)
			}
			for _, pid := range []string{first.PID, second.PID} {
				eventually(t, "guest invitation", func() bool { p, e := guest.Participation(pid); return e == nil && p.State == PartInvited })
			}
			if unaware {
				stopB()
			}
			if _, err = inviter.DismissParticipation(WithQueuedSend(tctx(t), ""), first.PID); err != nil {
				t.Fatal(err)
			}
			_ = inviter.FlushOutbox(tctx(t))
			if unaware {
				// Offline, the guest cannot know; the inviter decides.
				_, _ = guest.AcceptParticipation(tctx(t), first.PID)
				if _, e := guest.AcceptParticipation(tctx(t), second.PID); e != nil {
					t.Fatal(e)
				}
				runAgent(t, guest)
				awaitStaleAccept(t, inviter, conv, first.PID)
				if ip, _ := inviter.Participation(first.PID); ip.State != PartDismissed || ip.HumanActive() {
					t.Fatalf("stale accept revived cancelled invitation at inviter: %+v", ip)
				}
			}
			eventually(t, "guest learns cancellation", func() bool { p, e := guest.Participation(first.PID); return e == nil && p.State == PartDismissed })
			if !unaware {
				if p, err := guest.AcceptParticipation(tctx(t), first.PID); err == nil {
					t.Fatalf("cancelled invitation accepted: %+v", p)
				}
				if _, err = guest.AcceptParticipation(tctx(t), second.PID); err != nil {
					t.Fatal(err)
				}
			}
			eventually(t, "inviter sees guest active", func() bool { p, e := inviter.Participation(second.PID); return e == nil && p.HumanActive() })
			if _, err = inviter.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello", Topic: topic}); err != nil {
				t.Fatal(err)
			}
			eventually(t, "guest shows inviter turn", func() bool {
				for _, m := range groupTurns(t, guest, conv) {
					if m.Body == "hello" {
						return true
					}
				}
				return false
			})
			for _, m := range groupTurns(t, guest, conv) {
				if m.Body == "main only" {
					t.Fatalf("cancelled Main invitation still shows Main turns: %+v", m)
				}
			}
			assertNothingHeld(t, guest)
		})
	}
}

// The membership path ("Retract invitation"), solo group, both orders of the
// guest learning the cancellation.
func TestGroupMemberCancelledInvitationSolo(t *testing.T) {
	for _, unaware := range []bool{false, true} {
		t.Run(map[bool]string{false: "aware", true: "unaware"}[unaware], func(t *testing.T) {
			w := newWorld(t, "")
			persons(t, w.alice, w.bob)
			runAgent(t, w.alice)
			publishGroupFixtureCaps(t, w.alice, true)
			stopB := runAgent(t, w.bob)
			publishGroupFixtureCaps(t, w.bob, true)
			p, err := w.alice.CreateGroup(tctx(t), "Solo")
			if err != nil {
				t.Fatal(err)
			}
			first := groupLifecycleInvite(t, w, p, nil)
			awaitGroupInvitation(t, w.bob, first.ID, "pending")
			if unaware {
				stopB()
			}
			if err = w.alice.CancelGroupInvitation(tctx(t), first.ID); err != nil {
				t.Fatal(err)
			}
			second := groupLifecycleInvite(t, w, p, nil)
			if second.ID == first.ID {
				t.Fatal("fresh invitation reused the cancelled one")
			}
			_ = w.alice.FlushOutbox(tctx(t))
			if unaware {
				_ = w.bob.DecideGroupInvitation(tctx(t), first.ID, true) // offline: the relay fences it
				runAgent(t, w.bob)
			}
			awaitGroupInvitation(t, w.bob, second.ID, "pending")
			eventually(t, "guest learns cancellation", func() bool {
				r, e := groupInvitationIn(w.bob.store.db, first.ID, "in")
				return e == nil && r.State == "cancelled"
			})
			if !unaware {
				if err = w.bob.DecideGroupInvitation(tctx(t), first.ID, true); err == nil {
					t.Fatal("cancelled membership invitation accepted")
				}
			}
			if err = w.bob.DecideGroupInvitation(tctx(t), second.ID, true); err != nil {
				t.Fatal(err)
			}
			eventually(t, "second published", func() bool {
				r, e := groupInvitationIn(w.alice.store.db, second.ID, "out")
				return e == nil && r.State == "published"
			})
			if r, _ := groupInvitationIn(w.alice.store.db, first.ID, "out"); r.State != "cancelled" {
				t.Fatalf("first at inviter: %s", r.State)
			}
			eventually(t, "member context", func() bool {
				got, e := w.bob.GroupContext(p.Root.ID())
				return e == nil && len(got.State.Members) == 2
			})
			if _, err = w.alice.SendConv(tctx(t), p.Root.ID(), ConvOutgoing{Body: "hello"}); err != nil {
				t.Fatal(err)
			}
			eventually(t, "member shows inviter turn", func() bool { return slices.Contains(convBodies(t, w.bob, p.Root.ID()), "in:hello") })
			assertNothingHeld(t, w.bob)
		})
	}
}

// awaitStaleAccept waits until inviter holds the guest's acceptance of pid,
// so a check after it sees that acceptance counted.
func awaitStaleAccept(t *testing.T, inviter *Agent, conv, pid string) {
	t.Helper()
	eventually(t, "inviter holds stale accept", func() bool {
		evs, e := inviter.store.participationEvents(conv, pid)
		return e == nil && slices.ContainsFunc(evs, func(ev protocol.ParticipationEvent) bool { return ev.Type == protocol.EventAccept })
	})
}

// Same scope: the guest, offline, accepts the cancelled invitation; then a
// fresh one is sent and accepted.
func TestGroupGuestCancelledInvitationAcceptedUnaware(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	stopB := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	for _, a := range []*Agent{w.alice, w.bob} {
		roomReader(t, a)
	}
	inviter, guest, conv := w.alice, w.bob, p.Root.ID()
	first, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest first invitation", func() bool { p, e := guest.Participation(first.PID); return e == nil && p.State == PartInvited })
	stopB()
	if _, err = inviter.DismissParticipation(tctx(t), first.PID); err != nil {
		t.Fatal(err)
	}
	_ = inviter.FlushOutbox(tctx(t))
	_, _ = guest.AcceptParticipation(tctx(t), first.PID) // offline: it cannot know
	runAgent(t, guest)
	awaitStaleAccept(t, inviter, conv, first.PID)
	if ip, _ := inviter.Participation(first.PID); ip.State != PartDismissed || ip.HumanActive() {
		t.Fatalf("stale accept revived cancelled invitation at inviter: %+v", ip)
	}
	eventually(t, "guest learns cancellation", func() bool { p, e := guest.Participation(first.PID); return e == nil && p.State == PartDismissed })
	second, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "second")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest second invitation", func() bool { p, e := guest.Participation(second.PID); return e == nil && p.State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), second.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "inviter sees guest active", func() bool { p, e := inviter.Participation(second.PID); return e == nil && p.HumanActive() })
	if _, err = inviter.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest shows inviter turn", func() bool {
		return slices.ContainsFunc(groupTurns(t, guest, conv), func(m ConvMessage) bool { return m.Body == "hello" })
	})
	assertNothingHeld(t, guest)
}

// Mixed routes: the same person invited once as a guest and once as a
// member (neither duplicate guard sees the other path); the first is
// cancelled and both are accepted.
func TestGroupGuestAndMemberInvitationFirstCancelled(t *testing.T) {
	for _, guestFirst := range []bool{true, false} {
		for _, unaware := range []bool{false, true} {
			name := map[bool]string{true: "guest-first", false: "member-first"}[guestFirst] + "/" + map[bool]string{false: "aware", true: "unaware"}[unaware]
			t.Run(name, func(t *testing.T) {
				w := newWorld(t, "")
				persons(t, w.alice, w.bob)
				runAgent(t, w.alice)
				publishGroupFixtureCaps(t, w.alice, true)
				stopB := runAgent(t, w.bob)
				publishGroupFixtureCaps(t, w.bob, true)
				p, err := w.alice.CreateGroup(tctx(t), "Solo")
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range []*Agent{w.alice, w.bob} {
					roomReader(t, a)
				}
				conv := p.Root.ID()
				var guestP ParticipationInfo
				var member GroupInvitationInfo
				inviteGuest := func() {
					guestP, err = w.alice.InviteHumanInScope(WithQueuedSend(tctx(t), ""), conv, w.bob.Address, nil, "guest", nil)
					if err != nil {
						t.Fatal(err)
					}
					eventually(t, "guest invitation", func() bool { p, e := w.bob.Participation(guestP.PID); return e == nil && p.State == PartInvited })
				}
				inviteMember := func() {
					member = groupLifecycleInvite(t, w, p, nil)
					awaitGroupInvitation(t, w.bob, member.ID, "pending")
				}
				if guestFirst {
					inviteGuest()
					inviteMember()
				} else {
					inviteMember()
					inviteGuest()
				}
				if unaware {
					stopB()
				}
				if guestFirst {
					_, err = w.alice.DismissParticipation(WithQueuedSend(tctx(t), ""), guestP.PID)
				} else {
					err = w.alice.CancelGroupInvitation(tctx(t), member.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				_ = w.alice.FlushOutbox(tctx(t))
				if unaware {
					if guestFirst {
						_, _ = w.bob.AcceptParticipation(tctx(t), guestP.PID)
					} else {
						_ = w.bob.DecideGroupInvitation(tctx(t), member.ID, true)
					}
					runAgent(t, w.bob)
					if guestFirst {
						awaitStaleAccept(t, w.alice, conv, guestP.PID)
					}
				}
				if guestFirst {
					eventually(t, "guest learns cancellation", func() bool { p, e := w.bob.Participation(guestP.PID); return e == nil && p.State == PartDismissed })
					if !unaware {
						if _, e := w.bob.AcceptParticipation(tctx(t), guestP.PID); e == nil {
							t.Fatal("cancelled guest invitation accepted")
						}
					}
					if ip, _ := w.alice.Participation(guestP.PID); ip.State != PartDismissed || ip.HumanActive() {
						t.Fatalf("cancelled guest invitation at inviter: %+v", ip)
					}
					if err = w.bob.DecideGroupInvitation(tctx(t), member.ID, true); err != nil {
						t.Fatal(err)
					}
					eventually(t, "membership published", func() bool {
						r, e := groupInvitationIn(w.alice.store.db, member.ID, "out")
						return e == nil && r.State == "published"
					})
				} else {
					eventually(t, "member learns cancellation", func() bool {
						r, e := groupInvitationIn(w.bob.store.db, member.ID, "in")
						return e == nil && r.State == "cancelled"
					})
					if !unaware {
						if e := w.bob.DecideGroupInvitation(tctx(t), member.ID, true); e == nil {
							t.Fatal("cancelled membership invitation accepted")
						}
					}
					if r, _ := groupInvitationIn(w.alice.store.db, member.ID, "out"); r.State != "cancelled" {
						t.Fatalf("cancelled membership at inviter: %s", r.State)
					}
					if _, err = w.bob.AcceptParticipation(tctx(t), guestP.PID); err != nil {
						t.Fatal(err)
					}
					eventually(t, "inviter sees guest active", func() bool { p, e := w.alice.Participation(guestP.PID); return e == nil && p.HumanActive() })
				}
				if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
					t.Fatal(err)
				}
				eventually(t, "invitee shows inviter turn", func() bool {
					return slices.ContainsFunc(groupTurns(t, w.bob, conv), func(m ConvMessage) bool { return m.Body == "hello" })
				})
				assertNothingHeld(t, w.bob)
			})
		}
	}
}

// deliverPIDCopies hands receiver, in order, the sender's stored copies of
// pid addressed to it whose sub is sub, and nothing else.
func deliverPIDCopies(t *testing.T, sender, receiver *Agent, pid, sub string) {
	t.Helper()
	deliverCopies(t, sender, receiver, pidCopies(t, sender, receiver, pid, sub))
}

// pidCopies returns, in order, the sender's stored copies of pid addressed
// to receiver whose sub is sub.
func pidCopies(t *testing.T, sender, receiver *Agent, pid, sub string) []envelope.Envelope {
	t.Helper()
	rows, err := sender.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND pid=? AND sub=? ORDER BY rowid`, receiver.Address, pid, sub)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &env)
		}
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	rows.Close()
	return envs
}

// deliverCopies hands receiver envs, in order, as the relay would.
func deliverCopies(t *testing.T, sender, receiver *Agent, envs []envelope.Envelope) {
	t.Helper()
	for _, env := range envs {
		if err := sender.uploadAll(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if err := receiver.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
}

// The incident's timing (MEL-841): the inviter's dismissal of a pending
// visitor invitation reaches the invitee before any group context. It was
// held for context, so once context arrived the cancelled invitation stayed
// acceptable until a later proof retry. The exact inviter's dismissal of
// this device's own invitation is now recorded at once.
func TestGroupGuestCancellationBeforeContext(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	stopA := runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	stopB := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	stopA()
	stopB()
	inviter, guest, conv := w.alice, w.bob, p.Root.ID()
	first, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inviter.DismissParticipation(tctx(t), first.PID); err != nil {
		t.Fatal(err)
	}
	deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubEvent) // invite, scope, dismissal: no context yet
	if _, err = guest.GroupContext(conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("context before its carrier: %v", err)
	}
	deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubGroupProof)
	deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubGroupContext)
	if got, e := guest.Participation(first.PID); e != nil || got.State != PartDismissed {
		t.Fatalf("cancelled invitation not dismissed once context arrived: %+v %v", got, e)
	}
	if got, e := guest.AcceptParticipation(tctx(t), first.PID); e == nil {
		t.Fatalf("cancelled invitation accepted: %+v", got)
	}
}

// offlineGroupGuestFixture is a solo group whose inviter and invitee have
// published their capabilities and stopped: copies move only by hand.
func offlineGroupGuestFixture(t *testing.T) (*world, string) {
	t.Helper()
	w, p := groupLifecycleFixture(t, false)
	stopA := runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	stopB := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	stopA()
	stopB()
	return w, p.Root.ID()
}

// The incident's order: the first invitation and its dismissal arrive with
// no group context; context comes with the second invitation.
func TestGroupGuestCancellationBeforeContextSecondInvite(t *testing.T) {
	w, conv := offlineGroupGuestFixture(t)
	inviter, guest := w.alice, w.bob
	first, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inviter.DismissParticipation(tctx(t), first.PID); err != nil {
		t.Fatal(err)
	}
	second, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "second")
	if err != nil {
		t.Fatal(err)
	}
	deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubEvent)
	for _, sub := range []string{envelope.SubEvent, envelope.SubGroupProof, envelope.SubGroupContext} {
		deliverPIDCopies(t, inviter, guest, second.PID, sub)
	}
	if got, e := guest.Participation(first.PID); e != nil || got.State != PartDismissed {
		t.Fatalf("cancelled invitation not dismissed once context arrived: %+v %v", got, e)
	}
	for guest.retryProof(tctx(t)) {
	}
	if got, e := guest.AcceptParticipation(tctx(t), first.PID); e == nil {
		t.Fatalf("cancelled invitation accepted: %+v", got)
	}
	if got, e := guest.Participation(second.PID); e != nil || got.State != PartInvited || got.Held != 0 {
		t.Fatalf("live invitation: %+v %v", got, e)
	}
	if _, err = guest.AcceptParticipation(tctx(t), second.PID); err != nil {
		t.Fatal(err)
	}
	runAgent(t, inviter)
	runAgent(t, guest)
	eventually(t, "inviter sees guest active", func() bool { p, e := inviter.Participation(second.PID); return e == nil && p.HumanActive() })
	if _, err = inviter.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest shows inviter turn", func() bool {
		return slices.ContainsFunc(groupTurns(t, guest, conv), func(m ConvMessage) bool { return m.Body == "hello" })
	})
	assertNothingHeld(t, guest)
}

// Fail closed: a dismissal that does not follow the invitation exactly is
// not recorded early, whether it comes after the invitation or before it;
// it waits for context as before.
func TestGroupGuestCancellationBeforeContextWrongPrev(t *testing.T) {
	for _, beforeInvite := range []bool{false, true} {
		t.Run(map[bool]string{false: "after-invite", true: "before-invite"}[beforeInvite], func(t *testing.T) { groupGuestWrongPrev(t, beforeInvite) })
	}
}

func groupGuestWrongPrev(t *testing.T, beforeInvite bool) {
	w, conv := offlineGroupGuestFixture(t)
	inviter, guest := w.alice, w.bob
	first, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	me, _, err := inviter.store.selfPerson(inviter.Address)
	if err != nil {
		t.Fatal(err)
	}
	bogus := protocol.ParticipationEvent{V: 1, Conv: conv, PID: first.PID, Type: protocol.EventDismiss, Prev: strings.Repeat("ab", 32), TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: inviter.Address, Fingerprint: me.info.Fingerprint}}
	if !beforeInvite {
		deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubEvent)
	}
	bogus.Sign(inviter.id.Sign)
	body, _ := json.Marshal(bogus)
	root, _, _, err := inviter.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	rootRaw, _ := json.Marshal(root)
	key, err := guest.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: inviter.Address, To: guest.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: conv, Root: rootRaw, PID: first.PID, Sub: envelope.SubEvent, Body: string(body)}
	env, err := envelope.Seal(in, inviter.id.Sign, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = guest.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if beforeInvite {
		deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubEvent)
	}
	if heldReason(t, guest, env.ID) != reasonProof {
		t.Fatal("dismissal with another prev was not left waiting for context")
	}
	evs, err := guest.store.participationEvents(conv, first.PID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(evs, func(ev protocol.ParticipationEvent) bool { return ev.Type == protocol.EventDismiss }) {
		t.Fatal("dismissal with another prev recorded without context")
	}
}

// Review of 1cc6839e (MEL-841): the dismissal can reach the invitee before
// its invitation. It is then held for proof, and the invitation and its
// context could arrive before the next proof retry, leaving the cancelled
// invitation shown and acceptable while its exact cancellation was held.
// No proof retry runs here between arrival and the acceptance. Context comes
// with the cancelled invitation itself, or with a second, live one.
func TestGroupGuestCancellationBeforeInvite(t *testing.T) {
	for _, second := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "second"}[second], func(t *testing.T) {
			w, conv := offlineGroupGuestFixture(t)
			inviter, guest := w.alice, w.bob
			first, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "first")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = inviter.DismissParticipation(tctx(t), first.PID); err != nil {
				t.Fatal(err)
			}
			carrier := first.PID
			if second {
				live, e := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "second")
				if e != nil {
					t.Fatal(e)
				}
				carrier = live.PID
			}
			events := pidCopies(t, inviter, guest, first.PID, envelope.SubEvent)
			slices.Reverse(events) // the dismissal first, the invitation last
			deliverCopies(t, inviter, guest, events[:1])
			if heldReason(t, guest, events[0].ID) != reasonProof {
				t.Fatal("dismissal before its invitation was not held for proof")
			}
			deliverCopies(t, inviter, guest, events[1:])
			if second {
				deliverPIDCopies(t, inviter, guest, carrier, envelope.SubEvent)
			}
			deliverPIDCopies(t, inviter, guest, carrier, envelope.SubGroupProof)
			deliverPIDCopies(t, inviter, guest, carrier, envelope.SubGroupContext)
			if _, e := guest.GroupContext(conv); e != nil {
				t.Fatalf("context: %v", e)
			}
			if got, e := guest.Participation(first.PID); e != nil || got.State != PartDismissed {
				t.Fatalf("cancelled invitation shown once context arrived: %+v %v", got, e)
			}
			if got, e := guest.AcceptParticipation(tctx(t), first.PID); e == nil {
				t.Fatalf("cancelled invitation accepted: %+v", got)
			}
			if !second {
				return
			}
			for guest.retryProof(tctx(t)) {
			}
			if got, e := guest.Participation(carrier); e != nil || got.State != PartInvited || got.Held != 0 {
				t.Fatalf("live invitation: %+v %v", got, e)
			}
			if _, err = guest.AcceptParticipation(tctx(t), carrier); err != nil {
				t.Fatal(err)
			}
			runAgent(t, inviter)
			runAgent(t, guest)
			eventually(t, "inviter sees guest active", func() bool { p, e := inviter.Participation(carrier); return e == nil && p.HumanActive() })
			assertNothingHeld(t, guest)
		})
	}
}

// Fail closed, and never wedged: a dismissal that follows the invitation
// but names a roster step outside the inviter's chain or another group
// admission is not recorded before context (it would then never count yet
// hold the invitation as waiting for evidence); the invitation stays
// decidable once context arrives.
func TestGroupGuestCancellationBeforeContextUnverifiedAuthor(t *testing.T) {
	for name, mutate := range map[string]func(*protocol.EventAuthor){
		"unchained-roster": func(a *protocol.EventAuthor) { a.Roster = strings.Repeat("cd", 32) },
		"wrong-admission":  func(a *protocol.EventAuthor) { a.GroupAdmission = strings.Repeat("cd", 32) },
		"no-admission":     func(a *protocol.EventAuthor) { a.GroupAdmission = "" },
	} {
		t.Run(name, func(t *testing.T) {
			w, conv := offlineGroupGuestFixture(t)
			inviter, guest := w.alice, w.bob
			first, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "first")
			if err != nil {
				t.Fatal(err)
			}
			evs, err := inviter.store.participationEvents(conv, first.PID)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(evs, func(ev protocol.ParticipationEvent) bool { return ev.Type == protocol.EventInvite })
			if i < 0 {
				t.Fatal("no invitation")
			}
			deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubEvent)
			bad := protocol.ParticipationEvent{V: 1, Conv: conv, PID: first.PID, Type: protocol.EventDismiss, Prev: evs[i].Hash(), TS: time.Now().Unix(), Author: evs[i].Author}
			mutate(&bad.Author)
			bad.Sign(inviter.id.Sign)
			env := sealGuestEvent(t, inviter, guest, conv, bad)
			if err = guest.accept(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			if got, e := guest.store.participationEvents(conv, first.PID); e != nil || slices.ContainsFunc(got, func(ev protocol.ParticipationEvent) bool { return ev.Type == protocol.EventDismiss }) {
				t.Fatalf("unverified dismissal recorded without context: %v", e)
			}
			deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubGroupProof)
			deliverPIDCopies(t, inviter, guest, first.PID, envelope.SubGroupContext)
			for guest.retryProof(tctx(t)) {
			}
			if got, e := guest.Participation(first.PID); e != nil || got.State != PartInvited || got.Held != 0 {
				t.Fatalf("valid invitation wedged: %+v %v", got, e)
			}
			if _, err = guest.AcceptParticipation(tctx(t), first.PID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// sealGuestEvent seals ev as from's copy of it to to in conv.
func sealGuestEvent(t *testing.T, from, to *Agent, conv string, ev protocol.ParticipationEvent) envelope.Envelope {
	t.Helper()
	body, _ := json.Marshal(ev)
	root, _, _, err := from.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	rootRaw, _ := json.Marshal(root)
	key, err := to.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: from.Address, To: to.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: conv, Root: rootRaw, PID: ev.PID, Sub: envelope.SubEvent, Body: string(body)}
	env, err := envelope.Seal(in, from.id.Sign, key)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// convCopies returns, in order, the sender's stored copies in conv addressed
// to receiver whose sub is sub, without a PID.
func convCopies(t *testing.T, sender, receiver *Agent, conv, sub string) []envelope.Envelope {
	t.Helper()
	rows, err := sender.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND conv=? AND coalesce(sub,'')=? AND coalesce(pid,'')='' ORDER BY rowid`, receiver.Address, conv, sub)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &env)
		}
		if err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	return envs
}

// CI (macOS, b4d50ffe) in TestGroupGuestAndMemberInvitationFirstCancelled:
// a guest, holding the group's context as a visitor, accepts a membership;
// the inviter's first turn to the new member overtakes the new context
// (carriers wait for their upload; a turn does not). Checked against the
// visitor context, the turn was held as invalid for good and never shown.
// It waits for context instead, and shows once that arrives.
func TestGroupNewMemberTurnBeforeItsContext(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	stopB := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	p, err := w.alice.CreateGroup(tctx(t), "Solo")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		roomReader(t, a)
	}
	conv := p.Root.ID()
	guestP, err := w.alice.InviteHumanInScope(WithQueuedSend(tctx(t), ""), conv, w.bob.Address, nil, "guest", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest invitation", func() bool { p, e := w.bob.Participation(guestP.PID); return e == nil && p.State == PartInvited })
	eventually(t, "visitor context", func() bool { _, e := w.bob.GroupContext(conv); return e == nil })
	member := groupLifecycleInvite(t, w, p, nil)
	awaitGroupInvitation(t, w.bob, member.ID, "pending")
	stopB()
	if err = w.bob.DecideGroupInvitation(tctx(t), member.ID, true); err != nil {
		t.Fatal(err)
	}
	_ = w.bob.FlushOutbox(tctx(t))
	eventually(t, "membership published", func() bool {
		r, e := groupInvitationIn(w.alice.store.db, member.ID, "out")
		return e == nil && r.State == "published"
	})
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	turns := convCopies(t, w.alice, w.bob, conv, "")
	if len(turns) == 0 {
		t.Fatal("no turn copy for the new member")
	}
	deliverCopies(t, w.alice, w.bob, turns[len(turns)-1:])
	for _, sub := range []string{envelope.SubGroupProof, envelope.SubGroupContext} {
		deliverCopies(t, w.alice, w.bob, convCopies(t, w.alice, w.bob, conv, sub))
	}
	for w.bob.retryProof(tctx(t)) {
	}
	if !slices.Contains(convBodies(t, w.bob, conv), "in:hello") {
		t.Fatalf("new member's first turn not shown once its context arrived: %v (held %q)", convBodies(t, w.bob, conv), heldReason(t, w.bob, turns[len(turns)-1].ID))
	}
}

// The proof wait is the reader's own: a turn whose sender the reader's
// current context does not list stays refused, even once that person is
// admitted again later (a turn written while removed never counts against a
// newer context).
func TestGroupRemovedSenderTurnStaysRefused(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	stopA := runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	stopB := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	conv := p.Root.ID()
	first := groupLifecycleInvite(t, w, p, nil)
	awaitGroupInvitation(t, w.bob, first.ID, "pending")
	if err := w.bob.DecideGroupInvitation(tctx(t), first.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member context", func() bool {
		got, e := w.bob.GroupContext(conv)
		return e == nil && len(got.State.Members) == 2
	})
	stopA()
	stopB()
	bob, _, err := w.bob.Person()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.RemoveGroupMember(tctx(t), conv, bob.Person); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "written while removed"}); err != nil {
		t.Fatal(err)
	}
	stale := convCopies(t, w.bob, w.alice, conv, "")
	if len(stale) == 0 {
		t.Fatal("no copy of the removed member's turn")
	}
	deliverCopies(t, w.bob, w.alice, stale[len(stale)-1:])
	id := stale[len(stale)-1].ID
	if got := heldReason(t, w.alice, id); got != reasonInvalid {
		t.Fatalf("turn of a sender the context does not list held as %q", got)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	again, err := w.alice.InviteGroup(tctx(t), conv, bob.Person, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitGroupInvitation(t, w.bob, again.ID, "pending")
	if err = w.bob.DecideGroupInvitation(tctx(t), again.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member again at the inviter", func() bool {
		got, e := w.alice.GroupContext(conv)
		return e == nil && len(got.State.Members) == 2
	})
	for w.alice.retryProof(tctx(t)) {
	}
	if got := heldReason(t, w.alice, id); got != reasonInvalid {
		t.Fatalf("turn written while removed held as %q once readmitted", got)
	}
	if slices.Contains(convBodies(t, w.alice, conv), "in:written while removed") {
		t.Fatal("turn written while removed shown once readmitted")
	}
}

// agy re-check (A): a turn written while the reader was removed waits for
// no newer context; re-invited with no history, the reader still never
// sees it.
func TestGroupRemovedReaderTurnStaysRefused(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	stopB := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	conv := p.Root.ID()
	first := groupLifecycleInvite(t, w, p, nil)
	awaitGroupInvitation(t, w.bob, first.ID, "pending")
	if err := w.bob.DecideGroupInvitation(tctx(t), first.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member context", func() bool { got, e := w.bob.GroupContext(conv); return e == nil && len(got.State.Members) == 2 })
	bob, _, err := w.bob.Person()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.RemoveGroupMember(tctx(t), conv, bob.Person); err != nil {
		t.Fatal(err)
	}
	eventually(t, "removal at the reader", func() bool { got, e := w.bob.GroupContext(conv); return e == nil && len(got.State.Members) == 1 })
	stopB()
	root, _, _, err := w.alice.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	rootRaw, _ := json.Marshal(root)
	key, err := w.bob.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: conv, Root: rootRaw, Body: "written while removed", Origin: envelope.OriginUI}
	turn, err := envelope.Seal(in, w.alice.id.Sign, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.accept(tctx(t), turn); err != nil {
		t.Fatal(err)
	}
	if got := heldReason(t, w.bob, turn.ID); got != reasonInvalid {
		t.Fatalf("turn written while the reader was removed held as %q", got)
	}
	runAgent(t, w.bob)
	again, err := w.alice.InviteGroup(tctx(t), conv, bob.Person, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitGroupInvitation(t, w.bob, again.ID, "pending")
	if err = w.bob.DecideGroupInvitation(tctx(t), again.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member again at the reader", func() bool { got, e := w.bob.GroupContext(conv); return e == nil && len(got.State.Members) == 2 })
	for w.bob.retryProof(tctx(t)) {
	}
	if got := heldReason(t, w.bob, turn.ID); got != reasonInvalid {
		t.Fatalf("turn written while the reader was removed held as %q once re-invited", got)
	}
	if slices.Contains(convBodies(t, w.bob, conv), "in:written while removed") {
		t.Fatal("turn written while the reader was removed shown once re-invited")
	}
}

// agy re-check (B): another member's cancellation, not the inviter
// device's, came before the invitation and waits held; the decision counts
// it first (through the usual admission), before any proof retry.
func TestGroupGuestCancellationByAnotherMemberBeforeDecision(t *testing.T) {
	w := newWorld(t, "")
	carol := proofReader(t, w, "carol")
	persons(t, w.alice, w.bob)
	stops := []func(){runAgent(t, w.alice)}
	publishGroupFixtureCaps(t, w.alice, true)
	stops = append(stops, runAgent(t, w.bob))
	publishGroupFixtureCaps(t, w.bob, true)
	stops = append(stops, runAgent(t, carol))
	publishGroupFixtureCaps(t, carol, true)
	p, err := w.alice.CreateGroup(tctx(t), "Pair")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		roomReader(t, a)
	}
	conv := p.Root.ID()
	cp, _, err := carol.Person()
	if err != nil {
		t.Fatal(err)
	}
	inv, err := w.alice.InviteGroup(tctx(t), conv, cp.Person, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitGroupInvitation(t, carol, inv.ID, "pending")
	if err = carol.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member context", func() bool { got, e := carol.GroupContext(conv); return e == nil && len(got.State.Members) == 2 })
	for _, stop := range stops {
		stop()
	}
	first, err := w.alice.InviteHuman(tctx(t), conv, w.bob.Address, nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	deliverPIDCopies(t, w.alice, carol, first.PID, envelope.SubEvent)
	if _, err = carol.DismissParticipation(tctx(t), first.PID); err != nil {
		t.Fatal(err)
	}
	cancels := pidCopies(t, carol, w.bob, first.PID, envelope.SubEvent)
	if len(cancels) == 0 {
		t.Fatal("no copy of the other member's cancellation")
	}
	deliverCopies(t, carol, w.bob, cancels)
	for _, sub := range []string{envelope.SubEvent, envelope.SubGroupProof, envelope.SubGroupContext} {
		deliverPIDCopies(t, w.alice, w.bob, first.PID, sub)
	}
	if got, e := w.bob.Participation(first.PID); e != nil || got.State != PartInvited {
		t.Fatalf("fixture: the other member's cancellation no longer waits held: %+v %v", got, e)
	}
	if got, e := w.bob.AcceptParticipation(tctx(t), first.PID); e == nil {
		t.Fatalf("invitation another member cancelled accepted: %+v", got)
	}
	if got, e := w.bob.Participation(first.PID); e != nil || got.State != PartDismissed {
		t.Fatalf("the other member's cancellation not counted at the decision: %+v %v", got, e)
	}
}

// The decision's own guards, reached directly: with the invitation and its
// context held, (a) the exact cancellation still waiting held counts before
// the decision, which is refused; (b) a stored event that does not count
// (as the early path once stored) leaves the invitation waiting for evidence,
// and neither accepting nor declining goes through.
func TestGroupGuestDecisionGuards(t *testing.T) {
	setup := func(t *testing.T) (*world, string, ParticipationInfo, []envelope.Envelope) {
		w, conv := offlineGroupGuestFixture(t)
		first, err := w.alice.InviteHuman(tctx(t), conv, w.bob.Address, nil, "first")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.alice.DismissParticipation(tctx(t), first.PID); err != nil {
			t.Fatal(err)
		}
		events := pidCopies(t, w.alice, w.bob, first.PID, envelope.SubEvent) // invite, scope, dismissal
		deliverCopies(t, w.alice, w.bob, events[:len(events)-1])
		deliverPIDCopies(t, w.alice, w.bob, first.PID, envelope.SubGroupProof)
		deliverPIDCopies(t, w.alice, w.bob, first.PID, envelope.SubGroupContext)
		return w, conv, first, events
	}
	t.Run("held-cancellation", func(t *testing.T) {
		w, _, first, events := setup(t)
		dismissal := events[len(events)-1]
		raw, _ := json.Marshal(dismissal)
		if err := w.bob.store.quarantine(dismissal.ID, dismissal.From, reasonProof, raw); err != nil {
			t.Fatal(err)
		}
		if got, e := w.bob.Participation(first.PID); e != nil || got.State != PartInvited {
			t.Fatalf("before the decision: %+v %v", got, e)
		}
		if got, e := w.bob.AcceptParticipation(tctx(t), first.PID); e == nil {
			t.Fatalf("cancelled invitation accepted: %+v", got)
		}
		if got, e := w.bob.Participation(first.PID); e != nil || got.State != PartDismissed {
			t.Fatalf("held cancellation not counted at the decision: %+v %v", got, e)
		}
	})
	t.Run("held-evidence", func(t *testing.T) {
		w, conv, first, _ := setup(t)
		evs, err := w.bob.store.participationEvents(conv, first.PID)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(evs, func(ev protocol.ParticipationEvent) bool { return ev.Type == protocol.EventInvite })
		bad := protocol.ParticipationEvent{V: 1, Conv: conv, PID: first.PID, Type: protocol.EventDismiss, Prev: evs[i].Hash(), TS: time.Now().Unix(), Author: evs[i].Author}
		bad.Author.Roster = strings.Repeat("cd", 32)
		bad.Sign(w.alice.id.Sign)
		body, _ := json.Marshal(bad)
		tx, err := w.bob.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = insertParticipationEvent(tx, bad, body); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got, e := w.bob.Participation(first.PID); e != nil || got.State != PartInvited || got.Held != 1 {
			t.Fatalf("not waiting for evidence: %+v %v", got, e)
		}
		if got, e := w.bob.AcceptParticipation(tctx(t), first.PID); e == nil {
			t.Fatalf("accepted while evidence is held: %+v", got)
		}
		if got, e := w.bob.DeclineParticipation(tctx(t), first.PID); e == nil {
			t.Fatalf("declined while evidence is held: %+v", got)
		}
	})
}

// Review of f9c0d17a: the sender is checked before this device's own
// absence may wait. A visitor's verified context lists neither this device
// nor a third person, whose turn arrives against it; both then become
// members. The turn is refused for good: a later context never authorizes a
// turn whose sender the context it arrived against did not list.
func TestGroupUnlistedSenderTurnBeforeReaderContext(t *testing.T) {
	w := newWorld(t, "")
	carol := proofReader(t, w, "carol")
	persons(t, w.alice, w.bob)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	stopB := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	runAgent(t, carol)
	publishGroupFixtureCaps(t, carol, true)
	p, err := w.alice.CreateGroup(tctx(t), "Solo")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		roomReader(t, a)
	}
	conv := p.Root.ID()
	guestP, err := w.alice.InviteHumanInScope(WithQueuedSend(tctx(t), ""), conv, w.bob.Address, nil, "guest", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest invitation", func() bool { p, e := w.bob.Participation(guestP.PID); return e == nil && p.State == PartInvited })
	eventually(t, "visitor context", func() bool { _, e := w.bob.GroupContext(conv); return e == nil })
	stopB() // its verified context lists the inviter alone
	root, _, _, err := w.alice.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	rootRaw, _ := json.Marshal(root)
	key, err := w.bob.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: carol.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: conv, Root: rootRaw, Body: "from an unlisted sender", Origin: envelope.OriginUI}
	turn, err := envelope.Seal(in, carol.id.Sign, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.accept(tctx(t), turn); err != nil {
		t.Fatal(err)
	}
	if got := heldReason(t, w.bob, turn.ID); got != reasonInvalid {
		t.Fatalf("turn of a sender the reader's context does not list held as %q", got)
	}
	runAgent(t, w.bob)
	for _, a := range []*Agent{carol, w.bob} {
		person, _, e := a.Person()
		if e != nil {
			t.Fatal(e)
		}
		inv, e := w.alice.InviteGroup(tctx(t), conv, person.Person, nil)
		if e != nil {
			t.Fatal(e)
		}
		awaitGroupInvitation(t, a, inv.ID, "pending")
		if e = a.DecideGroupInvitation(tctx(t), inv.ID, true); e != nil {
			t.Fatal(e)
		}
		eventually(t, "published", func() bool {
			r, e := groupInvitationIn(w.alice.store.db, inv.ID, "out")
			return e == nil && r.State == "published"
		})
	}
	eventually(t, "reader context lists both", func() bool { got, e := w.bob.GroupContext(conv); return e == nil && len(got.State.Members) == 3 })
	for w.bob.retryProof(tctx(t)) {
	}
	if got := heldReason(t, w.bob, turn.ID); got != reasonInvalid {
		t.Fatalf("turn of an unlisted sender held as %q once both joined", got)
	}
	if slices.Contains(convBodies(t, w.bob, conv), "in:from an unlisted sender") {
		t.Fatal("turn of an unlisted sender shown once both joined")
	}
}

// Codex review (P2): the inviter's cancellation commits after the guest's
// checks and before its decision commits. The decision's transaction sees
// it: nothing is decided, stored or sent, and the invitation reads
// dismissed.
func TestGroupGuestCancellationDuringDecision(t *testing.T) {
	for _, typ := range []string{protocol.EventAccept, protocol.EventDecline} {
		t.Run(typ, func(t *testing.T) {
			w, conv := offlineGroupGuestFixture(t)
			first, err := w.alice.InviteHuman(tctx(t), conv, w.bob.Address, nil, "first")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.alice.DismissParticipation(tctx(t), first.PID); err != nil {
				t.Fatal(err)
			}
			events := pidCopies(t, w.alice, w.bob, first.PID, envelope.SubEvent) // invite, scope, dismissal
			deliverCopies(t, w.alice, w.bob, events[:len(events)-1])
			deliverPIDCopies(t, w.alice, w.bob, first.PID, envelope.SubGroupProof)
			deliverPIDCopies(t, w.alice, w.bob, first.PID, envelope.SubGroupContext)
			if got, e := w.bob.Participation(first.PID); e != nil || got.State != PartInvited || got.Held != 0 {
				t.Fatalf("before the decision: %+v %v", got, e)
			}
			landed := false
			decidePreSign = func(pid string) {
				if pid == first.PID && !landed {
					landed = true
					deliverCopies(t, w.alice, w.bob, events[len(events)-1:])
				}
			}
			t.Cleanup(func() { decidePreSign = nil })
			decide := w.bob.AcceptParticipation
			if typ == protocol.EventDecline {
				decide = w.bob.DeclineParticipation
			}
			if got, e := decide(tctx(t), first.PID); e == nil {
				t.Fatalf("decided an invitation cancelled meanwhile: %+v", got)
			}
			if !landed {
				t.Fatal("the cancellation did not land between the checks and the commit")
			}
			if got, e := w.bob.Participation(first.PID); e != nil || got.State != PartDismissed {
				t.Fatalf("after the refused decision: %+v %v", got, e)
			}
			evs, err := w.bob.store.participationEvents(conv, first.PID)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(evs, func(ev protocol.ParticipationEvent) bool { return ev.Type == typ }) {
				t.Fatal("refused decision stored")
			}
			var n int
			if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE pid=? AND sub=?`, first.PID, envelope.SubEvent).Scan(&n); err != nil || n != 0 {
				t.Fatalf("refused decision queued: %d %v", n, err)
			}
		})
	}
}
