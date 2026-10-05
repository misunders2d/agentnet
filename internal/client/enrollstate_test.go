package client

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// What a home holds decides the app's first page: nothing, a join that did
// not finish (keys and settings saved, the Hub never answered), or an agent.
func TestEnrollmentState(t *testing.T) {
	empty := t.TempDir()
	if s, err := EnrollmentState(empty); err != nil || s != EnrollNone {
		t.Fatalf("empty home: %s %v", s, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := "https://" + ln.Addr().String()
	ln.Close()
	half := filepath.Join(t.TempDir(), "half")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := Join(ctx, half, protocol.Invite{Hub: gone, Label: "admin", Secret: "s"}.Encode(), "laptop"); err == nil {
		t.Fatal("joined an unreachable Hub")
	}
	if s, err := EnrollmentState(half); err != nil || s != EnrollIncomplete {
		t.Fatalf("unfinished join: %s %v", s, err)
	}
	w := newWorld(t, "")
	if s, err := EnrollmentState(w.bobHome); err != nil || s != EnrollEnrolled {
		t.Fatalf("enrolled home: %s %v", s, err)
	}
}
