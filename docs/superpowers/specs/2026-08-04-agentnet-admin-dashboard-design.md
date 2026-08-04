# AgentNet administration dashboard design

**Date:** 2026-08-04  
**Status:** Design approved in conversation; implementation not started

## 1. Purpose

AgentNet needs a private, browser-based administration dashboard for ordinary network operators. The dashboard must make enrollment, access, server visibility, and security state understandable to non-technical users.

The dashboard represents one AgentNet trust network containing any number of independently enrolled ordinary server agents. It must never create, display, or imply a privileged `Hub` identity. A server is an enrolled server-agent harness with explicit capabilities, health, and lifecycle state.

The first release is an **operations control** surface. It covers enrollment/invitations, credentials and revocation, people and authority visibility, incidents and audit, and a read-only server fleet view. Conversations, rooms, tasks, files, artifact workflows, federation administration, and server lifecycle controls are out of scope for the first release.

## 2. User-facing language

The normal interface uses plain operational language:

- **Home**
- **Servers**
- **People**
- **Approvals**
- **Security**
- **Activity**

Visible status vocabulary includes `Online`, `Offline`, `Waiting for approval`, `Access removed`, `Expires soon`, `Could not complete`, `Waiting for server`, and `Updated just now`.

The interface must not expose OIDC, DPoP, credential epochs, proof-binding, database, protocol, or internal schema terminology in ordinary workflows. Technical identifiers and protocol details may appear in a muted, collapsed **Technical details** area for authorized administrators. Private keys, reusable tokens, approval secrets, protected message content, and credential-bearing URLs never appear in the dashboard.

Sign-in is presented as **Sign in**. Human confirmation is presented as **Approve with passkey**. Provider and protocol names remain implementation details unless an administrator explicitly opens technical details.

## 3. Main screens

### Home

Home summarizes the network:

- servers online/offline;
- enrolled people and agents;
- approvals waiting for the administrator;
- security issues requiring attention;
- freshness timestamps.

Home must show a clear healthy/degraded state without requiring technical interpretation.

### Servers

Servers lists every visible server-agent in the network. Each row/card includes:

- administrator-friendly name;
- online/offline/recent/stale state;
- last-checked or last-seen time;
- human-readable capabilities such as message delivery or offline delivery;
- capability or service blockers;
- credential/access state;
- optional muted technical details.

The first release is read-only for server fleet lifecycle. It does not add drain, maintenance, quarantine, capability mutation, or server revocation controls. Revoking a person or one of their laptop/agent harnesses remains available through the People and Security workflows.

### People

People shows verified human principals and their enrolled laptops/agents. A person may have multiple sibling harnesses. The screen must distinguish:

- human;
- exact laptop or agent;
- server-agent instance;
- access state;
- credential expiry or recovery state.

Revoking one harness must not revoke sibling harnesses or the human principal unless a separate explicitly authorized action does so.

People also shows current administrator/subordinate relationships with their scope, expiry, and revocation state. Relationship creation, modification, renewal, and removal are not first-release dashboard controls.

### Approvals

Approvals lists pending administrator actions in plain language. Each item identifies:

- what is being requested;
- who it is for;
- which laptop/agent/server is involved;
- requested access/capabilities;
- expiration;
- the exact consequence of approval.

Secrets, private continuation values, claim codes, receipts, or raw approval capabilities are never displayed.

### Security

Security shows actionable security state:

- expired or expiring credentials;
- removed access;
- failed or blocked enrollment;
- replay, stale, or authorization failures summarized without protected content;
- incident mode and audit-health blockers.

### Activity

Activity shows redacted audit events useful to an authorized administrator. It must preserve attribution, time, action, resource scope, result, and relevant server-agent identity without exposing protected content or unnecessary raw identity data.

### Interaction and accessibility

The first release is desktop-first but remains usable on narrow screens without hiding required status or controls. Every workflow must be keyboard operable, expose programmatic names and error messages, retain visible focus, and meet WCAG 2.2 AA contrast and target-size expectations. Status and risk are never communicated by color alone.

Forms preserve non-sensitive user input after a recoverable validation error. Destructive or access-removing actions use explicit verbs and consequences, not generic **Confirm** buttons. Success, pending, and uncertain states remain distinguishable to screen readers and sighted users.

## 4. Enrollment flows

The dashboard must support both enrollment targets:

1. **Add another laptop** for an existing person.
2. **Invite someone new** and enroll that person’s laptop.

The user-facing flow is:

1. Select **Enroll a laptop**.
2. Choose an existing person or **Invite someone new**.
3. Enter a friendly laptop/agent name.
4. Select the allowed capabilities from the options available to the administrator.
5. Review the exact person, device, capabilities, expiry, and consequences.
6. Start the enrollment request.
7. The target laptop completes AgentNet’s normal proof-of-possession flow.
8. The authorized human approves the exact transaction with a passkey when required.
9. The dashboard tracks **Waiting**, **Enrolled**, **Expired**, **Canceled**, **Blocked**, **Failed**, or **Unknown** honestly.

A dashboard click cannot enroll a laptop by itself. The target device must prove possession of its credential/key, and the required human approval must complete. The dashboard must not invent a QR, private URL, claim code, copy/paste receipt, or alternate ceremony if the installed AgentNet release does not provide one.

Enrollment is not restricted to a privileged Hub. Any ordinary server-agent with the explicit enrollment capability may handle an enrollment request. The dashboard may show which server handled it, but must not call that server Hub or grant it additional authority.

## 5. Credentials, revocation, and authority

Routine read actions require an authenticated administrator session with the relevant read entitlement. Sensitive actions require the relevant positive human authority, exact resource scope, current policy, and appropriate confirmation.

Sensitive dashboard actions include:

- inviting a new person;
- enrolling another laptop/agent;
- removing a laptop/agent harness or a person’s access;
- starting credential rotation or recovery;
- changing a person’s laptop/agent capabilities;
- acknowledging or resolving an incident.

The dashboard must display a review step before each sensitive mutation. Fresh passkey approval is required according to the operation’s risk class. The dashboard itself does not become an authority source: every mutation uses existing verified actor, policy, proof, audit, expiry, and revocation semantics.

Until accountable-owner evidence defines narrower risk classes, the fail-closed default requires fresh passkey approval for every sensitive dashboard mutation. No dashboard-only exception, remembered approval, or role label may bypass that confirmation.

Management relationships do not transfer a person’s data permissions. An administrator can see or control only the resources and actions granted by current policy.

## 6. Multi-server network model

The domain model contains separate records for:

- network/trust domain;
- server-agent instance and exact enrolled harness;
- human principal;
- laptop/agent harness;
- credential;
- capability set;
- presence/health lease;
- enrollment/invitation request;
- audit/incident state.

There is no `primary_server`, `hub_identity`, or implicit superuser field. A server’s capability set narrows what it can do; its name or role label cannot create authority.

The dashboard aggregates server status through an authenticated network control/read model. It does not read databases directly and does not hold reusable server credentials in the browser. Each server contributes only signed/authorized status and capability information. An action is routed only to a server currently eligible for that action.

If no eligible server is available, the UI shows **Waiting for server** or a named blocked/error state. A transport response, server receipt, or A2A task state does not become a dashboard claim of enrollment, processing, or business completion.

## 7. Architecture boundary

The dashboard uses a dedicated console origin and a narrow dashboard backend. This is an implementation boundary, not a user-facing product concept.

An administrator session is established only for an existing verified human-plus-harness actor. Workforce sign-in alone is insufficient. The enrolled manager harness proves a short-lived, purpose-bound challenge containing the console audience, trust domain, session identifier, nonce, expiry, and current credential epoch; the backend verifies that proof and current revocation state before binding the browser session to the exact human principal and harness. The ordinary UI may say **Open from AgentNet** rather than expose this mechanism. Direct browser navigation without current harness proof can show the sign-in page but cannot create an authorized administrator session.

The resulting browser credential is an opaque, short-lived, rotated, `Secure`, `HttpOnly`, host-only, `SameSite=Strict` session cookie. Mutating requests also require a session-bound anti-CSRF value and exact method/path/body binding at the backend boundary. The browser never receives the harness private key, a Core credential, an approval continuation, or a reusable server credential.

The browser receives:

- a normal authenticated administrator session;
- redacted read models;
- status/freshness events;
- purpose-specific mutation results.

The dashboard backend:

- resolves the administrator through the existing verified identity boundary;
- requests read models from AgentNet Core/domain services;
- translates user actions into existing proof-bound authorization and approval operations;
- aggregates multiple ordinary server-agent records;
- streams status updates with bounded polling/catch-up fallback;
- stores no private keys, reusable tokens, or browser-side authority secrets.

The dashboard must not bypass Core, Approval, audit, identity, policy, or server-agent capability checks. It must not directly mutate PostgreSQL tables or reconstruct caller identity from browser JSON, email strings, role labels, or display names.

The first release uses server-rendered semantic HTML and ordinary forms, with small package-owned JavaScript modules only for live updates and progressive enhancement. It does not introduce a single-page application framework, third-party scripts, remote fonts, analytics, or browser storage for sensitive state. Responses use a restrictive Content Security Policy and prevent framing.

Existing AgentNet identity, Approval, authorization, audit, presence, enrollment, and revocation services remain the semantic owners. The dashboard adds only the console session boundary, redacted read models, presentation, and routing needed for this interface; it does not build a second identity, policy, approval, or presence engine.

## 8. Freshness and failure behavior

The preferred update path is authenticated server-sent events or an equivalent authenticated live update channel, with bounded polling/catch-up fallback. The interface displays freshness rather than pretending all values are live.

Required states include:

- **Online**;
- **Offline**;
- **Recent** or **Stale** where applicable;
- **Waiting for server**;
- **Waiting for approval**;
- **Completed**;
- **Failed**;
- **Expired**;
- **Canceled**;
- **Unknown — needs reconciliation**.

A lost response must not cause duplicate enrollment, duplicate revocation, or false success. Retries use idempotency and existing durable state. Unknown outcomes remain visible until reconciled.

## 9. Verification requirements

Browser and service verification must prove:

- a single network displays multiple servers;
- servers with different capabilities render correctly;
- one offline server does not hide or mislabel other servers;
- existing-person laptop enrollment can be started from the dashboard;
- new-person invitation and laptop enrollment can be started from the dashboard;
- target proof-of-possession and required human approval are necessary;
- one harness can be revoked without revoking sibling harnesses;
- expired, blocked, canceled, failed, and unknown enrollment states remain visible;
- unauthorized administrators cannot access restricted views or controls;
- workforce sign-in without current manager-harness proof cannot create an administrator session;
- mismatched domain/audience, stale credential epoch, revoked harness, expired challenge, replayed challenge, or changed session binding fails closed;
- browser session cookies and protected values are not exposed to JavaScript, URLs, browser storage, or cross-site requests;
- secrets and protected content are absent from browser responses, UI, logs, and audit summaries;
- sensitive mutations produce durable redacted audit records;
- live updates and polling/catch-up produce equivalent state;
- duplicate/replayed mutation requests are idempotent or rejected;
- no server is labeled or treated as a privileged Hub;
- server fleet lifecycle mutations are absent from first-release routes and views;
- server receipts and transport responses are not misreported as processing or business-effect completion.

The end-to-end browser flow is required in addition to unit and route tests. The first implementation must not claim full AgentNet production readiness, HA, artifact readiness, federation readiness, or owner-decision completion merely because the dashboard works.

## 10. Requirements and decision dependencies

The affected stable requirement IDs are:

- `ARC-001`, `ARC-002`, `ARC-005` — separate agent-agnostic product boundary and owned internal interfaces;
- `ID-001`, `ID-002`, `ID-003`, `ID-004`, `ID-006`, `ID-007`, `ID-008`, `ID-009` — verified human-plus-harness identity, enrollment, revocation, domain scope, and credential lifecycle;
- `AUTH-001`, `AUTH-002`, `AUTH-003`, `AUTH-004`, `AUTH-005`, `AUTH-006`, `AUTH-007`, `AUTH-008`, `AUTH-009`, `AUTH-010` — verified caller, proof-derived context, human positive authority, exact harness attribution, fail-closed access, and approval policy;
- `ORG-004`, `ORG-006` — no management-to-data-authority transfer and explicit relationship lifecycle;
- `AVL-001`, `AVL-002`, `AVL-004`, `AVL-005`, `AVL-006`, `AVL-007`, `AVL-008` — multi-server topology, offline-normal behavior, honest outcomes, retries/reconciliation, failover visibility, and presence freshness;
- `UX-005` — approval and security attention policy;
- `SEC-001`, `SEC-003`, `SEC-004`, `SEC-005`, `SEC-006` — threat handling, redacted audit, minimization, freshness/replay, and containment;
- `OPS-001`, `OPS-002`, `OPS-003`, `OPS-004`, `OPS-006`, `OPS-007` — replaceable server roles, discovery, versioning, observability, portability, and verification/reuse discipline.

Accountable-owner dependencies remain `PD-001`, `PD-002`, `PD-003`, `PD-004`, `PD-005`, `PD-009`, `PD-010`, and `PD-011`. Until their required evidence exists, the dashboard uses the documented fail-closed defaults and does not imply owner approval.

Relevant release gates include `G06` identity/enrollment, `G17` owner policy, and `G19` freshness/crypto/audit roots. This design does not promote any requirement or gate to passed status. Implementation and evidence ledgers may change only after reproducible evidence at the required tier.
