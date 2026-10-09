package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

const inboxOutputBytes = 7200 // below native lookup's 8000-character capture

type inboxFlags struct {
	unread, review, peek, asJSON, full *bool
	limit                              *int
	before, id, section                *string
}

func parseInbox(args []string) (*flag.FlagSet, inboxFlags, error) {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := inboxFlags{
		unread: fs.Bool("unread", false, "only unread messages"), review: fs.Bool("review", false, "items waiting for a decision (read-only)"),
		peek: fs.Bool("peek", false, "inspect without marking read"), asJSON: fs.Bool("json", false, "JSON array"), full: fs.Bool("full", false, "full content of one exact --id (read-only)"),
		limit: fs.Int("limit", 20, "maximum records, 1..100"), before: fs.String("before", "", "continue an earlier page"), id: fs.String("id", "", "inspect one exact id without marking read"), section: fs.String("section", "", "review section: requests, invites, groups, links, held, joined"),
	}
	if err := fs.Parse(args); err != nil {
		return fs, f, err
	}
	if fs.NArg() != 0 || *f.limit < 1 || *f.limit > 100 || *f.unread && *f.review || *f.full && *f.id == "" || *f.id != "" && *f.before != "" || *f.section != "" && *f.unread {
		return fs, f, errors.New("usage: inbox [--unread | --review] [--peek] [--json] [--limit 1..100] [--before CURSOR] [--id ID [--full]] [--section SECTION]")
	}
	return fs, f, nil
}

func inboxClip(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

type inboxSummary struct {
	client.InboxPageItem
	BodyTruncated        bool `json:"body_truncated,omitempty"`
	DetailTruncated      bool `json:"detail_truncated,omitempty"`
	AttachmentsOmitted   int  `json:"attachments_omitted,omitempty"`
	AttachmentsTruncated bool `json:"attachments_truncated,omitempty"`
}

func summarizeInbox(m client.InboxPageItem, full bool) inboxSummary {
	s := inboxSummary{InboxPageItem: m}
	if full {
		return s
	}
	s.Body, s.BodyTruncated = inboxClip(s.Body, 512)
	s.Detail, s.DetailTruncated = inboxClip(s.Detail, 256)
	s.ReplyTo, _ = inboxClip(s.ReplyTo, 128)
	s.Responder, _ = inboxClip(s.Responder, 128)
	if len(s.Attachments) > 2 {
		s.AttachmentsOmitted = len(s.Attachments) - 2
		s.Attachments = s.Attachments[:2]
	}
	s.Attachments = append([]client.FileInfo(nil), s.Attachments...)
	for i := range s.Attachments {
		var clipped bool
		s.Attachments[i].Name, clipped = inboxClip(s.Attachments[i].Name, 96)
		s.AttachmentsTruncated = s.AttachmentsTruncated || clipped
		s.Attachments[i].SavedPath, clipped = inboxClip(s.Attachments[i].SavedPath, 128)
		s.AttachmentsTruncated = s.AttachmentsTruncated || clipped
	}
	return s
}

func inboxMessageText(s inboxSummary, full bool) string {
	var out strings.Builder
	m := s.Message
	mark := " "
	if !m.Read {
		mark = "*"
	}
	kind := m.Kind
	if m.Sub != "" {
		kind += " " + m.Sub
	}
	if m.State != "" {
		kind += " [" + m.State + "]"
	}
	if m.Status != "" {
		kind += " (" + m.Status + ")"
	}
	fmt.Fprintf(&out, "%s %s  %s  %s  %s\n", mark, m.ID, termText(m.From, ""), m.SentAt.Format(time.DateTime), termText(kind, "  "))
	if s.Conv != "" {
		fmt.Fprintf(&out, "  conversation %s", s.Conv)
		if s.PID != "" {
			fmt.Fprintf(&out, "  participation %s", s.PID)
		}
		out.WriteByte('\n')
	}
	if m.ReplyTo != "" {
		fmt.Fprintf(&out, "  (reply to %s)\n", termText(m.ReplyTo, ""))
	}
	body := m.Body
	fmt.Fprintf(&out, "  %s\n", termText(body, "  "))
	if m.Detail != "" {
		fmt.Fprintf(&out, "  [%s] %s\n", detailLabel(m.State), termText(m.Detail, "  "))
	}
	if m.Status == envelope.StatusReviewNotice {
		fmt.Fprintf(&out, "  Decide on that device; agentnet resolve %s only closes this report here.\n", m.ID)
	}
	for _, f := range m.Attachments {
		fmt.Fprintf(&out, "  [file] %q %d bytes", f.Name, f.Size)
		if f.SavedPath != "" {
			fmt.Fprintf(&out, " saved %s", termText(f.SavedPath, ""))
		}
		out.WriteByte('\n')
	}
	if !full && (s.BodyTruncated || s.DetailTruncated || s.AttachmentsOmitted > 0 || s.AttachmentsTruncated) {
		fmt.Fprintf(&out, "  [summary; full item: agentnet inbox --id %s --full]\n", m.ID)
	}
	return out.String()
}

var inboxSections = []string{"requests", "invites", "groups", "links", "held", "joined"}

func inboxSectionTitle(section string) string {
	switch section {
	case "requests":
		return "requests to your agent that have not run:"
	case "invites":
		return "invitations for your agent (agentnet dm accept-agent PID, or dm decline-agent PID; nothing runs unless you accept):"
	case "groups":
		return "group invitations for you (agentnet group accept ID, or group decline ID):"
	case "links":
		return "devices asking to be linked to your person (agentnet person approve ID, or person refuse ID):"
	case "held":
		return "messages held here, not shown (nothing runs them):"
	case "joined":
		return "your agents here that joined without your accept (agentnet resolve PID dismisses the notice; dm dismiss-agent PID ends one):"
	}
	return ""
}

func inboxNoticeText(n client.InboxNotice, section string, full bool) string {
	var out strings.Builder
	detail := n.Detail
	if section == "held" {
		switch n.Kind {
		case "key_changed":
			detail = n.From + "'s key changed: run agentnet trust " + n.From + " once you verified the new key with them"
		case "identity_conflict":
			detail = "person record conflicts"
		case "conflicting_duplicate":
			detail = "conflicting duplicate"
		default:
			detail = "it could not be accepted; the recorded checks failed, so it stays held"
			if n.Detail == "participation_binding_mismatch" {
				detail = "an internal participation record does not match its sending device or conversation; it stays held"
			}
		}
	}
	if !full {
		var clipped bool
		detail, clipped = inboxClip(detail, 256)
		if clipped || n.DetailTruncated {
			detail += " [summary]"
		}
	}
	fmt.Fprintf(&out, "  %s  %s", termText(n.ID, ""), termText(n.From, ""))
	if n.Conv != "" {
		fmt.Fprintf(&out, " in %s", n.Conv)
	}
	if detail != "" {
		fmt.Fprintf(&out, ": %s", termText(detail, "    "))
	}
	out.WriteByte('\n')
	if full && n.Content != nil {
		raw, _ := json.MarshalIndent(n.Content, "  ", "  ")
		fmt.Fprintf(&out, "  %s\n", termText(string(raw), "  "))
	}
	return out.String()
}

func inboxNext(f inboxFlags, section, cursor string) string {
	cmd := "agentnet inbox --peek"
	if *f.review {
		cmd += " --review"
	}
	if *f.unread {
		cmd += " --unread"
	}
	if *f.asJSON {
		cmd += " --json"
	}
	if *f.limit != 20 {
		cmd += fmt.Sprintf(" --limit %d", *f.limit)
	}
	if section != "" {
		cmd += " --section " + section
	}
	if cursor != "" {
		cmd += " --before " + cursor
	}
	return cmd
}

func runInbox(a *client.Agent, args []string) error {
	_, f, err := parseInbox(args)
	if err != nil {
		return err
	}
	o := client.InboxPageOptions{Unread: *f.unread, Review: *f.review, Limit: *f.limit, Before: *f.before, ID: *f.id}
	var out bytes.Buffer
	var ids []string
	var next string
	if *f.section != "" {
		page, err := a.InspectInboxNotices(*f.section, o)
		if err != nil {
			return err
		}
		next = page.Next
		list := []client.InboxNotice{}
		if !*f.asJSON {
			fmt.Fprintln(&out, inboxSectionTitle(*f.section))
		}
		for i, n := range page.Items {
			if !*f.full {
				n.Content = nil
				n.Detail, n.DetailTruncated = inboxClip(n.Detail, 256)
			}
			line := []byte(inboxNoticeText(n, *f.section, *f.full))
			if *f.asJSON {
				line, err = json.Marshal(append(list, n))
				if err != nil {
					return err
				}
			}
			size := out.Len() + len(line)
			if *f.asJSON {
				size = len(line)
			}
			if !*f.full && size > inboxOutputBytes-800 {
				if i == 0 {
					return errors.New("inbox: notice metadata exceeds summary budget; inspect --id explicitly")
				}
				next = page.Items[i-1].Cursor
				break
			}
			if *f.asJSON {
				list = append(list, n)
			} else {
				out.Write(line)
			}
		}
		if *f.asJSON {
			raw, err := json.Marshal(list)
			if err != nil {
				return err
			}
			out.Write(raw)
			out.WriteByte('\n')
		}
	} else {
		page, err := a.InspectInbox(o)
		if err != nil {
			return err
		}
		next = page.Next
		list := []inboxSummary{}
		reserve := 800
		if *f.review && !*f.asJSON {
			reserve = 3000
		}
		for i, m := range page.Items {
			if !*f.asJSON && m.Sub == envelope.SubEvent {
				m.Body = eventLine(m.Body)
			}
			s := summarizeInbox(m, *f.full)
			if !*f.full {
				// Terminal and JSON escaping can expand controls considerably.
				// Keep even a single maximal body/file manifest inspectable.
				raw, err := json.Marshal(s)
				if err != nil {
					return err
				}
				if len(raw) > 3000 || len(inboxMessageText(s, false)) > 3000 {
					var clipped bool
					s.Body, clipped = inboxClip(s.Body, 128)
					s.BodyTruncated = s.BodyTruncated || clipped
					s.Detail, clipped = inboxClip(s.Detail, 64)
					s.DetailTruncated = s.DetailTruncated || clipped
					s.AttachmentsOmitted += len(s.Attachments)
					s.Attachments = nil
				}
			}
			line := []byte(inboxMessageText(s, *f.full))
			if *f.asJSON {
				line, err = json.Marshal(append(list, s))
				if err != nil {
					return err
				}
			}
			size := out.Len() + len(line)
			if *f.asJSON {
				size = len(line)
			}
			if !*f.full && size > inboxOutputBytes-reserve {
				if i == 0 {
					return errors.New("inbox: message metadata exceeds summary budget; inspect --id with --full")
				}
				next = page.Items[i-1].Cursor
				break
			}
			ids = append(ids, m.ID)
			if *f.asJSON {
				list = append(list, s)
			} else {
				out.Write(line)
			}
		}
		if *f.asJSON {
			raw, err := json.Marshal(list)
			if err != nil {
				return err
			}
			out.Write(raw)
			out.WriteByte('\n')
		}
		if *f.review && !*f.asJSON && *f.id == "" {
			for si, section := range inboxSections {
				p, err := a.InspectInboxNotices(section, client.InboxPageOptions{Limit: 1})
				if err != nil {
					return err
				}
				if len(p.Items) == 0 && p.Next == "" {
					continue
				}
				var block strings.Builder
				fmt.Fprintln(&block, inboxSectionTitle(section))
				if len(p.Items) > 0 {
					block.WriteString(inboxNoticeText(p.Items[0], section, false))
				}
				if p.Next != "" {
					fmt.Fprintf(&block, "  inspect %s: %s\n", section, inboxNext(f, section, p.Next))
				}
				// Reserve actual guidance bytes for every remaining section and
				// the message page before deciding whether this sample fits.
				reserve := 0
				if next != "" {
					reserve = len("More: " + inboxNext(f, "", next) + "\n")
				}
				for _, remaining := range inboxSections[si+1:] {
					reserve += len(fmt.Sprintf("  inspect %s: %s\n", remaining, inboxNext(f, remaining, "")))
				}
				if out.Len()+block.Len()+reserve <= inboxOutputBytes {
					out.WriteString(block.String())
				} else {
					fmt.Fprintf(&out, "  inspect %s: %s\n", section, inboxNext(f, section, ""))
				}
			}
		}
	}
	if next != "" {
		hint := "More: " + inboxNext(f, *f.section, next) + "\n"
		if *f.asJSON {
			fmt.Fprint(os.Stderr, hint)
		} else {
			out.WriteString(hint)
		}
	}
	if !*f.full && out.Len() > inboxOutputBytes {
		return errors.New("inbox: summary exceeds output budget")
	}
	if _, err := os.Stdout.Write(out.Bytes()); err != nil {
		return err
	}
	if !*f.peek && !*f.review && *f.section == "" && *f.id == "" {
		return a.MarkRead(ids)
	}
	return nil
}
