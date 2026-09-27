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
