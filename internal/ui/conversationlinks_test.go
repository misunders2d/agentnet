package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestConversationLinkParser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/conversation_links_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestConversationLinkStore(t *testing.T) {
	esbuild := os.Getenv("AGENTNET_ESBUILD")
	if esbuild == "" {
		t.Skip("opt-in: set AGENTNET_ESBUILD to the installed frontend esbuild")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, pair := range [][2]string{{"web/src/store.ts", "store.ts"}, {"web/src/structural.ts", "structural.ts"}, {"web/src/api.ts", "api.ts"}, {"web/src/model.ts", "model.ts"}, {"web/src/linkText.ts", "linkText.ts"}, {"web/src/runningDetail.ts", "runningDetail.ts"}, {"web/src/requestResult.ts", "requestResult.ts"}, {"web/src/skin/brand.tsx", "skin/brand.tsx"}, {"static/optimistic.mjs", "optimistic.mjs"}, {"skins/shared/person-topics.mjs", "person-topics.mjs"}, {"skins/shared/collective-mentions.mjs", "collective-mentions.mjs"}} {
		b, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(dir, pair[1])
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(dir, "store.cjs")
	build := exec.Command(esbuild, filepath.Join(dir, "store.ts"), "--bundle", "--format=cjs", "--platform=node", "--target=es2022", "--log-level=warning", "--outfile="+bundle)
	build.Env = append(os.Environ(), "NODE_PATH="+filepath.Dir(filepath.Dir(esbuild)))
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = exec.Command(node, "testdata/conversation_links_store_check.cjs", bundle).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

type conversationLinkFixture struct {
	*filesFixture
	topic string
}

func (f *conversationLinkFixture) Overview() (Overview, error) {
	v, err := f.Fixture.Overview()
	v.Files = &FileLimits{MaxFile: 1 << 20, MaxCount: 8}
	return v, err
}

func (f *conversationLinkFixture) DM(id string) (DMThread, error) {
	v, err := f.Fixture.DM(id)
	if err != nil {
		return v, err
	}
	v.Role = "member"
	v.MainTopic = &ThreadSummary{ID: "main", Title: "Main flow", Count: 1, State: "active"}
	v.Topics = []ThreadSummary{{ID: f.topic, Title: "Renamed topic", Count: 1, State: "active"}}
	return v, nil
}
func (f *conversationLinkFixture) Thread(id string) (Thread, error) {
	v, err := f.Fixture.Thread(id)
	if err == nil {
		v.Topic = &ThreadSummary{ID: v.ID, Title: "Exact legacy root", State: "active"}
	}
	return v, err
}

func TestConversationLinksRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT")
	}
	conv, topic := strings.Repeat("b", 64), strings.Repeat("c", 32)
	set := NewWorkspaceProviders()
	for i, id := range []string{"default", strings.Repeat("e", 32)} {
		f := NewFixture(time.Now)
		f.person = &PersonView{Person: strings.Repeat("1", 32), Label: "Alice", Address: f.me.Address, State: PersonSelf, Published: true}
		f.role = RolePerson
		f.dms = []*fxDM{{id: conv, peer: PersonView{Person: strings.Repeat("2", 32), Label: "Bob", Address: "bob/desk", State: PersonPinned, Published: true}, created: time.Now(), msgs: []DMMessage{
			{ID: strings.Repeat("3", 32), Dir: "in", From: "bob/desk", Kind: KindMessage, Body: "Main fixture message\n[Open renamed topic](agentnet://open#realm=" + strings.Repeat([]string{"a", "f"}[i], 32) + "&conv=" + conv + "&topic=" + topic + ")", At: time.Now(), State: "received"},
			{ID: strings.Repeat("4", 32), Dir: "in", From: "bob/desk", Kind: KindMessage, Topic: topic, Body: "Exact named topic message", At: time.Now(), State: "received"},
		}}}
		p := &conversationLinkFixture{filesFixture: &filesFixture{Fixture: f, staged: map[string][]byte{}}, topic: topic}
		if _, err := set.Bind(client.Workspace{ID: id, Name: []string{"Local alias", "Other alias"}[i], Realm: strings.Repeat([]string{"a", "f"}[i], 32), Address: f.me.Address, State: "enrolled"}, p); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewUnstartedServer(nil)
	skins := t.TempDir()
	if err := os.Mkdir(filepath.Join(skins, "notebook"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"skin.json", "entry.mjs", "style.css"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "examples", "skins", "notebook", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skins, "notebook", name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := New(NewFixture(time.Now), ts.Listener.Addr().String(), testToken, skins).WorkspaceHandler(set)
	// A source worker may test its private build before the parent merges and
	// rebuilds Comic. Release checks use the checked-in integrated package.
	privateComic := os.Getenv("AGENTNET_LINKS_COMIC")
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
	t.Cleanup(ts.Close)
	privateScreenshots(t)
	out := browserCheck(t, "testdata/conversation_links_rendered.cjs", ts.URL+"/?t="+testToken)
	t.Logf("%s", out)
	comic := privateComic
	if comic == "" {
		comic = filepath.Join("static", "skins", "comic")
	}
	out = browserCheck(t, "testdata/skin_contract_check.cjs", ts.URL+"/?t="+testToken, comic, filepath.Join("..", "..", "examples", "skins", "notebook"))
	t.Logf("%s", out)
}
