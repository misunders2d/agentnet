package protocol

import (
	"encoding/json"
	"errors"
)

// CapDeviceHistory is explicit: older own2 devices do not understand inert
// copies of rootless device threads. They must never receive one as a request.
const CapDeviceHistory = CapOwnSyncV3

// DeviceHistory retains the original endpoints of an app-visible device turn.
// Item is the existing inert history manifest, checked by the client. RecipientKey
// may be absent on old accepted rows; a current pin never invents that old fact.
type DeviceHistory struct {
	V            int             `json:"v"`
	Person       string          `json:"person"`
	Roster       string          `json:"roster"`
	Recipient    string          `json:"recipient"`
	RecipientKey string          `json:"recipient_key,omitempty"`
	Item         json.RawMessage `json:"item"`
}

func ParseDeviceHistory(data []byte) (DeviceHistory, error) {
	var r DeviceHistory
	if len(data) > MaxBody || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Item) == 0 || r.Item[0] != '{' {
		return r, errors.New("device history: invalid owner or item")
	}
	if _, _, err := SplitAddress(r.Recipient); err != nil || r.RecipientKey != "" && !ValidFingerprint(r.RecipientKey) {
		return r, errors.New("device history: invalid original recipient")
	}
	return r, nil
}

// DeviceFile is an exact original-manifest request or response. Its item uses
// the existing history-file descriptor; clients validate that descriptor too.
type DeviceFile struct {
	V      int             `json:"v"`
	Person string          `json:"person"`
	Roster string          `json:"roster"`
	Item   json.RawMessage `json:"item"`
}

func ParseDeviceFile(data []byte) (DeviceFile, error) {
	var r DeviceFile
	if len(data) > MaxBody || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Item) == 0 || r.Item[0] != '{' {
		return r, errors.New("device file: invalid owner or descriptor")
	}
	return r, nil
}
