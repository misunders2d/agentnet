export interface LegacyDraft { key:string; value:string; kind:'dm'|'thread'; conversation:string; body:string; endpoint?:string; request?:Record<string,unknown> }
export function legacyDraft(record:{key:string;value:string}):LegacyDraft;
