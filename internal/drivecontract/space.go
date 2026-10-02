package drivecontract

import "errors"

// Space is public-to-the-conversation metadata only. Transport must authenticate
// Owner as its current conversation member and encrypt this object to the room.
// Account emails, OAuth credentials and broker grants never enter this object.
type Space struct {
	Conv         string `json:"conv"`
	Folder       string `json:"folder"`
	Name         string `json:"name"`
	Owner        string `json:"owner"`
	Revision     uint64 `json:"revision"`
	Previous     string `json:"previous,omitempty"`
	Disconnected bool   `json:"disconnected,omitempty"`
}

func (s Space) Validate() error {
	if s.Conv == "" || len(s.Conv) > 256 || s.Owner == "" || len(s.Owner) > 256 || s.Revision == 0 || !ValidID(s.Folder) || s.Name == "" || len(s.Name) > 255 {
		return errors.New("invalid conversation Drive metadata")
	}
	return nil
}
