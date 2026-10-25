package protocol

import (
	"encoding/json"
	"errors"
)

// V2 adds private completed-snapshot locations. It also reads V1 marks;
// V1 writers cannot infer this capability from any older advertisement.
const CapTopicStateSyncV2 = "tss2"

type TopicLocation struct {
	Scope            string `json:"scope"`
	Topic            string `json:"topic"`
	Destination      string `json:"destination"`
	DestinationTopic string `json:"destination_topic"`
	Operation        string `json:"operation"`
	At               int64  `json:"at"`
	Writer           string `json:"writer"`
	Revision         int64  `json:"revision"`
}

func (p TopicLocation) Valid() bool {
	_, _, e := SplitAddress(p.Scope)
	return (ValidHash(p.Scope) || e == nil) && ValidID(p.Topic) && ValidHash(p.Destination) && (p.DestinationTopic == "" || ValidID(p.DestinationTopic)) && ValidID(p.Operation) && ValidFingerprint(p.Writer) && p.At > 0 && p.At <= MaxTopicTitleRevision && p.Revision > 0 && p.Revision <= MaxTopicTitleRevision
}
func (p TopicLocation) Newer(old TopicLocation) bool {
	if p.Revision != old.Revision {
		return p.Revision > old.Revision
	}
	if p.At != old.At {
		return p.At > old.At
	}
	if p.Writer != old.Writer {
		return p.Writer > old.Writer
	}
	if p.Operation != old.Operation {
		return p.Operation > old.Operation
	}
	if p.Destination != old.Destination {
		return p.Destination > old.Destination
	}
	return p.DestinationTopic > old.DestinationTopic
}

type TopicLocationSync struct {
	V         int             `json:"v"`
	Person    string          `json:"person"`
	Roster    string          `json:"roster"`
	Locations []TopicLocation `json:"locations"`
}

func ParseTopicLocationSync(data []byte) (TopicLocationSync, error) {
	var r TopicLocationSync
	if len(data) > 65536 || decodeStrictJSON(data, &r) != nil || r.V != 2 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Locations) == 0 || len(r.Locations) > MaxTopicMarks {
		return r, errors.New("topic location sync: invalid owner or locations")
	}
	var fields struct {
		Locations []map[string]json.RawMessage `json:"locations"`
	}
	_ = json.Unmarshal(data, &fields)
	seen := map[[2]string]bool{}
	for i, p := range r.Locations {
		for _, name := range []string{"scope", "topic", "destination", "destination_topic", "operation", "at", "writer", "revision"} {
			raw := fields.Locations[i][name]
			if len(raw) == 0 || string(raw) == "null" {
				return r, errors.New("topic location sync: incomplete location")
			}
		}
		key := [2]string{p.Scope, p.Topic}
		if !p.Valid() || seen[key] {
			return r, errors.New("topic location sync: invalid or duplicate location")
		}
		seen[key] = true
	}
	return r, nil
}

// CheckTopicStateCarrier keeps both version-specific parsers strict.
func CheckTopicStateCarrier(data []byte) error {
	var v struct {
		V int `json:"v"`
	}
	if json.Unmarshal(data, &v) != nil {
		return errors.New("topic state sync: invalid version")
	}
	if v.V == 2 {
		_, e := ParseTopicLocationSync(data)
		return e
	}
	_, e := ParseTopicStateSync(data)
	return e
}
