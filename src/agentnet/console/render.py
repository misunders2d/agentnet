"""Escaped server-rendered HTML for the private administration console."""

from __future__ import annotations

import html
import secrets
from datetime import UTC, datetime
from typing import Iterable
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

    def document(
        self,
        *,
        title: str,
        current_nav: str,
        body: str,
        csrf_token: str,
        fresh_at: int,
        revision: int = 0,
    ) -> str:
        navigation = "".join(
            f'<li><a href="{path}"{(" aria-current=\"page\"" if key == current_nav else "")}>{label}</a></li>'
            for label, path, key in _NAV
        )
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
            f'<nav class="primary-nav" aria-label="Primary"><ul>{navigation}</ul></nav>'
            '<form class="sign-out" method="post" action="/v1/console/sign-out">'
            f'<input type="hidden" name="csrf_token" value="{_e(csrf_token)}">'
            '<button class="secondary" type="submit">Sign out</button></form>'
            "</div></header>"
            f'<main id="main" tabindex="-1">{body}'
            f'<p class="freshness" aria-live="polite" data-live-status data-revision="{revision}">'
            f"Updated {_e(_time(fresh_at))}</p></main></body></html>"
        )

    def home(self, home: HomeSummary, csrf_token: str) -> str:
        healthy = home.state is VisibleState.ONLINE
        state_title = "Network healthy" if healthy else "Network needs attention"
        state_copy = (
            "All visible server status and security checks are current."
            if healthy
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
            csrf_token=csrf_token,
            fresh_at=home.fresh_at,
        )

    def servers(self, page: ServerPage, csrf_token: str) -> str:
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
            csrf_token=csrf_token,
            fresh_at=page.fresh_at,
        )

    def people(self, page: PersonPage, csrf_token: str) -> str:
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
                        f'<form method="post" action="/harnesses/{quote(harness.harness_id, safe="")}/revoke">'
                        f'<input type="hidden" name="csrf_token" value="{_e(csrf_token)}">'
                        f'<input type="hidden" name="idempotency_key" value="{_e(secrets.token_urlsafe(24))}">'
                        '<div class="field"><label for="reason-'
                        f'{_e(harness.harness_id)}">Reason</label><textarea id="reason-{_e(harness.harness_id)}" name="reason" required maxlength="512"></textarea></div>'
                        '<div class="field"><label for="confirmation-'
                        f'{_e(harness.harness_id)}">Type “{_e(phrase)}”</label>'
                        f'<input id="confirmation-{_e(harness.harness_id)}" name="confirmation" required autocomplete="off"></div>'
                        '<button class="danger" type="submit">Remove this laptop’s access</button></form></details>'
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
            f'<option value="{_e(person.principal_id)}">{_e(person.display_name)}</option>'
            for person in page.people
            if person.access_state == "Active"
        )
        enroll = (
            '<section class="section panel" id="enroll" aria-labelledby="enroll-title"><p class="eyebrow">Proof-bound enrollment</p>'
            '<h2 id="enroll-title">Enroll a laptop</h2>'
            '<p>Starting this request creates no access. The target must verify identity, prove device possession, and receive fresh passkey approval.</p>'
            '<form method="post" action="/enrollments">'
            f'<input type="hidden" name="csrf_token" value="{_e(csrf_token)}">'
            f'<input type="hidden" name="idempotency_key" value="{_e(secrets.token_urlsafe(24))}">'
            '<fieldset class="field"><legend>Who will use this laptop?</legend>'
            '<label><input type="radio" name="target_kind" value="existing_person" checked> Existing person</label>'
            '<label><input type="radio" name="target_kind" value="new_person"> Invite someone new</label></fieldset>'
            f'<div class="field"><label for="target-principal">Existing person</label><select id="target-principal" name="target_principal_id"><option value="">Choose a person</option>{options}</select></div>'
            '<div class="field"><label for="invited-email">New person’s verified email</label><input id="invited-email" name="invited_email_alias" type="email" autocomplete="email" maxlength="320"></div>'
            '<div class="field"><label for="harness-name">Laptop or agent name</label><input id="harness-name" name="harness_name" required maxlength="128" autocomplete="off"></div>'
            '<fieldset class="field"><legend>Requested services</legend>'
            '<label><input type="checkbox" name="capabilities" value="message_delivery"> Message delivery</label>'
            '<label><input type="checkbox" name="capabilities" value="offline_delivery"> Offline delivery</label></fieldset>'
            '<div class="field"><label for="enrollment-reason">Reason</label><textarea id="enrollment-reason" name="reason" required maxlength="512"></textarea></div>'
            '<div class="notice warning"><strong>Exact consequence:</strong> This only starts a request. No access is created until the target verifies identity, proves device possession, and a fresh passkey approval completes.</div>'
            '<div class="field"><label><input type="checkbox" name="confirmation" value="Start this enrollment request" required> Start this enrollment request with these exact details</label></div>'
            '<button type="submit">Start enrollment request</button></form></section>'
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
            csrf_token=csrf_token,
            fresh_at=page.fresh_at,
        )

    def approvals(self, page: ApprovalPage, csrf_token: str) -> str:
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
                f'<p><a class="button secondary" href="{_e(approval_href)}">Approve with passkey</a></p>'
                + (
                    f'<form method="post" action="{_e(item.action_path)}">'
                    f'<input type="hidden" name="csrf_token" value="{_e(csrf_token)}">'
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
            csrf_token=csrf_token,
            fresh_at=page.fresh_at,
        )

    def security(self, page: SecurityPage, csrf_token: str) -> str:
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
            csrf_token=csrf_token,
            fresh_at=page.fresh_at,
        )

    def activity(self, page: ActivityPage, csrf_token: str) -> str:
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
            csrf_token=csrf_token,
            fresh_at=page.fresh_at,
        )


__all__ = ["ConsoleRenderer"]
