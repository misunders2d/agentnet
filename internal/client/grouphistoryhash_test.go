package client

import (
	"github.com/misunders2d/agentnet/internal/envelope"
	"strings"
	"testing"
)

func TestGroupHistoryCanonicalSelectionVector(t *testing.T) {
	item := HistoryItem{V: 1, ID: strings.Repeat("4", 32), LID: strings.Repeat("2", 32), From: "claimed.device", FromKey: strings.Repeat("c", 64), TS: 10, Kind: envelope.KindMessage, Body: "selected <text> & file", ReplyTo: strings.Repeat("3", 32), Attachments: []envelope.Attachment{{Name: "first.bin", Size: 7, SHA256: strings.Repeat("a", 64)}, {Name: "second.bin", Size: 9, SHA256: strings.Repeat("b", 64)}}}
	conv := strings.Repeat("1", 32)
	const want = "884f5cee25a5695a6e50c942c896790e8a0f328de405ac67cd075ce589480ab0"
	if got := historyRef(conv, item); got.Hash != want || got.Author != item.FromKey || got.LID != item.LID {
		t.Fatalf("typed content/author vector: %+v", got)
	}
	copy := item
	copy.ID, copy.From, copy.TS, copy.At, copy.GroupAdmission = strings.Repeat("5", 32), "another.claim", 99, 100, "not-content-authority"
	if historyRef(conv, copy).Hash != want {
		t.Fatal("physical metadata changed selected content hash")
	}
	copy.Attachments = []envelope.Attachment{item.Attachments[1], item.Attachments[0]}
	if historyRef(conv, copy).Hash == want {
		t.Fatal("ordered manifest mutation not bound")
	}
	copy = item
	copy.Body += " changed"
	if historyRef(conv, copy).Hash == want {
		t.Fatal("visible content mutation not bound")
	}
	copy = item
	copy.FromKey = strings.Repeat("d", 64)
	if historyRef(conv, copy).Author == item.FromKey || historyRef(conv, copy).Hash != want {
		t.Fatal("original author must be a separate exact ref field")
	}
}
