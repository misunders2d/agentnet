export function mountFileStorageOptions(container: HTMLElement, options: {
  provider: { storageSetup(request: { action?: string; [key: string]: unknown }): Promise<unknown> };
}): { destroy(): void };
