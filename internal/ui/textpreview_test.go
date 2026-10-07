package ui

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func TestTextPreviewBounds(t *testing.T) {
	runNodeCheck(t, "testdata/text_preview_check.mjs")
}

const previewSample = "Agent result — UTF-8\n<script>window.previewExecuted=true</script>\n<img src=x onerror=window.previewExecuted=true>\n"

type textPreviewFixture struct {
	*pictureFixture
	reads atomic.Int32
}

func (f *textPreviewFixture) DM(id string) (DMThread, error) {
	dm, err := f.pictureFixture.DM(id)
	dm.Messages[0].Attachments = []FileView{
		{Index: 0, Name: "agent-result.txt", Size: int64(len(previewSample)), Openable: true},
		{Index: 1, Name: "bad-utf8.txt", Size: 2, Openable: true},
		{Index: 2, Name: "unsafe.html", Size: int64(len(previewSample)), Openable: true},
	}
	return dm, err
}
func (f *textPreviewFixture) OpenFile(_ context.Context, _, _ string, index int) (io.ReadCloser, string, error) {
	f.reads.Add(1)
	if index == 1 {
		return io.NopCloser(bytes.NewReader([]byte{0xff, 0xfe})), "bad-utf8.txt", nil
	}
	name := "agent-result.txt"
	if index == 2 {
		name = "unsafe.html"
	}
	return io.NopCloser(bytes.NewReader([]byte(previewSample))), name, nil
}

func TestTextPreviewRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in rendered attachment check")
	}
	f := &textPreviewFixture{pictureFixture: newPictureFixture()}
	ts := httptest.NewUnstartedServer(nil)
	handler := New(f, ts.Listener.Addr().String(), testToken).Handler()
	// A disposable source bundle can qualify source before the coordinated
	// skin build; unset this to test the checked-in production package.
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if source := os.Getenv("AGENTNET_TEXT_PREVIEW_BUNDLE"); source != "" && r.URL.Path == "/assets/skins/comic/entry.mjs" {
			w.Header().Set("Content-Type", "text/javascript")
			http.ServeFile(w, r, source)
			return
		}
		handler.ServeHTTP(w, r)
	})
	ts.Start()
	defer ts.Close()
	browserCheck(t, "testdata/text_preview_rendered_check.cjs", ts.URL+"/?t="+testToken)
	if f.reads.Load() != 6 {
		t.Fatalf("explicit fetch count: %d, want 6 across desktop and phone", f.reads.Load())
	}
}
