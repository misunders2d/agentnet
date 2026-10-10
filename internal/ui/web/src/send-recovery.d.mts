import type { Host } from "./host";
import type { JournalEntry } from "./send-journal.mjs";

/** Proves local admission, never delivery or execution. Missing proof returns false. */
export function isSendPersisted(
  host: Pick<Host, "api">,
  entry: Pick<JournalEntry, "id" | "conversation" | "endpoint" | "request">,
): Promise<boolean>;
