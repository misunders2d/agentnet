package protocol

import (
	"encoding/json"
	"errors"
)

// CapReminderSync is an explicit reader for private reminders. It consumes the
// last v0.9.1 capability slot; older readers parse the record but receive no carrier.
const CapReminderSync = "rem1"
const MaxReminderMarks = 64

type ReminderMark struct {
	Conv        string `json:"conv"`
	Fingerprint string `json:"fingerprint"`
	LID         string `json:"lid"`
	Due         int64  `json:"due"`
	State       string `json:"state"`
	Revision    int64  `json:"revision"`
	Writer      string `json:"writer"`
}
type ReminderSync struct {
	V         int            `json:"v"`
	Person    string         `json:"person"`
	Roster    string         `json:"roster"`
	Reminders []ReminderMark `json:"reminders"`
}

func (m ReminderMark) Newer(old ReminderMark) bool {
	return m.Revision > old.Revision || m.Revision == old.Revision && m.Writer > old.Writer
}
func (m ReminderMark) Valid() bool {
	if m.Conv != "" && !ValidHash(m.Conv) || !ValidFingerprint(m.Fingerprint) || !ValidID(m.LID) || !ValidFingerprint(m.Writer) || m.Revision < 1 || m.Revision > MaxTopicTitleRevision || m.Due < 1 || m.Due > MaxTopicTitleRevision {
		return false
	}
	switch m.State {
	case "pending", "done", "cancelled", "replied":
		return true
	}
	return false
}
func ParseReminderSync(data []byte) (ReminderSync, error) {
	var r ReminderSync
	if len(data) > 65536 || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Reminders) < 1 || len(r.Reminders) > MaxReminderMarks {
		return r, errors.New("reminder sync: invalid owner or reminders")
	}
	var fields struct {
		Reminders []map[string]json.RawMessage `json:"reminders"`
	}
	_ = json.Unmarshal(data, &fields)
	seen := map[[3]string]bool{}
	for i, m := range r.Reminders {
		for _, name := range []string{"conv", "fingerprint", "lid", "due", "state", "revision", "writer"} {
			if raw := fields.Reminders[i][name]; len(raw) == 0 || string(raw) == "null" {
				return r, errors.New("reminder sync: incomplete reminder")
			}
		}
		key := [3]string{m.Conv, m.Fingerprint, m.LID}
		if !m.Valid() || seen[key] {
			return r, errors.New("reminder sync: invalid or duplicate reminder")
		}
		seen[key] = true
	}
	return r, nil
}
