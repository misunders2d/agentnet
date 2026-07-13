# G04 A2A TCK alpha2 HTTP+JSON classification

This is a non-green official TCK run, not an A2A certification or waiver.

- TCK: official `a2aproject/a2a-tck` tag `1.0.0.alpha2`
- SUT: ordinary `create_app` composition, `local_conformance` profile, native `DurableA2ARuntime`
- Command: `TCK_STREAMING_TIMEOUT=2.0 .venv/bin/python ./run_tck.py --sut-host http://127.0.0.1:18995/a2a/TCKAgentNetFinalRouteToken_20260713 --transport http_json --level must -v`
- JUnit: 235 selected; 46 passed, 12 failed, 177 skipped, 0 errors
- Failure outcomes classified: 12
- Skip outcomes exhaustively adjudicated: yes
- Official gate green: **no**

The preceding run exposed a real invalid-task-reference defect in
`CORE-MULTI-004`: an inaccessible `message.taskId` created a replacement
proposal. The runtime now resolves the exact peer-scoped task before
persistence, returns native `TaskNotFound`/404, and leaves no task residue.
That case passed this run and the checked-in A2A regression suite is 56/56.

The 12 remaining official failures are not waived:

1. Four `DM-ART-001` cases and one `DM-MSG-001` case require the executor
   behavior documented by this TCK, including message-ID-prefix selection of
   exact fixture outputs. AgentNet intentionally does not implement that
   fixture-specific executor because its production runtime is prefix-agnostic
   and its artifact URLs must pass corporate quarantine/release. This is an
   explicit compatibility gap even though adopting the fixture behavior would
   conflict with AgentNet's security profile.
2. `CORE-SEND-003` receives AgentNet's `ContentTypeNotSupported` response, while the
   TCK requirement runner does not accept that outcome for this case. It
   remains an official failed result.
3. `CORE-EXECUTION-MODE-001`, `CORE-EXECUTION-MODE-002`,
   `CORE-MULTI-001a`, `CORE-MULTI-002a`, and `CORE-MULTI-003` reuse the same
   message identifier with different request digests. AgentNet rejects conflicting
   idempotency reuse; the official cases expect continued execution. These are
   explicit conformance/security-profile conflicts and remain failed.
4. `TestRestStreaming::test_streaming_content_type` raises
   `httpx.ResponseNotRead` while handling AgentNet's disabled-streaming
   `UnsupportedOperation` response. Regardless of client-side cause, the
   official result remains failed.

All 177 skip outcomes were exhaustively grouped by their exact JUnit skip
message; the counts below sum to 177:

| Count | Exact reviewed reason group | Adjudication |
|---:|---|---|
| 44 | gRPC not configured or filtered by `--transport` | Outside this HTTP+JSON invocation. No gRPC result is promoted. |
| 60 | JSON-RPC not configured or filtered by `--transport` | Outside this HTTP+JSON invocation. No JSON-RPC result is promoted. |
| 28 | streaming unsupported or not advertised | Truthful disabled optional capability; the streaming matrix remains absent evidence. |
| 30 | push notifications unsupported | Truthful disabled optional capability; no push behavior is claimed. |
| 6 | authenticated extended Agent Card capability not advertised | Disabled optional capability; no extended-card behavior is claimed. |
| 2 | synthetic TCK required-extension fixture not advertised | Fixture-only extension is not part of AgentNet's production profile. |
| 7 | fixture expected COMPLETED or INPUT_REQUIRED but AgentNet returned SUBMITTED | Executor-profile compatibility gap; these are non-executed assertions, not passes. |

Reviewing a skip does not turn it into a pass or waive the missing binding or
capability. The 12 failures still make G04 non-green, and the 170 transport or
disabled-capability skips plus seven executor-precondition skips still prevent
a complete feature/binding claim. Per-outcome exact messages, counts, failure
records, and report hashes are in `manifest.json`; the exact generated reports
are retained beside it and are not release certification.
