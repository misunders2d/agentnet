package protocol

// CapTopicOrganization preserves exact selected-only topic moves and merges.
// Released topic readers reject these actions; room capabilities do not imply it.
const CapTopicOrganization = "org1"

// CapSharedTopicTitle gates presence-aware shared rename metadata.
const CapSharedTopicTitle = "ttl1"
