import type { T } from "./api";

/** The rail and Outbox sections share attention, not a count of stored work.
 * Drafts and pending schedules have no unread/attention state. */
export function outboxAttention(overview: Pick<T.Overview, "remind" | "reminders"> | null | undefined) {
  const reminders = overview?.remind ? (overview.reminders || []).filter(r => r.overdue).length : 0;
  return { scheduled: 0, drafts: 0, reminders, total: reminders };
}
