# Shared people and agent tags — MEL-502

Owner decision, October 9, 2026: a named tag identifies an explicit set of
people, agents, or both, and everyone in the workspace can use it. This
supersedes the earlier undecided personal-versus-shared list question.

## Behavior

- The mention picker offers `@everyone` for eligible participants already in
  the current chat, excluding the sending human. Shared named lists appear
  when they have eligible recipients there.
- Picking a tag expands it into ordinary visible person/agent mentions before
  Send. Overlapping tags do not repeat mentions. Editing a shared list later
  cannot rewrite an expanded draft or an already sent request.
- People receive ordinary attention references. Agent targets retain their
  exact participation IDs and existing concurrent dispatch, per-target
  permissions, attachments, cancellation, and failed-target retry behavior.
- A tag never admits a participant, shares old context, grants permission,
  or executes received mention text. People or agents outside the chat are
  counted as excluded; inviting them remains a separate explicit action.
- A creator manages the list without automatically becoming a recipient.
  Managers select and remove recipients; anyone may use the shared tag.
  Existing management transfer and last-manager rules remain in effect.

Comic exposes editing in Settings → People lists. Classic and Zoom expose it
in People. All three use the existing signed directory and message routes.

## Compatibility and identity

New explicit lists use version 2 of the existing signed team journal. Person
targets remain person IDs. An agent target is the tuple of agent ID, host
address, and host fingerprint; matching a display name or changed host key
never selects a replacement. Version 2 has explicit `add`, `remove`,
`agent-add`, and `agent-remove` operations, with managers distinct from
recipients. Self-join and self-leave remain version-1 operations only.

The existing signing domain is retained; the signed version distinguishes the
semantics. Optional agent fields do not change version-1 canonical bytes.
Go/browser canonical vectors and authority checks cover both versions.
The journal and existing materialized JSON state need no schema migration.

`GET /v1/teams?version=2` and the stream's `teams=2` parameter opt into the new
directory, marked `version: 2`. Old clients keep receiving version-1 lists;
the relay advertises `teams2` alongside `teams1`. An older relay can still
serve ordinary people-list mentions and `@everyone`, but creating mixed lists
requires upgrading it. Existing version-1 lists are not silently converted.

## Verification boundary

Focused tests cover signed authority, exact targets, version compatibility,
push delivery, no grant/message side effects, browser parity and persistence,
and headless desktop/mobile editing and composer dispatch in all three skins.
Full release/platform qualification belongs to the release coordinator. No
installed app, production relay, or real conversation is changed by this work.
