package gdrive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/misunders2d/agentnet/internal/drivecontract"
	"golang.org/x/oauth2"
	drive "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const FileScope = "https://www.googleapis.com/auth/drive.file"
const FullScope = "https://www.googleapis.com/auth/drive"
const FolderMIME = "application/vnd.google-apps.folder"
const OutsideE2EE = "Google Drive files are outside AgentNet end-to-end encryption. Google permissions apply independently."

func ValidID(id string) bool { return drivecontract.ValidID(id) }

var ErrConsent = errors.New("Google consent expired or revoked; connect Google again")
var ErrDenied = errors.New("Google access denied; check account, consent scope and folder permissions")
var ErrOffline = errors.New("Google Drive unavailable; retry when online")
var ErrCapability = errors.New("Google folder permissions do not allow this action")

type Capabilities struct {
	AddChildren  bool `json:"canAddChildren"`
	Share        bool `json:"canShare"`
	Download     bool `json:"canDownload"`
	ListChildren bool `json:"canListChildren"`
}
type File struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	MIME         string       `json:"mimeType"`
	Size         string       `json:"size,omitempty"`
	Parents      []string     `json:"parents,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Trashed      bool         `json:"trashed,omitempty"`
}

func (f File) URL() string {
	if f.MIME == FolderMIME {
		return "https://drive.google.com/drive/folders/" + f.ID
	}
	return "https://drive.google.com/file/d/" + f.ID + "/view"
}

type Page struct {
	Files      []File `json:"files"`
	Next       string `json:"nextPageToken,omitempty"`
	Incomplete bool   `json:"incompleteSearch"`
}
type Permission struct {
	ID      string             `json:"id"`
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Email   string             `json:"emailAddress,omitempty"`
	Details []PermissionDetail `json:"permissionDetails,omitempty"`
}
type PermissionDetail struct {
	Inherited bool   `json:"inherited"`
	From      string `json:"inheritedFrom,omitempty"`
}
type Client struct {
	HTTP      *http.Client
	Token     func(context.Context) (string, error)
	API       string
	UploadAPI string
}

func (c *Client) bases() (string, string) {
	a, u := c.API, c.UploadAPI
	if a == "" {
		a = "https://www.googleapis.com/drive/v3"
	}
	if u == "" {
		u = "https://www.googleapis.com/upload/drive/v3"
	}
	return strings.TrimRight(a, "/"), strings.TrimRight(u, "/")
}

type requestTokenSource struct {
	ctx context.Context
	get func(context.Context) (string, error)
}

func (s requestTokenSource) Token() (*oauth2.Token, error) {
	if s.get == nil {
		return nil, ErrConsent
	}
	t, e := s.get(s.ctx)
	if e != nil {
		return nil, e
	}
	if t == "" {
		return nil, ErrConsent
	}
	return &oauth2.Token{AccessToken: t, TokenType: "Bearer"}, nil
}

type uploadEndpointTransport struct {
	base     http.RoundTripper
	endpoint string
}

func (t uploadEndpointTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.Path, "/upload/drive/v3/") {
		copy := r.Clone(r.Context())
		u, e := url.Parse(t.endpoint + strings.TrimPrefix(r.URL.Path, "/upload/drive/v3"))
		if e != nil {
			return nil, e
		}
		u.RawQuery = r.URL.RawQuery
		copy.URL = u
		r = copy
	}
	return t.base.RoundTrip(r)
}
func (c *Client) service(ctx context.Context) (*drive.Service, error) {
	api, upload := c.bases()
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	safe := *hc
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	safe.Transport = &oauth2.Transport{Source: requestTokenSource{ctx, c.Token}, Base: uploadEndpointTransport{base, upload}}
	return drive.NewService(ctx, option.WithHTTPClient(&safe), option.WithEndpoint(api+"/"), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrConsent, ErrDenied, ErrOffline, ErrCapability} {
		if errors.Is(err, known) {
			return known
		}
	}
	var provider *googleapi.Error
	if errors.As(err, &provider) {
		switch provider.Code {
		case 401:
			return ErrConsent
		case 403, 404:
			return ErrDenied
		case 429, 500, 502, 503, 504:
			return ErrOffline
		}
		return fmt.Errorf("Google Drive request failed (HTTP %d)", provider.Code)
	}
	return ErrOffline
}

const fields = "id,name,mimeType,size,parents,trashed,capabilities(canAddChildren,canShare,canDownload,canListChildren)"

func localFile(f *drive.File) File {
	if f == nil {
		return File{}
	}
	out := File{ID: f.Id, Name: f.Name, MIME: f.MimeType, Parents: f.Parents, Trashed: f.Trashed}
	// Generated SDK intentionally normalizes missing and zero metadata size to unknown.
	if f.Size > 0 {
		out.Size = strconv.FormatInt(f.Size, 10)
	}
	if f.Capabilities != nil {
		out.Capabilities = Capabilities{AddChildren: f.Capabilities.CanAddChildren, Share: f.Capabilities.CanShare, Download: f.Capabilities.CanDownload, ListChildren: f.Capabilities.CanListChildren}
	}
	return out
}
func localPermission(p *drive.Permission) Permission {
	if p == nil {
		return Permission{}
	}
	out := Permission{ID: p.Id, Type: p.Type, Role: p.Role, Email: p.EmailAddress}
	for _, d := range p.PermissionDetails {
		out.Details = append(out.Details, PermissionDetail{Inherited: d.Inherited, From: d.InheritedFrom})
	}
	return out
}
func (c *Client) Get(ctx context.Context, id string) (File, error) {
	if !ValidID(id) {
		return File{}, errors.New("invalid Drive file id")
	}
	s, e := c.service(ctx)
	if e != nil {
		return File{}, safeError(e)
	}
	f, e := s.Files.Get(id).SupportsAllDrives(true).Fields(googleapi.Field(fields)).Context(ctx).Do()
	out := localFile(f)
	e = safeError(e)
	if e == nil && (!ValidID(out.ID) || out.Trashed) {
		e = ErrDenied
	}
	return out, e
}
func (c *Client) Folder(ctx context.Context, id string) (File, error) {
	f, e := c.Get(ctx, id)
	if e == nil && f.MIME != FolderMIME {
		e = errors.New("selected Drive item is not a folder")
	}
	return f, e
}
func (c *Client) CreateFolder(ctx context.Context, name string) (File, error) {
	if strings.TrimSpace(name) == "" || len(name) > 255 {
		return File{}, errors.New("folder name required (at most 255 bytes)")
	}
	s, e := c.service(ctx)
	if e != nil {
		return File{}, safeError(e)
	}
	f, e := s.Files.Create(&drive.File{Name: name, MimeType: FolderMIME}).SupportsAllDrives(true).Fields(googleapi.Field(fields)).Context(ctx).Do()
	return localFile(f), safeError(e)
}
func (c *Client) List(ctx context.Context, folder, page string) (Page, error) {
	f, e := c.Folder(ctx, folder)
	if e != nil {
		return Page{}, e
	}
	if !f.Capabilities.ListChildren {
		return Page{}, ErrCapability
	}
	if len(page) > 2048 {
		return Page{}, errors.New("invalid page token")
	}
	s, e := c.service(ctx)
	if e != nil {
		return Page{}, safeError(e)
	}
	p, e := s.Files.List().Q("'" + folder + "' in parents and trashed = false").Fields(googleapi.Field("nextPageToken,incompleteSearch,files(" + fields + ")")).PageSize(100).SupportsAllDrives(true).IncludeItemsFromAllDrives(true).PageToken(page).Context(ctx).Do()
	var out Page
	if p != nil {
		if p.Files != nil {
			out.Files = make([]File, 0, len(p.Files))
		}
		out.Next = p.NextPageToken
		out.Incomplete = p.IncompleteSearch
		for _, f := range p.Files {
			out.Files = append(out.Files, localFile(f))
		}
	}
	return out, safeError(e)
}
func (c *Client) Upload(ctx context.Context, folder, name, mime string, data io.Reader, size int64) (File, error) {
	if size < 0 || size > 32<<20 || strings.TrimSpace(name) == "" || len(name) > 255 {
		return File{}, errors.New("Drive upload requires a name and at most 32 MiB")
	}
	f, e := c.Folder(ctx, folder)
	if e != nil {
		return File{}, e
	}
	if !f.Capabilities.AddChildren {
		return File{}, ErrCapability
	}
	b, e := io.ReadAll(io.LimitReader(data, size+1))
	if e != nil {
		return File{}, errors.New("cannot read upload")
	}
	if int64(len(b)) != size {
		return File{}, errors.New("upload size changed")
	}
	s, e := c.service(ctx)
	if e != nil {
		return File{}, safeError(e)
	}
	uploaded, e := s.Files.Create(&drive.File{Name: name, Parents: []string{folder}}).Media(bytes.NewReader(b), googleapi.ChunkSize(0), googleapi.ContentType("application/octet-stream")).SupportsAllDrives(true).Fields(googleapi.Field(fields)).Context(ctx).Do()
	return localFile(uploaded), safeError(e)
}
func (c *Client) Share(ctx context.Context, folder, email, role string) (Permission, error) {
	f, e := c.Folder(ctx, folder)
	if e != nil {
		return Permission{}, e
	}
	if !f.Capabilities.Share {
		return Permission{}, ErrCapability
	}
	if role != "reader" && role != "writer" {
		return Permission{}, errors.New("permission must be reader or writer")
	}
	if !strings.Contains(email, "@") || len(email) > 320 || strings.ContainsAny(email, "\r\n") {
		return Permission{}, errors.New("explicit Google email required")
	}
	s, e := c.service(ctx)
	if e != nil {
		return Permission{}, safeError(e)
	}
	p, e := s.Permissions.Create(folder, &drive.Permission{Type: "user", Role: role, EmailAddress: email}).SupportsAllDrives(true).SendNotificationEmail(true).Fields("id,type,role,emailAddress").Context(ctx).Do()
	return localPermission(p), safeError(e)
}
func (c *Client) Permissions(ctx context.Context, folder, page string) ([]Permission, string, error) {
	if !ValidID(folder) || len(page) > 2048 {
		return nil, "", errors.New("invalid Drive id or page")
	}
	s, e := c.service(ctx)
	if e != nil {
		return nil, "", safeError(e)
	}
	p, e := s.Permissions.List(folder).SupportsAllDrives(true).Fields("nextPageToken,permissions(id,type,role,emailAddress,permissionDetails)").PageToken(page).Context(ctx).Do()
	var out []Permission
	next := ""
	if p != nil {
		if p.Permissions != nil {
			out = make([]Permission, 0, len(p.Permissions))
		}
		next = p.NextPageToken
		for _, p := range p.Permissions {
			out = append(out, localPermission(p))
		}
	}
	return out, next, safeError(e)
}
func (c *Client) RemovePermission(ctx context.Context, folder, id string) error {
	f, e := c.Folder(ctx, folder)
	if e != nil {
		return e
	}
	if !f.Capabilities.Share {
		return ErrCapability
	}
	if !ValidID(id) {
		return errors.New("invalid permission id")
	}
	s, e := c.service(ctx)
	if e != nil {
		return safeError(e)
	}
	return safeError(s.Permissions.Delete(folder, id).SupportsAllDrives(true).Context(ctx).Do())
}

type Account struct {
	PermissionID string `json:"permissionId"`
	Email        string `json:"emailAddress"`
	Name         string `json:"displayName"`
}

func (c *Client) Account(ctx context.Context) (Account, error) {
	s, e := c.service(ctx)
	if e != nil {
		return Account{}, safeError(e)
	}
	a, e := s.About.Get().Fields("user(permissionId,emailAddress,displayName)").Context(ctx).Do()
	var out Account
	if a != nil && a.User != nil {
		out = Account{PermissionID: a.User.PermissionId, Email: a.User.EmailAddress, Name: a.User.DisplayName}
	}
	e = safeError(e)
	if e == nil && out.PermissionID == "" {
		e = ErrConsent
	}
	return out, e
}
