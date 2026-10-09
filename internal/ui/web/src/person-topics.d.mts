import type { T } from './api';
export interface PersonTopicEntry {
 key: string; conv: string; topic: string; title: string; last: string; count: number; unread: number;
 state: string; guests: number; root: T.DMSummary; native?: T.ThreadSummary; main?: T.ThreadSummary;
}
export function personRoots(overview: T.Overview | null, dm: T.DMThread | null): T.DMSummary[];
export function orderedPersonRoots(roots: T.DMSummary[]): T.DMSummary[];
export function personMainRoot(roots: T.DMSummary[]): string;
export function mainPreferences(workspace: string, storage: () => Storage | null): (roots: T.DMSummary[], person: string) => string;
export function personTopicEntries(roots: T.DMSummary[], views: Record<string, T.DMThread>, main?: string): PersonTopicEntry[];
