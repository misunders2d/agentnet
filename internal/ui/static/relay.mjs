// The relay's browser page. Browser devices are not available yet; this only
// says whether this browser has the features a device would use: keys that
// cannot be exported, IndexedDB and Web Locks. Having a feature is not proof
// that it works here (storage can be refused, cleared or full), and code
// from this origin can use such keys: it is not a readiness guarantee.
import { support } from "./wire.mjs";

const missing = globalThis.isSecureContext ? await support() : ["a secure (https) connection"];
if (typeof indexedDB === "undefined") missing.push("IndexedDB storage");
if (!navigator.locks) missing.push("Web Locks");
document.getElementById("support").textContent = missing.length
  ? "This browser could not hold an AgentNet device: it lacks " + missing.join(", ") + "."
  : "This browser has the features an AgentNet device would use (keys that cannot be copied out, IndexedDB, Web Locks). " +
    "Whether it can keep a device's data is only known once one is set up.";
