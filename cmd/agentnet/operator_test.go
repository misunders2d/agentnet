package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// stewardPair is a host (no person yet) and another enrolled installation
// with a person, the steward to be.
func stewardPair(t *testing.T) (host, steward *client.Agent, person client.PersonInfo) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	host, _ = diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, err := host.Invite(ctx, "sergey", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	steward, err = client.Join(ctx, t.TempDir(), code, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { steward.Close() })
	if person, err = steward.CreatePerson(ctx, "Sergey"); err != nil {
		t.Fatal(err)
	}
	return host, steward, person
}

// operator grant --person names a steward and prints the person and its
// devices to check; list shows it; revoke --person ends it (MEL-532).
func TestOperatorPersonCommand(t *testing.T) {
	host, steward, person := stewardPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := runOperator(ctx, host, []string{"grant", "--person", steward.Address}, &out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, `"Sergey"`) || !strings.Contains(s, person.Person) || !strings.Contains(s, steward.Address) || !strings.Contains(s, "steward") {
		t.Fatalf("grant output %q", s)
	}
	out.Reset()
	if err := runOperator(ctx, host, []string{"list"}, &out); err != nil || !strings.Contains(out.String(), "steward, active") {
		t.Fatalf("list %q %v", out.String(), err)
	}
	out.Reset()
	if err := runOperator(ctx, host, []string{"revoke", "--person", person.Person}, &out); err != nil || !strings.Contains(out.String(), "no longer a steward") {
		t.Fatalf("revoke %q %v", out.String(), err)
	}
	out.Reset()
	if err := runOperator(ctx, host, []string{"list"}, &out); err != nil || !strings.Contains(out.String(), "operator grant --person") {
		t.Fatalf("empty list %q %v", out.String(), err)
	}
	if err := runOperator(ctx, host, []string{"grant", "--person"}, &out); err == nil {
		t.Fatal("grant --person without an address")
	}
}

// person service --steward ADDRESS makes this installation a service and
// names its steward in one step, at install; a bad steward changes nothing.
func TestPersonServiceSteward(t *testing.T) {
	host, steward, person := stewardPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := runPerson(ctx, host, []string{"service", "--steward", "nobody/none"}, &out); err == nil {
		t.Fatal("an unknown steward accepted")
	}
	if role, _ := host.Role(); role != "" {
		t.Fatalf("a failed steward still made a service: %q", role)
	}
	if err := runPerson(ctx, host, []string{"service", "--steward", steward.Address}, &out); err != nil {
		t.Fatal(err)
	}
	if role, _ := host.Role(); role != "service" {
		t.Fatalf("role %q", role)
	}
	if s := out.String(); !strings.Contains(s, "is its steward") || !strings.Contains(s, person.Person) {
		t.Fatalf("output %q", s)
	}
	if g, err := host.Stewards(); err != nil || len(g) != 1 || g[0].Person != person.Person {
		t.Fatalf("stewards %+v %v", g, err)
	}
}
