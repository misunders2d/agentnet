# Topics in every chat

Status: owner decisions of 2026-10-04 (final), built in the client
(`internal/client/topics.go`), the page API (`internal/ui/livetopics.go`), the
browser device (`internal/ui/static/engine.mjs`) and the messenger
(`internal/ui/web/src/features/Conversation.topics.tsx`,
`Conversation.alltopics.tsx`, `ChatList.topics.tsx`).

## What a topic is

A topic is one device thread with one agent: messages with that peer joined
by reply links, rebuilt from the messages on every read. Its id is its
earliest message; its automatic title is that message's first line. Each
topic is a separate reply chain, so the agent answers it in a fresh session.
**Topics stay per agent** (owner): one agent is one row in the chat list,
its topics are inside it. People DMs and groups have an optional set of topics
inside their chat and keep a main flow for messages without a topic.

Real use is many short topics (one agent: about 10 a day, half with one
message, most over in minutes), so the design has to stay fast and tidy at
thousands of topics.

## People chats: main flow and optional topics

New topic makes the next message start a separate flow. Make a topic on a
main-flow message promotes that message and its reply descendants. Opening a
topic filters its messages and sends follow-ups into that topic; Main flow
returns to quick messages. Quotes remain separate from reply links.

An optional signed, encrypted `topic` reference names the flow. Promotions
and shared Done/Reopen are ordinary human messages with `topic_event`
`{action: create|done|open, seen?: [logical ids]}`. Done covers the exact held
turns; an unseen turn reopens the topic even if its signed time is older.
Actions name the previous held action to preserve causal order; concurrent
branches use stable signed time and logical-id ordering. Everyone sees who
marked it done; any participant may reopen it. Creation and shared actions
reset the quiet interval. An agent asked in a topic replies in it, and only
its explicit `topic: done` trailer closes it.

Names and manual archive stay local. A new shared action or topic message
ends a local archive. Delete for me erases only that topic for this person's
devices through the existing erasure mechanism; other people keep copies.
Device-chat deletion and Done/Reopen remain local to this device.

## Lifecycle: active → done → archived

- **Done by the agent:** the last message is a successful answer or result
  carrying `topic_done: true`, and nothing in the topic is pending. The
  worker sets this only from an explicit final `topic: done` line on a
  nonempty successful reply. That line and an optional `reaction:` line
  may appear in either order. Its conclusion is the reply's first line,
  labelled as the agent's words. An ordinary final answer keeps the topic active.
- **Answered by hand:** a person's reply keeps the topic active. It never
  becomes an agent conclusion and never implies a local Mark done.
- **Done by the person:** "Mark done". "Reopen" makes a done (or archived)
  topic active again.
- **Not done:** a failed result, a result that needs a person, a progress
  update, a hand-written answer without status, or a plain message after the
  final answer.
- **Archived (derived):** quiet for `TopicArchiveAfter` (7 days) with nothing
  pending: never while anything waits for the person, the agent or the
  peer. Quiet means no message, and no Mark done / Reopen, in that time
  (`quiet_since`; an archived topic says "Quiet since" that date).
  Archived topics leave the overview and the bar; they are listed under All
  topics → Archived and open normally, saying they are archived. **Nothing
  is ever deleted by archiving.**
- **Any new message makes a topic active again** (it ends any Mark done or
  Reopen); the agent's next explicit close can make it done again. A mark
  covers only the messages the page showed when the person chose it
  (`count`): one that arrived meanwhile keeps the topic active, and the
  page says so.
- **Pending** means: a received request held, awaiting, needing a person or
  being run here; a sent question or task without a reply (a request the
  Hub refused, or that expired, waits on nobody); an open review notice.

## Rename

The person can rename a topic; the default title stays automatic (the first
line of its first message). Search finds both names.

## Where the person's changes live

For agent device chats, a name, Mark done and Reopen are stored **locally on this device**, like
read marks (`topic_state` in the client's SQLite, `kv` `topic/…` rows in the
browser device's IndexedDB). There is no cross-device sync in this release:
another device of the same person shows the automatic title and the derived
state. Derived states (done by the agent, archived) are the same on every
device that holds the same messages, because they come from the messages.
Deleting a topic (this device only) also forgets what was set on it.

## The bar and All topics

- The bar under the conversation header shows at most `TOPICS.barMax` (6)
  chips: topics that need the person first, then unread ones, then the open
  topic, then the most recent active ones; the last chip is
  **All topics (N)**. As many chips as fit are shown (phones fit fewer);
  chips truncate and the rest are under All topics, never in a scrollbar.
  The open topic's chip is its menu: Rename…, Mark done or Reopen, All
  topics. New topic stays beside the bar. On a phone the last chip reads
  **All N** so a second topic chip fits. When topics that need the person
  are not in the bar, the All topics chip carries a "needs you" mark (and
  says how many in its label); unread messages in other topics give it a
  red mark.
- **All topics (N)**: a searchable list (title, last line or the agent's
  conclusion, time, state, unread) with Active / Done / Archived filters,
  paged from the server; it opens a topic on click. Desktop: a side panel;
  phone: a full-screen sheet.
- The chat list's search also finds topics by name or last line, archived
  ones too, through the same paged route; "Show more topics" pages on.

All topics in Comic, Classic and Zoom offers checkboxes on desktop and phone.
Select several topics, then Delete for me, Mark done or Archive. One
confirmation starts a six-second Undo interval; no mutation is sent until
that interval ends. Undo or closing the view cancels the pending action.
Bulk Done carries each selected topic's displayed count, keeping messages
that arrived during Undo active. In people chats it emits one shared event
per topic. Each topic
retains its normal archive/deletion scope. A partial failure reports how
many topics changed and leaves the remaining topics unchanged.

## Changing it

"This should all be easily changed, if needed — not via settings, but with
code" (owner). Every tunable is one named, commented constant block per
language, and there is no user setting:

- Go: `internal/client/topics.go` (`TopicArchiveAfter`, `TopicPageDefault`,
  `TopicPageMax`, `TopicTitleMax`, and the store's read sizes).
- Browser device: `TOPICS` in `internal/ui/static/engine.mjs`, which must
  equal the Go block; `TestBrowserTopicsMatchGo` fails until it does.
- Screens: `TOPICS` in `internal/ui/web/src/model.ts` (bar size, chip
  widths, page sizes, search delay). Its `titleMax` and `pageMax` repeat the
  server's limits; `TestMessengerTopicLimitsMatchGo` fails until they match.

The derivation is one function on each side (`deriveTopic` in Go and in
engine.mjs), held to the same vectors
(`internal/client/testdata/topic_vectors.json`).

## API

- Overview: `GET /api/overview?topics=1` (the messenger) lists every
  device thread except archived topics; plain `GET /api/overview` (the
  previous interface, installed skins: pages that know nothing of topics)
  lists every thread, archived ones too, so none becomes unreachable there.
  Both carry `topics[]`: each peer's `total`, `archived`, `archived_unread`
  and `latest` topic (so an agent whose topics are all archived keeps its
  row); `topic_list` says the routes below are served. Each `ThreadSummary`
  has `state` (active, done, archived), `done_by` (agent, you),
  `conclusion`, `concluded_by`, `pending`, `renamed`, `auto_title` and
  `quiet_since`.
- `GET /api/thread` adds `topic`: the open thread as a topic, archived or not.
- `GET /api/topics?peer=&state=&q=&before=&limit=`: one page, most recently
  active first; `next` (`<seconds>|<peer>|<id>`, opaque to pages) is the
  `before` of the next page; `matched` counts every page. Review-notice
  threads are never listed.
- `POST /api/topic/rename` `{peer, id, title}` (empty or blank title:
  automatic again), `/api/topic/done` and `/api/topic/reopen`
  `{peer, id, count}` (`count`: the messages the page showed; omitted, all):
  the same guarded action path as every POST (cookie, same origin, JSON);
  `id` is the topic's id.

People-chat additions use the same routes and tunables:

- `/api/dm` adds `topics[]`; each message carries its derived `topic` and
  optional `topic_event`. A people-chat summary has `conv` and can have
  `done_by: person`, with `concluded_by` naming the signed action's device.
- `/api/topics?conv=...` lists that DM/group's topics, with the same filters,
  search and paging. Cursors remain opaque and scoped to the conversation.
- `/api/dm/send` and `/api/dm/agent/ask` accept optional `topic` (`new` starts
  a flow); an ordinary reply inherits its parent's topic.
- `/api/topic/create` `{conv,id}` promotes a held logical message; other
  changes accept `{conv,id,count?,title?}`. Bulk Done/Archive/Delete accepts
  `{conv|peer,id:"",ids:[...],counts?:{id:shownCount}}` (up to the existing page maximum).

Storage appends `topic` and `topic_event` inbox/outbox columns and reuses
`topic_done` and `topic_state`; signed history and selected-content hashes
carry the optional fields. No backfill, service or dependency is added.

## Testing a world with old history

A binary built with `-tags agentnet_testclock` reads a clock shift (seconds)
from the file `AGENTNET_TEST_CLOCK_FILE` names, for the times it stores on
device-thread messages and topic marks and for deriving states; envelopes
and Hub requests keep real time. Released binaries have no such seam.
`internal/ui/testdata/topics_seed.sh` uses it to seed a company world with
165 topics, 70 of them ten days old, through real sends and real answers.
