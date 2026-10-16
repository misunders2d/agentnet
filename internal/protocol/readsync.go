package protocol

import (
	"encoding/json"
	"errors"
)

// CapReadSync is explicit: rm1 alone never implies read-state sharing.
const CapReadSync = "rd1"
const CapReadSyncV2 = "rd2"
const MaxReadRefs = 64
const MaxAttentionRevision int64 = 9007199254740991

type AttentionGeneration struct {
	Rev    int64  `json:"rev"`
	Writer string `json:"writer"`
}

func (g AttentionGeneration) Valid() bool {
	return g.Rev > 0 && g.Rev <= MaxAttentionRevision && ValidFingerprint(g.Writer)
}

type AttentionMark struct {
	ReadRef
	AttentionGeneration
	State string `json:"state"`
}

type ReadRef struct {
	Conv        string `json:"conv"`
	Fingerprint string `json:"fingerprint"`
	LID         string `json:"lid"`
}
type ReadSync struct {
	V      int             `json:"v"`
	Person string          `json:"person"`
	Roster string          `json:"roster"`
	Refs   []ReadRef       `json:"refs"`
	Marks  []AttentionMark `json:"marks,omitempty"`
}

func ParseReadSync(data []byte) (ReadSync, error) {
	var r ReadSync
	if len(data) > 32768 || decodeStrictJSON(data, &r) != nil {
		return r, errors.New("read sync: invalid record")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	_, hasMarks := fields["marks"]
	if (r.V != 1 && r.V != 2) || r.V == 1 && hasMarks || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Refs)+len(r.Marks) == 0 || len(r.Refs)+len(r.Marks) > MaxReadRefs {
		return r, errors.New("read sync: invalid owner or references")
	}
	seen := map[ReadRef]bool{}
	for _, ref := range r.Refs {
		if ref.Conv != "" && !ValidHash(ref.Conv) || !ValidFingerprint(ref.Fingerprint) || !ValidID(ref.LID) || seen[ref] {
			return r, errors.New("read sync: invalid or duplicate reference")
		}
		seen[ref] = true
	}
	for _, mark := range r.Marks {
		ref := mark.ReadRef
		if ref.Conv != "" && !ValidHash(ref.Conv) || !ValidFingerprint(ref.Fingerprint) || !ValidID(ref.LID) || seen[ref] || !mark.AttentionGeneration.Valid() || mark.State != "read" && mark.State != "unread" {
			return r, errors.New("read sync: invalid or duplicate attention mark")
		}
		seen[ref] = true
	}
	return r, nil
}
