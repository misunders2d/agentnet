package ui

// heldWords is each recorded cause of a held notice in words: what is wrong,
// then what it means and who can act. The browser engine words the same
// (engine.mjs heldNoticeText; TestQuarantineCode compares them).
var heldWords = map[string][2]string{
	"envelope_malformed":              {"The received envelope has an invalid format.", "Its contents stay blocked. Archiving only hides this notice on this device."},
	"envelope_verification_failed":    {"The encrypted message could not pass validation on this device.", "Its signature, encrypted contents or bound fields could not be validated. Nothing was accepted or run."},
	"recipient_mismatch":              {"This envelope is addressed to another device.", "It was not accepted here. Archiving does not forward or resend it."},
	"recipient_identity_changed":      {"This device's identity changed while the message was being checked.", "It was not accepted here. Archiving does not forward or resend it."},
	"sender_key_unavailable":          {"This device could not obtain the sender's public key.", "The claimed sender has not been verified. Archiving does not trust an identity."},
	"history_malformed":               {"This history copy does not have a valid message or file manifest.", "The original retained copy stays blocked. Archiving does not import or run it."},
	"context_unavailable":             {"This message needs conversation, membership or person proof that is not available here yet.", "Checks continue when connected; the message appears only when verified. Archiving only hides this notice."},
	"admission_failed":                {"The message failed this device's conversation or sender checks.", "The precise check has no diagnostic code yet. The retained message stays blocked; archiving does not accept, resend or run it."},
	"participation_binding_mismatch":  {"An internal invitation or membership record does not match its sending device or conversation.", "It stays blocked and grants no access. Older AgentNet versions forward such records under the wrong device. Update AgentNet on the sending device to stop new copies."},
	"participation_invite_unresolved": {"It belongs to an agent or guest participation whose invitation this device can't resolve yet.", "It waits and runs nothing. Checks continue when invitations or membership change here."},
	"control_target_unknown_key":      {"This edit or deletion refers to a message signed by a device key that is no current member's.", "It changes nothing while it waits. Checks continue when membership changes here."},
	"group_context_unavailable":       {"This group record needs group context that this device hasn't verified yet.", "It waits and runs nothing. Checks continue when the group's latest state arrives."},
	"conversation_unavailable":        {"The conversation this record belongs to isn't on this device yet.", "It waits and runs nothing. It appears once the conversation arrives and verifies."},
	"control_not_author":              {"This edit or deletion is not from the person who sent the message.", "It was refused and changes nothing. Archiving does not accept it."},
	"history_forwarder_not_member":    {"This history copy came from a device that is not a verified original member.", "It stays blocked and grants no access. Check the group's membership with its administrator."},
	"recipient_not_current_member":    {"This group copy is addressed to a device key that is not a current member device.", "It stays blocked. Check this device's group membership with its administrator."},
	"participation_events_limit":      {"This conversation already holds the most participation events a device keeps.", "This record was not stored and grants nothing. Archiving does not accept it."},
	"group_invitation_outdated":       {"This invitation no longer matches the current group.", "If you still need to join, use the newer invitation or ask the inviter for a fresh one. You can archive this old notice."},
	"group_consent_mismatch":          {"This response does not match the invitation and decision recorded on this device.", "Check the group's current invitation. If you still need to join, ask its administrator for a fresh invitation."},
	"group_withdrawal_mismatch":       {"This departure record could not be verified against the current group and device identity.", "Check the group's current membership with its administrator. This record has not changed anyone's access."},
	"group_admission_unavailable":     {"The group admission named by this operation is withdrawn or unavailable.", "Check the group's membership and pending context. Archiving this notice does not restore access."},
	"group_authority_conflict":        {"The signed departure conflicts with the group's current administrator authority.", "Resolve the group authority or context with its administrator. Do not resend work automatically."},
	"group_conflicting_copy":          {"This copy conflicts with an existing logical group record.", "Ask the sender to check the original record and its status. Do not automatically resend requests."},
	"history_reader_not_member":       {"This history copy is not addressed to an original member's current linked device.", "Check the verified membership and device roster. A history copy cannot grant membership."},
	"captured_consent_mismatch":       {"The captured audience does not match the consent proof stored here.", "Check the participation's invitation and acceptance. This notice cannot grant consent."},
}

// heldBeforeOpen are the causes recorded before the envelope opened under
// the sender's key: such a copy only claims who sent it.
var heldBeforeOpen = map[string]bool{"envelope_malformed": true, "envelope_verification_failed": true, "recipient_mismatch": true, "recipient_identity_changed": true, "sender_key_unavailable": true}

func heldNoticeText(code, reason string) (string, string) {
	if w, ok := heldWords[code]; ok {
		return w[0], w[1]
	}
	if reason == "proof_pending" {
		return "", "You can archive this notice. Checks continue when connected; the message appears when verified."
	}
	if reason != "invalid" {
		return "", ""
	}
	return "The original detailed reason was not recorded or is unavailable.", "The retained message stays blocked. You can archive this notice locally; this does not accept, resend, or run it."
}

// heldSenderVerified: the copy opened under its sender's pinned key before
// it was held. Every proof wait and conflict is decided after that; an
// invalid copy only when its recorded cause is a later check. One without a
// known cause (stored before causes were) proves nothing about its sender,
// nor does the catch-all admission_failed by itself: a v0.8.16 browser
// stored its pre-open "local recipient identity changed" under it. Such a
// copy counts only with its logical record, which is kept once it opened.
func heldSenderVerified(reason, code, logical string) bool {
	switch reason {
	case "proof_pending", "identity_conflict", "conflicting_duplicate":
		return true
	case "invalid":
		_, known := heldWords[code]
		return known && !heldBeforeOpen[code] && (code != "admission_failed" || logical != "")
	}
	return false
}

// heldNoticeAction says who can act on a held notice's cause (Held*).
// Older producers make binding mismatches; a proof wait resolves only with
// evidence this device does not have yet. Nothing here can be done locally.
func heldNoticeAction(code, reason string) string {
	switch {
	case code == "participation_binding_mismatch":
		return HeldUpdateSender
	case code == "participation_invite_unresolved":
		return HeldWaitInvitation
	case reason == "proof_pending":
		return HeldWaitContext
	}
	return ""
}
