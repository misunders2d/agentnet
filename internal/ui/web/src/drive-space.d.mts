import type { DriveProvider } from "./host";
export function mountDriveSpace(container: HTMLElement, options: {
  conv: string; provider: DriveProvider; agents?: { pid: string }[]; openSettings: () => void;
}): { destroy(): void; refresh(): void };
export function saveAttachmentToDrive(provider: DriveProvider, conv: string, message: { id: string; dir: string }, index: number, confirm: boolean): Promise<unknown>;
