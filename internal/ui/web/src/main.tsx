// Comic, AgentNet's default skin, as a skin package (docs/UI_SKINS.md).
// The UI host (loader.js) calls mount with a root element inside a shadow
// tree and its host API v1: every read and action goes through host.api,
// host.listen, host.file and host.stage, exactly like any other skin. Comic
// owns that root and nothing else: its theme, popups and focus stay there.
import { createRoot, type Root } from "react-dom/client";
import { CSPProvider } from "@base-ui/react/csp-provider";
import { App } from "./App";
import { StoreContext } from "./context";
import type { Host } from "./host";
import { OwnedContext, ownTheme } from "./owned";
import { Store } from "./store";
import { applySavedTheme } from "./features/Settings";
import { clearPictures } from "./features/Message.files";

let live = 0; // mounted roots
const mounted = new WeakMap<Element, { root: Root; store: Store; stopTheme: () => void; parts: HTMLElement[] }>();

export async function mount(root: HTMLElement, host: Host): Promise<void> {
  root.classList.add("an-root");
  root.lang = navigator.language || "en";
  const app = document.createElement("div");
  const portals = document.createElement("div");
  portals.className = "an-portals";
  root.append(app, portals);
  const stopTheme = ownTheme(root);
  applySavedTheme();
  const store = new Store(host);
  // A notification click opens its exact conversation; it never accepts
  // or approves anything (the host routes #conv=, #msg= and #review here).
  host.onOpen((target, kind, context) => {
    if (kind === "conversation" && /^[0-9a-f]{64}$/.test(target)) void store.open({ kind: "dm", id: target });
    else if (kind === "message" && target) void store.openMessage(target, context);
    else if (kind === "review") store.showTab("oks");
    else if (target) void store.openChannel(target); // a browser notification's channel
    else store.showTab("chats"); // news in more than one conversation
  }, ["conversation", "message", "review"]);
  live++;
  const r = createRoot(app);
  mounted.set(root, { root: r, store, stopTheme, parts: [app, portals] });
  r.render(
    <CSPProvider disableStyleElements>
      <OwnedContext.Provider value={{ root, app, portals }}>
        <StoreContext.Provider value={store}>
          <App />
        </StoreContext.Provider>
      </OwnedContext.Provider>
    </CSPProvider>,
  );
  void store.start();
}

export async function unmount(root: HTMLElement): Promise<void> {
  const m = mounted.get(root);
  if (!m) return;
  mounted.delete(root);
  m.store.stop();
  m.root.unmount();
  m.stopTheme();
  if (--live === 0) clearPictures(); // the pictures fetched (object URLs) go with the last mount
  for (const p of m.parts) p.remove();
}
