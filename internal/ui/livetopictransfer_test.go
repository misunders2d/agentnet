package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestLiveTopicTransferRoutes(t *testing.T) {
	alice, bob, _, source, _, _ := liveGuestWorld(t)
	destination := liveTwoMemberGroup(t, alice, bob)
	for _, body := range []string{"First whole topic message", "Second source message"} {
		if _, e := alice.SendConv(t.Context(), source, client.ConvOutgoing{Body: body}); e != nil {
			t.Fatal(e)
		}
	}
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	defer ts.Close()
	server = New(NewLive(alice), strings.TrimPrefix(ts.URL, "http://"), testToken)
	call := func(action string, body, into any) {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		r := do(t, ts, "POST", "/api/topic/transfer/"+action, string(raw), post(ts))
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("%s %d %s", action, r.StatusCode, b)
		}
		if e = json.NewDecoder(r.Body).Decode(into); e != nil {
			t.Fatal(e)
		}
	}
	for _, action := range []string{"preview", "page", "apply", "status", "cancel", "location"} {
		r := do(t, ts, "POST", "/api/topic/transfer/"+action, `{}`, nil)
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %d", action, r.StatusCode)
		}
	}
	request := client.TopicTransferRequest{Source: source, Destination: destination}
	var review client.TopicTransferReview
	call("preview", request, &review)
	if review.TotalItems < 2 || review.TotalBatches < 2 || review.AudienceTotal != 2 {
		t.Fatalf("whole source/audience unavailable: %+v", review)
	}
	var page client.TopicTransferPage
	call("page", client.TopicTransferAction{ID: review.ID, Token: review.Token}, &page)
	if page.Body == "" || len(page.Items) != 1 {
		t.Fatalf("bounded source preview missing: %+v", page)
	}
	var status *client.TopicTransferReview
	call("status", client.TopicTransferAction{Source: source}, &status)
	if status == nil || status.ID != review.ID {
		t.Fatal("source-scope resume missing")
	}
	action := client.TopicTransferAction{ID: review.ID, Token: review.Token, Cursor: 0}
	call("apply", action, &review)
	call("apply", action, &review)
	if review.Cursor != 1 {
		t.Fatal("response retry advanced another quotation")
	}
	call("cancel", action, &review)
	if review.State != "canceled" || review.Cursor != 1 {
		t.Fatal("partial cancellation changed already queued work")
	}
	var location *client.TopicTransferLocation
	call("location", client.TopicTransferRequest{Source: source}, &location)
	if location != nil {
		t.Fatal("partial transfer created a completed source note")
	}
	call("preview", request, &review)
	for review.Cursor < review.TotalBatches {
		call("apply", client.TopicTransferAction{ID: review.ID, Token: review.Token, Cursor: review.Cursor}, &review)
	}
	if review.State != "queued" {
		t.Fatal("all quotations not queued")
	}
	call("location", client.TopicTransferRequest{Source: source}, &location)
	if location == nil || !location.Available || location.Operation != review.ID || location.Destination != destination {
		t.Fatalf("private location missing: %+v", location)
	}
}
