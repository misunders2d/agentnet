package ui

import "testing"

// Validator guards only; application timing still requires rendered evidence.
func TestInteractionCoverageGateGuards(t *testing.T) {
	runNodeCheck(t, "testdata/interaction_coverage_gate_check.cjs")
}
