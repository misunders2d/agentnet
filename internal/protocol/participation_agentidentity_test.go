package protocol

import (
	"bytes"
	"testing"
)

func TestParticipationOptionalAgentIdentity(t *testing.T) {
	e := vecInvite()
	legacy := append([]byte(nil), e.Canonical()...)
	e.Host.AgentID = ""
	if !bytes.Equal(legacy, e.Canonical()) {
		t.Fatal("absent identity changes legacy canonical bytes")
	}
	e.Host.AgentID = "00112233445566778899aabbccddeeff"
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bad", "00112233445566778899AABBCCDDEEFF", "00112233445566778899aabbccddeef"} {
		e.Host.AgentID = id
		if e.Validate() == nil {
			t.Fatalf("accepted malformed identity %q", id)
		}
	}
}
