package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func TestLocalReadOnlyDatabaseHelpersKeepLiteralHomePath(t *testing.T) {
	for _, tc := range []struct{ name, home string }{
		{"fragment", "home#saved"},
		{"query", "home?mode=memory"},
		{"percent", "home%2Fprivate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.name == "query" {
				t.Skip("question marks are not valid Windows directory names")
			}
			parent := t.TempDir()
			home := filepath.Join(parent, tc.home)
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			_, path := paths(home)
			db, err := sqlitedb.Open(path, []string{`CREATE TABLE config(k TEXT PRIMARY KEY, v TEXT);`})
			if err != nil {
				t.Fatal(err)
			}
			release := protocol.Release{Version: "v9.9.9", URL: "https://example.invalid/releases/v9.9.9"}
			refusal := UpdateRequired{Latest: "v9.9.9", Running: protocol.Version}
			for key, value := range map[string]any{"release": release, updateRequiredKey: refusal} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO config VALUES(?,?)", key, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if version, found, err := HomeSchema(home); err != nil || !found || version != 1 {
				t.Errorf("schema read from wrong home: version=%d found=%v error=%v", version, found, err)
			}
			if got, found := LocalRelease(home); !found || got != release {
				t.Errorf("release read from wrong home: %+v found=%v", got, found)
			}
			if got, found := LocalUpdateRequired(home); !found || got != refusal {
				t.Errorf("update refusal read from wrong home: %+v found=%v", got, found)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 || entries[0].Name() != tc.home {
				t.Fatalf("read-only helpers created another path: %v, %v", entries, err)
			}
		})
	}
}
