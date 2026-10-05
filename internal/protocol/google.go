package protocol

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/misunders2d/agentnet/internal/identity"
)

// GoogleConfig contains only public OAuth client identifiers. Desktop
// authorization codes are exchanged by the relay; no client secret or
// Google session is stored on the device.
type GoogleConfig struct {
	WebClientID     string `json:"web_client_id,omitempty"`
	DesktopClientID string `json:"desktop_client_id,omitempty"`
}

// NormalizeEmail deliberately accepts only plain ASCII mailbox addresses:
// no display names, whitespace, aliases invented by us or domain suffixes.
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	local, domain, ok := strings.Cut(s, "@")
	if !ok || len(s) > 254 || len(local) == 0 || len(local) > 64 || strings.Contains(domain, "@") {
		return "", errors.New("use an email address, such as name@example.com")
	}
	if _, err := NormalizeEmailDomain(domain); err != nil {
		return "", err
	}
	for _, c := range local {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune(".!#$%&'*+-/=?^_`{|}~", c)) {
			return "", errors.New("use a plain email address")
		}
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return "", errors.New("invalid email address")
	}
	return s, nil
}

func NormalizeEmailDomain(s string) (string, error) {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	if len(s) == 0 || len(s) > 253 || !strings.Contains(s, ".") {
		return "", errors.New("use a domain, such as example.com")
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid email domain")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("invalid email domain")
			}
		}
	}
	return s, nil
}

// GoogleLabel is an internal routing label, never a person's display name.
func GoogleLabel(email string) string {
	h := sha256.Sum256([]byte(email))
	return "g-" + hex.EncodeToString(h[:12])
}

// GoogleNonce binds a Google login to both keys, independently of the
// internal device address assigned after sign-in. A stolen ID token cannot
// enroll a different key.
func GoogleNonce(p identity.Public) string {
	h := sha256.Sum256([]byte("agentnet-google-device-v1\n" + base64.StdEncoding.EncodeToString(p.SignKey) + "\n" + p.BoxRecipient))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// GoogleRequest proves possession over every field, including the ID token.
// Prepare uses only IDToken and Public; join also carries a first signed
// roster or consent to the next step of an existing person's roster.
type GoogleRequest struct {
	IDToken string          `json:"id_token"`
	Public  identity.Public `json:"public"`
	First   *PersonRoster   `json:"first,omitempty"`
	Link    *GoogleLink     `json:"link,omitempty"`
	Sig     []byte          `json:"sig"`
}

func (r GoogleRequest) canonical() []byte {
	r.Sig = nil
	b, _ := json.Marshal(r)
	return append([]byte("agentnet-google-v1\n"), b...)
}
func (r *GoogleRequest) Sign(key ed25519.PrivateKey) { r.Sig = ed25519.Sign(key, r.canonical()) }
func (r GoogleRequest) Verify() error {
	if err := r.Public.Verify(); err != nil {
		return err
	}
	if !ed25519.Verify(r.Public.SignKey, r.canonical(), r.Sig) {
		return errors.New("invalid Google join signature")
	}
	return nil
}

type GooglePrepared struct {
	Email    string           `json:"email"`
	Name     string           `json:"name"`
	Address  string           `json:"address"`
	Head     *PersonRoster    `json:"head,omitempty"`
	Approver *identity.Public `json:"approver,omitempty"`
}

type GoogleLink struct {
	Email    string       `json:"email"`
	Person   string       `json:"person"`
	Seq      int64        `json:"seq"`
	Roster   string       `json:"roster"`
	Approver LinkApprover `json:"approver"`
	Expires  int64        `json:"expires"`
	Offer    string       `json:"offer"`
	Join     []byte       `json:"join"`
}

type GoogleAccess struct {
	WorkspaceURL string        `json:"workspace_url"`
	Enabled      bool          `json:"enabled"`
	CanAdmin     bool          `json:"can_admin"`
	Emails       []GoogleEmail `json:"emails"`
	Domains      []string      `json:"domains"`
}
type GoogleEmail struct {
	Email  string `json:"email"`
	Admin  bool   `json:"admin"`
	Denied bool   `json:"denied"`
}
type GoogleAccessChange struct {
	Email  string `json:"email,omitempty"`
	Domain string `json:"domain,omitempty"`
	Remove bool   `json:"remove"`
	Admin  bool   `json:"admin"`
}

// GoogleExchange is signed before the relay exchanges a Desktop OAuth
// code. The verifier is single-use and is never persisted or logged.
type GoogleExchange struct {
	Code     string          `json:"code"`
	Verifier string          `json:"verifier"`
	Redirect string          `json:"redirect"`
	Public   identity.Public `json:"public"`
	Sig      []byte          `json:"sig"`
}

func (r GoogleExchange) canonical() []byte {
	r.Sig = nil
	b, _ := json.Marshal(r)
	return append([]byte("agentnet-google-exchange-v1\n"), b...)
}
func (r *GoogleExchange) Sign(key ed25519.PrivateKey) { r.Sig = ed25519.Sign(key, r.canonical()) }
func (r GoogleExchange) Verify() error {
	if err := r.Public.Verify(); err != nil {
		return err
	}
	if !ed25519.Verify(r.Public.SignKey, r.canonical(), r.Sig) {
		return errors.New("invalid Google exchange signature")
	}
	return nil
}
