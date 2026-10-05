export const MaxPictureBytes: number;
export const MaxPicturePixels: number;
export function validatePicture(bytes: Uint8Array): void;
export function avatarPicture(overview: unknown, seed: string, name?: string): string;
export function openPictureEditor(options: { into: HTMLElement; save: (png: string) => Promise<unknown>; src?: string; current?: string }): Promise<boolean>;
