# The skin layer (Comic kit)

Comic is AgentNet's default skin and the base of every **overlay skin**: a
skin that keeps Comic's whole interface (every chat, topic, agent, approval,
setting and flow) and replaces only this folder. Because an overlay is built
from the AgentNet release it names, Comic's fixes and new features reach
every overlay skin on its next build; the overlay never forks Comic's code.
This includes the existing group header's optional shared-links sheet: overlays
inherit its member-only visibility, local drafts and stale-edit protection.
Standalone skins implement that optional field on the same Host API contract.


A skin that needs a different interface altogether is a **standalone skin**
instead: its own code on the [skin contract](../../../../../docs/UI_SKINS.md)
(examples: Notebook in `examples/skins/`, Holonet, Classic and Zoom in the
agentnet-skins collection).

## What an overlay may replace

Every file here is a slot. An overlay ships any of them; what it leaves out
stays Comic's.

| File | What it is | What it must keep |
|---|---|---|
| `theme.css` | Colour, font, corner, shadow and motion tokens for light and dark (`.an-root` and `.an-root[data-theme=dark]`), the Tailwind `@theme inline` names they map to, and any styles for the [parts](#parts) | Every token Comic defines (an overlay changes values, never removes names). Text at least 4.5:1 on its surface in both themes. Movement only outside `prefers-reduced-motion: reduce`. |
| `document.css` | `@font-face` rules (and only those), with URLs under `m/fonts/` | Each face's files in the overlay's `fonts/` folder, licensed for redistribution (OFL or similar). |
| `fonts/*.woff2` | The font files `document.css` names (copied to `m/fonts/`) | Absent: Comic's Onest and Rubik are used. |
| `brand.tsx` | `skin` (manifest `id`, `name`, Settings `blurb`, `storage` key prefix, `themeKey`), `words` (headings a skin may word its own way: today `chats`) and `Mark` (44px mark in Settings) | `id` equal to the manifest's. `storage` and `themeKey` start with the skin's own id (`pulp`, `pulp.theme`), never `agentnet.`, which holds the host's skin choice and trust (Comic, built in, keeps its historical `agentnet.messenger` and `agentnet.theme`). Actions keep AgentNet's words. |
| `Avatar.tsx` | `PersonAvatar`, `AgentAvatar` (with `mood`: neutral, working, waiting, done, asleep; and an optional `device` badge), `GroupAvatar`, `Presence` | The same exports and props. An agent is drawn from its stable seed so it always looks the same; moods stay recognisable; a person's own picture (`avatarPicture`) wins over a drawing; guests keep a distinct mark. Decorative only (`aria-hidden`). |
| `SendButton.tsx` | The composer's send control | A `type="submit"` button named by `label`, `aria-disabled` while not ready or busy, keeping focus in the field on a tap, `data-part="send"`. |
| `TopicIntro.tsx` | What shows above a topic's messages (Comic: nothing) | Receives `TopicFacts` (title, main, state, index, at, count). Never hides or delays the messages. |
| `RequestFrame.tsx` | The frame around a request that needs someone (`tone`: act, work, quiet; `label`; `at`) | Renders `children` (who asks, what, every action) reachable: at most one explicit "open" step, never dropped or reworded. `data-part="request"`. |

An overlay may add its own files beside these (a helper module, an SVG) and
import them from its slots. It may not edit anything outside this folder.

## Parts

Elements carry `data-part` names an overlay's `theme.css` may style. They are
a contract: AgentNet keeps them on the same elements, and
`TestSkinKitPartsDocumented` keeps this table equal to the code.

| Part | Element |
|---|---|
| `rail` | The desktop navigation rail |
| `tabs` | The phone tab bar |
| `list` | The chat list section |
| `list-title` | Its heading (`words.chats`) |
| `chat-row` | One chat in the list (`data-kind`: agent, group, person…; `data-unread` when unread) |
| `chat-title-line` | The row's first line: title, then time |
| `chat-title` | The chat's name |
| `chat-time` | When its last message arrived |
| `chat-preview` | The row's last-line preview |
| `unread` | The unread count |
| `conversation` | An open conversation |
| `conversation-head` | Its header |
| `agent-runtime` | The selected own agent's reported model and reasoning, below the header or above the composer |
| `topic-bar` | Its topics bar |
| `timeline` | The message log |
| `message` | One message (`data-from`: me, person, agent) |
| `bubble` | Its bubble |
| `typing` | Who is typing |
| `composer` | The message box |
| `composer-field` | Its text field's frame |
| `send` | The send control |
| `request` | A request card (`data-tone`: act, work, quiet) |

Prefer tokens over part styles, and part styles over new slots. Never hide a
control or text with a part style: restyle it. Write part styles outside any
`@layer`: unlayered rules win over Comic's Tailwind utilities, so no
`!important` is needed.

## Build an overlay

From an AgentNet checkout at the release the overlay names:

```sh
SKIN_OVERLAY=path/to/overlay SKIN_OUT=path/to/package SKIN_MANIFEST=path/to/skin.json \
SKIN_ID=pulp SKIN_NAME=Pulp SKIN_AGENTNET=v0.8.25 \
SKIN_LICENSES=path/to/LICENSES.txt SKIN_SUMS=path/to/SHA256SUMS \
sh internal/ui/web/build.sh
```

The package is complete (`skin.json`, `entry.mjs`, `style.css`,
`document.css`, `m/`) and installs like any other. The build is
deterministic: the same overlay on the same AgentNet commit gives the same
bytes. The agentnet-skins collection runs this for every overlay skin.
