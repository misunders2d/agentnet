package protocol

import "errors"

// CapReadSync is explicit: rm1 alone never implies read-state sharing.
const CapReadSync = "rd1"
const MaxReadRefs = 64

type ReadRef struct {
	Conv        string `json:"conv"`
	Fingerprint string `json:"fingerprint"`
	LID         string `json:"lid"`
}
type ReadSync struct {
	V      int       `json:"v"`
	Person string    `json:"person"`
	Roster string    `json:"roster"`
	Refs   []ReadRef `json:"refs"`
}

func ParseReadSync(data []byte) (ReadSync, error) {
	var r ReadSync
	if len(data) > 32768 || decodeStrictJSON(data, &r) != nil {
		return r, errors.New("read sync: invalid record")
	}
	if r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Refs) == 0 || len(r.Refs) > MaxReadRefs {
		return r, errors.New("read sync: invalid owner or references")
	}
	seen := map[ReadRef]bool{}
	for _, ref := range r.Refs {
		if ref.Conv != "" && !ValidHash(ref.Conv) || !ValidFingerprint(ref.Fingerprint) || !ValidID(ref.LID) || seen[ref] {
			return r, errors.New("read sync: invalid or duplicate reference")
		}
		seen[ref] = true
	}
	return r, nil
}
