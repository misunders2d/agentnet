package sqlitedb

import (
	"net/url"
	"strings"
	"testing"
)

func TestAndroidSQLiteDSNPreservesStoreAndReadonlySettings(t *testing.T) {
	for _, tc := range []struct {
		dsn  string
		want map[string]string
	}{
		{"file:/private/agent.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate", map[string]string{"_journal_mode": "WAL", "_busy_timeout": "5000", "_foreign_keys": "1", "_txlock": "immediate"}},
		{"file:/private/agent.db?mode=ro&_pragma=busy_timeout(2000)", map[string]string{"mode": "ro", "_busy_timeout": "2000"}},
		{"file:/private/snapshot.db?mode=ro&immutable=1", map[string]string{"mode": "ro", "immutable": "1"}},
		{"file:/private/agent.db?_pragma=busy_timeout(0)", map[string]string{"_busy_timeout": "0"}},
		{"/private/agent.db", nil},
	} {
		got, err := androidSQLiteDSN(tc.dsn)
		if err != nil {
			t.Fatal(err)
		}
		before, _, _ := strings.Cut(tc.dsn, "?")
		after, query, _ := strings.Cut(got, "?")
		if before != after {
			t.Fatalf("database path changed: %q to%q", before, after)
		}
		params, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if params.Has("_pragma") || params.Get("_synchronous") != "FULL" {
			t.Fatalf("unsupported pragma or weak durability in%q", got)
		}
		for key, value := range tc.want {
			if params.Get(key) != value {
				t.Fatalf("%s=%q,want%q in%q", key, params.Get(key), value, got)
			}
		}
	}
}
func TestAndroidSQLiteDSNRejectsUnsupportedOrWeakenedSettings(t *testing.T) {
	for _, query := range []string{
		"_pragma=synchronous(OFF)", "_pragma=unknown(1)", "_pragma=foreign_keys(0)",
		"_pragma=journal_mode(DELETE)", "_pragma=busy_timeout(-1)", "_pragma=busy_timeout(99999999999999)",
		"_pragma=busy_timeout", "_pragma=busy_timeout(1)&_busy_timeout=2", "_synchronous=NORMAL", "_sync=OFF", "%broken=value",
	} {
		if _, err := androidSQLiteDSN("file:/private/agent.db?" + query); err == nil {
			t.Fatalf("accepted unsupported Android SQLite query%q", query)
		}
	}
}
