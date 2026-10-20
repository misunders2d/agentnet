# Host API routes

This file lists every route a skin can reach through `host.api(path, body?)`.
[UI_SKINS.md](UI_SKINS.md) is still the skin contract; this file is its route
reference. `host.api` sends GET when `body` is omitted and POST JSON
otherwise. Put query parameters in `path`. For an action without fields, pass
`{}`, because the daemon refuses an empty body. Comic is the baseline skin: its
typed calls are in `internal/ui/web/src/api.ts`, and the view types in
`internal/ui/web/src/api.gen.ts` are generated from the Go structs in
`internal/ui/*.go`. **Platform codes**:

- `D`: this computer's program. That is the desktop app, `agentnet ui` and the
  Android app, which all run the same Go server (`internal/ui/server.go`).
  `agentnet ui --demo` also counts as `D`, but it serves only reads,
  `refresh`, sends, `act`, `person`, devices, the workspace name, approvals,
  invitations (`invite`, `invites`, `invite/revoke`), `folders`, `get-app`
  and `simulate`; `assistant-setup` answers an empty list with a note, and
  the `dm/guest/*` routes refuse (409). Every other route answers 404 there.
- `B`: a browser on a relay (`internal/ui/static/engine.mjs`, `api()`).
- `D B†`: the browser refuses the route with a sentence (quoted in Purpose).
  Android refusals are named in Purpose.

**Errors on D.** `host.api` rejects with an `Error` whose message is the JSON
`error` field, else the trimmed text body, else the status text. `.status` is
the HTTP status.

- 400 `bad request`: unknown body field (`DisallowUnknownFields`), malformed
  JSON, trailing data, empty body, or a body over 64 KiB.
- 404: a provider sentence such as "… not available here", or
  `404 page not found` for an unknown path.
- 405: wrong method.
- 409: a refusal sentence; show it as written.
- 409 `stale or disconnected workspace`: the handle was retired. Call
  `host.reconnect()`.
- 415 `JSON only`.
- 500 `internal error`.

The host itself throws before sending:

- `Invalid workspace API path` when the path is not `/api/…` or `/events`.
- `Stale workspace handle` once the membership's binding was replaced or
  disconnected; a browser membership whose relay failed its realm check
  throws that stored error instead. (`Disconnected workspace` comes from `host.workspaces.*`,
  never from `host.api`.)
- `File belongs to another workspace`.

The demo program has no workspaces. It accepts only `/api/` paths ("Expected
an AgentNet API path") and its errors are plain text with no `.status`.

**Errors on B.** The engine switches on the pathname only and **ignores the
HTTP method**. "GET" and "POST" differ only in whether `body` is passed.
`/api/reply-sessions`, `/api/agents` and `/api/approvals` refuse a body;
`/api/typing`, `/api/drive`, `/api/drive/setup` and `/api/google/access`
treat a body as their write; other reads ignore one. An unknown path throws
`Unknown request.`, and a refused route throws a sentence. Most body fields
are not checked strictly. An error from a relay call carries `.status`, the
**relay's** HTTP status (0 when the relay is unreachable), and `.code`.
`human_unsupported` and `agent_identity_unsupported` become
"<label>’s app needs an update first." (with a typographic apostrophe;
"This person’s app…" when the label is unknown), with `.code` kept and no
`.status`. Two more refusals apply:

- A device removed from its server refuses every call that has a body,
  except `/api/act`, with "This device was removed from its server: nothing
  more is sent or received here."
- While a workspace closes, these routes refuse with "This workspace is
  closing. Your draft is kept.": `/api/send`, `/api/dm/send`,
  `/api/dm/agent/ask`, `/api/request/followup`,
  `/api/topic/organization/apply`, `/api/topic/organization/undo`, `/api/history/contribution/apply`, and
  `/api/act` with `do` `reply` or `do_it`.

**Feature detection.** Use the overview booleans (below), then try the route
and catch the error. Failures differ by platform.

## Reads and events

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `GET /api/overview` | D B | Everything around the open chat. Read it again on every `change` event. With `topics=1`, archived agent topics are only counted in `topics` and are paged through `/api/topics` (Comic does this). Without it, every thread is listed. A read through `host.api` (with or without a query) records the identity that `host.reconnect()` needs. | `?topics=1` optional | `Overview` (see Overview fields) |
| `GET /api/thread` | D B | One device thread, which is a chat with an agent's device. | `?id=<any message id or synthetic direct-topic id in it>` | `{id, peer, key:{pinned?, pending?}, approved, permission_person?, question_target?, task_target?, task_grant, messages:[Message], topic?}` |
| `GET /api/dm` | D B | One conversation: a direct message or a group. | `?id=<conv>` | `{id, kind?, title?, peer, members?, role?, created, mine, frozen?, messages:[DMMessage], agents:[AgentView], guests?, main_topic?, topics?, audience_pending?, resources?:{links:[{name,url}],rev,writer,lid}}`; resources are present only for current human group members, never guests or DMs |
| `GET /api/unread` | D B | Optional, read-only page of effective unread messages, including accessible Done/Archived topics and deliberately marked sent messages. Unsupported providers omit the overview totals and refuse this route. | Optional `after` (previous `next`) and `limit` (default 50, maximum 200) | `{rows:[{key,kind,conv?,peer?,thread?,topic?,topic_title?,id,lid,author,dir,at,label,preview}],total,next?,content,seq}` |

**Events.** `host.listen(fn)` delivers the change counter. Native events may add `hint:"typing"` or `hint:"presence"`: refresh typing only, or overview plus typing, respectively. Missing/unknown hints mean a full refresh; existing skins may ignore the hint. Durable and ephemeral revisions are tracked separately, so coalescing never hides a durable invalidation.

- **D** sends `{type:"change", seq, workspace}` once on connect and then
  after changes; changes that come together are sent as one, so `seq` can
  skip. It sends `{type:"restart", workspace}` only when the daemon serving
  the page switches to an updated program (the desktop app and Android
  update as a whole: the app restarts, or Android's installer replaces it,
  and the page opens again on the new release), and
  `{type:"disconnect", workspace}` when the stream fails. After `restart`
  or `disconnect` the host has closed the stream: call `host.listen` again
  (Comic does, `store.ts`). The demo program sends the same events without
  `workspace`.
- **B** sends only `{type:"change", seq, workspace}`, with no event on
  subscribe. Stream loss arrives as a `change`, and connectivity is then in
  `overview.device.online`.

### Overview fields

| Field | Platforms | Meaning |
|---|---|---|
| `version` | D B | The build serving the page. D: the program's version (`"demo"` in the demo). B: the page's build, or the server's once the page is stale. A change while the skin runs means an update. Comic keeps the first value. On B it reloads by itself once nothing unsent would be lost. |
| `release` | D | A recommended build newer than this one, else absent. Always `""` on B. |
| `update_required` | D B | `{latest, url?, auto?}` while the relay refuses this device until it updates. Show a prominent update banner. |
| `seq` | D B | The change counter. It matches the `change` events. |
| `unread_total`, `alert_total` | D B | Optional authoritative totals across the accessible messages, including Main, named topics, Done/Archived and deliberately unread outgoing turns. `alert_total` excludes locally muted conversations; it does not enable alerts or change read state. Missing means unknown/unsupported, not zero. Do not add topic counts or `archived_unread` again. |
| `demo` | D B | `true` only on `agentnet ui --demo`. |
| `app` | D | `true` in the desktop app's or the Android app's window, otherwise absent (never on B). It does not mean the `host.app*` methods are present: they always are. |
| `topic_list` | D B | Topics are kept here, so use `?topics=1` and `/api/topics`. `false` in the demo. |
| `remind` | D | Reminders can be set. `false` in the demo and absent on B, so hide "Remind me" there. |
| `schedules` | D B | Optional local scheduled sending. Offer its controls only when true; absent/false providers refuse the routes below. No schedule records are included in overview. |
| `persons` | D B | Conversations between people are available. |
| `agents` | D B | Agents can be invited into conversations. `false` in the demo. |
| `groups` | D B | Group operations are available. `false` in the demo. |
| `files` | D B | Attachment limits `{max_file, max_count, max_message?}`. Absent means no attachments (the demo). `max_count` is 8 on both; B adds `max_message`. |
| `notify` | D B | `{available, native, enabled, pending, reason?, mutes:[conv], allowed:[address], preview?, preview_default?}`; B has no `native`. Absent when the provider has no alerts. Native preview choices are supported only when `preview_default` is present; `preview` absent/empty uses that default. On Android, `available` and `native` mean only that the app's notifier is registered; they do not follow the OS permission: use `host.android.notificationStatus()`. |
| `reply_receivers` | D | `GET /api/reply-receivers` is served. `true` on a live program, Android included, and `false` in the demo. Absent on B. |
| `reply_sessions` | D | Native reply sessions are listed. `true` on a live program and `false` in the demo. Absent on B, although `GET /api/reply-sessions` answers there. |
| `me` | D B | `{address, fingerprint, responder, responder_dir, agent}`. B adds `browser:true` and always has `responder:""` and `agent:false`. |
| `device` | B | `{online, revoked, persisted}`: the browser's connection to its relay. |
| `receive_status` | B | `{waiting, failed, more?, error?, attempts?, at?}` while received messages wait for verification (`error`, `attempts` and `at`: the latest failure). |
| `transport_error` | D | Android only: the receive loop stopped; cleared when the app connects again. |
| `workspace` | D B | `{name, server}`: the admin's name for the workspace and the relay host. |
| `role` | D B | `unset`, `person` or `service`. |
| `person`, `people`, `dms` | D B | This installation's person, the known people, and the conversations (`DMSummary`, with `unread`, `held`, `decide` and `waiting` counts). |
| `threads`, `topics` | D B | Device threads (`ThreadSummary`), and per-peer topic counts `{peer, total, archived, archived_unread, latest}`. |
| `needs_you`, `held` | D B | `ConvItem`s that wait for this person: decisions, and held person turns. See Request states. |
| `review`, `quarantine` | D B | Received device items and review notices, and held envelopes with `code` and `action`. |
| `directory`, `agent_devices`, `model_reports` | D B | The relay's member list, the devices that say they run an agent (a hint), and private model reports. Each report carries exact `host`, `host_key`, `agent_id`, `model`, optional `reasoning`, `harness`, `executor`, `at` (Unix seconds), and `revision`. Reports are historical observations for current own-human readers, not current settings; match the exact agent/host/key and show absent reasoning honestly. |
| `link`, `links`, `history`, `group_invitations` | D B | This device's own join request, new devices asking to join, history copying, and group invitations. |
| `reminders` | D | Pending reminders, soonest first. |
| `picture_url` (any `PersonView`) | D B | D: `/workspaces/<id>/<handle>/api/picture/<64 hex>`, already qualified by the host. Use it only as `<img src>`, never through `host.api`. B: `""` until the picture loads, then a `blob:` URL that arrives with a later `change`. |

## Conversations and messages

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `POST /api/send` | D B | Send a message, question or task in a device thread. B accepts only a `reply_receiver` of `{kind:"human"}` or one with an exact `host`. Android refuses (409) a non-human receiver on itself. | `{to, kind, body, reply_to?, quote?, files?: [staged], id?, agent_id?, reply_receiver?}`, where `kind` is message, question or task | `{id, lid?, state, path?, detail?}` |
| `POST /api/dm/new` | D B | Start a separate conversation with the person on a device. Nothing is sent until a message is. | `{address}` | `{id}` |
| `POST /api/dm/send` | D B | Send a message in a conversation. `files` holds `host.stage` results: string ids on D, staged objects on B. The same `reply_receiver` refusals as `/api/send` apply on B and Android. | `{conv, body, reply_to?, quote?, topic?, files?: [staged], id?, pid?, reply_receiver?}` | `{id, lid?, state, path?, detail?}` |
| `POST /api/dm/resources` | D B | Replace a group's named links as a current human member. Keep the original `rev` and `writer` while editing; a stale revision or changed authority is refused, never automatically retried. An empty list clears links. No provider access or task is created. | `{conv,links:[{name,url}],rev,writer}`; nonempty trimmed names and absolute HTTP(S) URLs without credentials; encoded list at most 64 KiB | `{links:[{name,url}],rev,writer,lid}` |
| `POST /api/refresh` | D B | One bounded check when a chat opens: presence and the receipts still in custody. Never poll it. | `{id}`: a thread message id or a conversation id | `{text, at?}` (B: `{text:""}`) |
| `POST /api/message/react` | D B | Add or remove a reaction. | `{id, dir, conv?, emoji, remove?}` | `{note}` |
| `POST /api/message/edit` | D B | Edit your own message. A running question or task keeps its original body. | `{id, dir, conv?, text}` | `{note}` |
| `POST /api/message/delete` | D B | Delete your own message. This hides it and cancels nothing. | `{id, dir, conv?}` | `{note}` |
| `POST /api/conversation/delete` | D B | Delete a conversation on all of this person's devices, or a device thread on this device. | `{conv}` or `{peer, thread}` | `{note}` |
| `POST /api/history/contribution/preview` | D B | Review quoting selected messages from one chat into another. | `{source, ids:[id], destination, topic?, new_topic?, title?, files?:[{id, index}]}` | review: the request plus `{operation, token, body, items, audience, imported_at, audience_pending}` |
| `POST /api/history/contribution/apply` | D B | Send exactly the reviewed contribution. If anything changed, it is refused: review again. | the review, unchanged | D: `{ID, LID, State, Detail, Copies}` (Go field names). B: `{id, lid, state, detail?, copies?}` (a repeated apply answers without `detail`) |

## Topics

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `GET /api/topics` | D B | One page of topics, unread first, then most recently active; ID and peer break ties. Exact agent filtering precedes counting and pagination. | `?conv=` (a people chat) or `?peer=` (agent chats; empty means every peer), plus optional `agent_host`, `agent_key`, `agent_id` (the immutable destination tuple), `state` (active, done or archived), `q`, `before` (the previous page's opaque `next`) and `limit` | `{topics:[ThreadSummary], next?, matched}` |
| `POST /api/topic/create` | D B | Start a topic in a people chat, rooted at one message. Agent threads have no `create`: D answers 404 and B "No such topic change." | `{conv, peer:"", id:<the message's lid>}` | `{note}` |
| `POST /api/topic/rename` | D B | Rename a named people-chat topic for everyone with access; any current authorized participant may do so. An empty `title` restores its automatic name for everyone. Device-thread names and `root:true` Main-flow labels remain personal. | `{peer, id, title}`, `{conv, peer:"", id, title}` or `{conv, root:true, peer:"", id:"", title}` | `{note}` |
| `POST /api/topic/done` | D B | Mark a topic done. `count` is how many messages the page showed. Batch with `ids` and `counts`. The Main flow (`root`) has no `done`: archive it instead, as Comic does. | `{peer or conv, id, count?}` or `{…, id:"", ids:[id], counts:{id:n}}` | `{note}` |
| `POST /api/topic/reopen` | D B | Reopen a topic, or the Main flow with `root:true`. | `{peer or conv, id, count?}` or `{conv, root:true, peer:"", id:"", count?}` | `{note}` |
| `POST /api/topic/archive` | D B | Archive a topic. This deletes nothing. Works in batches and with `root:true`. | `{peer or conv, id}`, `{…, ids, counts?}` or `{conv, root:true, peer:"", id:"", count?}` | `{note}` |
| `POST /api/topic/delete` | D B | Delete a topic: on this device for an agent thread, on your devices for a chat. Works in batches and with `root:true`. | as for archive | `{note}` |
| `POST /api/topic/organization/preview` | D B | Review moving selected messages into another topic. Main is an explicit choice: `topic:"", main:true`. | `{conv, ids:[up to 200 ids], topic, main?, merge?, new?, title?}`; direct agent chats: `{peer, ids, topic, merge?}` or `{peer, ids, new:true, title, topic:""}` (a supplied fresh topic ID is also accepted) | review: the normalized request plus `{operation, token, moves:[{lid, author, hash, topic?}], parent?}`; direct reviews also bind `destination:{host,fingerprint,agent_id?}` and optional `synthetic_topics:[id]`; a new direct topic uses `topic === operation` |
| `POST /api/topic/organization/apply` | D B | Apply exactly the reviewed move. If anything changed, it is refused. | the review, unchanged | D: `{ID, LID, State, Detail, Copies}`. B: `{id, lid, state, detail?, copies?}` (a repeated apply answers without `detail`) |
| `POST /api/topic/organization/undo` | D B | A current full-member human restores an admitted agent move to its persisted prior source topics, including Main. All source groups are checked and committed together; intervening changes refuse the inverse. Reuse the same `id` for a retry. | `{conv, lid, author, id}`; direct agent chats on the originating device: `{peer, lid, id}` (an empty `conv`/`author` may be included) | array of sent operations, in the platform's existing sent shape |

Unread rows supply navigation references only. `key` identifies the original
author and logical message within its scope; `id` is the currently selected
local message ID. For `kind:"chat"`, use `conv` and `topic` (absent/empty is Main);
for `kind:"device"`, use `peer` and `thread`. Open that exact scope and focus
`id`, preserving `dir`; do not search unrelated conversations or infer authority
from the label/preview. Merely listing or sorting unread rows never acknowledges
them or decides a request.

`topic_title` is a bounded literal display name from the same topic projection,
including shared renames and automatic-name resets. Use it to identify a named
scope that has never been opened; `label` still identifies the author. An empty
chat topic means Main flow. These names never establish access or authority.

Old personal people-topic names remain personal until an explicit shared
rename. No historical cross-person rename time is inferred. Later admitted
renames converge by revision and original writer; a rename made after observing
another supersedes it. A stale allocation is refused for explicit retry. Topic
IDs, membership, read state and messages are unchanged by renaming.

Both topic and unread cursors describe positions, not retained row identities.
Pass them unchanged. A topic cursor from the older chronological format is
refused; reload from the first page. During live changes, refill the already
loaded prefix and deduplicate scoped identities. This is a moving list, not a
snapshot: a representative can change position. Preserve displayed order under
pointer/focus/selection while updating counts, and make vanished rows inactive
until the interaction ends.
A non-empty topic search uses trimmed `q` and omits `state` on every page,
searching Active, Done and Archived together within the existing conversation
or peer scope. Show each result's actual state. Clearing the search restores
the selected status filter; changing tabs during search does not narrow results.

## Files and Drive

Staging, opening and discarding files are covered by host methods (see
Host-internal). Google Drive is outside AgentNet end-to-end encryption.

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `POST /api/file/request` | D B | Ask the device of yours that a history message came from for its file (`availability: "requestable"`). | `{id, index}` | D: `{ok:true}`. B: `{note}` |
| `POST /api/file/open-image` | D | Open one verified PNG, JPEG, GIF or WebP attachment in this computer's default image app, through a private copy kept for one hour. It takes no path or program. Android answers 404 "Open in default app is available on a computer."; other files are refused (409) with "Use Download". B: "Unknown request." (show the bytes fullscreen or download instead) | `{id, dir: "in"\|"out", index}` | `{ok:true}` |
| `GET /api/drive` | D B | A conversation's Drive project-space status. Usually called through `host.drive.drive({conv, action:"status"})`. | `?conv=` | `{enabled, configured, connected, full, space?, pending?, folder?, page?, file?, permissions?, next?, consent_url?, account?, grants?, notice}` |
| `POST /api/drive` | D B | A Drive action. `action` is one of status, configure, consent, disconnect-account, create, connect, disconnect-space, publish-pending, list, permissions, share, remove-permission or grant. B refuses `configure` ("Workspace admin configures public Google client IDs in Settings > File storage options"). D also accepts `save-attachment`, which answers only `file` and `notice` (its status fields read false), not the status view; B answers it "Unknown Drive action". D errors are text: 404 when this program has no Drive, 401 consent needed, 403 denied or missing capability, 503 offline, 400 otherwise. | `{conv, action, name?, folder?, page?, full?, confirm_outside_e2ee?, confirm_account?, client_id?, client_secret?, email?, role?, permission?, confirm_access?, pid?, grant?, message?, index?, dir?}` | same view as GET |
| `POST /api/drive/upload` | D B | Upload plaintext to Drive, after explicit confirmation. On D the body is octet-stream with `?conv&name&size&confirm_outside_e2ee=true` (32 MiB at most), so use `host.drive.driveUpload(conv, file, true)`: `host.api` cannot send it. | B: `{conv, file: File, confirm:true}` | the Drive file `{id, name, mimeType, size?, parents?, capabilities, trashed?}` |
| `GET /api/drive/setup` | D B | Settings > File storage options (workspace admin). | — | `{settings, draft, steps, notice}` |
| `POST /api/drive/setup` | D B | Change file storage options. `action` is status, local-secret, save-draft or publish. B refuses `local-secret` ("Browser uses Google Web OAuth consent; no Desktop secret on browser"). | `{action, draft?, expect?, confirm?, desktop_client_secret?}` | `{settings, draft, steps, notice}` |

## Agents, requests and decisions

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `POST /api/act` | D B | A local decision, permission change, or personal read/attention state. Request decisions use only the item's `actions`; personal read/unread controls use admitted visible messages. B accepts 8 of the 18 verbs (table below). | `{do, id?, body?, reason?, ids?, attention?: {id:{rev,writer}}, key?, attempt?, send_id?, held?}` | `{note}` |
| `POST /api/operator/decide` | D B | Decide a request that another host reported, as its granted operator. A current owner-human device may also answer a private native callback, without an operator grant; see the native contract below. On your own host you can also `continue`, `resolve` or `cancel` it. `check:true` only checks that you may, and only for your own waiting request with `report:""`: `resolve` with `expect:"needs_human"` or `cancel` with `expect:"running"`. | `{host, id, key, action, expect, attempt, report, text?, send_id?, check?}`, where `action` is accept, decline, resolve, reply, cancel or continue | `{note}` |
| `POST /api/request/followup` | D B | Send a correction to your exact request and executor. A positive `attempt` binds only its current run. | `{id, conv?, ref:{id, fingerprint}, attempt?, body, files?: [staged]}` | `{note}` |
| `GET /api/approvals` | D B | Standing grants, as `agentnet approvals` lists them. B always answers `{questions:[], tasks:[], participations:[], read_only:true}`, and refuses a body. | — | `{questions, tasks, native_tasks?, own_devices?, removed_devices?, participations, read_only, unresolved?}` |
| `POST /api/approvals/revoke` | D B† | End one standing grant. B: "Nothing runs in this browser: approvals and grants are made and revoked on a computer with AgentNet." | `{kind:"question" or "task", person}`, `{kind, address}` or `{kind:"participation", pid}` | `{note}` |
| `GET /api/agents` | D B | Without `host`: this computer's agents (`local:true`, with harnesses). With `?host=`: that host's verified public catalog. B needs another device's `host`; without it, it refuses "This browser runs no local agents or responders." | `?host=` optional | `{host, local, agents:[{record:{v, id, host, host_key, label, ts, sig?, harness?, harness_sig?}, enabled, responder?, default?}], harnesses?}` |
| `POST /api/agents` | D B† | Configure the agents on this computer. B: "This browser runs no local agents or responders; configure agents on their host computer." Android refuses (409). | `{action, id?, label?, harness?, dir?}`, where `action` is create, update, disable or publish | `{saved, published, agent?, note}` |
| `GET /api/responder` | D | How this computer handles questions and tasks. B: "Unknown request." | — | `{agent_id?, chosen, manual, harness?, dir?, timeout_seconds?, context?, ready, problem?, harnesses:[{name, found, path?, tested_live?, question_mode?}]}` |
| `POST /api/responder` | D | Choose manual handling, configure a harness and folder, or select an existing concrete local `agent_id` as the default. Android refuses (409). B: "Unknown request." | `{agent_id?, manual, harness, dir}` | the GET view plus `note` |
| `GET /api/reply-receivers` | D | Local reply-receiver bindings: replies continued in native sessions or by managed agents. B: "Unknown request." | — | `[{id, conv?, request_ref, receiver, label, host, state, detail?, handoff_state?, handoff_label?, inputs?}]` |
| `GET /api/reply-sessions` | D B | The native sessions a reply can be routed to. Without a query: this host (B: an empty list). With `host` and `host_key` together: a linked host's catalog. | `?host=&host_key=` optional, both or neither | `{host, local, sessions:[{handle, harness, label, active}]}`; a linked host adds `host_key, status` (pending, ready or unavailable), `at?, detail?` |
| `POST /api/dm/agent/invite` | D B | Invite a member's agent; an accepted human topic guest may bring only their own agent into that exact topic, with already-visible selected history and no task-key grant. | `{conv, host, share:[message id], tasks_from:[fingerprint], note, agent_id?, topic?}` | `AgentView` |
| `POST /api/dm/agent/decide` | D B† | Accept or decline an invitation for this device's agent (`needs_you` reason `agent_invite` without `role:"human"`; D refuses a guest invitation here with 409). B: "This browser runs no agent: an agent is accepted on the computer that runs it." | `{pid, accept}` | `AgentView` |
| `POST /api/dm/agent/dismiss` | D B | End an agent's part in a conversation. | `{pid}` | `AgentView` |
| `POST /api/dm/agent/ask` | D B | Ask a question of, or give a task to, an active participant agent. The same `reply_receiver` refusals as `/api/send` apply on B and Android. | `{pid, kind, body, topic?, files?: [staged], id?, send_group?, reply_receiver?}` | `{id, lid?, state, path?, detail?}` |

An ordinary tagged message or direct-agent follow-up binds to that agent's
verified, nonstale `running` request in the current chat/topic. Direct threads
send `conv:""`, the exact original message ID and `from_key`, with the current
host pin and selected default/named identity; invited requests retain their
conversation/PID binding. Send the positive safe-integer `exec.attempt`
as `attempt`, with the exact original author/ref, retained send ID and files.
No active request uses the ordinary ask route. If multiple roots are active,
resolve through the existing Reply ancestry or retain the draft and report
ambiguity; do not choose the newest or add a request selector. A strict
correction cannot start later work or fall back to a queued continuation.
Only the host's `steered` status proves native consumption; the POST note does
not. Exact uncertain retries retain their original ID/ref/attempt/body/files.
Omitted or zero `attempt` preserves the legacy API's queued-followup semantics;
the ordinary tagged composer always binds a positive attempt for active work.

### `/api/act` verbs

| Verb | Fields | Effect | B |
|---|---|---|---|
| `reply` | `id, body, send_id?` | Answer a received question or task by hand, which takes it over. | yes |
| `do_it` | `id, send_id?` | Confirm a stored proposal as a task to the same agent. | yes |
| `change_proposal` | `id, body, send_id?` | Confirm a revised proposal. | yes |
| `continue` | `id, body, attempt, key, send_id` | Continue a `needs_human` request held on this device (the item's `continuation`) with an answer, or return the domain-tagged native response described below to the exact current local callback. When `continuation.host` is set, send `POST /api/operator/decide` `{host: continuation.host, id: continuation.id, key: continuation.key, action:"continue", expect:"needs_human", attempt: continuation.attempt, text, send_id, report:""}` instead. | no (B uses `/api/operator/decide`); Android no |
| `accept` | `id` | Run a task once, let the responder answer a held question, or run again. | no; Android no |
| `accept_always` | `id` | Accept, and let later tasks from that sender run without asking. | no; Android no |
| `decline` | `id, reason?` | Decline. The requester is told, and nothing runs. | no |
| `resolve` | `id` | Close a `needs_human` or interrupted item without a reply. It also dismisses `self_consented` and `device_admin` notices and held turns. | yes |
| `cancel` | `id` | Stop a running request. | no; Android no |
| `approve` | `id`: person id or device address | Answer this sender's future questions automatically. | no; Android no |
| `unapprove` | `id`: person id or device address | Remove that question permission. | no |
| `trust` | `id` (peer), `key` (the fingerprint compared) | Trust a peer's changed key. | no |
| `grant_tasks` | `id`: person id or device address | Let this sender's future tasks run without asking. | no; Android no |
| `revoke_tasks` | `id`: person id or device address | End that task permission. | no |
| `archive_held` | `id` | Hide one held-envelope notice here. Nothing is accepted. | yes |
| `archive_held_batch` | `held:[{id, reason, detail_code}]` | Hide several notices. | yes |
| `read` | `ids:[id], attention?: {id:{rev,writer}}` | Acknowledge the exact displayed messages. For device threads, include visible outgoing IDs too: verified original sends can have incoming copies on another own device. Submit only the open topic's IDs and deduplicate successful acknowledgments. Only the exact full observed attention generation clears a deliberate unread mark; absent or stale generations leave it intact. Reading never accepts or reruns work. | yes |
| `unread` | `id` anchor, `ids:[id]` including the anchor | Atomically mark these exact admitted normal messages in the anchor's current chat/topic unread. A single-message action sends one ID; from-here sends that row and later admitted rows in display order from the complete topic view, without a product count cap. Later arrivals and sibling topics are excluded. This changes personal attention only, not receipts, notifications, permissions or execution. | yes |

The browser refuses the other verbs with "Nothing runs in this browser:
accept, approve and grants are made on a computer with AgentNet." Android
refuses its verbs with 409 "This Android device cannot run or configure local
assistants. Use your linked agent computer." An unknown verb gives 409
"Unknown action." on a live D; the demo answers 404 "no such item" or 409.

## People, devices and guests

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `POST /api/person` | D B | Create this installation's person, once. | `{label}` | `{person: PersonView, note}` |
| `POST /api/person/label` | D B | Change your own display label. It is a claim, never checked. | `{label}` | person record `{person, label, email?, picture?, address, fingerprint, state, …}` |
| `POST /api/person/picture` | D B | Set your picture, or clear it with `""`. The PNG is 64 KiB at most; D caps this body at 90 KiB. | `{png: base64 PNG}` | person record |
| `GET /api/picture/{hash}` | D | PNG bytes. Use it only as `<img src>` through `picture_url` (see Overview fields), never through `host.api`. B has no route: its `picture_url` is a `blob:` URL. | — | `image/png` |
| `POST /api/device/service` | D B† | Mark this installation as a service, with no person. B: "A browser is always a person's device: a service joins from a computer with AgentNet." | `{}` | `{note}` |
| `POST /api/device/link` | D B | Make a one-use link for a new device of this person. | `{}` | `{url, expires, app_url?}` |
| `POST /api/device/decide` | D B | Approve or refuse a new device asking to join (`overview.links`). `agent_host:true` (accept only) approves it as an agent host; D refuses it (409) where unsupported. | `{id, accept, agent_host?}` | `{note}` |
| `POST /api/device/remove` | D B | Take a device off this person. The last device cannot be removed. | `{address}` | `{note}` |
| `POST /api/dm/guest/check` | D B | Check before inviting a person as a guest. | `{conv, host}` | `{ready, needs_update:[{label, me, role}], offline:[label], text}` |
| `POST /api/dm/guest/invite` | D B | Invite a person as a guest into a conversation. | `{conv, host, share?:[message lid or id], note?, topic?}` | `GuestView` `{pid, state, state_text, host, host_here, inviter, shared, missing, held, can_decide, can_leave, can_end, can_send, audience_pending, topic?, needs_update?}` |
| `POST /api/dm/guest/decide` | D B | Accept or decline a guest invitation. | `{pid, accept}` | `GuestView` |
| `POST /api/dm/guest/end` | D B | Leave a guest participation, or end it. | `{pid}` | `GuestView` |

## Groups and teams

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `GET /api/groups/invitations` | D B | Group invitations, sent and received. Comic reads them when `overview.groups` is set. | — | `[{id, conv, direction, status, title, inviter, target, history, can_cancel?, can_refresh?, error?}]` or `null` |
| `POST /api/groups/new` | D B | Create a group. | `{title}` | `{id}` |
| `POST /api/groups/invite` | D B | Invite a person, optionally with selected history. | `{conv, person, history:{last?, since?, refs?:[{lid, author, hash}]}}` | group invitation |
| `POST /api/groups/decide` | D B | Accept or decline a received invitation. `accept` is required. | `{id, accept}` | `{recorded}` |
| `POST /api/groups/publish` | D B | Publish an accepted invitation. | `{id}` | `{published}` |
| `POST /api/groups/cancel` | D B | Cancel an invitation you sent (`can_cancel`). | `{id}` | `{cancelled}` |
| `POST /api/groups/refresh` | D B | Renew an invitation you sent (`can_refresh`). | `{id}` | group invitation |
| `POST /api/groups/manage` | D B | Rename the group, promote, demote or remove a member, or leave. | `{conv, action, title?, person?}`, where `action` is rename, promote, demote, remove or leave | `{queued}` |
| `GET /api/teams` | D B | This realm's teams. B answers `status:"unsupported"` when the relay publishes no realm, without `tags` and `realm_id`. | — | `{tags, legacy_add?, realm_id?, status, current, reason?, at, truncated, teams:[{…TeamState, member, manager, conflict, listed}]}` |
| `POST /api/team` | D B | Change a team. `op` is create, join, leave, rename, archive, restore, remove, manager-add, manager-remove, add, agent-add or agent-remove. Without a realm, B refuses "Teams are not available on this server." | `{op, team?, name?, target?, agent?:{id, host, host_key}, v?}` | `{realm_id, id, name, seq, hash, managers, members, agents?, archived, version?}` |
| `POST /api/teams/snapshot` | D B | A reviewable snapshot of the selected teams. It adds no one to a conversation. | `{teams:[id]}` | `{realm_id, sources:[{id, seq, hash}], persons:[{id, seq, hash}], at}` |

Legacy people lists retain version-1 join/leave and manager rules. With
`legacy_add:true`, a current manager may add a person; that signed transition
uses version3 and keeps the same list identity and chain. Version2 tags keep
managed person/agent-target semantics. Version3 is a legacy-list extension,
not conversion to a tag; do not offer agent targets or remove Join/Leave.
`legacy_head`, when present in TeamState, is a replay-derived compatible signed
prefix reference for old readers, never an incoming membership proof. Selection
requires a current matching head and is rechecked after roster refresh. No list
operation grants conversation or task access.

## Notifications and typing

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `POST /api/notify/enable` | D B | Turn alerts on. Android refuses when native notifications are unavailable. B subscribes to Web Push. | `{}` | `{note}` |
| `POST /api/notify/disable` | D B | Turn alerts off. | `{}` | `{note}` |
| `POST /api/notify/mute` | D B | Mute one conversation, or unmute it. | `{conv, muted}` | `{note}` |
| `POST /api/notify/allow` | D B | Let one person's turns alert, or stop them. | `{person: <person id>, allowed}` | `{note}` |
| `POST /api/notify/seen` | D B | Report the messages this page actually presented. | `{conv, ids:[id]}` | `{note}` |
| `POST /api/notify/preview` | D | Optional local native notification content preference. Changes neither alert consent nor mute/sender scopes. Unsupported providers refuse it. | `{preview:""\|"full"\|"sender"\|"hidden"}`; empty means host default | `{note}` |
| `GET /api/notify/resolve` | B | Turn a browser notification's channel into a conversation (`host.onOpen` kind `"channel"`). D has only `POST /api/notify/{what}`: GET gives 405, and POST gives 404 "no such alert control". | `?chan=` | `{conv}` (`""` when unknown) |
| `GET /api/typing` | D B | Who is typing in a scope, and this device's preferences. | `?conv=&peer=&thread=` | `{scope, preferences:{send, show}, supported, current, entries:[{person?, address, label, expires}]}` |
| `POST /api/typing` | D B | Report that this person is typing. It carries no content. | `{scope:{conv?, peer?, thread?}, active}` | `{submitted, skipped, throttled}` |
| `POST /api/typing/preferences` | D B | This device's own choices: share my typing, show others'. | `{send, show}` | `{send, show}` |
| `POST /api/remind` | D | Set or move a "remind me later" reminder on a received message. B: "Unknown request." (`overview.remind` is absent there.) | `{id, due: unix seconds}` | `{note}` |
| `POST /api/remind/done` | D | Mark a reminder done. B: "Unknown request." | `{id}` | `{note}` |
| `POST /api/remind/cancel` | D | Cancel a reminder. B: "Unknown request." | `{id}` | `{note}` |

Full preview includes locally resolved message content; Sender includes only the
verified author and generic text. Hidden, and Sender without a verified author,
retain only fixed activity, reminder, decision or security notices, never caller
text or remote computer names. Built-in desktop
notifications default to Hidden. Content-callback hosts retain their configured
default, including the mobile Full default. Show the actual `preview_default`,
not a guessed platform default. Changing this preference does not dismiss old
OS notifications or change lock-screen, sound, DND or permission settings.
Closed-page browser push stays generic. Existing macOS osascript banners cannot
route a click back to the exact message; do not promise that behavior there.

## Scheduled messages

These optional routes belong to the selected local workspace/device. They do
not schedule an agent job. A browser must have its AgentNet page open; a native
sender must be running. Pending work dispatches at or after its deadline when
connectivity and current chat information permit. Mobile process limits can
delay it. No relay-side timer or closed-page browser execution is promised.

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `GET /api/schedules` | D B | List this device's schedules, optionally for one exact conversation. | Optional `conv`; `all=1` includes terminal records, absent/0 lists active records | `ScheduledSend[]` |
| `GET /api/schedules?id=…` | D B | Read one exact schedule, or 404. Cannot combine with a nonempty `conv` or `all`. | Exact 32-lowercase-hex `id` | `ScheduledSend` |
| `GET /api/schedule/audience` | D B | Read the current verified audience for explicit review. Reads do not change a saved ceiling. | `conv`, optional existing `topic` | `{persons,pids,author_pid?}` |
| `POST /api/schedule/create` | D B | Durably save an ordinary human chat message locally. | `ScheduleDraft`, optional stable `id`, no positive `rev` | `ScheduledSend` |
| `POST /api/schedule/edit` | D B | Change a mutable schedule at its exact revision. | `ScheduleDraft` with `id` and positive `rev` | `ScheduledSend` |
| `POST /api/schedule/cancel` | D B | Cancel a mutable schedule. | `{id,rev}` | `ScheduledSend` |
| `POST /api/schedule/send_now` | D B | Dispatch through the same guarded sender now, retaining its approved audience. | `{id,rev}` | `ScheduledSend` |

`ScheduleDraft` is `{id?,rev?,conv,topic?,body,reply_to?,quote?,due,timezone,
files?,audience?}`. `due` is an absolute RFC3339 instant. Browser precision is
milliseconds; nonzero finer digits are refused. Stage actual files through
`host.stage` and pass its values unchanged: native string IDs and browser
workspace-bound file handles differ. Parsed save/refusal attempts consume native
stage IDs. A retry re-stages the original actual files. Omitted edit files retain
and reverify them; `files:[]` removes them.

`ScheduledSend` returns the draft fields, `id`, `rev`, `state`, the saved audience,
file descriptors `{sha256,size,name}`, and optional fixed `detail`. States are
`pending`, `dispatching`, `sent`, `cancelled`, `needs_review`. **Sent means queued
in the ordinary outbox, not delivered.** A same-scope text/time edit omitting
`audience` retains its ceiling. Scope changes or reapproval require a newly
reviewed exact audience; Send now never recaptures one silently.

Capture one immutable create ID, payload and actual files before saving. The same
ID and equal canonical content return the existing record without another save,
including a terminal or past-due record; different content or a pre-existing
ordinary message LID refuses with 409. A response loss or failed exact lookup is
not a reason to allocate another ID. Exact GET proves presence only: it never
proves file equality from a name/size or permits clearing a different draft.
An unconfirmed request can still commit after a not-found lookup. Keep that
uncertainty visible, preserve the draft, and do not automatically resend after a
page reload or recover nonexistent file bytes.

A validated `ReportItem.native` is an actionable native request for the current
owner-human device even with `actionable:false`; it never grants operator access.
The complete DTO/forms/lifetime contract is in [UI_SKINS.md](UI_SKINS.md).
For a local daemon use the existing `POST /api/act` continue action with
`{id,key,attempt,body,send_id}`. For another own host use existing
`POST /api/operator/decide` with `{host,id,key,attempt,action:"continue",
expect:"running",report:<exact private notice id>,text,send_id}`.
`body`/`text` is `agentnet-native-response-v1\n` followed by JSON
`{token,choice,answers?,values?}`. Retry the identical text and send ID after an
ambiguous transport result. `expires:0` means the exact live process lifetime;
nonzero expiry is Unix milliseconds. Native resolution/end/cancel/restart
remove the action. The host rechecks current exact human/host keys, admission,
report/ref/attempt/token/native call and one-shot lifetime; operator, agent-host,
foreign and historical claims never authorize it. Saved is not completed.

## Workspace, storage and setup

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `GET /api/workspace` | D B | The workspace's own name, and whether this device may rename it for everyone. | — | `{name, server, can_rename}` |
| `POST /api/workspace/name` | D B | Rename the workspace for everyone (admin only). `""` clears the name. | `{name}` | `{name, server, can_rename}` |
| `GET /api/storage` | D B | Read-only: where this device's files are kept and how much space they use. | — | `{local, remote}` |
| `GET /api/assistant-setup` | D | Connect coding sessions (hooks): which tools are detected and configured. Android and the demo answer `local:false` with a `note`. B: "Unknown request." | — | `{local, harnesses:[{id, label, detected, configured, registered, supported, state, note, change?, next?, target?}], review_id?, note?}` |
| `POST /api/assistant-setup` | D | Review a change with `action:"review"`, then apply exactly that review with `action:"apply"`. Any other action gives 409 on a live program, Android included; the demo answers every POST with `local:false` and a note. B: "Unknown request." | `{action, harnesses?:[id], review_id?}` | same view |
| `GET /api/folders` | D B† | One folder's subfolders, read-only, for Connect an agent's folder picker. Android refuses (409). B: "Folders are chosen in the AgentNet app on a computer: this browser cannot see that computer's folders." | `?path=` optional | `{path, parent?, home, roots?, dirs:[{name, path}], truncated?}` |
| `GET /api/get-app` | D B | Where to download the AgentNet app for this version. B reports the relay's version and detects the platform from the browser. | — | `{version, detected?, platforms:[{id, label, url}]}` |
| `POST /api/invite` | D B | Make an invitation link for a new person (admin only). | `{name, admin, days}`, where `days` is 1, 7 or 30 | `{link, label, expires, message}` |
| `GET /api/invites` | D B | Whether this device may invite, and the unused invitations. | — | `{can_invite, invites:[{id, name, label, admin, by, created?, expires}]}` |
| `POST /api/invite/revoke` | D B | Withdraw an unused invitation. | `{id}` | `{revoked:true}` |
| `GET /api/google/access` | D B | Google sign-in membership settings. | — | `{workspace_url, enabled, can_admin, emails:[{domain_member, email, admin, denied}], domains}` |
| `POST /api/google/access` | D B | Add or remove an email or a domain (admin). | `{email?, domain?, remove, admin?}` | D: `{}`. B: `null` |

## Demo only

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `POST /api/simulate` | D B† | Demo controls that stand in for events elsewhere. Served only by `agentnet ui --demo` (`overview.demo`); a live program answers `404 page not found`. B: "Not available here." | `{what}`, where `what` is `"arrival"` or `"finish"` | `{}` |

## Host-internal

These routes back host methods. A skin uses the method, not the route. The
exception is `/api/upload/discard`, which skins call through `host.api`.

| Route | Platforms | Purpose | Request | Response |
|---|---|---|---|---|
| `POST /api/upload` | D | Behind `host.stage(file)` on D: the file's bytes as octet-stream. `host.api` cannot send it. | `?name=` (at most 255 bytes, no control characters) | `{id}` |
| `POST /api/upload/discard` | D | Release staged ids that will not be sent. Called through `host.api` with D's string ids, as Comic's `discard` does. Staged files older than an hour are also dropped when another file is staged; there is no timer. B: "Unknown request." (browser staged values need no discard) | `{ids:[up to 64 ids]}` | `{ok:true}` |
| `GET /api/files/{id}/{i}` | D | Behind `host.file(id, i, dir)` on D. | `?dir=in` or `?dir=out` | bytes; the host resolves `{bytes}` |
| `GET /api/file` | B | Behind `host.file(id, i, dir)` on B. | `?id=&i=&dir=` | `{name, size, image, bytes}` |
| `GET /events` | D | Behind `host.listen` on D: server-sent events `change`, `typing`, `presence` (seq; the latter two become hinted `change` in the host) and `restart` (only when the daemon switches to an updated program). B's `host.listen` uses the engine; `/events` there gives "Unknown request." | — | event stream |
| `GET /api/drive/service` | B | Behind `host.drive` on B: returns the engine's Drive provider object (functions), not JSON. D: `404 page not found` | — | provider object |

## Not through host.api

A bound `host.api` reaches only the workspace's own page API. These routes
answer 404 on D and "Unknown request." on B; use the host method instead. The
setup page's `/api/setup*` routes are not a skin surface.

Workspace routes (`internal/ui/workspaces.go`):

- `GET /api/workspaces`: read by the host at boot; use
  `host.workspaces.list()`.
- `GET /api/workspaces/all`: `host.workspaces.disconnected()`.
- `POST /api/workspaces/join`: `host.workspaces.join({name, invite, agent,
  id?})`. Its errors carry `.status`, and `.retryID` when the join can be
  retried.
- `POST /api/workspaces/rename`: `host.workspaces.rename(id, name)`.
- `POST /api/workspaces/disconnect`: `host.workspaces.disconnect(id)`.
- `POST /api/workspaces/reconnect`: `host.workspaces.reconnect(id)`.

App routes (`cmd/agentnet/appupdate.go`), served only by the desktop app's
runner. Android answers the first three methods through its own bridge.

- `GET /api/app/status`: `host.appStatus()`.
- `GET /api/app/check`: `host.appCheckUpdate()`.
- `POST /api/app/update`: `host.appUpdate()`.
- `POST /api/app/cli`: `host.appReplaceCommand()`. Android rejects it.

## Request states

Three sources describe where a request stands. Show `actions` as given; never
derive decisions from a state word.

**Executor status** (`exec.state` on a request this device sent). This is the
executing device's own word. For a conversation request that this device's
own agent executes, `exec.state` is the local job state (next table).
`exec.stale` is true when that device is not connected now or is suspended
until it updates, or when a run is older than the maximum age. Treat a stale
state as unknown, not as current. A review notice's `report.items[].state` is
different: it uses the host's local state names (next table), with `blocker`
and `attempt`, and is what `/api/operator/decide` echoes as `expect`.

| State | Meaning |
|---|---|
| `queued` | Queued for the agent there, or waiting there until it may run. |
| `awaiting` | Waits for the host person's accept (a task or a guest's question) or approval (a held question). |
| `running` | Running. `detail` says "Stopping; waiting for the process to exit" while a cancel is pending. |
| `steered` | Accepted into the current native session run. Completion is not proven. |
| `answered` | The agent replied. |
| `needs_human` | The agent asked for a person there. |
| `resolved` | A person there closed it without a reply. |
| `failed` | The run failed there. |
| `interrupted` | The program stopped while it ran. |
| `cancelled` | Stopped there. |
| `not_run` | It never ran there. |
| `stopped` | It ran, but its reply was held back and not sent. |
| `declined` | The host person declined it. |
| `done`, `timeout`, `proposal` | On D, once the host's terminal answer or result arrived: its status (`proposal`: an offer, not run). |
| `unavailable` | The host was permanently removed from this workspace. Always stale. |

**Local state of a received request** (`state` of an incoming `Message` or
`DMMessage`). `actions` come from the state and kind.

| State | Meaning | Typical `actions` |
|---|---|---|
| `pending`, `accepted` | Eligible for the responder (an approved sender, or accepted). | — |
| `held` | A question from a sender not approved for automatic answers. | `reply, accept, approve, decline` |
| `awaiting` | A task, or a guest's question, waiting for this person's accept. | device thread: `accept, accept_always, reply, decline`; conversation: `accept, decline` |
| `conv_held` | A question or task held for the person in a conversation. Nothing runs it. | none on the message; answered in the chat; `resolve` on its `held` item closes it |
| `part_waiting` | For your agent, not started yet. It runs only while a responder is chosen and the invitation allows it. | — |
| `running` | The responder is working on it. | `cancel` |
| `cancel_requested` | Stopping. | — |
| `steered` | Accepted into a running native session. | — |
| `answered` | The responder replied. | — |
| `manual` | Replied by hand. | — |
| `needs_human` | The responder asked for a person. | device thread: `reply, accept, resolve`; conversation: `accept, resolve`; plus `continue` when `continuation` is set |
| `interrupted` | The program stopped while it ran. | device thread: `accept, reply, resolve`; conversation: `accept, resolve` |
| `failed`, `cancelled` | The run failed, or was stopped. | device thread: `accept, reply`; conversation: `accept` |
| `declined`, `resolved`, `summarized` | Closed: declined, closed by you, or a follow-up summary stored here. | — |
| `not_run`, `not_delivered` | Never ran here (its participation ended, or the agent is unavailable); or it ran but the reply was kept here. | — |

**Why an item waits.** `ConvItem.reason` in `needs_you` and `held`:

- `agent_awaiting`: an accept is needed.
- `agent_needs_human`: the agent asked for you.
- `agent_interrupted`: the run was interrupted.
- `agent_running`: the request is running. It is stoppable when `actions` has
  `cancel`, and it is never an approval.
- `agent_invite`: an invitation waits. With `role:"human"` it invites you
  as a guest: decide it with `/api/dm/guest/decide`. Otherwise it is for
  your agent: `/api/dm/agent/decide`.
- `person_turn`: a held turn, answered in the chat.

`decide_on` names another host where the item is decided. The item is then
read-only here, except that an item with `continuation` can be continued or
resolved through `/api/operator/decide`; `stale` is set when that host has no
current word. `ReviewItem.reason` is absent for most items. On D it may be
`held`, `agent_running`, `self_consented` or `device_admin`; B sets only
`device_admin`. `self_consented` and `device_admin` are notices, dismissed
with `resolve`.

**Comic's agent status (derived, not API values).** These come from
`features/Approvals.words.ts` `phaseOf` and the `Mood` of Comic's
`AgentAvatar`.

- **Phase**, the first rule that matches, top to bottom:
  1. `needs_human` when `continuation` is set.
  2. `running` when `actions` has `cancel`.
  3. `stopped` for a request whose state (a received one's own `state`; a
     sent one's `exec.state`, unless stale) is interrupted, failed,
     cancelled, stopped, timeout or not_run.
  4. none when `actions` is empty.
  5. For anything but a request (a report, or a follow-up that needs a
     person): `closing` when `actions` has `resolve`, otherwise none.
  6. `stopped` when the state (a received one's `state`; a sent one's
     `exec.state`, stale or not) is interrupted, failed or cancelled.
  7. `needs_human` when `actions` has `resolve`, or the state is
     `needs_human`.
  8. `decide` otherwise.
- **Mood:** each view picks its own, from what it shows; the order of
  precedence differs per view (the room panel checks `asleep` before
  `working`).
  - `working` when a thread has `running > 0`, or an agent's message or a
    participant has `running` as its state or `exec.state`.
  - `waiting` when something waits for a person: the `decide` and
    `needs_human` phases, `review` or `decide` counts, an invitation, an
    overdue reminder, a responder that cannot start.
  - `asleep` when it cannot answer now: its device is not connected
    (`directory` presence), its responder is not ready or it is turned off,
    or it has not joined the conversation.
  - `done` only for a settled good state: a ready responder, the chosen
    harness, an empty approvals list (and as decoration on About).
  - `neutral` otherwise, finished requests included.

**Other state words.**

- Delivery of a sent copy (`state` of an outgoing message, `copies[].state`):
  - `queued`
  - `waiting`: kept here because that device cannot read it yet, or
    because the server needs an update or cannot be reached (`detail` says
    which).
  - `receiver_waiting`: kept here until the selected reply receiver
    accepts this exact request.
  - `custody`: on the relay.
  - `delivered`
  - `expired`
  - `failed`
  - `not_delivered`
  - `quarantined`

  `delivered` is not "read", and `answered` is not "correct".
- Participation (`AgentView.state` and `GuestView.state`): `pending`,
  `invited`, `active`, `declined`, `conflict` or `dismissed`.
- Topic (`ThreadSummary.state`): `active`, `done` or `archived`.
