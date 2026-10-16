package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui/static"
)

type imageViewerFixture struct {
	*imageOpenFixture
	images [][]byte
	video  []byte
}

// Empty ancillary providers keep fixture startup free of expected 404s, so
// the browser check can reject every console error, including resource errors.
func (f *imageViewerFixture) AgentCatalog(_ context.Context, host string) (AgentCatalogView, error) {
	return AgentCatalogView{Host: host, Local: host == "", Agents: []CatalogAgent{}}, nil
}
func (f *imageViewerFixture) ChangeAgent(context.Context, AgentCatalogChange) (AgentCatalogChangeResult, error) {
	return AgentCatalogChangeResult{}, ErrRefused
}
func (f *imageViewerFixture) ResponderStatus() (ResponderView, error) {
	return ResponderView{Chosen: true, Manual: true, Harnesses: []HarnessView{}}, nil
}
func (f *imageViewerFixture) SetResponder(ResponderChange) (string, error) { return "", ErrRefused }
func (f *imageViewerFixture) Typing(protocol.TypingScope) (client.TypingView, error) {
	return client.TypingView{Entries: []client.TypingEntry{}}, nil
}
func (f *imageViewerFixture) SendTyping(context.Context, protocol.TypingScope, bool) (client.TypingResult, error) {
	return client.TypingResult{}, ErrRefused
}
func (f *imageViewerFixture) SetTypingPreferences(client.TypingPreferences) error { return ErrRefused }
func (f *imageViewerFixture) Refresh(string) (Presence, error) {
	return Presence{Text: "Connection unknown"}, nil
}

func (f *imageViewerFixture) Overview() (Overview, error) {
	o, err := f.pictureFixture.Overview()
	peer := PersonView{Person: "carol", Label: "Carol", Address: "carol/desk", State: "pinned"}
	o.People = append(o.People, peer)
	o.DMs = append(o.DMs, DMSummary{ID: "other-video-chat", Peer: peer, Title: "Other chat", Created: f.now(), LastAt: f.now(), Last: "Another conversation"})
	return o, err
}

func (f *imageViewerFixture) DM(id string) (DMThread, error) {
	dm, err := f.pictureFixture.DM(id)
	if id == "other-video-chat" {
		dm.Peer = PersonView{Person: "carol", Label: "Carol", Address: "carol/desk", State: "pinned"}
		dm.Messages = []DMMessage{}
		return dm, err
	}
	dm.Messages[0].Attachments = []FileView{
		{Index: 2, Name: "wide.png", Size: int64(len(f.images[0])), Openable: true},
		{Index: 5, Name: "tall.png", Size: int64(len(f.images[1])), Openable: true},
		{Index: 8, Name: "received.webm", Size: int64(len(f.video)), Openable: true},
		{Index: 9, Name: "spoof.mp4", Size: 16, Openable: true},
		{Index: 10, Name: "broken.webm", Size: 4, Openable: true},
		{Index: 11, Name: "large.webm", Size: 16*1024*1024 + 1, Openable: true},
		{Index: 12, Name: "unknown.mov", Size: 0, Openable: true},
		{Index: 13, Name: "oversized-return.webm", Size: 4, Openable: true},
		{Index: 14, Name: "late.webm", Size: int64(len(f.video)), Openable: true},
		{Index: 15, Name: "unavailable.webm", Size: 4, Openable: true},
	}
	topic := strings.Repeat("e", 32)
	dm.Topics = []ThreadSummary{{ID: topic, Conv: id, Title: "Video topic", Count: 1, State: "active", LastAt: f.now()}}
	dm.Messages = append(dm.Messages, DMMessage{ID: "topic-video-message", LID: "topic-video-message", Dir: "in", From: "bob/desk", Kind: "message", Body: "Another topic", At: f.now(), State: "received", Topic: topic})
	return dm, err
}

func (f *imageViewerFixture) OpenFile(_ context.Context, dir, id string, index int) (io.ReadCloser, string, error) {
	if dir != "in" || id != "picture-message" {
		return nil, "", ErrNotFound
	}
	switch index {
	case 2:
		return io.NopCloser(bytes.NewReader(f.images[0])), "wide.png", nil
	case 5:
		return io.NopCloser(bytes.NewReader(f.images[1])), "tall.png", nil
	case 8, 14:
		return io.NopCloser(bytes.NewReader(f.video)), "received.webm", nil
	case 9:
		return io.NopCloser(strings.NewReader("<html>bad</html>!")), "spoof.mp4", nil
	case 10:
		return io.NopCloser(bytes.NewReader([]byte{0x1a, 0x45, 0xdf, 0xa3})), "broken.webm", nil
	case 13:
		return io.NopCloser(bytes.NewReader(make([]byte, 16*1024*1024+1))), "oversized-return.webm", nil
	case 15:
		return nil, "", ErrRefused
	}
	return nil, "", ErrNotFound
}

func TestImageViewerRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	f := &imageViewerFixture{imageOpenFixture: &imageOpenFixture{pictureFixture: newPictureFixture()}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sample := os.Getenv("AGENTNET_VIDEO_SAMPLE")
	if sample == "" {
		sample = filepath.Join(t.TempDir(), "sample.webm")
		encode := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "3", "-c:v", "libvpx", "-pix_fmt", "yuv420p", "-c:a", "libvorbis", "-threads", "1", "-y", sample)
		if out, err := encode.CombinedOutput(); err != nil {
			t.Fatalf("local deterministic media prerequisite: %v\n%s", err, out)
		}
	}
	var err error
	f.video, err = os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("locally encoded VP8/Vorbis sample: %d bytes SHA256 %x", len(f.video), sha256.Sum256(f.video))
	for i, rect := range []image.Rectangle{image.Rect(0, 0, 2400, 1600), image.Rect(0, 0, 1000, 2000)} {
		img := image.NewNRGBA(rect)
		for y := 0; y < rect.Dy(); y++ {
			for x := 0; x < rect.Dx(); x++ {
				img.SetNRGBA(x, y, color.NRGBA{uint8(80 + 100*i), uint8(x % 256), uint8(y % 256), 255})
			}
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		f.images = append(f.images, b.Bytes())
	}
	ts := httptest.NewUnstartedServer(nil)
	s := New(f, ts.Listener.Addr().String(), testToken)
	set := NewWorkspaceProviders()
	memberships := []client.Workspace{{ID: "default", Name: "Video fixture", State: "enrolled"}, {ID: strings.Repeat("d", 32), Name: "Second fixture", State: "enrolled"}}
	other := &imageViewerFixture{imageOpenFixture: &imageOpenFixture{pictureFixture: newPictureFixture()}, images: f.images, video: f.video}
	for i, provider := range []Provider{f, other} {
		if _, err := set.Bind(memberships[i], provider); err != nil {
			t.Fatal(err)
		}
	}
	set.Known = func() ([]client.Workspace, error) { return memberships, nil }
	h := s.WorkspaceHandler(set)
	// Owned immutable builds can be exercised before root regenerates the
	// integrated package. The page and file API retain the real host CSP.
	privateComic := os.Getenv("AGENTNET_VIDEO_COMIC")
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if privateComic != "" && strings.HasPrefix(r.URL.Path, "/assets/skins/comic/") {
			name := strings.TrimPrefix(r.URL.Path, "/assets/skins/comic/")
			if name == "entry.mjs" || name == "style.css" || name == "document.css" {
				if name == "entry.mjs" {
					w.Header().Set("Content-Type", "text/javascript")
				}
				http.ServeFile(w, r, filepath.Join(privateComic, name))
				return
			}
		}
		h.ServeHTTP(w, r)
	})
	ts.Start()
	defer ts.Close()
	t.Log(browserCheck(t, "testdata/image_viewer_check.cjs", ts.URL+"/?t="+testToken))
	f.lock.Lock()
	defer f.lock.Unlock()
	if len(f.opened) != 2 || f.opened[0] != (imageReference{"in", "picture-message", 2}) || f.opened[1] != (imageReference{"in", "picture-message", 5}) {
		t.Fatal("desktop opened the wrong attachment", f.opened)
	}
}

func TestInlineVideoPolicies(t *testing.T) {
	const host = "127.0.0.1:43871"
	desktop := New(NewFixture(time.Now), host, testToken)
	mobile := New(NewFixture(time.Now), host, testToken)
	mobile.SetMobile()
	relayOrigins, err := static.WithConnectOrigins(static.Relay(), []string{"https://workspace.example"})
	if err != nil {
		t.Fatal(err)
	}
	for name, handler := range map[string]http.Handler{
		"desktop": desktop.Handler(), "mobile": mobile.Handler(),
		"desktop setup": NewSetup(nil, host, testToken), "mobile setup": NewMobileSetup(nil, host, testToken),
		"native workspace": desktop.WorkspaceHandler(NewWorkspaceProviders()),
		"relay":            static.Relay(), "relay workspace": relayOrigins,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://"+host+"/", nil)
			req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("page status %d", w.Code)
			}
			var media string
			for _, directive := range strings.Split(w.Header().Get("Content-Security-Policy"), ";") {
				if strings.HasPrefix(strings.TrimSpace(directive), "media-src ") {
					media = strings.TrimSpace(directive)
				}
			}
			if media != "media-src blob:" {
				t.Fatalf("media grant %q, must allow only blob:", media)
			}
		})
	}
}

func TestVideoContainerSniff(t *testing.T) {
	out, err := exec.Command("node", "testdata/video_container_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestVideoNestedLeavingContext(t *testing.T) {
	if os.Getenv("AGENTNET_WEB_MODULES") == "" {
		t.Skip("opt-in: existing pinned frontend modules required")
	}
	out, err := exec.Command("node", "testdata/motion_leaving_check.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
