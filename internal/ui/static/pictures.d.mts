export const MaxPictureBytes: number;
export const MaxPicturePixels: number;
export function validatePicture(bytes: Uint8Array): void;
export function cleanPicturePNG(bytes: Uint8Array): Uint8Array;
export function pictureCrop(width: number, height: number, zoom?: number, x?: number, y?: number): { zoom: number; x: number; y: number; side: number };
export function pastePictures(event: { clipboardData?: DataTransfer | null; preventDefault(): void }, add: (files: File[]) => void, options?: { clipboardImage?: () => Promise<File | null>; insertText?: (text: string) => void; error?: (text: string) => void }): Promise<void> | undefined;
export function avatarPicture(overview: unknown, seed: string, name?: string): string;
export function openPictureEditor(options: { into: HTMLElement; save: (png: string) => Promise<unknown>; src?: string; current?: string }): Promise<boolean>;
