# UI packages

AgentNet's UI host loads a complete interface independently of the messenger.
A package can replace navigation, layout, conversation presentation, dialogs,
settings and interaction flow. It uses the same data and actions as the built-in
interface. It does not change membership, encryption, delivery or permissions.

This is the trusted package boundary for a future UI marketplace. It does not
implement marketplace discovery, downloads, signing, sandboxing or billing.

The intended distribution contract is client-owned: a person must be able to
install, choose and share a skin without changing their relay or other clients.
Desktop local installation and browser-local import, storage and removal use
the same manifest and consent digest. Relay-provided packages are optional
offerings, not a prerequisite. The Notebook-specific journey below binds
compatibility evidence to exact package bytes. General conformance adapters
and advisory LLM review remain future work; neither is skin certification.

## Install without rebuilding AgentNet

Place a package in `<home>/skins/<id>/` for a laptop daemon, or
`<hub-data>/skins/<id>/` for a relay's browser page. Restart the serving process.
No Go changes, rebuild or changes to embedded app files are needed.

On Unix, the skins directory, package directories and files must belong to the
serving user and must not be writable by group or others. Keep the home/data
folder private. On Windows, access control comes from the home folder's inherited
ACL; AgentNet does not inspect Windows skin ACLs separately.

Select the package in **You → Appearance → Interface**, or open the
page with `?skin=<id>` after authenticating. The first use of a package (and each
changed version) asks for trust. Selection and consent are local to that browser
origin, including the daemon's port. The built-in interface remains available at
`?skin=default`. Remove the package and restart to uninstall it.

Installed code has the same access as the default interface: it can read chats,
send messages and invoke permitted actions. This is **not** a sandbox. Installing
on a relay makes the package available to every browser device using that relay;
the relay operator already controls the page served to those devices. Never
install an untrusted package or let another account write these directories.

Packages are immutable snapshots until restart. The consent digest covers the
manifest and every declared file. Requests cannot read undeclared files, paths
outside the package, or symlinks. Limits: 32 packages, 32 files per package,
4 MiB per file, 16 MiB per package and 64 MiB for the catalog.

### Import into this browser

Under **You → Appearance → Interfaces stored in this browser**, choose a
package folder, or select its `skin.json` and declared files. Import validates
and stores the package; it does not execute it or upload anything. Select it
in **Interface**, review the full-trust notice and digest, then choose **Use
this UI**. A changed digest requires fresh consent. Browser-local IDs have a
`local:` prefix, so a relay package cannot replace a local selection.

The browser's CacheStorage keeps immutable package snapshots. Its existing
service worker serves only the reserved `/local-skins/<digest>/` asset path;
chat and API requests keep their normal transport. Relative imports and CSS
remain inside the selected version. Updates retain older snapshots for tabs
already using them; **Remove** deletes every version of that local package.
The 64 MiB local limit includes retained versions and manifest bytes. Clearing
browser site data or browser storage eviction can remove packages; the built-in
interface remains the fallback. Packages and consent belong to this browser
profile and origin, including the daemon port, not to a workspace or relay.

This requires a secure context with Service Workers, CacheStorage, Web Locks
and WebCrypto. Unsupported browsers show that local storage is unavailable.
Choosing a local interface explicitly activates the asset worker; importing
alone does not take over tabs. No marketplace or package download service is
involved.

## Manifest

`skin.json`:

```json
{
  "api": 1,
  "id": "notebook",
  "name": "Notebook example",
  "entry": "entry.mjs",
  "style": "style.css",
  "files": ["entry.mjs", "style.css"]
}
```

IDs use lowercase letters, digits and hyphens, starting with a letter. `default`
is reserved. Only declared JS/MJS, CSS, JSON, PNG, SVG, WebP and WOFF2 assets are
served. Entry and optional style must appear in `files`. Unsupported API versions
and invalid packages are omitted. Package-owned imports use local relative
paths; host-provided modules may use `/assets/` paths (the bundled Notebook
uses the shared typing presenter). There is no CDN loading or evaluation of
inline code.

The entry exports `async function mount(root, host)`. It owns `root`; use DOM text
nodes for message and profile content. No default UI scripts or global helpers
are needed. Styling is package-owned. `core.css` supplies setup-page styles and
semantic colors such as `--surface`, `--text`, `--muted`, `--accent` and `--danger`.
A package can replace these in its stylesheet. Settings and flow are package-owned;
there is no mandatory layout schema or framework.

`root` lives in a shadow tree of the page's `#skin` element, with `core.css` and
the package's stylesheet linked inside it: the package's rules apply to its own
tree only, and `document.querySelector` does not reach its elements (query from
`root`). This is styling isolation, not a security boundary: the code still runs
with the page's full trust. The page's CSP allows no inline styles or scripts,
so use the stylesheet, never `style` attributes.

The host keeps its own strip above every installed interface: an "AgentNet"
button (bottom right, in its own shadow tree, above the skin's mount) that names
the interface and the address in use and lists the way back to the built-in
interface and to other installed ones. A package's stylesheet is scoped to its
own tree, so a conforming package leaves that way back reachable; the package's
code still runs with the page's full trust, so this is a conformance
expectation, not a security guarantee. What a package does not do, it should
say on its page rather than leave out silently; the host button is where the
built-in interface takes over.

## Host API v1

| Method | Contract |
| --- | --- |
| `host.version` | `1`; additive extensions keep this version. |
| `host.platform` | `daemon` or `browser`. |
| `host.api(path, body?)` | Existing JSON `/api/*` read/action surface. Omit body for GET; provide an object for POST. Resolves parsed JSON or rejects with a user-readable error. |
| `host.listen(fn)` | Push events `{type:"change",seq}`, `{type:"disconnect"}` or `{type:"restart"}`. Returns an unsubscribe function. Reload data on change. Reconnect/reload after interruption; do not poll. |
| `host.onOpen(fn)` | Required. Register notification routing. `fn(channel)` resolves via `/api/notify/resolve?chan=...`; `fn(convID,"conversation")` opens a known DM directly. A missing destination opens the inbox, never another arbitrary chat. |
| `host.stage(file)` | Prepares a File for sending; resolves an opaque attachment value for `files` in a send. Keep that value only until that send consumes it. Browser encrypts through its engine; daemon stages locally. |
| `host.file(messageID,index,dir)` | Opens an attachment this device holds: a received one, or a copy kept of one it sent. Pass the message's own `dir` (`in` or `out`): a received id is the sender's choice and can equal a sent one here. Resolves `{bytes:Uint8Array}` (browser may add name/size/image). Show Open only where the view says `openable: true`; a sent file without a kept copy says so in `note`. Validate magic bytes before inline display; never execute HTML or SVG. |
| `host.skins` | Installed catalog, including the built-in interface. |
| `host.selectSkin(id)` | Reloads into an installed UI. The current UI must preserve or explicitly resolve unsent drafts first. |
| `host.workspace` | The membership this host is bound to: `{id, name, endpoint, address, realm, state}`. A host never changes membership: what a skin holds when an operation starts (a send, a staged file, a file open) stays bound to it. |
| `host.workspaces` | `null` on a program without workspaces. Otherwise `{list(), active(), has(id), select(id), state(id), onChange(fn), join({name, invite, agent}), disconnect(id)}` over the persistent switcher the host draws above every interface. `has(id)` is by local registration only, never by anything a message or notification says. |

### Workspaces

The host draws the workspace switcher (the module's selector, Join and
Disconnect) above every interface. When the person selects another
workspace the host mounts the skin again with a host bound to that
membership: `module.unmount(root)` (optional) is called first, then
`module.mount(newRoot, newHost)`. Keep drafts in
`host.workspaces.state(host.workspace.id)`, keyed there by conversation type
and id. The same conversation id in two memberships still names separate
drafts; shared labels never merge people or workspaces. This renderer state
survives workspace remounts on the current page, not page reloads. Stop what `host.listen` returned
in `unmount`. An operation started before the switch keeps its host: a
send under way goes where it was written, a staged file belongs to the
workspace it was staged in (the host refuses it elsewhere).

A notification fragment is `#conv=<hash>&workspace=<id>`: the host
verifies the id against its registrations, selects that workspace, then
calls `onOpen`'s handler; an unknown id opens nothing (never the current
workspace instead).

The host handles the authenticated transport, browser engine and enrollment.
It does not interpret receipt states as task completion, silently approve anything,
or automatically send messages. All effects still require normal core checks.
A skin should use returned actions/capabilities to decide which controls exist.
A browser has no local responder or daemon reminders.

Common JSON routes (see `internal/ui/ui.go` for concrete view types and
`internal/ui/server.go` for request bodies):

- Reads: `/api/overview`, `/api/thread?id=...`, `/api/dm?id=...`.
- Conversations: `/api/send` `{to,kind,body,reply_to,files}`;
  `/api/dm/new`; `/api/dm/send` `{conv,body,reply_to,files}`.
- Decisions: `/api/act`; returned message actions determine availability.
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
- Agents: `/api/dm/agent/invite`, `/decide`, `/dismiss`, `/ask` under that prefix.
- Notifications: `/api/notify/enable`, `/disable`, `/mute`, `/allow`, `/seen`,
  plus GET `/api/notify/resolve?chan=...`.
- Files: `/api/upload/discard`, `/api/file/request`; use host stage/file for
  platform differences.
- Message controls: `POST /api/message/react` `{conv?,id,dir,emoji,remove?}`,
  `/api/message/edit` `{conv?,id,dir,text}`, `/api/message/delete` `{conv?,id,dir}`.
  A message view carries what controls did to it: `reactions[]` (`emoji`, `by[]`
  as `{id,label}`, `mine`), `edited`/`revision`/`text` (the text to show; `body`
  stays what was sent), `deleted`, and `can[]` (`react`, `edit`, `delete`): show
  only the actions listed. An edit never reruns anything; a deletion hides text
  and files and recalls nothing already read, saved or given to an agent.
- Reminders: `/api/remind` and existing `/api/remind/{action}`.
- Headless: a request message may carry `exec` `{state, at, host, stale, attempt?, detail}`,
  the executing host's own signed word (never delivery, presence or a timer);
  show it apart from delivery and say when it is stale. A review notice may carry
  `report` `{host, at, items[{id, from, kind, state, blocker, since, key?, attempt?, excerpt?, actionable, result?}]}`:
  a snapshot at `at`, never a live queue; offer actions only for `actionable`
  items, through `POST /api/operator/decide` `{host, id, key, action, expect,
  attempt, text, report}`; the host's answer lands on the item as `result`.
  A notification's `#msg=<id>` (optionally `&conv=…&dir=in|out`) lands on that
  message and does nothing else.
- Storage: GET `/api/storage` (read-only; `local.areas[]` with known usage or an
  `unknown` reason, `local.complete`, `remote.status` available | unsupported |
  unavailable with the Hub's own-usage report). Show unknown as unknown, never
  zero; the Hub's quota is the whole server's, never an allowance.
- Presence refresh: `/api/refresh`, triggered by user navigation; no polling.

## Independent example and validation

`examples/skins/notebook/` is a small independent UI: a notebook with a table of
contents (people, then devices and services), one conversation page at a time,
a To line and target-named Send button, files added by choosing or dropping,
received and kept files opened as clippings, and explicit decision buttons. It
imports none of the default application. It is a developer example, not a
feature-complete alternative: it says on its page that finding new people,
adding devices, settings and notifications are not in it. Install it in a
synthetic home to exercise the contract.

Default text drafts are saved before switching interfaces and restored when
returning to AgentNet. Other interfaces have their own drafts. Pending default
attachments must be sent or removed first. Closing/reloading the default page
still loses unsent files.

Notebook keeps text, native File objects, send kind and selected conversation in
the host's existing per-workspace view state. Switching A/B/A restores that
workspace's draft and target, even when another workspace has the same ids.
Sends already started keep their original host and target; returning during a
send does not duplicate it. Accepted sends clear only the text and files they
captured; new edits and other drafts stay.

While any workspace has draft text, files or a send in progress, Notebook uses
the browser's native `beforeunload` warning for host escape/reload. Cancel keeps
the page and drafts; confirming leave discards this page's in-memory drafts.
A send already under way may still be accepted after leaving. Clean pages have
no leave listener. This is not crash durability: warning display needs browser
support and user interaction and is unreliable on mobile OS termination. See
[the browser API's limits](https://developer.mozilla.org/en-US/docs/Web/API/Window/beforeunload_event).

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

Before publishing a skin, test both providers, desktop and mobile, unread/error/
offline states, file safety, drafts, keyboard/focus and notification routing.
Never use live enrollment codes or real messages as a fixture.
