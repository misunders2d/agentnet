# Agent model streamlining — plan for the release after v0.8.17

Owner: "my agents, that's it. one of them is chosen to be the default." This spec (from a judged design pass during v0.8.17) makes the default agent a local mark on one of the person's named agents, with no wire change. Not implemented yet.

# Implementation spec: one agent list, Default as a mark (v0.8.17)

Base: release/v0.8.17 @ 67d33c29. Line references were checked at that commit. No repo files were edited.

## 0. Decisions in one place

1. **One concept.** An agent is an entry in the existing signed local catalog (`agent_catalog_v1`): name, program and folder.
   - "Default" is a local mark on at most one enabled agent.
   - The old device responder stops being a separate kind of thing. It survives in only two roles: a transitional state called the "earlier Default", and a write-through mirror for older binaries.
2. **No wire change.** Requests without `agent_id` still mean "this device's Default".
   - Answers to such requests stay device-authored (`AgentID ""`).
   - Lanes (`""`), session resume (`session.go:73`), model-report key `""`, room participations and the reciprocal resolver keep their meaning.
3. **No store schema step.** All new state lives in one config key that is written in the same transaction as the catalog. Nothing in `store.go`'s step list or in `agentIdentitySchema` (`agentcatalog.go:15`) changes.
4. **Migration never changes behaviour without a click, except in one case.** If the old default's program has no agent yet, it becomes an agent automatically.
   - That agent is signed but not published, with the same program, folder, context and timeout, and it holds the Default mark.
   - In every other mismatch, the old setting keeps routing exactly as today until the person chooses.
5. **At most one agent per program**, enforced on the server for add, turn-on and the compatibility path. A second one needs an explicit "another" choice, which records the program's agents as deliberately kept.
6. **Turning off the Default clears the mark**, after a warning. There is never a fallback to another agent.
7. **One shared surface.** The UI computes nothing about groups or choices. Go's `AgentSetup()` decides them, and the CLI, Comic, skins and phone all render that result.

## 1. Model and states

`agent_default_v1` (config value, decoded with `decodeStrict`, `v` must be 1):

```json
{"v":1,"mode":"agent","agent":"<id>","kept":["<id>",...],"rev":3,"published":3,"legacy":"<sha256 hex>"}
```

`mode` takes one of four values:

| mode | Meaning | Un-named requests | User words |
|---|---|---|---|
| `unset` | Nothing chosen (keeps the MEL-428 distinction) | wait | "No Default yet" |
| `none` | The person answers them | wait | "No default: I answer requests that don't name an agent" |
| `agent` | `agent` = catalog ID holding the mark | run with that entry's Responder | "Default" tag on the row |
| `setting` | Earlier Default: the legacy `responder` key (program + folder, not an agent) | run with the legacy key, as today | "your Default from before (not an agent yet)" |

The other fields:
- `kept`: agent IDs the person deliberately kept as extra agents for a program.
- `rev`: incremented on every catalog change.
- `published`: the `rev` last confirmed by the Hub. When `rev > published`, the UI shows "Saved here, but others can't see the change yet."
- `legacy`: `sha256(rawResponder + "\x00" + rawResponderManual)`, where an absent key counts as `""`. It holds the legacy keys as this binary last wrote or adopted them, and is used to detect writes made by an older binary.

**Groups needing a choice** are computed in Go only. For each program H:
- members = the enabled entries with `Responder.Harness == H` (oldest `Record.TS` first);
- `earlier` = `mode == setting && legacy.Harness == H`;
- n = len(members) + (1 if `earlier`).

The group needs a choice when `n > 1` and either `earlier` is true or some member is not in `kept`.

## 2. Go client (`internal/client`)

### New file `defaultagent.go`

```go
const defaultAgentConfig = "agent_default_v1"
type DefaultMode string // "unset" | "none" | "agent" | "setting"
type defaultState struct{ V int; Mode DefaultMode; Agent string; Kept []string; Rev, Published int64; Legacy string } // json tags as above
var ErrSameProgram  // "%s already has an agent here (%s). Add another only on purpose (--another)."
var ErrProgramChange = errors.New("an agent keeps its program; add an agent for the other program")
var ErrNoEarlier     = errors.New("there is no Default from before to keep")

func ProgramName(harness string) string // agy→Antigravity, claude→Claude, codex→Codex, omp→OMP, pi→Pi
func nextLabel(entries []LocalAgentInfo, harness string) string
// ProgramName if no entry (enabled or off) has that label, else "<Program> N" with the smallest N>=2.
// Never derived from a folder.

func legacyDigestIn(q dbq) (string, error)
func (a *Agent) defaultStateIn(tx *sql.Tx) (defaultState, error)
// Reads the state. If it is absent, runs adoptIn(tx) and returns the result.
func (a *Agent) writeDefaultIn(tx *sql.Tx, st defaultState, entries []LocalAgentInfo) error
// Writes the catalog (if entries != nil), the state and the legacy mirror, and sets st.Legacy to the new digest.
func (a *Agent) defaultIn(q dbq) (r *Responder, via string, err error)
func (a *Agent) adoptDefault() error // called from Open
func (a *Agent) adoptIn(tx *sql.Tx, prev *defaultState) (defaultState, error)

type DefaultView struct{ Mode DefaultMode; AgentID string; Earlier *Responder; TurnedOff string /* ID of a dangling mark */ }
type ProgramGroup struct{ Harness string; AgentIDs []string; Earlier, HasDefault bool }
type AgentSetup struct{ Default DefaultView; Agents []LocalAgentInfo; Kept map[string]bool; Groups []ProgramGroup; Unpublished bool; SlotsLeft int }
func (a *Agent) AgentSetup() (AgentSetup, error)

type AddOptions struct{ Another, Default bool }
func (a *Agent) AddAgent(label string, r Responder, o AddOptions) (protocol.AgentRecord, error)
func (a *Agent) AddEarlierAsAgent(label string) (protocol.AgentRecord, error)
func (a *Agent) SetAgent(id string, r Responder) error                  // enabled entry: folder/context/timeout only; a different harness gives ErrProgramChange
func (a *Agent) TurnOnAgent(id string, r Responder, another bool) error // off entry, with the one-per-program check
func (a *Agent) TurnOffAgent(id string) (wasDefault bool, err error)
func (a *Agent) SetDefaultAgent(id string) error // enabled entry only; mode agent; drops an earlier setting
func (a *Agent) SetNoDefault() error             // mode none
func (a *Agent) KeepOnly(harness, id string, earlier bool, label string) error
func (a *Agent) KeepAll(harness, label string) error

type DefaultChange struct{ AgentID, Label string; Created, Moved bool; Note string }
func (a *Agent) UseAsDefault(r Responder, keepContext bool) (DefaultChange, error) // facade for `responder set` and POST /api/responder
```

### Shared rules for every write

- Each write runs in one transaction. The DSN already uses `_txlock=immediate`.
- Validate first, outside the transaction: `validateResponder` and `checkOMPResponderSetup`.
- Then, inside the transaction: `localAgentsIn(tx)`, `defaultStateIn(tx)`, mutate, `writeDefaultIn`, commit, `a.store.changed()`, `notifyDaemon(a.home)`.

**Mirror rule** (inside `writeDefaultIn`):
- mode `agent` with an enabled entry: `responder` = that entry's Responder JSON; `responder_manual` deleted.
- mode `none`: `responder` deleted; `responder_manual = 1`.
- mode `unset`, or a dangling mark: both keys deleted.
- mode `setting`: the keys are the source and are left untouched.
- In every case `Legacy` is set to the digest of the keys as written.

### Edits to existing code

- **`agentcatalog.go`**
  - `ExecutorStamp` (:33-37) gains `Via string json:"via,omitempty"`. It is local only, never sent and never compared for authority. The readers at `modelreport.go:62`, `replyreceiver.go:568` and `replyreceiver_worker.go:35` use `json.Unmarshal`, so older binaries ignore it.
  - Extract `signAgentRecord(label)` and `putCatalogIn(tx, entries)` from `CreateLocalAgent` (:65-101). `CreateLocalAgent` stays a low-level helper with no one-per-program check; about 1,457 test call sites use it. It bumps `rev` through `writeDefaultIn`.
  - `SetLocalAgentResponder` (:102-144), inside the same transaction:
    - if `id` holds the mark and `r == nil`, set mode `unset`, empty `agent`, and drop `id` from `kept`;
    - if `id` holds the mark and `r != nil`, rewrite the mirror;
    - always `rev++`.
  - `PublishAgentCatalog` (:185-191) reads the records and `rev` together. After the Hub returns 2xx, it sets `published = max(published, thatRev)` in a transaction.
- **`responder.go`**
  - `SetResponder` (:111-137) keeps its name and its 869 test call sites, but becomes the explicit legacy writer, in one transaction:
    - `nil` → mode `none`;
    - non-nil → validate, write the `responder` key, delete `responder_manual`, mode `setting`, update `Legacy`.
    - Its doc comment says that product surfaces use `UseAsDefault`, `SetDefaultAgent` and `SetNoDefault`.
  - `Responder()` (:292) returns `a.defaultIn(a.store.db)`. The free function `responderIn` (:296-310) remains only as the fallback used inside `defaultIn` when the state is absent.
  - `ResponderChosen()` (:267) returns true for `none`, true for `agent` or `setting` when the resolved responder is non-nil, and false for `unset` or a dangling mark.
  - `AdvertisesAgent` (:278) needs no change.
- **`defaultIn`**
  1. Read the state with a plain query.
  2. If it is absent, fall back to the old behaviour (`responderIn`, so a migration that never ran cannot break routing).
  3. `agent`: return the entry's Responder if it is enabled, with `via = id`. It is not re-validated at claim time, matching today's default path. A missing entry returns nil.
  4. `setting`: return `responderIn`.
  5. `none` / `unset`: return nil.
  6. A corrupt state returns `"corrupt default agent configuration"`, which fails closed like `responder.go:307`.
- **`workerlanes.go:59-87` (`availableResolver`)**: for `id == ""`, call `current, via, err = a.defaultIn(q)` in the claim transaction. After `ResolveExecutorIn`, set `stamp.Via = via`. Lane keys stay `stamp.AgentID` (`""`).
- **Callers that need no change** (they go through `Responder()`): the `name` gate at `worker.go:143-151` and `:204-208`; `reciprocalResolver` (`workerreciprocal.go:132`); `modelreport.go:124`; `conv.go:261/290`; `participation.go:969-976` (`agentEnabled("")`); `ui/live.go:63`; `ui/liveagent.go:438`.
- **`client.go` Open**: call `a.adoptDefault()` after `recoverInvalidStatuses` (:285-289) and before `a.hub.workspaceCheck` (:290). Errors are recorded for doctor, never returned (see section 3).
- **`respond.go:255-273` (`NothingRuns`)**, for `agentID == ""`, by mode:
  - `unset`: `no Default agent is chosen here: mark one (agentnet agents default AGENT), or answer it yourself (agentnet reply ID TEXT)`
  - `none`: `you chose to answer requests that name no agent yourself: answer it (agentnet reply ID TEXT), or mark a Default agent (agentnet agents default AGENT)`
  - dangling mark: `your Default agent "LABEL" is turned off: mark another (agentnet agents default AGENT), or answer it yourself (agentnet reply ID TEXT)`
- **`maint.go:169-187` (doctor)**: keep the check name `responder` and change its text.
  - The not-chosen case reads `not chosen yet: requests that don't name an agent wait for you; see agentnet agents`.
  - The `%s in %s` lines get a `Default "LABEL": ` prefix in mode `agent`.
  - Add a check `agents`, always `OK = true` so it does not fail doctor:
    - `N agents; Default LABEL | none | not chosen | from before (codex in DIR)`;
    - one appended `; needs your choice: codex is set up 3 times (agentnet agents)` per group;
    - `; saved here, not yet shared (agentnet agents publish)` when unpublished;
    - `; could not read your agent setup: ERR` after an adoption error.
- **`reviewopen.go:226`**: `no Default agent is chosen here (agentnet agents default AGENT)`. `reviewDir` (:190) is unchanged in shape.

### `UseAsDefault` (compatibility path)

The rules, applied in order:
1. An enabled agent with the same harness and the same folder exists (compared with `filepath.EvalSymlinks` on both sides, falling back to string equality). Mark the oldest one Default. If the CLI gave `--context` or `--timeout`, apply them to that agent.
   - Note: `"LABEL" is your Default.` (plus `Its context/time limit changed; requests that name LABEL use it too.` when changed).
2. The current Default is an agent of the same harness. Change its folder (and context/timeout if given), keeping the mark.
   - Note: `LABEL now works in DIR (requests that name LABEL go there too).`
3. The harness has no enabled agent. `AddAgent(nextLabel, r, {Default: true})`.
   - Note: `Added agent "LABEL" (PROGRAM in DIR); it is your Default. Others can't pick it by name until it is shared (agentnet agents publish).`
4. Otherwise refuse:
   - `PROGRAM already has agents here: LABEL (DIR)[, …]. Make one your Default (agentnet agents default AGENT), or add another on purpose (agentnet agents add --harness H --dir DIR --another --default).`

In mode `setting`, a success under rule 1 or 3 drops the earlier setting. The POST path copies context and timeout from the current Default, as `liveresponder.go:93-99` does today.

### `KeepOnly` / `KeepAll` / `AddEarlierAsAgent`

- **`KeepOnly(H, id, earlier, label)`**
  - With `earlier`: requires mode `setting` with harness H. It creates an agent from the stored legacy Responder (verbatim, with no `validateResponder` stat), labelled `label` or `nextLabel`.
  - It turns off every other enabled member of H (Responder becomes nil; the identity stays) and drops them from `kept`.
  - If the mark or the earlier setting was in H, the mark moves to the kept agent.
  - `rev++`.
- **`KeepAll(H, label)`**
  - If the earlier setting is for H, it becomes its own agent (`label` or `nextLabel`) and takes the mark.
  - `kept += all members` (and the new agent).
- **`AddEarlierAsAgent(label)`**: requires mode `setting`. It creates the agent and marks it Default.
- All three refuse with `local agent catalog is full` when 32 entries exist; turned-off entries count, as at `agentcatalog.go:87`.

## 3. Migration (`adoptDefault` / `adoptIn`)

`adoptDefault` steps:
1. Read the state and `legacyDigestIn` without a transaction.
2. If the state exists and `st.Legacy == digest`, return. This is the common path and takes no write lock.
3. Otherwise `BEGIN` (immediate), re-read, and run `adoptIn(tx, prev)`.
   - `prev` carries `kept`, `rev` and `published` over when a state existed. That case means an older binary wrote the legacy keys during an update switch, or after a downgrade and re-upgrade.

`adoptIn` cases:

| Case | Condition | Result |
|---|---|---|
| A | The legacy `responder` exists and is decodable. An enabled entry has the same harness, the same `EvalSymlinks` folder, an equal `Context` slice and an equal `Timeout`. | mode `agent` pointing at the oldest such entry. No new identity; catalog bytes unchanged. |
| B | Legacy present, and no enabled entry has that harness. | Sign a new `AgentRecord` (`V:1`, `NewID`, `Host`, `HostKey`, `Label = nextLabel`, `TS = now`) with the stored Responder verbatim. Append it, set mode `agent`, `rev++`, so it is unpublished. |
| C | Legacy present; the harness has enabled agents but none matches exactly. Also the full catalog (32) and a legacy value that does not decode. | mode `setting`. Routing is exactly as today. A legacy value that does not decode makes `defaultIn` return the corrupt error, as today. |
| D | No legacy `responder`; `responder_manual` present. | mode `none` |
| E | Neither key. | mode `unset`. No auto-pick, even with exactly one agent. |

Rules that hold throughout:
- Write the state with `Legacy = digest`. The mirror keys are not rewritten during adoption: they already equal the source.
- Never delete, re-sign or relabel an existing entry.
- Never touch `inbox` or `outbox` `agent_id`, executor stamps, participations, reply-receiver bindings, `person_grants`, task grants or approvals. They are keyed per person, device or address, and reply receivers keep their exact `AgentID`.
- Never publish. The daemon does not auto-publish; any later explicit save does.
- If `localAgentsIn` fails (a corrupt catalog), leave the state absent. Routing falls back to the legacy keys, and doctor's `agents` line reports the error. Open still succeeds.

**Zenbook walk-through**

Starting point: catalog Codex (codex, A), Claude, Pi, Zenbook (codex, D); legacy codex in E.
- Case C applies, so mode becomes `setting`. Un-named requests keep running in E, device-authored.
- The list shows one card, "Codex is set up 3 times", with the options Codex (A), Zenbook (D), and E as "your Default from before". No identity is created until the person clicks.
- If D == E after `EvalSymlinks`, case A applies instead: Default goes on Zenbook, and the same card offers Codex or Zenbook.

**Other computer (no default)**

- Case E: mode `unset`. The card "No Default yet" offers one click, "Make Claude the Default", or "I'll answer them myself".
- Approved questions and accepted tasks that are already waiting are claimed after the click, as today when a responder is chosen. The card says so.
- The terminal fallback from `agentnet open` names the new commands.

## 4. Wire and compatibility

- **Unchanged:** `envelope.Target` and `AgentID` rules, `protocol.AgentRecord` v1 canonical bytes, `PUT`/`GET /v1/agent-catalog`, Hub storage, `engine.mjs`, `MaxAdvertisedCaps`, `ModelSync`.
- **Un-named requests:** the answer `AgentID` stays `j.AgentID` (`""`) at `worker.go:912/918`; `inbox.agent_id` stays `""` (`store.go:1278`). `Via` lives only in the local `inbox.executor` JSON.
- **Senders of any version:** no difference.
- **Older recipients:** no difference.
- **Own devices on older builds:** suspended by the latest-only policy. They would see nothing different anyway.
- **Same home, old daemon and new CLI during an update switch:**
  - The catalog format is unchanged, so the old binary still decodes it.
  - The legacy keys mirror the resolved Default, so the old worker routes identically.
  - A write the old binary makes to the legacy keys changes the digest and is re-adopted at the next Open of the new binary.
- **Downgrade:** the old binary sees the mirror and the full catalog, and ignores `agent_default_v1`.
- **API changes** (`/api/agents`, `/api/responder`, overview): all additive JSON.
- **Recorded for later (DECISIONS.md), not done now:** answers to un-named requests carrying the Default's `agent_id`. That would touch:
  - `agentwire.go` `checkDeviceAgent`
  - `devicehistoryqueue.go:369`
  - `replyreceiver_original.go:52`
  - `proposal.go:305`
  - `assistantreaction.go:550`
  - `engine.mjs:7096/8963`
  - `workerreciprocal.go:21-61`
  - session continuity (`session.go:73`)
  - `MaxAdvertisedCaps` (`person.go:517`)

## 5. UI server (`internal/ui`)

### `liveagentcatalog.go`

Additive types:
- `CatalogAgent` gains `Default bool json:"default,omitempty"` and `Extra bool json:"extra,omitempty"` (in `kept` within a group larger than one).
- `AgentCatalogView` gains, for local views only:
  - `Default *DefaultAgentView` = `{mode, agent_id?, earlier?: ResponderView, turned_off?: id}`;
  - `Attention []AgentAttention` = `{kind: "unset"|"earlier"|"group"|"unshared", harness?, agent_ids?, earlier?, has_default?, full?}`;
  - `SlotsLeft int json:"slots_left"`.
  - All three are built from `client.AgentSetup()`.
- `HarnessView` (`ui.go`) gains `Label string json:"label"` = `client.ProgramName`.

`AgentCatalogChange` gains `Another bool json:"another,omitempty"`, `MakeDefault bool json:"make_default,omitempty"` and `Earlier bool json:"earlier,omitempty"`. The actions:

| Action | Body | Calls | Publishes |
|---|---|---|---|
| `create` | label?, harness, dir, another?, make_default? | `AddAgent` (an empty label uses `nextLabel`) | yes |
| `update` (enabled) | id, dir[, harness equal] | `SetAgent`; a different harness is refused with "An agent keeps its program. Add an agent for the other program instead." | no (the public list is unchanged) |
| `update` (off) | id, harness, dir, another? | `TurnOnAgent` | yes |
| `disable` | id | `TurnOffAgent` | yes |
| `default` | id, or `""` for no default | `SetDefaultAgent` / `SetNoDefault` | no |
| `keep-only` | harness, id or earlier:true, label? | `KeepOnly` | yes |
| `keep-all` | harness, label? | `KeepAll` | yes |
| `add-earlier` | label? | `AddEarlierAsAgent` | yes |
| `publish` | (unchanged) | `PublishAgentCatalog` | yes |

- `ErrSameProgram` maps to a refusal that tells the page to set `another`.
- A `disable` of the Default sets `Note`: "LABEL is off. Requests that don't name an agent now wait for you until you choose another Default."
- `default` notes:
  - an agent: "LABEL is your Default. Requests that don't name an agent go to it from the next one."
  - no default: "Requests that don't name an agent now wait for you."
- The `responderReady` check (:121) is unchanged.
- The remote-host branch (:65-73) is unchanged.

### Other server files

- **`liveresponder.go`**: GET returns the Default's `ResponderView`, plus additive `mode`, `agent_id` and `label`. POST: `Manual` calls `SetNoDefault`; otherwise the existing PATH/harness checks (:70-88), then `UseAsDefault`, returning its `Note` or refusal.
- **`ui.go` `Me` (:838-839)**: add `DefaultAgent string json:"default_agent,omitempty"` (the label in mode `agent`). `live.go:63` fills it.
- **`assistantsetup.go`** (and `cmd/agentnet/assistantsetup.go:296` note): "Only selected integrations will be updated for this workspace. Your agents, Default, grants, skills, MCP configuration and history are unchanged."
- **Message view (optional, can slip):** `client` conversation `Message` and `ui.Message` gain `RanAs string json:"ran_as,omitempty"`: the label of the executor's `agent_id`, or of `via`. It is shown in received-request details as "Ran as LABEL" or "Ran as LABEL (your Default)".
- **Regenerate** `web/src/api.gen.ts` (checked by `tsgen_test.go`).

## 6. Comic, desktop and phone (`internal/ui/web/src`)

### New `features/YourAgents.tsx` and `features/YourAgents.model.ts`

- The model file has type imports only and is node-checked by `testdata/your_agents_model_check.mjs`.
- It holds the row order (Default first, then by program label, then by name) and every string below.

**Native computer** (the `local` view). Header and lead:
- **Your agents on this computer**
- "Each agent is one program working in one folder, with your own settings and sign-in. Requests that name an agent go to it; requests that don't go to your Default."

Attention cards come first and are rendered from `attention`:
1. `unset` with agents:
   - Title "No Default yet".
   - Body "Requests that don't name an agent wait for you until you choose a Default. Approved questions and accepted tasks already waiting go to it too."
   - Buttons [Make LABEL the Default] (when exactly one agent is enabled) or [Choose a Default] (focuses the first Default radio), and [I'll answer them myself].
   - With no agents: body "Add an agent so requests can be answered for you, or answer them yourself." Buttons [Add an agent] and [I'll answer them myself].
   - If `turned_off` is set, prefix the body with "Your Default, LABEL, is turned off."
2. `earlier` (no group):
   - Title "Your Default isn't in this list yet".
   - Body "Requests that don't name an agent run PROGRAM in DIR, as before."
   - Button [Add it as "NAME"], opening a sheet with an editable name prefilled from `nextName`.
   - If `full`: "This computer already keeps 32 agent names, the most it can (turned-off agents count too). Your Default keeps working as it is."
3. `group`:
   - Title "PROGRAM is set up twice" or "PROGRAM is set up N times".
   - Body "You usually need one PROGRAM agent. Keep more only if you use different folders on purpose."
   - Radio options:
     - "LABEL · FOLDER", plus a Default tag;
     - for the earlier setting: "FOLDER · your Default from before (not an agent yet)".
     - The preselected option is the Default or the earlier setting when either is in the group, else the oldest.
   - When `has_default || earlier`: "The one you keep becomes your Default."
   - [Keep only this one] opens a confirm:
     - title "Keep only LABEL?";
     - body "OTHERS will be turned off. They stop taking requests; their names stay on past messages, and you can turn them on again." (plus "Your Default from before (DIR) will no longer be used." when applicable);
     - when keeping the earlier setting, a Name field (default `nextName`);
     - button "Keep only LABEL".
   - [Keep all N]:
     - with an earlier setting: confirm "Keep all N?", body "DIR becomes its own agent named "NAME" and stays your Default. Others can pick each one by name.";
     - otherwise it acts at once, with the toast "Keeping all N PROGRAM agents."
4. `unshared`: "Saved here, but others can't see the change yet." [Share again]. This reuses the existing string.

Rows: one per enabled agent, plus, in mode `setting`, a first row for the earlier setting with the "Default" tag and the subline "Not an agent yet".
- Avatar.
- Name and a "Default" tag.
- "PROGRAM · folderName(dir)".
- Ready or Not ready dot with `problem`.
- `AgentModelReport`. The Default row also shows the reports for agent `""`.
- Active chats via the existing `Chats`. Participations with `agent_id ""` are listed under the Default row, captioned "sent to this computer's Default".
- [Chat with LABEL].
- A radio labelled "Default" in a radiogroup with `aria-label` "Default agent", whose last row is "No default: I answer requests that don't name an agent myself".
  - A change uses the existing sticky unsaved-change bar: "Make LABEL your Default?" (plus " Your Default from before (PROGRAM in DIR) will no longer be used." in mode `setting`), with [Undo] and [Save].
- [Change folder] opens a sheet:
  - title "Change LABEL's folder";
  - a `FolderField`;
  - the note "Requests that name LABEL start here from the next one" (plus ", and so do requests that don't name an agent, because it's your Default.");
  - button "Save".
- [Turn off] opens a confirm:
  - title "Turn off LABEL?";
  - body "It stops taking requests on this computer. Its name stays on past messages, and you can turn it on again.";
  - for the Default, add "It is your Default: requests that don't name an agent will wait for you until you choose another Default.";
  - with active chats, add "N active chats use it.";
  - button "Turn off".
- `Details`: Works in (full path), Program (path), Time limit, Always given (context), and "What requests may use" (`HarnessLimits`, moved from `Settings.assistant` `Limits`).

`Details` "Turned off (N)" holds rows reading "Turned off. Its name stays on past messages." with [Turn on], which opens the sheet with program, folder and the another check.

[Add an agent] opens `AgentsView.forms.tsx` `AgentSheet`, reworked:
- Title "Add an agent".
- Description "Choose the program, its folder and a name others will see."
- Program radios use the server `label`. The right-hand text reads "You have LABEL", "Installed here" or "Not installed here".
- If the chosen program has an agent, a required checkbox "Add a second PROGRAM agent on purpose", with the hint "Requests that name LABEL still go to LABEL." It sends `another: true`.
- Name, prefilled `nextName`, maximum 64.
- Folder (`FolderField`, existing hint).
- Checkbox "Make it the Default", pre-ticked when mode is `unset` or there are no agents, with the hint "Requests that don't name an agent go to it."
- Footer "It uses your own PROGRAM settings, skills, plugins and sign-in. Adding it lets nobody in: your approvals stay as they are."
- Button "Add agent".

**Other own computers** (desktop and phone, the same component `OtherComputersAgents`):
- For each own device with `runsAgent`, a heading "On DEVICE".
- Rows of that device's public catalog: name, model report and [Chat with LABEL].
- Then a row "Default on DEVICE" with the subline "Answers requests that don't name an agent, if a Default is chosen there", plus " · last ran PROGRAM" when that host's report for `""` carries a harness. Its button is [Chat], which sends un-named.
- Footer "Add or change agents on DEVICE itself."
- No edit controls.
- `BrowserNote` stays only when no own device runs an agent.

### Screens

- **`AgentsView.tsx`**
  - Remove `MyAgent` (:102-151), `NamedAgents`/`NamedAgent` (:166-246) and the "Connect an agent" wizard.
  - The "Your agents" section renders `<YourAgents/>` when native, then `<OtherComputersAgents/>`, replacing `OwnAgentChats includeLocal={false}` at :79.
- **`Settings.tsx`**
  - Nav label (:40) "Your agents".
  - Summary (:187), by mode:
    - `unset`: "Not set up"
    - `none`: "You answer"
    - `agent`: "Default: LABEL"
    - `setting`: "Default: PROGRAM"
- **`Settings.assistant.tsx`**
  - `AssistantSection` = `PageHead` "Your agents" (lead as above) + `<YourAgents/>` + the "Connect your open sessions" card + `AppControls`.
  - Remove `AssistantForm`, `Status`, `Choice`, `Limits` and the "Default answers and permissions" `Details` (:42-102+).
  - `harnessName` becomes a fallback only; callers use the server `label`.
- **`Settings.system.tsx:313`**: Fact "Default agent": "LABEL (PROGRAM in DIR)".
- **`AssistantSetup.tsx` / `.model.ts`** (hooks only):
  - Remove `AgentPick`, `agentsFor`, `canHaveAgent`, `agentStatus`, `firstPick`, `pickOf`, `pickFor`, `notReady`'s agent part, `applySetup`'s agent loop, `SavedAgent`, `savedLine`, `Applied.{catalog, picks, agents, shared}`, `AgentChoice` and `AgentLine`.
  - `selectable` becomes `nativeSelectable`.
  - Strings:
    - card title "Connect your open sessions";
    - home "Lets the Claude Code, Codex, Pi or OMP sessions you open yourself see what arrived for AgentNet. It adds no agent and changes no Default.";
    - stage titles "Choose sessions to connect", "Check the changes", "Sessions connected";
    - review "Only these tools change. Your agents, Default, approvals, skills, MCP servers and history stay as they are."
- **`AgentChat.tsx` `ownAgentChatTargets` (:26-48)**
  - Local targets are the enabled named agents, labelled "LABEL (Default)" for the mark.
  - The un-named local target exists only in mode `setting`, labelled "Your Default (PROGRAM)".
  - Remote own hosts get their named agents plus a "Default on DEVICE" un-named target.
  - `checkAgentChatTarget` (:51-69) uses `mode` from `/api/responder`.

## 7. Skins and static pages

Rebuild the bundles and `SHA256SUMS`.

- **`static/assistant-setup.mjs` and `skins/{classic,zoom}/src/assistant-setup.mjs`**: hooks only. Drop the agent create/update/publish loop (static :35-58; skins around :61). The review text is the same as Comic's.
- **`skins/{classic,zoom}/src/entry.mjs` `renderNamedAgents` (classic :4993+)**
  - Heading "Your agents on this computer".
  - A per-row "Default" tag and a "Make Default" button; a final "No default" button.
  - One line per `attention` entry, with the same buttons as Comic (keep-only, keep-all, add-earlier, share).
  - On `ErrSameProgram`, confirm "Add a second PROGRAM agent on purpose?" and resend with `another: true`.
- **Strings** (classic line numbers; zoom equivalents, for example zoom :3697):
  - :792-793 → "Your Default agent: LABEL (PROGRAM)" / "No Default: requests that don't name an agent wait for you". Details "To choose one, see agentnet help agents."
  - :2873 → "No Default agent is chosen on this computer yet: what is asked of it waits until you choose one."
  - :3899-3901 → "Your Default agent (LABEL) in DIR" / "Nothing yet: no Default agent is chosen. It stays accepted until you choose one (agentnet agents default)."
  - :3833 uses `ran_as` when present.
- **`docs/UI_SKINS.md:446-456`**:
  - The setup contract ends after "applies exactly that reviewed change. It creates no agent and changes no Default."
  - Add an `/api/agents` paragraph: the actions in section 5, `attention` kinds, "never move the Default mark without the person's click; create a second agent for a program only with `another: true`".

## 8. CLI (`cmd/agentnet`)

New `agents.go`, plus `case "agents": return runAgents(ctx, a, rest, os.Stdout)` next to `main.go:184`.

AGENT is an exact label (unique among all entries), a full ID, or a unique prefix of 8 or more characters. An ambiguous match errors with `"NAME" matches several agents: ID8 "LABEL", …; use an ID`.

```
agentnet agents [list]
agentnet agents add --harness NAME --dir DIR [--name NAME] [--context FILE]... [--timeout D] [--default] [--another]
agentnet agents add --earlier [--name NAME]
agentnet agents set AGENT [--dir DIR] [--context FILE]... [--no-context] [--timeout D]
agentnet agents default AGENT | --none
agentnet agents off AGENT
agentnet agents on AGENT --harness NAME --dir DIR [--another]
agentnet agents keep-only AGENT | --earlier [--name NAME]
agentnet agents keep-all PROGRAM [--name NAME]
agentnet agents publish
```

List output: two-space separated, first word is the kind, labels quoted with `%q`, IDs shown as 8 characters. Example (Zenbook after upgrade):

```
default  from before: codex in /home/u/e (not an agent yet)
agent    3f2a9c1e  "Codex"    codex   /home/u/a  ready
agent    9b1c77d0  "Zenbook"  codex   /home/u/d  ready
agent    c0de1234  "Claude"   claude  /home/u/b  ready
agent    5e6f7a8b  "Pi"       pi      /home/u/c  not ready: pi is not on PATH
off      12345678  "Old"
choose   codex is set up 3 times (Codex, Zenbook, from before /home/u/e): keep one (agentnet agents keep-only AGENT, or --earlier) or all (agentnet agents keep-all codex)
note     saved here; others can't see the latest change yet (agentnet agents publish)
found    claude codex pi (on this shell's PATH; not run, so sign-in is unchecked)
```

- The default line has four other forms: `default  agent ID8 "LABEL"`, `default  none: you answer requests that don't name an agent`, `default  not chosen: requests that don't name an agent wait for you (agentnet agents default AGENT)`, and, for a dangling mark, `default  agent ID8 "LABEL" is turned off: requests that don't name an agent wait for you`.
- Agent lines end with ` default` and/or ` extra` (kept).
- Commands that change the public list (`add`, `on`, `off`, `keep-only`, `keep-all`, `add --earlier`) call `PublishAgentCatalog` and print `shared` or `saved here; not shared: ERR (agentnet agents publish)`.
- `set` and `default` print what changed:
  - `"Codex" now works in DIR (requests that name Codex go there too; it is your Default)`
  - `"Claude" is your Default: requests that don't name an agent go to it from the next one`
- An `add` refused by the one-per-program rule prints `ErrSameProgram`.

**`responder` stays as a documented alias** (`runResponder`, `main.go:838-917`):
- `list`: `printHarnesses`. The last line (:1046) becomes "Choose with the person: agentnet agents add --harness NAME --dir DIR --default, or agentnet agents default --none."
- `show`: prepends `agent ID8 "LABEL"` in mode `agent`, or `from before (not an agent yet; see agentnet agents)` in mode `setting`. The existing harness/dir/timeout/context/note/approved lines are unchanged. The unset line becomes `not chosen yet: ask the person, starting from agentnet agents`.
- `set`: `UseAsDefault`. Prints `Note`, then the old `responder %s in %s` line for scripts.
- `off`: `SetNoDefault`. The same line as today.

**Help and text**
- `help.go`:
  - New `agents` topic (text below). Register `agents <sub>` for each subcommand in `init` (:1283).
  - Add `name` to `valueFlags` (:1294).
  - The `responder` topic (:737) gets a first paragraph: "Older name for agentnet agents, kept so scripts keep working: set marks or adds the program's agent as your Default (see agentnet help agents); off is agents default --none."
- `agents` topic body:

```
Your agents on this computer. Each agent is one program (agy, claude, codex,
omp or pi) working in one folder, with your own settings, skills, plugins and
sign-in; AgentNet changes none of them. Requests that name an agent go to it.
Requests that don't go to your Default: one of these agents, or nobody
(default --none: they wait for you). Nothing is chosen for you.

  list       your agents, the Default, programs found here, and anything that needs a choice
  add        add an agent, named after its program unless --name is given. A program
             that already has an agent needs --another. --default also makes it your
             Default. add --earlier turns your Default from before (a program and
             folder chosen with an older AgentNet) into an agent; it stays your Default.
  set        change an agent's folder, context files or time limit; it keeps its name
             and program. If it is your Default, requests that don't name an agent follow it.
  default    mark AGENT as your Default, or --none to answer those requests yourself.
             Applies from the next request.
  off        turn an agent off: it stops taking requests; its name stays on past
             messages. If it was your Default, requests that don't name an agent wait
             for you until you choose another.
  on         turn an agent on again with a program and folder.
  keep-only  when a program is set up more than once, keep AGENT and turn the program's
             other agents off (--earlier keeps your Default from before as a new agent).
             The one you keep becomes your Default if the Default was one of them.
  keep-all   keep every agent of PROGRAM on purpose; a Default from before for it
             becomes its own agent and stays your Default.
  publish    share the saved list so others can pick your agents by name. add, on,
             off, keep-only and keep-all try this too and say whether the Hub confirmed it.

AGENT is a name from list, an ID, or its first 8 or more characters.
Approvals and task permissions belong to people and devices, not agents:
changing agents changes none of them.
```

- Join hint (`main.go:359-361`): `next: set up the person's agent on this device (agentnet help agents); if they have not chosen, ask them or offer to answer by hand (agentnet agents default --none)`. Update `join_test.go:74` to match `person's agent`.
- `invite.go:163-188`, step 5:
  - "Help the person choose their agent: who answers for them."
  - "First run: agentnet agents."
  - Keep an existing Default or `none`.
  - A `choose` line is shown to the person, who picks.
  - Otherwise use the `found` line.
  - The commands become `agentnet agents add --harness NAME --dir DIR --default   or   agentnet agents default --none`.
  - The rest of the wording is unchanged.
- `skill/SKILL.md:13`: `agentnet responder` becomes `agentnet agents`.
- `dm.go:259-272`: `dm invite` gains `--agent NAME|ID`. It resolves against the verified `a.AgentCatalog(ctx, HOST)` (exact label or ID) and calls the existing `InviteNamedAgent` (`participation.go:562`). Usage: `dm invite [--agent NAME|ID] [--grant …] [--tasks …] [--note TEXT] ID HOST`.

## 9. Docs

- `README.md:294-299`, `docs/revival/M4.md:32` and `docs/revival/INSTALL.md:231-234`: use `agentnet agents add --harness codex --dir … --default` / `agentnet agents default --none`, and mention `responder` as an alias.
- `docs/DECISIONS.md`: new section "11. One agent list; Default is a local mark (v0.8.17)". It records:
  - the model and the four modes;
  - "un-named answers stay device-authored", with reasons (no Target to bind, session resume, lanes and model key, V3 participations);
  - the rejected dfa1 design and the section 4 checklist;
  - the adoption cases A–E;
  - the one-per-program rule.
- `docs/HANDOFF.md`: scope and verification rows. `docs/UI_SKINS.md` as in section 7.
- M-notes: the known limit that un-named lane `""` and a named lane for the same agent can run at the same time in one folder, as already happens today when a named agent matches the old default.

## 10. Tests

### Client (new `internal/client/defaultagent_test.go`)

**Adoption**
- `TestAdoptExactMatchNoNewIdentity`: catalog bytes unchanged, mode agent, symlinked folder still matches.
- `TestAdoptCreatesSignedUnpublishedAgentWhenProgramHasNone`: `Verify(Self)` passes; label `Codex`, or `Codex 2` when "Codex" exists even turned off; never a folder name; rev > published.
- `TestAdoptMismatchKeepsEarlierSetting`: the Zenbook fixture gives mode setting; un-named jobs keep stamp `AgentID ""` with the legacy folder.
- `TestAdoptManualAndUnset`: no auto-pick with exactly one agent.
- `TestAdoptCatalogFullKeepsSetting`.
- `TestAdoptCorruptCatalogLeavesLegacyRouting`: Open succeeds; doctor `agents` reports the error.
- `TestAdoptIdempotentAndConcurrentOpens`: two Opens create at most one agent.
- `TestOlderBinaryLegacyWriteReadopted`.
- `TestMirrorWriteDoesNotTriggerReadoption`.

**Mirror and the legacy writer**
- `TestLegacyMirrorFollowsDefault`: after `SetDefaultAgent`, `SetAgent`, `TurnOffAgent`, `SetNoDefault` and `KeepOnly`, the `responder` / `responder_manual` keys equal the resolved Default.
- `TestSetResponderIsLegacyWriter`: mode setting, routing identical to today.

**Grants and history**
- `TestAdoptionLeavesGrantsHistoryParticipations`: `person_grants`, task grants, approvals, inbox/outbox `agent_id`, executor JSON, participation `AgentID` and reply-receiver `AgentID` are byte-identical.

**Execution and authorship**
- `TestUnnamedThroughDefaultIsDeviceAuthored`: answer `AgentID ""`, `inbox.agent_id ""`, `executor.via == id`, lane key `""`, `Target` nil.
- `TestDefaultChatSessionStillResumes`.
- `TestDefaultResolvedInClaimTransaction`.
- `TestMovingDefaultAppliesToNextJobOnly`.
- `TestModelReportDefaultKeyUnchanged`.
- `TestReciprocalUnchanged`.

**Turning off and resolution**
- `TestTurnOffDefaultClearsMarkAndHolds`: no fallback; the `NothingRuns` text.
- `TestDanglingMarkFailsClosed`: an older binary turned off the agent.
- `TestCorruptDefaultStateFailsClosed`.

**Program rules**
- `TestAddAgentOnePerProgram`: `ErrSameProgram`; `Another` adds to `kept`.
- `TestTurnOnNeedsAnother`.
- `TestSetAgentRefusesProgramChange`.
- `TestKeepOnlyMovesDefaultAndKeepsIdentities`, including the earlier variant and a custom name.
- `TestKeepAllConvertsEarlier`.
- `TestAddEarlierAsAgent`.
- `TestGroupsComputation`: the earlier setting forces a group; fully kept groups are silent.

**Compatibility path, status and publishing**
- `TestUseAsDefaultFourRules`: mark, move folder with note, create, refuse with guidance.
- `TestResponderChosenAdvertisesAgentAgentEnabledPerMode`.
- `TestSelfConsentViaDefaultMark`.
- `TestPublishRecordsRevision`: a lost confirmation stays unpublished.
- `TestDoctorAgentsLine`.
- `TestReviewOpeningNoDefaultText`.

### UI Go (`internal/ui`)

- `liveagentcatalog_test.go`: GET `default`/`attention`/`slots_left` for the Zenbook, unset and setting fixtures; every new action and its notes; `ErrSameProgram` refusal and the retry with `another`; `default` does not publish; the remote view is unchanged.
- `live_test.go:540-600`: the existing facade test still passes (timeout survives; manual). Add the rule-4 refusal, and GET `agent_id`/`label`/`mode`.
- `tsgen_test.go`: regenerated types.
- `classic_test.go` and `skins_browser_test.go`: rebuilt `SHA256SUMS`.

### Rendered checks (`internal/ui/testdata`, wired like `assistant_setup_rendered.cjs`)

- New `your_agents_model_check.mjs`: ordering and every string in section 6.
- New `your_agents_rendered.cjs`:
  - Zenbook: one group card; Keep only and Keep all confirms; Default moves.
  - Unset: one-click Make Default and answer-myself.
  - Setting row.
  - Turned-off section.
  - Add sheet requires the another tick; Make Default pre-ticked when unset.
  - Turning off the Default warns.
  - Settings and the Agents tab render the same component.
  - No `MyAgent`/`AssistantForm` text remains.
  - Phone: per-computer read-only groups with a Default row and no edit controls.
- Update `assistant_setup_rendered.cjs`, `assistant_setup_model_check.mjs` and `assistant_setup_skin_check.mjs` (hooks only; no "Choose your agents"), plus `comic_setup_parity_check.cjs`, `named_agent_browser_check.cjs`, `onboarding_browser_check.cjs`, `modelstatus_rendered.cjs`, `agent_chat_rendered.cjs` (no un-named local target in mode agent), `classic_contract_check.cjs` and `zoom_contract_check.cjs`.

### CLI (`cmd/agentnet`)

- New `agents_test.go`: list output for each mode; add/set/default/--none/off/on/keep-only/--earlier/keep-all/publish; the ambiguous-label error; one-per-program refusal.
- The `responder` alias outputs for all four subcommands.
- `help_test.go`: the `agents` topics.
- `join_test.go`: the hint text.
- `dm invite --agent`.
- The invite text snapshot.

### itest

- New `agents_test.go` (real binary): `responder set` on a fresh home creates the "Codex" agent marked Default, unpublished. A restart changes nothing. `agents keep-only` and `agents default` route the next un-named question to the chosen folder.
- `update_switch_test.go`: an old daemon and a new CLI on one home answer an un-named question with the same harness and folder (the mirror).
- `dmagent_test.go`: the default chat stays device-authored after the mark moves.
- `taskgrant_test.go`: grants still apply after adoption.
- `help_test.go`: list `agents`.

### Gates (AGENTS.md)

1. Focused runs first: `defaultagent`, `liveagentcatalog`, `live_test`, `agents` CLI, rendered checks.
2. Before the full run, audit the 869 `SetResponder` test sites. They keep legacy semantics, so expect zero changes. Grep for tests that count or publish catalog entries after `responder set` through the CLI or `/api/responder`.
3. Then, once: `go vet ./...`, `scripts/race-shard.sh others`, and `client-K 12` for K = 0..11. Rerun `ui-0`/`ui-1` only if UI changes after that.
4. Skipped tests are not reported as run.

## 11. Order of work, effort, open items

- **Order:**
  1. `defaultagent.go`, resolution, mirror and adoption, with client tests (about 2 days).
  2. CLI and help (0.5 day).
  3. UI Go API (0.5 day).
  4. Comic `YourAgents`, phone and hooks-only setup (1.5 days).
  5. Skins, static page and bundles (0.5 day).
  6. Docs, then one full gate (0.5 day plus run time).
- **Total:** about 5–6 developer days. The core risk is small; most of the work is UI and tests.
- **If the release gets tight, cut first:** the `ran_as` display (`Via` stays stored) and `dm invite --agent`.
- **Owner decisions left for later:**
  - (a) Allow renaming an agent that was never published (for example an auto-created "Codex 2").
  - (b) Let a phone show which named agent holds another computer's Default. That needs an additive own-device sync field behind latest-only.
  - (c) Make answers to requests that name no agent carry the Default's `agent_id` (the section 4 checklist).
- **Known limits, recorded in M-notes:**
  - Un-named and named requests to the same agent may run at the same time in one folder, as today.
  - Auto-created agents count toward the 32-name limit for good, as turned-off ones do.
  - The "unpublished" flag only reflects changes made since adoption; a publish that failed before the upgrade is not detected.