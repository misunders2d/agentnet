package protocol

import (
	"encoding/json"
	"errors"
)

// CapTopicStateSync reads private topic marks (Mark done, Reopen, Archive)
// between current own-human devices. Explicitly advertised: older own2
// readers hold the unknown subtype, so it is never sent to them.
const CapTopicStateSync = "tss1"

// MaxTopicMarks bounds the marks of one carrier.
const MaxTopicMarks = 64

// The marks a person sets on a topic. None clears an earlier mark.
const (
	TopicMarkNone     = ""
	TopicMarkDone     = "done"
	TopicMarkOpen     = "open" // reopened: active even if the agent had finished
	TopicMarkArchived = "archived"
)

// TopicMark is a person's latest private mark on one topic: a display
// preference, never a shared topic event, a receipt or a permission. Count
// is how many of the topic's items the mark covers, so a later one still
// ends it on every device. At is the writer's time of the mark; the newest
// wins, and Writer, Mark and Count break ties, so devices converge on one
// mark. The current own-human carrier authenticates it, not these fields.
type TopicMark struct {
	Scope  string `json:"scope"`
	Topic  string `json:"topic"`
	Mark   string `json:"mark"`
	Count  int64  `json:"count"`
	At     int64  `json:"at"`
	Writer string `json:"writer"`
}

// TopicStateSync carries marks between one person's own human devices.
type TopicStateSync struct {
	V      int         `json:"v"`
	Person string      `json:"person"`
	Roster string      `json:"roster"`
	Marks  []TopicMark `json:"marks"`
}

// Newer reports whether m replaces old, a mark on the same topic.
func (m TopicMark) Newer(old TopicMark) bool {
	switch {
	case m.At != old.At:
		return m.At > old.At
	case m.Writer != old.Writer:
		return m.Writer > old.Writer
	case m.Mark != old.Mark:
		return m.Mark > old.Mark
	}
	return m.Count > old.Count
}

// Valid reports whether m has the shape every reader accepts.
func (m TopicMark) Valid() bool {
	_, _, addressErr := SplitAddress(m.Scope)
	if !ValidHash(m.Scope) && addressErr != nil || !ValidID(m.Topic) || !ValidFingerprint(m.Writer) || m.At < 1 || m.At > MaxTopicTitleRevision {
		return false
	}
	switch m.Mark {
	case TopicMarkNone:
		return m.Count == 0
	case TopicMarkDone, TopicMarkOpen, TopicMarkArchived:
		return m.Count >= 1 && m.Count <= MaxTopicTitleRevision
	}
	return false
}

// ParseTopicStateSync decodes a carrier strictly: every field of every mark
// is present, and no topic appears twice.
func ParseTopicStateSync(data []byte) (TopicStateSync, error) {
	var r TopicStateSync
	if len(data) > 65536 || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Marks) == 0 || len(r.Marks) > MaxTopicMarks {
		return r, errors.New("topic state sync: invalid owner or marks")
	}
	var fields struct {
		Marks []map[string]json.RawMessage `json:"marks"`
	}
	_ = json.Unmarshal(data, &fields)
	seen := map[[2]string]bool{}
	for i, m := range r.Marks {
		for _, name := range []string{"scope", "topic", "mark", "count", "at", "writer"} {
			if raw := fields.Marks[i][name]; len(raw) == 0 || string(raw) == "null" {
				return r, errors.New("topic state sync: incomplete mark")
			}
		}
		key := [2]string{m.Scope, m.Topic}
		if !m.Valid() || seen[key] {
			return r, errors.New("topic state sync: invalid or duplicate mark")
		}
		seen[key] = true
	}
	return r, nil
}
