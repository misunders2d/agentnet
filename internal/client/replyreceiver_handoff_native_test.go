package client

import (
	"net"
	"os"
	"testing"
)

func TestClosedReplyReceiverActualNativeShutdownToManaged(t *testing.T) {
	bin := os.Getenv("AGENTNET_LIVE_RECEIVER_BINARY")
	if bin == "" {
		t.Skip("opt-in private native+managed fixture")
	}
	interfaces, e := net.Interfaces()
	if e != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("requires loopback-only namespace")
	}
	nativeReceiverJourney(t, bin, "pi", true)
}

func TestClosedReplyReceiverActualOMPShutdownToManaged(t *testing.T) {
	bin := os.Getenv("AGENTNET_LIVE_RECEIVER_BINARY")
	if bin == "" {
		t.Skip("opt-in private OMP+managed fixture")
	}
	interfaces, e := net.Interfaces()
	if e != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("requires loopback-only namespace")
	}
	nativeReceiverJourney(t, bin, "omp", true)
}
