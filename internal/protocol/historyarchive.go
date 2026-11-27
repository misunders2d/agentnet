package protocol

import (
	"errors"
	"unicode/utf8"
)

// CapHistoryArchive reads large gzip-compressed encrypted own-device history
// archive parts. ha2 replaces ha1 (50-entry uncompressed chunks): a device
// that advertises only ha1 gets the existing per-message carriers instead.
const CapHistoryArchive = "ha2"

// A linked device's history copy moves as a few large parts, as Signal's and
// WhatsApp's linked-device transfers do, not as 50-message pieces. Each part
// is one gzip-compressed JSON chunk, then age-encrypted as an ordinary file
// attachment. MaxHistoryArchivePart bounds the compressed attachment (its
// signed plaintext size); MaxHistoryArchivePlaintext bounds the decompressed
// JSON, enforced while inflating. Both bound a phone importer's memory.
const (
	HistoryArchiveFormat       = "agentnet-history-v2"
	MaxHistoryArchiveEntries   = 5000
	MaxHistoryArchivePlaintext = 16 << 20
	MaxHistoryArchivePart      = 16 << 20
	// HistoryArchiveWindow is how many parts may be in flight to one device
	// at once (uploading, queued or held by the relay) instead of waiting
	// for each part's receipt before packing the next.
	HistoryArchiveWindow = 4
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
