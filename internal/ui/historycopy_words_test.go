package ui

import (
	"os/exec"
	"testing"
)

func TestHistoryCopyWordsDistinguishProducerAndDelivery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, "testdata/history_copy_words_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
