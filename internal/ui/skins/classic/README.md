# Classic reference skin

Classic is a complete standalone skin, with its own source and declared assets.
Copy this directory, change only the manifest ID/name, then run
`./build.sh OUTPUT`. The build regenerates package-owned `manifest.mjs`;
`host.skin` supplies the current identity, with that manifest as fallback.
There is no source or build dependency on Comic or Zoom. `src/entry.mjs` exports
`mount(root, host)` and `unmount(root)`; its state and queries belong to that root.
The build copies only this skin's sources into an installable package. AgentNet
can embed that same package; it is accepted by the installed custom-skin loader.
Production built-in registration belongs to the host integration change.

Contract: Host API v1 in `docs/UI_SKINS.md`. The additive capabilities below are
requested from the host owner; final integration must use their documented shape.
Classic uses api (existing view/action
routes), listen, stage, file, onOpen (conversation/message/review), skins,
selectSkin, onSkinsChange, skin, workspace and optional workspaces.state/select.
Optional reconnect preserves the exact native membership after daemon restart;
optional drive retains explicit project-space consent; manageLocalSkins draws
the browser-local package importer. No engine, loader DOM, private globals or
raw transport is used. Helpers/QR code and CSS are package-owned relative assets.
Notifications and messages grant no authority; returned capabilities/actions
remain the core's authority. Message content is rendered using text nodes.

Classic keeps workspace drafts under `workspaces.state(id)[skinID]` and pending
send activity under `[skinID + "Pending"]`; local reload drafts use
`agentnet.<skinID>.reload.<workspace>`. Local identity prefixes are normalized.
Unmount stops streams,
listeners and timers. Pending sends retain their captured host. Files require
sending/removal or explicit leave confirmation before a reload/switch.

Checks: `node --check src/entry.mjs`; `./build.sh`; Go package/contract checks
and rendered journeys in `internal/ui/testdata/classic_*`. Tests use isolated
synthetic homes, never a real daemon or personal AgentNet profile.

Demo controls remain for `agentnet ui --demo` parity: they are visible only
when the public overview reports `demo: true`, and use public `/api/simulate`.
Normal native/browser data never shows the invented-people notice.
