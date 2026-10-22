package gdrive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

func TestCiphertextErrorReasonsRemainSeparate(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   error
	}{
		{"rateLimitExceeded", ErrOffline}, {"userRateLimitExceeded", ErrOffline},
		{"storageQuotaExceeded", ErrStorageFull}, {"insufficientFilePermissions", ErrDenied},
	} {
		err := &googleapi.Error{Code: 403, Message: "PRIVATE_PROVIDER_BODY", Errors: []googleapi.ErrorItem{{Reason: tc.reason}}}
		if got := cipherError(err); !errors.Is(got, tc.want) || strings.Contains(got.Error(), "PRIVATE_PROVIDER_BODY") {
			t.Fatalf("reason %s: %v, want %v", tc.reason, got, tc.want)
		}
		if !errors.Is(safeError(err), ErrDenied) {
			t.Fatal("plaintext error handling changed")
		}
	}
}

func TestCiphertextSlowStreamingAndRequestCancellation(t *testing.T) {
	const id, realm, blob = "stream-file", "realm", "blob"
	sha := sha256.Sum256([]byte("abc"))
	checksum := hex.EncodeToString(sha[:])
	var mu sync.Mutex
	block := false
	cancelObserved := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "media" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", "metadata-only-etag")
			json.NewEncoder(w).Encode(map[string]any{"id": id, "size": "3", "mimeType": "application/octet-stream", "sha256Checksum": checksum, "appProperties": cipherTags(realm, blob, checksum)})
			return
		}
		if r.Header.Get("If-Match") != "" {
			t.Error("metadata ETag was assumed to apply to media")
		}
		w.Header().Set("Content-Length", "3")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		mu.Lock()
		paused := block
		mu.Unlock()
		if paused {
			<-r.Context().Done()
			cancelObserved <- struct{}{}
			return
		}
		time.Sleep(120 * time.Millisecond)
		io.WriteString(w, "abc")
	}))
	defer server.Close()
	hc := server.Client()
	hc.Timeout = 20 * time.Millisecond
	c := &Client{API: server.URL, UploadAPI: server.URL, HTTP: hc, Token: func(context.Context) (string, error) { return "fixture-token", nil }}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	download, err := c.CiphertextDownload(ctx, id, realm, blob, checksum, 3)
	if err != nil {
		t.Fatal(err)
	}
	body, err := download.Range(ctx, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(got) != "abc" {
		t.Fatalf("whole-response timeout truncated a progressing stream: %q %v", got, err)
	}
	mu.Lock()
	block = true
	mu.Unlock()
	streamCtx, stop := context.WithCancel(t.Context())
	body, err = download.Range(streamCtx, 0, 2)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("request cancellation did not stop body read")
	}
	body.Close()
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("provider stream was not canceled")
	}
}

type repeatedCiphertext struct{ maxRead int }

func (r *repeatedCiphertext) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	for i := range p {
		p[i] = 0xa7
	}
	return len(p), nil
}

func TestCiphertextResumable100MiBAndStableID(t *testing.T) {
	const size = 100 << 20
	const realm, blob, id, folder = "fixture-realm", "fixture-blob", "reserved-file", "folder"
	hash := sha256.New()
	io.CopyN(hash, &repeatedCiphertext{}, size)
	sha := hex.EncodeToString(hash.Sum(nil))
	var mu sync.Mutex
	var meta map[string]any
	var received int64
	var creates, chunks int
	stored := false
	uploadHash := sha256.New()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-only" {
			t.Error("missing provider authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/files/"+id:
			if !stored {
				http.Error(w, `{"error":{"code":404}}`, 404)
				return
			}
			if r.URL.Query().Get("alt") == "media" {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(size))
				io.CopyN(w, &repeatedCiphertext{}, size)
				return
			}
			json.NewEncoder(w).Encode(meta)
		case r.Method == "POST" && r.URL.Path == "/files" && r.URL.Query().Get("uploadType") == "resumable":
			creates++
			if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
				t.Error(err)
			}
			if meta["id"] != id {
				t.Error("preallocated Drive ID not used")
			}
			w.Header().Set("Location", server.URL+"/session")
		case (r.Method == "PUT" || r.Method == "POST") && r.URL.Path == "/session":
			chunks++
			n, err := io.Copy(uploadHash, r.Body)
			if err != nil || n > 8<<20 {
				t.Errorf("unbounded or failed chunk: %d %v", n, err)
			}
			received += n
			total := strings.Split(r.Header.Get("Content-Range"), "/")
			if len(total) != 2 {
				t.Error("missing resumable range")
				w.WriteHeader(400)
				return
			}
			if total[1] == "*" {
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", received-1))
				// The pinned SDK requests Google's no-308 wire variant.
				if r.Header.Get("X-GUploader-No-308") != "yes" {
					t.Error("missing resumable SDK protocol header")
				}
				w.Header().Set("X-Http-Status-Code-Override", "308")
				w.WriteHeader(200)
				return
			}
			if received != size || hex.EncodeToString(uploadHash.Sum(nil)) != sha {
				t.Error("ciphertext changed in transit")
			}
			meta["size"], meta["sha256Checksum"] = strconv.FormatInt(received, 10), sha
			stored = true
			json.NewEncoder(w).Encode(meta)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := &Client{API: server.URL, UploadAPI: server.URL, Token: func(context.Context) (string, error) { return "fixture-only", nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	source := &repeatedCiphertext{}
	if err := client.PutCiphertext(ctx, id, folder, realm, blob, sha, size, source); err != nil {
		t.Fatal(err)
	}
	if source.maxRead > 8<<20 {
		t.Fatalf("upload buffered an unbounded read: %d", source.maxRead)
	}
	// Simulate a lost create response / process restart: retry the same saved ID.
	if err := client.PutCiphertext(ctx, id, folder, realm, blob, sha, size, source); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || chunks < 13 {
		t.Fatalf("create/chunk counts %d/%d", creates, chunks)
	}
	remote, err := client.OpenCiphertext(ctx, id, realm, blob, sha, size)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	readHash := sha256.New()
	n, err := io.Copy(readHash, remote)
	if err != nil || n != size || hex.EncodeToString(readHash.Sum(nil)) != sha {
		t.Fatalf("download differs: %d %v", n, err)
	}
	if _, err = client.OpenCiphertext(ctx, id, "different-realm", blob, sha, size); err == nil {
		t.Fatal("cross-workspace object accepted")
	}
}

func TestCiphertextResumableLocationCannotLeakBearer(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("bearer sent outside configured provider origin")
	}))
	defer destination.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			http.Error(w, `{"error":{"code":404}}`, 404)
			return
		}
		w.Header().Set("Location", destination.URL+"/capture")
	}))
	defer provider.Close()
	client := Client{API: provider.URL, UploadAPI: provider.URL, Token: func(context.Context) (string, error) { return "must-not-leak", nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.PutCiphertext(ctx, "id", "folder", "realm", "blob", strings.Repeat("a", 64), 9<<20, &repeatedCiphertext{})
	if err == nil || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("unsafe error: %v", err)
	}
}
