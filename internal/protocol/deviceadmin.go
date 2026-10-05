package protocol

import "errors"

// DeviceAdminNotice is Hub-owned role metadata, delivered only to exact
// devices of the affected person. It reports a change and grants nothing.
type DeviceAdminNotice struct {
	Seq    int64  `json:"seq"`
	ID     string `json:"id"`
	Person string `json:"person"`
	Device string `json:"device"`
	By     string `json:"by"`
	Admin  bool   `json:"admin"`
	At     int64  `json:"at"`
}

func (n DeviceAdminNotice) Valid() error {
	if n.Seq <= 0 || !ValidID(n.ID) || !ValidID(n.Person) || n.At <= 0 || n.At >= 1<<40 {
		return errors.New("invalid device admin notice")
	}
	for _, a := range []string{n.Device, n.By} {
		if _, _, err := SplitAddress(a); err != nil {
			return errors.New("invalid device admin notice address")
		}
	}
	return nil
}
