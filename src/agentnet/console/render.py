"""Escaped server-rendered HTML for the private administration console."""

from __future__ import annotations

import html
import secrets
from datetime import UTC, datetime
from collections.abc import Callable, Iterable, Mapping, Sequence
from urllib.parse import quote

from agentnet.console.models import (
    ActivityPage,
    ApprovalPage,
    HomeSummary,
    PersonPage,
    SecurityPage,
    ServerPage,
    VisibleState,
)


MutationAuthorizer = Callable[[str, Mapping[str, Sequence[str]]], str]

_NAV = (
    ("Home", "/", "home"),
    ("Servers", "/servers", "servers"),
    ("People", "/people", "people"),
    ("Approvals", "/approvals", "approvals"),
    ("Security", "/security", "security"),
    ("Activity", "/activity", "activity"),
)


def _e(value: object) -> str:
    return html.escape(str(value), quote=True)


def _time(value: int | None) -> str:
    if value is None:
        return "Not yet available"
    return datetime.fromtimestamp(value, UTC).strftime("%d %b %Y, %H:%M UTC")


def _status_class(state: VisibleState | str) -> str:
    value = state.value if isinstance(state, VisibleState) else str(state)
    if value in {"Online", "Completed", "Recent", "Active"}:
        return "good"
    if value in {"Offline", "Access removed", "Failed", "Expired", "Blocked", "Could not complete"}:
        return "bad"
    if value in {"Waiting for server", "Waiting for approval", "Expires soon", "Stale"}:
        return "warn"
    return "info"


def _tags(values: Iterable[str], *, empty: str) -> str:
    items = tuple(values)
    if not items:
        return f'<p class="muted">{_e(empty)}</p>'
    return '<ul class="tags">' + "".join(f"<li>{_e(item)}</li>" for item in items) + "</ul>"


def _technical(values: dict[str, str] | None) -> str:
    if not values:
        return ""
    rows = "".join(f"<dt>{_e(key)}</dt><dd><code>{_e(value)}</code></dd>" for key, value in values.items())
    return f"<details><summary>Technical details</summary><dl class=\"meta\">{rows}</dl></details>"

class ConsoleRenderer:
    def __init__(self, *, asset_version: str, approval_origin: str | None = None) -> None:
        self.asset_version = asset_version
        self.approval_origin = approval_origin.rstrip("/") if approval_origin else None

    @staticmethod
    def _mutation_token_input(
        authorize_mutation: MutationAuthorizer,
        *,
        path: str,
        form: Mapping[str, Sequence[str]],
    ) -> str:
        token = authorize_mutation(path, form)
        return f'<input type="hidden" name="mutation_token" value="{_e(token)}">'

    def document(
        self,
        *,
        title: str,
        current_nav: str,
        body: str,
        authorize_mutation: MutationAuthorizer,
        fresh_at: int,
        revision: int = 0,
    ) -> str:
        navigation = "".join(
            f'<li><a href="{path}"{(" aria-current=\"page\"" if key == current_nav else "")}>{label}</a></li>'
            for label, path, key in _NAV
        )
        sign_out_token = authorize_mutation("/v1/console/sign-out", {})
        return (
            "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">"
            '<meta name="viewport" content="width=device-width,initial-scale=1">'
            '<meta name="referrer" content="no-referrer">'
            f"<title>{_e(title)} · AgentNet</title>"
            f'<link rel="stylesheet" href="/assets/console.css?v={_e(self.asset_version)}">'
            f'<script src="/assets/console.js?v={_e(self.asset_version)}" defer></script>'
            "</head><body>"
            '<a class="skip-link" href="#main">Skip to main content</a>'
            '<header class="site-header"><div class="header-inner">'
            '<a class="brand" href="/">AgentNet administration</a>'
            f'<nav aria-label="Primary"><div class="primary-nav"><ul>{navigation}</ul></div></nav>'
            '<form class="sign-out" method="post" action="/v1/console/sign-out">'
            f'<input type="hidden" name="mutation_token" value="{_e(sign_out_token)}">'
            '<button class="secondary" type="submit">Sign out</button></form>'
            "</div></header>"
            f'<main id="main" tabindex="-1">{body}'
            f'<p class="freshness" aria-live="polite" data-live-status data-revision="{revision}">'
            f"Updated {_e(_time(fresh_at))}</p></main></body></html>"
        )

    def mutation_review(
        self,
        *,
        title: str,
        consequence: str,
        action_path: str,
        action_label: str,
        form: Mapping[str, Sequence[str]],
        authorize_mutation: MutationAuthorizer,
        fresh_at: int,
    ) -> str:
        hidden = "".join(
            f'<input type="hidden" name="{_e(name)}" value="{_e(value)}">'
            for name in sorted(form)
            for value in form[name]
        )
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Exact action review</p>'
            f"<h1>{_e(title)}</h1></div></div>"
            f'<section class="panel"><div class="notice warning"><strong>Exact consequence:</strong> {_e(consequence)}</div>'
            f'<form method="post" action="{_e(action_path)}">'
            f'{self._mutation_token_input(authorize_mutation, path=action_path, form=form)}'
            f"{hidden}"
            f'<button class="danger" type="submit">{_e(action_label)}</button></form></section>'
        )
        return self.document(
            title=title,
            current_nav="people",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=fresh_at,
        )

    def home(self, home: HomeSummary, authorize_mutation: MutationAuthorizer) -> str:
        healthy = home.state is VisibleState.ONLINE
        state_title = (
            "Network healthy"
            if healthy
            else "Waiting for server"
            if home.state is VisibleState.WAITING_SERVER
            else "Network needs attention"
        )
        state_copy = (
            "All visible server status and security checks are current."
            if healthy
            else "No enrolled server is currently visible."
            if home.state is VisibleState.WAITING_SERVER
            else "Review offline servers, waiting approvals, and security issues below."
        )
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Operations overview</p>'
            f"<h1>{state_title}</h1><p>{state_copy}</p></div>"
            f'<span class="status {_status_class(home.state)}">{_e(home.state.value)}</span></div>'
            '<section class="summary-strip" aria-label="Network summary">'
            f'<div class="summary-item"><span class="summary-value">{home.server_online}/{home.server_total}</span><span class="summary-label">servers online</span></div>'
            f'<div class="summary-item"><span class="summary-value">{home.people_total}</span><span class="summary-label">enrolled people</span></div>'
            f'<div class="summary-item"><span class="summary-value">{home.approvals_waiting}</span><span class="summary-label">approvals waiting</span></div>'
            f'<div class="summary-item"><span class="summary-value">{home.security_issues}</span><span class="summary-label">security issues</span></div>'
            "</section>"
            '<section class="section" aria-labelledby="next-actions"><h2 id="next-actions">Next actions</h2>'
            '<div class="stack"><div class="panel"><h3>Enroll a laptop</h3>'
            '<p>Add another laptop for an existing person or invite someone new. Access is not created by this dashboard alone.</p>'
            '<a class="button" href="/people#enroll">Enroll a laptop</a></div>'
            '<div class="panel"><h3>Review current servers</h3>'
            f'<p>{home.server_total - home.server_online} server(s) are offline or stale. Each server remains independent.</p>'
            '<a class="button secondary" href="/servers">View servers</a></div></div></section>'
        )
        return self.document(
            title="Home",
            current_nav="home",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=home.fresh_at,
        )

    def servers(self, page: ServerPage, authorize_mutation: MutationAuthorizer) -> str:
        if page.servers:
            cards = []
            for server in page.servers:
                blockers = _tags(server.blockers, empty="No service blockers reported")
                cards.append(
                    '<article class="panel">'
                    '<div class="panel-header"><div>'
                    f"<h2>{_e(server.friendly_name)}</h2><p class=\"muted\">{_e(server.kind)}</p></div>"
                    f'<span class="status {_status_class(server.state)}">{_e(server.state.value)}</span></div>'
                    '<dl class="meta">'
                    f"<div><dt>Last checked</dt><dd>{_e(_time(server.last_checked_at))}</dd></div>"
                    f"<div><dt>Access</dt><dd>{_e(server.access_state)}</dd></div></dl>"
                    '<h3>Available services</h3>'
                    f'{_tags(server.capabilities, empty="No services are currently available")}'
                    '<h3>Service blockers</h3>'
                    f"{blockers}{_technical(server.technical)}</article>"
                )
            content = '<div class="stack">' + "".join(cards) + "</div>"
        else:
            content = '<div class="empty"><h2>No servers are visible</h2><p>Check enrollment and your current server-view access.</p></div>'
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Read-only fleet view</p>'
            '<h1>Servers</h1><p>Ordinary enrolled server agents are shown independently. An offline server does not hide another server.</p>'
            f"</div></div>{content}"
        )
        return self.document(
            title="Servers",
            current_nav="servers",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=page.fresh_at,
        )

    def people(
        self,
        page: PersonPage,
        authorize_mutation: MutationAuthorizer,
        *,
        enrollment_values: Mapping[str, object] | None = None,
        enrollment_error: str | None = None,
    ) -> str:
        values = enrollment_values or {}
        target_kind = str(values.get("target_kind", "existing_person"))
        target_principal_id = str(values.get("target_principal_id", ""))
        invited_email_alias = str(values.get("invited_email_alias", ""))
        harness_name = str(values.get("harness_name", ""))
        reason = str(values.get("reason", ""))
        capability_values = values.get("capabilities", ())
        selected_capabilities = (
            {
                str(value)
                for value in capability_values
                if isinstance(value, str)
            }
            if isinstance(capability_values, (tuple, list))
            else set()
        )
        people: list[str] = []
        for person in page.people:
            harnesses: list[str] = []
            for harness in person.harnesses:
                remove = ""
                if harness.can_remove:
                    phrase = f"Remove access for {harness.harness_id}"
                    remove = (
                        '<details><summary>Review access removal</summary>'
                        '<p>This removes access for this exact laptop or agent only. The person and sibling laptops remain active.</p>'
                        f'<form method="post" action="/harnesses/{quote(harness.harness_id, safe="")}/revoke/review">'
                        f'<input type="hidden" name="idempotency_key" value="{_e(secrets.token_urlsafe(24))}">'
                        '<div class="field"><label for="reason-'
                        f'{_e(harness.harness_id)}">Reason</label><textarea id="reason-{_e(harness.harness_id)}" name="reason" required maxlength="512"></textarea></div>'
                        '<div class="field"><label for="confirmation-'
                        f'{_e(harness.harness_id)}">Type “{_e(phrase)}”</label>'
                        f'<input id="confirmation-{_e(harness.harness_id)}" name="confirmation" required autocomplete="off"></div>'
                        '<button class="danger" type="submit">Remove this harness’s access</button></form></details>'
                    )
                harnesses.append(
                    f'<li class="panel" id="harness-{_e(harness.harness_id)}"><div class="panel-header">'
                    f'<div><h3>{_e(harness.friendly_name)}</h3><p class="muted">{_e(harness.kind)}</p></div>'
                    f'<span class="status {_status_class(harness.access_state)}">{_e(harness.access_state)}</span></div>'
                    '<dl class="meta">'
                    f'<div><dt>Credential</dt><dd>{_e(harness.credential_state)}</dd></div>'
                    f'<div><dt>Expires</dt><dd>{_e(_time(harness.credential_expires_at))}</dd></div></dl>'
                    f'{_technical(harness.technical)}{remove}</li>'
                )
            people.append(
                '<article class="section">'
                f'<div class="panel-header"><div><h2>{_e(person.display_name)}</h2><p class="muted">Verified person</p></div>'
                f'<span class="status {_status_class(person.access_state)}">{_e(person.access_state)}</span></div>'
                f'<ul class="stack" aria-label="Laptops and agents for {_e(person.display_name)}">{"".join(harnesses)}</ul></article>'
            )
        relationships = ""
        if page.relationships:
            rows = "".join(
                '<tr>'
                f'<td data-label="Relationship">{_e(item.direction)}</td>'
                f'<td data-label="Person">{_e(item.person)}</td>'
                f'<td data-label="Scope">{_e(item.scope)}</td>'
                f'<td data-label="State">{_e(item.state)}</td>'
                f'<td data-label="Expires">{_e(_time(item.expires_at))}</td></tr>'
                for item in page.relationships
            )
            relationships = (
                '<section class="section" aria-labelledby="relationships"><h2 id="relationships">Administration relationships</h2>'
                '<p>These relationships do not transfer the person’s data permissions.</p>'
                '<div class="table-wrap"><table><thead><tr><th>Relationship</th><th>Person</th><th>Scope</th><th>State</th><th>Expires</th></tr></thead>'
                f"<tbody>{rows}</tbody></table></div></section>"
            )
        options = "".join(
            f'<option value="{_e(person.principal_id)}"'
            f'{(" selected" if person.principal_id == target_principal_id else "")}>'
            f'{_e(person.display_name)}</option>'
            for person in page.people
            if person.access_state == "Active"
        )
        error = (
            f'<div class="notice warning" role="alert"><strong>Review the form:</strong> {_e(enrollment_error)}</div>'
            if enrollment_error
            else ""
        )
        enroll = (
            '<section class="section panel" id="enroll" aria-labelledby="enroll-title"><p class="eyebrow">Proof-bound enrollment</p>'
            '<h2 id="enroll-title">Enroll a laptop</h2>'
            '<p>Reviewing this request creates no access. The target must verify identity, prove device possession, and receive fresh passkey approval.</p>'
            f"{error}"
            '<form method="post" action="/enrollments/review">'
            f'<input type="hidden" name="idempotency_key" value="{_e(secrets.token_urlsafe(24))}">'
            '<fieldset class="field"><legend>Who will use this laptop?</legend>'
            '<label><input type="radio" name="target_kind" value="existing_person"'
            f'{(" checked" if target_kind == "existing_person" else "")}> Existing person</label>'
            '<label><input type="radio" name="target_kind" value="new_person"'
            f'{(" checked" if target_kind == "new_person" else "")}> Invite someone new</label></fieldset>'
            f'<div class="field" data-enrollment-existing{(" hidden" if target_kind != "existing_person" else "")}><label for="target-principal">Existing person</label><select id="target-principal" name="target_principal_id"><option value="">Choose a person</option>{options}</select></div>'
            f'<div class="field" data-enrollment-new{(" hidden" if target_kind != "new_person" else "")}><label for="invited-email">New person’s verified email</label>'
            f'<input id="invited-email" name="invited_email_alias" type="email" autocomplete="email" maxlength="320" value="{_e(invited_email_alias)}"></div>'
            '<div class="field"><label for="harness-name">Laptop name</label>'
            f'<input id="harness-name" name="harness_name" required maxlength="128" autocomplete="off" value="{_e(harness_name)}"></div>'
            '<fieldset class="field"><legend>Requested services</legend>'
            '<label><input type="checkbox" name="capabilities" value="message_delivery"'
            f'{(" checked" if "message_delivery" in selected_capabilities else "")}> Message delivery</label>'
            '<label><input type="checkbox" name="capabilities" value="offline_delivery"'
            f'{(" checked" if "offline_delivery" in selected_capabilities else "")}> Offline delivery</label></fieldset>'
            '<div class="field"><label for="enrollment-reason">Reason</label>'
            f'<textarea id="enrollment-reason" name="reason" required maxlength="512">{_e(reason)}</textarea></div>'
            '<div class="notice warning"><strong>Exact consequence:</strong> This review creates no access. No access is created until the target verifies identity, proves device possession, and a fresh passkey approval completes.</div>'
            '<button type="submit">Review enrollment request</button></form></section>'
        )
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Verified identities</p><h1>People</h1>'
            '<p>Each laptop and agent keeps its own access state. Removing one does not remove its siblings.</p></div></div>'
            f'{("".join(people) if people else "<div class=\"empty\"><h2>No people are visible</h2><p>Enroll a verified person to begin.</p></div>")}'
            f"{relationships}{enroll}"
        )
        return self.document(
            title="People",
            current_nav="people",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=page.fresh_at,
        )

    def enrollment_review(
        self,
        *,
        person: str,
        harness_kind: str,
        harness_name: str,
        capabilities: Sequence[str],
        reason: str,
        consequence: str,
        expires_at: int,
        review_token: str,
        authorize_mutation: MutationAuthorizer,
        fresh_at: int,
    ) -> str:
        confirmation = "Create this reviewed enrollment request"
        form = {
            "confirmation": [confirmation],
            "review_token": [review_token],
        }
        capability_labels = {
            "message_delivery": "Message delivery",
            "offline_delivery": "Offline delivery",
        }
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Exact enrollment review</p>'
            '<h1>Review enrollment request</h1>'
            '<p>No durable enrollment intent exists yet.</p></div></div>'
            '<section class="panel" aria-labelledby="reviewed-enrollment">'
            '<h2 id="reviewed-enrollment">Reviewed details</h2><dl class="meta">'
            f'<div><dt>Person</dt><dd>{_e(person)}</dd></div>'
            f'<div><dt>Device</dt><dd>{_e(harness_name)} ({_e(harness_kind)})</dd></div>'
            f'<div><dt>Expires</dt><dd>{_e(_time(expires_at))}</dd></div>'
            f'<div><dt>Reason</dt><dd>{_e(reason)}</dd></div></dl>'
            '<h3>Allowed requested services</h3>'
            f'{_tags((capability_labels.get(value, value) for value in capabilities), empty="No additional services requested")}'
            f'<div class="notice warning"><strong>Exact consequence:</strong> {_e(consequence)}</div>'
            '<form method="post" action="/enrollments">'
            f'{self._mutation_token_input(authorize_mutation, path="/enrollments", form=form)}'
            f'<input type="hidden" name="review_token" value="{_e(review_token)}">'
            f'<label><input type="checkbox" name="confirmation" value="{_e(confirmation)}" required> {_e(confirmation)}</label>'
            '<button type="submit">Create enrollment request</button></form></section>'
        )
        return self.document(
            title="Review enrollment request",
            current_nav="people",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=fresh_at,
        )

    def approvals(self, page: ApprovalPage, authorize_mutation: MutationAuthorizer) -> str:
        approval_href = (self.approval_origin + "/approval") if self.approval_origin else "/approvals"
        if page.approvals:
            items = "".join(
                '<article class="panel"><div class="panel-header"><div>'
                f'<h2>{_e(item.title)}</h2><p class="muted">For {_e(item.person)}</p></div>'
                f'<span class="status {_status_class(item.state)}">{_e(item.state.value)}</span></div>'
                f'{("<p><strong>Laptop or agent:</strong> " + _e(item.harness) + "</p>" if item.harness else "")}'
                '<h3>Requested services</h3>'
                f'{_tags(item.capabilities, empty="No additional services requested")}'
                f'<div class="notice warning"><strong>Exact consequence:</strong> {_e(item.consequence)}</div>'
                f'<p class="muted">Expires {_e(_time(item.expires_at))}</p>'
                + (
                    f'<p><a class="button secondary" href="{_e(approval_href)}">Approve with passkey</a></p>'
                    if item.state is VisibleState.WAITING_APPROVAL
                    else ""
                )
                + (
                    f'<form method="post" action="{_e(item.action_path)}">'
                    f'{self._mutation_token_input(authorize_mutation, path=item.action_path, form={"confirmation": [item.action_confirmation]})}'
                    f'<label><input type="checkbox" name="confirmation" value="{_e(item.action_confirmation)}" required> {_e(item.action_confirmation)}</label>'
                    f'<button type="submit">{_e(item.action_label)}</button></form>'
                    if item.action_path and item.action_confirmation and item.action_label
                    else ""
                )
                + "</article>"
                for item in page.approvals
            )
            content = f'<div class="stack">{items}</div>'
        else:
            content = '<div class="empty"><h2>No approvals are waiting</h2><p>New requests will appear here with their exact consequences.</p></div>'
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Human confirmation</p><h1>Approvals</h1>'
            '<p>Every sensitive action requires a fresh passkey decision. Approval cannot be remembered or inferred from a role.</p></div></div>'
            f"{content}"
        )
        return self.document(
            title="Approvals",
            current_nav="approvals",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=page.fresh_at,
        )

    def security(self, page: SecurityPage, authorize_mutation: MutationAuthorizer) -> str:
        audit = "Activity record healthy" if page.audit_healthy else "Activity record needs attention"
        issues = "".join(
            '<article class="panel"><div class="panel-header"><div>'
            f'<h2>{_e(issue.title)}</h2><p>{_e(issue.description)}</p></div>'
            f'<span class="status {_status_class(issue.state)}">{_e(issue.state.value)}</span></div>'
            f'{("<p><a href=\"" + _e(issue.action_path) + "\">Review this issue</a></p>" if issue.action_path else "")}'
            f'{("<p class=\"muted\">Observed " + _e(_time(issue.occurred_at)) + "</p>" if issue.occurred_at else "")}</article>'
            for issue in page.issues
        )
        if not issues:
            issues = '<div class="empty"><h2>No security issues need attention</h2><p>Access, credentials, enrollment, and activity checks are current.</p></div>'
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Actionable protection state</p><h1>Security</h1>'
            '<p>Risk is described with text as well as status color.</p></div></div>'
            '<section class="summary-strip" aria-label="Security summary">'
            f'<div class="summary-item"><span class="summary-value">{len(page.issues)}</span><span class="summary-label">issues</span></div>'
            f'<div class="summary-item"><span class="summary-value">{_e(page.incident_mode)}</span><span class="summary-label">incident protection</span></div>'
            f'<div class="summary-item"><span class="summary-value">{_e(audit)}</span><span class="summary-label">activity integrity</span></div>'
            '</section><section class="stack" aria-label="Security issues">'
            f"{issues}</section>"
        )
        return self.document(
            title="Security",
            current_nav="security",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=page.fresh_at,
        )

    def activity(self, page: ActivityPage, authorize_mutation: MutationAuthorizer) -> str:
        if page.events:
            rows = "".join(
                '<tr>'
                f'<td data-label="Time">{_e(_time(event.occurred_at))}</td>'
                f'<td data-label="Actor">{_e(event.actor)}</td>'
                f'<td data-label="Action">{_e(event.action)}</td>'
                f'<td data-label="Scope">{_e(event.resource)}</td>'
                f'<td data-label="Result"><span class="status {_status_class(event.result)}">{_e(event.result)}</span></td>'
                f'<td data-label="Server">{_e(event.server or "—")}{_technical(event.technical)}</td></tr>'
                for event in page.events
            )
            content = (
                '<div class="table-wrap"><table><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Scope</th><th>Result</th><th>Server</th></tr></thead>'
                f"<tbody>{rows}</tbody></table></div>"
            )
        else:
            content = '<div class="empty"><h2>No activity is visible</h2><p>Authorized, redacted administrator actions will appear here.</p></div>'
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Redacted audit view</p><h1>Activity</h1>'
            '<p>Attribution, action, scope, result, and server are shown without protected content.</p></div></div>'
            f"{content}"
        )
        return self.document(
            title="Activity",
            current_nav="activity",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=page.fresh_at,
        )


__all__ = ["ConsoleRenderer"]
