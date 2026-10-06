// Package googleauth validates Google credentials without retaining them.
package googleauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

var ErrCredential = errors.New("Google sign-in could not be verified; sign in again")

type Claims struct{ Email, Name, Subject, Domain string }
type Validator struct {
	verifier  *idtoken.Validator
	audiences []string
}

func New(client *http.Client, audiences []string) (*Validator, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	v, err := idtoken.NewValidator(context.Background(), option.WithHTTPClient(&safe))
	if err != nil {
		return nil, err
	}
	return &Validator{verifier: v, audiences: slices.Clone(audiences)}, nil
}

func (v *Validator) Verify(ctx context.Context, token, nonce string) (Claims, error) {
	if len(token) == 0 || len(token) > 16384 || len(v.audiences) == 0 || nonce == "" {
		return Claims{}, ErrCredential
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrCredential
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	var header struct {
		Alg string `json:"alg"`
	}
	if err != nil || json.Unmarshal(b, &header) != nil || header.Alg != "RS256" {
		return Claims{}, ErrCredential
	}
	p, err := v.verifier.Validate(ctx, token, "")
	if err != nil || (p.Issuer != "accounts.google.com" && p.Issuer != "https://accounts.google.com") ||
		!slices.Contains(v.audiences, p.Audience) || p.Expires <= time.Now().Unix() || p.IssuedAt > time.Now().Add(time.Minute).Unix() || p.Subject == "" {
		return Claims{}, ErrCredential
	}
	verified, _ := p.Claims["email_verified"].(bool)
	n, _ := p.Claims["nonce"].(string)
	e, _ := p.Claims["email"].(string)
	email, err := protocol.NormalizeEmail(e)
	if !verified || n != nonce || err != nil {
		return Claims{}, ErrCredential
	}
	if azp, ok := p.Claims["azp"]; ok && azp != p.Audience {
		return Claims{}, ErrCredential
	}
	name, _ := p.Claims["name"].(string)
	if protocol.ValidLabel(name) != nil {
		name = strings.Split(email, "@")[0]
		if len(name) > protocol.MaxPersonLabel {
			name = name[:protocol.MaxPersonLabel]
		}
	}
	domain, _ := p.Claims["hd"].(string)
	return Claims{Email: email, Name: name, Subject: p.Subject, Domain: strings.ToLower(domain)}, nil
}
