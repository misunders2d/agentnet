package ui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The skin contract's route reference (docs/HOST_API_ROUTES.md) lists every
// route a skin reaches through host.api, with the platforms that serve it.
// This keeps it equal to the program's routes (server.go) and the browser
// engine's (static/engine.mjs): a route cannot be added, renamed or removed
// without the contract saying so (docs/UI_SKINS.md, Versions and capabilities).
func TestHostAPIRoutesDocumented(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	doc, server, engine := read("../../docs/HOST_API_ROUTES.md"), read("server.go"), read("static/engine.mjs")

	type row struct{ method, path, platforms string }
	var rows []row
	for _, m := range regexp.MustCompile("(?m)^\\| `(GET|POST) (/[^` ]+)` \\| ([^|]+) \\|").FindAllStringSubmatch(doc, -1) {
		path, _, _ := strings.Cut(m[2], "?")
		rows = append(rows, row{m[1], path, strings.TrimSpace(m[3])})
	}
	if len(rows) < 60 {
		t.Fatalf("only %d route rows parsed from HOST_API_ROUTES.md", len(rows))
	}
	daemon := func(r row) bool { return strings.Contains(r.platforms, "D") }
	browser := func(r row) bool { return strings.Contains(r.platforms, "B") }

	// Program routes: Go 1.22 patterns; {name} matches one segment and a
	// trailing slash matches the whole subtree.
	type pattern struct {
		method, path string
		re           *regexp.Regexp
	}
	var patterns []pattern
	for _, m := range regexp.MustCompile(`mux\.Handle(?:Func)?\("(GET|POST) (/api/[^"]*|/events)"`).FindAllStringSubmatch(server, -1) {
		expr := regexp.QuoteMeta(m[2])
		expr = regexp.MustCompile(`\\\{[^}]*\\\}`).ReplaceAllString(expr, `[^/]+`)
		if strings.HasSuffix(m[2], "/") {
			expr += ".+"
		}
		patterns = append(patterns, pattern{m[1], m[2], regexp.MustCompile("^" + expr + "$")})
	}
	// Engine routes: the exact paths its request switch answers.
	start, end := strings.Index(engine, "async apiRequest("), strings.Index(engine, `throw new Error("Unknown request.")`)
	if start < 0 || end < start {
		t.Fatal("engine request switch not found")
	}
	engineRoutes := map[string]bool{}
	for _, m := range regexp.MustCompile(`case "(/api/[^"]+)"`).FindAllStringSubmatch(engine[start:end], -1) {
		engineRoutes[m[1]] = true
	}
	// Scheduling dispatches before that switch. Read its actual bounded
	// handler's explicit paths and action allowlist, never unrelated literals.
	scheduleStart, scheduleEnd := strings.Index(engine, "async scheduleAPI("), strings.Index(engine, "async saveScheduledSend(")
	if scheduleStart < 0 || scheduleEnd < scheduleStart || !strings.Contains(engine[start:end], `return this.scheduleAPI(u,body)`) {
		t.Fatal("engine schedule dispatcher not found")
	}
	schedule := engine[scheduleStart:scheduleEnd]
	scheduleMethods := map[string]string{}
	for _, m := range regexp.MustCompile(`if\(u.pathname==="(/api/[^"]+)"\)`).FindAllStringSubmatch(schedule, -1) {
		engineRoutes[m[1]] = true
		scheduleMethods[m[1]] = "GET"
	}
	actions := regexp.MustCompile(`if\(!\[("[^"]+"(?:,"[^"]+")*)\]\.includes\(what\)\)`).FindStringSubmatch(schedule)
	if len(scheduleMethods) != 2 || len(actions) != 2 {
		t.Fatal("engine schedule paths or action allowlist not found")
	}
	allowed := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(actions[1], -1)
	if len(allowed) != 4 {
		t.Fatalf("parsed %d schedule actions", len(allowed))
	}
	for _, action := range allowed {
		path := "/api/schedule/" + action[1]
		engineRoutes[path] = true
		scheduleMethods[path] = "POST"
	}
	if len(patterns) < 50 || len(engineRoutes) < 50 {
		t.Fatalf("parsed %d program and %d engine routes", len(patterns), len(engineRoutes))
	}

	for _, p := range patterns {
		found := false
		for _, r := range rows {
			if daemon(r) && r.method == p.method && p.re.MatchString(r.path) {
				found = true
			}
		}
		if !found {
			t.Errorf("program route %s %s is missing from HOST_API_ROUTES.md (platform D)", p.method, p.path)
		}
	}
	for path := range engineRoutes {
		found := false
		for _, r := range rows {
			if browser(r) && r.path == path {
				found = true
			}
		}
		if !found {
			t.Errorf("browser engine route %s is missing from HOST_API_ROUTES.md (platform B)", path)
		}
	}
	for _, r := range rows {
		if daemon(r) {
			served := false
			for _, p := range patterns {
				if p.method == r.method && p.re.MatchString(r.path) {
					served = true
				}
			}
			if !served {
				t.Errorf("HOST_API_ROUTES.md says the program serves %s %s; it does not", r.method, r.path)
			}
		}
		if browser(r) && !engineRoutes[r.path] {
			t.Errorf("HOST_API_ROUTES.md says the browser serves %s %s; its engine does not", r.method, r.path)
		}
		if method, ok := scheduleMethods[r.path]; browser(r) && ok && r.method != method {
			t.Errorf("HOST_API_ROUTES.md says the browser serves %s %s; its engine serves %s", r.method, r.path, method)
		}
		if !daemon(r) && !browser(r) {
			t.Errorf("HOST_API_ROUTES.md row %s %s names no platform: %q", r.method, r.path, r.platforms)
		}
	}
}
