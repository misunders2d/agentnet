// The relay's browser page. Browser devices are not available yet; this only
// says whether this browser could hold a device: keys that cannot be
// exported, storage for them, and one tab owning the connection.
import { support } from "./wire.mjs";

const missing = globalThis.isSecureContext ? await support() : ["a secure (https) connection"];
if (typeof indexedDB === "undefined") missing.push("IndexedDB storage");
if (!navigator.locks) missing.push("Web Locks");
document.getElementById("support").textContent = missing.length
  ? "This browser could not hold an AgentNet device: it lacks " + missing.join(", ") + "."
  : "This browser has what an AgentNet device needs: keys that cannot be copied out, and storage for them.";
