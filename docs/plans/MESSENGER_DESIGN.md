# Messenger design: "Pop Huddle"

Status: chosen 2026-10-03 for the default interface (internal/ui/web). Three
directions were mocked up (A "Lush & alive", B "Playful bold", C "Cinematic
dark") and judged on clarity and delight; both judges ranked B first. This
document is the design lead's contract for building B at a lighter work
weight, with grafts from A and C. The owner may still redirect it.

Product rules that outrank this document: AGENTS.md (authority, receipts,
offline, no polling) and docs/UI_SKINS.md (host API v1).

**Recommendation:** build **B "Pop Huddle"** at a lighter "work weight", with grafts from A and C. Both judges ranked B first: clarity 8 vs 7 vs 6, delight 8 vs 7.5 vs 6.5. It has the clearest labels, the most distinctive look and the best measured contrast. Its weaknesses are fixable: it looks heavy, some targets are under 44px, and the approval card leaks setup details.

**What I checked:** I opened B m2, m3, m4 and d1, A m4 and C m2-light. In B I confirmed these slips: "Where: Warehouse server" on m4, "Not now" touching the card border, reaction stickers overlapping the card edge, and a 9:41 status bar next to 10:33 timestamps. I did not open C m4, so "Always allow is wider than Decline" is the clarity judge's claim, not mine. Dark-mode hex values below are proposals and have not been contrast-measured.

## Grafts
- **From A:**
  - The approval card's boundary line: "Holds stock only. No shipping, no invoice."
  - "Where" names a physical place.
  - The working card's live step list.
  - The agent's lead-in naming who decides: "Your call, Vitalii."
- **From C:**
  - Quiet "Asked Zen" / "Task for Zen" labels under sent messages.
  - A "See what Zen saw" link on the join divider.
  - The exposure strip with a legend: Earlier: private / Shared / Since joining.
  - "Nothing earlier" in the invite preview.
  - An Effect row saying whether the action can be undone: "Release anytime."
  - Calm dark-theme discipline.
- **Gaps all three left open:**
  - a "left" divider in the history
  - Bring in reachable from the composer "+" menu
  - every target at least 44px
  - a confirm step for Always allow
  - a way to bring a dismissed guest back
  - plain footer copy on the approval card

## Design contract

**Adjectives, each tied to behavior**
1. **Warm.** Agents have faces and names and speak in the first person. Sheets use plain sentences. There are no system codes.
2. **Explicit.** Every state is written in words as well as color: GUEST, Asked or Task, "saw 10 messages", "Waiting for Vitalii's OK". Nothing relies on color alone.
3. **Accountable.** Every action shows who decides, what it changes, what it won't do and how to reverse it. The UI never claims more than a receipt proves.

**Typography**
- **Fonts:** Rubik 700/800 is display only: screen titles, chat name, card headline, big numbers. Onest 400–700 is everything else. Both are OFL, bundled as variable woff2 subsets (Latin, Latin Extended, Cyrillic, Cyrillic Extended) in `static/m/fonts/`, with `system-ui` as fallback. The mockups used Bricolage Grotesque and Figtree, which have no Cyrillic: Russian and Ukrainian text fell back to a mismatched serif, so they were replaced. Never load fonts from a CDN at runtime, because offline is normal.
- **Size scale (px):** 12 (timestamps only), 13 (meta and hints, the minimum for meaningful text), 15 (desktop body), 16 (mobile body, line-height 1.45), 18 (chat name), 22 (card headline), 28 (screen title), 34 (big numbers).
- Times and counts use tabular numbers.

**Color tokens (light / dark)**

| Role | Light | Dark |
|---|---|---|
| canvas | #FFF8EC | #15111E |
| surface | #FFFFFF | #1F1A2B |
| text / outline | #1B1530 | #F5F0FF / outline #5A5075 |
| secondary text | #463F5E | #CFC6E3 |
| muted / meta | #625B7A | #A69DBD |
| bubble-mine (text) | #FFF0B8 (ink) | #3B3317 (#FFF4CF) |
| bubble-theirs | #FFFFFF | #241E33 |
| bubble-agent | #EEEAFF | #2B2452 |
| agent accent text | #4F33C4 | #B8A8FF |
| agent selected fill | #A897FF + ink text | #5B47C9 + #FFFFFF |
| act / approval (yellow) | #FFD43B + ink text (12.3:1) | same |
| approval text on canvas | #7A5200 | #FFD86B |
| guest ring/tag | #E8603C | #FF8A65 |
| guest text / surface | #A8361A / #FFE6DC | #FFB49C / #3A2019 |
| success text / surface | #0B6E46 / #DDF5E8 | #6FD9A3 / #11301F |
| online dot | #1FA463 | #34C77B |
| danger (cancel, errors) | #B42318 | #FF8A80 |

- **Color rules:**
  - Saturated yellow appears only on things you can act on, plus the "Needs your OK" band. Butter yellow is used only for your own bubbles.
  - Coral means guest, and only guest.
  - Each agent gets one of 8 fixed hues, picked from its key, and its name is always shown as text.
- Light mode is the default and follows the system setting.

**Shape, radius, elevation**
- **Outlines:** 1.5px ink on mobile, 1px on desktop. Dark mode uses the #5A5075 outline.
- **Hard shadows:** a 3px/3px ink offset shadow only on primary buttons, the approval card and open sheets. Everything else is flat.
- **Corners:**
  - people's bubbles 20px, with a 6px tail corner
  - agent bubbles 12px (caption boxes)
  - cards 16px
  - chips fully round
  - sheets 24px at the top
- **Background:** the dot texture is at most 4% opacity and off in Focus density.
- Reaction stickers stay inside their card. Text links keep at least 12px from any border.

**Avatars**
- **People:** circle, using a photo, or illustrated initials when there is none.
- **Presence:** a 12px dot with a 2px canvas ring. Online is a filled green dot; offline is a hollow grey ring, so shape carries the meaning as well as color.
- **Agents:**
  - rounded square (30% corner radius)
  - a robot head with one prop
  - a device badge (laptop, server or cloud) at 32px and up
  - an "owner's agent · device" subtitle
- **Faces by size:** full face and prop at 32px and up; eyes only at 20–31px; below 20px a colored square with the initial.
- **Moods:** only neutral, working (eyes scanning), done (smile) and waiting (eyes up). No frowning or angry faces.
- **Asleep:** eyes closed, 40% desaturated, "asleep · ZenBook offline". Messages to it show "Zen gets this when ZenBook wakes".
- **Guests:** a 2px dashed coral ring plus a "GUEST" tag on the name line, everywhere they appear.

**Motion**

| What | Timing | Easing |
|---|---|---|
| Press | 120ms | — |
| Toggle or chip | 200ms | cubic-bezier(.2,.8,.2,1) |
| Sheet open | 280ms | cubic-bezier(.2,.8,.2,1) |
| Guest joins (pop) | 320ms | cubic-bezier(.34,1.56,.64,1), at most one overshoot |
| Reaction | 180ms | — |
| Working dots | 1.2s loop | — |
| Agent blink | every ~4s | — |
| Approval arrow | 3 loops, then stops | — |

- No confetti.
- Loops pause when the tab is hidden.
- **Reduced motion:** no scaling, sliding or looping. Use opacity fades of 120ms or less. Working state is the static "Working…" text plus the step list.

**Mobile layout**
- **Bottom navigation:** Chats, Agents, OKs (with a badge).
- **Header:** at most 64px. Back with unread count, avatar, name, presence line, a 44px Bring-in icon and overflow.
- **Guest bar:** 52px under the header, showing name, GUEST and "since 10:33", with a labeled **Dismiss** button. With 2 or more guests it collapses to "2 guests · Zen, Bohdan · Manage", which opens a sheet.
- **Composer:** one row of +, text field and send, all 44–48px.
  - The "+" menu's first item is "Bring someone in".
  - The Answer | Do it row appears only after you @mention an agent.
- **Sheets:** at most 85% of the screen height, with a drag handle, so part of the chat stays visible.
- **Gestures:**
  - swipe right on a message to reply
  - long-press for React, Reply, Make this a task, Select, Copy
  - swipe from the edge to go back
  - no destructive action is ever available by gesture alone
- When you open a chat from an approval, it scrolls to the request that started it, not to the bottom.

**Desktop layout**
- **Width 1280px and up:** a 72px rail (Chats, Agents, OKs, Settings), a 360px list, the conversation (lines no wider than 720px) and a 360px "In this chat" panel. The panel opens automatically when there are guests or a pending OK.
- **1024–1279px:** the panel becomes an overlay drawer.
- **Below 1024px:** use the mobile layout.
- **Guest controls:** header guest chips are a summary only. Dismiss, "What X saw" and the exposure strip live in the panel, which removes the 24px × buttons.
- **Shortcuts:** Ctrl K searches; Alt D switches Answer and Do it.

## Topics (an agent's separate conversations)
Owner decisions and the API: docs/plans/TOPICS.md. One agent is one row in
the chat list; each of its topics is a separate reply chain.
- **Bar** under the header: at most 6 chips (`TOPICS.barMax`), as many as
  fit: what needs you, then unread, then the open topic, then the most recent
  active; the open topic always keeps its chip. The last chip is **All
  topics (N)**, with a red count (a dot on phones) when topics not shown have
  unread messages. No scrollbar: chips share the width and truncate (title in
  full on hover); phones fit one topic chip. "+ New topic" sits at the end (an
  icon with a label for screen readers on phones).
- **State in words:** Needs you (yellow), Waiting (agent violet), Done (green),
  Archived (muted), each with an icon. Bar chips show the icon with the word
  for screen readers and on hover; lists show the word.
- **The open chip is the topic menu** (ink chip with a chevron): Rename…,
  Mark done or Reopen, All topics. Rename opens a sheet that says the name is
  kept on this device only, with "Use its first message" to undo.
- **All topics:** desktop a 440px side panel from the right edge, phone a
  full-screen sheet with Back. Search field, then Active / Done / Archived
  as three equal pill buttons with counts, then rows: title, time, state,
  the last line (or "Agent: <conclusion>" when the agent finished it),
  unread count; Show more pages on. Footer: "Names you give topics and Done
  marks are kept on this device only."
- **End of a done topic:** a card after the last message: Done, who finished
  it, "<Agent>'s conclusion: …" in the agent's words, and Reopen. An archived
  topic says Archived, how long it has been quiet, and that nothing was
  deleted and a new message makes it active again.
- **Chat search** lists matching topics (archived too) under "Topics", each
  opening that topic.

## Invite → guest → dismiss
1. **Open the sheet** from "+" → "Bring someone in", the header icon, or the panel. The sheet says: "They join as a guest and see only what you share. Anyone here can dismiss them."
2. **Pick who.** Search people and agents. A person is preselected only for a stated, local reason, such as "mentioned by Vitalii". There is no suggestion engine.
3. **Choose what they can see** ("What can Bohdan see?"): **Recent messages − 10 +** (the default), or **Selected messages**, which come from long-press → Select.
4. **Preview.** "BOHDAN WILL SEE" lists the real lines with their authors, collapses after 4 ("+6 more") and ends "Nothing earlier."
5. **Optional "Why?"**, then a 52px **Bring Bohdan in** button.
6. **History divider:** "Bohdan joined to help · saw 10 messages · invited by you · See what Bohdan saw". The link opens a read-only sheet of exactly those messages.
7. **Dismiss takes effect immediately.**
   - The divider reads "Bohdan left · dismissed by Vitalii · 10:52".
   - A snackbar offers **Bring back**, which reopens the invite sheet with "since they left" filled in.
   - Copy under Dismiss: "Stops new messages. What was already shared stays with them."
   - When an agent leaves on its own: "Zen left when done".
8. **Past guests** are listed in the panel: "Bezos · Mon · saw 3 · dismissed by Vitalii".

## Ask vs task, and approval
- **Default:** a plain message. There is no picker.
- **Choosing Answer or Do it:**
  - @mentioning an agent shows **"Zen should [Answer | Do it]"**, with Answer selected.
  - Do it turns send into a labeled **⚡ Do it** pill.
  - The hint reads "Anything an owner must OK will wait for them."
- **Sent label:** "Asked Zen" or "Task for Zen", followed by states the protocol can prove: Delivered → Accepted → Working (step list) → Done / Declined / Needs a person / Expired.
  - Never show "Seen" or "done" from transport alone.
  - If an answer needs an action the agent may not take, it shows **Needs a person**, not a guess.
- **One-tap result buttons** ("⚡ Ask Stocky to hold 400") create a task through the same path. Tapping one never approves anything.
- **Approval card:** a system card, never a bubble. It appears only in the owner's own app.
  - The pill "Stocky is your agent, so this waits for you" sits first.
  - Then the agent's lead-in ("Your call, Vitalii"), then the card itself.
  - Header: Zen → Stocky with faces and owners.
  - Headline as a sentence: "Zen asks Stocky to hold 400 cases for Savannah".
  - Rows: What, Where (a physical place), For, Effect ("Release anytime").
  - The green boundary line.
  - Buttons, stacked and full width:
    1. **Allow once** (yellow, 52px)
    2. **Decline** (outlined, 48px)
    3. **Always allow Zen → Stocky…**, a 44px text button that is never more prominent than Decline
  - Always allow opens a confirm: "Zen can give Stocky any task without asking you. Ends if Zen's key changes. Turn off in Settings → Permissions." with [Always allow] and [Just once].
  - Footer: "Only you can approve this. Typing in chat can't approve it."
- **Requester's view:** a compact card, "Waiting for Vitalii's OK · Delivered 10:38", with **Remind** and **Cancel request**.
- **Outcome divider:** "Vitalii allowed once · 10:40".
- **Home banner:** always names who and what: "Zen wants Stocky to hold 400 cases · Review".

## Anti-goals
- **Visual style:**
  - No glass, blur, aurora gradients or violet→blue "AI" gradient on your own bubbles.
  - No icon wallpaper, no confetti, no 2–3px outlines on everything.
  - Not dark by default.
- **Meaning and size:**
  - No color-only meaning.
  - No tap targets under 44px.
  - No faces on mascots below 20px, and no frowning agents.
- **Interaction:**
  - No Message/Question/Task radio buttons.
  - No destructive action by gesture alone.
- **Approval:**
  - No approval through chat text, and no approval drawn as a bubble.
  - Always allow is never more prominent than Decline.
  - "Where" never shows a machine name.
- **Receipts:** no "Seen" or "Done" beyond what a receipt proves.
- **Fonts:** no fonts loaded at runtime from a CDN.
- **B's polish slips to fix:** status bar time, "Not now" touching the border, reactions overlapping the card edge, and "saw 10 messages" repeated on both the guest bar and the divider.