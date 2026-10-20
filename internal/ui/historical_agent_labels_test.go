package ui

import (
	"os"
	"testing"
)

func TestHistoricalAgentAttentionLabels(t *testing.T) {
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("installed pinned frontend compiler required")
	}
	runNodeCheck(t, "testdata/historical_agent_labels_check.cjs")
}
