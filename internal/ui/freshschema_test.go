package ui

import (
	"database/sql"
	"os"
	"sync/atomic"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/sqlitedb/schematest"
)

// No test here is about schema steps, upgrades or backups; they are proved one by one in
// sqlitedb, hub, cmd/agentnet and itest, and row by row in client. Starting each of these fixtures' stores
// from a copy took a measured sample of them from 1.12 times the base to 0.81 under race.
//
// AGENTNET_TEST_VIEW_COVER=1 runs them as with client.ViewCoverSchema appended (scoped
// change keys, not yet a step): every kept view still meets the parity oracles there.
var viewCovered atomic.Int64 // stores given ViewCoverSchema, said by TestMain

func init() {
	if os.Getenv("AGENTNET_TEST_VIEW_COVER") == "" {
		schematest.Use()
		return
	}
	schematest.UseWith(func(db *sql.DB) error {
		var clientStore, covered int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='view_dirty'`).Scan(&clientStore); err != nil || clientStore == 0 {
			return err
		}
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'view_cover_%'`).Scan(&covered); err != nil || covered > 0 {
			return err
		}
		_, err := db.Exec(client.ViewCoverSchema)
		viewCovered.Add(1)
		return err
	})
}
