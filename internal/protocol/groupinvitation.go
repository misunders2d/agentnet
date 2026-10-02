package protocol

import (
	"encoding/json"
	"errors"
)

// GroupInvitation is a proposal, not membership. Original public authority
// records arrive separately through bounded GroupJournalPage carriers. The
// semantic ID is shared by all devices of the invited person; delivery keys
// belong exclusively to the signed envelope's GroupCarrier descriptor.
type GroupInvitation struct {
	V           int               `json:"v"`
	Root        ConvRoot          `json:"root"`
	State       GroupState        `json:"state"`
	Withdrawals []GroupWithdrawal `json:"withdrawals"`
	Target      string            `json:"target"`
	Roster      string            `json:"roster"`
	Seq         int64             `json:"seq"`
	Prev        string            `json:"prev"`
	History     []GroupHistoryRef `json:"history"`
}

func (p GroupInvitation) ID() string {
	raw, _ := json.Marshal(p)
	return hashHex(append([]byte("agentnet-group-invitation-v1\n"), raw...))
}

func (p GroupInvitation) Validate() error {
	if p.V != 1 || ValidateGroupRoot(p.Root) != nil || p.State.Validate() != nil || p.State.Conv != p.Root.ID() || p.State.Realm != p.Root.Realm || !ValidID(p.Target) || !ValidHash(p.Roster) || p.Seq != p.State.Seq+1 || p.Prev != p.State.Hash() {
		return errors.New("group: invalid invitation binding")
	}
	// Reuse the exact admission's selected-history and seq/prev constraints.
	a := GroupAdmission{Conv: p.State.Conv, Realm: p.Root.Realm, Person: p.Target, Roster: p.Roster, Seq: p.Seq, Prev: p.Prev, History: p.History, By: p.Root.Creator.Fingerprint}
	if err := a.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(p)
	if len(raw) > MaxGroupState {
		return errors.New("group: invitation exceeds current-context bound")
	}
	return nil
}

// GroupConsent carries only an explicit local decision about one exact
// proposal. A signed envelope authenticates the sender; accept additionally
// requires the original GroupAdmission signature. Neither is a task.
type GroupConsent struct {
	V          int             `json:"v"`
	Invitation string          `json:"invitation"`
	Decision   string          `json:"decision"`
	Admission  *GroupAdmission `json:"admission,omitempty"`
}

func (c GroupConsent) Validate() error {
	if c.V != 1 || !ValidHash(c.Invitation) {
		return errors.New("group: invalid consent proposal")
	}
	if c.Decision == "declined" && c.Admission == nil {
		return nil
	}
	if c.Decision == "accepted" && c.Admission != nil {
		return c.Admission.Validate()
	}
	return errors.New("group: explicit accept or decline required")
}
