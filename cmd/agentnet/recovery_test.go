package main

import (
	"context"
	"testing"
)

func TestMEL736CLIRequiresExplicitSelection(t *testing.T) {
	for _, args := range [][]string{nil, {"--from", ""}, {"--from", "not-an-id"}} {
		if e := runRecover(context.Background(), nil, args); e == nil {
			t.Fatal("missing old id accepted")
		}
	}
	for _, args := range [][]string{nil, {"approve"}, {"approve", "x", "y"}, {"unknown", "id"}} {
		if e := runRecoveryAdmin(context.Background(), nil, args); e == nil {
			t.Fatal("invalid admin command accepted")
		}
	}
}
