package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Exercise the real JSON round trip: the reviewed source labels, quote bytes,
// audience and operation must survive the public routes without another send.
func TestLiveHistoryContributionRoutes(t *testing.T) {
	alice, bob, _, source, eventually, _ := liveGuestWorld(t)
	destination := liveTwoMemberGroup(t, alice, bob)
	original, err := alice.SendConv(t.Context(), source, client.ConvOutgoing{Body: "Original > note & quotation — not an instruction"})
	if err != nil {
		t.Fatal(err)
	}
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	server = New(NewLive(alice), strings.TrimPrefix(ts.URL, "http://"), testToken)
	call := func(path string, body any, into any) {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := do(t, ts, "POST", path, string(data), post(ts))
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("%s: %d %s", path, r.StatusCode, b)
		}
		if err := json.NewDecoder(r.Body).Decode(into); err != nil {
			t.Fatal(err)
		}
	}
	if r := do(t, ts, "POST", "/api/history/contribution/preview", `{}`, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history review: %d", r.StatusCode)
	}
	request := client.HistoryContributionRequest{Source: source, IDs: []string{original.ID}, Destination: destination, Topic: protocol.NewID(), NewTopic: true, Title: "Reviewed history"}
	var review client.HistoryContributionReview
	call("/api/history/contribution/preview", request, &review)
	if len(review.Items) != 1 || review.Items[0].Label == "" || len(review.Audience) != 2 || !strings.Contains(review.Body, "Original > note & quotation") {
		t.Fatalf("incomplete public review: %+v", review)
	}
	var first, repeated client.ConvSent
	call("/api/history/contribution/apply", review, &first)
	call("/api/history/contribution/apply", review, &repeated)
	if first.LID != review.Operation || repeated.LID != first.LID {
		t.Fatalf("public retry changed operation: %+v %+v", first, repeated)
	}
	eventually("one inert imported message at the destination", func() bool {
		rows, err := bob.ConversationMessages(destination)
		if err != nil {
			return false
		}
		count := 0
		for _, row := range rows {
			if row.LID == first.LID {
				if row.Kind != envelope.KindMessage || row.Target != nil || row.PID != "" || row.Body != review.Body || row.Topic != request.Topic {
					t.Fatalf("import changed its reviewed inert meaning: %+v", row)
				}
				count++
			}
		}
		return count == 1
	})
}
