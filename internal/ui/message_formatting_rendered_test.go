package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

const formattingOldBody = "__old bold__ and _old italic_ with `||literal code||`"
const formattingHiddenBody = "Before **||outer _nested_\n\nsecond secret||** after"

type formattingFixture struct {
	*pictureFixture
	mu         sync.Mutex
	messages   []DMMessage
	rejectEdit bool
	readDelay  time.Duration
}

func (f *formattingFixture) Overview() (Overview, error) {
	o, err := f.pictureFixture.Overview()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) > 0 {
		m := f.messages[len(f.messages)-1]
		o.DMs[0].Last = client.BodyPreview(m.Controls.Shown(m.Body))
		o.DMs[0].Count = len(f.messages)
	}
	return o, err
}

func (f *formattingFixture) DM(id string) (DMThread, error) {
	dm, err := f.pictureFixture.DM(id)
	f.mu.Lock()
	dm.Messages = append([]DMMessage(nil), f.messages...)
	delay := f.readDelay
	f.readDelay = 0
	f.mu.Unlock()
	// Hold the response without blocking local input or later writes.
	time.Sleep(delay)
	return dm, err
}

func (f *formattingFixture) SendDM(d DMDraft) (Sent, error) {
	time.Sleep(1200 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	m := DMMessage{ID: d.ID, LID: d.ID, Dir: "out", From: "alice/laptop", Kind: KindMessage,
		Body: d.Body, ReplyTo: d.ReplyTo, Quote: d.Quote, At: time.Now(), State: "delivered", Controls: Controls{Can: []string{"edit"}}}
	f.messages = append(f.messages, m)
	return Sent{ID: m.ID, LID: m.LID, State: "delivered"}, nil
}

func (f *formattingFixture) React(ControlAction) (string, error)         { return "", ErrNotFound }
func (f *formattingFixture) DeleteMessage(ControlAction) (string, error) { return "", ErrNotFound }

func (f *formattingFixture) EditMessage(a ControlAction) (string, error) {
	time.Sleep(1200 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectEdit {
		f.rejectEdit = false
		return "", Refuse("Synthetic edit refusal")
	}
	for i := range f.messages {
		m := &f.messages[i]
		if m.ID == a.ID || m.LID == a.ID {
			m.Edited = true
			m.Text = a.Text
			return "Edited", nil
		}
	}
	return "", ErrNotFound
}

func (f *formattingFixture) seed(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages, f.rejectEdit, f.readDelay = nil, false, 0
	for i := range count {
		id := "history-" + strconv.Itoa(i)
		body := "Retained message " + strconv.Itoa(i)
		if i == count-2 {
			body = formattingOldBody
		}
		if i == count-1 {
			body = formattingHiddenBody
		}
		f.messages = append(f.messages, DMMessage{ID: id, LID: id, Dir: "in", From: "bob/desk", Kind: KindMessage,
			Body: body, At: time.Now().Add(-time.Duration(count-i) * time.Second), State: "received"})
	}
}

func TestSpoilerProjectionBrowser(t *testing.T) {
	runNodeCheck(t, "testdata/spoiler_projection_check.mjs")
}

func TestMessageFormattingRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in rendered message formatting")
	}
	f := &formattingFixture{pictureFixture: newPictureFixture()}
	ts := httptest.NewUnstartedServer(nil)
	defer ts.Close()
	app := New(f, ts.Listener.Addr().String(), testToken).Handler()
	base := os.Getenv("AGENTNET_FORMATTING_BASE")
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture-formatting/seed":
			count, _ := strconv.Atoi(r.URL.Query().Get("count"))
			f.seed(count)
			w.WriteHeader(http.StatusNoContent)
			return
		case "/fixture-formatting/reject-edit":
			f.mu.Lock()
			f.rejectEdit = true
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		case "/fixture-formatting/sync":
			f.mu.Lock()
			f.messages = append(f.messages, DMMessage{ID: "sync-row", LID: "sync-row", Dir: "in", From: "bob/desk", Kind: KindMessage,
				Body: "Concurrent history insert", At: time.Now(), State: "received"})
			f.readDelay = 350 * time.Millisecond
			f.mu.Unlock()
			f.Fixture.mu.Lock()
			f.Fixture.bump()
			f.Fixture.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if base != "" {
			if name, ok := strings.CutPrefix(r.URL.Path, "/assets/skins/comic/"); ok && filepath.IsLocal(name) {
				if strings.HasSuffix(name, ".mjs") {
					w.Header().Set("Content-Type", "text/javascript")
				}
				http.ServeFile(w, r, filepath.Join(base, name))
				return
			}
		}
		app.ServeHTTP(w, r)
	})
	ts.Start()
	t.Log(browserCheck(t, "testdata/message_formatting_rendered.cjs", ts.URL+"/?t="+testToken))
}
