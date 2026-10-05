package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeLines writes rows as a Claude transcript and returns its size.
func claudeLines(t *testing.T, file string, rows ...[]byte) int64 {
	t.Helper()
	var out bytes.Buffer
	for _, r := range rows {
		out.Write(r)
		out.WriteByte('\n')
	}
	if e := os.WriteFile(file, out.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	return int64(out.Len())
}

func claudeRow(t *testing.T, row map[string]any) []byte {
	t.Helper()
	raw, e := json.Marshal(row)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

// A long session never refuses (MEL-537): more than the old 100k records
// and 32 MiB in all are read, one record at a time.
func TestNativeScanNoTotalCap(t *testing.T) {
	sid := "long-session"
	file := filepath.Join(t.TempDir(), sid+".jsonl")
	row := claudeRow(t, map[string]any{"type": "assistant", "sessionId": sid, "version": "2.1.287", "pad": strings.Repeat("x", 200)})
	var out bytes.Buffer
	for out.Len() <= 33<<20 || bytes.Count(out.Bytes(), []byte{'\n'}) <= 100000 {
		out.Write(row)
		out.WriteByte('\n')
	}
	if e := os.WriteFile(file, out.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := claudeNativeScan(file, sid, false, 0, nil); e != nil {
		t.Fatalf("a long transcript refused: %v", e)
	}
	// Pi's branch reader has no total cap either.
	pi := filepath.Join(t.TempDir(), "pi.jsonl")
	nativeWrite(t, pi, sid)
	f, _ := os.OpenFile(pi, os.O_APPEND|os.O_WRONLY, 0600)
	parent := ""
	for i := range 120000 {
		id := "e" + strings.Repeat("0", 6-len(itoa(i))) + itoa(i)
		line, _ := json.Marshal(map[string]any{"type": "message", "id": id, "parentId": parent})
		f.Write(append(line, '\n'))
		parent = id
	}
	f.Close()
	branch, e := nativeBranch(pi, sid, "e000010", false)
	if e != nil || len(branch) != 11 {
		t.Fatalf("branch of a long Pi session: %d %v", len(branch), e)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// One record over nativeRecordMax is read through and skipped, for
// validation and matching alike: never a refusal, never a receipt.
func TestNativeScanSkipsOversizeRecord(t *testing.T) {
	sid := "native-selected-session"
	ack := ReplyReceiverAck{BindingID: "binding", InputID: "input", ClaimID: "claim", InputToken: "unpredictable-token"}
	file := filepath.Join(t.TempDir(), sid+".jsonl")
	huge := claudeRow(t, map[string]any{"type": "user", "sessionId": "another-session", "pad": strings.Repeat("y", nativeRecordMax)})
	receipt := claudeRow(t, claudeReceiptRow(sid, claudeChannelSource, ack))
	claudeLines(t, file, huge, receipt)
	if ok, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, 0); e != nil || !ok {
		t.Fatalf("receipt after an oversize record: %v %v", ok, e)
	}
	// The oversize record itself never matches, even if it held the tuple.
	big := claudeReceiptRow(sid, claudeChannelSource, ack)
	big["pad"] = strings.Repeat("z", nativeRecordMax)
	claudeLines(t, file, claudeRow(t, big))
	if ok, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, 0); e != nil || ok {
		t.Fatalf("an oversize record became a receipt: %v %v", ok, e)
	}
	// Codex: a long record is skipped, not refused; the header still counts.
	codex := codexRollout(t, sid, map[string]any{"ordinal": 1, "type": "event_msg", "payload": map[string]any{"pad": strings.Repeat("w", nativeRecordMax)}}, codexReceiptRow(sid, "text"))
	if ok, e := codexNativeReceipt(codex, sid, "text", 0); e != nil || !ok {
		t.Fatalf("codex receipt after an oversize record: %v %v", ok, e)
	}
}

// A receipt is read from the claim's offset: what was written before the
// claim can never be its receipt, an offset inside a record starts at the
// next one, and a file shorter than the offset fails closed.
func TestClaudeReceiptFromClaimOffset(t *testing.T) {
	sid := "native-selected-session"
	ack := ReplyReceiverAck{BindingID: "binding", InputID: "input", ClaimID: "claim", InputToken: "unpredictable-token"}
	file := filepath.Join(t.TempDir(), sid+".jsonl")
	before := claudeRow(t, map[string]any{"type": "assistant", "sessionId": sid})
	receipt := claudeRow(t, claudeReceiptRow(sid, claudeChannelSource, ack))
	size := claudeLines(t, file, before, receipt)
	if ok, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, int64(len(before)+1)); e != nil || !ok {
		t.Fatalf("receipt after the offset: %v %v", ok, e)
	}
	if ok, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, size); e != nil || ok {
		t.Fatalf("a record before the claim counted: %v %v", ok, e)
	}
	if ok, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, int64(len(before)+5)); e != nil || ok {
		t.Fatalf("a record begun before the claim counted: %v %v", ok, e)
	}
	if ok, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, 3); e != nil || !ok {
		t.Fatalf("offset inside the first record: %v %v", ok, e)
	}
	if ok, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, size+1); e == nil || ok {
		t.Fatalf("a file shorter than the claim offset: %v %v", ok, e)
	}
}
