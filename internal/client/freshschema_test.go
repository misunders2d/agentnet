package client

import (
	"os"
	"testing"

	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"github.com/misunders2d/agentnet/internal/sqlitedb/schematest"
)

// By default every store in these tests runs every schema step, as a program's does.
// Only the race partitions ask for the copy (scripts/race-shard.sh): a full run of the
// steps costs Join and Open fixtures about 4 s each there, against a 600 s bound. Their
// output counts the stores that were copies, and those results are not schema proof.
func imageAsked() bool { return os.Getenv("AGENTNET_TEST_SCHEMA_IMAGE") == "1" }

func init() { restoreImage() }

// restoreImage puts back what this run asked for, after a test changed it.
func restoreImage() {
	if imageAsked() {
		schematest.Use()
	} else {
		sqlitedb.ReuseFreshSchema(false, nil)
	}
}

func TestFreshSchemaImageMatchesEveryStep(t *testing.T) {
	t.Cleanup(restoreImage) // first in, so last out: after the check's own reset
	schematest.CheckFreshImage(t, schema)
}
