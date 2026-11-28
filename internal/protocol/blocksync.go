package protocol

import (
	"encoding/json"
	"errors"
)

// CapReminderSyncV2 reads private reminders and private person blocks
// (envelope.SubBlockSync). It replaces rem1 in the same advertised slot and
// implies that reader (CapsRecord.Reads): the capability list is full.
const CapReminderSyncV2 = "rem2"

// CapReminderSyncV3 reads everything rem2 does (reminders, person blocks)
// and this person's device chat deletions (a clear with no conversation,
// CapDeviceClear). It replaces rem2 in the same advertised slot and implies
// rem2 and rem1 (CapsRecord.Reads): the capability list is full. All three
// gate only copies to this person's own devices (blocksync.go,
// remindersync.go, convclear.go), so an older own device that reads neither
// name holds those copies until it updates; no other person's device and no
// relay reads them.
const CapReminderSyncV3 = "rem3"

// MaxBlockMarks bounds one block-sync carrier.
const MaxBlockMarks = 64

// BlockMark is one person's private choice about another person: Blocked
// stops that person's direct messages from reaching any of the owner's
// devices. The newest revision wins, the writer's fingerprint breaking ties.
// It never travels to the blocked person or the relay in the clear.
type BlockMark struct {
	Person   string `json:"person"`
	Blocked  bool   `json:"blocked"`
	Revision int64  `json:"revision"`
	Writer   string `json:"writer"`
}

// BlockSync carries block marks between current own-human devices.
type BlockSync struct {
	V      int         `json:"v"`
	Person string      `json:"person"`
	Roster string      `json:"roster"`
	Blocks []BlockMark `json:"blocks"`
}

func (m BlockMark) Newer(old BlockMark) bool {
	return m.Revision > old.Revision || m.Revision == old.Revision && m.Writer > old.Writer
}

func (m BlockMark) Valid() bool {
	return ValidID(m.Person) && ValidFingerprint(m.Writer) && m.Revision >= 1 && m.Revision <= MaxTopicTitleRevision
}

func ParseBlockSync(data []byte) (BlockSync, error) {
	var r BlockSync
	if len(data) > 65536 || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Blocks) < 1 || len(r.Blocks) > MaxBlockMarks {
		return r, errors.New("block sync: invalid owner or blocks")
	}
	var fields struct {
		Blocks []map[string]json.RawMessage `json:"blocks"`
	}
	_ = json.Unmarshal(data, &fields)
	seen := map[string]bool{}
	for i, m := range r.Blocks {
		for _, name := range []string{"person", "blocked", "revision", "writer"} {
			if raw := fields.Blocks[i][name]; len(raw) == 0 || string(raw) == "null" {
				return r, errors.New("block sync: incomplete block")
			}
		}
		if !m.Valid() || seen[m.Person] || m.Person == r.Person {
			return r, errors.New("block sync: invalid or duplicate block")
		}
		seen[m.Person] = true
	}
	return r, nil
}
