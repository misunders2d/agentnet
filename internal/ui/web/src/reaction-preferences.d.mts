type PreferenceStorage = () => Pick<Storage, "getItem" | "setItem"> | null;
export function recordReaction(emoji: string, storage?: PreferenceStorage): void;
export function reactionChoices(fallback?: string[], count?: number, storage?: PreferenceStorage): string[];
export function humanReactionOrder<T extends { by?: { assistant?: boolean }[] | null }>(reactions: T[]): T[];
