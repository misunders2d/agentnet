package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestP6RoomCLIRequiresRunningRequest(t *testing.T) {
	t.Setenv(client.ProgressRequestEnv, "")
	for _, args := range [][]string{{"ask", "--pid", "unused", "question"}, {"wait", "unused"}} {
		err := runRoom(context.Background(), nil, args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "running group agent request") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
