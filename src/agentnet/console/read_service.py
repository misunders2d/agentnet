"""Authorization-first projections over AgentNet's existing domain state."""

from __future__ import annotations

import json
import time
from collections.abc import Callable
from typing import Any

from agentnet.console.models import (
    ActivityPage,
    ActivitySummary,
    ApprovalPage,
    ApprovalSummary,
    HarnessSummary,
    HomeSummary,
    PersonPage,
    PersonSummary,
    RelationshipSummary,
    SecurityIssue,
    SecurityPage,
    ServerPage,
    ServerSummary,
    VisibleState,
)
from agentnet.identity.actors import VerifiedActor
from agentnet.storage.backend import StoreBackend


_CAPABILITY_LABELS = {
    "offline_custody": "Offline delivery",
    "artifact_storage": "Artifact storage",
    "relay": "Message relay",
    "store_and_forward": "Store and forward",
    "a2a_gateway": "Public A2A gateway",
    "federation": "Federation",
    "effect_executor": "Business effect execution",
    "local_binding": "Local manager binding",
}

_ACTION_LABELS = {
    "harness.revoked": "Removed laptop access",
    "harness.revocation_reconfirmed": "Confirmed removed laptop access",
    "console.session.opened": "Signed in",
    "console.mutation.requested": "Requested administrator action",
    "console.enrollment.requested": "Started laptop enrollment",
    "incident.mode_changed": "Changed incident protection",
    "credential.rotated": "Rotated a credential",
    "credential.recovery_started": "Started credential recovery",
}


class ConsoleReadService:
    def __init__(
        self,
        *,
        store: StoreBackend,
        require: Callable[..., None],
        clock: Callable[[], int] | None = None,
    ) -> None:
        self.store = store
        self.require = require
        self.clock = clock or (lambda: int(time.time()))

    def _authorize(self, actor: VerifiedActor, view: str) -> None:
        self.require(
            actor=actor,
            action=f"console.{view}.read",
            resource=f"console-domain:{actor.domain_id}",
        )

    def home(self, *, actor: VerifiedActor) -> HomeSummary:
        self._authorize(actor, "home")
        now = self.clock()
        servers = self._servers(actor.domain_id, now=now, include_technical=False)
        people_total = self.store.fetch_one(
            "SELECT COUNT(*) AS count FROM principals WHERE domain_id=? AND status!='revoked'",
            (actor.domain_id,),
        )
        agents_total = self.store.fetch_one(
            "SELECT COUNT(*) AS count FROM harnesses WHERE domain_id=? AND status!='revoked'",
            (actor.domain_id,),
        )
        waiting = self.store.fetch_one(
            """SELECT
                 (SELECT COUNT(*) FROM console_mutations WHERE domain_id=? AND state='waiting_approval') +
                 (SELECT COUNT(*) FROM console_enrollment_intents WHERE domain_id=? AND state='waiting_approval')
                 AS count""",
            (actor.domain_id, actor.domain_id),
        )
        issue_count = self._security_issue_count(actor.domain_id, now=now)
        online = sum(server.state is VisibleState.ONLINE for server in servers)
        healthy = issue_count == 0 and online == len(servers)
        return HomeSummary(
            state=VisibleState.ONLINE if healthy else VisibleState.OFFLINE,
            server_total=len(servers),
            server_online=online,
            people_total=int(people_total["count"]) if people_total else 0,
            agent_total=int(agents_total["count"]) if agents_total else 0,
            approvals_waiting=int(waiting["count"]) if waiting else 0,
            security_issues=issue_count,
            fresh_at=now,
        )

    def servers(self, *, actor: VerifiedActor, include_technical: bool = False) -> ServerPage:
        self._authorize(actor, "servers")
        now = self.clock()
        return ServerPage(
            servers=self._servers(actor.domain_id, now=now, include_technical=include_technical),
            fresh_at=now,
        )

    def _servers(
        self, domain_id: str, *, now: int, include_technical: bool
    ) -> tuple[ServerSummary, ...]:
        rows = self.store.fetch_all(
            """SELECT h.*,s.contribution_json,s.contribution_digest,s.received_at,s.expires_at
               FROM harnesses h LEFT JOIN console_server_status s ON s.harness_id=h.harness_id
               WHERE h.domain_id=? AND h.kind='server-agent'
               ORDER BY h.display_name,h.harness_id""",
            (domain_id,),
        )
        servers: list[ServerSummary] = []
        for row in rows:
            status = self._json_object(row["contribution_json"])
            status_current = row["expires_at"] is not None and int(row["expires_at"]) > now
            blockers = self._string_tuple(status.get("blocker_codes"))
            capabilities = self._capabilities(row["capabilities_json"])
            if status.get("capability_digest") and status.get("capability_digest") != self._capability_digest(
                row["capabilities_json"]
            ):
                blockers = (*blockers, "Configuration mismatch")
                status_current = False
            technical = None
            if include_technical:
                technical = {
                    "Harness": str(row["harness_id"]),
                    "Status contribution": str(row["contribution_digest"] or "Unavailable"),
                }
            servers.append(
                ServerSummary(
                    harness_id=str(row["harness_id"]),
                    friendly_name=str(row["display_name"]),
                    kind="Server agent",
                    state=(
                        VisibleState.ACCESS_REMOVED
                        if row["status"] == "revoked"
                        else VisibleState.ONLINE
                        if status_current
                        else VisibleState.OFFLINE
                    ),
                    last_checked_at=(int(row["received_at"]) if row["received_at"] is not None else None),
                    capabilities=capabilities,
                    blockers=blockers,
                    access_state=self._access_label(str(row["status"])),
                    technical=technical,
                )
            )
        return tuple(servers)

    def people(self, *, actor: VerifiedActor, include_technical: bool = False) -> PersonPage:
        self._authorize(actor, "people")
        now = self.clock()
        rows = self.store.fetch_all(
            """SELECT p.principal_id,p.domain_id,p.verified_email,p.status AS principal_status,
                      h.harness_id,h.display_name,h.kind,h.status AS harness_status,
                      h.credential_epoch,c.credential_id,c.status AS credential_status,c.expires_at
               FROM principals p
               LEFT JOIN harnesses h ON h.principal_id=p.principal_id AND h.domain_id=p.domain_id
               LEFT JOIN credentials c ON c.harness_id=h.harness_id AND c.epoch=h.credential_epoch
               WHERE p.domain_id=?
               ORDER BY p.verified_email,p.principal_id,h.display_name,h.harness_id""",
            (actor.domain_id,),
        )
        grouped: dict[str, dict[str, Any]] = {}
        for row in rows:
            principal_id = str(row["principal_id"])
            group = grouped.setdefault(
                principal_id,
                {
                    "domain_id": str(row["domain_id"]),
                    "display_name": str(row["verified_email"]),
                    "access_state": self._access_label(str(row["principal_status"])),
                    "harnesses": [],
                },
            )
            if row["harness_id"] is None:
                continue
            expires_at = int(row["expires_at"]) if row["expires_at"] is not None else None
            credential_state = self._credential_label(
                status=str(row["credential_status"] or "missing"),
                expires_at=expires_at,
                now=now,
            )
            technical = None
            if include_technical:
                technical = {
                    "Harness": str(row["harness_id"]),
                    "Credential": str(row["credential_id"] or "Unavailable"),
                }
            group["harnesses"].append(
                HarnessSummary(
                    harness_id=str(row["harness_id"]),
                    friendly_name=str(row["display_name"]),
                    kind=self._kind_label(str(row["kind"])),
                    access_state=self._access_label(str(row["harness_status"])),
                    credential_state=credential_state,
                    credential_expires_at=expires_at,
                    can_remove=(
                        row["harness_status"] == "active"
                        and str(row["harness_id"]) != actor.harness_id
                        and str(row["kind"]) != "server-agent"
                    ),
                    technical=technical,
                )
            )
        people = tuple(
            PersonSummary(
                principal_id=principal_id,
                domain_id=value["domain_id"],
                display_name=value["display_name"],
                access_state=value["access_state"],
                harnesses=tuple(value["harnesses"]),
            )
            for principal_id, value in grouped.items()
        )
        return PersonPage(people=people, relationships=self._relationships(actor, now=now), fresh_at=now)

    def _relationships(self, actor: VerifiedActor, *, now: int) -> tuple[RelationshipSummary, ...]:
        rows = self.store.fetch_all(
            """SELECT r.relationship_id,r.administrator_harness_id,r.subordinate_harness_id,
                      r.assignment_scope_json,r.state,r.relationship_expires_at,
                      ah.display_name AS administrator_name,sh.display_name AS subordinate_name
               FROM relationship_governance_transactions r
               JOIN harnesses ah ON ah.harness_id=r.administrator_harness_id
               JOIN harnesses sh ON sh.harness_id=r.subordinate_harness_id
               WHERE r.domain_id=? AND (
                   r.administrator_harness_id=? OR r.subordinate_harness_id=?
               ) ORDER BY r.relationship_expires_at,r.relationship_id""",
            (actor.domain_id, actor.harness_id, actor.harness_id),
        )
        result: list[RelationshipSummary] = []
        for row in rows:
            administrator = str(row["administrator_harness_id"]) == actor.harness_id
            scopes = self._json_value(row["assignment_scope_json"])
            scope = ", ".join(str(item) for item in scopes) if isinstance(scopes, list) else "Assigned work"
            state = str(row["state"])
            if state == "active" and int(row["relationship_expires_at"]) <= now:
                state = "expired"
            result.append(
                RelationshipSummary(
                    relationship_id=str(row["relationship_id"]),
                    direction="Administrator of" if administrator else "Managed by",
                    person=str(row["subordinate_name"] if administrator else row["administrator_name"]),
                    scope=scope,
                    state=state.replace("_", " ").title(),
                    expires_at=int(row["relationship_expires_at"]),
                )
            )
        return tuple(result)

    def approvals(self, *, actor: VerifiedActor) -> ApprovalPage:
        self._authorize(actor, "approvals")
        now = self.clock()
        approvals: list[ApprovalSummary] = []
        mutations = self.store.fetch_all(
            """SELECT mutation_id,mutation_kind,resource,request_json,state,expires_at
               FROM console_mutations WHERE domain_id=? AND state IN ('prepared','waiting_approval','unknown')
               ORDER BY created_at,mutation_id""",
            (actor.domain_id,),
        )
        for row in mutations:
            request = self._json_object(row["request_json"])
            approvals.append(
                ApprovalSummary(
                    request_id=str(row["mutation_id"]),
                    title=self._mutation_title(str(row["mutation_kind"])),
                    person=str(request.get("person", "Current network")),
                    harness=str(request["harness_name"]) if request.get("harness_name") else None,
                    capabilities=self._string_tuple(request.get("capabilities")),
                    consequence=str(request.get("consequence", "The reviewed access change will be applied.")),
                    state=self._pending_state(str(row["state"]), int(row["expires_at"]), now=now),
                    expires_at=int(row["expires_at"]),
                    action_path=f"/mutations/{row['mutation_id']}/reconcile",
                    action_confirmation="Apply this approved action",
                    action_label="Check approval and apply",
                )
            )
        enrollments = self.store.fetch_all(
            """SELECT intent_id,target_kind,request_json,state,expires_at FROM console_enrollment_intents
               WHERE domain_id=? AND state IN ('waiting_target','candidate_verified','waiting_approval','unknown')
               ORDER BY created_at,intent_id""",
            (actor.domain_id,),
        )
        for row in enrollments:
            request = self._json_object(row["request_json"])
            approvals.append(
                ApprovalSummary(
                    request_id=str(row["intent_id"]),
                    title="Enroll a laptop",
                    person=str(request.get("person", "Invited person")),
                    harness=str(request.get("harness_name", "New laptop")),
                    capabilities=self._string_tuple(request.get("capabilities")),
                    consequence="The named laptop can join only after identity, device possession, and passkey approval complete.",
                    state=self._pending_state(str(row["state"]), int(row["expires_at"]), now=now),
                    expires_at=int(row["expires_at"]),
                    action_path=(
                        f"/enrollments/{row['intent_id']}/request-approval"
                        if row["state"] == "candidate_verified"
                        else f"/enrollments/{row['intent_id']}/reconcile"
                        if row["state"] == "waiting_approval"
                        else None
                    ),
                    action_confirmation=(
                        "Request passkey approval"
                        if row["state"] == "candidate_verified"
                        else "Issue this approved invitation"
                        if row["state"] == "waiting_approval"
                        else None
                    ),
                    action_label=(
                        "Request passkey approval"
                        if row["state"] == "candidate_verified"
                        else "Check approval and issue invitation"
                        if row["state"] == "waiting_approval"
                        else None
                    ),
                )
            )
        return ApprovalPage(approvals=tuple(approvals), fresh_at=now)

    def security(self, *, actor: VerifiedActor) -> SecurityPage:
        self._authorize(actor, "security")
        now = self.clock()
        issues = self._security_issues(actor.domain_id, now=now)
        control = self.store.fetch_one(
            "SELECT mode FROM domain_incident_controls WHERE domain_id=?", (actor.domain_id,)
        )
        audit_healthy, _ = self.store.verify_audit_chain()
        return SecurityPage(
            issues=issues,
            incident_mode=self._incident_label(str(control["mode"]) if control else "normal"),
            audit_healthy=audit_healthy,
            fresh_at=now,
        )

    def activity(self, *, actor: VerifiedActor, include_technical: bool = False) -> ActivityPage:
        self._authorize(actor, "activity")
        now = self.clock()
        rows = self.store.fetch_all(
            "SELECT sequence,occurred_at,record_json,record_hash FROM audit_log ORDER BY sequence DESC LIMIT 100"
        )
        events: list[ActivitySummary] = []
        for row in rows:
            record = self._json_object(row["record_json"])
            if record.get("domain_id") not in {None, actor.domain_id}:
                continue
            action_code = str(record.get("action", ""))
            harness_id = record.get("harness_id") or record.get("actor_harness_id")
            principal_id = record.get("principal_id") or record.get("actor_principal_id")
            resource = record.get("resource") or record.get("resource_id") or harness_id or "Administration console"
            events.append(
                ActivitySummary(
                    event_id=f"audit-{int(row['sequence'])}",
                    occurred_at=int(row["occurred_at"]),
                    actor=(
                        f"Person {str(principal_id)[:12]}" if principal_id else "Administrator"
                    ),
                    action=_ACTION_LABELS.get(action_code, "Administrative activity"),
                    resource=str(resource),
                    result="Could not complete" if record.get("outcome") in {"denied", "failed"} else "Completed",
                    server=str(harness_id) if harness_id else None,
                    technical=(
                        {"Audit digest": str(row["record_hash"]), "Action code": action_code or "unknown"}
                        if include_technical
                        else None
                    ),
                )
            )
        return ActivityPage(events=tuple(events), fresh_at=now)

    def _security_issue_count(self, domain_id: str, *, now: int) -> int:
        return len(self._security_issues(domain_id, now=now))

    def _security_issues(self, domain_id: str, *, now: int) -> tuple[SecurityIssue, ...]:
        issues: list[SecurityIssue] = []
        credentials = self.store.fetch_all(
            """SELECT c.credential_id,c.expires_at,c.status,h.display_name,h.harness_id
               FROM credentials c JOIN harnesses h ON h.harness_id=c.harness_id
               WHERE h.domain_id=? AND (c.status!='active' OR c.expires_at<=?)
               ORDER BY c.expires_at,c.credential_id""",
            (domain_id, now + 7 * 86_400),
        )
        for row in credentials:
            state = (
                VisibleState.ACCESS_REMOVED
                if row["status"] == "revoked"
                else VisibleState.EXPIRED
                if int(row["expires_at"]) <= now
                else VisibleState.EXPIRES_SOON
            )
            issues.append(
                SecurityIssue(
                    issue_id=f"credential:{row['credential_id']}",
                    title=f"{row['display_name']} credential {state.value.casefold()}",
                    description="Review this exact laptop or agent before restoring access.",
                    state=state,
                    occurred_at=int(row["expires_at"]),
                    action_path=f"/people#harness-{row['harness_id']}",
                )
            )
        enrollments = self.store.fetch_all(
            """SELECT intent_id,state,updated_at FROM console_enrollment_intents
               WHERE domain_id=? AND state IN ('blocked','failed','unknown') ORDER BY updated_at,intent_id""",
            (domain_id,),
        )
        for row in enrollments:
            state = {
                "blocked": VisibleState.BLOCKED,
                "failed": VisibleState.FAILED,
                "unknown": VisibleState.UNKNOWN,
            }[str(row["state"])]
            issues.append(
                SecurityIssue(
                    issue_id=f"enrollment:{row['intent_id']}",
                    title=f"Laptop enrollment {state.value.casefold()}",
                    description="Review the enrollment request; no access was inferred from an uncertain result.",
                    state=state,
                    occurred_at=int(row["updated_at"]),
                    action_path="/approvals",
                )
            )
        audit_healthy, _ = self.store.verify_audit_chain()
        if not audit_healthy:
            issues.append(
                SecurityIssue(
                    issue_id="audit-chain",
                    title="Activity record needs attention",
                    description="Protected administrator actions remain blocked until the activity record is healthy.",
                    state=VisibleState.BLOCKED,
                    occurred_at=now,
                    action_path="/activity",
                )
            )
        return tuple(issues)

    @staticmethod
    def _json_value(value: Any) -> Any:
        if not isinstance(value, str):
            return None
        try:
            return json.loads(value)
        except (json.JSONDecodeError, UnicodeError):
            return None

    @classmethod
    def _json_object(cls, value: Any) -> dict[str, Any]:
        decoded = cls._json_value(value)
        return decoded if isinstance(decoded, dict) else {}

    @staticmethod
    def _string_tuple(value: Any) -> tuple[str, ...]:
        if not isinstance(value, (list, tuple)):
            return ()
        return tuple(str(item) for item in value if isinstance(item, str))

    @classmethod
    def _capabilities(cls, raw: Any) -> tuple[str, ...]:
        value = cls._json_object(raw)
        capabilities = cls._string_tuple(value.get("server_agent_capabilities"))
        return tuple(_CAPABILITY_LABELS.get(item, item.replace("_", " ").title()) for item in capabilities)

    @staticmethod
    def _capability_digest(raw: Any) -> str:
        import hashlib

        return hashlib.sha256(str(raw or "{}").encode("utf-8")).hexdigest()

    @staticmethod
    def _kind_label(value: str) -> str:
        labels = {
            "server-agent": "Server agent",
            "pi": "Pi agent",
            "codex": "Codex agent",
            "claude": "Claude agent",
            "laptop": "Laptop",
        }
        return labels.get(value, value.replace("_", " ").replace("-", " ").title())

    @staticmethod
    def _access_label(value: str) -> str:
        return {
            "active": "Active",
            "pending": "Waiting for approval",
            "revoked": "Access removed",
            "quarantined": "Blocked",
            "deterministic_only": "Limited",
        }.get(value, "Unknown")

    @staticmethod
    def _credential_label(*, status: str, expires_at: int | None, now: int) -> str:
        if status == "revoked":
            return "Access removed"
        if status != "active" or expires_at is None:
            return "Could not verify"
        if expires_at <= now:
            return "Expired"
        if expires_at <= now + 7 * 86_400:
            return "Expires soon"
        return "Active"

    @staticmethod
    def _incident_label(value: str) -> str:
        return {
            "normal": "Normal",
            "freeze_new_authority": "New access paused",
            "freeze_privileged": "Administrator actions paused",
            "freeze_all": "All protected activity paused",
        }.get(value, "Unknown")

    @staticmethod
    def _mutation_title(value: str) -> str:
        return {
            "harness_revoke": "Remove laptop access",
            "credential_rotation_start": "Rotate a credential",
            "credential_recovery_start": "Recover a credential",
            "entitlement_issue": "Grant access",
            "entitlement_revoke": "Remove granted access",
            "incident_set": "Change incident protection",
        }.get(value, "Administrator action")

    @staticmethod
    def _pending_state(value: str, expires_at: int, *, now: int) -> VisibleState:
        if expires_at <= now:
            return VisibleState.EXPIRED
        if value == "waiting_approval":
            return VisibleState.WAITING_APPROVAL
        if value == "unknown":
            return VisibleState.UNKNOWN
        return VisibleState.WAITING_SERVER


__all__ = ["ConsoleReadService"]
