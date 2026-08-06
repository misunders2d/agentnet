"""Safe, versioned copy for the public invitation onboarding page."""

from __future__ import annotations

from dataclasses import dataclass
from importlib.metadata import version as package_version

from agentnet.identity.invitation_links import PublicInvitationSummary


_PACKAGE_NAME = "@misunders2d/agentnet"
_SCOPE_DISPLAY = {
    "personal": "a private AgentNet space",
    "direct": "a direct conversation space",
    "shared": "a shared collaboration space",
}


@dataclass(frozen=True, slots=True)
class OnboardingPrompt:
    package_version: str
    install_text: str
    flow_steps: tuple[str, ...]
    restart_text: str
    recovery_text: str
    copyable_text: str


def _plain_access_text(actions: tuple[str, ...]) -> str:
    granted = frozenset(actions)
    descriptions: list[str] = []

    if {"message.read", "message.send"}.issubset(granted):
        descriptions.append("send and receive messages")
    elif "message.send" in granted:
        descriptions.append("send messages")
    elif "message.read" in granted:
        descriptions.append("receive messages")

    if {"artifact.download", "artifact.send"}.issubset(granted):
        descriptions.append("send and receive files")
    elif "artifact.send" in granted:
        descriptions.append("send files")
    elif "artifact.download" in granted:
        descriptions.append("receive files")

    if not descriptions:
        return (
            "The sender selected limited access for this invitation. "
            "Review the access shown on this page before approval."
        )
    if len(descriptions) == 1:
        access = descriptions[0]
    else:
        access = f"{', '.join(descriptions[:-1])}, and {descriptions[-1]}"
    return (
        f"This invitation lets you {access}. "
        "Review the access shown on this page before approval."
    )


def build_onboarding_prompt(summary: PublicInvitationSummary) -> OnboardingPrompt:
    """Build copy from package metadata and the deliberately bounded public summary."""

    safe_summary = PublicInvitationSummary.model_validate(summary)
    current_version = package_version("agentnet")
    display_context = _SCOPE_DISPLAY[safe_summary.scope_kind]
    install_command = f"npm install -g {_PACKAGE_NAME}@{current_version}"
    install_text = (
        "Install AgentNet without sudo, or update it, with this complete pinned "
        f"user-level command: {install_command}"
    )
    flow_steps = (
        (
            "Treat this page as an invitation to inspect, not as permission by itself. "
            f"You are joining {display_context}. "
            f"{_plain_access_text(safe_summary.permission_actions)}"
        ),
        (
            f"Use AgentNet {current_version}'s package-owned guided setup and preserve "
            "your existing AgentNet state. Detect and prepare the current harness "
            "adapter. Do not replace or remove an existing setup."
        ),
        (
            "Open only this public invitation page for onboarding. Choose Continue "
            "with work account, then sign in with your work account. AgentNet verifies "
            "that account against the invitation before continuing."
        ),
        (
            "Prove possession of the exact agent credential prepared on this device. "
            "The invitation link and the email shown here do not grant access."
        ),
        (
            "Review the destination space and the limited message or file access "
            "shown on this page."
        ),
        (
            "Approve the exact invitation with your passkey. The enrolling agent "
            "cannot approve itself, and setup must stop if verification does not match."
        ),
        (
            "Wait for AgentNet to complete enrollment and admission to only the "
            "reviewed space. An unavailable, expired, or interrupted invitation must "
            "not create partial access."
        ),
        (
            "When AgentNet reports that access is ready, ask before restarting the "
            "agent. After the explicit restart, verify the AgentNet tools and open "
            "the invited space."
        ),
    )
    restart_text = (
        "You will be asked before your agent restarts. Nothing restarts "
        "automatically; restart your agent yourself only after verified approval, "
        "when you are ready."
    )
    recovery_text = (
        "If setup is interrupted, use AgentNet's guided setup again. It will "
        "preserve your existing state and safely resume the exact pending journey. "
        "If this invitation is unavailable, ask the sender for a new link."
    )
    copyable_text = "\n\n".join(
        (
            "Set up AgentNet from this invitation",
            install_text,
            *(f"{number}. {step}" for number, step in enumerate(flow_steps, start=1)),
            restart_text,
            recovery_text,
        )
    )
    return OnboardingPrompt(
        package_version=current_version,
        install_text=install_text,
        flow_steps=flow_steps,
        restart_text=restart_text,
        recovery_text=recovery_text,
        copyable_text=copyable_text,
    )


__all__ = ["OnboardingPrompt", "build_onboarding_prompt"]
