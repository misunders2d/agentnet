package client

import (
	"errors"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const heldNoticeSchema = `ALTER TABLE quarantine ADD COLUMN detail_code TEXT NOT NULL DEFAULT '';
ALTER TABLE quarantine ADD COLUMN notice_archived INTEGER NOT NULL DEFAULT 0;`

// Only exact audited diagnostic constants are persisted. Arbitrary errors may
// contain received content; an unmapped cause stays explicitly unknown.
func heldDiagnosticCode(why string) string {
	switch why {
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
	default:
		return ""
	}
}

// ArchiveHeldNotice hides a local notice without deleting or admitting the
// envelope. Keeping quarantine is essential to the receive deduplication gate.
func (a *Agent) ArchiveHeldNotice(id string) error {
	if !protocol.ValidID(id) {
		return errors.New("invalid held-message ID")
	}
	return a.store.archiveHeldNotice(id)
}
func (s *store) archiveHeldNotice(id string) error {
	result, err := s.db.Exec(`UPDATE quarantine SET notice_archived=1 WHERE id=? AND reason='invalid'`, id)
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
