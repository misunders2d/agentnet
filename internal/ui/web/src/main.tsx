// The default messenger interface. The UI host (loader.js) calls mount with
// a root element and its host API v1 (docs/UI_SKINS.md): every read and
// action goes through host.api, host.listen, host.file and host.stage,
// exactly like an installed interface.
import { createRoot, type Root } from "react-dom/client";
import { CSPProvider } from "@base-ui/react/csp-provider";
import { App } from "./App";
import { StoreContext } from "./context";
import type { Host } from "./host";
import { Store } from "./store";
import { applySavedTheme } from "./features/Settings";

const mounted = new WeakMap<Element, { root: Root; store: Store }>();

export async function mount(root: Element, host: Host): Promise<void> {
  applySavedTheme();
  const store = new Store(host);
  // A notification click opens its exact conversation; it never accepts
  // or approves anything (the host routes #conv= and #msg= links here).
  host.onOpen((target, kind) => {
    if (kind === "conversation" && /^[0-9a-f]{64}$/.test(target)) void store.open({ kind: "dm", id: target });
    else if (kind === "message" && target) void store.openMessage(target);
    else if (kind === "review") store.showTab("oks");
  });
  const r = createRoot(root);
  mounted.set(root, { root: r, store });
  r.render(
    <CSPProvider disableStyleElements>
      <StoreContext.Provider value={store}>
        <App />
      </StoreContext.Provider>
    </CSPProvider>,
  );
  void store.start();
}

export async function unmount(root: Element): Promise<void> {
  const m = mounted.get(root);
  if (!m) return;
  m.store.stop();
  m.root.unmount();
  mounted.delete(root);
}
