package client

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const heldNoticeSchema = `ALTER TABLE quarantine ADD COLUMN detail_code TEXT NOT NULL DEFAULT '';
ALTER TABLE quarantine ADD COLUMN notice_archived INTEGER NOT NULL DEFAULT 0;`

// heldLogicalSchema keeps each held copy's logical record (heldLogicalKey).
// Rows held before it keep it empty and are counted by envelope size.
const heldLogicalSchema = `ALTER TABLE quarantine ADD COLUMN logical TEXT NOT NULL DEFAULT '';`

// heldLogicalKey names the logical record a held copy carries, so a notice
// counts records, not envelopes: an older producer re-sending one item under
// fresh envelope and carrier IDs makes copies of one key. It hashes
// identifiers only (conversation, author address and key, logical or
// original ID), never content, and only groups notices: it admits, dedupes
// and authorizes nothing. History is named by its carried original, so
// copies forwarded by different own devices share it. The browser engine
// computes the same (engine.mjs heldLogicalKey; vectors in
// internal/ui/testdata/held_logical.json).
func heldLogicalKey(in envelope.Inner, senderFP string) string {
	fields := []string{"direct", in.Conv, in.Sub, in.From, senderFP, cmp.Or(in.LID, in.ID)}
	if in.Sub == envelope.SubHistory {
		var h struct {
			From    string `json:"from"`
			FromKey string `json:"from_key"`
			ID      string `json:"id"`
			LID     string `json:"lid"`
			Sub     string `json:"sub"`
		}
		if json.Unmarshal([]byte(in.Body), &h) == nil {
			fields = []string{"history", in.Conv, h.Sub, h.From, h.FromKey, cmp.Or(h.LID, h.ID)}
		}
	}
	sum := sha256.Sum256([]byte("agentnet/held-logical/v1\x00" + strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// Only exact audited diagnostic constants are persisted. Arbitrary errors may
// contain received content; an unmapped cause stays explicitly unknown.
func heldDiagnosticCode(why string) string {
	switch why {
	case "malformed envelope":
		return "envelope_malformed"
	case "envelope verification failed":
		return "envelope_verification_failed"
	case "addressed to another device":
		return "recipient_mismatch"
	case "sender key unavailable":
		return "sender_key_unavailable"
	case ErrGroupContextPending.Error():
		return "group_context_unavailable"
	case "named participation has no unambiguous invitation", "external output history has no unambiguous invitation",
		"outside traffic has no invitation proof yet", "external invitation proof is incomplete or conflicting",
		"outside host has no verified invitation root", "human turn waits for its original invitation root":
		return "participation_invite_unresolved"
	case "the target's sender key is no member's (yet)":
		return "control_target_unknown_key"
	case "the conversation is not here (yet)":
		return "conversation_unavailable"
	case "group: only verified original members forward historical participation ends":
		return "history_forwarder_not_member"
	case errGroupRecipientNotCurrent.Error():
		return "recipient_not_current_member"
	case errTooManyEvents.Error():
		return "participation_events_limit"
	case "a malformed history item", "group: malformed/nonordinary historical item", "group: history file is not a manifest":
		return "history_malformed"
	case "participation: the event is not the sending device's own, for this conversation":
		return "participation_binding_mismatch"
	case ErrGroupInvitationStale.Error(), errGroupInvitationOutdated.Error(), "group: target already has effective membership":
		return "group_invitation_outdated"
	case "group: decision conflicts with recorded local intent", "group: consent has no recorded local invitation", "group: consent is not the exact local invitation and current person":
		return "group_consent_mismatch"
	case "group: invalid withdrawal binding", "group: departure descriptor/signing author differs", "group: withdrawal signing roster is not latest locally pinned roster", "group: withdrawal device signature invalid or removed":
		return "group_withdrawal_mismatch"
	case "group: fresh self admission is withdrawn", "group: forwarded target has a withdrawn or pending admission":
		return "group_admission_unavailable"
	case "group: withdrawal conflicts with promoted admin; explicit authority/context resolution required", "group: departure conflicts with current promoted administrator":
		return "group_authority_conflict"
	case "group: conflicting logical turn", "group: conflicting departure logical carrier", "group: lifecycle logical conflict":
		return "group_conflicting_copy"
	case "human history remains with original members' own linked devices":
		return "history_reader_not_member"
	case "human: captured consent differs from local proof":
		return "captured_consent_mismatch"
	case controlOtherPerson:
		return "control_target_person_mismatch"
	default:
		return ""
	}
}

func heldFailureCode(reason, why string) string {
	if code := heldDiagnosticCode(why); code != "" {
		return code
	}
	// This stage is known at new admission boundaries; it does not invent a
	// specific historical cause or persist arbitrary error/received text.
	// context_unavailable stays the catch-all for unmapped proof waits, as
	// rows stored before the precise codes keep it.
	if why != "" {
		if reason == reasonProof {
			return "context_unavailable"
		}
		if reason == reasonInvalid {
			return "admission_failed"
		}
	}
	return ""
}

// ArchiveHeldNotice hides a local notice without deleting or admitting the
// envelope. Pending proof remains in the normal retry queue; keeping quarantine
// is also essential to the receive deduplication gate.
func (a *Agent) ArchiveHeldNotice(id string) error {
	if !protocol.ValidID(id) {
		return errors.New("invalid held-message ID")
	}
	return a.store.archiveHeldNotice(id)
}
func (s *store) archiveHeldNotice(id string) error {
	result, err := s.db.Exec(`UPDATE quarantine SET notice_archived=1 WHERE id=? AND reason IN ('invalid','proof_pending')`, id)
	if err == nil {
		count, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if count == 0 {
			return ErrNoMessage
		}
	}
	return s.done(err)
}
