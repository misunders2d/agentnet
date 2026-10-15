package client

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPiRPCPreservesLookupAndNativeLaunchArgs(t *testing.T) {
	for _, name := range []string{"pi", "omp"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			extension := filepath.Join(t.TempDir(), "question lookup.ts")
			cmd := exec.CommandContext(ctx, name, "-p", "--no-session", "--extension", extension, "--add-dir", "retained folder", "--config", "native.yml")
			b, e := newPiRPCBridge(ctx, cmd, "Original", io.Discard, name)
			if e != nil {
				t.Fatal(e)
			}
			defer b.close()
			want := []string{name, "--no-session", "--extension", extension, "--add-dir", "retained folder", "--config", "native.yml", "--mode", "rpc"}
			if !reflect.DeepEqual(cmd.Args, want) {
				t.Fatalf("native RPC changed launch args: got=%q want=%q", cmd.Args, want)
			}
		})
	}
}
