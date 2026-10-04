# Zoom reference skin

Zoom is a complete standalone API-v1 package: Everyone → person/device →
conversation → message, with its own markup, styles, helpers and QR code.
Copy this directory, change only skin.json ID/name, run ./build.sh OUTPUT.
The build regenerates its manifest module. No source, runtime, build import
or symlink points at Classic or Comic. Duplicate helpers are package-owned.

Entry exports mount(root, host)/unmount(root). It uses host.skin (manifest
fallback), api, stage, file, listen, onOpen, skins/selectSkin and optional
onSkinsChange, manageLocalSkins, reconnect, drive, workspaces. The requested
additive shapes await the host owner's documented production integration.
No private globals, raw transport or loader DOM. Queries/styles own root.

Zoom alone renders hierarchy and write dialogs. Hidden draft controls support
its saved drafts, attachment staging and existing action/setup dialogs; no
other skin renderer runs. State namespaces derive from current skin identity.
Captured hosts keep operations on their original membership. Unmount stops
subscriptions/listeners/timers and frees preview URLs. Google access remains
a public provider responsibility; absent provider reports unavailability.

Demo controls are conditional parity for agentnet ui --demo. Normal data never
shows the invented-people notice. Core remains authority for actions, keys,
consent and receipts; this package grants none. Message content uses text nodes.

Checks: ./build.sh; node --check src/entry.mjs; go test ./internal/ui/skins/zoom;
Zoom-specific runtime/renderer checks in internal/ui/testdata/zoom_*.cjs use
only disposable synthetic homes. No real daemon, personal data or Google login.
