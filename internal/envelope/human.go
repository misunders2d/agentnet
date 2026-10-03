package envelope

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const MaxHumanAudience = 16
const MaxHumanProof = 2 * MaxHumanAudience

// HumanScope commits one exact invitation and acceptance. It is not membership.
type HumanScope struct {
	PID      string `json:"pid"`
	Invite   string `json:"invite"`
	Decision string `json:"decision"`
}

// HumanTurn has the same bytes in every logical copy. Fan stays the original
// two members. Proof is each scope's public projection (its inviter's signed
// scope record) and the host's acceptance: never the invitation itself (its
// grant, task keys or note), never unselected earlier turns. It rides on an
// ordinary turn, or on an addressed request to an assistant participation
// (PID) and that assistant's output, so the captured audience sees them.
type HumanTurn struct {
	AuthorPID string                        `json:"author_pid,omitempty"`
	Audience  []HumanScope                  `json:"audience"`
	Proof     []protocol.ParticipationEvent `json:"proof"`
}

func (h HumanTurn) Validate(conv string) error {
	if h.AuthorPID != "" && !protocol.ValidID(h.AuthorPID) || len(h.Audience) == 0 || len(h.Audience) > MaxHumanAudience || len(h.Proof) > MaxHumanProof {
		return errors.New("human: invalid author or audience bound")
	}
	seen := map[string]HumanScope{}
	author := h.AuthorPID == ""
	for _, s := range h.Audience {
		if !protocol.ValidID(s.PID) || !protocol.ValidHash(s.Invite) || !protocol.ValidHash(s.Decision) {
			return errors.New("human: invalid scope reference")
		}
		if _, ok := seen[s.PID]; ok {
			return errors.New("human: duplicate audience")
		}
		seen[s.PID] = s
		author = author || s.PID == h.AuthorPID
	}
	if !author {
		return errors.New("human: author outside captured audience")
	}
	events := map[string]bool{}
	for _, e := range h.Proof {
		s, ok := seen[e.PID]
		raw, _ := json.Marshal(e)
		if !ok || len(e.Sig) != ed25519.SignatureSize || len(raw) > protocol.MaxParticipationEvent || e.Conv != conv || e.Validate() != nil || (e.Type != protocol.EventScope && e.Type != protocol.EventAccept) || e.Type == protocol.EventScope && e.Role != protocol.RoleHuman {
			return errors.New("human: proof outside audience")
		}
		hash := e.Hash()
		if events[hash] || e.Prev != s.Invite || e.Type == protocol.EventAccept && hash != s.Decision {
			return errors.New("human: duplicate or unrelated proof")
		}
		events[hash] = true
		if e.Type == protocol.EventScope {
			events["scope/"+s.PID] = true
		}
	}
	for _, s := range h.Audience {
		if !events["scope/"+s.PID] || !events[s.Decision] {
			return errors.New("human: missing scope or acceptance proof")
		}
	}
	return nil
}
func validateHumanInner(in Inner) error {
	if in.Human == nil {
		return nil
	}
	if in.V == Version3 {
		// An assistant's reaction to an addressed request, to that request's
		// captured audience: its host is the author (as of its output), and a
		// control carries no root (the DM is the reader's pinned one). No
		// other control carries an audience.
		if !AssistantReaction(in) || in.Conv == "" || in.PID == "" || in.Human.AuthorPID != "" || len(in.Root) != 0 || in.Kind != KindMessage {
			return errors.New("human: on a control, only an assistant's reaction to its captured audience")
		}
		return in.Human.Validate(in.Conv)
	}
	root, err := protocol.ParseConvRoot(in.Root)
	if err != nil || root.ID() != in.Conv || root.Kind != protocol.ConvKindDM || len(root.Members) != 2 || in.V != Version2 || in.Sub != "" {
		return errors.New("human: ordinary non-executing DM turn only")
	}
	switch h := in.Human; {
	case in.Kind == KindMessage && in.Status == "" && in.Target == nil && in.AgentID == "" && !AgentOrigin(in.Origin) && in.Emotion == "" && in.PID == h.AuthorPID:
		// an ordinary turn
	case (in.Kind == KindQuestion || in.Kind == KindTask) && in.Target != nil && in.PID != "" && in.PID != h.AuthorPID && in.AgentID == "" && !AgentOrigin(in.Origin) && in.Status == "" && in.ReceiverRoute == nil:
		// a request addressed to the assistant participation PID
	case (in.Kind == KindAnswer || in.Kind == KindResult || in.Kind == KindMessage && in.Status == StatusProgress) && in.Target == nil && in.PID != "" && h.AuthorPID == "" && in.ReceiverRoute == nil:
		// that assistant's output, to the captured audience
	default:
		return errors.New("human: ordinary turn, addressed request or assistant output only")
	}
	return in.Human.Validate(in.Conv)
}
