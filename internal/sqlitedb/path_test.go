package sqlitedb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDatabasePathRemainsLiteralThroughReopenAndUpgrade(t *testing.T) {
	for _, tc := range []struct{ name, filename string }{
		{"fragment", "agent#saved.db"},
		{"query", "agent?mode=memory.db"},
		{"percent", "agent%2Fprivate.db"},
		{"spaces_unicode", "my agent Привіт.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.name == "query" {
				t.Skip("question marks are not valid Windows file names")
			}
			path := filepath.Join(t.TempDir(), tc.filename)
			db, err := Open(path, v1)
			if err != nil {
				t.Fatal(err)
			}
			var sequence int
			var name, opened string
			if err := db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &opened); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if filepath.Clean(opened) != path {
				db.Close()
				t.Fatalf("opened %q instead of literal database path %q", opened, path)
			}
			if _, err := db.Exec("INSERT INTO a VALUES(17)"); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path, v2)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var value int
			if err := db.QueryRow("SELECT y FROM b").Scan(&value); err != nil || value != 17 {
				t.Fatalf("literal-path data did not survive upgrade: %d, %v", value, err)
			}
			snapshot := path + ".v1.bak"
			if err := validateSnapshot(snapshot, 1); err != nil {
				t.Fatalf("literal-path snapshot: %v", err)
			}
			if runtime.GOOS != "windows" {
				for _, saved := range []string{path, snapshot} {
					info, err := os.Stat(saved)
					if err != nil || info.Mode().Perm() != 0o600 {
						t.Fatalf("owner-only database/snapshot %q: %v, %v", saved, info, err)
					}
				}
			}
		})
	}
}
