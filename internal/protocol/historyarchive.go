package protocol

import (
	"errors"
	"unicode/utf8"
)

// CapHistoryArchive reads bounded encrypted own-device history archives.
// It is explicit: older history readers still receive their existing carriers.
const CapHistoryArchive = "ha1"

const (
	HistoryArchiveFormat       = "agentnet-history-v1"
	MaxHistoryArchiveEntries   = 50
	MaxHistoryArchivePlaintext = 4 << 20
)

// HistoryArchive is the signed, encrypted descriptor of one archive attachment.
// The envelope binds its sender and exact recipient. This record grants no
// authority: clients still require current verified own-human roster members.
type HistoryArchive struct {
	V      int    `json:"v"`
	Person string `json:"person"`
	Roster string `json:"roster"`
	Count  int    `json:"count"`
	Format string `json:"format"`
}

func ParseHistoryArchive(data []byte) (HistoryArchive, error) {
	var r HistoryArchive
	if len(data) > 4096 || !utf8.Valid(data) || decodeStrictJSON(data, &r) != nil || r.V != 1 ||
		!ValidID(r.Person) || !ValidHash(r.Roster) || r.Count < 1 || r.Count > MaxHistoryArchiveEntries || r.Format != HistoryArchiveFormat {
		return r, errors.New("history archive: invalid owner, format or count")
	}
	return r, nil
}
