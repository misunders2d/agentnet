package client

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceRenamePersistsOnlyLocalAlias(t *testing.T) {
	w := newWorld(t, "")
	registry, err := OpenWorkspaces(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := registry.List()
	keyBefore, _ := os.ReadFile(filepath.Join(w.bobHome, "identity.json"))
	renamed, err := registry.Rename(DefaultWorkspace, "  Product operations  ")
	if err != nil {
		t.Fatal(err)
	}
	expected := before[0]
	expected.Name = "Product operations"
	if !reflect.DeepEqual(renamed, expected) {
		t.Fatal("rename changed identity binding")
	}
	again, err := OpenWorkspaces(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := again.List()
	if !reflect.DeepEqual(after[0], expected) {
		t.Fatal("alias lost on reopen")
	}
	keyAfter, _ := os.ReadFile(filepath.Join(w.bobHome, "identity.json"))
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("rename changed keys")
	}
	for _, name := range []string{" ", "name\nline", strings.Repeat("a", 121)} {
		if _, err := again.Rename(DefaultWorkspace, name); err == nil {
			t.Fatal("invalid name accepted")
		}
	}
	final, _ := again.List()
	if !reflect.DeepEqual(after, final) {
		t.Fatal("refusal changed registry")
	}
	if _, err := again.Rename(strings.Repeat("b", 32), "Other"); err == nil {
		t.Fatal("unknown membership renamed")
	}
}
