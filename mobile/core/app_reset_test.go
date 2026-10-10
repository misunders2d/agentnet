package core

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/ui"
)

func TestMobileSetAsidePreservesWholeHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mobile")
	if e := os.MkdirAll(filepath.Join(home, "workspaces", "other"), 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"identity.json", "agent.db", "agent.db-wal", "workspaces.json", "workspaces/other/identity.json"} {
		if e := os.WriteFile(filepath.Join(home, name), []byte("private saved "+name), 0600); e != nil {
			t.Fatal(e)
		}
	}
	backup, e := mobileSetAside(home, time.Unix(1790000000, 0))
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(backup)
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatal("backup permissions", e)
	}
	for _, name := range []string{"identity.json", "agent.db", "agent.db-wal", "workspaces.json", "workspaces/other/identity.json"} {
		data, e := os.ReadFile(filepath.Join(backup, "home", name))
		if e != nil || string(data) != "private saved "+name {
			t.Fatal("lost", name, e)
		}
	}
	entries, e := os.ReadDir(home)
	if e != nil || len(entries) != 0 {
		t.Fatal("fresh home", e)
	}
}
func TestAppExplicitIncompleteResetKeepsOrigin(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mobile")
	if e := os.Mkdir(home, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "identity.json"), []byte("incomplete keys"), 0600); e != nil {
		t.Fatal(e)
	}
	a, e := OpenApp(home, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	origin, entry := a.Origin(), a.URL()
	v, e := a.setup.SetupStartAgain()
	if e != nil || v.State != client.EnrollNone {
		t.Fatal(v, e)
	}
	if a.Origin() != origin || a.URL() != entry {
		t.Fatal("changed browser origin")
	}
	matches, e := filepath.Glob(home + "-previous-*/home/identity.json")
	if e != nil || len(matches) != 1 {
		t.Fatal("missing backup", matches, e)
	}
	data, e := os.ReadFile(matches[0])
	if e != nil || string(data) != "incomplete keys" {
		t.Fatal("changed keys", e)
	}
	if _, e = a.setup.SetupJoin(ui.SetupJoin{Code: "bad"}); e == nil {
		t.Fatal("invalid invitation accepted after reset")
	}
}
func TestAppResetRefusesEnrolledOrDamagedHome(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	a, e := OpenApp(home, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.setup.SetupStartAgain(); e == nil {
		t.Fatal("reset live enrolled home")
	}
	a.Close()
	damaged := filepath.Join(t.TempDir(), "damaged")
	os.Mkdir(damaged, 0700)
	os.WriteFile(filepath.Join(damaged, "agent.db"), []byte("not a database"), 0600)
	if app, e := OpenApp(damaged, 0); e == nil {
		app.Close()
		t.Fatal("damaged data opened as resettable setup")
	}
	data, e := os.ReadFile(filepath.Join(damaged, "agent.db"))
	if e != nil || string(data) != "not a database" {
		t.Fatal("damaged data changed", e)
	}
}

func TestAppEndedResetPreservesHistoryAndWorkspaces(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	db, e := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT OR REPLACE INTO config(k,v) VALUES('link','{"state":"refused"}')`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if e = os.MkdirAll(filepath.Join(home, "workspaces", "retained"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(home, "workspaces", "retained", "history"), []byte("saved history"), 0600); e != nil {
		t.Fatal(e)
	}
	a, e := OpenApp(home, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	if v := a.setup.SetupState(); v.State != client.EnrollRefused {
		t.Fatal(v)
	}
	if v, e := a.setup.SetupStartAgain(); e != nil || v.State != client.EnrollNone {
		t.Fatal(v, e)
	}
	files, e := filepath.Glob(home + "-previous-*/home/workspaces/retained/history")
	if e != nil || len(files) != 1 {
		t.Fatal(files, e)
	}
	body, e := os.ReadFile(files[0])
	if e != nil || string(body) != "saved history" {
		t.Fatal("history changed", e)
	}
	backups, e := filepath.Glob(home + "-previous-*/home/agent.db")
	if e != nil || len(backups) != 1 {
		t.Fatal(backups, e)
	}
	db, e = sql.Open("sqlite", backups[0])
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var link string
	if e = db.QueryRow(`SELECT v FROM config WHERE k='link'`).Scan(&link); e != nil || link != `{"state":"refused"}` {
		t.Fatal("retained enrollment changed", link, e)
	}
}
