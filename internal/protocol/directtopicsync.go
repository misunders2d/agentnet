package protocol

import "errors"

// CapDirectTopicSync is explicit: older own-history readers do not understand
// these private display placements and must retain their copies until updated.
const CapDirectTopicSync = "dts1"

// DirectTopicDestination binds personal organization to the original executor,
// never to a display label or the host's current default agent.
type DirectTopicDestination struct {
	Host        string `json:"host"`
	Fingerprint string `json:"fingerprint"`
	AgentID     string `json:"agent_id,omitempty"`
}

func (d DirectTopicDestination) Valid() bool {
	_, _, err := SplitAddress(d.Host)
	return err == nil && ValidFingerprint(d.Fingerprint) && (d.AgentID == "" || ValidID(d.AgentID))
}

type DirectTopicPlacement struct {
	Peer        string                 `json:"peer"`
	ID          string                 `json:"id"`
	Author      string                 `json:"author"`
	Topic       string                 `json:"topic"`
	Revision    int64                  `json:"revision"`
	Writer      string                 `json:"writer"`
	Synthetic   bool                   `json:"synthetic,omitempty"`
	Destination DirectTopicDestination `json:"destination"`
}

func (p DirectTopicPlacement) Valid() bool {
	_, _, err := SplitAddress(p.Peer)
	return err == nil && ValidID(p.ID) && ValidFingerprint(p.Author) && ValidID(p.Topic) && p.Revision > 0 && p.Revision <= MaxTopicTitleRevision && ValidFingerprint(p.Writer) && p.Destination.Valid()
}

func (p DirectTopicPlacement) Newer(old DirectTopicPlacement) bool {
	if p.Revision != old.Revision {
		return p.Revision > old.Revision
	}
	if p.Writer != old.Writer {
		return p.Writer > old.Writer
	}
	if p.Topic != old.Topic {
		return p.Topic > old.Topic
	}
	if p.Peer != old.Peer {
		return p.Peer > old.Peer
	}
	if p.Synthetic != old.Synthetic {
		return p.Synthetic
	}
	if p.Destination.Host != old.Destination.Host {
		return p.Destination.Host > old.Destination.Host
	}
	if p.Destination.Fingerprint != old.Destination.Fingerprint {
		return p.Destination.Fingerprint > old.Destination.Fingerprint
	}
	return p.Destination.AgentID > old.Destination.AgentID
}

type DirectTopicSync struct {
	V          int                    `json:"v"`
	Person     string                 `json:"person"`
	Roster     string                 `json:"roster"`
	Placements []DirectTopicPlacement `json:"placements"`
}

func ParseDirectTopicSync(data []byte) (DirectTopicSync, error) {
	var r DirectTopicSync
	if len(data) > 65536 || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Placements) == 0 || len(r.Placements) > MaxTopicMarks {
		return r, errors.New("direct topic sync: invalid owner or placements")
	}
	seen := map[[2]string]bool{}
	for _, p := range r.Placements {
		key := [2]string{p.Author, p.ID}
		if !p.Valid() || seen[key] {
			return r, errors.New("direct topic sync: invalid or duplicate placement")
		}
		seen[key] = true
	}
	return r, nil
}
