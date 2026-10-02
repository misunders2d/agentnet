package protocol

import "errors"

// CapGroup is negotiated only after both engines support group carriers.
// It is deliberately absent from the default advertised capabilities.
const CapGroup = "grp1"

// GroupCarrier describes one bounded JSON attachment. The subtype selects
// GroupJournalPage or the current GroupContext; original signatures inside
// that attachment, rather than the forwarding device, establish authority.
type GroupCarrier struct {
	V     int    `json:"v"`
	Seq   int64  `json:"seq"`
	Hash  string `json:"hash"`
	ToKey string `json:"to_key"`
}

func (c GroupCarrier) Validate() error {
	if c.V != 1 || c.Seq < 0 || !ValidHash(c.Hash) || !ValidFingerprint(c.ToKey) {
		return errors.New("group: invalid carrier descriptor")
	}
	return nil
}
