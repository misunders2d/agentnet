# Skins

AgentNet is the core: the program (the daemon on this computer, or the
browser engine a relay serves) and the skin contract documented here, host
API v1. Every interface is a **skin**: a standalone package that draws the
whole interface (navigation, layout, conversations, dialogs, settings and
flow) on that contract. A skin uses the same data and actions as any other;
it does not change membership, encryption, delivery or permissions.

Anyone can build a skin. The built-in ones are packages too, loaded through
exactly the same path; only trust differs (see [Trust](#trust)).

For a reusable creator starting point, examples and independent build/test
commands, use [agentnet-skins](https://github.com/misunders2d/agentnet-skins).
This document remains the authoritative Host API v1 contract. A collection
skin must produce a self-contained package and pass the contract and runtime
checks; adding a skin requires no AgentNet source change or relay-wide rollout.

Shared skins should show what people will install. Include current, real
rendered previews in the repository's README or linked gallery, covering
desktop and phone layouts and supported light/dark appearances. Use fictional
people, companies and messages. Invent names; never borrow names, photos or
identities from the owner's coworkers or contacts, even for synthetic messages.
Keep private workspace data, keys and fixture URLs out.
Label concept mockups as concepts, never as previews of a working package.
The agentnet-skins collection requires previews before listing a skin as
available. Visual previews complement the compatibility checks below; they
do not replace them or belong inside the installable package.

Give people a reason to choose a new skin: a distinct layout, interaction
style, or coherent playful experience. Changing colors and button corners
alone is a theme variant. Collection examples should demonstrate what a
complete skin can do, including purposeful transitions where they fit the
design. Keep those transitions fast, interruptible and optional through
reduced-motion preferences; preserve readable content and familiar action
labels. Passing compatibility tests does not establish a successful design.

- **Comic** (`comic`) is AgentNet's own skin, the default and the only
  built-in one: the messenger, built from `internal/ui/web` into the package
  `internal/ui/static/skins/comic/` and embedded in the program.
- **Classic** and **Zoom** (`classic`, `zoom`) are packages in the
  [agentnet-skins](https://github.com/misunders2d/agentnet-skins) collection,
  installed like any other skin.
- Installed skins: packages placed next to the program (below), and
  browser-local skins imported into one browser.

This is the trusted package boundary for skins. The [skin
gallery](#skin-gallery) discovers and downloads the published packages of one
fixed collection and verifies them against its catalog; there is no other
marketplace, no signing beyond that catalog's hashes, no sandboxing and no
billing. Distribution is client-owned: a person can install, choose and share
a skin without changing their relay or other clients.

## How a skin is loaded

The UI host (`internal/ui/static/loader.js`) does the same for every skin:

1. reads the catalog (`/assets/skins/index.json`: the built-in packages first,
   then installed ones) and the packages stored in this browser. When the
   catalog fails or has not answered within 1.5 seconds of its request, the
   host also asks for Comic's manifest from its fixed built-in path
   (`/assets/skins/comic/skin.json`) and opens with whichever of the two
   answers first; lateness alone never fails the page. Comic opened from its
   own manifest is trusted by the host's list as always; a saved choice is
   then kept for the next load, and the catalog fills `host.skins` if it
   arrives later;
2. picks the skin: `?skin=<id>`, else the one chosen before in this browser,
   else Comic. A saved `default` or `classic` (the names before skins were
   packages) opens Comic and is rewritten once; choices made through the package loader
   are marked separately so choosing an installed Classic package survives reload. `?skin=default` names Comic;
3. asks for trust unless the skin is built in;
4. adopts the package's [document rules](#document-rules-fonts) at document
   level, waiting for them at most a moment (rules that arrive later are
   adopted then);
5. gives the skin a root in a shadow tree of the page's `#skin` element, links
   the host's base sheet and then the package's stylesheet inside it;
6. imports the entry and calls `mount(root, host)`; on a workspace switch (or
   a rebind) it calls `unmount(root)` and mounts again on a fresh root over a
   host bound to the new membership.

Comic draws its own way to switch (Settings → Appearance → Skin) and its
own workspace menu. Every other skin gets the host's **switcher bar** above
it: one step back to Comic, the skin menu (Comic, then installed and
browser-local skins, with import and removal), and the workspace control
(switch, join, leave, reconnect). The bar sits in the page's flow above the
skin's box, never over it, in its own shadow tree, so no skin stylesheet can
hide or restyle it; notifications the skin does not take wait there with a
button that opens them in Comic. This keeps the way back reachable for a
conforming skin; a skin's code still runs with the page's full trust, so this
is a conformance expectation, not a security guarantee.

### Trust

A built-in skin is trusted by the host's own fixed list of ids (`comic`),
never by anything a manifest says. No installed or browser-local package may
use those ids or `default`, or show as a built-in skin (the name Comic, in
any case or spacing): the program
skips such a directory and the browser refuses such a package. Every list of
skins says where each one comes from (built in, installed on this computer,
stored in this browser). The rules live in one browser module,
`/assets/skin-choice.mjs` (which skin opens, its trust, the reserved ids and
names), shared by the host and the browser-local package manager; Go tests
pin it to `static/skins.go`. Any other skin asks first:
it can read your chats and act as you, including sending messages and
approving work. Consent is to the package's exact digest, per browser origin
(including the daemon's port); a changed package asks again.

Installed code has the same access as Comic. This is **not** a sandbox.
Installing on a relay makes the package available to every browser device
using that relay; the relay operator already controls the page served to
those devices. Never install an untrusted package or let another account
write these directories.

## Install and update a skin

### On this computer or a relay

Place a package in `<home>/skins/<id>/` for a laptop daemon, or
`<hub-data>/skins/<id>/` for a relay's browser page, and restart the serving
process. No Go changes, rebuild or changes to embedded files are needed.

On Unix, the skins directory, package directories and files must belong to
the serving user and must not be writable by group or others. Keep the
home/data folder private. On Windows, access control comes from the home
folder's inherited ACL; AgentNet does not inspect Windows skin ACLs
separately.

Choose it in Comic under **Settings → Appearance → Skin**, or open the page
with `?skin=<id>` after authenticating. The first use asks for trust.

**Update:** replace the package's files and restart the serving process.
Packages are immutable snapshots until restart, so replacing files never
swaps code underneath an open page; the new digest asks for trust again.
**Uninstall:** remove the directory and restart; a browser that had chosen it
opens Comic.

The consent digest covers the manifest and every declared file. Requests
cannot read undeclared files, paths outside the package, or symlinks.
Limits: 32 packages, 32 files per package, 4 MiB per file, 16 MiB per
package and 64 MiB for the catalog.

### Import into this browser

In Comic under **Settings → Appearance → Skin**, or from the switcher's
**Import or remove skins…**, choose a package folder, or select its
`skin.json` and declared files. Import validates and stores the package; it
does not execute it or upload anything. Choose it under Skin, review the
trust notice and digest, then choose **Use this skin**. Browser-local IDs
have a `local:` prefix, so a relay package cannot replace a local selection.

**Update:** import the new version the same way; its new digest asks for
trust again. Tabs already using the version they loaded keep it until they
reload. **Remove** deletes every version of that package in this browser.

The browser's CacheStorage keeps immutable package snapshots. Its existing
service worker serves only the reserved `/local-skins/<digest>/` asset path;
chat and API requests keep their normal transport. Relative imports and CSS
remain inside the selected version. The 64 MiB local limit includes retained
versions and manifest bytes. Clearing browser site data or storage eviction
can remove packages; Comic remains. Packages and consent belong to this
browser profile and origin, including the daemon port, not to a workspace or
relay.

This requires a secure context with Service Workers, CacheStorage, Web Locks
and WebCrypto. Unsupported browsers say that skins cannot be stored there.
Choosing a local skin explicitly activates the asset worker; importing alone
does not take over tabs. No marketplace or package download service is
involved.

### Skin gallery

The gallery offers the skins of the
[agentnet-skins](https://github.com/misunders2d/agentnet-skins) collection
inside AgentNet, so nobody needs GitHub to find, preview or install one.
Comic shows it under **Settings → Appearance → Skin gallery**; any skin may
draw its own through `host.gallery`.

- **One fixed source.** The host reads only
  `https://api.github.com/repos/misunders2d/agentnet-skins/releases/latest`
  (the latest *published* release: drafts and pre-releases are ignored) and
  files under
  `https://raw.githubusercontent.com/misunders2d/agentnet-skins/<that tag>/published/`.
  The page's content security policy allows exactly these two besides its
  own origin (`static.GalleryConnectSources`), on this computer's program
  and the relay page. The v0.8.25 Android APK has an additional WebView
  interceptor that blocks these sources; its gallery is unavailable until
  an APK correction ships. The Android browser page is unaffected.
  Requests carry no credentials or referrer and
  refuse redirects. Work in progress on the repository's `main` never
  reaches the gallery: only a published release does.
- **Verified bytes.** `published/catalog.json` names, for every skin, each
  file's path, size and SHA-256, its previews (PNG or WebP, at most six of
  1 MiB) and the package's consent digest (as computed under
  [Trust](#trust)). The host checks every file and preview against the
  catalog and the whole package against its digest before anything is
  stored; a mismatch stores nothing.
- **Compatibility.** A skin whose `api` is not 1, or whose `agentnet` names
  a release newer than the one serving this page (`overview.version`),
  is listed as incompatible and cannot be installed. An unstamped
  development build never counts as older, and neither does a page that
  has not read an overview through `host.api` yet.
- **Stored like an import.** Installing uses the browser-local package
  manager: the same limits, `local:` ids and per-browser, per-origin
  storage. Choosing the skin asks for trust with its exact digest; an
  update stores a new digest, so it asks again. Nothing is uploaded.

Catalog format (`published/catalog.json`, written by the collection's
publish step, never by hand):

```json
{
  "catalog": 1,
  "skins": [{
    "id": "pulp", "name": "Pulp", "description": "…", "version": "1.0.0",
    "api": 1, "agentnet": "v0.8.25", "digest": "<64 hex>",
    "files": [{"path": "skin.json", "sha256": "<64 hex>", "size": 412}, …],
    "previews": [{"path": "previews/desktop-light.png", "sha256": "<64 hex>", "size": 81234, "alt": "Pulp on a desktop, light"}]
  }]
}
```

Package files live at `published/<id>/<path>`. A malformed entry is left out
without hiding the others; a malformed catalog fails as a whole.

## Manifest

`skin.json` (an illustration: the real Notebook example in
`examples/skins/notebook/` declares only `entry.mjs` and `style.css`):

```json
{
  "api": 1,
  "id": "notebook",
  "name": "Notebook example",
  "entry": "entry.mjs",
  "style": "style.css",
  "document": "document.css",
  "files": ["entry.mjs", "style.css", "document.css", "fonts/body.woff2"]
}
```

IDs use lowercase letters, digits and hyphens, starting with a letter, at
most 48 characters; `name` is at most 80 bytes and is not a built-in
skin's name (see [Trust](#trust));
`comic` and `default` are reserved. The optional
`agentnet` field names the oldest AgentNet release (vX.Y.Z) the package
supports (`"agentnet": "v0.8.25"`); the program and browser import both
treat an empty or `null` value as absent. Where it is checked:

- A program on this computer or a relay leaves an installed package that
  names a newer release than its own out of its catalog (a relay compares
  with its own build version). A development build stamped from a release
  counts as that release; an unknown running version (an unstamped
  development build) never leaves one out.
- The [skin gallery](#skin-gallery) (`host.gallery`) refuses to install a
  package whose catalog entry names a release newer than the page's
  `overview.version`.
- Browser import checks only that the field is a vX.Y.Z release; it does
  not compare it with the running release.
- `host.selectSkin` refuses a package that names a release newer than the
  page's `overview.version`, once the page has read an overview through
  `host.api`. A saved choice, `?skin=<id>` and the host's switcher do not
  check the field.
- Releases up to v0.8.24 ignore the field.

Name the release whose contract the package was checked against (see
[Versions and capabilities](#versions-and-capabilities)). `files` lists every
file the package uses (at most 32, no duplicates, never `skin.json`
itself); only declared JS/MJS, CSS,
JSON, PNG, SVG, WebP and WOFF2 files are served. `entry` (JS/MJS) and the
optional `style` and `document` (CSS) must appear in `files`. Unsupported API
versions and invalid packages are omitted. Package-owned imports and assets
use relative paths (resolve them against `import.meta.url` in code, or
relative to the stylesheet in CSS); the one host module a skin may import is
`/assets/typing.mjs` (`mountTyping`, the shared typing presenter). There is
no CDN loading or evaluation of inline code: the page's CSP allows no inline
styles or scripts, so use the stylesheet, never `style` attributes in markup.

### Document rules (fonts)

Browsers ignore `@font-face` and `@property` inside a shadow tree. A package
that needs them declares a `document` stylesheet: the host reads it, keeps
**only** its `@font-face` and `@property` rules, resolves every `url()`
against the file and drops a rule whose URL leaves the package, and adopts
the result at document level before mounting. A stalled request holds the
mount for at most 1.5 seconds: the skin then shows in fallback fonts and the
rules are adopted when they arrive (a failure seen before mounting still stops
the skin). Anything else in that file is ignored. Comic declares its Onest and Rubik faces and Tailwind's registered
properties this way.

### The root and its styles

The entry exports `async function mount(root, host)` and optionally
`async function unmount(root)`. The skin owns `root` and nothing outside it:

- `root` lives in a shadow tree of `#skin`, under the host's base sheet
  (`/assets/skin-base.css`: `core.css` in the lowest cascade layer, so the
  package's own rules win, except over `core.css`'s one `!important` rule,
  `[hidden] { display: none }`) and the package's stylesheet. Semantic
  colors such as `--surface`, `--text`, `--muted`, `--accent` and `--danger`
  inherit from the page; a package can replace them.
- The skin's box fills the page under the switcher (or the whole page for
  Comic) and scrolls inside itself; the page never scrolls.
- The page is the part of the screen a phone's keyboard leaves visible.
  Android shrinks the page itself (the page's viewport has
  `interactive-widget=resizes-content`). iOS shrinks only the visual
  viewport, so there the host sets two custom properties on `<html>`, which
  inherit into the shadow tree: `--an-viewport-h`, the page's height while
  the keyboard is open, and `--an-keyboard`, how much of the screen's bottom
  it covers. Both are absent while nothing covers the page and while the
  page is pinch-zoomed. Size the skin
  from `root` (`height: 100%` down to the frame), never from `vh`, `dvh` or
  `svh`, so the message box stays above the keyboard; a fixed bottom popup
  (a bottom sheet) sits at `bottom: var(--an-keyboard, 0px)` and is no
  taller than `var(--an-viewport-h, 100dvh)`. Both names are the host's
  own: a skin reads them and never sets them.
- Put theme, tokens and resets on `root` (`:root` and `html`/`body` rules do
  not match in a shadow tree). Render popups (menus, dialogs, sheets) into a
  container inside `root`. Read focus from `root.getRootNode().activeElement`,
  query from `root`, never `document`. Do not write to `document.body`,
  `<html>` (`lang`, classes, styles), the page's adopted style sheets or font
  set, or anything else outside `root`, and do not read the host's own page
  globals.
- A modal dialog is modal inside `root`: mark it `aria-modal="true"`, make
  the rest of the skin behind it `inert`, and keep Tab and Shift+Tab within
  it. Page-level scroll locks and `aria-hidden` on the page's other elements
  (what many dialog libraries do in their fully modal mode) write outside
  `root`. Comic does this for its sheets and confirmations (`owned.tsx`
  `useModal`); its picture editor (`pictures.mjs`) is a native
  `showModal()` dialog.
- `unmount` stops what `host.listen` returned, timers, object URLs and every
  `window` or `document` listener the skin added, and leaves `root` empty.
  Use DOM text nodes for message and profile content.

The shadow tree is styling isolation, not a security boundary: the code
still runs with the page's full trust.

## Host API v1

| Method | Contract |
| --- | --- |
| `host.version` | `1`; additive extensions keep this version. What the running release offers is in [Versions and capabilities](#versions-and-capabilities). |
| `host.platform` | `daemon` (this computer's program: the desktop app or `agentnet ui`), `browser` (a page a relay serves, with its in-browser engine) or `android` (the Android app). Android's pages are served by the same Go UI server as `daemon` (`internal/ui`, embedded in the app's Go core), so the daemon routes apply there too, with the Android refusals [HOST_API_ROUTES.md](HOST_API_ROUTES.md) names, plus `host.android` and `host.onBack`. `daemon` alone never proves the desktop app (see the app methods). |
| `host.api(path, body?)` | The JSON `/api/*` read/action surface: every route, its platforms, request and answer are listed in [HOST_API_ROUTES.md](HOST_API_ROUTES.md). Omit body for GET; provide an object for POST. Resolves parsed JSON or rejects with an `Error` whose `message` is a user-readable sentence. On this computer's program and Android a rejection also carries `status`, the HTTP status: 400 `bad request` for an unknown body field, malformed JSON or an empty body; 404 for an unknown path or a provider's "not available here"; 405 for the wrong method; 409 for a refusal, whose sentence you show as written. Only the 409 message `stale or disconnected workspace` means the program retired this binding (see `host.reconnect`). In a browser, `status` (when present) is the relay's own status (0: unreachable), `code` names some engine refusals, an unknown path rejects with `Unknown request.`, and neither the HTTP method nor unknown body fields are checked: test a skin on both. A skin never fetches `/api` or `/events` itself; the one exception is a `picture_url` from a view, used only as an `<img src>` (a `blob:` URL in a browser, a host-qualified `/api/picture/<hash>` here), never built by the skin. |
| `host.listen(fn)` | Push events; returns an unsubscribe function. Reload data on `change`; do not poll. This computer's program and Android send one `{type:"change",seq}` as soon as the stream opens and one after changes (changes that come together are sent as one, so `seq` can skip), `{type:"restart"}` only when the daemon serving the page switches to an updated program (the desktop app and Android update as a whole: the app restarts, or Android's installer replaces it, and the page opens again on the new release) and `{type:"disconnect"}` when the stream fails; events carry `workspace`, the bound membership's id. After `restart` or `disconnect` the host has closed the stream: call `host.listen` again. A browser sends only `change` (also when its relay stream drops or returns: read `overview.device.online` for connectivity) and nothing at subscription, so load once in `mount` as well. |
| `host.onOpen(fn, kinds?)` | Required. Registers notification routing: `fn(target, kind, context)`. Every skin gets `"channel"` (a browser notification's channel: resolve it with GET `/api/notify/resolve?chan=…`, a browser-only route, which answers `{conv}` when this device has that conversation; an empty channel means news in more than one or, on this computer's program and Android, where channels are always empty, open the list) and `"conversation"` (a DM id). Listing `"message"` in `kinds` adds `fn(messageID, "message", {conv?, dir?})` (the conversation and direction the notification names) and `"review"` adds `fn("", "review")`. Listing `"topic"` adds `fn(conv, "topic", {topic})`, where `topic` is explicit `"main"` or the exact 32-hex topic id; listing `"thread"` adds `fn(rootID, "thread", {})` for an exact legacy conversation root. These handlers may return a promise and must reject an unavailable destination without substituting another chat. Portable landing reads already admitted local views and does not call the shared-history refresh route. A destination a skin does not take waits in the switcher with the way to open it in Comic. A missing destination opens the skin's list, never another arbitrary chat. |
| `host.android` | Present only when `platform` is `android`; the Android app's own actions, each a promise: `connection()` → `{enabled, badging?}` (read the saved connection channel’s badge choice), optional `connectionSettings()` → `{opened}` (open only this app’s Background connection notification category from a foreground click), and `setConnection(enabled)` (keep the background connection), `requestNotifications()` and read-only `notificationStatus()` → `{granted, app_granted?, channel?: {importance, sound, vibration}}`. `granted` includes app permission and channel blocking; the optional fields report existing OS choices, without changing them. Optional `notificationSettings()` → `{opened}` opens this app's Chat activity channel settings from a foreground click; it accepts no caller-selected channel/package and preserves saved choices. `copyText(text)` (Android's system clipboard; only while the app is in the foreground, at most 128 KiB), `legacyDrafts()` → `{drafts[{key,value}]}` and `acknowledgeLegacyDraft(key, value)` (drafts saved by the earlier Android preview), `scanQR()` → `{text}` or `{canceled: true}`. `visible()` says whether the app is in the foreground and `onVisibility(fn)` calls `fn(visible)` each time the app reports its state, repeats included (for example after an installer result): re-read what may have changed (returns an unsubscribe function). The WebView's own `document.visibilityState` can lag the app, so treat the page as seen only when both say visible. |
| `host.onBack(fn)` | Optional on Android and browser pages. Registers Back handling: `fn()` returns `true` after closing the top sheet or going up a level; `false` lets Back leave the app. The browser host keeps one history guard, including across reload, and returns to an earlier document from the root. A directly opened tab with no earlier document stays usable. Returns an unsubscribe function; the latest registration wins. |
| `host.appControlsAvailable` | Optional presentation hint. `false` means this page has no installed-app bridge; skip app status/update calls unless the current overview explicitly reports `app: true`. Omission preserves legacy host detection. This flag grants no authority. |
| `host.clipboardImage()` | Optional, native shell only. Resolves `File` for a clipboard image or `null` when there is none. Call only for a user paste that contains no browser image file; ordinary text paste stays available. Capture this host callback for the paste operation; standalone skins never read native clipboard globals themselves. |
| `host.appStatus()` | Desktop-app capability, present on every host: it rejects where there is no app (a browser rejects with "Open the AgentNet app for this computer's settings."; `agentnet ui` rejects with the message `404 page not found`, without `status`). Android answers status, check and update but rejects `appReplaceCommand`; an Android build updated through its app store rejects all four. Show app controls only after `appStatus()` resolved; never decide from the methods' presence. Resolves `{cli_path, cli_state:"installed"\|"custom"\|"error", cli_problem?, version, app_update_supported, problem?, update_result?}`. The location/state describe this computer's AgentNet command; `problem` explains unavailable app updates. Omit unavailable controls. |
| `host.appCheckUpdate()` | Optional desktop-app action, called only from an explicit Check for updates choice. Resolves `{version, latest, state:"available"\|"current"\|"ahead"}` using the app's own version and the latest published stable release. Lookup/comparison errors reject; they never mean up to date. Checking does not install, pause work or change the server recommendation, and works even when this installation cannot update itself. Keep results separate from installation success; discard completions after leaving the view/workspace. Omit this control on browser hosts or when `appStatus()` rejected. |
| `host.appUpdate()` | Optional desktop-app action, called from the person's Update choice. Resolves `{state, message}`: `restarting` after the desktop app prepared the update (the app restarts when ready); `complete` (with `version`) when the app already runs that release, or there the state an earlier update recorded (`pending`, `failed` or `partial`); `installer` on Android, whose package installer then asks the person. Otherwise it rejects with a user-readable error. Never treat any state as proof that the restart or installation completed. |
| `host.appReplaceCommand()` | Optional desktop-app action, called only after explicit confirmation to replace a custom AgentNet command. Resolves the updated command fields `{cli_path, cli_state:"installed"\|"custom"\|"error", cli_problem?}`. Merge these fields into the previous app status, or read `appStatus()` for the complete status. Opening or canceling confirmation never calls this method. |
| `host.stage(file)` | Prepares a File for sending; resolves an opaque attachment value for `files` in a send. Keep that value only until that send consumes it. Browser encrypts through its engine; daemon stages locally. |
| `host.prepareVideo(file,signal)` | Optional sender-only native preparation, before `host.stage`. Resolves a new MP4 `File`; never changes the original, stages a send or sends bytes to the relay. Available through a captured daemon workspace binding only when local Linux tools support it; reject/unavailable preserves the original. Abort with the supplied `AbortSignal`. A skin may first use local browser codecs, then this fallback, and must retain an explicit exact-original choice. |
| `host.file(messageID,index,dir)` | Opens an attachment this device holds: a received one, or a copy kept of one it sent. Pass the message's own `dir` (`in` or `out`): a received id is the sender's choice and can equal a sent one here. Resolves `{bytes:Uint8Array}` (browser may add name/size/image). Show Open only where the view says `openable: true`; a sent file without a kept copy says so in `note`. Validate magic bytes before inline display; never execute HTML or SVG. |
| `host.drive` | Project-space provider bound to this host's membership, present on every host (presence says nothing about configuration): `drive({conv, action, ...})` resolves the existing Drive view/result; `driveUpload(conv, file, confirm)` uploads plaintext to Google only after explicit confirmation. It reuses the core's Drive actions, consent and permissions. Browser providers also expose `prepareGoogle()` and `beginGoogleConsent({conv, full?, confirm_account})`: prepare first, then call begin directly from the confirming click, with no intervening await, to retain the browser user gesture. Never stage this upload through `host.stage`; Google files are outside AgentNet encryption. Switching workspace never retargets a captured provider; retired/disconnected bindings reject new calls. No tokens or raw transport are exposed. Absence/configuration errors must be shown as unavailable. |
| `host.skins` | The catalog: `{api, id, name, entry, style, document, files, agentnet?, digest?, local?, builtin?}` (browser-local entries add `package_id` and `size`) for Comic, the other built-in, installed and browser-local skins. `builtin` is the host's word (its fixed list), never a manifest's. The array is updated in place. |
| `host.onSkinsChange(fn)` | Calls `fn()` when `host.skins` changes (a browser-local skin imported or removed). Returns an unsubscribe function. |
| `host.selectSkin(id)` | Reloads into a skin from `host.skins`. It throws, without reloading, for an unknown id or a package that needs a newer AgentNet (see [Manifest](#manifest)). The current skin must preserve or explicitly resolve unsent drafts first. |
| `host.controls` | Optional for external skins. `open(trigger?)` opens the host's existing skin/workspace/Comic recovery controls in a native dialog and restores focus to `trigger`. A skin exposing this action in its existing Settings calls `claim(root)` with its exact mounted root and keeps the returned teardown until unmount; this hides the host's floating fallback. Claims from stale roots or workspaces are ignored. Unmount, failed mount and remount restore fallback access. Comic already supplies its own controls and receives no extra surface. |
| `host.manageLocalSkins(root)` | Present on every host; where the browser cannot store skins it draws only its heading, its introduction and "This browser cannot store skins." Draws the host's browser-local skin manager (import files or a folder, the stored skins, Remove) into an element the skin owns; returns its teardown, which empties that element. Consent stays the host's: a stored skin asks for trust when chosen. |
| `host.gallery` | The [skin gallery](#skin-gallery), present wherever the host has it: `list({fresh?})` resolves `{release, skins[{id, name, description, version, api, agentnet?, state, installed, previews, alts}]}` (`state`: `install`, `installed`, `update` (a newer digest is published) or `incompatible` (it needs another Host API or a newer AgentNet than the one serving this page); `installed`: this browser's catalog id for a stored version, else `""`); `preview(id, index)` resolves one verified preview as a `Blob` (show it with an object URL and revoke it); `install(id)` downloads, verifies and stores the package in this browser and resolves its catalog id (`local:<id>`), which the skin passes to `host.selectSkin` (trust is then asked as for any stored skin). Every method rejects with a sentence to show: unreachable, busy, no published release, a file that did not match, a skin that needs a newer AgentNet, or a browser that cannot store skins. It never selects a skin or grants trust. |
| `host.reconnect()` | Optional, present on this computer's program with workspaces and on Android. It works once the skin has read `/api/overview` (with or without a query) through this host, which proves the membership's identity; otherwise it rejects, as it does for a membership whose `realm` is absent or not 32 hex characters. After the program restarted and retired this membership's handle, binds the same membership again (only when the program still names it with the endpoint, realm, address and key it proved in its own overview) and mounts the skin again over the new binding. Resolves without remounting when the binding still holds. Nothing under way is retargeted or replayed: staged files and sends stay with the old binding, so a skin keeps text drafts and asks for files again. |
| `host.workspace` | The membership this host is bound to: `{id, handle, name, endpoint, address, realm?, state, hub_name?}` (`realm` may be absent; a browser's never has `hub_name`, read `overview.workspace.name` instead). `name` is this device's own label for it (`""` for none); `hub_name` is the workspace's own name its admin set, as last listed. A host never changes membership: what a skin holds when an operation starts (a send, a staged file, a file open) stays bound to it. |
| `host.linkTo(target)` | Optional. Returns a portable `agentnet://open#realm=<32hex>&conv=<64hex>&topic=<main-or-32hex>` for `{conv, topic}` (Main is always explicit), or `agentnet://open#realm=<32hex>&thread=<32hex>` for `{thread}`. `thread` names an existing exact legacy conversation root, never a later message or a peer-wide chat, and cannot be mixed with `conv` or `topic`. The realm comes from this host’s locally recorded workspace identity; a missing identity or malformed target throws a readable error. Names, device-local aliases, endpoint URLs, ports, credentials and message content are absent. |
| `host.openLink(value)` | Optional. Opens that exact portable URI or its `#realm=...` fragment after checking its strict fields, exactly one enrolled matching realm, connected local binding, and accessible local destination. Resolves after the skin’s handler; rejects with a shared host error if invalid, ambiguous or unavailable. Validation occurs before selecting or remounting a workspace; failures preserve its draft, agent choice and staged files. A skin that does not list `"topic"` or `"thread"` gets an explicit **Open it in Comic** choice retaining the exact destination, without an automatic workspace or skin switch. An explicit click on a portable anchor uses the same host path. No enrollment, join, approval, send, replay or shared-history fetch follows from a link. |
| `host.workspaces` | `null` on a program without workspaces. Otherwise `{list(), active(), has(id), select(id), state(id), onChange(fn), join({name, invite, agent, id?}), disconnect(id)}`. `onChange(fn)` calls `fn({id, previous, host, state})` and returns an unsubscribe function; the host then mounts the skin again, so a skin need not use the event's `host` (a bound transport, not a full host). `join` resolves the new membership's bound transport; a rejection may carry `retryID`: offer Retry, which calls `join` again with the same fields and `id: retryID`. A browser cannot disconnect the workspace it was enrolled from (`disconnect` rejects). `has(id)` is by local registration only, never by anything a message or notification says. Optional, present on this computer's program and on Android (never in a browser enrollment), so check before use: `disconnected()` resolves the memberships disconnected here, each `{id, name, endpoint, address, realm, state}` (`list()` leaves them out); `reconnect(id)` routes one of them again, as the same membership with the same keys and history under a new handle, and refuses one whose identity changed. It does not select it. Optional, present where the host can keep it (this computer's program, Android, or a browser with its enrollments): `rename(id, name)` sets this device's own label of a connected membership and resolves its updated entry; `name` `""` clears it; any other name that is not 1–120 readable characters (no control character) is refused. It renames nothing on the server and fires no `onChange`. |

Native `change` events may carry optional `hint:"typing"` or `hint:"presence"`. A skin may refresh only typing for the first, and overview plus typing for the second; neither requires reloading chat history. Missing or unknown hints require the ordinary full refresh. The daemon retains separate monotonic revisions internally, so a coalesced message/edit is always an ordinary change even when followed by typing. Existing skins may ignore hints and keep reloading normally.

**Naming a workspace.** Show a workspace by, in this order: this device's own
label (`name`, unless it is a placeholder such as `""`, "Current workspace",
"This computer" or "This server"); the workspace's own name its admin set
(`overview.workspace.name` for the current one, `hub_name` for others); the
relay's host name (`overview.workspace.server`, or the endpoint's host);
"AgentNet" only when none of these says anything. Comic and the host's bar
say the same.

The app methods reach the computer-wide app routes (`GET /api/app/status`,
`GET /api/app/check`, `POST /api/app/update` with `{}`, `POST /api/app/cli`
with `{replace:true}`). In the desktop app they go through its native bridge
(only from its main window, on the page it opened, with that page's session
cookie), then the routes' own checks. On Android they go to the app's
updater. Without a bridge the page fetches the routes, which check the exact
Host and UI session cookie; POST also requires the exact Origin, a JSON
content type and same-origin fetch metadata when supplied. These are computer-wide operations,
not membership APIs. A standalone skin uses the host captured when the
operation starts, never `host.api`, direct fetches, native globals or a later
workspace's host for these actions. Switching memberships does not send an
app action to a remote workspace. Handle rejection: neither the methods'
presence nor `host.platform === "daemon"` proves the native app is present.

### Workspaces

When the person selects another workspace (in the skin's own menu or the
host's switcher) the host mounts the skin again with a host bound to that
membership: `module.unmount(root)` (optional) is called first, then
`module.mount(newRoot, newHost)`. Keep drafts in
`host.workspaces.state(host.workspace.id)`, keyed there by conversation type
and id (Comic also keeps text drafts per workspace in this browser's
storage, so they survive a reload and a switch of skin). The same
conversation id in two memberships still names separate drafts; shared
labels never merge people or workspaces. Stop what `host.listen` returned in
`unmount`. An operation started before the switch keeps its host: a send
under way goes where it was written, a staged file belongs to the workspace
it was staged in (the host refuses it elsewhere).

Disconnected memberships (BUG-19): `GET /api/workspaces` lists the mounted
memberships only, each with its `handle`. This computer's program and
Android list every membership with its `state` at `GET /api/workspaces/all` (a
disconnected one has `state: "disconnected"` and no handle) and routes one
again with `POST /api/workspaces/reconnect` and body `{"id": "<its id>"}`
(JSON, same origin, at the page's root, never under a membership's
`/workspaces/<id>/<handle>/` prefix). The answer is the membership bound again
under a new handle (`state: "enrolled"`); 404 says no disconnected
membership has that id. `host.api` is bound to one membership's prefix, so
a skin reaches these two through `host.workspaces.disconnected()` and
`host.workspaces.reconnect(id)` (above), present on this computer's program
and on Android; the host's switcher offers Reconnect over every skin but Comic, which
draws its own workspace menu.

A notification fragment is `#conv=<hash>&workspace=<id>`,
`#msg=<id>[&conv=<hash>][&dir=in|out][&workspace=<id>]`, `#review[&workspace=<id>]`
or `#workspace=<id>` alone (news in that workspace: the handler gets
`fn("", "channel")`, which opens its list): the host verifies the workspace id against its
registrations, selects that workspace, then calls the `onOpen` handler; an
unknown id opens nothing (never the current workspace instead).

The host handles the authenticated transport, browser engine and enrollment.
It does not interpret receipt states as task completion, silently approve
anything, or automatically send messages. All effects still require normal
core checks. A skin should use returned actions/capabilities to decide which
controls exist. A browser has no local responder or daemon reminders.

What the routes mean for a skin, by area. Every route's exact path,
platforms, request and answer are in [HOST_API_ROUTES.md](HOST_API_ROUTES.md)
(`TestHostAPIRoutesDocumented` checks its route tables, see
[Versions and capabilities](#versions-and-capabilities)). The full view
types are the Go structs in `internal/ui/*.go`, generated into
`internal/ui/web/src/api.gen.ts`; Comic's typed client is
`internal/ui/web/src/api.ts` and `model.ts`:

- Reads: `/api/overview`, `/api/thread?id=...`, `/api/dm?id=...`.
  Private `overview.model_reports` are last reported runtime observations, not
  settings or availability. Match exact `host`, `host_key` and `agent_id`
  (empty identifies an unnamed legacy executor, not today's concrete default),
  and show `model`, optional `reasoning` and `at` in
  own-agent picker/details and active agent chat. Missing model/reasoning is
  not reported, never inferred from names/configuration; a report can be stale
  even while the host is online. No report grants access or controls a harness.
- Device threads: `/api/send` `{to,kind,body,reply_to,files}`.
  Conversations: `/api/dm/new`; `/api/dm/send` `{conv,body,reply_to,files}`.
- Decisions: `/api/act`; returned message actions determine request-decision
  availability. Personal unread controls are separate: normal admitted message
  rows carry effective `unread` and optional `attention:{rev,writer}`. Offer
  Mark unread and Mark unread from here in the existing message menu/sheet,
  including own-sent and personally held excerpt rows; read-only context viewers,
  deleted, optimistic and protocol/event rows do not get these controls.
  Use `unread` for all badges/counts, including deliberately marked outgoing rows.
  Send one atomic `/api/act {do:"unread",id,ids}` using the full current topic's
  admitted ordered rows, independently of any rendered window. Preserve other
  topics and later arrivals. Marking creates no notification or execution effect.
  Deliberate marks survive the current visit, refresh, restart and restored mount.
  Only an actual leave/reopen or topic transition arms read tokens, from that
  entry's first fresh view; same-scope reselection does not. Later generations
  learned during the visit remain unread. Acknowledgments use
  `{do:"read",ids,attention:{id:{rev,writer}}}` with the exact full observed
  generation; never clear by revision alone or by an old cached entry. Deduplicate
  pending/successful reads by generation. Acknowledge only meaningfully visible
  rows in the active, focused chat: opening a topic must not read its unseen tail,
  a one-pixel sliver or content covered by the composer/footer. Tall messages
  count once at least half the available timeline viewport is visible; ordinary
  rows count once at least half the row is visible. Re-evaluate during scrolling
  and layout changes without scanning offscreen history. Keep the visit and
  full-generation fences above; no polling. Linked-device marks
  use `rd2`, which succeeds/implies `rd1`; unchanged refs-only carriers remain V1.
- Unread overview and Review: optional `overview.unread_total` is the full
  accessible effective count, including Main, named, Done/Archived topics and
  marked outgoing turns. `alert_total` applies local conversation mutes to that
  same set; it does not change read state or notification consent. An absent
  total is unknown, never zero. Do not add topic or archived subtotals again.
  Optional `GET /api/unread?after=&limit=` returns display/navigation rows with
  an uncapped `total`, default50/max200 pages and captured `content`/`seq`.
  Keep this section separate from approvals; visiting it never accepts or runs
  work. Use the returned stable `key` for deduplication and the selected `id`
  plus exact chat/topic or device scope for navigation. Refresh loaded prefixes
  on durable changes, reject stale responses and preserve pointer/focus order.
  Optional literal `topic_title` supplies the resolved name even before the
  topic was opened; `label` remains the author. Empty chat topic means Main.
- Native notification privacy: when `overview.notify.preview_default` is
  present, offer Default, Full, Sender and Hidden through the optional
  `/api/notify/preview` route. Empty `preview` uses the returned host default;
  never infer it from the operating system. Full shows locally resolved
  content, Sender only the verified author with generic text. Hidden and Sender
  without a verified author retain fixed activity, reminder, decision or security
  notices without names or caller text. Saving this choice neither enables alerts nor changes mutes,
  permissions, sound or lock-screen rules. Do not offer it for closed-page
  browser push, which stays generic. The existing macOS banner mechanism
  cannot navigate a click to the exact message; state that limit honestly.
- Scheduled messages: offer the optional routes only with `overview.schedules`.
  Use a separate durable Save for ordinary human DM/group/existing-topic drafts;
  do not call normal send, clear a draft early or show an optimistic sent bubble.
  Show the chosen timezone, a future instant, approved audience and the local
  running/page-open requirement. Preserve an unconfirmed save's immutable ID,
  payload and actual files across navigation. Retry that same ID with re-staged
  actual files; a presence lookup alone never clears a draft. A reload cannot
  recover file bytes and must not automatically resend or allocate a replacement
  ID. Display `sent` as queued, keep revision conflicts visible, and require an
  explicit reviewed audience for widening; ordinary edits and Send now retain
  the saved ceiling. See the route reference for all five states and actions.
  Comic and the shared controller use the same purpose-specific recovery key,
  `agentnet.messenger.schedule-save.<workspace-id>`, with only attempt ID,
  conversation and file names/sizes. The shared in-memory ledger preserves the
  creating skin's draft namespace: another skin may settle that exact ID but
  cannot consume its guarded draft-clear. Cold tokens are owner-agnostic and
  require explicit Check or Forget; they do not defer self-updates. Actual
  in-memory saving/uncertain attempts do defer reload, including when storage
  is unavailable. Pass the existing draft namespace as the shared controller's
  `owner`, and destroy controllers before replacing their view or binding.
  Manage scheduled messages, drafts and personal reminders in a dedicated
  Outbox, using compact rows and a due-reminder count in navigation. Personal
  reminder lists do not belong above Chats. Request pending schedules with
  `all=0`; request sent/cancelled history only while explicitly expanded. An
  open edit keeps its captured revision, field focus and position across pushes.
  Listing drafts does not fetch every chat, decode attachments or choose a new
  default agent. Resume their exact original chat/topic/agent and available File
  objects; do not claim file persistence across reload.
  The optional shared controller's `outboxRoot` enables its global panel;
  `openOutbox('scheduled'|'drafts'|'reminders')` opens the selected view.
  Entries supply `chatText(conv)`, `getDrafts()` records
  `{key,record,title,text,files,detail}`, `resumeDraft(key,record)`, and
  `getReminders()` records `{id,title,chat,when,detail,overdue,open,change,done,cancel}`.
  `openOriginalSave(conv)` reopens the bound uncertain-save review, and
  `onOutboxChange(open)` supports the entry's existing visibility/focus cleanup.
  These callbacks use the captured host and original drafts; they do not grant
  send authority. Existing callers without `outboxRoot` retain scoped behavior.
- Standing grants: GET `/api/approvals` lists what `agentnet approvals` does:
  `{questions[{address, status?}|{person, label, status}],
  tasks[{address, fingerprint, status}|{person, label, status}],
  native_tasks?[{address, fingerprint, status}],
  own_devices?[{address, name, this?, questions, tasks}],
  removed_devices?[address],
  participations[{conv, pid, agent_id?, keys[], tasks_from[PersonView], external?}],
  read_only, unresolved?[]}`. A person grant covers that verified person's
  devices and names no address (its task entry has `address` and
  `fingerprint` empty); a device grant names the exact device.
  `native_tasks` are your own trusted devices' direct-task permissions,
  which revoking a person or device task grant leaves in place;
  `removed_devices` is read-only evidence that the exact current pinned peer
  key belonged to a verified roster and has been removed. Suppress new-grant
  suggestions for those devices; retain saved grants and their revoke controls.
  A reused address with a new key is not a removed device.
  `own_devices` lists your person's devices with the source of each one's
  question and task permission here. (`unresolved`:
  conversations whose agents cannot be resolved here now, such as a group
  whose context is pending; their grants are listed once they can be.)
  `POST /api/approvals/revoke` `{kind: "question"|"task", person}` (a person
  grant), `{kind, address}` (a device grant) or `{kind: "participation",
  pid}` ends that one grant (as `unapprove`, `unapprove --tasks`, `dm
  dismiss-agent`; an `external` one is ended by a member only). A browser
  keeps none: its list is empty and `read_only`, and revoking is refused.
- Identity: `/api/person` creates a person (it does **not** rename);
  `POST /api/person/label` `{label}` changes the existing person's self-claimed
  display name through a signed roster step. Keep person ID, keys, devices,
  routing addresses, history and grants separate from that name. A failed or
  ambiguous confirmation must not show an optimistic rename; refresh the
  person before retrying. First-contact directory trust is not independent
  verification of a real-world owner.
  Device operations:
  `/api/device/service`, `/api/device/link`, `/api/device/decide`,
  `/api/device/remove`.
  `/api/device/link` answers `{url, expires, app_url}`: `app_url`
  (`agentnet://open#<code>`) opens the same link in the AgentNet app on this
  computer. A browser skin makes the link when the person presses Open
  AgentNet, so its ten minutes cover only that step.
- The AgentNet app (MEL-533, MEL-534, MEL-536): `overview.app` is `true` when
  the page is the app's window (the app updates as a whole: offer the new
  version of the app, never a terminal command). Google membership (P8):
  GET `/api/google/access` answers `{workspace_url, enabled, can_admin,
  emails[{email, admin, denied, domain_member}], domains[]}`. Only `can_admin` may edit:
  POST the same route with one `{email}` or `{domain}`, plus `remove`, and
  `admin` with an `{email}` only (optional promotion only; `{domain,
  admin}` is refused; an invite never demotes an admin). Removal of an email blocks domain admission too and
  revokes its active and pending devices. Offer Invite by email and Allow
  everyone at @domain in Workspaces; the Get AgentNet link uses
  `workspace_url`. A person's optional `email` is checked only by the workspace relay and
  trusted on first sight, like the M1 first-contact key limit. Roster
  signatures preserve the claim; they do not independently prove Google
  ownership. Show email only in profile/people details as verified by this
  workspace. Keep key-change warnings; a second pinned person claiming
  that email is a conflict and its email is hidden. Later-device
  Google sign-in uses the existing one-tap device approval. With Google
  enabled, `/api/invites` hides code invites; codes remain an admin CLI
  fallback. Without Google configured, the existing invitation UI works:
  `POST /api/invite` `{name, admin, days}` (`days` 1, 7 or 30) answers
  `{link, label, expires, message}` (one link that offers the app and opens
  in it; `message` is ready to send); GET `/api/invites` answers
  `{can_invite, invites[{id, name, label, admin, by, created?, expires}]}`:
  show Invite people only when `can_invite` (this device is an admin on its
  server), load it when Settings or New chat opens; `POST /api/invite/revoke`
  `{id}` withdraws an unused one. Where to get the app: GET `/api/get-app`
  answers `{version, detected?, platforms[{id, label, url}]}`
  (`static/getapp.json`, the one table). Folders, for Connect an agent: GET
  `/api/folders?path=` (empty: the person's home) answers `{path, parent?,
  home, roots?, dirs[{name, path}], truncated?}`, subfolders only, hidden
  ones left out; read-only, and only on this computer's program (a browser
  refuses it). A folder the person picks goes to `POST /api/responder` as
  today; never ask for a typed path.
- Agents: `/api/dm/agent/invite`, `/decide`, `/dismiss`, `/ask` under that prefix.
- Notifications: `/api/notify/enable`, `/disable`, `/mute`, `/allow`, `/seen`,
  optional native `/preview`, plus, in a browser only, GET `/api/notify/resolve?chan=...`.
- Scheduled messages, when `overview.schedules`: GET `/api/schedules` and
  `/api/schedule/audience`; POST `/api/schedule/create`, `/edit`, `/cancel`,
  `/send_now`. Files use the existing `host.stage` values, never native paths.
- Files: `/api/upload/discard` (this computer's program and Android only),
  `/api/file/request`; use host stage/file for platform differences. On a
  computer, `POST /api/file/open-image` `{id, dir: "in"|"out", index}` asks
  this computer's default image app to open that exact verified PNG, JPEG, GIF
  or WebP attachment. It takes no caller-supplied path or program. Private
  temporary copies expire after one hour and are removed on restart. Browser
  engines and Android do not offer this action; they can display the original
  bytes fullscreen and download.
- Message controls: `POST /api/message/react` `{conv?,id,dir,emoji,remove?}`,
  `/api/message/edit` `{conv?,id,dir,text}`, `/api/message/delete` `{conv?,id,dir}`.
  A message view carries what controls did to it: `reactions[]` (`emoji`, `by[]`
  as `{id,label}`, `mine`), `edited`/`revision`/`text` (the text to show; `body`
  stays what was sent), `deleted`, and `can[]` (`react`, `edit`, `delete`): show
  only the actions listed. An edit never reruns anything; a deletion hides text
  and files and recalls nothing already read, saved or given to an agent.
- Reminders: `overview.remind` says this device keeps personal reminders,
  including current browser devices, and `overview.reminders[]` lists the pending ones
  (`{message, conv?, from, title, due, overdue}`, soonest first). POST
  `/api/remind` `{id, due}` (unix seconds, in the future) sets or moves the
  reminder on a message stored here, received or sent; `/api/remind/done`
  and `/api/remind/cancel` `{id}` end it. Their existing `{note}` result may add
  `{seq, message, reminder}`: the exact pending reminder read back after the
  action, or `null` when ended. `seq` is captured before the readback, as in
  overview and the optional `seq` on DM/thread views. Reconcile confirmed
  attention promptly and preserve newer snapshots when older loads finish.
  A read action's success alone does not prove its generation CAS won.
  Only the person is reminded, on this
  computer (a notification while AgentNet runs, window open or not): nothing
  is sent and the message is unchanged; a reply to it ends it, but an
  automatic report that the work failed, timed out or was cancelled does
  not.
- Groups: POST `/api/groups/manage` `{conv, action, person?, title?}` →
  `{queued}`: `rename` (title only), `promote`, `demote`, `remove` (the
  member's `person`), `leave` (nothing else; `queued` until the others
  confirm). Show admin actions only to an admin of a group that is not
  frozen; anyone in it may leave. A removal recalls nothing already saved.
  A group read that throws `code: "group_context_pending"` (native HTTP503,
  or the same engine error code) is a temporary verification wait. Keep the
  last complete verified view, text and files, pause sends, and show that group
  details are updating. Do not announce an ordinary failure, infer removal or
  acknowledge cached messages as a fresh read. A cold load has no view yet.
  The existing change feed and a fresh successful response clear the pause;
  stale requests or another workspace cannot change the current state. Do not
  poll. A successfully returned frozen former-member view remains readable
  and cannot send; it is separate from this temporary wait.
  `/api/groups/invite` `history` takes `{last}`, `{since}` or exact `{refs}`.
- Trust and standing answers for a device: `/api/act` `{do: "trust", id:
  address, key}` trusts exactly the compared key (`thread.key.pending`) and
  lets held messages from it in; `{do: "approve"|"unapprove"|"grant_tasks"|"revoke_tasks",
  id}`, where `id` is a verified person id (all their current and future
  verified devices) or an exact device address, turns automatic answers on
  or off, or lets tasks run without asking or stops that (nothing running
  is stopped; Android refuses `approve` and `grant_tasks`). A browser does neither: say to do it
  in AgentNet on a computer, never as a command. `thread.key_changed` on a
  thread summary flags a paused one.
- Held back: `overview.quarantine[]` `{id, peer, code, reason, at,
  sender_verified?}`; `code` is `key_changed`, `proof_pending`,
  `identity_conflict`, `conflicting_duplicate`, `invalid` or `unverified`:
  write your own sentence from it. `sender_verified` is true when the copy
  opened under `peer`'s pinned key before it was held (always for
  `proof_pending`, `identity_conflict` and `conflicting_duplicate`; for
  `invalid` only when its recorded cause is a later check); the program's
  own definition says that without it `peer` is only a claim. Comic names
  the sender as that person when `sender_verified` is true or the code is
  `key_changed`, `proof_pending`, `identity_conflict` or
  `conflicting_duplicate` (`model.ts` `holdVerified`). `reason` is a
  fallback sentence; where it names the sender, it names the device in
  words, not its address. An `unverified`
  one (or a code you don't know) only claims its sender: say who it says it
  is from, never draw it as that person. Content is never shown, and these
  are not decisions to count.
- Agent invitation: `/api/dm/agent/invite` `tasks_from` lists the member key
  fingerprints (at most 16) that may give the agent tasks without asking:
  offer it per person (their devices now), never per address.
- Typing: GET `/api/typing` carries this device's `preferences` `{send,
  show}`; POST `/api/typing/preferences` `{send, show}` saves them here.
- Connecting coding sessions (MEL-528): GET `/api/assistant-setup` lists the
  tools found on this computer `{local, harnesses[{id, label, detected,
  configured, registered, supported, state, note, change?, next?, target?}],
  note?}` (`state`: connected, needs_activation, detected, needs_setup,
  not_detected, unsupported or error: write your own words for each and keep
  `note` and `target` behind details). POST `{action: "review", harnesses}`
  adds `review_id`; POST `{action: "apply", harnesses, review_id}` applies
  exactly that reviewed change and refuses one that changed since. Then each
  chosen tool that `/api/agents` lists as `found` gets its named agent through
  POST `/api/agents` `{action: "create", label, harness, dir}` or `{action:
  "update", id, harness, dir}`, and one `{action: "publish"}`. Update an
  explicitly selected existing ID; distinct configured instances keep distinct
  IDs even when their names, tools or folders match.
  Read the tool list first: `local: false` (nothing can be installed here)
  means show `note` and skip `/api/agents`. A browser does not serve this
  route ("Unknown request."): treat that like `local: false`. Say an agent is ready only when
  its `responder.ready` says so. The folder is chosen by browsing GET
  `/api/folders?path=` (read-only), never typed; say so when `truncated` is
  set, and when a folder can't be read (deleted, or closed to the person)
  still offer Up and Home, not only a retry. Setup approves nobody, shares
  no history and keeps the default agent.
- Concrete agent identity (MEL-550): list configured catalog records, labelled
  with their actual `record.harness` and execution device. The optional
  `harness`/`harness_sig` extension is signed separately by the exact host; v1
  identity canonical bytes remain unchanged. Missing metadata means the program
  is unavailable, not a guessed current harness. A local catalog's `default`
  and `/api/responder.agent_id` are settings on one concrete record, never an
  additional recipient. New agent sends carry that concrete `agent_id`.
  Do not fabricate a recipient from `me.agent`, detected executables or an empty
  remote catalog. Keep unavailable historical chats readable without silently
  routing them to the current default.
  Thread summaries and `/api/thread.topic` add `agent_host`, `agent_key`,
  `agent_id` and optional `agent_legacy`. The tuple belongs to the original
  exchange; moving messages, relabelling a topic or changing the default cannot
  change it. Group agent chats by the full tuple, not by device or display name.
  Plain human threads may also carry an organization tuple; only a concrete
  `agent_id` or `agent_legacy:true` selects agent chat behavior. Legacy unnamed
  history is its own identity and is never attributed to today's default.
  When the execution host differs from `/api/thread.peer`, the optional
  `agent_host_key` carries that host's current pin and pending-key state.
  `thread.key` still belongs to `peer`; never compare an agent target against
  another device's pin.
  Filter a composer's possible running requests to its exact selected target
  before considering freshness. Reconcile an uncertain matching request with
  one exact fresh view; if it remains uncertain, show the request and Refresh
  recovery while retaining text and files. Do not guess an attempt or send new
  work to escape a blocked follow-up.
  A normal Reply to a verified agent answer or clarification can select that
  agent through its exact stored ancestry, without a manual tag. Settled
  ordinary continuations use `attempt:0`; active steering retains its positive
  exact attempt. Preserve the quoted message, draft and files, and allow the
  person to remove the addressed agent to send an ordinary reply. The route
  and authority checks are specified in `HOST_API_ROUTES.md`.
- Topics (every chat, docs/plans/TOPICS.md): `overview.threads`
  lists every thread, archived topics too; a skin that pages topics itself
  asks `/api/overview?topics=1` and gets them without archived topics.
  `overview.topics[]` counts each peer's topics (`total`, `archived`,
  `archived_unread`, `latest`) and `overview.topic_list` says the routes
  below exist. A thread summary carries `state` (active, done, archived),
  `done_by` (agent, you), `conclusion` (the final reply's first line: the
  agent's words from an explicit close, labelled as theirs; a local
  Mark done carries no conclusion), `concluded_by`, `pending`, `renamed`,
  `auto_title`, `quiet_since`; `/api/thread` adds `topic` and may add
  `peer_topics` with the same per-peer count shape and exact original agent
  group. When the assembly revision stays unchanged, its `archived_unread`
  gives the absolute archived count even if the selected topic is omitted from
  the overview. Replace that group's count until an overview at least as new
  as the view's `seq` arrives; do not also apply a per-thread archived delta.
  Missing metadata keeps the normal overview fallback. For an admitted agent
  organization, an otherwise automatic topic name may use the exact canonical
  explicit-new-topic name carried by its earliest signed move notice. The
  pinned phrase is `Moved selected messages to new topic “<name>”.`; do not
  reword/localize it without preserving its decoder. Generic legacy notices
  never supply a name: they can contain another person's private label.
  Explicit private/shared names keep their existing precedence. The fallback
  also works for retained explicit-new notices; it changes no title register,
  move or execution authority. Older hosts/generic history retain automatic names.
  GET
  `/api/topics?peer=&state=&q=&before=&limit=` pages `{topics, next,
  matched}`, unread first with activity time, ID and peer tie breaks. Optional
  `agent_host`, `agent_key` and `agent_id` scope the immutable agent destination
  before counting and pagination; pass the complete tuple from the selected
  topic, including an empty legacy agent ID. Cursors
  are opaque comparison positions; an old chronological cursor requires a
  first-page reload. Refill the loaded prefix on live changes and deduplicate
  scoped IDs. Freeze displayed order under pointer/focus/selection, updating
  counts without moving the target; vanished rows cannot act. A trimmed
  non-empty search omits `state` on every page and searches
  Active, Done and Archived together within the same peer/conversation scope;
  results keep their actual state. Clearing search restores the selected status
  tab. POST `/api/topic/rename` `{peer,id,title}`, `/api/topic/done`
  and `/api/topic/reopen` `{peer,id,count}` (`count`: the messages shown).
  Device-chat names, Done/Reopen and Archive follow this person's own
  linked devices, never other people; say so. People DMs/groups
  keep a main flow and opt-in topics: `/api/dm` adds `topics[]`, messages
  have derived `topic` and optional shared `topic_event`, and
  `/api/topics?conv=...` pages the chat's topics. People-topic Done/Reopen
  are shared and attributed (`done_by: person`, `concluded_by`); any member
  may reopen. Send/ask accepts `topic` (`new` starts a topic), and
  `/api/topic/create` `{conv,id}` promotes a held logical message and replies.
  Topic changes use `{conv,id,count?,title?}`; bulk Done/Archive/Delete uses
  `{conv|peer,id:"",ids:[...],counts?:{id:shownCount}}`. Offer one confirmation before calling the host; never claim the change
  before the host result. Delete for me in people chats
  affects only this person's devices; others keep their copies. Archive
  follows this person's own linked devices only; new messages/shared actions
  clear an archive. A named people-topic rename is shared with everyone who
  has access, including authorized topic guests. Say that before submitting;
  an empty name restores the automatic name for everyone. Existing private
  names are not published until the next explicit rename. Main-flow labels
  remain personal. Admitted changes converge by revision and original writer;
  a stale rename is refused for explicit retry. Renaming changes no topic ID,
  access, message, status or read state. Comic implements these controls
  through Host API v1 only, as do Classic and Zoom in the agentnet-skins
  collection.
- Shared group links use the optional `resources` projection on `GET /api/dm`:
  `{links:[{name,url}],rev,writer,lid}`. Its absence means hide the controls;
  a present empty list is an empty register, not an unsupported feature.
  Current human group members may read/add/rename/change/remove links through
  the existing header; disable edits while frozen. Submit the complete list to
  `POST /api/dm/resources` with the original `rev` and `writer`. Retain drafts
  across pushes and show stale refusals without overwriting or automatic retry.
  Names and HTTP(S) URLs are untrusted context, not instructions or access grants.
  Opening follows existing host navigation policy: Android opens HTTPS only;
  HTTP links remain visible but are not opened by its wrapper.
  Guests receive no metadata. Member agents read links in their existing chat
  context, except when that request's captured reply audience includes human
  guests. No agent edit path, lookup command, provider fetch or automatic work
  is added.
- Topic organization uses `/api/topic/organization/preview` and `/apply` with
  exact selected logical IDs and a different same-chat destination. Main is
  explicitly `{topic:"", main:true}`; a missing destination is refused.
  An admitted agent's display move carries `topic_event.request` and
  `origin:"agent:organization"`; use `verified_agent`, never the origin label,
  to attribute it. The host validates the exact human request/current run and
  supplied context. It preserves all original messages, files and audience.
  A current full-member human may call `/api/topic/organization/undo` with
  `{conv,lid,author,id}`; keep the action `id` for retries. The host restores
  persisted source assignments, checks every group before one transaction and
  refuses intervening changes. See the route reference for sent results.
  These agent moves and the new explicitly human-to-Main wire shape require
  independent `aorg1`; ordinary canonical-PID agent reactions require `amr1`.
  Shipped `org1`/`agr1`/`rm1` meanings are unchanged. Shared topic renames add
  the independent `ttl1` gate; shared group-link metadata adds independent `res1`.
  Direct agent chats use `{peer, ids, topic, merge?}` for an existing destination or `{peer, ids, new:true, title, topic:""}` for a named new topic, instead of `conv`. Preview returns the normalized destination; a new direct topic uses the review operation ID as its topic ID. Echo the entire review, including `destination` and `synthetic_topics`, unchanged for apply/retry. Only select topics with the same immutable `agent_host`, `agent_key` and `agent_id` (empty legacy IDs stay distinct). Synthetic IDs are accepted by `/api/thread`; navigation anchors are held originals and do not replace the topic ID. These placements are private to the owner’s current linked human devices, require explicit `dts1`, and produce no message or execution event. The result proves local storage (`state:stored`), not remote delivery. Direct undo uses `{peer,lid,id}` on the originating device; retain `id` for retries. Original reply links still determine later replies and native context.
  Private own-device reminder state adds independent `rem1`, consuming the
  last reserved producer slot. Producers and existing readers accept at most
  32 signed names; records stay bounded at 1024 bytes. Missing readers retain their copies;
  history/archive/repack keeps each extension independently and never runs
  imported work.
- Headless: a request message may carry `exec` `{state, at, host, stale?, attempt?, detail?}`,
  the executing host's own signed word (never delivery, presence or a timer);
  show it apart from delivery and say when it is stale. Current native activity
  may include `Thinking: ` followed by a host-filtered line of at most120
  characters, alongside the fixed lifecycle/tool labels. Display usable wording
  literally in the current request/attempt's Working slot, replacing the prior
  line; never render it as Markdown or append chat messages. Unsupported or
  absent detail keeps the ordinary generic activity. Activity is not an answer,
  delivery receipt or new permission. A review notice may carry
  `report` `{host, at, items[{id, from, key, kind, state, blocker, since, attempt, excerpt?, actionable?, conv?, proposal?, native?, result?}]}`:
  a snapshot at `at`, never a live queue; offer actions only for `actionable`
  items, through `POST /api/operator/decide` `{host, id, key, action, expect,
  attempt, text, report}`; the host's answer lands on the item as `result`.
  The host applies a decision only on a report it sent that operator and still
  holds: once the host has deleted its chat with the operator, decisions on the
  reports in it are refused.
  An own-request `continuation.handled` may carry the exact host's resolve
  decision result. Show `handled.refused` as a visible refusal; a queued send
  is not proof of handling. Retain the item until its signed host state clears.
  A report naming requests (items) went to a device that decides them (a
  steward's or an operator's): its card says this device decides them. A
  report to a device that may not decide has no items, only `count` and
  `deciders[{person,label}|{address}]`: say who decides them from their own
  devices ("Sergey decides these from his devices"; "You decide these" when
  this device's person is among them; a count with no deciders: nobody yet,
  whoever installed that machine can name a steward there), never "decide
  on that machine", and no command on the card. A notice without a version 2
  report (count text) says nothing about who decides: claim nothing. An
  item with `conv` is a DM or group request: offer no Answer for it. A task
  carrying out an agent's proposal has `proposal` `{question_id, question,
  asker, proposal_id, proposal, confirmed_by, edited?, task?}` (first lines; the engine
  parses it for actionable items as Go does). Local device messages carry
  the same `proposal` shape with the full original question and suggestion.
  All skins show the three steps: the asker asked, your agent suggested,
  the asker chose Do it (with `edited`: edited the suggestion and sent
  `task`). This uses only the asker's usual task approval;
  model output adds no authority. Running items remain visible with their
  start time and the existing Stop action. `blocker: seems_stuck` is a
  once-per-run silence notice, never an automatic kill. The newest report from a
  host replaces the older ones (the host keeps one open card); a report
  with no items and no count says nothing waits any more and arrives
  already resolved, and so does a late, older report once a newer one was
  stored, open or dismissed. A decision's `result` shows on whichever
  report of that host lists the same request and attempt.
- Native requests (v0.8.27): a validated current owner-human report item may
  carry `native` even though `actionable` is false. It is a distinct native
  response, never an operator grant. Show it on the requesting device:
  `{token,harness:"codex",host,request,key,attempt,method,thread,turn,item?,
  approval_id?,agent_id?,operation?,cwd?,reason?,choices,expires,cancel_run?,
  unsupported?,questions?,fields?}`. `expires` is Unix milliseconds; zero
  means only the exact live process/turn lifetime. Resolution, ending,
  cancellation and restart remove the callback. Imported history never
  creates an active native response. Show the entire supplied operation,
  working folder, reason and unsupported boundary; never offer Allow from
  a truncated command or file operation.

  `choices` lists only supported `allow`, `deny`, `cancel`, `submit` responses.
  Questions are `{id,header?,question,isOther?,isSecret?,options?[{label,
  description}]}`; use password inputs for `isSecret`, preserve exact question
  IDs and offered options, and send `{answers:{id:string}}`. Standard MCP form
  fields are `{id,title?,description?,type:"string"|"boolean",required?,enum?}`;
  send `{values:{id:string|boolean}}`. Unsupported authenticated/URL or other
  schemas have no fabricated proof or Allow. `cancel_run:true` means Cancel
  stops the owned worker rather than supplying a native cancel response.

  Prepare one stable send ID and the exact text
  `agentnet-native-response-v1\n` followed by JSON `{token,choice,answers?,values?}`.
  A local daemon uses `/api/act` `{do:"continue",id:native.request,
  key:native.key,attempt:native.attempt,body:text,send_id}`. A remote requesting
  human device uses `/api/operator/decide` `{host:native.host,id:native.request,
  key:native.key,attempt:native.attempt,action:"continue",expect:"running",
  report:<private notice id>,text,send_id}`. Keep identical text/ID on an
  ambiguous retry. No second AgentNet confirmation or permission grant is
  required. The host checks the exact current human key, private report,
  token, admitted job, native callback and lifetime before one handover to
  the original process. Saved/delivered responses prove no completed effect;
  native result reports remain the completion evidence. Results are scoped
  to the callback token, so an earlier input cannot settle a later one in
  the same attempt. Other roles receive no private native payload.
- Proposals (MEL-521): an answer with `status` `proposal` is an agent's offer,
  never run: its body is exactly the task it proposes. A device-thread or
  conversation question's run may propose. The answer carries
  `actions:["do_it"]` when this device may confirm it (the program lists it
  only for the verified asker and an unchanged, unconfirmed proposal).
  Show it as a suggestion, never as done ("Suggested a task · not run", "Proposed
  task (not run)"); confirming it (the CLI's `agentnet do ID`, or the page's
  Do it: `/api/act` `{do: "do_it", ...}`, see HOST_API_ROUTES.md) sends
  exactly that stored text as a task, under the usual task approval. A
  revised suggestion goes through `/api/act` `{do: "change_proposal", id,
  body}` and is sent as an ordinary task, under ordinary task approval. A
  task carrying out a proposal has `proposal` `{question_id, question,
  asker, proposal_id, proposal, confirmed_by, edited?, task?}` (`task`: the
  text sent); with `edited` true, `confirmed_by` edited the suggestion and
  sent `task` instead (say so: Comic says "edited and sent this task", not
  "chose Do it"). A
  review item for such a task carries `proposal` as above.
  A notification's `#msg=<id>` (optionally `&conv=…&dir=in|out`) lands on that
  message and does nothing else.
- Workspace name: `overview.workspace` `{name, server}` is the workspace's own
  name its admin set (`""` for none) and the relay's host name. GET
  `/api/workspace` adds `can_rename` (this device holds the Hub's admin role:
  an admin invite made it one, its person granted it with `agentnet
  person admin`, or the workspace's Google sign-in made that person's email
  an admin, which gives the role to all their current devices; otherwise a
  person's devices never inherit it; unknown is false). POST `/api/workspace/name`
  `{name}` renames it for every member (an empty name clears it); a member is
  refused "Only an admin of this workspace can rename it for everyone." It is
  a company setting: offer it only on the person's own tap, never from an
  agent. Members get the new name with the member list, without reloading.
- Who runs an agent: `me.agent` (this device) and `overview.agent_devices`
  (the other devices that, as last listed, say they run one; kept offline)
  are the only "runs an agent" signal. Show a device as an agent, and offer
  Ask, Do it or Bring in toward it, only when it says so: a phone or a
  browser never does, and its messages are its person's. Both are hints for
  display and offers; nothing a skin does with them grants anything.
- People, not addresses: name a device by its person and device
  ("Vitalii · Phone", "You · Pixel"; the device in words, dashes as spaces);
  keep addresses for verified-detail panels. When a person's name is also
  another person's (or yours), or the server only lists them, add the device
  key's first group so a look-alike cannot pass as someone else. The page's
  own sentences (`state_text`, `why`, `reason`) already name people this way.
- Storage: GET `/api/storage` (read-only): `local.areas[]`, each with
  `status` (available or unavailable), `usage` `{files, bytes}` (absent when
  unknown) and `reason`; `local.known` (the sum of the areas whose usage is
  known, never a total of unknown ones) and `local.complete`; `remote`
  `{status, usage?, reason?}`, `status` available | unsupported |
  unavailable, `usage` the Hub's own-usage report (absent when unknown).
  Show unknown as unknown, never zero; the Hub's quota is the whole
  server's, never an allowance.
- Storage management (v0.9.3): `remote.usage.can_change_quota` advertises the
  confirmed admin-only `/api/storage/quota` action. `/api/storage/cache` reviews
  received files and clears only the exact confirmed selection after local and
  remote ciphertext verification; unavailable copies stay here. Outgoing spools,
  staged files and the last recoverable copy are protected. Counts overlap:
  relay logical usage includes local and Drive custody, while device cache is
  this device's separate copy. Show unknown usage as unknown. Exact failed-send
  retry uses `/api/storage/retry_send`, preserving existing ciphertext and IDs;
  its `retried` count proves requeue only. The host excludes legacy or uncertain
  handover, stopped and delivered copies. Show a producer `send_error:<code>:`
  detail once, with the corresponding member/admin action; supported codes are
  `relay_quota`, `file_too_large`, `relay_disk_full`, `local_disk_full` and
  `network_unavailable`. Drive migration pause is separate from send failure.
- Workspace attachments: GET `/api/storage/attachments` is where the relay
  keeps attachment ciphertext (`state`, `local`, `remote`, `pending`,
  `remote_only_files`, `migration`); one relay admin connects Google Drive
  and members never connect a Google account. Offer Connect, Move existing
  and Disconnect only with `can_admin`, a display hint: the relay decides
  and refuses others. POST `{action:"connect"}` returns `consent_url` once,
  to open from that click; `move` and `disconnect` need `confirm:true`, and
  disconnect is refused while `remote_only_files` is above 0. A read never
  starts consent. 404 means this relay or platform does not offer it.
- Messaging: DM/group messages carry `delivery`, `sent_at`, and `quote`;
  device-thread messages carry `sent_at` and `quote`. Use `delivery` for
  delivery ticks: each other person's best device copy, then the least
  advanced person. Own devices count only when there are no other people.
  `state` remains the raw stored state, never a completion claim. Copies
  carry `own` and `person` (a display label); Details should name the person
  and device, without routing addresses. Show `sent_at`; keep timeline order
  and day dividers by `at` (arrival). Show an arrival note in Details when
  the gap is at least 60 seconds.
  `POST /api/send` and `/api/dm/send` accept `quote`, independent of
  `reply_to` (threading/session continuity). Only an explicit `quote` gets
  a quote card. Agent answers/results/progress have no quote card; a distant
  request gets a compact jump link, an adjacent request no reference.
- Human guests: `POST /api/dm/guest/check` `{conv,host}` returns
  `{ready,needs_update:[{label,me,role}],offline:[],text}`. Check when the person
  is selected; an older app may still be invited and its stored invitation
  waits for an update. Guest views expose `needs_update` labels for waiting
  copies. “Ask NAME to update” opens a DM and prefills a draft; sending is
  always the person's choice. Changed keys remain refused.
- Presence refresh: `/api/refresh`, triggered by user navigation; no polling.
- What a conversation view says without its sentences: a DM message's
  `reply_to` names the message as this device shows it (an agent's answer
  names its executor's copy of the request; the view links it to the
  request's own `id`); a participation record carries `event_type` (`invite`,
  `accept`, `decline`, `dismiss`, `scope` or `share`) and `event_by` (its author's
  person label as known here, or device address) beside `event`. A DM
  summary carries the counts `guests` (active participations: people and
  agents) and `decide` (its requests with actions for this device's person;
  none on a browser) and, when its latest row is a participation record,
  `last_event` `{kind, pid, by}` (`kind` as `event_type`; a scope counts as
  its invite). A DM view lists the participations themselves:
  `guests[]` (people) and `agents[]` (agents invited into it). A device thread summary
  carries `agent_id` when one is named.


## Versions and capabilities

`host.version` stays `1` while the contract only grows: new host members,
routes and view fields are additions, and a skin feature-detects them.
What a skin can rely on:

- **The running release.** `overview.version` is the program serving this
  page (a browser reports its page's own build until a reload is due, then
  the build its relay serves). It changes when
  AgentNet is updated (see [Staying current](#staying-current)).
  `overview.release`, when set, is a newer published release recommended
  here; `overview.update_required` `{latest, url?, auto?}` says the relay
  serves this device again only after an update.
- **Capability flags** on the overview say which areas this device offers;
  draw a control only when its flag says so: `persons`, `agents`, `groups`,
  `files` (limits), `notify` (alerts, with `enabled`), `remind`,
  `topic_list`, `reply_receivers`, `reply_sessions`, `app` (the page is the
  desktop or Android app's window), `demo`; a browser adds `device`
  (`online`, its relay connection). Routes that exist on only one platform
  are marked in [HOST_API_ROUTES.md](HOST_API_ROUTES.md); a refused route
  rejects with a sentence, which a skin shows rather than retrying.
- **Host members** that exist only somewhere (`host.android`,
  `host.onBack`, `host.clipboardImage`, `host.reconnect`,
  `host.workspaces.disconnected`/`reconnect`/`rename`,
  `host.drive.prepareGoogle`/`beginGoogleConsent`, browser only) are
  absent elsewhere: check before use. The
  app methods and `host.drive` are always present; handle their rejection.
- **A minimum release** in the manifest (`agentnet`) for a package that
  needs routes or fields that older releases lack.

When AgentNet changes this contract it changes its built-in package
(Comic) in the same change. Skins in the
[agentnet-skins](https://github.com/misunders2d/agentnet-skins) collection
are tested against the AgentNet commit pinned in their `upstream.json`, and
AgentNet's release checklist (AGENTS.md) runs the agentnet-skins tests
against each release candidate before it is published.
`TestHostAPIRoutesDocumented` keeps the route tables of
[HOST_API_ROUTES.md](HOST_API_ROUTES.md) equal, by method and path, to the
program's page routes (`internal/ui/server.go`; a `{what}` route, and the
`POST /api/groups/…` actions read as one subtree, match every documented
action) and to the paths the browser engine answers; the
workspace and app routes listed in prose are not checked.

## Staying current

AgentNet runs only the latest release (outdated devices are suspended by
their relay). The host never reloads a skin by itself, so a skin keeps its
page current:

- Remember `overview.version` from the first overview. When a later
  overview names another version, AgentNet was updated.
- In a browser (`platform` `browser`) reload the page with
  `location.reload()` as soon as nothing unsent would be lost; until then
  say that AgentNet was updated and that the page reloads once drafts are
  sent or cleared, and offer Reload. Sends already accepted wait in the
  browser's outbox across the reload.
- When the daemon serving the page switches to an updated program it sends
  `restart` and the host closes the stream: say it is restarting, call
  `host.listen` again, then reload data. The desktop app and Android update
  as a whole (the app restarts, or Android's installer replaces it) and the
  page opens again on the new release.
- While `overview.update_required` is set, keep a notice on top that says
  how to update (the app's own Update where `appStatus()` offers it; never a
  terminal command inside the app).

Comic does this (`internal/ui/web/src/store.ts`,
`internal/ui/web/src/features/Status.tsx`); it offers its update only after
`appStatus()` says `app_update_supported`.

## Presentation: motion and avatars

Everything a skin draws is its own: layout, transitions, avatars and
illustrations. The contract fixes what they may claim and how they move.

**Motion.** Keep transitions short (Comic's last 90–320 ms: entrances
ease out, exits speed up, and a new item's pop-in overshoots slightly;
longer effects are its 1.8-second highlight on a message it jumps to and
the arrow on a request that waits for your decision, which nudges three
times),
purposeful (they show where something came from or went) and
interruptible: a new tap or keystroke never waits for an animation, and an
action takes effect at once, whatever is still moving. What is already
there when a view opens appears settled; only new items get an entrance
(Comic's `ui/Motion.tsx`). Text is readable as soon as it shows: fade or
rise it in, never type it out or hold it back (Comic delays only its
"Loading emoji…" notice, by 350 ms). Under
`prefers-reduced-motion: reduce` replace movement with an instant change
or a short plain fade, and stop looping animations. Comic does this for its
own movement (panes, pages, sheets, popups, switches, chevrons and
scrolling); its 1.2–1.6-second loops stop, its working robot's scan among
them, and the arrow does not nudge. Its 1.8-second highlight, a change of
color, still runs.

**Avatars.** A skin may draw people and agents however it likes, from data
the views already carry:

- People: the person's own picture (`picture_url` on people in the
  overview, chosen by that person and shown as an `<img>`; public on the
  workspace, so never proof of identity) or, without one, their initials;
  use the person id (`person`) or address as the stable seed for colors.
- Agents: a drawing seeded by the agent's stable id (its address or
  `agent_id`, so the same agent always looks the same), with a mood from
  real state only: working while its request runs; waiting while something
  waits for a person (a request in `decide` or `needs_human`, an
  invitation, an overdue reminder); asleep when it cannot answer now
  (`responder.ready` false, turned off, or its device offline in the
  directory); neutral otherwise, finished requests included. Comic uses
  done only for a settled good state (a ready responder, the chosen
  harness, nothing left to approve) and as decoration on About. The
  request states are listed in
  [HOST_API_ROUTES.md](HOST_API_ROUTES.md#request-states). Never show an
  agent as working or done from delivery receipts or timers.
- A device badge (laptop, phone, server) is a display hint only: Comic
  guesses it from the device's name, and an avatar's look never grants
  anything or says who someone is. Guests keep a distinct mark.

Comic's own avatars are in `internal/ui/web/src/skin/Avatar.tsx`; a skin may
restyle them completely (a character-styled robot, a costume) as long as
the moods above stay recognizable.

## Storage and shared helpers

A skin may keep its own preferences and drafts in this browser's
`localStorage` under keys that start with its own package id (for example
`notebook.`); never `agentnet.`, which holds the host's skin choice and
trust records (`agentnet.skin`, `agentnet.skin.package`,
`agentnet.skin.trusted.<id>`). Comic, built in, uses
`agentnet.messenger.*` for drafts, main-flow choices, emoji skin tone and
reaction usage (the main-flow choices and reaction usage are shared with
Classic and Zoom in the agentnet-skins collection, which copy the same
modules), `agentnet.theme`, and on Android
`agentnet.android.send-journal.v1.<workspace>` for sends saved across the
app being closed; its bundled emoji picker caches its data under
`frimousse/` in `localStorage` and `sessionStorage`. Per-workspace state
goes in `host.workspaces.state(id)`. Never store keys, invitation codes or tokens;
browser storage can be cleared at any time, so treat it as a convenience.

Only `/assets/typing.mjs` may be imported from the host at run time. Other
helpers the built-in skins use are copied into the package at build time:
`internal/ui/static/pictures.mjs` (profile pictures: `avatarPicture`, the
picture editor, paste), `internal/ui/static/optimistic.mjs` (pending-send
previews that agree across skins), and from `internal/ui/skins/shared/` the
presentation-only Drive panels (`drivespace.mjs`, `drivespace-setup.mjs`,
`drivespace.css`) and the topic, mention and reaction modules. (Classic and
Zoom in the agentnet-skins collection also carry their own Connect-an-agent
panel and QR encoder.) Keep their
licenses and take them from the AgentNet release named in `agentnet`.
`internal/ui/static/drivespace*.mjs` and `assistant-setup.mjs` are the
host's own modules: never copy them into a skin.

## Build your own skin

There are three ways to make a skin, from the least work to the most:

- **A theme**: Comic with only its colours, fonts and corners changed. An
  overlay that ships just `theme.css` (and fonts). The contract calls a
  colours-only change a theme variant: fine to share, listed as such.
- **An overlay skin**: Comic's whole interface with its skin layer replaced
  (theme, fonts, brand, avatars, send button, topic intro, request frame, and
  styles for the documented parts of the screen). It keeps every Comic
  feature and gets Comic's fixes on its next build, because it is built from
  the AgentNet release it names, never a copy of Comic's code. The skin
  layer, its slots, its parts and the build are documented in
  [internal/ui/web/src/skin/README.md](../internal/ui/web/src/skin/README.md)
  (the Comic kit); `build.sh` builds an overlay with `SKIN_OVERLAY` and
  related variables. Pulp in the agentnet-skins collection is one.
- **A standalone skin**: your own interface on the host API, in any toolchain
  whose output is ES modules and CSS. `examples/skins/notebook/` is a plain
  starting point with no build step; Holonet, Classic and Zoom in the
  collection are larger ones.

The [agentnet-skins](https://github.com/misunders2d/agentnet-skins)
collection scaffolds all three (`npm run new-skin`), builds, tests and
publishes them to the gallery; its contributing guide says how to submit
one. For a standalone skin:

1. Start from `examples/skins/notebook/` or a collection skin. Comic is built
   with React, Base UI and Tailwind (`internal/ui/web/build.sh`); its package
   is the reference for a bundled skin.
2. Write `skin.json` listing every file. Register `host.onOpen` in `mount`,
   use only the host API above, keep everything inside `root`, and leave
   `root` empty in `unmount`.
3. Try it without installing: import the folder into a browser (Comic,
   Settings → Appearance → Skin). Or install it next to the program and
   restart.
4. Check it (below), on both providers, desktop and phone, light and dark,
   with unread/error/offline states, file safety, drafts, keyboard/focus and
   notification routing. Never use live enrollment codes or real messages as
   a fixture.

## Checks

**Contract only** (`internal/ui/testdata/skin_contract_check.cjs`, Go test
`TestSkinPackagesContractOnly`, opt-in with `AGENTNET_PLAYWRIGHT`): mounts
packages from copied package bytes at an unrelated path, with only a public
host the check implements itself (no loader, no workspace shell, no page
globals), over the demo daemon. It fails on a read of a private page global,
a direct `/api` or `/events` request, a request for an undeclared file (the
host's `/assets/typing.mjs`, `core.css` and `skin-base.css` excepted), any
DOM write to the page's document outside the skin's shadow tree after the
entry is imported, any change to the page's adopted style sheets or font
set, a `window` or `document` listener left after `unmount`, errors, or a
root left non-empty after `unmount` (mount/unmount A/B/A). DOM writes made
while the entry module itself is evaluated, and writes inside the skin's
shadow tree next to `root`, are not caught. Comic's journey also sends a
message through the host and requires one sheet (the trust sheet) and one
confirmation (deleting a conversation or topic) to be modal inside the root
(aria-modal, the app behind them inert, focus kept inside through 10 Tabs
and 10 Shift+Tabs). It runs Comic and the Notebook example
at 1440 and 390 pixels, and needs `node` and Chromium (`AGENTNET_CHROMIUM`
or `/usr/bin/chromium`):

```sh
AGENTNET_PLAYWRIGHT=/absolute/path/to/playwright-core go test ./internal/ui -run TestSkinPackagesContractOnly -v
```

**Production host** (`skins_browser_check.cjs`, `TestSkinsRendered`): the real
loader and catalog with Comic as a package (shadow root, fonts and
properties adopted, no switcher), saved choices, an installed skin behind
its trust step under the switcher (layout, keyboard, theme), notification
destinations (a review notice that names only a message opens its
conversation in Comic), a browser-local skin imported, re-trusted after a change and
removed, and the step back to Comic, at 1800×960 light and 390×844 dark.
`workspace_reconnect_browser_check.cjs` covers the switcher's workspace
control.

Digest-bound passing evidence establishes those checks for those bytes, not
a sandbox, safety guarantee or certification of harmlessness. Skins still
execute with the page's full trust.

## Independent example: Notebook

`examples/skins/notebook/` is a small independent skin: a notebook with a
table of contents (people, then devices and services), one conversation page
at a time, a To line and target-named Send button, files added by choosing or
dropping, received and kept files opened as clippings, reactions, edits and
explicit decision buttons, in light and dark, desktop and phone. It imports
nothing of Comic. It is a developer example, not a feature-complete skin: it
says on its page that finding new people, adding devices, settings and
notifications are not in it, and the switcher above it takes you to Comic
for those.

Notebook keeps text, native File objects, send kind and selected conversation
in the host's per-workspace view state. Switching A/B/A restores that
workspace's draft and target, even when another workspace has the same ids.
Sends already started keep their original host and target; returning during a
send does not duplicate it. Accepted sends clear only the text and files they
captured; new edits and other drafts stay.

When the event stream ends (AgentNet restarting, or the connection lost),
Notebook reconnects by itself, with growing pauses: through
`host.reconnect()` where the host has it (the same membership bound again,
Notebook mounted again over it, text drafts kept), then a fresh stream and
refresh. After about a minute without an answer it asks for a reload.

While any workspace has draft text, files or a send in progress, Notebook uses
the browser's native `beforeunload` warning for host escape/reload. Cancel keeps
the page and drafts; confirming leave discards this page's in-memory drafts.
A send already under way may still be accepted after leaving. Clean pages have
no leave listener. This is not crash durability: warning display needs browser
support and user interaction and is unreliable on mobile OS termination. See
[the browser API's limits](https://developer.mozilla.org/en-US/docs/Web/API/Window/beforeunload_event).

Comic keeps text drafts per workspace in this browser; switching skins asks
to send or remove files waiting in a draft first, since files cannot survive
the reload.

### Notebook journey and digest evidence

Use an already installed Playwright Core and Chromium, with a fresh isolated
profile. No live enrollments, real peers or agent execution are used. Example:

```sh
umask 077
mkdir -p /tmp/agentnet-notebook-check/screenshots
AGENTNET_PLAYWRIGHT=/absolute/path/to/installed/playwright-core \
AGENTNET_CHROMIUM=/absolute/path/to/chromium \
AGENTNET_SCREENSHOTS=/tmp/agentnet-notebook-check/screenshots \
node internal/ui/testdata/notebook_conformance_check.cjs \
  --skin-dir examples/skins/notebook \
  > /tmp/agentnet-notebook-check/evidence.json
```

`--skin-dir` defaults to the example directory. The runner snapshots the manifest
and declared files and calls existing `local-skins.prepare()` for the same
package digest used by installation/consent. Its JSON identifies that package,
digest, host version, adapter, browser, viewport journeys and screenshot paths.
The server serves those exact snapshotted bytes throughout the run. Keep evidence
and screenshots private and local unless separately authorized to publish them.

The adapter is `notebook-device-thread-drafts-v1`: Notebook device-thread
navigation, asynchronous sends/refusal/queue, files, A/B/A host-state retention,
and native leave cancel/confirm/clean behavior at 390 and 1280 pixels. It uses a
captured Host API v1 provider and the actual `WorkspaceShell.state`; it does not
certify all providers, DMs, all skin flows, live routing or a mobile OS. Its
controls reproduce host navigation for the native dialog test; this journey
does not replace the separate production-loader tests.

Only a Notebook package declaring `entry.mjs` and `style.css` is supported by
these selectors. Other packages return `status: "unsupported"`, `pass: false`
and exit 2; invalid/failing packages exit nonzero. A Notebook candidate using
`--skin-dir` is tested as Notebook, never described as generic conformance.
An advisory review should name the digest and tested journeys alongside any
unchecked flows. Digest-bound passing evidence establishes those compatibility
checks for those bytes, not a sandbox, safety guarantee or certification of
harmlessness. Skins still execute with the page's full trust.
