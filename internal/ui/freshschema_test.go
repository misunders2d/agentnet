package ui

import "github.com/misunders2d/agentnet/internal/sqlitedb/schematest"

// No test here is about schema steps, upgrades or backups; they are proved one by one in
// sqlitedb, client, hub, cmd/agentnet and itest. Starting each of these fixtures' stores
// from a copy took a measured sample of them from 1.12 times the base to 0.81 under race.
func init() { schematest.Use() }
