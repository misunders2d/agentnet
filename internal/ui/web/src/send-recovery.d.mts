import type { Host } from "./host";
import type { JournalEntry } from "./send-journal.mjs";

/** Proves local admission, never delivery or execution. Missing proof returns false. */
export function isSendPersisted(
  host: Pick<Host, "api">,
  entry: Pick<JournalEntry, "id" | "conversation" | "endpoint" | "request">,
): Promise<boolean>;

export type SavedSendState = "held" | "pending" | "sent" | "discarded";
/** The person's explicit retry of one saved request; a held message is never sent from here. */
export function replaySavedSend(
  host: Pick<Host, "api">,
  entry: Pick<JournalEntry, "id" | "conversation" | "endpoint" | "request">,
): Promise<SavedSendState>;
export function savedSendReceipt(state: SavedSendState): string;
