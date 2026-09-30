# UI packages

AgentNet's UI host loads a complete interface independently of the messenger.
A package can replace navigation, layout, conversation presentation, dialogs,
settings and interaction flow. It uses the same data and actions as the built-in
interface. It does not change membership, encryption, delivery or permissions.

This is the trusted package boundary for a future UI marketplace. It does not
implement marketplace discovery, downloads, signing, sandboxing or billing.

## Install without rebuilding AgentNet

Place a package in `<home>/skins/<id>/` for a laptop daemon, or
`<hub-data>/skins/<id>/` for a relay's browser page. Restart the serving process.
No Go changes, rebuild or changes to embedded app files are needed.

On Unix, the skins directory, package directories and files must belong to the
serving user and must not be writable by group or others. Keep the home/data
folder private. On Windows, access control comes from the home folder's inherited
ACL; AgentNet does not inspect Windows skin ACLs separately.

Select the package in **You → Appearance → Installed interfaces**, or open the
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
and invalid packages are omitted. Package imports must use local relative paths;
there is no CDN loading or evaluation of inline code.

The entry exports `async function mount(root, host)`. It owns `root`; use DOM text
nodes for message and profile content. No default UI scripts or global helpers
are needed. Styling is package-owned. `core.css` supplies setup-page styles and
semantic colors such as `--surface`, `--text`, `--muted`, `--accent` and `--danger`.
A package can replace these in its stylesheet. Settings and flow are package-owned;
there is no mandatory layout schema or framework.

## Host API v1

| Method | Contract |
| --- | --- |
| `host.version` | `1`; additive extensions keep this version. |
| `host.platform` | `daemon` or `browser`. |
| `host.api(path, body?)` | Existing JSON `/api/*` read/action surface. Omit body for GET; provide an object for POST. Resolves parsed JSON or rejects with a user-readable error. |
| `host.listen(fn)` | Push events `{type:"change",seq}`, `{type:"disconnect"}` or `{type:"restart"}`. Returns an unsubscribe function. Reload data on change. Reconnect/reload after interruption; do not poll. |
| `host.onOpen(fn)` | Required. Register notification routing. `fn(channel)` resolves via `/api/notify/resolve?chan=...`; `fn(convID,"conversation")` opens a known DM directly. A missing destination opens the inbox, never another arbitrary chat. |
| `host.stage(file)` | Prepares a File for sending; resolves an opaque attachment value for `files` in a send. Keep that value only until that send consumes it. Browser encrypts through its engine; daemon stages locally. |
| `host.file(messageID,index)` | Opens a received attachment; resolves `{bytes:Uint8Array}` (browser may add name/size/image). Validate magic bytes before inline display; never execute HTML or SVG. |
| `host.skins` | Installed catalog, including the built-in interface. |
| `host.selectSkin(id)` | Reloads into an installed UI. The current UI must preserve or explicitly resolve unsent drafts first. |

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
  `/api/device/service`, `/api/device/link`, `/api/device/decide`,
  `/api/device/remove`.
- Agents: `/api/dm/agent/invite`, `/decide`, `/dismiss`, `/ask` under that prefix.
- Notifications: `/api/notify/enable`, `/disable`, `/mute`, `/allow`, `/seen`,
  plus GET `/api/notify/resolve?chan=...`.
- Files: `/api/upload/discard`, `/api/file/request`; use host stage/file for
  platform differences.
- Reminders: `/api/remind` and existing `/api/remind/{action}`.
- Presence refresh: `/api/refresh`, triggered by user navigation; no polling.

## Independent example and validation

`examples/skins/notebook/` is a small independent UI: a notebook-style conversation
picker and continuous reading page, with send/reply and explicit decision buttons.
It imports none of the default application. It is a developer example, not a
feature-complete alternative: it has no enrollment editor, attachment composer,
search or full settings. Install it in a synthetic home to exercise the contract.

Default text drafts are saved before switching interfaces and restored when
returning to AgentNet. Other interfaces have their own drafts. Pending attachments
must be sent or removed first. Closing/reloading a page still loses unsent files,
as before.

Before publishing a skin, test both providers, desktop and mobile, unread/error/
offline states, file safety, drafts, keyboard/focus and notification routing.
Never use live enrollment codes or real messages as a fixture.
