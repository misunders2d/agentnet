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

// ProtectHome=true hides /home, /root, and /run/user from the managed units and
// PrivateTmp=true replaces /tmp and /var/tmp, so a runtime executable under any
// of them is unusable by the services even when privileged setup can read it.
// Kept here so the launcher digest and the Python preflight refuse the same set.
export const SERVICE_HIDDEN_ROOTS = Object.freeze([
  "/home",
  "/root",
  "/run/user",
  "/tmp",
  "/var/tmp",
]);

export function serviceVisiblePath(resolved, hiddenRoots = SERVICE_HIDDEN_ROOTS) {
  return !hiddenRoots.some(
    (root) => resolved === root || resolved.startsWith(`${root}/`),
  );
}

export function requireServiceVisiblePath(
  resolved,
  label,
  hiddenRoots = SERVICE_HIDDEN_ROOTS,
) {
  if (!serviceVisiblePath(resolved, hiddenRoots)) {
    throw new Error(
      `installed ${label} executable is hidden by the managed service sandbox`,
    );
  }
  return resolved;
}

export function forwardedSignals(platform) {
  if (!supportedPlatform(platform)) {
    throw new Error(`AgentNet does not support host platform: ${platform}`);
  }
  return platform === "win32"
    ? Object.freeze(["SIGINT", "SIGTERM", "SIGBREAK"])
    : Object.freeze(["SIGINT", "SIGTERM", "SIGHUP"]);
}
