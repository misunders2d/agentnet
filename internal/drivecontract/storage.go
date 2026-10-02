package drivecontract

import (
	"errors"
	"net/url"
	"regexp"
)

// StorageConfig is workspace-admin configuration. Public OAuth client IDs are
// identifiers, never access/refresh tokens or client secrets. Zero means off.
// This is separate from each person's private consent and token state.
type StorageConfig struct {
	Enabled          bool     `json:"enabled"`
	Project          string   `json:"project,omitempty"`
	DesktopClientID  string   `json:"desktop_client_id,omitempty"`
	BrowserClientID  string   `json:"browser_client_id,omitempty"`
	BrowserOrigins   []string `json:"browser_origins,omitempty"`
	APIConfirmed     bool     `json:"api_confirmed,omitempty"`
	ConsentConfirmed bool     `json:"consent_confirmed,omitempty"`
	ClientsConfirmed bool     `json:"clients_confirmed,omitempty"`
}

var projectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
var oauthID = regexp.MustCompile(`^[0-9]+-[A-Za-z0-9_-]+\.apps\.googleusercontent\.com$`)

func ValidProject(id string) bool { return projectID.MatchString(id) }
func (c StorageConfig) Validate() error {
	if c.Project != "" && !ValidProject(c.Project) {
		return errors.New("Google project ID must be 6-30 lowercase letters, digits or hyphens")
	}
	for _, id := range []string{c.DesktopClientID, c.BrowserClientID} {
		if id != "" && (len(id) > 512 || !oauthID.MatchString(id)) {
			return errors.New("use a registered Google OAuth client ID, not a token or secret")
		}
	}
	if len(c.BrowserOrigins) > 20 {
		return errors.New("at most 20 browser OAuth origins")
	}
	for _, origin := range c.BrowserOrigins {
		u, e := url.Parse(origin)
		if e != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "[::1]" || u.Hostname() == "::1"))) {
			return errors.New("Google browser OAuth origin must be an HTTPS origin or local loopback HTTP origin")
		}
	}
	if c.Enabled && (c.Project == "" || (c.DesktopClientID == "" && c.BrowserClientID == "") || !c.APIConfirmed || !c.ConsentConfirmed || !c.ClientsConfirmed) {
		return errors.New("finish project, Drive API, consent and OAuth client setup before enabling optional Google Drive")
	}
	if c.Enabled && c.BrowserClientID != "" && len(c.BrowserOrigins) == 0 {
		return errors.New("register browser authorized origins before enabling browser Drive")
	}
	return nil
}

type StorageSettings struct {
	Config   StorageConfig `json:"config"`
	Revision uint64        `json:"revision"`
	CanAdmin bool          `json:"can_admin"`
}
type StorageUpdate struct {
	Config StorageConfig `json:"config"`
	Expect uint64        `json:"expect"`
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func ValidID(id string) bool { return validID.MatchString(id) }
