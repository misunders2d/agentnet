export interface JournalEntry {
 id: string;
 conversation: string;
 endpoint: "/api/send" | "/api/dm/send" | "/api/dm/agent/ask" | "/api/act";
 request: Record<string, unknown>;
 createdAt: string;
 error?: string;
}
export function readPending(storage: Pick<Storage,"getItem">, workspace: string): JournalEntry[];
export function saveBatch(storage: Pick<Storage,"getItem"|"setItem">, workspace: string, entries: JournalEntry[]): void;
export function acknowledge(storage: Pick<Storage,"getItem"|"setItem">, workspace: string, id: string): void;
export function recordFailure(storage: Pick<Storage,"getItem"|"setItem">, workspace: string, id: string, error: string): void;
