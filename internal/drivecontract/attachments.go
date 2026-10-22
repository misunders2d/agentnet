package drivecontract

// RelayStorageStatus describes the relay-wide encrypted attachment backend.
// It is separate from a person's optional plaintext Google Drive project space.
type RelayStorageStatus struct {
	CanAdmin        bool           `json:"can_admin"`
	Configured      bool           `json:"configured"`
	Connected       bool           `json:"connected"`
	State           string         `json:"state"`
	Notice          string         `json:"notice,omitempty"`
	Account         *RelayAccount  `json:"account,omitempty"`
	Folder          *RelayFolder   `json:"folder,omitempty"`
	Local           RelayAmount    `json:"local"`
	Remote          RelayAmount    `json:"remote"`
	Pending         RelayAmount    `json:"pending"`
	RemoteOnlyFiles int64          `json:"remote_only_files"`
	Migration       RelayMigration `json:"migration"`
	ConsentURL      string         `json:"consent_url,omitempty"`
}

type RelayAccount struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}
type RelayFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type RelayAmount struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}
type RelayMigration struct {
	Running    bool  `json:"running"`
	Files      int64 `json:"files"`
	Bytes      int64 `json:"bytes"`
	TotalFiles int64 `json:"total_files"`
	TotalBytes int64 `json:"total_bytes"`
}
type RelayStorageAction struct {
	Action  string `json:"action"`
	Confirm bool   `json:"confirm,omitempty"`
}
