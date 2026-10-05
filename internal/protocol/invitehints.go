package protocol

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Invitation hints (Invite.Name, From, Workspace) are short readable text.
const (
	MaxInviteHint    = 64  // runes in a person's name on an invitation
	MaxWorkspaceHint = 120 // runes in a workspace's name on an invitation
)

// ValidInviteHint reports whether s may be written on an invitation as a
// hint of at most max runes: empty, or printable text without surrounding
// spaces.
func ValidInviteHint(s string, max int) bool {
	if s == "" {
		return true
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max || strings.TrimSpace(s) != s {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

func cleanHint(s string, max int) string {
	if !ValidInviteHint(s, max) {
		return ""
	}
	return s
}

// InviteLink makes a browser invitation link from an invite code: the Hub's
// page with the private, single-use code entirely in the fragment, which a
// browser never sends with its request for the page. A browser cannot apply
// a certificate pin, so a pinned invite is refused rather than dropped.
func InviteLink(code string) (string, error) {
	inv, err := DecodeInvite(code)
	if err != nil {
		return "", fmt.Errorf("cannot create a browser invitation: invalid invite code")
	}
	if inv.CertPEM != "" {
		return "", fmt.Errorf("browser invitations require HTTPS trusted by the browser; this invite uses a certificate pin, which browsers cannot apply (use a regular CLI invitation)")
	}
	u, err := url.Parse(inv.Hub)
	if err != nil {
		return "", fmt.Errorf("cannot create a browser invitation: invalid Hub URL")
	}
	u.Path = "/"
	u.Fragment = strings.TrimSpace(code)
	return u.String(), nil
}

// AppOpenURL is the link that opens the AgentNet app on fragment (an
// invitation or device link code, or a page destination such as conv=…):
// agentnet://open#fragment. The app only shows what it names; it never
// acts on it without the person's click.
func AppOpenURL(fragment string) string {
	if fragment == "" {
		return "agentnet://open"
	}
	return "agentnet://open#" + fragment
}
