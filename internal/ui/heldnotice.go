package ui

func heldNoticeText(code, reason string) (string, string) {
	switch code {
	case "envelope_malformed":
		return "The received envelope has an invalid format.", "Its contents stay blocked. Archiving only hides this notice on this device."
	case "envelope_verification_failed":
		return "The encrypted message could not pass validation on this device.", "Its signature, encrypted contents or bound fields could not be validated. Nothing was accepted or run."
	case "recipient_mismatch":
		return "This envelope is addressed to another device.", "It was not accepted here. Archiving does not forward or resend it."
	case "sender_key_unavailable":
		return "This device could not obtain the sender's public key.", "The claimed sender has not been verified. Archiving does not trust an identity."
	case "history_malformed":
		return "This history copy does not have a valid message or file manifest.", "The original retained copy stays blocked. Archiving does not import or run it."
	case "context_unavailable":
		return "This message needs conversation, membership or person proof that is not available here yet.", "Checks continue when connected; the message appears only when verified. Archiving only hides this notice."
	case "admission_failed":
		return "The message failed this device's conversation or sender checks.", "The precise check has no diagnostic code yet. The retained message stays blocked; archiving does not accept, resend or run it."
	case "participation_binding_mismatch":
		return "An internal invitation or membership record does not match its sending device or conversation.", "AgentNet could not synchronize this record. It stays blocked and grants no access."
	case "group_invitation_outdated":
		return "This invitation no longer matches the current group.", "If you still need to join, use the newer invitation or ask the inviter for a fresh one. You can archive this old notice."
	case "group_consent_mismatch":
		return "This response does not match the invitation and decision recorded on this device.", "Check the group's current invitation. If you still need to join, ask its administrator for a fresh invitation."
	case "group_withdrawal_mismatch":
		return "This departure record could not be verified against the current group and device identity.", "Check the group's current membership with its administrator. This record has not changed anyone's access."
	case "group_admission_unavailable":
		return "The group admission named by this operation is withdrawn or unavailable.", "Check the group's membership and pending context. Archiving this notice does not restore access."
	case "group_authority_conflict":
		return "The signed departure conflicts with the group's current administrator authority.", "Resolve the group authority or context with its administrator. Do not resend work automatically."
	case "group_conflicting_copy":
		return "This copy conflicts with an existing logical group record.", "Ask the sender to check the original record and its status. Do not automatically resend requests."
	case "history_reader_not_member":
		return "This history copy is not addressed to an original member's current linked device.", "Check the verified membership and device roster. A history copy cannot grant membership."
	case "captured_consent_mismatch":
		return "The captured audience does not match the consent proof stored here.", "Check the participation's invitation and acceptance. This notice cannot grant consent."
	case "control_target_person_mismatch":
		return "This edit or deletion is for a message another person sent.", "Only the person who sent a message edits or deletes it. It stays blocked and changed nothing; archiving only hides this notice."
	default:
		if reason == "proof_pending" {
			return "", "You can archive this notice. Checks continue when connected; the message appears when verified."
		}
		if reason != "invalid" {
			return "", ""
		}
		return "The original detailed reason was not recorded or is unavailable.", "The retained message stays blocked. You can archive this notice locally; this does not accept, resend, or run it."
	}
}
