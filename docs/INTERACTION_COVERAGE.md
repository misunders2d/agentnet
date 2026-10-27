# Next-release interaction coverage and responsiveness

Owner correction, October 26, 2026: "no need" to log in to Telegram;
"both slack and telegram are sub-0.5 second, I'll settle for that."
The acceptance target is now **under 500 ms from input to the intended local
visual change**, with real work continuing asynchronously and truthful pending,
confirmed and failure states. This supersedes the earlier 100 ms local-feedback
target and the requirement to measure matching Slack/Telegram accounts. No
comparative performance claim is made. Sergey also confirmed that no physical
Android test device is available; emulator evidence must remain labelled as such.
Every reachable interaction still needs executed evidence and error/recovery
coverage. Publication is blocked until the applicable gate is demonstrated.
The updater-only candidate
`3f9797f641a9707f3f945d35d6de6d5d810165de` is a baseline, not a qualified release.

Release authorization, October 26, 2026: Sergey explicitly authorized driving
the next fully responsive release, including generated assets, commit, push,
deployment and publication, without further approval requests. The earlier
runtime-artifact approval question is resolved. This authorization does not
waive the responsiveness, recovery, platform, skin or release verification gates.

## Hard gate: reuse the working messenger implementation

Owner correction, October 26, 2026: copy what already works in Telegram/Slack.
AgentNet supplies the new way to communicate with native harnesses; messenger
architecture and interaction behavior must come from inspected proven designs.
This is an implementation acceptance gate as well as a publication gate.

- Before changing a messenger flow, inspect its working counterpart. Record the
  pinned source revision and exact functions/flow, what existing code/components
  or pattern is reused, and the necessary AgentNet-specific adaptation. Existing
  applicable reference evidence can be reused. Closed-source counterparts need
  observed behavior and authoritative documentation with unverified parts named.
- Prefer reuse for local state, background operations, acknowledgements, retry,
  synchronization and bounded history loading. Similar styling or an invented
  architecture described as Telegram-like does not satisfy the requirement.
  A new messenger architecture requires evidence that reuse cannot meet the
  requirement and Sergey's explicit approval before implementation.
- Apply the intended local visual change on input, before the backend finishes.
  Sending immediately adds a pending message; reacting changes the reaction;
  dismissing immediately removes active attention; navigation immediately shows
  the chosen destination or its loading state. A spinner beside the unchanged
  old state does not satisfy this gate. Background work reconciles the result;
  failures explicitly identify the action and restore affected current state or
  provide retry. Local presentation never implies an execution permission or
  unproven remote completion.
- Execute rendered delayed/blocked/failing-backend cases. Prove the intended
  local effect happens while the request remains blocked and other controls
  still work. Verify failure/restoration/retry, duplicate input, workspace
  changes, newer synchronization and stale responses. Preserve drafts, files,
  exact targets and native authority. Keep the all-interaction timing
  requirements below; immediate presentation alone does not qualify the app.
- Missing reuse evidence, a backend-dependent local effect, frozen controls,
  silent failure, or failed recovery blocks acceptance and publication. Existing
  functionality tests, a pending indicator, or an implementation claim cannot
  waive this gate.

The gate is recorded; implementation compliance remains unverified. Root owns
the repair. Sergey needs to do nothing while owned fixtures are being tested.

## Acceptance

- Enumerate controls by screen, placement, action, meaningful state, role and
  capability, for Comic and every supported skin/platform. Include menus,
  nested menus, dialogs, links, tabs, inputs, toggles, pickers, file/media controls,
  context actions, navigation/back/cancel and native bridges. Record keyboard and
  touch variants. A shared handler is not evidence that every control was used.
- Reconcile source inventory with rendered discovery and actual executed cases.
  New or unmatched controls, missing states and missing cases remain open.
- Record input to first visible acknowledgement, local action commitment,
  confirmed visible result and relevant remote completion separately. Verify
  correctness and preserved input/focus as well as elapsed time. A spinner or
  optimistic success does not establish completion.
- Exercise normal and 10x history, cold and warm views, synchronization/catch-up,
  repeated input, offline/error/recovery and permission/capability boundaries.
  Record repetitions, distributions and worst observed delays; averages cannot
  conceal stalls.
- Verify the owner's under-500-ms local-feedback target on recorded hardware,
  data and network conditions. Live Slack/Telegram benchmarking is no longer
  required; do not present the owner's target as a measured comparison.
- Report every required case as passed, failed, blocked or unexecuted with its
  exact candidate/platform/fixture and evidence. Unsupported controls must be
  accounted for through their actual unavailable/disabled behavior. Missing
  execution or unresolved latency blocks publication.
- Preserve original identities, exact targets, native permissions, history,
  shared read/reminder generations and truthful transport state. Use owned
  fixtures for destructive actions; never dismiss the owner's real requests
  merely to collect timings.

## October 26 local interaction candidate

### October 27 continuation: the 500 ms target

All ten people-list mutations now show their exact local intent before the
signed write: rename, add/remove person or agent, grant/remove manager role,
join, leave and delete. Leaving closes only that selected list; deletion hides
only its row. Pending display never changes canonical membership, roles or
permissions. A saved receipt remains pending until a current verified list read
confirms or supersedes it. Failure restores the current display with exact retry
and read-only checking. Duplicate input shares one write; newer signed state,
owner replacement, navigation and teardown cannot revive old feedback. This
reuses the inspected Telegram local-update/reconciliation pattern and the
existing group/creation pending records, without a new backend contract.

The actual TeamsSection/Store regression exercises all ten controls at 1440 and
390 pixels with held writes/reads, failure, retry, duplicate input, navigation,
another selected list and preserved unrelated drafts. Visual inspection caught
rename feedback and its failure above the phone scrollport; the active list now
reveals its action/error once, and the test checks both viewport and ancestor
clipping. Saved replies and cards behind another selected list do not scroll it.
The final 20-case run records 39–60 ms trusted-input feedback. Phone action and
representative desktop/recovery layouts were inspected; captures wait for actual
finite animations after timing, so the outgoing confirmation is not mistaken for
a settled screen. All five affected race parents pass: list mutation, creation,
selection, the two-part teams workload and the canonical bundle (mutation
feedback 37–60 ms). These are focused synthetic-host checks, not exhaustive
control, native-platform or final skin qualification.

Skins commit `9d9bab5` gives Classic, Holonet and Zoom immediate per-chat mute
presentation, reusing their existing reminder-intent and targeted control-update
patterns. Canonical mute state and native notification permission are unchanged.
Headers and already-open details dialogs stay current through pending, saved,
confirmed and failed states. Errors restore current synchronized state, retain
exact retry, announce failure even after phone details close, and offer read-only
confirmation for acknowledged writes. Duplicate input, another selected chat,
newer synchronized state, workspace replacement and teardown are covered.
The previous source fails the held-write regression for all three skins. Final
feedback is 31–41 ms across their six desktop/phone cases. All 34 affected
mounted/mute/read checks pass. The existing real phone-details control test
passes with a 1400 ms write delay; missing and wrong-target confirmation still
fail its original 15-second bound. Static package validation and all seven
generated catalog checks pass.

The disposable signed Zoom touch journey now passes all 28 taps at this skin
candidate against the retained `f7355154` core pin, including mute/unmute,
navigation, project space, 200-message DM and device histories, send, media
details and Activity navigation. Its five final screenshots were inspected.
Activity's captured unread content is still loading; that image proves its
navigation response, not completed history. The prior failing journeys remain
recorded; this focused pass does not constitute final core pin compatibility
or exhaustive interaction qualification.

At `18789c85`, run `40445110427` passes the actual people-list creation and
selection regressions three times on Linux, macOS and Windows. Affected platform
run `40445112905` passes the teams workload on Linux, macOS, Windows and the
Android emulator. Native macOS Create list records 14 ms local feedback,
57 ms verified visible result and 372 ms through the paint check; Android records
18.6, 151 and 169.1 ms respectively. These are the existing synthetic-host
workloads, not native database durability or exhaustive control qualification.
Earlier unrelated macOS failures remain open.

The skins candidate repairs Zoom's no-op device-thread renderer: a confirmed
read now invokes the existing guarded refresh before the unrelated typing
lookup completes. Its regression fails on the old source and passes with exact
thread, host, visit and unmount guards. Zoom navigation also replaces the prior
screen before waiting for history, with a named loading destination, Back and
exact retry. Pending navigation hides stale topic controls. Focused actual-method
tests cover four DM/thread and phone/desktop combinations at 40–99 ms; the old
source fails. The mounted-package fixture measures its in-viewport destination
at 46–90 ms with held/error/retry/cancel cases, and all four final screenshots
were inspected. Its first attempt reopened an already cached DM, correctly
restoring the cache after read failure; the uncached case now resets that fixture
state explicitly. Visual inspection also caught the earlier media probe above
the destination; the final fixture hides that probe and asserts viewport bounds.
Neither invalid measurement is counted as a pass. The 51-check affected suite
passes, followed by 31 passing mounted/read checks after hiding stale topic
controls; shipping package generation and its seven catalog checks also pass.

The earlier disposable Zoom touch journey fails at Mute chat: its POST remains
pending for more than 15 seconds and the chip waits for completion. The bounded
trace records the exact trusted click and no page error. The renderer repair does
not resolve that separate feedback defect; the later mute repair and touch pass
are recorded above. Complete skin/runtime qualification
and the exhaustive interaction gate remain open; nothing is released or deployed.

Create list now inserts its named pending card and clears the submitted name
before the native write. Its shared state follows navigation; only a current
verified list read supplies the real ID and manager/member controls. A saved
receipt remains visibly unconfirmed until that read arrives. Failure keeps the
exact name, explains the uncertain outcome, and offers read-only checking and
an explicit retry. Duplicate pending input shares one write. Retry preserves
newer input, and completion cannot replace a subsequently selected list, reopen
an unmounted view or update a changed owner/stopped workspace. This reuses the
already inspected Telegram local-state/background reconciliation pattern and
AgentNet's existing Store/API, without creating a new authority mechanism.

The actual TeamsSection/Store held-write and held-read regression passes at
1440/390 pixels: initial trusted-input feedback is 51–105 ms, and the integrated
race run records 66–77 ms. All four pending/error screenshots were inspected.
The same race run passes existing list selection, the affected two-part teams
workload and canonical bundle checks (four parents, 62.384 seconds). Workload
feedback and completed list opening are now measured separately. This is local
focused evidence; the later platform results above retain this evidence, while
final skin qualification remains outstanding.

At `4645ead6`, run `40442190118` passes the group-administration and
pending-people regressions three times on Linux, macOS and Windows. Affected
platform run `40442193425` passes Linux and Windows groups/teams. Android
fails Create list at 697 ms; native macOS fails the same action at 724 ms.
Browser WebKit also delays opening a group (984 ms) and people-list settings
(870 ms). That candidate's Create list waits for the backend before showing the
new list; the repair above does not waive those original failures. UI race partitions
1 and 2 pass in `40442196954`; partition 0 has one failed parent, the media-probe
guard's obsolete expected summary omitted the new zero-valued diagnostic count.
The corrected guard passes under `-race`; its original failed partition and all
other unchanged parent results are retained. No data race was reported.

Run `40439149014` at `21a88b26` passes the people-list selection and group
invitation-decision regressions three times on each of Linux, macOS and Windows.
The prior macOS driver-timing failure remains in its original record; this run
uses the trusted-input timestamp and frame checks described below.

Group rename, promote, demote, remove and leave previously awaited the signed
write before changing their local presentation. They now close confirmation
immediately: the requested name is displayed, role/removal rows show the exact
pending choice, and leaving closes only the originally selected chat. Shared
pending/error cards follow navigation. Verified cached membership and admin
rights are unchanged; a write acknowledgement still shows saved/checking state.
Failure reveals current state with explicit uncertainty, read-only confirmation
and an exact-target retry. Repeated pending input shares one write. A newer
group snapshot or changed person defeats old replies; stopped workspaces cannot
change. This reuses the already inspected local projection/background
reconciliation pattern; there is no new permission or transport mechanism.

The actual group controls pass ten trusted-input held-write cases (five actions,
1440/390 pixels), with local feedback 61–111 ms. They execute failure, retry,
read-only confirmation, duplicate input, another selected chat, retained drafts,
newer group state and teardown. All twenty pending/error images were inspected.
The affected group workload, canonical bundle and existing Comic guest controls
also pass. The existing pending-people fixture failed on both the old `21a88b26`
bundle and the candidate because it classified the read-only topic-location
POST as a mutation. Inspection of `TopicTransferLocation` confirms it only reads;
the fixture now permits only that exact source/empty-topic request, retaining the
no-invitation/no-decision assertion. Both desktop/phone journeys pass. Final
platform/skin qualification of the group-admin change remains outstanding.

The f7355154 skin compatibility run retains 308 passing unique Node checks after
normal package generation and the affected catalog rerun. Its runtime run fails
Zoom phone touch while opening Anna's chat; the other applicable runtime groups
pass. A focused isolated run subsequently fails the mute-chip change after a
trusted complete click. Neither failure is dismissed as flaky. Mac native and
WebKit stalls, the exhaustive control inventory and final skin qualification
still block release. No tag, deployment or release is claimed.

The complete macOS run `40430293982` at `557e82e6` fails. Native feedback
includes Permissions 574 ms, the first message menu 582 ms, composer menu
776 ms, default-agent dialog 719 ms, and storage review 844 ms. Browser WebKit
also exceeds the bound. These are unresolved failures, not waived runner noise.
Separately, the new exact-chat assertion incorrectly expected a person's label
for named groups and omitted the harness suffix of a direct agent. The fixture
now expects the actual exact group/agent title while keeping both the immediate
navigation and completed-history requirements. This repairs test expectations,
not application speed. Desktop target setup now recognizes any macOS runner,
allowing a recorded Intel comparison of the same candidate and workload.

Additional control coverage exposed clipboard refusal behind the device-link
modal's inert background. Copy now shows pending state immediately and reports
failure inside the active dialog, retaining the exact link and a usable retry.
Only a successful clipboard reply displays Copied. Repeated pending input is
deduplicated; closing or replacing the link invalidates old replies. This uses
the same inspected local-state/modal recovery pattern cited below, with the
existing browser/native clipboard authority unchanged. Trusted-input tests at
1440 and 390 pixels show held clipboard feedback in 38–44 ms; both error
screenshots were inspected. The profile workload now clicks Copy link and
measures opening the link dialog separately from completed link generation.
The focused integrated run passes all six parents: device link, shared device
decisions, notification changes, picture changes, affected profile/topics/groups/
compose workloads, and canonical bundle verification. Explicit Check device
status, Check notification status, and Check picture status controls are also
executed with delayed confirmation, without repeating their writes. This is
focused coverage, not completion of the control inventory or release gate.

Device removal had the same backend-dependent interaction: the new regression
fails on `e72d5cbd` while the confirmation stays open and the device card remains
unchanged. The selected card now becomes an explicit pending removal and the
dialog closes immediately. The actual roster remains unchanged until readback;
identity, routing and permission selectors never consume a guessed removal.
Pending state follows leaving/reopening Settings. Failure reveals current state,
newer removal or changed keys defeat old replies, repeated input shares one
write, and delayed readback offers a read-only Check removal status action.
Both trusted-input widths pass at 60–62 ms, and all four pending/error images
were inspected. The seven-parent integration selection passes, including the
existing removed-device permission-target regression, selected-view completion,
device decisions/link controls, profile/review workloads and bundle parity.
Reuse is the same inspected local choice/background reconciliation pattern.
Final platform and skin qualification for this correction remains open.

Group invitation decisions also waited for backend completion in both Chats and
Review. The new actual-component regression fails on `74851142` with no local
pending decision within 500 ms. The shared Store now projects only the exact
decided invitation out of attention and both placements display the local choice
while it is saved. It never creates membership or history access. Failure
reveals current state; newer decisions and changed invitation scope defeat old
responses. Duplicate choices share one request; stale invitations still cannot
be accepted. The explicit read-only Check group status action covers delayed
confirmation. Four trusted-input combinations pass (254–390 ms from the driver
call, including its wait for the initial card animation), with all eight
pending/error images inspected. Review now captures focus landing before the
card disappears for both group and device decisions; rendered checks prove the
heading receives focus before the held backend reply. Six integrated parents
pass, including the profile/review workloads and bundle parity. The same
inspected local mutation/reconciliation pattern is reused; final platform and
skin qualification remains open. The existing message-review interaction test
also passes. A real signed relay/browser group journey with twelve queued
messages passes with its opt-in explicitly enabled; it is a functional check,
not the full history stress or per-control latency qualification.

Candidate `e72d5cbd` passes the focused four-parent rendered selection three
times on Linux/macOS/Windows (`40433702568`), all three UI race partitions
(`40433705153`), and Android emulator profile/review/topics/groups/compose
qualification (`40433710886`). These results predate the device-removal and
group-decision corrections. The Apple Silicon leg of comparison run
`40433708162` still fails the unchanged 500 ms bound; an Intel comparison is
pending. No release or deployment has been made.

The later Apple Silicon observation includes a 933 ms Permissions response:
the first DOM mutation occurs at 6 ms, but its first animation frame is at
943 ms. Browser WebKit similarly delays Coding agents to 800 ms despite a
7 ms first mutation. Paint cost remains a hypothesis, not a diagnosed cause.
An opt-in diagnostic now compares baseline, animation/transition removal and
flat paint (also removing shadows, filters, gradients and rounding) using the
same built app and unchanged timing assertions. Every such observation is
explicitly marked diagnostic, including its baseline; browser summaries never
count it as an executed qualification pass. Local checks exercise all three
modes and their classification. These temporary fixture styles are never
applied to the shipping interface. Explicit runner matrices now give each
runner unique artifact names, preserving the release workflow's default names.

The Intel leg of `40433708162` passes all 24 browser cells across Chromium,
Firefox and WebKit (maximum feedback 198 ms), plus three of four native
workloads. Its native quick-reaction picker takes 1,861 ms, so the leg fails.
The same-build paint experiment `40436529466` also fails in every mode:
native Permissions takes 762 ms at baseline, 641 ms without motion, and
769 ms with flat paint. Removing those effects is not a demonstrated fix;
no such style change is applied to the product. Native reaction-opening
profiling is the next diagnostic.

The new group-choice regression passes three times on Linux and Windows in
`40435704231`, but one macOS repetition fails its total driver-call bound.
That test's original locator click waits for the initial card animation before
dispatching input; the failed run did not record the event timestamp, so its
cause remains unconfirmed. The test now sends a trusted mouse click to the
visible card during that animation, records the exact event, and retains the
under-500-ms driver-call bound through two frame opportunities. All four local
cases pass at 55–70 ms, with 47–55 ms from the trusted event. This is a more
explicit input observation, not a claimed repair of the Mac renderer stalls.

Selecting a people list for a conversation also used to wait for its verified
snapshot before opening New group. The regression fails against `26aed84b`;
the dialog now opens before that read and displays recipients only after the
captured list is verified. Failure offers retry for the same list. A closed,
unmounted or superseded request cannot reopen the form or populate another
list's recipients. The new group and invitation operations remain untouched.
Both trusted-input widths pass at 63–107 ms, all four pending/error images were
inspected, and the focused people-list workload, recovery and bundle checks
pass. The same previously inspected immediate-modal/background-read pattern
is reused. Final platform and skin qualification remains outstanding.

Linux CI dependency setup previously spent over 30 minutes on the runner's
Azure Ubuntu mirror before cancellation, including the 27.3 MB WebKit package.
The next disposable runner uses Ubuntu's main HTTPS archive instead, preserving
the selected Ubuntu suites and package-signature verification. This is a CI
download recovery, not a passed app test. The next run, `40421371994`, completes
dependency setup in 43 seconds. Its full Linux native-shell/browser matrix passes:
140 cells, 4,394 synthetic actions, maximum feedback 258 ms. This is the existing
workload matrix at `aed2cb53`, not exhaustive source/control reconciliation.

The later `54018a9a` Linux runner uses `mirror+file` with
`/etc/apt/apt-mirrors.txt`, so changing only the conventional source files missed
its Azure endpoint. Its still-running dependency download was cancelled after
23 minutes; completed macOS/Windows results are retained. The correction also
rewrites the observed mirror list, preserving Ubuntu package signatures and
suites. The corrected affected Linux run passes in `40430290954`; its Android
profile job also passes. Device-link and notification rendered regressions pass
three times on all three desktop systems in `40430297380`.

Profile picture removal also waited for storage. The actual profile/shared-avatar
regression fails against `0b81b58d` with the avatar still visible after five
seconds of held storage. Applying the existing local-projection / background
request / reconciliation pattern now changes all own avatars immediately. Local
trusted-input checks at 1440 and 390 pixels take 26–27 ms including two frame
opportunities; physical paint is not measured. Held writes, duplicate clicks,
refusal/retry, newer synchronized pictures, stale successful replies, delayed
confirmation reads, and stopped-workspace completion are covered. Current state
is revealed on failure; an old snapshot is never restored. Pending picture work
also guards automatic reload. This reuses the inspected Telegram local-update
pattern already cited below; API permissions and public picture storage remain
unchanged. The picture regression and affected state models pass three times on
Linux, macOS and Windows in `40424291595`. The affected native/browser profile
and media run `40424295626` passes Linux and Windows. Native macOS passes both;
browser WebKit still fails first-view timing. Android's unsupported-video action
takes 593 ms, so that candidate is not qualified. Overlay qualification remains
open.

Notification preferences had the same storage-dependent local state: the new
rendered test fails against `997a3b98` while Off still displays On. The selected
toggle/preview now changes locally with explicit pending/confirmation feedback.
Denied or failed OS permission requests never call the enable API or display a
grant; newer authoritative preferences win over older replies. Six local
daemon/Android/browser fixture combinations pass at both widths, maximum 32 ms
including frame opportunities. Android permission calls in this focused fixture
are simulated; the real HTTP consent/privacy tests pass separately. Pending and
failure screenshots were visually checked at 390 and 1440 pixels.

The media audit reproduced unnecessary native-preview restarts: entering
preparation and keeping the original file revoked and recreated its unchanged
blob URL. The real-file regression fails against the earlier Composer and passes
when unchanged bytes retain their preview URL and staging handle. Media work
also yields a paint opportunity before cached codecs run, with bounded progress
in hidden views and abortable queue waits. Real codecs, original-file recovery,
queue cancellation and the existing two-minute deadline pass locally. Eight
affected local Chromium workload cells (settings/profile/media/permissions) pass,
157 synthetic actions, maximum 282 ms. Native reruns remain required.

The complete macOS sample in `40424815338` captures main-thread QuartzCore/Metal
flush waits and shader compilation, but its native timing passes; browser WebKit
still fails at 763–780 ms. This identifies renderer work, not a proven app-level
cause for each failure. Apple documents first-use pipeline compilation and the
resulting hitching risk in [Optimize GPU renderers with Metal](https://developer.apple.com/videos/play/wwdc2023/10127/)
(checked October 27, 2026). The candidate removes the small Settings entrance
translation to test a simpler rendering path; its macOS effect is unverified.

The `54018a9a` affected macOS native/browser run passes all four workloads
(settings/profile/media/permissions): 28 cells, 490 synthetic actions, maximum
local feedback 144 ms. Windows also passes those workloads. All three UI race
partitions pass in `40427508307`; notification/media/pane focused tests pass
three repetitions on Linux, macOS and Windows in `40427505506`. These are
retained scoped checks, not complete control qualification. Android in
`40427502669` still fails `media.open-chat` at 614 ms, while its first DOM
mutation is 37 ms. The old measure required loaded history for navigation
feedback. It now requires the exact selected chat's visible header/loading view
or ready timeline, while separately requiring and recording the completed
history. An imposed 800 ms history delay produces 32–35 ms navigation and
919–922 ms confirmed history locally at both widths; completion is not relabelled
as fast. Existing 500 ms feedback and completion-correctness assertions remain.
Long-frame diagnostics now cover every measured action. Android execution of
this corrected measurement remains required; the old failure remains recorded.

The Android background-connection control also failed the actual rendered
held-bridge regression: the switch stayed Off until the native reply. Its
selected setting now appears immediately with an explicit pending message.
Duplicate input sends one operation; reads begun before the change cannot erase
it, refusal restores the current setting, and subsequent native Stop changes
remain authoritative. The existing inspected local-intent/reconciliation pattern
is reused without changing the native bridge or permission. Local trusted-input
checks pass at both widths, 27–31 ms including two frame opportunities. Pending
and error screenshots at 390 and 1440 pixels were inspected. Native bridge
responses in this regression are simulated; emulator verification remains open.

Creating a device link previously left the settings page unchanged until the
API replied. The actual Add a device regression fails against `5c45c075` with
no dialog after 500 ms. The dialog now opens immediately with pending content,
then displays only the returned real link. Closing while generation runs never
reopens it on a late reply; deliberate reopening retains that result and repeated
input never creates duplicate links. Failure offers retry without changing any
device membership. Both widths pass locally (65–96 ms driver-to-visible dialog),
with pending/error screenshots inspected. This reuses the already inspected
Telegram modal/local-state pattern; AgentNet's signed link and native approval
remain authoritative. Native and gallery qualification are pending.

The shared device-decision regression fails against `557e82e6`: Settings and
Review leave the request active while the API is held. The Store now projects
only that exact request out of attention and shows a shared pending decision;
it never adds a device or grants membership. Duplicate input shares one write.
Failure reveals the current authoritative request, while newer resolved requests,
changed device keys and stopped workspaces defeat stale completion. A saved
reply with held readback remains explicitly unconfirmed with a read-only check.
Four local Settings/Review and wide/phone cases pass (42–259 ms in the first
run); eight pending/error views were visually inspected. The real signed Hub
browser-approval and local-issuer Google enrollment tests pass separately.
Affected local profile/review workloads pass 88 synthetic actions, maximum
228 ms; native and exact final-pin overlay qualification remain open. This
reuses the inspected local intent/reconciliation pattern, preserving native
permission and roster authority.

The updated connection/device-dialog regressions pass three repetitions on
Linux/macOS/Windows in `40430297380`. Linux affected workloads and Android
profile pass in `40430290954` at `557e82e6`. Android media/permissions pass at
`5c45c075` in job `121943512970`; its parent run `40429518057` is not a passing
workflow because an empty desktop matrix was invalid. No skipped desktop job
is counted as executed. The skin pin `54018a9a` completes 308 Node checks with
zero failures/skips and every applicable runtime group, including the previously
failing Zoom provider. All 20 shipping previews are refreshed and visually
inspected. This predates subsequent core changes and is not final release
qualification.

The remaining Base UI popup entrances used transparent or fully offscreen
starting states. Real message-menu/reaction/delete and All Topics regressions
first fail with animation callbacks held, then pass after those initial states
are removed. All ten tested combinations (desktop menu/reaction/delete and
desktop/phone topics, normal/reduced motion) become visible within 500 ms while
the callback canary remains held. Closing behavior and native permission checks
are unchanged. The complete existing local Chromium workload run passes 41
cells / 1,372 synthetic actions, maximum feedback 226 ms. Affected Topics and
media cancellation cells pass after the final fixture/panel corrections. These
counts do not establish exhaustive control coverage or native-platform timing.
The five focused rendered parents (popup, message interaction, review, Sheet,
pane entrance) pass three repetitions on all three native operating systems in
`40421276122`. These execute browser regressions on those runners; they do not
replace the native shell's full interaction matrix.

Media qualification at `37461c9e` passes Android and Windows, and native macOS
media also passes. macOS browser WebKit still delays opening a chat; Chromium
also exposed a fixture race where the tiny clip finishes before the test's two
paint opportunities. Cancellation now holds the real encoder capability check,
proves that work reached it and restores the original bytes on cancel. Normal
real-file encoding/audio remains separately tested. The first macOS sampling
attempt captured a newly spawned prewarm process, missing the active renderer;
diagnostic sampling now includes that disposable runner's existing WebKit pool.

Native focused touch/Sheet/navigation checks pass three repetitions on Linux,
macOS and Windows in [40417133967](https://github.com/misunders2d/agentnet/actions/runs/40417133967)
at `8385362d`. Android startup, notification boundaries, denied permission and
retained-data upgrade pass in [40417137751](https://github.com/misunders2d/agentnet/actions/runs/40417137751);
its three unread actions peak at 171 ms. Retain the other 16 unchanged Android
workloads from `54abbf4c`. This is API35 emulator evidence, not a physical phone.

The Linux media crash was reproduced with the actual AppImage runtime in an
isolated virtual display. Normal conversion also crashes, so cancellation is not
required. Identical video packets succeed without audio; retaining the source's
mono channel also succeeds with its audio. Forcing stereo is unnecessary and is
now removed for mono sources. The original real 36-action media workload then
passes, maximum local feedback 244 ms; the real encoded-file regression verifies
audio remains mono and audible, plus cancellation/original bytes. The precise
native memory-corruption function remains unidentified. New candidate platform
media execution is still required.

The same native probe then confirms stereo-to-stereo succeeds, while forcing
six-channel audio into stereo also corrupts memory. Browser preparation now
preserves the source channel count. A six-channel encoder refusal returns a
normal `EncodingError` instead of crashing the bundled runtime. The rendered
real-file regression verifies six channels survive encoding or the exact
original bytes remain with an explicit fallback notice; local Chromium takes
that original-file path. Native FFmpeg recovery retains its existing conversion
policy. The rebuilt bundle, media budget/deadline, authenticated preparation,
native FFmpeg cancellation/cleanup and real-file rendered checks pass locally.

The `54abbf4c` complete skin run has 36 passing applicable runtime groups and one
Zoom browser-provider fixture failure: its empty `<ol>` has no box, while the
screenshot shows the correct empty DM and enabled Write action. The fixture now
requires that visible usable action and an attached timeline. The affected real
browser-provider enrollment/send/file/reload journey passes separately. Five
inapplicable groups remain excluded; this is compatibility evidence, not complete
interaction timing coverage or a final candidate pin.

Broader macOS qualification remains failing. Diagnostic run
[40417813390](https://github.com/misunders2d/agentnet/actions/runs/40417813390)
separates fixture-query cost (0–2 ms) from 586–942 ms observed feedback delays.
Several first DOM mutations occur in 4–10 ms, followed by a delayed first frame;
the initial menu also waits while transparent. Optional sampling of the renderer process pool is confined to the disposable macOS fixture; sampled timings
are diagnostic only and cannot qualify the release.

The 450 ms hold still left one macOS result at 553 ms when both callback queues
were delayed. The actual message-menu counterpart was then inspected at the same
pinned Telegram revision:
[`useContextMenuHandlers.ts`](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/hooks/useContextMenuHandlers.ts)
uses a 200 ms long tap (its generic `useLongPress.ts` defaults to 250 ms).
AgentNet now reuses that 200 ms message-context threshold, retaining its exact
pointer release guard, move/cancel cleanup and native link behavior. Ordinary-tap
and native-scroll negatives accompany the delayed-timer test. This is an inspected
interaction pattern, not a measured Telegram responsiveness comparison.

Android `54abbf4c` qualification executes 16 workloads with every recorded action
under 500 ms (history maximum 393 ms, composer 92 ms). Unread startup is unexecuted:
its native document focus check fails. The Activity may gain focus before its
asynchronously created WebView attaches; the existing ownership-preserving focus
helper now also runs at internal document readiness. The notification fixture's
invented notices can enter the still-running core startup restore; it now waits
for the existing host-ready boundary before posting them. Both corrections require
new Android execution. Prior notification failures and absence of physical-device
evidence remain open.

The native macOS timer diagnostic separates gesture recognition from rendering:
one 450 ms callback fired at 479 ms just after the 478 ms observation, and another
at 524 ms. Recognition now checks the same 450 ms deadline on available animation
frames as well as the timer; either path cancels both for that exact gesture.
A delayed-timer regression (1,500 ms) opens through the frame path in 495 ms locally;
normal local cases measure 461–478 ms. Move/cancel/release and deliberate next-tap
assertions remain. This is a local result; native qualification is still open.
Chromium's historical scheduler explanation supports the scheduling hypothesis,
not proof of today's exact browser cause:
[Chromium issue 41227744](https://issues.chromium.org/issues/41227744), inspected
October 27, 2026. React's earlier forced commit alone did not resolve the failure.

Paused page entrances and delayed old-pane cleanup also reproduce hidden navigation.
The selected pane now starts on screen, with the departing pane hidden and inert.
Optional entrance motion no longer begins transparent/offscreen; reduced motion
removes entrance movement. Existing late-loading notices retain their delay.
Four width/motion paused-navigation regressions and ten baseline browser cells pass;
maximum local feedback is 219 ms. Existing DOM/scroll retention and cleanup remain.
These reuse the already inspected local-navigation-first pattern, without a new
state store or history architecture. Native macOS/browser timing must be rerun.

The full gallery run on `d70235ad` passes 307 static/Node checks but fails Zoom's
Device conversations center-hit check (covered by the bottom Activity tab) and
the disposable qa-orbit mount (20-second bound). Remaining summary groups pass.
Both failures remain open; a successful lifecycle check is not the mount check.
The owned company was removed and raw screenshots/logs remain private temporary data.

Candidate `d70235ad` integrated qualification `40409823579` exposes real failures
at the new bound: Android history has seven actions at 589–1,134 ms; its composer
plus menu takes 527 ms. Windows WebView2's same menu takes 587 ms and Firefox's
concurrent history update takes 944 ms. Android's foreground/background notification
fixture also fails; physical execution remains unavailable. Linux/macOS integrated
jobs are still running. No publication or deployment is qualified.

The composer plus menu reproduces the sheet's animation dependency: holding
animation callbacks keeps its starting-style opacity at zero past 500 ms. Removing
that initial transparent/scale state makes its real menu visible and selectable
while callbacks remain held. Eight sheet and four plus-menu width/motion cases pass;
menu DOM feedback is 64–141 ms in this isolated run. Background animation scheduling
is not paint evidence. Other required native/platform checks remain open.

All three UI race partitions now pass through retained `ui-0`/`ui-1` and corrected
`ui-2` run `40409836064`. Native Linux/Windows focused UI repetitions and three
Windows offline-leave repetitions pass. Original failures remain recorded below.
A temporary AST inventory finds 627 interactive JSX candidates across 77 of 126
TypeScript source files. This is a narrower candidate extraction than the earlier
text search; reachability, non-JSX/native/skin controls and rendered reconciliation
remain unresolved, so it is not an exhaustive executed inventory.

The broad rendered runner now fails local feedback at 500 ms rather than the
historical 2,000 ms emergency bound. Background completion remains separate.
Local Chromium's five baseline workloads pass 74 actions (maximum 278 ms).
The extended 390/1440 run passes 30 of 31 screen/viewport cells, 1,182 recorded
actions overall and maximum recorded feedback 350 ms. The remaining Agents
case clicked a disabled Save before the folder field's entrance animation
made its required picker visible; no second write occurred. The fixture now
includes that picker in the selected-program visible predicate. Both Agents
widths then pass 43 actions each, maximum 145 ms, at the unchanged 500 ms bound.
These use synthetic DOM input and invented backend data; they do not establish
physical-device, exhaustive-control or real-backend completion qualification.

Default-agent Save replaces its editor immediately with the chosen program
and folder plus truthful saving status. Failure restores the retained editor.
A confirmed write's returned view feeds the Agents tab before reopening, and
older in-flight reads cannot replace that response. Existing editor retention,
resize, exact target and failed-save tests pass under the race detector.

macOS's repeated touch regression at `914acf57` shows the 450 ms recognized
long press sometimes does not commit its sheet before the 470 ms held-touch
observation; release subsequently opens it. The timer now synchronously commits
that existing sheet, preserving the exact initiating-pointer release guard.
Local touch/edit/recovery coverage passes. Native macOS still fails after the
synchronous commit change: the held observation is 473–705 ms after the actual
browser pointerdown and still has no sheet. Timer scheduling is now instrumented;
the previous React-scheduling explanation is unproven.

The visible-row source harness now extracts the intended entry-layout effect,
instead of the earlier navigation-ref effect added by the render optimization.
Its complete visibility/epoch/scroll/resize assertions pass unchanged.

### Sheet visibility and narrow topic labels

Android emulator qualification `40402621499` reproduced the first Actions sheet
remaining transparent: `data-starting-style` stayed set while animation-frame
callbacks stopped. The sheet now follows its controlled `open` value directly;
opening no longer needs a second React commit or animation callback, and closing
hides its retained portal immediately. Reduced-motion CSS no longer overrides
that visibility. Base UI still owns its dialog, Escape and focus behavior;
AgentNet's existing root-local modal handling still isolates background input.

Counterpart: Telegram Web A, the same pinned revision above,
[`Modal.tsx`](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/components/ui/Modal.tsx),
uses controlled open/close state, a portal, focus trapping and Escape dismissal.
AgentNet retains that established flow through its existing Base UI component.
Removing the animation-dependent visibility is the adaptation to the observed
WebView stall; no Telegram latency measurement or identical animation is claimed.

The committed pre-fix Sheet fails the new rendered regression's 500 ms bound.
The fixed component passes eight first/repeated-open cases at 390/1440 widths,
normal/reduced motion, with animation callbacks deliberately held: 5–74 ms
input-to-visible DOM state in local Chromium. Close removes the blocking layer
and restores unrelated draft input before callbacks resume. Desktop and mobile
screenshots were inspected. These are controlled browser measurements, not a
physical Android result. The emulator must rerun the integrated candidate.

At 390 px, New topic's words previously consumed the selected topic label's
remaining width, hiding a successful rename. The icon retains its accessible
label below 640 px; the renamed topic stays visible. All four rendered
edit/delete/topic cases (100/1,000 messages, 390/1440 widths) pass.

The message-toolbar regression now accepts an unmounted inactive toolbar as
absent, as required by the existing lazy toolbar implementation; the former
test waited for an absent button in order to inspect its opacity. The rendered
exact-target, keyboard, narrow controls and reaction assertions remain.

This continuation starts from clean `edec2bab` on
`codex/oct25-agent-conversations`. It does not qualify the app-wide requirement.
No release, installed application update, physical-phone pass or Slack/Telegram
latency parity is claimed.

Reuse evidence: Telegram Web A revision
`f67a40879a7d78c2683ec5a296cf55bc22417d56`,
[`messages.ts`](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/global/actions/api/messages.ts):
`editMessage` clears the editor before its API call (lines 825–869);
`deleteMessages`/ephemeral deletion project locally and recover after failure
(1203–1268); viewport loading uses a local slice before deferred fetching
(222–327). The previously inspected `reactions.ts` reference below remains
applicable. AgentNet reuses these interaction patterns through its existing
Store, API calls and retained editor text. No new transport, worker service,
permission grant or durable success is inferred from a local preview.

Edits/deletions retain canonical message bodies and execution input, with a
separate presentation recording pending, saved or failed. A changed canonical
revision takes precedence. A failed edit keeps its attempted text even after
newer linked-device content arrives; Retry uses an explicit editor, and Cancel
or Discard releases the draft. Pending writes and failed edit drafts block
automatic reload. Duplicate exact mutations share a flight; different edits
cannot silently inherit an older write's success. Topic previews retain their
original version guard and the existing confirmation boundary. The projections
reuse their message arrays when unrelated draft text changes.

Evidence on the source candidate: `go vet ./...` passes. Focused Go race runs
cover Store action refresh, local changes rendered, responsive editor drafts,
reaction pending/revisit, message interactions, first unread entry, keyboard
timeline, topic-bar selection, clock formatting and timeline projection.
The new rendered fixture uses trusted mouse/touch input and held synthetic
host writes, not real persistence or remote execution. Its four width/history
combinations pass edit/delete/topic local changes, refusal/restoration,
retained edit/rename text, retry/discard, newer synced text and unrelated input.
One final sample has event-to-DOM updates of 8.9–64.4 ms and two-frame presentation
opportunities of 26.5–127.8 ms for edit/delete. These are individual observations,
not a p95 distribution, physical display timing or comparative qualification.

The reaction regression was first reproduced with the unchanged base Store.
The corrected run shows local reaction changes at 7.6–11.7 ms while controlled
writes take 617–645 ms, and passes failure, repeated intent, revisit, independent
emoji responses, newer linked intent, hidden tabs and stopped workspaces.
The draft fixture covers eight existing editor types. Temporary screenshots of
wide/narrow pending and recovered message views were inspected.

The temporary final build also passes 391 functional action observations in
12 suite/viewport partitions: history, groups, media, outbox, agents, teams
(list/group), profile, storage (wide/narrow), workspaces and permissions.
Earlier navigation/settings/unread/actions/compose/review checks and the
corrected three-width topic check pass. These remain synthetic-host functional
checks. Four final-build observations exceed the informational 200 ms budget:
exact old quote parent (218.7 ms), invite open (304.5 ms), member invite open
(248.3 ms) and team creation (209.6 ms). Their original timings remain evidence
of unfinished responsiveness, not waived exceptions.

Follow-up to source commit `753a4795`: profiling the two invite openings finds
repeated full-directory presence lookups and per-person parsing of the same
recent messages. The candidate now indexes presence once per pool build,
parses collective mentions once per recent message, and skips name-regex
construction when both ASCII strings prove there is no match. Non-ASCII names
or text keep the existing Unicode matcher. First-record duplicate semantics,
unknown/stale presence, exact references, Unicode/overlapping boundaries,
latest mentioned recipient, pending invitations and native permissions are
unchanged in the source regression. For 1,000 people and four recent messages,
the test permits only a linear directory pass, four collective parses and no
unnecessary ASCII name matchers.

One comparable profiled groups pair falls from 333.2 to 154.4 ms for opening
the guest invite and from 256.4 to 119.5 ms for member invite. Two-frame
presentation opportunities fall from 376.9/307.9 to 182.1/146.8 ms. This is
one synthetic-host diagnostic pair, still above the 100 ms feedback target;
it does not establish a distribution or close the release gate. The race
detector also exposed the existing latency fixture starting its HTTP server
before assigning the handler's server pointer. It now initializes the provider
before listening; this is a harness repair, not a production concurrency fix.
The corrected race run passes the source-model check, the groups journey and
composer journeys at 1440, 320 and 360 pixels without a race report.

Harness corrections preserve original semantic assertions: the history journey
opens the existing collapsed outcome disclosure and checks both exact target
IDs; complete-history counts include every grouped message ID rather than one
DOM row per send. Topic Reopen waits for confirmation to enable its control.
Touch injection now waits for actual hit-test readiness after a closing modal;
it no longer dispatches through the departing backdrop. Original failed runs
remain in temporary evidence. These fixture repairs are not product fixes.

Reminder local intent now uses the same inspected Telegram mutation/recovery
pattern. Set and Move close their sheet and show the chosen reminder immediately;
Done and Cancel remove its due attention immediately. A separate pending status
names the affected reminder. Failure restores current canonical state and retains
the chosen time for explicit retry. Duplicate actions share one write; conflicting
actions cannot reuse its result. A removed Details sheet stays closed when a
newer linked reminder arrives. Unconfirmed older-provider readback retries reads
only, without replaying the write. Pending/failed intent prevents automatic reload.

The existing native reminder revision and browser revision/writer are exposed as
optional view fields. The existing reminder routes accept an optional expected
generation. Native SQL updates and browser checked IndexedDB writes reject stale
Move/Done/Cancel and duplicate create operations, including a newer generation
with the same due time; an old move cannot resurrect a completed reminder.
No schema, transport, execution permission or native notification authority changes.
Older skins keep their existing unguarded route behavior; complete skin update
and qualification remain necessary before release.

`TestReminderChangesRendered` executes actual Store/Reminders/RemindSheet controls
with trusted mouse input at 1440/390 pixels and 100/1,000 loaded messages. Done,
Cancel, Move, Set, Retry, Dismiss error and read-only confirmation recovery pass
with held/refused writes, newer same-due synchronization, duplicate input and
unrelated typing/navigation. The focused race run observes 4.2–11.1 ms from the
click event to the intended DOM change for measured Done/Move/Set and 18.4–27.9 ms
to a two-frame presentation opportunity. These are individual synthetic-host
observations, not p95, physical paint, touch execution or comparator evidence.
Pending and recovered wide/narrow screenshots were inspected. Native conditional
storage tests, browser generation tests, exact response/readback checks and live
generation projection tests pass; the preceding local edit/delete/topic and
editor-draft regressions also pass under the race detector.
The rebuilt shipping bundle passes its checksum/type contract and the 44-action
Outbox functional journey. Real live-host reminder controls pass New York/Kyiv
date/DST cases at desktop and narrow widths, including Cancel/Create/Done and
separate pending-versus-confirmed completion. Conflicting reminder controls are
disabled until their existing write is confirmed; unrelated input stays usable.
The browser guard executes 42 checks against both memory storage and Chromium
IndexedDB. `go vet ./...` passes. The Outbox diagnostic still observes 123.1 ms
for opening an original message; complete responsiveness qualification is open.

Outstanding scope includes remaining settings/participation/bulk flows,
real-backend large-history startup/catch-up,
complete rendered inventory reconciliation, all skins and native platforms,
and measured comparable messenger baselines. Existing successful representative
suites do not close those gaps. Source/build validation is not installation.

History follow-up after `b8928ab4`: profiling finds full-directory allocation in
every avatar and message-author lookup, before checking an exact first peer.
These lookups now stop at the exact match without copying unrelated people.
Identity precedence remains guest, chat peer/member, own person, then directory;
avatar label ambiguity and exact picture removal retain their existing behavior.
Source regressions verify 10,000-person directories require zero unrelated reads
for the own/chat peer and one read for the first directory match. No stale cache,
history-window change or authority decision is introduced. Three affected model
and picture race parents plus the 14-action rendered history journey pass.
One isolated profiled before/after pair does **not** establish an overall latency
improvement: exact notification/quote feedback is 122.3/153.2 ms before and
162.8/186.9 ms after. Geometry/layout, message rendering and pane animation work
remain material, and those interactions still exceed the feedback target.

Release verification at `b8928ab4`: the real-binary `TestMixedVersion` passes
against the newest published lower release, with required compatibility enabled
(97.24 seconds; not skipped). All 18 client race partitions passed in
[CI run 40397384917](https://github.com/misunders2d/agentnet/actions/runs/40397384917).
The later lookup-only delta does not change client source. No publication or
deployment is claimed while the interaction/platform/skin gates remain open.

The next pane-change delta replaces a synchronous computed-animation scan of
the whole incoming pane with DOM matching of its existing entrance nodes. Their
entrances settle through CSS; later arrivals and continuous working indicators
keep animating. Actual-component Chromium checks cover wide/narrow layouts,
both motion preferences, inert leaving content and later class updates. Eight
alternating history runs (four per build, identical binary/backend fixture,
Chromium 155 on this Linux laptop) pass the existing 14 functional actions.
Median notification/quote feedback changes from 221.4/239.6 to 185.4/198.6 ms;
ranges overlap, host load varies, and sending does not improve. These synthetic
diagnostics support removing the measured scan, not a claim of app-wide speed
or release qualification. Exact history targets, retained rows and scroll-anchor
assertions remain unchanged. Raw profiles/logs stay in temporary local storage.

History navigation also recreated every row's focus/jump callbacks when its
target or visit changed. Stable callbacks now read the last committed navigation
context. The actual Timeline/MessageView regression first fails on the previous
source: a notification redraws all 100 retained bubbles. The corrected source
redraws two for notification/visit changes and two to four for quote jumps, with
100/1,000 messages at 1440/390 pixels and 100–344 mounted rows. Exact visibility,
keyboard stop, retained history, a quote after a newer visit and unrelated typing
are verified with trusted mouse input. The existing rendered history, keyboard
and first-unread regressions pass under the race detector. Shipping type/hash
checks pass. The synthetic history timing still exceeds the target; this is
bounded rendering-cost evidence, not complete latency qualification.

The native CI workflow now explicitly installs the pinned browser and executes
eight rendered mutation/recovery/navigation parents on each selected OS. Normal
native runs previously skipped these opt-in tests. This adds execution to the
gate; it does not convert prior skips into passes or substitute Chromium for a
native shell or physical phone.

The first expanded native run exposes harness portability gaps: six Windows
parents cannot execute the Unix Tailwind wrapper; a macOS history-preparation
click races the timeline's automatic prepend/removal of the Earlier control.
The harness now invokes Tailwind's pinned JavaScript entry through Node and
prepares retained pages by scrolling before the measured trusted navigation.
Seven affected rendered parents pass together locally with unchanged functional
assertions. The native source suite also finds a missing retained-editor import
in the topic-copy harness; it now evaluates the real retained-text hook and topic
key function. Its original scope/copy assertions pass under the race detector.
These are test repairs, not product improvements. A macOS reaction measurement
still reaches its original 100 ms wait bound; additional diagnostics retain the
browser timing/state on failure. The latency assertion and bound are unchanged.

Five native Windows client partitions pass. Partition 2 fails while setting up
verified group context, before its outgoing-history-control assertions. The
exact parent then passes three isolated native Windows repetitions in
[run 40402846764](https://github.com/misunders2d/agentnet/actions/runs/40402846764).
Only that failed full partition is rerunning; the unexplained initial setup
failure remains recorded, not silently treated as a clean first pass.

## Current evidence and work

Checklist editor: root. Contributors link evidence and request state changes.
Private timings, screenshots and profiles stay in ignored/local evidence;
this document records the test contract and coverage gaps.

| ID | Required outcome | Owner | State | Evidence or next check |
| --- | --- | --- | --- | --- |
| C1 | Complete source and rendered control/state inventory | root | source candidates collected; reconciliation open | AST inspection finds 627 interactive JSX candidates across 126 core TS files; dynamic reachability, native/standalone-skin controls and executed-case reconciliation remain open. The earlier text scan is not a control inventory. |
| C2 | Every inventory entry maps to an executed case and timing result | root | validator reviewed; execution open | Offline validator rejects incomplete or synthetic evidence; its unit fixtures establish validator behavior only |
| P1 | Dismissal mutation, refresh and visible result are timed separately | root | real rendered baseline and controlled candidate exercised; full timing matrix open | Owned mixed fixture and trusted browser input separate local display, request, commit and refresh. The candidate clears the card while the request is held before the real backend; complete normal/10x and synchronization qualification remain open |
| P2 | Shared slow refresh cause is reproduced and profiled | root | investigating | Installed v0.9.5 compact overview measured 25.645s and later 3.398s; selected read-only store methods completed in 0.844s, so those queries alone do not explain the delay |
| P3 | Measured bottlenecks fixed and normal/10x cases pass | root | scoped candidate; broader qualification open | Per-build participation reuse has adversarial review and focused signed-membership/unread regressions; no general responsiveness fix or 10x pass is claimed |
| P4 | Local dismissal changes UI before asynchronous processing | root | implemented; focused checks pass; release qualification open | Telegram local mutation/reconciliation flow reused. Real blocked-request card/count removal, usable menu/navigation, Review pending state and failure restoration exercised; stale/new-attempt/lost-response/workspace tests pass. Source candidate is not an installed repair |
| P5 | Composer sizing avoids repeated forced layout while typing | root | implemented; rendered regression and affected checks pass; stress journey still fails | Reused Telegram's separate natural-height measurement and batched conditional writes. Old skin writes height 32 times for ten single-line characters; new desktop/narrow regression permits at most two. Four affected race test parents pass; the complete 6x renderer journey still hits its unchanged watchdog |
| P6 | Typing does not rebuild unrelated topic/chat lists | root | implemented; focused checks pass; stress remains failed | TopicBar subscribes only to selected topic/new-topic state and skips workspace chat-list construction for human DMs. Actual-component regression and six rendered chat/layout combinations pass; owned normal real-backend journey passes. The 6x renderer run fails during startup before typing, so no stress improvement is claimed |
| P7 | Repeated message times avoid per-row locale formatter setup | root | implemented; output/cost regressions pass; stress remains failed | Reuses a fixed clock formatter within one synchronous batch, then clears it before later work. Actual model and Chromium parity checks preserve locale, invalid dates, DST and changing device zones; five focused race parents pass. Full 6x journey still reaches its unchanged watchdog |
| P8 | Edit/delete, navigation and topic changes show local intent before I/O | root | source candidate; focused rendered checks pass | Actual Store/Timeline at 1440 and 390 pixels, 100 and 1,000 messages; writes held and refused, drafts/retry/discard, newer linked edits, unrelated typing/navigation and topic rename/done/reopen exercised. Uncached navigation selects its loading destination synchronously. Complete control/platform coverage remains open |
| P9 | A later reaction removal survives returning to its chat | root | source candidate; rendered regression passes | Reproduced the existing revisit failure on unchanged `edec2bab`; keep the exact actor/target/emoji write lane when returning, so removal queues behind an unfinished addition. DM/group/thread and stale-response/workspace cases pass |
| P10 | Timeline day/run comparisons avoid repeated timestamp parsing | root | source candidate; focused regressions pass | CPU profile identified repeated `sameDay` conversions; one conversion per message now supplies adjacent run/day comparisons. Four device zones, DST, midnight, invalid input, unread boundaries and 10,000-message conversion count checked |
| P11 | Large invite directories avoid repeated presence and mention scans | root | source candidate; source parity/cost checks pass | Profiled 1,000-person dialog openings improve from 333/256 ms to 154/120 ms in one diagnostic pair; remaining latency and comparative qualification stay open |
| P12 | Reminder actions change local attention immediately and preserve newer generations | root | native storage, browser/IndexedDB and rendered checks pass; full skin/platform qualification open | Set/Move/Done/Cancel, duplicate input, held/refused writes, newer same-due state, retained retry, stale-dialog closure and read-only confirmation recovery exercised. Optional conditional writes use existing generations; no schema or authority changes |
| U1 | Update version checks do not depend on chat projection | root | verified source only | Existing lightweight endpoint; real blocked-chat regression, native three-OS/race tests and live patched check at 1.089ms; installed app still v0.9.5 |
| X1 | Confusing history/review screen explains state and next action accurately | UX reviewer/root | investigating | Owner screenshot mixes saved history, unknown original agent and contradictory continuation/approval instructions |
| H1 | Pi/OMP idle notices do not repeat remaining items as new | root | candidate tested; installed activation open | Passive activity uses one native status slot. Pi/OMP focused regressions and isolated real Pi rendering pass; prompt acknowledgement remains tied to native persistence. Existing sessions not reloaded |
| B1 | Every intended local change appears under 500 ms | root | owner target updated; full execution open | Live Slack/Telegram benchmarking explicitly removed by owner; measure AgentNet directly and keep background completion separate |
| R1 | Every required interaction passes on final candidate | root/verifier | blocked | No v0.9.7 tag, draft or publication is authorized past this gate |

The previous final-pin skin run passed functional compatibility: 306 browser/static
checks and 37 applicable runtime groups, with five explicitly inapplicable entries.
That run does not establish exhaustive interaction timing or acceptable performance.

The broad latency runner now enforces the owner's 500 ms local-feedback bound
(the historical bound was 2,000 ms). It still cannot establish the complete gate:
its 200 ms smoothness result is informational, and
some journeys use fixed-latency synthetic responses. Cold page startup is outside
those action timers. These results remain functional evidence only. Required
performance evidence must identify the actual backend, trusted input, rendered
control, presented result, exact operation and target, with separate confirmed
commit and remote receipt where applicable. Raw source candidates and matching
test names remain unexecuted until their rendered cases are reconciled and run.

One real-backend Comic/browser dismissal journey now verifies the exact request
resolves once, its original thread stays visible and its attention card clears.
The acknowledgement handler took 0.805ms; SQL observed the committed state by
0.991ms. Conservative screenshot-completion upper bounds were 409ms for feedback
and 886ms for the cleared card. This is one normal/idle functional diagnostic,
without repetitions, 10x history, synchronization load or comparator evidence.
The initial dialog and final Review captures include transitions and do not prove
those screens had visually settled. None of these results qualifies the release.

The local-dismissal candidate reuses the inspected flow in Telegram Web A
[`f67a408`, `toggleReaction`](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/global/actions/api/reactions.ts#L234-L272):
apply the local visual change before awaiting the operation, then reconcile or
restore on failure. AgentNet adapts that behavioral pattern through the existing
Store and resolve operation. A transient presentation removes exact active
attention and decision counts; original message data remains authoritative.
Pending status remains visible in the history and Review. Newer authoritative
state wins; a lost acknowledgement triggers fresh reads without replaying work.

Eight controlled Store behavior cases pass, including duplicate input, delayed
reads, byte-identical overview rows from a newer attempt, rollback, committed
write followed by response loss, and workspace departure. Five affected race
test parents pass; the existing wide/narrow history/Review browser regression
also passes. The directly affected link-store test needed its two pre-existing
schedule-module imports copied into its temporary bundle; assertions remain.
Real Go/SQLite browser checks hold the resolve request before forwarding it:
the exact card leaves immediately, another menu and Review navigation remain
usable, and Review displays pending instead of AllClear. A controlled refusal
restores the current card and badge with an explicit error. Screenshots of the
major chat/menu/Review/pending/recovery states were inspected. These establish
the scoped behavior, not comparative latency, Android performance, exhaustive
control coverage, cross-device convergence or release readiness.

Composer sizing follows the inspected Telegram Web A
[`MessageInput.tsx`, same pinned revision](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/components/middle/composer/MessageInput.tsx#L276-L350):
measure a separate natural-height copy, batch geometry reads, and write only a
changed height. AgentNet uses the existing mention mirror with one unconstrained
inner element; its textarea and input handlers remain the same. One stable
observer schedules one animation frame, and departure cancels it. The focused
rendered regression covers growth, shrink, trailing newline, capped scrolling,
composition events, attachment space, reduced available height and same-chat
read-only/reopen at desktop and narrow widths. Screenshots were inspected.
Resize and multi-agent Composer race checks pass; panel and schedule checks pass
after two bounded harness repairs: allowing only the exact read-only topic-location
lookup already observed on clean HEAD, and providing a temporary screenshot
directory required by the schedule runner. Other write guards remain intact.

The unchanged 20-second stress-driver watchdog still expires in both 6x CPU
renderer runs. Sampled resize work drops from approximately 1,320ms to 42ms in
that one before/after pair, while opening still has an approximately 1,138ms long
task. This is renderer simulation with owned data, not a physical-phone result
or a qualified latency distribution. Full-history payloads, full timeline
preprocessing, topic layout and unrelated list rebuilding remain open.

The narrower TopicBar candidate removes its whole-draft subscription and skips
the unused workspace chat-list construction for human DMs. Its actual-component
check verifies stable selected values when only draft text changes and zero DM
chat-list calls, retaining the existing compact/layout/state checks. Six mocked
desktop/phone combinations across direct chats, groups and agents pass under
the race detector. The harness needed an exact read-only topic-location lookup
instead of incorrectly emitting a content change, plus Cancel-before-Confirm
and precise confirmation wording matching the already recorded owner contract.
The complete request ledger and mutation/result/scope assertions remain.

One owned normal Go/SQLite/Chromium journey passes in 18.07s. Chat opening took
1.548s, the typing phase 342ms, first/repeated Actions 127/69ms, Review navigation
91ms and cached reopening 89ms. These are single driver intervals, not latency
distributions or first physical-frame measurements. Screenshots of chat, menu,
Review, confirmation and the cleared item were inspected. The same candidate's
6x renderer run fails at the initial Chats click's unchanged five-second limit,
before the modern-chat phases; its startup profile includes 772/760ms long
tasks. Neither a stress pass nor a causal before/after speedup is established.

Clock formatting follows Telegram Web A's inspected
[`formatter reuse`, same pinned revision](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/util/localization/dateFormat.ts#L364-L387).
The startup profile identified repeated `timeOf`/`toLocaleTimeString` work.
AgentNet shares one fixed hour/minute formatter between message and reminder
clocks within a synchronous batch, then discards it in a microtask so later work
uses the device's current default zone. Invalid dates retain their previous
output. There is no formatted-result cache or timezone listener.

The actual-model regression fails on the old source with 1,016 per-row locale
calls in its batch. The candidate uses that path only for four invalid inputs
and creates one formatter for the valid rows. Matching output and later-batch
recreation pass across five changing zones and DST boundaries, in default,
Ukrainian and Arabic Node locales. Chromium checks also pass all 15 locale/zone
combinations, including live timezone overrides. Five affected race parents
pass, including rendered reminder and saved-history controls. The complete
6x renderer journey still fails its original 20-second watchdog after seven
captures. Its exact owned request was committed once, but final UI freshness
verification did not complete; that run is a failure, not a performance pass.

The real 48-topic regression exposed a separate binding defect. All 48 topics
remain stored: 40 ordinary human-device topics and eight legacy agent-history
topics. An explicitly opened legacy question shows All topics (0), despite
eight exact-binding overview rows. Its bar incorrectly uses the active-agent
directory projection. The paged list also lacks a way to distinguish those two
bindings when host/key/empty agent ID coincide. This failure remains open;
changing the test's 48 expectation to 40 would hide it. Current-agent directory
filtering and original stored identities must remain intact when repairing it.

## Reported failures that remain open

This register preserves the owner's reports, including cases that have a source
candidate. A passing unrelated test never closes an entry. No next release or
installed repair is claimed by this register.

| ID | Failure | Current evidence and status | Required closing test |
| --- | --- | --- | --- |
| F01 | Interactions leave the user waiting without a clear result | Owner reports delays above 10 seconds across the app; installed compact overview measured 25.645s and 3.398s. Unresolved. | Every inventoried control: trusted input to visible feedback and confirmed result, under the complete required workload/platform/skin matrix and the owner’s under-500-ms local-feedback target. |
| F02 | Mark as handled takes many seconds to disappear | Local-first source candidate clears exact active attention before the request reaches the backend, retains pending status and restores current state on failure. Controlled real-backend browser and focused regressions pass; installed-device and full timing qualification remain open. | Exact task disappears promptly on the initiating and linked devices; preserve unrelated attention and show refusals/failures truthfully. Include normal/10x and concurrent sync. |
| F03 | About cannot update the app | Source fix verified; installed v0.9.5 remains unchanged. Its version check waits for the slow chat overview. | Actual installed old-to-new update, including app, command and running background process; no dependence on slow chat reads and no interruption of admitted work. |
| F04 | Saved agent history, unknown agent and contradictory Reply instructions are confusing | Copy candidate and combined wide/narrow regression exist; not installed. The wider history/composer workflow remains unqualified. | Same history thread with a waiting request: clear available actions, exact Reply routing, retained draft/files and no misleading permission text. |
| F05 | A read-only preflight becomes a human-action blocker | Exact inspected request says "Do not start or modify anything yet." Its report is needs_human, attempt 2, without a native permission prompt. A related answer reports the worker is outside Herdr. The worker's proposal prompt also incorrectly groups environment failures with native permissions. Original phone-to-worker routing remains unverified. | Trace the original phone instruction through delegation and the selected harness. Ordinary clarification/environment reports return to the requester; only a real required authorization becomes a permission decision. |
| F06 | Steering from the phone does not reliably reach the running harness | Owner report unresolved. Source contains native Pi steering and Codex turn/steer calls; their existence is not end-to-end proof. | Phone correction reaches the exact active native session/turn once, is acknowledged by the native steering API and visibly changes the ongoing work. Test completion races, queued follow-ups, stale attempts and lost connections. |
| F07 | Pi repeatedly appends large notification blocks | Candidate replaces passive transcript snapshots/toasts with a keyed native status. Old code fails the regression; five focused test parents pass, including Pi/OMP shrinking/reordered lists and full prompt persistence. Isolated real Pi renders the status through three idle wakes with zero passive transcript entries or acknowledgements. Existing sessions not activated; native OMP rendering unexecuted. | Load and verify the corrected integration in connected harnesses. Pi and OMP: changing activity updates one native status; the next prompt still receives complete current unread/decision metadata with native-persistence acknowledgement. Verify exact selected receiver delivery separately. |
| F08 | Read or completed activity appears again in harness notices | Owner screenshots include repeated answered/running records. State labels alone do not prove effective read status; exact shared-read mismatch remains to be checked. | Read on phone/browser/desktop clears the corresponding hook notice across linked devices; preserve deliberate unread generations, newer unseen items and unresolved decisions. |
| F09 | Passing tests were treated as proof of acceptable performance | Existing runners can pass below 2,000ms feedback, ignore 200ms smoothness failures and use synthetic responses. Reviewed validator guards now pass; complete rendered performance execution is absent. | Incomplete, skipped, synthetic, stale, slow or mismatched evidence fails qualification; every required real interaction has inspectable timing and outcome evidence. |
| F10 | Android loading and first Actions interaction remain slow or invisible | Earlier roughly 13-second phone load and API35 first-menu stall remain unqualified. No current physical-phone tests. | Current candidate on supported Android targets: cold/warm opening, first/repeated Actions, refresh and navigation, with trusted input and displayed-result timings. |
| F11 | Screenshots can capture unfinished transitions while assertions pass | Owned diagnostic's initial dialog and final Review captures show transitional content. Original captures retained. | Verify the intended screen has visibly settled; include transitions in completion timing instead of disabling them for measurement. |
| F12 | Connected harnesses can keep old copied hooks without a clear refresh notice | Runtime source and loaded-session revision are distinct; automatic revision/refresh notice remains unimplemented. | Update hook files, detect an older loaded integration, give the correct native refresh action and verify new-session activation without closing the user's sessions. |
| F13 | Agent/device labels disagree in the request card | Combined history regression still shows an existing Laptop/Zenbook label mismatch. Not repaired by the copy candidate. | Original signed/admitted identity and host labels agree across header, card and action target; never infer a historical agent from today's default. |
