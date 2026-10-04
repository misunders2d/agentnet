# Topics: an agent's separate conversations

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
its topics are inside it. DMs and groups are not topics.

Real use is many short topics (one agent: about 10 a day, half with one
message, most over in minutes), so the design has to stay fast and tidy at
thousands of topics.

## Lifecycle: active → done → archived

- **Done by the agent:** the topic's latest request (question or task) got
  its final answer or result with status `done`, that reply is the topic's
  last message, and nothing in the topic is pending. Its conclusion is the
  first line of that reply, always shown labelled as the agent's words.
- **Answered by the person:** on the device that answered, a final reply
  the person wrote by hand (`Reply` takes the request over: state `manual`;
  in the browser device, which runs no agent, every reply) makes the topic
  done by the person (`done_by` you), and the reply is shown as *their*
  answer, never the agent's. **Known limit:** the device that asked cannot
  tell: a v1 reply does not say whether a person or the agent wrote it, so
  there it reads as the agent's conclusion.
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
  Reopen); the agent's next final answer can make it done again. A mark
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

A name, Mark done and Reopen are stored **locally on this device**, like
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

## Testing a world with old history

A binary built with `-tags agentnet_testclock` reads a clock shift (seconds)
from the file `AGENTNET_TEST_CLOCK_FILE` names, for the times it stores on
device-thread messages and topic marks and for deriving states; envelopes
and Hub requests keep real time. Released binaries have no such seam.
`internal/ui/testdata/topics_seed.sh` uses it to seed a company world with
165 topics, 70 of them ten days old, through real sends and real answers.
