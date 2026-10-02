package client

import (
	"context"
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/gdrive"
)

func (a *Agent) DriveStorageConfig(ctx context.Context) (gdrive.StorageSettings, error) {
	var v gdrive.StorageSettings
	if e := a.hub.do(ctx, http.MethodGet, "/v1/storage/drive", nil, &v); e != nil {
		return v, errors.New("workspace file storage settings unavailable; native encrypted attachments remain available")
	}
	return v, v.Config.Validate()
}
func (a *Agent) SetDriveStorageConfig(ctx context.Context, u gdrive.StorageUpdate) (gdrive.StorageSettings, error) {
	var v gdrive.StorageSettings
	if e := u.Config.Validate(); e != nil {
		return v, e
	}
	if e := a.hub.do(ctx, http.MethodPut, "/v1/admin/storage/drive", u, &v); e != nil {
		return v, e
	}
	return v, v.Config.Validate()
}

type DriveSetupRequest struct {
	Secret  string            `json:"desktop_client_secret,omitempty"`
	Action  string            `json:"action"`
	Draft   gdrive.SetupDraft `json:"draft"`
	Expect  uint64            `json:"expect"`
	Confirm bool              `json:"confirm"`
}
type DriveSetupView struct {
	Settings gdrive.StorageSettings `json:"settings"`
	Draft    gdrive.SetupDraft      `json:"draft"`
	Steps    []gdrive.SetupStep     `json:"steps"`
	Notice   string                 `json:"notice"`
}

// DriveSetup is Settings > File storage options. It never executes gcloud or
// provisions a project. Optional Drive setup never enters messenger onboarding.
func (a *Agent) DriveSetup(ctx context.Context, r DriveSetupRequest) (DriveSetupView, error) {
	var v DriveSetupView
	settings, e := a.DriveStorageConfig(ctx)
	if e != nil {
		return v, e
	}
	v.Settings = settings
	mu := a.driveLock()
	mu.Lock()
	defer mu.Unlock()
	s, e := a.readDrive()
	if e != nil {
		return v, e
	}
	draft := gdrive.SetupDraft{Config: settings.Config, ExistingProject: true}
	if s.Setup != nil {
		draft = *s.Setup
	}
	switch r.Action {
	case "", "status":
	case "local-secret":
		if !settings.Config.Enabled || settings.Config.DesktopClientID == "" {
			return v, errors.New("workspace Desktop Drive setup must be enabled first")
		}
		if len(r.Secret) > 512 {
			return v, errors.New("invalid Desktop client secret")
		}
		if s.Token.Access != "" {
			return v, errors.New("disconnect Google before changing local Desktop client secret")
		}
		s.ClientID = settings.Config.DesktopClientID
		s.Secret = r.Secret
		if e = a.writeDrive(s); e != nil {
			return v, e
		}
	case "save-draft":
		if !settings.CanAdmin {
			return v, errors.New("workspace admin only")
		}
		if _, e = gdrive.SetupGuide(r.Draft); e != nil {
			return v, e
		}
		draft = r.Draft
		s.Setup = &draft
		if e = a.writeDrive(s); e != nil {
			return v, e
		}
	case "publish":
		if !settings.CanAdmin || !r.Confirm {
			return v, errors.New("workspace admin must explicitly confirm optional Drive configuration")
		}
		if _, e = gdrive.SetupGuide(r.Draft); e != nil {
			return v, e
		}
		v.Settings, e = a.SetDriveStorageConfig(ctx, gdrive.StorageUpdate{Config: r.Draft.Config, Expect: r.Expect})
		if e != nil {
			return v, e
		}
		draft = r.Draft
		s.Setup = &draft
		if e = a.writeDrive(s); e != nil {
			return v, e
		}
	default:
		return v, errors.New("unknown file storage setup action")
	}
	v.Draft = draft
	v.Steps, e = gdrive.SetupGuide(draft)
	v.Notice = "Google Drive optional, off by default. Checklist records admin confirmation, not live Google verification. Workspace setup shares public client IDs only. Each person separately consents on their client; existing Google tokens are never replaced automatically."
	return v, e
}
