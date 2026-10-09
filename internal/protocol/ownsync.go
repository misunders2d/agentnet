package protocol

import "errors"

// CapOwnSyncV2 advertises history witnesses and inert own-human invitation views.
const CapOwnSyncV2 = "own2"

// InvitationSync is a view of the sending device's outgoing intent. It is
// never consent, membership, or authority to publish/cancel an invitation.
type InvitationSync struct {
	V        int             `json:"v"`
	Person   string          `json:"person"`
	Roster   string          `json:"roster"`
	ID       string          `json:"id"`
	Revision int64           `json:"revision"`
	Status   string          `json:"status"`
	Proposal GroupInvitation `json:"proposal"`
}

func ParseInvitationSync(data []byte) (InvitationSync, error) {
	var r InvitationSync
	if len(data) > MaxGroupState+1024 || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || !ValidHash(r.ID) || r.Revision < 1 || r.Revision > 9007199254740991 || r.Proposal.Validate() != nil || r.Proposal.ID() != r.ID {
		return r, errors.New("invitation sync: invalid view")
	}
	switch r.Status {
	case "pending", "accepted", "declined", "cancelled", "published", "stale", "reissue", "history-unavailable":
	default:
		return r, errors.New("invitation sync: invalid status")
	}
	return r, nil
}
