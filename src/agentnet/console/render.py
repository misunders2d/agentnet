"""Escaped server-rendered HTML for the private administration console."""

from __future__ import annotations

import html
import secrets
import xml.etree.ElementTree as ET
from collections.abc import Callable, Iterable, Mapping, Sequence
from datetime import UTC, datetime
from urllib.parse import quote, urlsplit

from agentnet.console.models import (
    ActivityPage,
    ApprovalPage,
    HomeSummary,
    PersonPage,
    SecurityPage,
    ServerPage,
    VisibleState,
)
from agentnet.identity.onboarding_prompt import OnboardingPrompt


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


_SVG_NAMESPACE = "http://www.w3.org/2000/svg"
_SAFE_QR_ATTRIBUTES = {
    f"{{{_SVG_NAMESPACE}}}svg": frozenset(
        {"class", "height", "preserveAspectRatio", "version", "viewBox", "width"}
    ),
    f"{{{_SVG_NAMESPACE}}}path": frozenset(
        {
            "class",
            "d",
            "fill",
            "shape-rendering",
            "stroke",
            "stroke-linecap",
            "stroke-width",
            "transform",
        }
    ),
}


def _https_invitation_url(value: str) -> str:
    parsed = urlsplit(value)
    if (
        parsed.scheme != "https"
        or not parsed.netloc
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.fragment
    ):
        raise ValueError("invitation URL must be an HTTPS URL")
    return value


def _safe_invitation_qr_svg(value: str) -> str:
    svg = value.strip()
    if not svg or len(svg) > 1_000_000 or "<!" in svg or "<?" in svg:
        raise ValueError("invitation QR code is not a safe SVG")
    try:
        root = ET.fromstring(svg)
    except ET.ParseError as exc:
        raise ValueError("invitation QR code is not a safe SVG") from exc
    if root.tag != f"{{{_SVG_NAMESPACE}}}svg":
        raise ValueError("invitation QR code is not a safe SVG")
    path_count = 0
    for element in root.iter():
        allowed = _SAFE_QR_ATTRIBUTES.get(element.tag)
        if allowed is None:
            raise ValueError("invitation QR code is not a safe SVG")
        if element.tag == f"{{{_SVG_NAMESPACE}}}path":
            path_count += 1
        if element.text and element.text.strip():
            raise ValueError("invitation QR code is not a safe SVG")
        if element.tail and element.tail.strip():
            raise ValueError("invitation QR code is not a safe SVG")
        for name, attribute in element.attrib.items():
            if (
                name not in allowed
                or "url(" in attribute.casefold()
                or any(character in attribute for character in "<>\"'`")
            ):
                raise ValueError("invitation QR code is not a safe SVG")
    if path_count == 0:
        raise ValueError("invitation QR code is not a safe SVG")
    return svg


def _local_invitation_revoke_path(value: str) -> str:
    parsed = urlsplit(value)
    if (
        parsed.scheme
        or parsed.netloc
        or parsed.query
        or parsed.fragment
        or not parsed.path.startswith("/invitations/")
        or not parsed.path.endswith("/revoke")
        or "//" in parsed.path
    ):
        raise ValueError("invitation revocation path must be local")
    return parsed.path


def _invitation_expiry(expires_at: int, fresh_at: int) -> str:
    remaining = expires_at - fresh_at
    if remaining <= 0:
        return "Expired"
    hours = max(1, (remaining + 3_599) // 3_600)
    if hours < 48:
        return f"Expires in {hours} hour{'s' if hours != 1 else ''}"
    days = (hours + 23) // 24
    return f"Expires in {days} day{'s' if days != 1 else ''}"

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

    def public_invitation(
        self,
        *,
        prompt: OnboardingPrompt,
        continue_path: str,
    ) -> str:
        parsed = urlsplit(continue_path)
        if (
            parsed.scheme
            or parsed.netloc
            or parsed.query
            or parsed.fragment
            or not parsed.path.startswith("/join/")
            or not parsed.path.endswith("/continue")
            or "//" in parsed.path
        ):
            raise ValueError("invitation continuation path must be local")
        steps = "".join(f"<li>{_e(step)}</li>" for step in prompt.flow_steps)
        return self.public_document(
            title="Join AgentNet",
            body=(
                '<div class="page-heading"><div><p class="eyebrow">Invitation</p>'
                "<h1>You are invited to AgentNet</h1>"
                "<p>Review the exact setup and access before continuing.</p></div></div>"
                '<section class="section panel" aria-labelledby="invitation-setup-title">'
                '<h2 id="invitation-setup-title">Set up this agent</h2>'
                f"<p>{_e(prompt.install_text)}</p>"
                f"<ol>{steps}</ol>"
                '<div class="field"><label for="onboarding-prompt">'
                "Copy these onboarding instructions</label>"
                f'<textarea id="onboarding-prompt" readonly rows="24">{_e(prompt.copyable_text)}</textarea></div>'
                '<p><button class="secondary" type="button" data-copy-target="onboarding-prompt" '
                'data-copy-status="public-onboarding-copy-status">Copy onboarding instructions</button> '
                '<span id="public-onboarding-copy-status" class="muted" aria-live="polite"></span></p>'
                '<noscript><p class="muted">Select the instructions and use your device’s copy command.</p></noscript>'
                f"<p>{_e(prompt.restart_text)}</p>"
                f"<p>{_e(prompt.recovery_text)}</p>"
                f'<form method="post" action="{_e(parsed.path)}" data-invitation-continue>'
                '<button type="submit">Continue with work account</button>'
                '<span class="muted" aria-live="polite" data-invitation-continue-status></span>'
                "</form></section>"
            ),
        )

    def public_invitation_status(self, *, state: str) -> str:
        copy = {
            "waiting_approval": (
                "Approve with passkey",
                "Review the exact person, agent, space, and access in the secure approval page.",
            ),
            "restart_required": (
                "Restart your agent to enable AgentNet",
                "Access is ready. Nothing restarts automatically; restart the exact agent yourself when you are ready.",
            ),
            "active": (
                "AgentNet is active",
                "The exact agent is connected to the approved space.",
            ),
        }
        if state not in copy:
            raise ValueError("invitation browser state is invalid")
        title, description = copy[state]
        return self.public_document(
            title=title,
            body=(
                '<section class="section panel" aria-live="polite">'
                f"<h1>{_e(title)}</h1><p>{_e(description)}</p>"
                "</section>"
            ),
        )

    def public_invitation_unavailable(self) -> str:
        return (
            '<!doctype html><html lang="en"><head><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width,initial-scale=1">'
            "<title>Invitation unavailable</title></head><body><main>"
            "<p>This invitation is unavailable. Ask the sender for a new link.</p>"
            "</main></body></html>"
        )

    def public_document(self, *, title: str, body: str) -> str:
        return (
            '<!doctype html><html lang="en"><head><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width,initial-scale=1">'
            '<meta name="referrer" content="no-referrer">'
            f"<title>{_e(title)} · AgentNet</title>"
            f'<link rel="stylesheet" href="/assets/console.css?v={_e(self.asset_version)}">'
            f'<script src="/assets/console.js?v={_e(self.asset_version)}" defer></script>'
            '</head><body><a class="skip-link" href="#main">Skip to main content</a>'
            f'<main id="main" tabindex="-1">{body}</main></body></html>'
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
            '<p>Each laptop and agent keeps its own access state. Removing one does not remove its siblings.</p>'
            '<p><a class="button" href="/invitations/new">Invite a colleague</a></p></div></div>'
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

    def invitation_new(
        self,
        *,
        spaces: Sequence[tuple[str, str]],
        authorize_mutation: MutationAuthorizer,
        fresh_at: int,
        values: Mapping[str, object] | None = None,
        error: str | None = None,
    ) -> str:
        form_values = values or {}
        email = str(form_values.get("email", ""))
        selected_scope = str(form_values.get("scope_id", ""))
        raw_permissions = form_values.get("permissions", ())
        selected_permissions = (
            {str(value) for value in raw_permissions if isinstance(value, str)}
            if isinstance(raw_permissions, (tuple, list, set, frozenset))
            else set()
        )
        options = "".join(
            f'<option value="{_e(scope_id)}"'
            f'{(" selected" if scope_id == selected_scope else "")}>{_e(display_name)}</option>'
            for scope_id, display_name in spaces
        )
        if not options:
            options = '<option value="">No spaces are available</option>'
        review_error = (
            f'<div class="notice warning" role="alert"><strong>Review the form:</strong> {_e(error)}</div>'
            if error
            else ""
        )
        permissions = (
            ("message.send", "Can send messages"),
            ("message.read", "Can read messages"),
            ("artifact.send", "Can send files"),
            ("artifact.download", "Can download files"),
        )
        permission_fields = "".join(
            f'<label><input type="checkbox" name="permissions" value="{_e(value)}"'
            f'{(" checked" if value in selected_permissions else "")}> {_e(label)}</label>'
            for value, label in permissions
        )
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Invite a colleague</p>'
            '<h1>Create an invitation</h1>'
            '<p>Choose one space and only the actions this person needs.</p></div></div>'
            '<section class="section panel" aria-labelledby="invitation-form-title">'
            '<h2 id="invitation-form-title">Invitation details</h2>'
            '<p>The invitation will work only for the work email entered below and will expire automatically.</p>'
            f"{review_error}"
            '<form method="post" action="/invitations">'
            '<div class="field"><label for="invitation-email">Work email</label>'
            f'<input id="invitation-email" name="email" type="email" autocomplete="email" maxlength="320" required value="{_e(email)}"></div>'
            '<div class="field"><label for="invitation-space">Space</label>'
            f'<select id="invitation-space" name="scope_id" required>{options}</select></div>'
            '<fieldset class="field"><legend>What can this person do?</legend>'
            f"{permission_fields}</fieldset>"
            '<div class="notice warning"><strong>Before you create it:</strong> Share the invitation only with the person named above. You can revoke it at any time.</div>'
            f'<button type="submit"{(" disabled" if not spaces else "")}>Create invitation</button>'
            "</form></section>"
        )
        return self.document(
            title="Create invitation",
            current_nav="people",
            body=body,
            authorize_mutation=authorize_mutation,
            fresh_at=fresh_at,
        )

    def invitation_detail(
        self,
        *,
        work_email: str,
        space: str,
        permissions: Sequence[str],
        invitation_url: str,
        qr_svg: str,
        expires_at: int,
        revoked: bool,
        revoke_path: str,
        authorize_mutation: MutationAuthorizer,
        fresh_at: int,
    ) -> str:
        expired = expires_at <= fresh_at
        state = "Access removed" if revoked else "Expired" if expired else "Active"
        permission_labels = {
            "message.send": "Can send messages",
            "message.read": "Can read messages",
            "artifact.send": "Can send files",
            "artifact.download": "Can download files",
        }
        visible_permissions = (
            permission_labels[value]
            for value in permission_labels
            if value in permissions
        )
        actions = ""
        if not revoked and not expired:
            public_url = _https_invitation_url(invitation_url)
            safe_svg = _safe_invitation_qr_svg(qr_svg)
            safe_revoke_path = _local_invitation_revoke_path(revoke_path)
            download_url = "data:image/svg+xml;charset=utf-8," + quote(safe_svg, safe="")
            onboarding = (
                f"You have been invited to the space “{space}” in AgentNet.\n\n"
                f"Open this invitation link: {public_url}\n"
                f"Sign in with {work_email} and follow the steps shown there.\n\n"
                f"This invitation expires at {_time(expires_at)}."
            )
            revoke_token = self._mutation_token_input(
                authorize_mutation,
                path=safe_revoke_path,
                form={},
            )
            actions = (
                '<section class="section panel" aria-labelledby="invitation-link-title">'
                '<h2 id="invitation-link-title">Share the invitation</h2>'
                f'<p><a id="invitation-url" href="{_e(public_url)}">{_e(public_url)}</a></p>'
                '<p><button class="secondary" type="button" data-copy-target="invitation-url" '
                'data-copy-status="invitation-link-copy-status">Copy invitation link</button> '
                '<span id="invitation-link-copy-status" class="muted" aria-live="polite"></span></p>'
                '<noscript><p class="muted">Select the invitation link and use your device’s copy command.</p></noscript>'
                '<div role="img" aria-label="QR code for this invitation link">'
                f"{safe_svg}</div>"
                f'<p><a class="button secondary" href="{_e(download_url)}" download="agentnet-invitation-qr.svg">Download QR code</a></p>'
                '<div class="field"><label for="onboarding-instructions">Onboarding instructions</label>'
                f'<textarea id="onboarding-instructions" readonly rows="7">{_e(onboarding)}</textarea></div>'
                '<p><button class="secondary" type="button" data-copy-target="onboarding-instructions" '
                'data-copy-status="onboarding-copy-status">Copy onboarding instructions</button> '
                '<span id="onboarding-copy-status" class="muted" aria-live="polite"></span></p>'
                '<noscript><p class="muted">Select the instructions and use your device’s copy command.</p></noscript>'
                '<details><summary>Revoke this invitation</summary>'
                '<p>The link will stop working immediately. This does not remove access already completed through a different invitation.</p>'
                f'<form method="post" action="{_e(safe_revoke_path)}">{revoke_token}'
                '<button class="danger" type="submit">Revoke invitation</button></form></details>'
                "</section>"
            )
        body = (
            '<div class="page-heading"><div><p class="eyebrow">Invitation</p>'
            '<h1>Invitation details</h1>'
            '<p>Share this only with the person listed below.</p></div>'
            f'<span class="status {_status_class(state)}">{_e(state)}</span></div>'
            '<section class="panel" aria-labelledby="invitation-summary-title">'
            '<h2 id="invitation-summary-title">Who and where</h2><dl class="meta">'
            f'<div><dt>Work email</dt><dd>{_e(work_email)}</dd></div>'
            f'<div><dt>Space</dt><dd>{_e(space)}</dd></div>'
            f'<div><dt>Expiry</dt><dd>{_e(_invitation_expiry(expires_at, fresh_at))}</dd></div>'
            f'<div><dt>Available until</dt><dd>{_e(_time(expires_at))}</dd></div></dl>'
            '<h3>Allowed actions</h3>'
            f'{_tags(visible_permissions, empty="No message or file actions")}</section>'
            f"{actions}"
        )
        return self.document(
            title="Invitation details",
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
