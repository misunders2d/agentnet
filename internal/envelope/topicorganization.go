package envelope

import (
	"errors"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// TopicMove names one reviewed visible version and its prior display topic.
// The message's signed topic, reply, execution and audience are never changed.
type TopicMove struct {
	LID    string `json:"lid"`
	Author string `json:"author"`
	Hash   string `json:"hash"`
	Topic  string `json:"topic,omitempty"`
}

const MaxTopicMoves = 200

func TopicOrganization(e *TopicEvent) bool {
	return e != nil && (e.Action == "move" || e.Action == "merge")
}

func checkTopicOrganization(in Inner) error {
	e := in.TopicEvent
	if in.PID != "" || in.Human != nil || in.AgentID != "" || in.Ref != nil || in.ReceiverRoute != nil || in.Followup != nil || in.TopicDone || len(e.Seen) != 0 || len(e.Moves) == 0 || len(e.Moves) > MaxTopicMoves {
		return errors.New("topic organization is a bounded original-member display action")
	}
	if e.Action == "merge" && (!protocol.ValidID(e.Merge) || e.Merge == in.Topic) || e.Action == "move" && e.Merge != "" {
		return errors.New("invalid topic merge source")
	}
	seen := map[string]bool{}
	for _, m := range e.Moves {
		key := m.Author + ":" + m.LID
		if !protocol.ValidID(m.LID) || !protocol.ValidFingerprint(m.Author) || !protocol.ValidHash(m.Hash) || m.Topic != "" && !protocol.ValidID(m.Topic) || m.Topic == in.Topic || seen[key] || e.Merge != "" && m.Topic != e.Merge {
			return errors.New("invalid or duplicate selected topic message")
		}
		seen[key] = true
	}
	return nil
}
