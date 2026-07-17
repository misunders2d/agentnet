import path from "node:path";

export const SUPPORTED_PLATFORMS = Object.freeze(["linux", "darwin", "win32"]);

export function supportedPlatform(platform) {
  return SUPPORTED_PLATFORMS.includes(platform);
}

export function platformStateRoot(platform, environment, homeDirectory) {
  if (!supportedPlatform(platform)) {
    throw new Error(`AgentNet does not support host platform: ${platform}`);
  }
  if (platform === "linux") {
    return environment.XDG_STATE_HOME || path.posix.join(homeDirectory, ".local", "state");
  }
  if (platform === "darwin") {
    return path.posix.join(homeDirectory, "Library", "Application Support");
  }
  const localAppData = environment.LOCALAPPDATA;
  if (!localAppData || !path.win32.isAbsolute(localAppData)) {
    throw new Error("AgentNet on Windows requires an absolute LOCALAPPDATA directory");
  }
  return localAppData;
}

export function forwardedSignals(platform) {
  if (!supportedPlatform(platform)) {
    throw new Error(`AgentNet does not support host platform: ${platform}`);
  }
  return platform === "win32"
    ? Object.freeze(["SIGINT", "SIGTERM", "SIGBREAK"])
    : Object.freeze(["SIGINT", "SIGTERM", "SIGHUP"]);
}
