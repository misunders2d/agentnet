package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/gdrive"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// DriveMetadataPublisher is implemented by the conversation-control owner.
// It queues authenticated E2EE metadata to current members and linked devices.
// A missing transport hook refuses create/connect before any Google mutation.
type DriveMetadataPublisher interface {
	PublishDriveSpace(context.Context, gdrive.Space) error
}
type DriveRequest struct {
	Dir            string `json:"dir,omitempty"`
	brokerPID      string // set only by admitted-job broker, never JSON
	Conv           string `json:"conv"`
	Action         string `json:"action"`
	Name           string `json:"name,omitempty"`
	Folder         string `json:"folder,omitempty"`
	Page           string `json:"page,omitempty"`
	Full           bool   `json:"full,omitempty"`
	ConfirmOutside bool   `json:"confirm_outside_e2ee,omitempty"`
	ConfirmAccount bool   `json:"confirm_account,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	ClientSecret   string `json:"client_secret,omitempty"`
	Email          string `json:"email,omitempty"`
	Role           string `json:"role,omitempty"`
	Permission     string `json:"permission,omitempty"`
	ConfirmAccess  bool   `json:"confirm_access,omitempty"`
	PID            string `json:"pid,omitempty"`
	Grant          string `json:"grant,omitempty"`
	Message        string `json:"message,omitempty"`
	Index          int    `json:"index,omitempty"`
}
type DriveView struct {
	Enabled     bool                `json:"enabled"`
	Pending     *gdrive.Space       `json:"pending,omitempty"`
	Configured  bool                `json:"configured"`
	Connected   bool                `json:"connected"`
	Full        bool                `json:"full"`
	Space       *gdrive.Space       `json:"space,omitempty"`
	Folder      *gdrive.File        `json:"folder,omitempty"`
	Page        *gdrive.Page        `json:"page,omitempty"`
	File        *gdrive.File        `json:"file,omitempty"`
	Permissions []gdrive.Permission `json:"permissions,omitempty"`
	Next        string              `json:"next,omitempty"`
	ConsentURL  string              `json:"consent_url,omitempty"`
	Account     string              `json:"account,omitempty"`
	Grants      map[string]string   `json:"grants,omitempty"`
	Notice      string              `json:"notice"`
}
type driveState struct {
	Setup    *gdrive.SetupDraft           `json:"setup,omitempty"`
	Pending  map[string]gdrive.Space      `json:"pending,omitempty"`
	Consent  string                       `json:"consent,omitempty"`
	ClientID string                       `json:"client_id"`
	Secret   string                       `json:"secret,omitempty"`
	Token    gdrive.Token                 `json:"token"`
	Account  string                       `json:"account,omitempty"`
	Spaces   map[string]gdrive.Space      `json:"spaces"`
	Grants   map[string]map[string]string `json:"grants"`
}

var driveLocks sync.Map

func (a *Agent) driveLock() *sync.Mutex {
	p := filepath.Join(a.home, "google-drive.age")
	m, _ := driveLocks.LoadOrStore(p, &sync.Mutex{})
	return m.(*sync.Mutex)
}
func (a *Agent) readDrive() (driveState, error) {
	s := driveState{Pending: map[string]gdrive.Space{}, Spaces: map[string]gdrive.Space{}, Grants: map[string]map[string]string{}}
	b, e := secfile.Read(filepath.Join(a.home, "google-drive.age"))
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	r, e := age.Decrypt(bytes.NewReader(b), a.id.Box)
	if e != nil {
		return s, errors.New("cannot decrypt local Google state")
	}
	if json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&s) != nil {
		return s, errors.New("invalid local Google state")
	}
	if s.Pending == nil {
		s.Pending = map[string]gdrive.Space{}
	}
	if s.Spaces == nil {
		s.Spaces = map[string]gdrive.Space{}
	}
	if s.Grants == nil {
		s.Grants = map[string]map[string]string{}
	}
	return s, nil
}
func (a *Agent) writeDrive(s driveState) error {
	var b bytes.Buffer
	w, e := age.Encrypt(&b, a.id.Box.Recipient())
	if e != nil {
		return e
	}
	if e = json.NewEncoder(w).Encode(s); e != nil {
		return e
	}
	if e = w.Close(); e != nil {
		return e
	}
	return secfile.Write(filepath.Join(a.home, "google-drive.age"), b.Bytes())
}
func (a *Agent) driveOwner(conv string) (string, error) {
	m, e := a.dmMembers(conv)
	if e != nil {
		return "", e
	}
	me, ok, e := a.store.selfPerson(a.Address)
	if e != nil || !ok {
		return "", errors.New("a local person must join this conversation")
	}
	if _, ok = m.persons[me.info.Person]; !ok {
		return "", errors.New("current person is not a verified conversation member")
	}
	return me.info.Person, nil
}

// AdmitDriveSpace is called ONLY after the envelope/control owner verifies the
// actual signing key and resolves its current member person into author. It
// never trusts Owner supplied in the payload. Existing ownership is immutable.
func (a *Agent) AdmitDriveSpace(author string, s gdrive.Space) error {
	if e := s.Validate(); e != nil {
		return e
	}
	m, e := a.dmMembers(s.Conv)
	if e != nil {
		return e
	}
	if _, ok := m.persons[author]; !ok || s.Owner != author {
		return errors.New("Drive metadata author is not its verified member owner")
	}
	mu := a.driveLock()
	mu.Lock()
	defer mu.Unlock()
	st, e := a.readDrive()
	if e != nil {
		return e
	}
	old, ok := st.Spaces[s.Conv]
	if ok {
		if old.Owner != author {
			return errors.New("only the Drive space owner may change its metadata")
		}
		if s.Revision <= old.Revision {
			if s == old {
				return nil
			}
			return errors.New("stale or conflicting Drive metadata")
		}
		if s.Revision != old.Revision+1 || s.Previous != old.Folder {
			return errors.New("Drive metadata missing predecessor")
		}
	} else if s.Revision != 1 || s.Previous != "" {
		return errors.New("Drive metadata missing initial revision")
	}
	st.Spaces[s.Conv] = s
	delete(st.Grants, s.Conv)
	if e = a.writeDrive(st); e != nil {
		return e
	}
	a.NoteChange()
	a.convWork.due(convRetry)
	a.kickNow()
	return nil
}

// DriveCommand is client-local. Tokens and direct Google access are never
// exposed through conversation records or the public profile/directory.
func (a *Agent) DriveCommand(ctx context.Context, r DriveRequest) (DriveView, error) {
	return a.driveCommand(ctx, r, nil, nil)
}
func (a *Agent) driveCommand(ctx context.Context, r DriveRequest, fixtureOAuth *gdrive.OAuthConfig, fixtureDrive *gdrive.Client) (DriveView, error) {
	return a.driveCommandWithPublisher(ctx, r, fixtureOAuth, fixtureDrive, nil)
}
func (a *Agent) driveCommandWithPublisher(ctx context.Context, r DriveRequest, fixtureOAuth *gdrive.OAuthConfig, fixtureDrive *gdrive.Client, fixturePublisher DriveMetadataPublisher) (DriveView, error) {
	v := DriveView{Notice: gdrive.OutsideE2EE}
	owner, e := a.driveOwner(r.Conv)
	if e != nil {
		return v, e
	}
	var workspace gdrive.StorageConfig
	if fixtureDrive == nil && fixtureOAuth == nil {
		settings, err := a.DriveStorageConfig(ctx)
		if err != nil && r.Action != "disconnect-account" {
			return v, err
		}
		workspace = settings.Config
		v.Enabled = workspace.Enabled
		if !workspace.Enabled && r.Action != "disconnect-account" {
			v.Notice = "Google Drive is off for this workspace. Admin may enable it in Settings > File storage options. Native encrypted attachments remain available."
			if r.Action == "" || r.Action == "status" {
				return v, nil
			}
			return v, errors.New("optional Google Drive is disabled by workspace admin")
		}
	} else {
		v.Enabled = true
	}
	mu := a.driveLock()
	mu.Lock()
	defer mu.Unlock()
	s, e := a.readDrive()
	if e != nil {
		return v, e
	}
	if fixtureDrive == nil && fixtureOAuth == nil && r.Action != "disconnect-account" {
		if workspace.DesktopClientID == "" {
			return v, errors.New("workspace admin has not configured a Desktop OAuth client")
		}
		if s.Token.Access != "" && s.ClientID != workspace.DesktopClientID {
			return v, errors.New("workspace Google OAuth client changed; disconnect this client's Google account and explicitly reconnect")
		}
		if r.Action == "configure" && r.ClientID != workspace.DesktopClientID {
			return v, errors.New("use workspace admin's registered Desktop client ID")
		}
		if s.Token.Access == "" && s.ClientID != workspace.DesktopClientID {
			s.ClientID = workspace.DesktopClientID
			s.Secret = ""
			if e = a.writeDrive(s); e != nil {
				return v, e
			}
		}
	}
	o := gdrive.OAuthConfig{ClientID: s.ClientID, ClientSecret: s.Secret}
	if fixtureOAuth != nil {
		o = *fixtureOAuth
	}
	d := &gdrive.Client{}
	if fixtureDrive != nil {
		copy := *fixtureDrive
		d = &copy
	}
	d.Token = func(ctx context.Context) (string, error) {
		if s.Token.Access == "" {
			return "", gdrive.ErrConsent
		}
		if time.Until(s.Token.Expiry) < time.Minute {
			t, e := o.Refresh(ctx, s.Token)
			if e != nil {
				return "", e
			}
			s.Token = t
			if e = a.writeDrive(s); e != nil {
				return "", e
			}
		}
		return s.Token.Access, nil
	}
	refresh := func() {
		v.Configured = s.ClientID != ""
		v.Connected = s.Token.Access != ""
		v.Full = s.Token.Has(gdrive.FullScope)
		v.Account = s.Account
		v.Grants = s.Grants[r.Conv]
		v.Pending = nil
		if pending, ok := s.Pending[r.Conv]; ok {
			v.Pending = &pending
		}
		if sp, ok := s.Spaces[r.Conv]; ok && !sp.Disconnected {
			v.Space = &sp
		}
	}
	refresh()
	if r.brokerPID != "" {
		p, err := a.Participation(r.brokerPID)
		g := s.Grants[r.Conv][r.brokerPID]
		if err != nil || p.Conv != r.Conv || !p.Claimable() || !p.HostHere || v.Space == nil || (g != "read" && g != "write") {
			return v, errors.New("agent has no Drive read grant")
		}
		if r.Action != "list" {
			return v, errors.New("broker action not allowed")
		}
	}
	if r.Action == "" || r.Action == "status" {
		return v, nil
	}
	switch r.Action {
	case "configure":
		if r.ClientID == "" || len(r.ClientID) > 512 || len(r.ClientSecret) > 512 {
			return v, errors.New("Google Desktop OAuth client ID required")
		}
		if s.Token.Access != "" {
			return v, errors.New("disconnect Google before changing OAuth client")
		}
		s.ClientID = r.ClientID
		s.Secret = r.ClientSecret
		e = a.writeDrive(s)
	case "consent":
		if !r.ConfirmAccount {
			return v, errors.New("confirm connecting your Google account on this device")
		}
		nonce := time.Now().UTC().Format(time.RFC3339Nano)
		s.Consent = nonce
		if e = a.writeDrive(s); e != nil {
			return v, e
		}
		url, e := o.Consent(context.Background(), r.Full, func(t gdrive.Token) error {
			mu.Lock()
			defer mu.Unlock()
			fresh, e := a.readDrive()
			if e != nil {
				return e
			}
			if fresh.ClientID != s.ClientID || fresh.Consent != nonce {
				return errors.New("OAuth setup changed")
			}
			accountClient := *d
			accountClient.Token = func(context.Context) (string, error) { return t.Access, nil }
			account, e := accountClient.Account(context.Background())
			if e != nil {
				return e
			}
			fresh.Grants = map[string]map[string]string{}
			fresh.Token = t
			fresh.Consent = ""
			fresh.Account = account.Email
			if fresh.Account == "" {
				fresh.Account = account.Name
			}
			return a.writeDrive(fresh)
		})
		if e != nil {
			return v, e
		}
		v.ConsentURL = url
		return v, nil
	case "disconnect-account":
		t := s.Token
		s.Consent = ""
		s.Token = gdrive.Token{}
		s.Account = ""
		s.Grants = map[string]map[string]string{}
		if e = a.writeDrive(s); e != nil {
			return v, e
		}
		refresh()
		if e = o.Revoke(ctx, t); e != nil {
			return v, errors.New("local Google credentials removed; provider revocation unconfirmed, revoke AgentNet in Google account settings")
		}
		return v, nil
	case "create", "connect", "disconnect-space", "publish-pending":
		pub, ok := any(a).(DriveMetadataPublisher)
		if fixturePublisher != nil {
			pub = fixturePublisher
			ok = true
		}
		if !ok {
			return v, errors.New("encrypted conversation Drive transport not installed")
		}
		if existing, ok := s.Spaces[r.Conv]; ok && existing.Owner != owner {
			return v, errors.New("only this conversation space owner may change its folder")
		}
		if !r.ConfirmOutside {
			return v, errors.New("confirm Google Drive storage outside AgentNet E2EE")
		}
		var f gdrive.File
		if pending, ok := s.Pending[r.Conv]; ok && r.Action != "publish-pending" {
			v.Pending = &pending
			v.Notice += " Folder operation already succeeded; retry sharing metadata for this saved folder instead of creating another."
			return v, nil
		}
		if r.Action == "publish-pending" {
			pending, ok := s.Pending[r.Conv]
			if !ok || pending.Owner != owner {
				return v, errors.New("no pending folder publication owned here")
			}
			f = gdrive.File{ID: pending.Folder, Name: pending.Name}
		} else if r.Action == "disconnect-space" {
			if v.Space == nil {
				return v, errors.New("no conversation space")
			}
			f = gdrive.File{ID: v.Space.Folder, Name: v.Space.Name}
		} else if r.Action == "create" {
			f, e = d.CreateFolder(ctx, r.Name)
		} else {
			if !s.Token.Has(gdrive.FullScope) {
				return v, errors.New("connecting an existing folder requires explicit full-folder Google consent; drive.file cannot promise existing children visibility")
			}
			f, e = d.Folder(ctx, r.Folder)
		}
		if e != nil {
			return v, e
		}
		sp := gdrive.Space{Conv: r.Conv, Folder: f.ID, Name: f.Name, Owner: owner, Revision: 1, Disconnected: r.Action == "disconnect-space"}
		if old, ok := s.Spaces[r.Conv]; ok {
			sp.Revision = old.Revision + 1
			sp.Previous = old.Folder
		}
		if r.Action == "publish-pending" {
			sp = s.Pending[r.Conv]
		}
		s.Pending[r.Conv] = sp
		v.Pending = &sp
		if e = a.writeDrive(s); e != nil {
			v.Notice += " Google folder exists, but local recovery could not be saved. Keep its folder ID shown here; do not create again."
			return v, nil
		}
		if e = pub.PublishDriveSpace(ctx, sp); e != nil {
			v.Notice += " Google folder operation succeeded. Sharing with this conversation is unconfirmed. Retry publication for the saved folder; do not create another."
			return v, nil
		}
		delete(s.Pending, r.Conv)
		s.Spaces[r.Conv] = sp
		delete(s.Grants, r.Conv)
		e = a.writeDrive(s)
	case "list":
		if v.Space == nil {
			return v, errors.New("connect a conversation Drive folder first")
		}
		f, err := d.Folder(ctx, v.Space.Folder)
		if err != nil {
			return v, err
		}
		v.Folder = &f
		p, err := d.List(ctx, f.ID, r.Page)
		if err != nil {
			return v, err
		}
		v.Page = &p
		return v, nil
	case "permissions", "share", "remove-permission":
		if v.Space == nil {
			return v, errors.New("connect a conversation Drive folder first")
		}
		if v.Space.Owner != owner {
			return v, errors.New("only conversation space owner manages access here")
		}
		if r.Action == "permissions" {
			v.Permissions, v.Next, e = d.Permissions(ctx, v.Space.Folder, r.Page)
			return v, e
		}
		if !r.ConfirmAccess {
			return v, errors.New("explicit Google permission change confirmation required")
		}
		if r.Action == "share" {
			_, e = d.Share(ctx, v.Space.Folder, r.Email, r.Role)
		} else {
			e = d.RemovePermission(ctx, v.Space.Folder, r.Permission)
		}
		v.Notice += " Removing a direct folder grant does not remove inherited access or independent child-file grants. Conversation joins/leaves change no Google permissions."
	case "grant":
		if v.Space == nil {
			return v, errors.New("connect a Drive space before granting agent access")
		}
		if r.Grant != "none" && r.Grant != "read" && r.Grant != "write" {
			return v, errors.New("agent space grant must be none, read or write")
		}
		p, e := a.Participation(r.PID)
		if e != nil || p.Conv != r.Conv || !p.Claimable() || !p.HostHere {
			return v, errors.New("space grants require an active verified agent participation hosted here")
		}
		if s.Grants[r.Conv] == nil {
			s.Grants[r.Conv] = map[string]string{}
		}
		s.Grants[r.Conv][r.PID] = r.Grant
		e = a.writeDrive(s)
		v.Notice += " Grant controls only AgentNet's Drive broker, not independent Google tools already allowed in the harness."
	default:
		return v, errors.New("unknown Drive action")
	}
	refresh()
	return v, e
}

// CheckDriveGrant is the worker broker gate. A participation is never selected
// by a received name or request; the worker binds its own admitted PID first.
func (a *Agent) CheckDriveGrant(conv, pid string, write bool) error {
	p, e := a.Participation(pid)
	if e != nil || p.Conv != conv || !p.Claimable() || !p.HostHere {
		return errors.New("agent participation cannot access this Drive space")
	}
	mu := a.driveLock()
	mu.Lock()
	defer mu.Unlock()
	s, e := a.readDrive()
	if e != nil {
		return e
	}
	sp, ok := s.Spaces[conv]
	if !ok || sp.Disconnected {
		return errors.New("no active Drive space")
	}
	g := s.Grants[conv][pid]
	if g != "write" && (write || g != "read") {
		return errors.New("agent has no required Drive space grant")
	}
	return nil
}

// DriveUpload is called only by the authenticated human UI or by a broker that
// first binds and checks its admitted participation. It never grants an agent.
func (a *Agent) DriveUpload(ctx context.Context, conv, name string, data io.Reader, size int64, confirm bool) (gdrive.File, error) {
	return a.driveUpload(ctx, conv, name, data, size, confirm, "")
}
func (a *Agent) driveUpload(ctx context.Context, conv, name string, data io.Reader, size int64, confirm bool, pid string) (gdrive.File, error) {
	if !confirm {
		return gdrive.File{}, errors.New("confirm uploading plaintext outside AgentNet E2EE")
	}
	if _, e := a.driveOwner(conv); e != nil {
		return gdrive.File{}, e
	}
	settings, e := a.DriveStorageConfig(ctx)
	if e != nil {
		return gdrive.File{}, e
	}
	if !settings.Config.Enabled {
		return gdrive.File{}, errors.New("optional Google Drive is disabled by workspace admin")
	}
	mu := a.driveLock()
	mu.Lock()
	defer mu.Unlock()
	s, e := a.readDrive()
	if e != nil {
		return gdrive.File{}, e
	}
	if s.ClientID != settings.Config.DesktopClientID {
		return gdrive.File{}, errors.New("workspace Google client changed; reconnect explicitly")
	}
	sp, ok := s.Spaces[conv]
	if !ok || sp.Disconnected {
		return gdrive.File{}, errors.New("no active Drive space")
	}
	if pid != "" {
		p, e := a.Participation(pid)
		if e != nil || p.Conv != conv || !p.Claimable() || !p.HostHere || s.Grants[conv][pid] != "write" {
			return gdrive.File{}, errors.New("agent has no Drive write grant")
		}
	}
	o := gdrive.OAuthConfig{ClientID: s.ClientID, ClientSecret: s.Secret}
	d := gdrive.Client{Token: func(ctx context.Context) (string, error) {
		if time.Until(s.Token.Expiry) < time.Minute {
			t, e := o.Refresh(ctx, s.Token)
			if e != nil {
				return "", e
			}
			s.Token = t
			if e = a.writeDrive(s); e != nil {
				return "", e
			}
		}
		return s.Token.Access, nil
	}}
	return d.Upload(ctx, sp.Folder, name, "", data, size)
}

// DriveSaveAttachment validates the exact conversation/direction before opening
// its signed, digest-checked native attachment. Upload remains explicit plaintext.
func (a *Agent) DriveSaveAttachment(ctx context.Context, conv, dir, id string, index int, confirm bool) (gdrive.File, error) {
	if !confirm {
		return gdrive.File{}, errors.New("confirm saving attachment outside AgentNet E2EE")
	}
	msgs, e := a.ConversationMessages(conv)
	if e != nil {
		return gdrive.File{}, e
	}
	found := false
	for _, m := range msgs {
		if m.ID == id && m.Dir == dir && !m.Deleted && index >= 0 && index < len(m.Attachments) {
			found = true
			break
		}
	}
	if !found {
		return gdrive.File{}, errors.New("attachment is not in this conversation and direction")
	}
	r, f, e := a.OpenFileFrom(ctx, dir, id, index)
	if e != nil {
		return gdrive.File{}, errors.New("attachment unavailable here; download/request it first")
	}
	defer r.Close()
	return a.DriveUpload(ctx, conv, f.Name, r, f.Size, true)
}
