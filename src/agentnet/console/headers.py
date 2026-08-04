"""Response security policy for the dedicated console origin."""

from __future__ import annotations

from collections.abc import Mapping

_CSP = "; ".join(
    (
        "default-src 'self'",
        "base-uri 'none'",
        "connect-src 'self'",
        "font-src 'self'",
        "form-action 'self'",
        "frame-ancestors 'none'",
        "img-src 'self' data:",
        "object-src 'none'",
        "script-src 'self'",
        "style-src 'self'",
        "upgrade-insecure-requests",
    )
)


def protected_headers(extra: Mapping[str, str] | None = None) -> dict[str, str]:
    headers = {
        "Cache-Control": "no-store",
        "Content-Security-Policy": _CSP,
        "Cross-Origin-Opener-Policy": "same-origin",
        "Cross-Origin-Resource-Policy": "same-origin",
        "Permissions-Policy": "camera=(), geolocation=(), microphone=(), payment=(), usb=()",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
        "X-Frame-Options": "DENY",
    }
    if extra:
        headers.update(extra)
    return headers


__all__ = ["protected_headers"]
