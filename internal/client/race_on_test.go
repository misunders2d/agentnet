//go:build race

package client

// raceOn: the race detector slows the store about tenfold; size tests that
// measure throughput down so they stay in their shard.
const raceOn = true
