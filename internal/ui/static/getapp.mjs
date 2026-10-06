// Getting the AgentNet app: which device this page runs on, and where the
// app's installers are. The table is getapp.json, the same file Go reads
// (static/getapp.go), so the page and the program never disagree.

// detectPlatform names the device from the browser's own report: android,
// iphone, ipad, windows, macos, linux, or "" when it cannot tell. platform
// is navigator.userAgentData.platform where the browser has it; iPadOS
// reports itself as a Mac, with a touch screen.
export function detectPlatform(ua, platform, maxTouchPoints) {
  const u = String(ua || ""), p = String(platform || "");
  if (/Android/i.test(u) || /^Android$/i.test(p)) return "android";
  if (/iPhone|iPod/.test(u)) return "iphone";
  if (/iPad/.test(u) || (/Macintosh/.test(u) && Number(maxTouchPoints) > 1)) return "ipad";
  if (/CrOS/.test(u) || /Chrome OS/i.test(p)) return "";
  if (/^Windows$/i.test(p) || /Windows/.test(u)) return "windows";
  if (/^macOS$/i.test(p) || /Macintosh|Mac OS X/.test(u)) return "macos";
  if (/^Linux$/i.test(p) || /Linux|X11/.test(u)) return "linux";
  return "";
}

// isPhone: the app comes to the home screen (this page, installed),
// not as a download.
export const isPhone = (platform) => platform === "android" || platform === "iphone" || platform === "ipad";

const releaseTag = /^v[0-9]+\.[0-9]+\.[0-9]+$/;

// downloads lists the installers for version, as Go's static.Downloads
// does: that release's files for vX.Y.Z, otherwise the latest release's.
export function downloads(table, version) {
  const base = table.releases + (releaseTag.test(String(version || "")) ? "/download/" + version + "/" : "/latest/download/");
  return table.platforms.map((p) => ({ id: p.id, label: p.label, asset: p.asset, url: base + p.asset }));
}

// forPlatform is the installer offered first on platform: the AppImage
// for Linux (it runs on any distribution), otherwise the one of the same id.
export function forPlatform(list, platform) {
  return list.find((d) => d.id === platform) || null;
}

// appLink is the link that opens the installed app on a code or page
// destination: agentnet://open#… (protocol.AppOpenURL).
export const appLink = (fragment) => (fragment ? "agentnet://open#" + fragment : "agentnet://open");
