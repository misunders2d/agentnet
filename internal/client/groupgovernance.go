package client

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/misunders2d/agentnet/internal/protocol"
)

var ErrGroupLastAdmin = errors.New("group: last administrator cannot demote, remove or leave; promote a successor first")

// Each governance action signs one exact current CAS transition. Admissions
// remain byte-for-byte consented; a conflicting CAS is never silently rebuilt.
func (a *Agent) RenameGroup(ctx context.Context, conv, title string) (GroupContext, error) {
	return a.governGroup(ctx, conv, "rename", strings.TrimSpace(title))
}
func (a *Agent) PromoteGroupMember(ctx context.Context, conv, person string) (GroupContext, error) {
	return a.governGroup(ctx, conv, "promote", person)
}
func (a *Agent) DemoteGroupMember(ctx context.Context, conv, person string) (GroupContext, error) {
	return a.governGroup(ctx, conv, "demote", person)
}
func (a *Agent) RemoveGroupMember(ctx context.Context, conv, person string) (GroupContext, error) {
	return a.governGroup(ctx, conv, "remove", person)
}

func (a *Agent) governGroup(ctx context.Context, conv, action, target string) (GroupContext, error) {
	defer notifyDaemon(a.home)
	packet, err := a.GroupContext(conv)
	if err != nil {
		return packet, err
	}
	for _, m := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, m.Person, false); err != nil {
			return packet, err
		}
	}
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return packet, err
	}
	withdrawals, pins, err := a.groupPacketWithdrawals(ctx, packet, resolve)
	if err == nil {
		err = a.verifyGroupCurrentPacket(packet, resolve, withdrawals, pins)
	}
	if err != nil {
		return packet, err
	}
	if err = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return packet, err
	}
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return packet, err
	}
	own, member := packet.State.Member(self.roster.Person)
	if !ok || !member || !own.Admin {
		return packet, errors.New("group: current person administrator required")
	}
	next := packet
	next.Proof = nil
	next.Withdrawals = currentGroupWithdrawals(packet.State, withdrawals)
	next.State.Members = slices.Clone(packet.State.EffectiveMembers(withdrawals))
	if action == "rename" {
		if target == packet.State.Title {
			return packet, nil
		}
		next.State.Title = target
	} else {
		if !protocol.ValidID(target) {
			return packet, errors.New("group: target must be exact current person ID")
		}
		index := -1
		for i, m := range next.State.Members {
			if m.Person == target {
				index = i
				break
			}
		}
		if index < 0 {
			return packet, errors.New("group: target is not a current effective member")
		}
		m := next.State.Members[index]
		if (action == "demote" || action == "remove") && m.Admin && len(packet.State.Admins()) == 1 {
			return packet, ErrGroupLastAdmin
		}
		switch action {
		case "promote":
			if m.Admin {
				return packet, nil
			}
			next.State.Members[index].Admin = true
		case "demote":
			if !m.Admin {
				return packet, nil
			}
			next.State.Members[index].Admin = false
		case "remove":
			next.State.Members = append(next.State.Members[:index], next.State.Members[index+1:]...)
		default:
			return packet, errors.New("group: unknown governance action")
		}
	}
	next.Withdrawals = currentGroupWithdrawals(next.State, withdrawals)
	next.State.Seq = packet.State.Seq + 1
	next.State.Prev = packet.State.Hash()
	next, err = a.SignGroupState(ctx, next)
	if err != nil {
		return packet, err
	}
	commit, err := a.BuildGroupCommit(ctx, next)
	if err == nil {
		_, err = a.PublishGroup(ctx, commit, next)
	}
	return next, err
}

// Queued proves local departure and a durable encrypted fanout, never receipts.
// Administrator self-departure instead uses the existing explicit CAS removal.
type GroupLeaveResult struct {
	Withdrawal *protocol.GroupWithdrawal `json:"withdrawal,omitempty"`
	Context    *GroupContext             `json:"context,omitempty"`
	Queued     bool                      `json:"queued"`
}

func (a *Agent) LeaveGroup(ctx context.Context, conv string) (GroupLeaveResult, error) {
	var out GroupLeaveResult
	packet, err := a.GroupContext(conv)
	if err != nil {
		return out, err
	}
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, ErrGroupContextPending
	}
	member, ok := packet.State.Member(self.roster.Person)
	if !ok {
		return out, errors.New("group: this person is not a current member")
	}
	if member.Admin {
		if len(packet.State.Admins()) == 1 {
			return out, ErrGroupLastAdmin
		}
		next, err := a.RemoveGroupMember(ctx, conv, self.roster.Person)
		out.Context = &next
		return out, err
	}
	w, err := a.SignGroupWithdrawal(ctx, conv)
	if err == nil {
		out.Withdrawal = &w
		out.Queued = w.By == a.Self().Fingerprint()
	}
	return out, err
}
