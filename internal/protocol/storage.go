package protocol

// StorageBucket counts ciphertext reservations from the Hub's existing blob
// rows. ReservedBytes is the quota charge, not measured filesystem usage;
// RecordedReceivedBytes is the last progress recorded for those reservations.
type StorageBucket struct {
	Files                 int64 `json:"files"`
	ReservedBytes         int64 `json:"reserved_bytes"`
	RecordedReceivedBytes int64 `json:"recorded_received_bytes"`
}

type BlobStorageUsage struct {
	Stored     StorageBucket `json:"stored"`
	Incomplete StorageBucket `json:"incomplete"` // includes reclamation still charged to quota
}

// HubStorage is an authenticated caller's storage view. Global is included
// only for a verified Hub admin. QuotaBytes always applies to the whole Hub:
// it is never the caller's personal quota or available allowance.
type HubStorage struct {
	Scope                string            `json:"scope"`       // caller-owned-ciphertext
	QuotaScope           string            `json:"quota_scope"` // hub-global
	Own                  BlobStorageUsage  `json:"own"`
	Global               *BlobStorageUsage `json:"global,omitempty"`
	QuotaBytes           int64             `json:"quota_bytes"`
	MaxFileBytes         int64             `json:"max_file_bytes"` // plaintext bytes
	UploadIdleTTLSeconds float64           `json:"upload_idle_ttl_seconds"`
	Location             string            `json:"location"` // relative, never an operator's path
	Policy               HubStoragePolicy  `json:"policy"`
}

// These describe existing behavior; none schedules or performs cleanup.
type HubStoragePolicy struct {
	DeliveredAttachments              string  `json:"delivered_attachments"`
	UnattachedAttachments             string  `json:"unattached_attachments"`
	UndeliveredAttachments            string  `json:"undelivered_attachments"`
	MessageEnvelopes                  string  `json:"message_envelopes"`
	IncompleteUploads                 string  `json:"incomplete_uploads"`
	ManualDeliveredAgeDefaultSeconds  float64 `json:"manual_delivered_age_default_seconds"`
	ManualUnattachedAgeDefaultSeconds float64 `json:"manual_unattached_age_default_seconds"`
}
