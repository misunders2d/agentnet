package client

import "testing"

func TestGroupAgentRejoinStaleInviteUsesCurrentPID(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	old := p6Member(t, w.alice, w.bob, conv)
	if _, err := w.alice.DismissParticipation(tctx(t), old.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "host sees dismissal", func() bool { return stateAt(t, w.bob, old.PID).State == PartDismissed })
	next, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if next.PID == old.PID || next.State != PartInvited {
		t.Fatalf("not a fresh pending participation: %+v", next)
	}
	repeat, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil || repeat.PID != next.PID {
		t.Fatalf("pending stale invite duplicated: %+v %v", repeat, err)
	}
	eventually(t, "host sees rejoin", func() bool { return stateAt(t, w.bob, next.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), next.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "sender sees active rejoin", func() bool { return stateAt(t, w.alice, next.PID).Claimable() })
	repeat, err = w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil || repeat.PID != next.PID {
		t.Fatalf("active stale invite duplicated: %+v %v", repeat, err)
	}
	before := stateAt(t, w.alice, old.PID)
	if before.State != PartDismissed || len(before.Grant) != len(old.Grant) || len(before.TaskKeys) != len(old.TaskKeys) {
		t.Fatalf("old history/grants changed: %+v", before)
	}
}
