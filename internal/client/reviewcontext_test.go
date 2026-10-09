package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestReviewReportKeepsRequestContext(t *testing.T) {
	a := &Agent{Address: "owner/host"}
	id := protocol.NewID()
	files := []FileInfo{{Name: "screenshot.png", SavedPath: "/private/local/file"}, {Name: "notes.md"}}
	summary := reviewFileExcerpt(files)
	it := ReportItem{ID: id, Kind: envelope.KindQuestion, State: stateNeedHuman, Attempt: 1, Excerpt: summary}
	detail := "The environment needs repair.\nNo task was replayed."
	var report Report
	if err := json.Unmarshal([]byte(a.reportBodyFor([]ReportItem{it}, map[string]string{id: detail}, true, false)), &report); err != nil {
		t.Fatal(err)
	}
	got := report.Items[0]
	if got.ID != id || got.Attempt != 1 || got.Actionable || !strings.HasPrefix(got.Excerpt, "Request: Files: screenshot.png, notes.md\n") || !strings.HasSuffix(got.Excerpt, detail) || strings.Contains(got.Excerpt, "/private/") {
		t.Fatalf("request context lost or authority changed: %#v", got)
	}
	if err := json.Unmarshal([]byte(a.reportBodyFor([]ReportItem{it}, map[string]string{id: detail}, false, true)), &report); err != nil {
		t.Fatal(err)
	}
	if report.Items[0].Excerpt != summary {
		t.Fatal("ordinary operator report received full private agent output")
	}
	if n := len([]rune(reviewFileExcerpt([]FileInfo{{Name: strings.Repeat("x", 300)}}))); n > 120 {
		t.Fatal("unbounded file summary", n)
	}
}
