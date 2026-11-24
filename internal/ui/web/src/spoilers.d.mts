export const hiddenText: string;
export function spoilerRanges(text: string): {start: number; end: number}[];
export function replaceSpoilers(text: string, replacement: (secret: string) => string): string;
export function maskSpoilers(text: string): string;
