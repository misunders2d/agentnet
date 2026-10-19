package protocol

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ResourceLink is a human member's named link, not provider access or an action.
type ResourceLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// CapConversationResources gates the links extension of inert topic metadata.
const CapConversationResources = "res1"

const MaxConversationResourceBytes = 64 << 10

func ValidateResourceLinks(links []ResourceLink) error {
	for _, link := range links {
		if link.Name == "" || strings.TrimSpace(link.Name) != link.Name || !utf8.ValidString(link.Name) || strings.ContainsFunc(link.Name, unicode.IsControl) {
			return errors.New("resource name must be nonempty text without surrounding whitespace or control characters")
		}
		if strings.TrimSpace(link.URL) != link.URL || !utf8.ValidString(link.URL) || strings.ContainsFunc(link.URL, unicode.IsControl) {
			return errors.New("resource URL must be an absolute HTTP(S) link without surrounding whitespace or control characters")
		}
		u, err := url.Parse(link.URL)
		if err != nil || u.Hostname() == "" || u.User != nil || !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
			return errors.New("resource URL must be an absolute HTTP(S) link without credentials")
		}
		if strings.Contains(u.Hostname(), "%") {
			return errors.New("resource URL must not contain an IPv6 zone")
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n > 65535 {
				return errors.New("resource URL port is outside the valid range")
			}
		}
	}
	encoded, err := json.Marshal(links)
	if err != nil || len(encoded) > MaxConversationResourceBytes {
		return errors.New("shared resource links exceed the metadata size limit")
	}
	return nil
}
