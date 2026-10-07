# AgentNet v0.8.4 — October 7, 2026

[Download v0.8.4](https://github.com/misunders2d/agentnet/releases/tag/v0.8.4).
This release fixes chat navigation, mixed-version group sends, agent setup,
retry labels, notifications and desktop update recovery.

## Changes

- **One chat per person.** Main stays stable; earlier conversations and native
  topics appear together in All topics. Their original messages, drafts and
  participants stay attached to their exact topic.
- **Keep a group send moving.** Current members can receive a message while an
  older member’s encrypted copy waits for their update. Details name the waiting
  device and missing support; context and consent are preserved.
- **Set up installed agents together.** Claude, Codex, Pi and OMP use one setup
  flow for answering questions and accepted tasks, with their own settings and
  permissions. Native hooks appear separately in Details. Codex/Claude readiness
  no longer breaks on unrelated settings-file formatting.
- **Address the right group agent.** Configured names and exact recipients are
  shown clearly, including when a task comes from an approved agent proposal.
  Pi and OMP questions can ask another admitted group agent and wait for its
  answer; the exact selected local child can run while its parent waits.
  Claude and Codex keep their existing question restrictions.
- **Remind yourself about sent messages.** Remind me works on your own messages
  as well as received ones, with the same personal reminder editor. Saved
  reminders reopen the exact stored message.
- **Retry means retry.** Stopped, failed and interrupted requests to your own
  agent show their execution state and offer an explicit rerun. They do not ask
  for approval again or restart themselves. Held messages retain their checks
  and hidden contents without calling every refusal a failed identity check.
- **Mute each chat once.** Chats notify by default under your device’s global
  opt-in. A person’s mute covers Main and Topics; existing quiet choices stay
  quiet. Group alerts do not need a separate direct chat.
- **Repair desktop updates safely.** About exposes controls for an independently
  running daemon. The updater uses a stable restart directory and waits for
  accepted work before replacement. An older busy daemon may refuse preparation;
  retry after its current job finishes.
- **Everyday fixes.** Reminder date/time edits persist; Zoom retains draft text
  when a send route changes. Incomplete agent context is no longer shown as an
  invented invitation. Attachments have explicit downloads and bounded text
  previews; skin import uses the native folder picker.

## Upgrading

Keep your existing identity and data directory. Use **Settings → About → Update
AgentNet**. From v0.8.0, install the matching desktop package once. From
v0.8.1/v0.8.2, use About once: their older standalone commands retain the old
CLI-only updater until replaced. v0.8.3 introduced whole-app `agentnet update`
and automatic adoption of unchanged official commands; v0.8.4 preserves that
behavior and adds the desktop/daemon recovery above. Custom commands stay
protected. If an older attached app has no Update control, install the current
desktop package once, keeping the same identity and data. AgentNet does not
update Codex, Claude, Pi, other devices or the relay.

<details>
<summary><strong>Qualification and remaining limits</strong></summary>

Source: `ac084454e767b5f1f8096533b64c0c57b2d822b3`.
Published October 7, 2026 at 15:54 UTC. The [source CI](https://github.com/misunders2d/agentnet/actions/runs/37642753786) and [release workflow](https://github.com/misunders2d/agentnet/actions/runs/37645700324)
passed on `ac084454e767b5f1f8096533b64c0c57b2d822b3`. All 12 asset digests and
sizes matched GitHub, and all 11 package entries matched `SHA256SUMS`. The
downloaded Linux command reports v0.8.4 and that exact clean source revision.
Both isolated Linux update journeys passed with the final downloaded AppImage.
The relay was upgraded after a verified stopped-state backup, preserving its
existing volume and realm. Its public version and client recommendation both
report v0.8.4. No existing desktop or phone installation was updated.

Focused native regressions and actual embedded Comic/Classic/Zoom fixtures
passed at desktop and phone widths. These are synthetic rendering sizes, not
physical phone notification/clipboard qualification. Isolated Linux native
checks cover attachment downloads, folder selection, a genuine published
v0.8.3 helper/package upgrade and an independently managed daemon’s idle-safe
cutover, with accepted work completed once. The older published UI’s network
release-download/button journey and interactive Windows/macOS installation
remain unverified. Installers remain unsigned.

OMP qualification includes fresh own-settings question/task jobs and exact
native question-tool registration; a post-fix live model lookup was not rerun.
A separate local Pi stall came from a custom extension’s OAuth listener and was
repaired locally; this release makes no broad model-speed claim.

</details>

See [the handoff](HANDOFF.md) for final evidence and remaining work, and
[the changelog](../CHANGELOG.md) for release history. Projects and assigning
existing messages to an agent remain future work.
