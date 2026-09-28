package hub

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// joinAs enrolls a fresh key (or id) at address with invite secret and
// returns the status and error body.
func joinAs(t *testing.T, h *Hub, secret, address string, id *identity.Identity) (int, protocol.Error) {
	t.Helper()
	if id == nil {
		id, _ = identity.Generate()
	}
	req := protocol.JoinRequest{Secret: secret, Public: id.Public(address)}
	protocol.SignJoin(&req, id.Sign)
	body, _ := json.Marshal(req)
	w := serve(h, httptest.NewRequest("POST", "/v1/join", bytes.NewReader(body)))
	var e protocol.Error
	json.Unmarshal(w.Body.Bytes(), &e)
	return w.Code, e
}

func invites(t *testing.T, h *Hub, label string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if err := h.store.createInvite(s, label, false, time.Hour, "admin/test"); err != nil {
			t.Fatal(err)
		}
	}
}

// A taken address is refused with a code and a free name to offer the
// person; the invite and key stay usable for the name they confirm.
// Revoked addresses stay taken, and an existing -N suffix is continued.
func TestEnrollmentCollisionSuggestsFreeName(t *testing.T) {
	h, _, _ := testHub(t)
	invites(t, h, "bernard", "i1", "i2", "i3", "i4")
	if c, _ := joinAs(t, h, "i1", "bernard/thinkpad", nil); c != http.StatusCreated {
		t.Fatalf("first join: %d", c)
	}
	kim, _ := identity.Generate()
	c, e := joinAs(t, h, "i2", "bernard/thinkpad", kim)
	if c != http.StatusConflict || e.Code != protocol.CodeAddressTaken ||
		e.Error != "address bernard/thinkpad is already enrolled; bernard/thinkpad-2 is free now (not reserved)" {
		t.Fatalf("collision: %d %+v", c, e)
	}
	// The suggestion is not a reservation: someone else may take it first.
	if c, _ := joinAs(t, h, "i3", "bernard/thinkpad-2", nil); c != http.StatusCreated {
		t.Fatalf("taking the suggestion first: %d", c)
	}
	if err := h.store.revoke("bernard/thinkpad-2"); err != nil {
		t.Fatal(err)
	}
	c, e = joinAs(t, h, "i2", "bernard/thinkpad", kim)
	if c != http.StatusConflict || !strings.Contains(e.Error, "bernard/thinkpad-3 is free now") {
		t.Fatalf("revoked address offered again: %d %+v", c, e)
	}
	c, e = joinAs(t, h, "i2", "bernard/thinkpad-2", kim)
	if c != http.StatusConflict || !strings.Contains(e.Error, "bernard/thinkpad-3 is free now") {
		t.Fatalf("revoked address enrollable, or suffix not continued: %d %+v", c, e)
	}
	// Same invite, same key, confirmed name.
	if c, _ := joinAs(t, h, "i2", "bernard/thinkpad-3", kim); c != http.StatusCreated {
		t.Fatalf("retry with the confirmed name: %d", c)
	}
	if c, _ := joinAs(t, h, "i2", "bernard/thinkpad-3", kim); c != http.StatusCreated {
		t.Fatalf("exact replay after a collision: %d", c)
	}
	// Other people's labels are not involved.
	invites(t, h, "bernard-kim", "k1")
	if c, _ := joinAs(t, h, "k1", "bernard-kim/thinkpad", nil); c != http.StatusCreated {
		t.Fatalf("distinct label: %d", c)
	}
}

// Suggestions stay valid names at the length limit.
func TestFreeNameFitsNameRules(t *testing.T) {
	long := strings.Repeat("a", 31) + "b" // 32, the maximum
	cases := []struct {
		name  string
		taken []string
		want  string
	}{
		{"laptop", []string{"laptop"}, "laptop-2"},
		{"laptop", []string{"laptop", "laptop-2", "laptop-3"}, "laptop-4"},
		{"laptop-7", []string{"laptop-7"}, "laptop-8"},
		{"laptop-0", []string{"laptop-0"}, "laptop-0-2"},
		{"v2", []string{"v2"}, "v2-2"},
		{"x-999999", []string{"x-999999"}, "x-1000000"},
		{"x-" + strconv.Itoa(math.MaxInt), []string{"x-" + strconv.Itoa(math.MaxInt)}, "x-" + strconv.Itoa(math.MaxInt) + "-2"},
		{"x-" + strconv.Itoa(math.MaxInt32), []string{"x-" + strconv.Itoa(math.MaxInt32)}, "x-" + strconv.Itoa(math.MaxInt32) + "-2"},
		{long, []string{long}, strings.Repeat("a", 30) + "-2"},
		{strings.Repeat("a", 29) + "-bc", []string{strings.Repeat("a", 29) + "-bc"}, strings.Repeat("a", 29) + "-2"},
		{strings.Repeat("a", 29) + "--x", []string{strings.Repeat("a", 29) + "--x"}, strings.Repeat("a", 29) + "-2"},
	}
	for _, c := range cases {
		taken := map[string]bool{}
		for _, n := range c.taken {
			taken[n] = true
		}
		got := freeName(c.name, taken)
		if got != c.want || !protocol.ValidName(got) || taken[got] {
			t.Errorf("freeName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// Simultaneous joins for one address never overwrite each other: exactly
// one wins, every other one gets the collision answer (not a storage
// error) and can retry.
func TestConcurrentJoinsForOneAddress(t *testing.T) {
	h, _, _ := testHub(t)
	const n = 8
	secrets := make([]string, n)
	for i := range secrets {
		secrets[i] = "c" + string(rune('a'+i))
	}
	invites(t, h, "bernard", secrets...)
	var wg sync.WaitGroup
	codes := make([]int, n)
	errs := make([]protocol.Error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], errs[i] = joinAs(t, h, secrets[i], "bernard/thinkpad", nil)
		}(i)
	}
	wg.Wait()
	won := 0
	for i := 0; i < n; i++ {
		switch codes[i] {
		case http.StatusCreated:
			won++
		case http.StatusConflict:
			if errs[i].Code != protocol.CodeAddressTaken {
				t.Errorf("join %d: %+v", i, errs[i])
			}
		default:
			t.Errorf("join %d: status %d %+v", i, codes[i], errs[i])
		}
	}
	if won != 1 {
		t.Fatalf("%d joins won", won)
	}
}
