package ui

import "testing"

func TestBrowserDecisionExecutionReconciliation(t *testing.T) {
	runNodeCheck(t, "testdata/decision_reconcile_engine_check.mjs")
}
