import type { Api, T } from "./api";

export interface TopicPreview {
  action: Parameters<Api["changeTopic"]>[0];
  change: T.TopicChange;
  original: string;
}
export const topicVersion = (t: T.ThreadSummary) => JSON.stringify([t.id, t.conv, t.peer, t.title, t.state, t.count, t.last_at, t.agent_host, t.agent_key, t.agent_id, t.agent_legacy]);

export function presentTopic(t: T.ThreadSummary, p: TopicPreview): T.ThreadSummary {
  if (topicVersion(t) !== p.original) return t;
  const patch = p.action === "rename" ? { title: p.change.title || t.auto_title || t.title }
    : p.action === "done" ? { state: "done" }
    : p.action === "reopen" ? { state: "active" }
    : p.action === "archive" ? { state: "archived" } : {};
  return { ...t, ...patch };
}
