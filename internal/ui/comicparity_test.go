package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// comicParity pins what the old app could do (MEL-528): each feature's
// route or act verb, how Comic must use it, and the string Classic and Zoom
// must still reference. Comic counts only a real use in its features (a
// call site, an act verb written out, a field read), never the definition
// in api.ts or the generated types: api.ts defined remind and manageGroup
// for months while no screen called them. A UI that drops one fails here
// until this table is edited on purpose; that it is reachable is for the
// rendered checks to prove.
var comicParity = []struct {
	feature string
	route   string // the host API route, act verb or overview field
	comic   string // a regexp over Comic's sources outside api.ts and api.gen.ts; "" = not in Comic yet
	skins   string // a regexp some Classic and Zoom src/*.mjs file must match
}{
	{"1 reminders: set or move", "POST /api/remind", `\.remind\(`, route("/api/remind")},
	{"1 reminders: done", "POST /api/remind/done", `\.remindDone\(`, route("/api/remind/done")},
	{"1 reminders: cancel", "POST /api/remind/cancel", `\.remindCancel\(`, route("/api/remind/cancel")},
	{"1 reminders: list", "overview.reminders", `\.reminders\b`, `\.reminders\b`},
	{"2 group admin and leave", "POST /api/groups/manage", `\.manageGroup\(`, route("/api/groups/manage")},
	{"3 trust a changed key", "act trust", `do: "trust"`, `do: "trust"`},
	{"4 held-back list", "overview.quarantine", `\.quarantine\b`, `\.quarantine\b`},
	{"5 answer questions automatically", "act approve", `do: "approve"`, `do: "approve"`},
	{"5 stop automatic answers", "act unapprove", `do: "unapprove"`, `do: "unapprove"`},
	{"5 stop tasks without asking", "act revoke_tasks", `do: "revoke_tasks"`, `do: "revoke_tasks"`},
	{"6 tasks without asking on agent invite", "POST /api/dm/agent/invite tasks_from", `tasks_from: [A-Za-z]`, `tasks_from: [A-Za-z]`},
	{"7 group history since a date", "POST /api/groups/invite history", `onMode\("since"\)`, `\{ since: `},
	{"8 typing preferences", "POST /api/typing/preferences", `\.typingPreferences\(`, route("/api/typing/preferences")},
	// Items 9-13 are still only in Classic and Zoom (MEL-528 P5b fills in comic).
	{"9 service or bot", "POST /api/device/service", "", route("/api/device/service")},
	{"10 teams", "GET /api/teams", "", route("/api/teams")},
	{"10 team changes", "POST /api/team", "", route("/api/team")},
	{"10 a team's people", "POST /api/teams/snapshot", "", route("/api/teams/snapshot")},
	{"11 hooks setup", "/api/assistant-setup", "", route("/api/assistant-setup")},
	{"12 reply receivers", "GET /api/reply-receivers", "", route("/api/reply-receivers")},
	{"12 reply sessions", "GET /api/reply-sessions", "", route("/api/reply-sessions")},
	{"13 drive setup", "/api/drive/setup", "", route("/api/drive/setup")},
	{"13 save to drive", "host.drive.driveUpload", "", `driveUpload`},
}

// route matches a route written as a string literal, in either quotes.
func route(r string) string { return `["']` + regexp.QuoteMeta(r) + `["']` }

// readSources concatenates the files under dir with one of exts, except skip.
func readSources(t *testing.T, dir string, exts []string, skip ...string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		for _, s := range skip {
			if filepath.Base(path) == s {
				return nil
			}
		}
		for _, e := range exts {
			if strings.HasSuffix(path, e) {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				b.Write(data)
				b.WriteByte('\n')
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestComicParityRoutes(t *testing.T) {
	comic := readSources(t, "web/src", []string{".ts", ".tsx"}, "api.ts", "api.gen.ts")
	skins := map[string]string{
		"classic": readSources(t, "skins/classic/src", []string{".mjs"}),
		"zoom":    readSources(t, "skins/zoom/src", []string{".mjs"}),
	}
	for _, row := range comicParity {
		if row.comic != "" && !regexp.MustCompile(row.comic).MatchString(comic) {
			t.Errorf("Comic no longer uses %s (%s): nothing in web/src outside api.ts matches %s", row.route, row.feature, row.comic)
		}
		for name, src := range skins {
			if !regexp.MustCompile(row.skins).MatchString(src) {
				t.Errorf("%s no longer uses %s (%s): no src/*.mjs holds %s", name, row.route, row.feature, row.skins)
			}
		}
	}

	// The most member keys an agent invitation may name is the protocol's.
	m := regexp.MustCompile(`TASK_KEYS_MAX = (\d+)`).FindStringSubmatch(comic)
	if m == nil {
		t.Fatal("Comic no longer states TASK_KEYS_MAX")
	}
	if n, _ := strconv.Atoi(m[1]); n != protocol.MaxTaskKeys {
		t.Errorf("Comic's TASK_KEYS_MAX is %d, protocol.MaxTaskKeys is %d", n, protocol.MaxTaskKeys)
	}
}
