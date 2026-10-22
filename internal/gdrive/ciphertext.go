package gdrive

// This path stores already encrypted relay blobs. It deliberately does not use
// Upload's bounded, user-facing plaintext project-file implementation.
import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	drive "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const cipherFields = "id,name,size,sha256Checksum,appProperties,trashed,parents,mimeType"

var ErrMissing = errors.New("Google Drive attachment is missing")
var ErrCiphertext = errors.New("Google Drive attachment metadata or checksum does not match")
var ErrChecksumUnavailable = errors.New("Google Drive has not supplied the attachment SHA-256 checksum")
var ErrStorageFull = errors.New("Google Drive storage is full; free space and retry Move existing")

var cipherDefaultTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = time.Minute
	return t
}()

// Drive uses HTTP 403 for both transient limits and actual permission errors.
// Keep this distinction specific to the relay ciphertext path.
func cipherError(err error) error {
	for _, known := range []error{ErrMissing, ErrCiphertext, ErrChecksumUnavailable, ErrStorageFull} {
		if errors.Is(err, known) {
			return known
		}
	}
	var provider *googleapi.Error
	if errors.As(err, &provider) && provider.Code == 403 {
		for _, detail := range provider.Errors {
			if detail.Reason == "storageQuotaExceeded" {
				return ErrStorageFull
			}
		}
		for _, detail := range provider.Errors {
			if detail.Reason == "rateLimitExceeded" || detail.Reason == "userRateLimitExceeded" {
				return ErrOffline
			}
		}
	}
	return safeError(err)
}

// CiphertextDownload binds range reads to one validated immutable relay object.
// Recipients still check the SHA-256 of their complete downloaded ciphertext.
type CiphertextDownload struct {
	client *Client
	id     string
	size   int64
}

func (c *Client) CiphertextDownload(ctx context.Context, id, realm, blob, sha string, size int64) (*CiphertextDownload, error) {
	f, err := c.cipherMeta(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = checkCipherMeta(f, realm, blob, sha, size); err != nil {
		return nil, err
	}
	return &CiphertextDownload{client: c, id: id, size: size}, nil
}
func (d *CiphertextDownload) Range(ctx context.Context, start, end int64) (io.ReadCloser, error) {
	if start < 0 || end < start || end >= d.size {
		return nil, ErrCiphertext
	}
	s, err := d.client.cipherService(ctx)
	if err != nil {
		return nil, cipherError(err)
	}
	call := s.Files.Get(d.id).Context(ctx)
	call.Header().Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	response, err := call.Download()
	if err != nil {
		return nil, cipherError(err)
	}
	want := end - start + 1
	full := start == 0 && end == d.size-1
	valid := response.StatusCode == 206 && response.Header.Get("Content-Range") == fmt.Sprintf("bytes %d-%d/%d", start, end, d.size)
	if full && response.StatusCode == 200 {
		valid = true
	}
	if !valid || response.ContentLength >= 0 && response.ContentLength != want {
		response.Body.Close()
		return nil, ErrCiphertext
	}
	return response.Body, nil
}

type cipherOriginTransport struct {
	base    http.RoundTripper
	origins map[string]bool
}

func (t cipherOriginTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !t.origins[r.URL.Scheme+"://"+r.URL.Host] {
		return nil, ErrDenied
	}
	return t.base.RoundTrip(r)
}
func (c *Client) cipherService(ctx context.Context) (*drive.Service, error) {
	copy := *c
	hc := &http.Client{}
	if c.HTTP != nil {
		*hc = *c.HTTP
	}
	// A whole-response timeout truncates slow but progressing ciphertext
	// streams. Bound connection/response headers instead; bodies remain tied
	// to request cancellation, and resumable uploads have a per-chunk bound.
	hc.Timeout = 0
	base := hc.Transport
	if base == nil {
		base = cipherDefaultTransport
	} else if transport, ok := base.(*http.Transport); ok {
		bounded := transport.Clone()
		bounded.ResponseHeaderTimeout = time.Minute
		base = bounded
	}
	api, upload := c.bases()
	origins := map[string]bool{}
	for _, endpoint := range []string{api, upload} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" {
			return nil, ErrDenied
		}
		origins[u.Scheme+"://"+u.Host] = true
	}
	hc.Transport = cipherOriginTransport{base, origins}
	copy.HTTP = hc
	return copy.service(ctx)
}

// GenerateID reserves an immutable provider ID before a create request. Saving
// it locally first makes a retried or crash-interrupted create idempotent.
func (c *Client) GenerateID(ctx context.Context) (string, error) {
	s, err := c.cipherService(ctx)
	if err != nil {
		return "", cipherError(err)
	}
	v, err := s.Files.GenerateIds().Count(1).Space("drive").Type("files").Context(ctx).Do()
	if err != nil {
		return "", cipherError(err)
	}
	if len(v.Ids) != 1 || !ValidID(v.Ids[0]) {
		return "", ErrCiphertext
	}
	return v.Ids[0], nil
}

func cipherTags(realm, blob, sha string) map[string]string {
	return map[string]string{"agentnet_realm": realm, "agentnet_blob": blob, "agentnet_sha256": sha}
}

func (c *Client) cipherMeta(ctx context.Context, id string) (*drive.File, error) {
	if !ValidID(id) {
		return nil, ErrCiphertext
	}
	s, err := c.cipherService(ctx)
	if err != nil {
		return nil, cipherError(err)
	}
	f, err := s.Files.Get(id).Fields(cipherFields).Context(ctx).Do()
	var api *googleapi.Error
	if errors.As(err, &api) && api.Code == 404 {
		return nil, ErrMissing
	}
	if err == nil && (f == nil || f.Id != id) {
		return nil, ErrCiphertext
	}
	return f, cipherError(err)
}

func checkCipherMeta(f *drive.File, realm, blob, sha string, size int64) error {
	if f == nil || f.Trashed || f.Size != size || f.MimeType != "application/octet-stream" {
		return ErrCiphertext
	}
	for key, value := range cipherTags(realm, blob, sha) {
		if f.AppProperties[key] != value {
			return ErrCiphertext
		}
	}
	if f.Sha256Checksum == "" {
		return ErrChecksumUnavailable
	}
	if f.Sha256Checksum != sha {
		return ErrCiphertext
	}
	return nil
}

// PutCiphertext streams a resumable upload with at most one 8 MiB SDK chunk in
// memory. A prior completed create is reused only with matching immutable tags,
// size and provider checksum before the caller may drop its verified local copy.
func (c *Client) PutCiphertext(ctx context.Context, id, folder, realm, blob, sha string, size int64, data io.Reader) error {
	if !ValidID(id) || !ValidID(folder) || size <= 0 {
		return ErrCiphertext
	}
	f, err := c.cipherMeta(ctx, id)
	if err == nil {
		return checkCipherMeta(f, realm, blob, sha, size)
	}
	if !errors.Is(err, ErrMissing) {
		return err
	}
	s, err := c.cipherService(ctx)
	if err != nil {
		return cipherError(err)
	}
	_, err = s.Files.Create(&drive.File{Id: id, Name: blob + ".age", MimeType: "application/octet-stream", Parents: []string{folder}, AppProperties: cipherTags(realm, blob, sha)}).
		Media(io.LimitReader(data, size), googleapi.ChunkSize(8<<20), googleapi.ChunkTransferTimeout(5*time.Minute), googleapi.ChunkRetryDeadline(10*time.Minute), googleapi.ContentType("application/octet-stream")).Fields(cipherFields).Context(ctx).Do()
	if err != nil {
		// A response may be lost after creation; a stable ID prevents duplicates.
		var api *googleapi.Error
		if !errors.As(err, &api) || api.Code != 409 {
			return cipherError(err)
		}
	}
	f, err = c.cipherMeta(ctx, id)
	if err != nil {
		return err
	}
	return checkCipherMeta(f, realm, blob, sha, size)
}

func (c *Client) OpenCiphertext(ctx context.Context, id, realm, blob, sha string, size int64) (io.ReadCloser, error) {
	f, err := c.cipherMeta(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = checkCipherMeta(f, realm, blob, sha, size); err != nil {
		return nil, err
	}
	s, err := c.cipherService(ctx)
	if err != nil {
		return nil, cipherError(err)
	}
	response, err := s.Files.Get(id).Context(ctx).Download()
	if err != nil {
		return nil, cipherError(err)
	}
	if response.ContentLength >= 0 && response.ContentLength != size {
		response.Body.Close()
		return nil, ErrCiphertext
	}
	return response.Body, nil
}

func (c *Client) DeleteCiphertext(ctx context.Context, id, realm, blob, sha string, size int64) error {
	f, err := c.cipherMeta(ctx, id)
	if errors.Is(err, ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = checkCipherMeta(f, realm, blob, sha, size); err != nil {
		return err
	}
	s, err := c.cipherService(ctx)
	if err != nil {
		return cipherError(err)
	}
	err = s.Files.Delete(id).Context(ctx).Do()
	var api *googleapi.Error
	if errors.As(err, &api) && api.Code == 404 {
		return nil
	}
	return cipherError(err)
}

func (c *Client) EnsureCipherFolder(ctx context.Context, id, realm string) (File, error) {
	f, err := c.cipherMeta(ctx, id)
	if errors.Is(err, ErrMissing) {
		s, e := c.cipherService(ctx)
		if e != nil {
			return File{}, cipherError(e)
		}
		f, err = s.Files.Create(&drive.File{Id: id, Name: "AgentNet encrypted attachments", MimeType: FolderMIME,
			AppProperties: map[string]string{"agentnet_realm": realm, "agentnet_kind": "relay_ciphertext"}}).Fields(cipherFields).Context(ctx).Do()
		var api *googleapi.Error
		if errors.As(err, &api) && api.Code == 409 {
			f, err = c.cipherMeta(ctx, id)
		}
	}
	if err != nil {
		return File{}, cipherError(err)
	}
	if f == nil || f.Trashed || f.MimeType != FolderMIME || f.AppProperties["agentnet_realm"] != realm || f.AppProperties["agentnet_kind"] != "relay_ciphertext" || strings.TrimSpace(f.Id) != id {
		return File{}, ErrCiphertext
	}
	return localFile(f), nil
}
