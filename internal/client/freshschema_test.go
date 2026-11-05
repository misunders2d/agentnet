package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/sqlitedb/schematest"
)

// A new store in these tests starts from a copy instead of running every schema step
// again, which cost Join and Open fixtures about 4 s each under race. An old store still
// runs its later steps on the way up, as ever. The steps themselves are proved by the
// check below, and one by one in sqlitedb, hub, cmd/agentnet and itest.
func init() { schematest.Use() }

// The check turns the copy off and on to compare; later tests get it back afterwards.
func TestFreshSchemaImageMatchesEveryStep(t *testing.T) {
	t.Cleanup(schematest.Use)
	schematest.CheckFreshImage(t, schema)
}
