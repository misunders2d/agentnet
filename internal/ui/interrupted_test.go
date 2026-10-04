package ui

import (
	"slices"
	"testing"
)

// BUG-07: an interrupted question or task waits for the person (client
// reviewStates): the page says so and offers running it again. An
// interrupted follow-up is no request: nobody waits on it.
func TestInterruptedNeedsYouOnThePage(t *testing.T) {
	for _, k := range []string{KindQuestion, KindTask} {
		if got := Next("in", k, "interrupted", "bob/desk", false); got != "you" {
			t.Errorf("%s interrupted: next %q", k, got)
		}
		if a := ActionsFor(k, "interrupted"); !slices.Contains(a, DoAccept) {
			t.Errorf("interrupted %s actions %v", k, a)
		}
	}
	for _, k := range []string{KindAnswer, KindResult} {
		if got := Next("in", k, "interrupted", "bob/desk", false); got != "" {
			t.Errorf("interrupted %s follow-up: next %q", k, got)
		}
	}
}
