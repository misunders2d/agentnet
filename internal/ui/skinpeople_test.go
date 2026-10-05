package ui

import (
	"os/exec"
	"testing"
)

// TestSkinPeopleWords runs Classic's and Zoom's own people words against
// the shared device-word vectors and the people rules.
func TestSkinPeopleWords(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/skin_people_words_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
