package static

import (
	"io"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

// MEL-519: a phone's keyboard never covers the message box. Every page a
// skin runs in (the daemon's index.html and the relay's device page made
// from it) lets Android shrink the page with the keyboard; core.css sizes
// the page from the host's --an-viewport-h; and loader.js measures the iOS
// keyboard from visualViewport events (never by polling), sets
// --an-viewport-h and --an-keyboard while it covers the page, and takes
// them away when it closes or the person zooms in.
func TestPageFollowsKeyboard(t *testing.T) {
	const viewport = `<meta name="viewport" content="width=device-width, initial-scale=1, interactive-widget=resizes-content">`
	index, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), viewport) {
		t.Errorf("index.html: the viewport does not resize with the keyboard: want %s", viewport)
	}
	resp := serve("GET", "/")
	relay, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(relay), viewport) {
		t.Errorf("relay device page (%d): the viewport does not resize with the keyboard", resp.StatusCode)
	}
	core, err := fs.ReadFile(Files, "core.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(core), "html, body { height: var(--an-viewport-h, 100%); margin: 0; }") {
		t.Error("core.css does not size the page from --an-viewport-h")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "--input-type=module", "-e", fitViewportCases).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "fit viewport PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// fitViewportCases runs loader.js in a bare context: its fitting starts
// before the host's first import, which fails there and ends the rest.
const fitViewportCases = `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
const props = new Map(), scrolls = [], polls = [], on = {};
const vv = { height: 844, width: 390, offsetTop: 0, offsetLeft: 0, scale: 1,
  addEventListener(type, fn) { (on[type] ||= []).push(fn); } };
const fire = (type) => { for (const fn of on[type] || []) fn(); };
const page = {
  innerHeight: 844, scrollX: 0, scrollY: 0, visualViewport: vv,
  scrollTo(x, y) { scrolls.push([x, y]); page.scrollX = x; page.scrollY = y; },
  setInterval() { polls.push("setInterval"); }, setTimeout() { polls.push("setTimeout"); }, requestAnimationFrame() { polls.push("requestAnimationFrame"); },
  document: { getElementById: () => ({}), documentElement: { style: { setProperty: (k, v) => props.set(k, v), removeProperty: (k) => props.delete(k) } } },
};
page.window = page;
const done = new vm.Script(readFileSync("loader.js", "utf8")).runInContext(vm.createContext(page));
await done.catch(() => {}); // the host's import of skin-choice.mjs: not in this context
const now = () => Object.fromEntries(props);
assert.deepEqual(Object.keys(on).sort(), ["resize", "scroll"], "listens to the visual viewport's resize and scroll");
assert.deepEqual([on.resize.length, on.scroll.length], [1, 1], "one listener each");
assert.deepEqual(now(), {}, "nothing covers the page: no properties");

vv.height = 508; fire("resize"); // the iOS keyboard: the layout viewport keeps its height
assert.deepEqual(now(), { "--an-viewport-h": "508px", "--an-keyboard": "336px" }, "iOS keyboard open");
assert.deepEqual(scrolls, [], "the page had not moved: no scroll");
page.scrollY = 120; fire("scroll");
assert.deepEqual(scrolls, [[0, 0]], "iOS scrolled the page to the field: back to its top");
vv.offsetTop = 36; fire("scroll");
assert.equal(now()["--an-keyboard"], "300px", "a panned visual viewport: the keyboard covers what is under it");

vv.scale = 2; fire("resize");
assert.deepEqual(now(), {}, "zoomed in: the page keeps its size");
vv.scale = 1; vv.offsetTop = 0; vv.height = 844; fire("resize");
assert.deepEqual(now(), {}, "keyboard closed: no properties");

page.innerHeight = 520; vv.height = 520; fire("resize"); // Android: the page itself shrank
assert.deepEqual(now(), {}, "the layout viewport followed the keyboard: nothing to set");
vv.height = 519.5; fire("resize");
assert.deepEqual(now(), {}, "under a pixel is no keyboard");
assert.deepEqual(polls, [], "no polling");
console.log("fit viewport PASS");
`
