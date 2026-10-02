package ui

import (
	"os/exec"
	"testing"
)

func TestComicExpressions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/comic_expression_check.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
