package client

import (
	"slices"
	"testing"

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
