package ui

import "testing"

// Validator guards only; application timing still requires rendered evidence.
func TestInteractionCoverageGateGuards(t *testing.T) {
	runNodeCheck(t, "testdata/interaction_coverage_gate_check.cjs")
	runNodeCheck(t, "testdata/c1_oracle_check.cjs")
	runNodeCheck(t, "testdata/interaction_inventory_check.cjs")
	runNodeCheck(t, "testdata/c1_team_snapshot_fixture_check.cjs")
	runNodeCheck(t, "testdata/c1_topic_rename_fixture_check.cjs")
}
