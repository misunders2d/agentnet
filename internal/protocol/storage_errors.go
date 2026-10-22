package protocol

// Attachment producers distinguish policy limits from exhausted physical disk.
// Drive quota is a migration pause after local custody, not a send refusal.
const (
	CodeRelayQuota    = "relay_quota"
	CodeFileTooLarge  = "file_too_large"
	CodeRelayDiskFull = "relay_disk_full"
)
