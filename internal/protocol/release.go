package protocol

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// Release is a Hub operator's recommended client version: information
// for people and their agents, never installation authority. An empty
// Version means no recommendation.
type Release struct {
	Version string `json:"version"`
	URL     string `json:"url,omitempty"`  // https page with update instructions
	Note    string `json:"note,omitempty"` // for people; not given to models
}

// ReleaseRequest sets (or, with Clear, removes) the recommendation.
type ReleaseRequest struct {
	Release
	Clear bool `json:"clear,omitempty"`
}

// Validate checks a recommendation: a printable version without spaces, an
// https URL without credentials, and a short one-line printable note.
func (r Release) Validate() error {
	if r.Version == "" || len(r.Version) > 64 || strings.IndexFunc(r.Version, func(c rune) bool { return c <= ' ' || c > '~' }) >= 0 {
		return errors.New("version must be 1-64 printable ASCII characters without spaces")
	}
	u, err := url.Parse(r.URL)
	if err != nil || len(r.URL) > 512 || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		strings.IndexFunc(r.URL, func(c rune) bool { return c <= ' ' || c == 0x7f }) >= 0 {
		return errors.New("url must be an https URL without credentials, spaces or control characters (at most 512 bytes)")
	}
	if len(r.Note) > 300 || strings.IndexFunc(r.Note, func(c rune) bool { return !unicode.IsPrint(c) }) >= 0 {
		return errors.New("note must be one line of printable text (at most 300 bytes)")
	}
	return nil
}

// Key identifies a recommendation for "already told" bookkeeping: setting
// the same one again is not new, a different version or URL is.
func (r Release) Key() string { return r.Version + " " + r.URL }

// FeatureUpdate says the Hub reads VersionHeader, its CORS check allows it,
// and, when the Hub runs a release, it asks clients older than that release
// to update (GET /v1/version). A browser page sends that header to
// another origin's relay only when the relay says this: an older relay's
// CORS check refuses headers it does not know.
const FeatureUpdate = "update1"

// CodeUpdateRequired is the "error" of a Hub request refused with HTTP 426
// because the client must be updated first (UpdateRequiredError).
const CodeUpdateRequired = "update_required"

// UpdateRequired names the release a client must run to use this Hub again:
// the relay's own release, never an admin's recommendation.
// It is the data of the "update_required" push event, which follows the
// "release" event on the stream of a suspended device; that stream then
// carries pings only, so what is addressed to the device stays in the Hub's
// custody until it connects again, updated.
type UpdateRequired struct {
	Latest string `json:"latest"`
	URL    string `json:"url"` // ReleaseURL(Latest)
}

// UpdateRequiredError is the HTTP 426 answer to a request of a suspended
// device, except those that let work admitted before drain (its stream,
// its ping and message acknowledgements, GET /v1/release, read-only member
// lookups, and posting an answer or a result). Error is CodeUpdateRequired;
// Message is for people.
type UpdateRequiredError struct {
	Error string `json:"error"`
	UpdateRequired
	Message string `json:"message"`
}

// NewUpdateRequired is the 426 answer that asks for release latest.
func NewUpdateRequired(latest string) UpdateRequiredError {
	return UpdateRequiredError{Error: CodeUpdateRequired, UpdateRequired: UpdateRequired{Latest: latest, URL: ReleaseURL(latest)},
		Message: "Update AgentNet to " + latest + " to continue."}
}
