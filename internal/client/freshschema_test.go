package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/sqlitedb/schematest"
)

// This package's own tests run every schema step: the image is on only inside this check.
func TestFreshSchemaImageMatchesEveryStep(t *testing.T) {
	schematest.CheckFreshImage(t, schema)
}
