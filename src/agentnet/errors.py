"""Typed, non-disclosing errors used at trust boundaries."""

from __future__ import annotations


class ExtensionError(Exception):
    """Base extension failure."""

    code = "extension_error"
    http_status = 400

    def public_detail(self) -> dict[str, str]:
        return {"code": self.code, "message": "request could not be processed"}


class AuthenticationError(ExtensionError):
    code = "authentication_failed"
    http_status = 401


class AuthorizationError(ExtensionError):
    code = "not_authorized"
    http_status = 404


class ConflictError(ExtensionError):
    code = "conflict"
    http_status = 409


class RetryableConflictError(ConflictError):
    """A transactional race that the same exact request may safely retry."""

    code = "retryable_conflict"


class ValidationError(ExtensionError):
    code = "invalid_request"
    http_status = 422


class UnsupportedMediaTypeError(ValidationError):
    """A validated protocol request carries a media type this service rejects."""

    code = "unsupported_media_type"
    http_status = 415


class GateBlocked(ExtensionError):
    code = "gate_blocked"
    http_status = 503

    def __init__(self, gate: str, reason: str) -> None:
        super().__init__(reason)
        self.gate = gate
        self.reason = reason

    def public_detail(self) -> dict[str, str]:
        return {"code": self.code, "gate": self.gate, "message": self.reason}


class ReplayError(AuthenticationError):
    code = "replay_rejected"


class IdempotencyConflict(ConflictError):
    code = "idempotency_digest_conflict"
