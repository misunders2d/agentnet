package ui

import (
	"os/exec"
	"testing"
)

// TestComicPeopleModel runs Comic's people helpers (web/src/model.ts) in
// Node: agents only where one runs, own devices are you, other people's
// devices are those people, Bring in offers agents only, look-alike names
// get a short key, and the shared device words.
func TestComicPeopleModel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/comic_people_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
