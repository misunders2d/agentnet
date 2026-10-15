package envelope

import (
	"errors"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// FollowupRef retains exact request provenance. A positive Attempt binds a
// correction to that still-running host attempt; it can never launch later.
type FollowupRef struct {
	Ref
	Attempt int64 `json:"attempt,omitempty"`
}

// CheckFollowup distinguishes an explicit correction/continuation from an
// ordinary reply or quotation. It is a new request under normal admission,
// never permission to restart the referenced request or to change its target.
func CheckFollowup(in Inner) error {
	if in.Followup == nil {
		return nil
	}
	f := in.Followup
	if f.Attempt < 0 || f.Attempt > 9007199254740991 || !protocol.ValidID(f.ID) || !protocol.ValidFingerprint(f.Fingerprint) || f.ID == in.ID || f.ID == in.LID || in.ReplyTo == "" ||
		(in.V != Version && in.V != Version2) || (in.Kind != KindQuestion && in.Kind != KindTask) || in.Sub != "" || in.Status != "" || in.AgentID != "" || AgentOrigin(in.Origin) || in.TopicEvent != nil || in.TopicDone {
		return errors.New("follow-up must bind another exact request on a human question or task")
	}
	return nil
}
