package client

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
)

// Fixture enrollment needs a current, empty store, not hundreds of repeats
// of the same migration. Make it once through the production opener, close
// it (checkpointing its WAL), then copy only its immutable bytes. Every
// fixture still generates its own keys and does real enrollment, commits,
// recovery and reopening. Tests of fresh Join/Open and old schemas do not
// use this helper.
var fixtureStoreImage = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "agentnet-fixture-schema-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "agent.db")
	s, err := openStore(path)
	if err != nil {
		return nil, err
	}
	if err := s.db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
})

func seedFixtureStore(t *testing.T, home string) {
	t.Helper()
	// As in Join, restrict the home before creating the store. On
	// Windows, the copy inherits its owner's ACL, not the temp root's access.
	if err := secfile.EnsureDir(home); err != nil {
		t.Fatal(err)
	}
	_, path := paths(home)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return // never replace an existing store in a retry/recovery fixture
	}
	if err != nil {
		t.Fatal(err)
	}
	data, err := fixtureStoreImage()
	if err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestFixtureStoresKeepIndependentPersistentState(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	seedFixtureStore(t, first)
	_, path := paths(first)
	a, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.setConfig(map[string]string{"fixture": "first"}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	// A fresh copy has the current schema but none of the first's rows;
	// seeding the first again must leave its durable mutation intact.
	seedFixtureStore(t, second)
	seedFixtureStore(t, first)
	for home, want := range map[string]string{first: "first", second: ""} {
		_, path := paths(home)
		s, err := openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		got, configErr := s.config("fixture")
		var version int
		versionErr := s.db.QueryRow("PRAGMA user_version").Scan(&version)
		closeErr := s.db.Close()
		configOK := configErr == nil
		if want == "" {
			configOK = errors.Is(configErr, sql.ErrNoRows)
		}
		if !configOK || got != want || versionErr != nil || version != len(schema) || closeErr != nil {
			t.Fatalf("fixture %s: value=%q want=%q version=%d errors=%v/%v/%v", home, got, want, version, configErr, versionErr, closeErr)
		}
		if _, err := os.Stat(filepath.Join(home, "identity.json")); !os.IsNotExist(err) {
			t.Fatalf("schema image carried an identity: %v", err)
		}
		if _, err := secfile.Read(path); err != nil {
			t.Fatalf("fixture permissions: %v", err)
		}
	}
}
