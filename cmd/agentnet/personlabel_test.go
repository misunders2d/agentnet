package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestPersonRenameDispatchRetainsIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	before, err := a.CreatePerson(ctx, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runPerson(ctx, a, []string{"rename", "Alice New"}, &out); err != nil {
		t.Fatal(err)
	}
	after, _, err := a.Person()
	if err != nil || after.Person != before.Person || after.Address != before.Address || after.Label != "Alice New" {
		t.Fatalf("rename %+v %v", after, err)
	}
	for _, text := range []string{"Display label: Alice New", "Person ID: " + before.Person, "Address unchanged: " + a.Address} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("output lacks %q: %s", text, &out)
		}
	}
	if err := runPerson(ctx, a, []string{"rename"}, &out); err == nil {
		t.Fatal("missing name accepted")
	}
}
