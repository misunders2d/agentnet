export interface JournalEntry {
 id: string;
 conversation: string;
 endpoint: "/api/send" | "/api/dm/send" | "/api/dm/agent/ask" | "/api/act" | "/api/request/followup";
 request: Record<string, unknown>;
 createdAt: string;
 error?: string;
}
export function readPending(storage: Pick<Storage,"getItem">, workspace: string): JournalEntry[];
export function saveBatch(storage: Pick<Storage,"getItem"|"setItem">, workspace: string, entries: JournalEntry[]): string[];
export function acknowledge(storage: Pick<Storage,"getItem"|"setItem">, workspace: string, id: string): void;
export function recordFailure(storage: Pick<Storage,"getItem"|"setItem">, workspace: string, id: string, error: string): void;
export function onJournalChange(fn: () => void): () => void;

export interface SavedDraftOwner {workspace:string;handle:string;endpoint:string;realm:string;address:string;fingerprint:string}
export interface SavedDraftText {text:string;replyTo?:string;agent?:string;topic?:string;newTopic?:boolean;plainReply?:boolean;mediaMode?:"original"|"smaller"}
export interface SavedSendDraft {version:1;id:string;conversation:string;owner:SavedDraftOwner;draft:SavedDraftText;revision:string;hadFiles:boolean}
export function savedSendDrafts(storage: Storage, draftsKey: string): {
 read(conv:string,owner?:SavedDraftOwner):{draft:SavedDraftText;revision:string};
 all(owner?:SavedDraftOwner):Record<string,{draft:SavedDraftText;revision:string}>;
 write(conv:string,draft:SavedDraftText,revision?:string):string;
 prepare(id:string,conv:string,owner:SavedDraftOwner,draft:SavedDraftText & {files?:unknown[]},revision:string):SavedSendDraft;
 pending(id:string,conv:string,owner:SavedDraftOwner):SavedSendDraft|undefined;
 entries(owner:SavedDraftOwner):SavedSendDraft[];
 restore(id:string,conv:string,owner:SavedDraftOwner):{draft:SavedDraftText;revision:string}|undefined;
 release(id:string,conv:string,owner:SavedDraftOwner):void;
 replaced():{conversation:string;revision:string;draft:SavedDraftText}[];
 releaseReplaced(conv:string,revision:string):void;
 nextRevision():string;
  olderWriterSeen(): boolean;
 isStorageKey(key:string|null):boolean;
};
