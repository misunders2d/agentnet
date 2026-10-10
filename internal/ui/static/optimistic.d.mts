export function sendID(): string;
export function groupedSends<T>(messages: T[]): {m: T; compact: boolean}[];
export function pendingSends(host: object, changed?: () => void): {
 begin(key: string, message: object, retry?: () => void): string;
 has(id: string): boolean;
 move(id: string, key: string): void;
 ready(id: string): Promise<void>;
 finish(id: string, result?: {id?: string; lid?: string; state?: string; detail?: string}): void;
 fail(id: string, reason: string): boolean;
 remove(id: string): void;
 unsent(): boolean;
 dispose(): void;
 merge<T>(key: string, messages?: T[]): T[];
};
