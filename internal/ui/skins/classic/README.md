# Classic reference skin

Classic is a complete standalone skin, with its own source and declared assets.
Copy this directory, change the manifest ID/name, and update the corresponding
`classic`/`local:classic` IDs in `src/entry.mjs` (including workspace and reload
state namespaces). Then run `./build.sh OUTPUT`.
There is no source or build dependency on Comic or Zoom. `src/entry.mjs` exports
`mount(root, host)` and `unmount(root)`; its state and queries belong to that root.
The build copies only this skin's sources into an installable package. AgentNet
can embed that same package; it is accepted by the installed custom-skin loader.
Production built-in registration belongs to the host integration change.

Contract: Host API v1 in `docs/UI_SKINS.md`. The additive capabilities below are
requested from the host owner; final integration must use their documented shape.
Classic uses api (existing view/action
routes), listen, stage, file, onOpen (conversation/message/review), skins,
selectSkin, onSkinsChange, workspace and optional workspaces.state/select.
Optional reconnect preserves the exact native membership after daemon restart;
optional drive retains explicit project-space consent; manageLocalSkins draws
the browser-local package importer. No engine, loader DOM, private globals or
raw transport is used. Helpers/QR code and CSS are package-owned relative assets.
Notifications and messages grant no authority; returned capabilities/actions
remain the core's authority. Message content is rendered using text nodes.

Classic keeps workspace drafts under `workspaces.state(id).classic` and pending
send activity under `.classicPending`; local
reload drafts use `agentnet.classic.reload.<workspace>`. Unmount stops streams,
listeners and timers. Pending sends retain their captured host. Files require
sending/removal or explicit leave confirmation before a reload/switch.

Checks: `node --check src/entry.mjs`; `./build.sh`; Go package/contract checks
and rendered journeys in `internal/ui/testdata/classic_*`. Tests use isolated
synthetic homes, never a real daemon or personal AgentNet profile.
