package static

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// jsList reads `name = Object.freeze([...])` or `name = [...]` of string
// literals from a browser file.
func jsList(t *testing.T, file, name string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\b` + name + `\s*=\s*(?:Object\.freeze\()?\[([^\]]*)\]`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s: no %s list", file, name)
	}
	var out []string
	for _, q := range regexp.MustCompile(`["']([^"']*)["']`).FindAllSubmatch(m[1], -1) {
		out = append(out, string(q[1]))
	}
	return out
}

// The browser's copy of the skin ids is the program's: skin-choice.mjs
// trusts exactly the reserved ids but "default" as built in, opens the
// first embedded skin by default, and local-skins.mjs refuses exactly the
// reserved ids (it imports them, so the copies cannot drift).
func TestBrowserSkinIDsMatchProgram(t *testing.T) {
	builtIn := jsList(t, "skin-choice.mjs", "BUILT_IN")
	want := slices.DeleteFunc(slices.Clone(ReservedSkinIDs), func(id string) bool { return id == "default" })
	if !slices.Equal(builtIn, want) {
		t.Errorf("skin-choice.mjs BUILT_IN %v, want ReservedSkinIDs without default %v", builtIn, want)
	}
	reserved := append(slices.Clone(builtIn), "default")
	if !slices.Equal(reserved, ReservedSkinIDs) {
		t.Errorf("skin-choice.mjs RESERVED %v, want %v", reserved, ReservedSkinIDs)
	}
	src, err := os.ReadFile("skin-choice.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `export const HOME = "`+BuiltinSkins[0]+`";`) {
		t.Errorf("skin-choice.mjs HOME is not %q, the first built-in skin", BuiltinSkins[0])
	}
	if !strings.Contains(string(src), "export const RESERVED = Object.freeze([...BUILT_IN, \"default\"]);") {
		t.Error("skin-choice.mjs RESERVED is not BUILT_IN and \"default\"")
	}
	local, err := os.ReadFile("local-skins.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`import \{ RESERVED as reserved\b[^}]*\} from './skin-choice\.mjs';`).Match(local) || regexp.MustCompile(`const reserved\s*=`).Match(local) {
		t.Error("local-skins.mjs does not take its reserved ids from skin-choice.mjs")
	}
	loader, err := os.ReadFile("loader.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loader), `await import("/assets/skin-choice.mjs")`) || regexp.MustCompile(`BUILT_IN\s*=|HOME\s*=\s*"`).Match(loader) {
		t.Error("loader.js keeps its own copy of the skin ids instead of skin-choice.mjs")
	}
}

// Which skin opens and when consent is asked, case by case, in node.
func TestSkinChoiceAndTrust(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "--input-type=module", "-e", skinChoiceCases).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "skin choice PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
}

const skinChoiceCases = `
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";
const { mark, choose, trusted, takenName, HOME } = await import(pathToFileURL("skin-choice.mjs").href);
const digest = "a".repeat(64), other = "b".repeat(64);
const skins = [
  { api: 1, id: "comic", name: "Comic", digest: "c".repeat(64) },
  { api: 1, id: "notebook", name: "Notebook", digest },
  { api: 1, id: "sketch", name: "Sketch", digest, local: true },
  { api: 1, id: "zoom", name: "Not Zoom", digest, local: true }, // a stored package can never be built in
].map(mark);
const id = (o) => o.selected && o.selected.id;
// Saved choices and the page's ?skin=.
assert.equal(HOME, "comic");
assert.deepEqual([id(choose(skins, {})), choose(skins, {}).save], ["comic", null], "no choice opens Comic");
assert.deepEqual([id(choose(skins, { saved: "default" })), choose(skins, { saved: "default" }).save], ["comic", "comic"], "saved default is rewritten");
assert.deepEqual([id(choose(skins, { saved: "classic" })), choose(skins, { saved: "classic" }).save], ["comic", "comic"], "saved classic is rewritten");
assert.deepEqual([id(choose(skins, { saved: "comic" })), choose(skins, { saved: "comic" }).save], ["comic", null]);
assert.equal(id(choose(skins, { saved: "notebook" })), "notebook", "a saved installed skin opens");
assert.equal(id(choose(skins, { query: "default", saved: "notebook" })), "comic", "?skin=default names Comic");
assert.equal(id(choose(skins, { query: "classic" })), "comic", "?skin=classic opens Comic in this release");
assert.equal(id(choose(skins, { query: "sketch", saved: "comic" })), "sketch", "?skin= wins over the saved choice");
assert.equal(id(choose(skins, { saved: "gone" })), "comic", "an unknown id opens Comic");
const withClassic = [...skins, mark({api:1,id:"classic",name:"Classic"})];
assert.equal(id(choose(withClassic, {saved:"classic", packageChoice:true})), "classic", "new package Classic selection survives reload");
assert.equal(id(choose(withClassic, {saved:"classic"})), "comic", "legacy Classic selection still migrates");
assert.equal(choose([], {}).selected, null, "no Comic in the catalog: nothing to open");
// Trust: built in by the host's list; anything else by its exact digest.
const by = (x) => skins.find((s) => s.id === x);
assert.equal(by("comic").builtin, true);
assert.equal(trusted(by("comic"), null), true, "built-in");
assert.equal(trusted(by("notebook"), null), false, "installed, never accepted");
assert.equal(trusted(by("notebook"), digest), true, "installed, accepted digest");
assert.equal(trusted(by("notebook"), other), false, "installed, changed or tampered digest");
assert.equal(trusted(by("sketch"), null), false, "browser-local, never accepted");
assert.equal(trusted(by("sketch"), digest), true, "browser-local, accepted digest");
assert.equal(trusted(by("sketch"), digest.toUpperCase()), false, "browser-local, tampered digest");
assert.equal(by("zoom").builtin, false, "a stored package with a built-in id is not built in");
assert.equal(trusted(by("zoom"), null), false);
assert.equal(trusted(mark({ id: "notebook", builtin: true, digest }), null), false, "a manifest cannot claim to be built in");
// No package shows as a built-in skin.
for (const n of ["Comic", " comic ", "CLASSIC", "Zoom"]) assert.equal(takenName(n), true, n);
for (const n of ["Notebook example", "Comic book", "Default"]) assert.equal(takenName(n), false, n);
console.log("skin choice PASS");
`

// An installed package may not show as a built-in skin: one named "Comic"
// (in any case or spacing), "Classic" or "Zoom" is left out of the catalog.
func TestInstalledSkinCannotTakeBuiltInName(t *testing.T) {
	for _, name := range []string{"Comic", " comic ", "CLASSIC", "Zoom"} {
		if !ReservedSkinName(name) {
			t.Errorf("%q is not reserved", name)
		}
	}
	for _, name := range []string{"Notebook example", "Comic book", "Default"} {
		if ReservedSkinName(name) {
			t.Errorf("%q is reserved", name)
		}
	}
	home := t.TempDir()
	dir := filepath.Join(home, "lookalike")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"skin.json": `{"api":1,"id":"lookalike","name":"comic","entry":"entry.mjs","files":["entry.mjs"]}`,
		"entry.mjs": `export function mount(root) { root.textContent = "lookalike"; }`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if list := catalogOf(t, home); len(list) != len(BuiltinSkins) || list[0]["id"] != "comic" {
		t.Fatalf("a package named like a built-in skin was listed: %v", list)
	}
}
