package client

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/tlscert"
)

type realmRelay struct {
	server   *httptest.Server
	version  atomic.Pointer[protocol.VersionInfo]
	requests atomic.Int64
	cert     string
}

func newRealmRelay(t *testing.T, v protocol.VersionInfo) *realmRelay {
	t.Helper()
	r := &realmRelay{}
	r.version.Store(&v)
	r.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/version" || req.URL.RawQuery != "" {
			t.Errorf("unexpected realm request: %s %s", req.Method, req.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r.version.Load())
	}))
	cert, key, err := tlscert.Generate("127.0.0.1", "synthetic realm endpoint", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	r.cert = string(cert)
	r.server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	r.server.StartTLS()
	t.Cleanup(r.server.Close)
	return r
}

func realmAgent(t *testing.T, relay *realmRelay) *Agent {
	t.Helper()
	home := t.TempDir()
	seedFixtureStore(t, home)
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Save(filepath.Join(home, "identity.json")); err != nil {
		t.Fatal(err)
	}
	st, err := openStore(filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setConfig(map[string]string{"enrolled": "1", "address": "member/device", "hub": relay.server.URL, "hub_cert": relay.cert}); err != nil {
		t.Fatal(err)
	}
	st.db.Close()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// Compare existing security/history rows, including all their columns, not
// only the realm value. These table names are fixed by this synthetic test.
func realmRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	state := make(map[string][][]any)
	for _, table := range []string{"config", "peers", "approvals", "task_grants", "inbox", "outbox"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			refs := make([]any, len(cols))
			for i := range values {
				refs[i] = &values[i]
			}
			if err := rows.Scan(refs...); err != nil {
				t.Fatal(err)
			}
			for i, value := range values {
				if data, ok := value.([]byte); ok {
					values[i] = string(data)
				}
			}
			state[table] = append(state[table], values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return state
}

func addRealmHistoryAndGrants(t *testing.T, a *Agent) {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	peer := id.Public("peer/device")
	if err := a.store.pin(peer); err != nil {
		t.Fatal(err)
	}
	if err := a.Approve(peer.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GrantTasks(peer.Address); err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{ID: protocol.NewID(), From: peer.Address, To: a.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "synthetic earlier history"}
	if err := a.store.addInbox(in, peer.Fingerprint()); err != nil {
		t.Fatal(err)
	}
}

func TestRealmClientFirstRecordPersistsAndLocalReadDoesNotConnect(t *testing.T) {
	id := protocol.NewID()
	relay := newRealmRelay(t, protocol.VersionInfo{Protocol: protocol.ProtocolVersion, RealmID: id})
	a := realmAgent(t, relay)
	if got, err := a.RealmID(); got != "" || !errors.Is(err, ErrRealmUnknown) || relay.requests.Load() != 0 {
		t.Fatalf("unrecorded local read guessed or contacted Hub: %q %v", got, err)
	}
	key, err := secfile.Read(filepath.Join(a.home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.CheckRealm(context.Background()); err != nil || got != id || relay.requests.Load() != 1 {
		t.Fatalf("first explicit verified check failed: %q %v", got, err)
	}
	if got, err := a.CheckRealm(context.Background()); err != nil || got != id {
		t.Fatalf("same identity refused: %q %v", got, err)
	}
	a.Close()
	again, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	relay.server.Close()
	requests := relay.requests.Load()
	if got, err := again.RealmID(); err != nil || got != id || relay.requests.Load() != requests {
		t.Fatalf("reopen lost realm or local read contacted offline Hub: %q %v", got, err)
	}
	if got, err := again.CheckRealm(context.Background()); err == nil || got != "" {
		t.Fatalf("offline check claimed a freshly verified identity: %q %v", got, err)
	}
	if got, err := again.RealmID(); err != nil || got != id {
		t.Fatal("offline check replaced the pin")
	}
	if got, err := secfile.Read(filepath.Join(a.home, "identity.json")); err != nil || string(got) != string(key) {
		t.Fatalf("realm recording changed device credentials: %v", err)
	}
}

func TestRealmClientUnknownMalformedAndChangedRefuseWithoutMutation(t *testing.T) {
	pinned := protocol.NewID()
	relay := newRealmRelay(t, protocol.VersionInfo{Protocol: protocol.ProtocolVersion, RealmID: pinned})
	a := realmAgent(t, relay)
	addRealmHistoryAndGrants(t, a)
	if _, err := a.CheckRealm(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := realmRows(t, a.store.db)
	key, err := os.ReadFile(filepath.Join(a.home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		v    protocol.VersionInfo
		want error
	}{
		{"old", protocol.VersionInfo{Protocol: 1}, ErrRealmUnsupported},
		{"short", protocol.VersionInfo{Protocol: 1, RealmID: "abc"}, ErrRealmInvalid},
		{"uppercase", protocol.VersionInfo{Protocol: 1, RealmID: strings.Repeat("A", 32)}, ErrRealmInvalid},
		{"nonhex", protocol.VersionInfo{Protocol: 1, RealmID: strings.Repeat("z", 32)}, ErrRealmInvalid},
		{"changed", protocol.VersionInfo{Protocol: 1, RealmID: protocol.NewID()}, nil},
		{"protocol changed", protocol.VersionInfo{Protocol: 2, RealmID: pinned}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay.version.Store(&tc.v)
			got, err := a.CheckRealm(context.Background())
			if err == nil || got != "" || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("unknown/refused identity claimed verified: %q %v", got, err)
			}
			if tc.name == "changed" {
				var changed *RealmChangedError
				if !errors.As(err, &changed) || changed.Pinned != pinned || changed.Offered != tc.v.RealmID {
					t.Fatalf("changed identity not explicit: %v", err)
				}
			}
			if after := realmRows(t, a.store.db); !reflect.DeepEqual(before, after) {
				t.Fatal("failed realm check changed config, peers, grants or history")
			}
			if got, err := os.ReadFile(filepath.Join(a.home, "identity.json")); err != nil || string(got) != string(key) {
				t.Fatalf("failed realm check replaced credentials: %v", err)
			}
		})
	}
}

func TestRealmClientOldOrMalformedFirstRecordStaysUnknown(t *testing.T) {
	for _, id := range []string{"", "bad", strings.Repeat("A", 32)} {
		name := id
		if name == "" {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			relay := newRealmRelay(t, protocol.VersionInfo{Protocol: 1, RealmID: id})
			a := realmAgent(t, relay)
			before := realmRows(t, a.store.db)
			if got, err := a.CheckRealm(context.Background()); err == nil || got != "" {
				t.Fatalf("unrecognized first identity recorded: %q %v", got, err)
			}
			if got, err := a.RealmID(); got != "" || !errors.Is(err, ErrRealmUnknown) {
				t.Fatalf("unknown realm synthesized from endpoint: %q %v", got, err)
			}
			if !reflect.DeepEqual(before, realmRows(t, a.store.db)) {
				t.Fatal("failed first identity changed local state")
			}
		})
	}
}

func TestRealmClientCorruptLocalPinIsNotRepaired(t *testing.T) {
	relay := newRealmRelay(t, protocol.VersionInfo{Protocol: 1, RealmID: protocol.NewID()})
	a := realmAgent(t, relay)
	if err := a.store.setConfig(map[string]string{"realm_id": "corrupt"}); err != nil {
		t.Fatal(err)
	}
	before := realmRows(t, a.store.db)
	if got, err := a.CheckRealm(context.Background()); got != "" || !errors.Is(err, ErrRealmInvalid) || relay.requests.Load() != 0 {
		t.Fatalf("corrupt pin silently repaired/contacted Hub: %q %v", got, err)
	}
	if err := a.store.recordRealm(relay.version.Load().RealmID); !errors.Is(err, ErrRealmInvalid) {
		t.Fatalf("join seam overwrote corrupt identity: %v", err)
	}
	if !reflect.DeepEqual(before, realmRows(t, a.store.db)) {
		t.Fatal("corrupt pin changed")
	}
	// Ordinary Open remains compatible: only explicit identity inspection
	// diagnoses this new config field, without interrupting existing homes.
	again, err := Open(a.home)
	if err != nil {
		t.Fatalf("new field disrupted ordinary home open: %v", err)
	}
	defer again.Close()
	if _, err := again.RealmID(); !errors.Is(err, ErrRealmInvalid) {
		t.Fatalf("reopen hid corrupt identity: %v", err)
	}
}

func TestRealmClientSameClaimDoesNotBypassTLSVerification(t *testing.T) {
	id := protocol.NewID()
	relay := newRealmRelay(t, protocol.VersionInfo{Protocol: 1, RealmID: id})
	a := realmAgent(t, relay)
	foreign := newRealmRelay(t, protocol.VersionInfo{Protocol: 1, RealmID: id})
	// An endpoint claiming the same namespace still needs the accepted TLS
	// authority. Use the original pinned cert with the foreign endpoint.
	conn, err := newHubConn(foreign.server.URL, relay.cert, a.Address, a.id.Sign)
	if err != nil {
		t.Fatal(err)
	}
	a.hub = conn
	before := realmRows(t, a.store.db)
	if got, err := a.CheckRealm(context.Background()); err == nil || got != "" || foreign.requests.Load() != 0 {
		t.Fatalf("namespace claim bypassed certificate verification: %q %v", got, err)
	}
	if !reflect.DeepEqual(before, realmRows(t, a.store.db)) {
		t.Fatal("unverified endpoint recorded identity")
	}
}

func TestRealmClientCompetingFirstPinsCannotOverwrite(t *testing.T) {
	relay := newRealmRelay(t, protocol.VersionInfo{Protocol: 1})
	a := realmAgent(t, relay)
	b, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ids := []string{protocol.NewID(), protocol.NewID()}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, st := range []*store{a.store, b.store} {
		go func(st *store, id string) {
			<-start
			results <- st.recordRealm(id)
		}(st, ids[i])
	}
	close(start)
	successes, refusals := 0, 0
	for range ids {
		err := <-results
		var changed *RealmChangedError
		if err == nil {
			successes++
		} else if errors.As(err, &changed) {
			refusals++
		} else {
			t.Fatalf("unexpected first-pin failure: %v", err)
		}
	}
	if successes != 1 || refusals != 1 {
		t.Fatalf("first identity overwritten: successes=%d refusals=%d", successes, refusals)
	}
	pin, err := a.RealmID()
	if err != nil || (pin != ids[0] && pin != ids[1]) {
		t.Fatalf("invalid winning pin: %q %v", pin, err)
	}
	if other, err := b.RealmID(); err != nil || other != pin {
		t.Fatalf("two opens disagree on pin: %q %v", other, err)
	}
}

func TestRealmJoinIntegrationAndExplicitLegacyHomeLearning(t *testing.T) {
	w := newWorld(t, "")
	want := w.hub.Hub.RealmID()
	for _, a := range []*Agent{w.alice, w.bob} {
		if got, err := a.RealmID(); err != nil || got != want {
			t.Fatalf("join did not record verified version identity: %q %v", got, err)
		}
	}
	// A pre-feature home has no realm field. Ordinary reopen does not need
	// the network or invent a namespace; only the explicit manager check
	// learns it, preserving the already enrolled device's history/grants.
	addRealmHistoryAndGrants(t, w.bob)
	if err := w.bob.store.deleteConfig("realm_id"); err != nil {
		t.Fatal(err)
	}
	w.bob.Close()
	again, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.RealmID(); got != "" || !errors.Is(err, ErrRealmUnknown) {
		t.Fatalf("ordinary reopen silently learned a workspace: %q %v", got, err)
	}
	before := realmRows(t, again.store.db)
	if got, err := again.CheckRealm(tctx(t)); err != nil || got != want {
		t.Fatalf("explicit legacy-home learning failed: %q %v", got, err)
	}
	after := realmRows(t, again.store.db)
	// Only the new config entry may be added; all existing rows stay exact.
	for table, rows := range before {
		if table != "config" && !reflect.DeepEqual(rows, after[table]) {
			t.Fatalf("first learning changed %s", table)
		}
	}
	configs := after["config"]
	var existing [][]any
	for _, row := range configs {
		if row[0] != "realm_id" {
			existing = append(existing, row)
		}
	}
	if !reflect.DeepEqual(before["config"], existing) {
		t.Fatal("first learning changed endpoint/enrollment/responder config")
	}
}
