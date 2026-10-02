package gdrive

import (
	"errors"
	"fmt"
	"github.com/misunders2d/agentnet/internal/drivecontract"
	"net/mail"
	"strings"
)

type StorageConfig = drivecontract.StorageConfig

func ValidProject(id string) bool { return drivecontract.ValidProject(id) }

// SetupDraft is resumable admin-local guidance, not proof of remote setup.
// CloudAccount remains on this local admin client, never public config.
type SetupDraft struct {
	Config          StorageConfig   `json:"config"`
	CloudAccount    string          `json:"cloud_account,omitempty"`
	ExistingProject bool            `json:"existing_project"`
	Completed       map[string]bool `json:"completed,omitempty"`
}
type SetupStep struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Commands []string `json:"commands,omitempty"`
	URL      string   `json:"url,omitempty"`
	Complete bool     `json:"complete"`
}

const ProjectsDoc = "https://docs.cloud.google.com/sdk/gcloud/reference/projects/create"
const ServicesDoc = "https://docs.cloud.google.com/sdk/gcloud/reference/services/enable"
const ConsentDoc = "https://developers.google.com/workspace/guides/configure-oauth-consent"
const CredentialsDoc = "https://developers.google.com/workspace/guides/create-credentials"

// SetupGuide builds reviewable commands only. It executes no cloud mutation,
// changes no default CLI project/account, and creates no IAP/IAM OAuth clients.
func SetupGuide(d SetupDraft) ([]SetupStep, error) {
	check := d.Config
	check.Enabled = false
	if e := check.Validate(); e != nil {
		return nil, e
	}
	if d.CloudAccount != "" {
		a, e := mail.ParseAddress(d.CloudAccount)
		if e != nil || a.Address != d.CloudAccount || strings.ContainsAny(d.CloudAccount, "'\r\n") {
			return nil, errors.New("choose the exact verified Google Cloud account email")
		}
	}
	complete := func(id string) bool { return d.Completed[id] }
	steps := []SetupStep{{ID: "account", Title: "Choose Google Cloud account", Detail: "Install gcloud if needed. Inspect signed-in accounts, then explicitly choose your admin account. No existing CLI defaults are changed.", Commands: []string{"gcloud auth list --format='table(account,status)'"}, URL: "https://docs.cloud.google.com/sdk/docs/install", Complete: complete("account")}}
	project, account := d.Config.Project, d.CloudAccount
	var commands []string
	if project != "" && account != "" {
		if !d.ExistingProject {
			commands = append(commands, fmt.Sprintf("gcloud projects create %s --account='%s'", project, account))
		}
		commands = append(commands, fmt.Sprintf("gcloud projects describe %s --account='%s' --format='table(projectId,name,lifecycleState)'", project, account))
	}
	steps = append(steps, SetupStep{ID: "project", Title: "Create or select project", Detail: "Choose an existing project or a new unique project ID. Organization restrictions may require your organization/folder administrator. Inspect intended project and account before any mutation.", Commands: commands, URL: ProjectsDoc, Complete: complete("project")})
	commands = nil
	if project != "" && account != "" {
		commands = []string{fmt.Sprintf("gcloud services enable drive.googleapis.com --project=%s --account='%s'", project, account), fmt.Sprintf("gcloud services list --enabled --project=%s --account='%s' --filter='config.name:drive.googleapis.com' --format='value(config.name)'", project, account)}
	}
	steps = append(steps, SetupStep{ID: "api", Title: "Enable Google Drive API", Detail: "Run the enable command only after verifying account and project. Confirm Drive API is listed enabled; an unchecked box is not provider proof.", Commands: commands, URL: ServicesDoc, Complete: complete("api")})
	steps = append(steps, SetupStep{ID: "consent", Title: "Configure Google Auth platform", Detail: "In Google Cloud console configure Branding, Audience and Data Access. Request drive.file by default; all-existing-folder access additionally needs explicit drive scope. Add intended test users while testing and review Google's scope verification requirements.", URL: ConsentDoc, Complete: complete("consent")}, SetupStep{ID: "clients", Title: "Create Google OAuth clients", Detail: "Use Google Cloud console Google Auth platform > Clients. Create Desktop app client for daemon PKCE/loopback; Web application client for browser GIS and register exact authorized origins. Copy public client IDs here. This guide provides no supported gcloud command for Google Workspace OAuth-client creation; IAP/IAM OAuth clients are different products. Keep any Desktop client secret on each consenting client, never workspace settings.", URL: CredentialsDoc, Complete: complete("clients")}, SetupStep{ID: "enable", Title: "Enable optional Google Drive", Detail: "Admin saves only project/public client IDs/origins. Every person separately connects their own Google account. Native AgentNet encrypted attachments remain available even with Drive off. Checklist marks are admin confirmations, not an automatic live Google verification.", Complete: complete("enable")})
	return steps, nil
}

type StorageSettings = drivecontract.StorageSettings
type StorageUpdate = drivecontract.StorageUpdate
