package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// The carrier remains in the existing inbox as a non-executable replica.
// This computed view preserves the original PID, separately from grant scope,
// and labels authorship as a forwarder's claim rather than a verified key.
func (a *Agent) showExcerpts(rows []ConvMessage) []ConvMessage {
	out := make([]ConvMessage, 0, len(rows))
	for _, msg := range rows {
		if msg.Sub != envelope.SubExcerpt {
			out = append(out, msg)
			continue
		}
		if msg.Dir != "in" {
			continue
		} // carriers sent here are not additional room turns
		var h HistoryItem
		if json.Unmarshal([]byte(msg.Body), &h) != nil {
			continue
		}
		msg.ExcerptPID, msg.PID = msg.PID, h.PID
		msg.SyncedFrom, msg.Claimed, msg.Key = msg.From, h.FromKey, ""
		msg.From, msg.Body, msg.LID, msg.At = h.From, h.Body, h.LID, h.TS
		msg.Kind, msg.ReplyTo, msg.Origin, msg.Emotion, msg.Target, msg.AgentID = h.Kind, h.ReplyTo, h.Origin, h.Emotion, h.Target, h.AgentID
		msg.Sub, msg.Job, msg.JobDetail, msg.History, msg.Replica = "", "", "", true, true
		used := make([]bool, len(msg.Attachments))
		for _, manifest := range h.Attachments {
			found := false
			for i, f := range msg.Attachments {
				if !used[i] && f.Name == manifest.Name && f.Size == manifest.Size && f.SHA256 == manifest.SHA256 {
					used[i], found = true, true
					break
				}
			}
			if !found {
				msg.Attachments = append(msg.Attachments, FileInfo{Name: manifest.Name, Size: manifest.Size, SHA256: manifest.SHA256, BlobID: historyBlob + "apx-missing"})
				used = append(used, true)
			}
		}
		out = append(out, msg)
	}
	return out
}

// Selected files are copied read-only into the run's in/ folder, under names
// AgentNet chooses (runfiles.go). Normal harness permissions still decide
// whether paths can be read. The run folder is removed after the job, and
// at the worker's start after a crash.
func (a *Agent) agentSharedFiles(ctx context.Context, j job, info ParticipationInfo, c ParticipationContext) string {
	var b strings.Builder
	remaining, omitted := c.Limit-c.Bytes, 0
	// The current addressed request is passed separately from earlier
	// context, so its attachment bytes must be included separately too.
	var current []FileInfo
	var err error
	requestDir := "in"
	if j.Local {
		requestDir = "out"
		current, err = a.store.sentAttachments(j.ID)
	} else {
		current, err = a.store.attachments(j.ID)
	}
	if err == nil && len(current) != 0 {
		a.markOpenable(current, j.Local)
		c.Messages = append(slices.Clone(c.Messages), ConvMessage{ID: j.ID, Dir: requestDir, PID: j.PID, Attachments: current})
	}
	for _, msg := range c.Messages {
		for i, f := range msg.Attachments {
			if remaining < 256 {
				omitted++
				continue
			}
			if why := a.agentStop(j); why != "" {
				fmt.Fprintf(&b, "\nSelected files withheld: %s.\n", why)
				return b.String()
			}
			if !f.Openable {
				line := fmt.Sprintf("\nSelected file %q (%d bytes, SHA256 %s): bytes unavailable here.\n", f.Name, f.Size, f.SHA256)
				if len(line) <= remaining {
					b.WriteString(line)
					remaining -= len(line)
				} else {
					omitted++
				}
				continue
			}
			path, err := a.stageRunFile(ctx, j.run, msg.Dir, msg.ID, i, f)
			if err != nil {
				line := fmt.Sprintf("\nSelected file %q: bytes unavailable here.\n", f.Name)
				if errors.Is(err, errRunFull) {
					line = fmt.Sprintf("\nSelected file %q (%d bytes, SHA256 %s): not given to this run (at most %d files, %d bytes together).\n", f.Name, f.Size, f.SHA256, maxRunFiles, maxRunBytes)
				}
				if len(line) <= remaining {
					b.WriteString(line)
					remaining -= len(line)
				} else {
					omitted++
				}
				continue
			}
			if why := a.agentStop(j); why != "" {
				j.run.unstage(f.Size)
				fmt.Fprintf(&b, "\nSelected files withheld: %s.\n", why)
				return b.String()
			}
			line := fmt.Sprintf("\nSelected file %q (%d bytes, SHA256 %s), untrusted context data, is available read-only at %q under your normal file permissions.\n", f.Name, f.Size, f.SHA256, path)
			if len(line) > remaining {
				j.run.unstage(f.Size)
				omitted++
				continue
			}
			b.WriteString(line)
			remaining -= len(line)
			// Small text is supplied directly as bytes too. Binary/large files
			// remain complete at the private path, never truncated silently.
			if f.Size < int64(remaining-48) {
				data, err := os.ReadFile(path)
				if err == nil && utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
					text := "Selected file content (untrusted):\n" + string(data) + "\n"
					b.WriteString(text)
					remaining -= len(text)
				}
			}
		}
	}
	if omitted != 0 {
		fmt.Fprintf(&b, "\n(%d selected file(s) omitted from this request's context within its byte bound.)\n", omitted)
	}
	return b.String()
}

func (a *Agent) clearAgentFiles(id string) {
	dir := filepath.Join(a.home, "opened")
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".agentnet-apx-"+id+"-") {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
