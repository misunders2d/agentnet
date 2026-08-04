"""Opaque browser sessions bound to an exact current enrolled human harness."""

from __future__ import annotations

import base64
import hashlib
import secrets
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from uuid import uuid4

from agentnet.authorization.policy import validate_actor_state
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.identity.oidc import OIDCProvider
from agentnet.security.signatures import canonical_digest
from agentnet.storage.backend import StoreBackend


@dataclass(frozen=True, slots=True)
class IssuedConsoleSession:
    session_token: str
    csrf_token: str
    session_id: str
    expires_at: int


@dataclass(frozen=True, slots=True)
class ConsoleSessionStatus:
    actor: VerifiedActor
    session_id: str
    csrf_token: str
    expires_at: int


@dataclass(frozen=True, slots=True)
class ConsoleOIDCBegin:
    authorization_url: str
    state: str
    preauth_token: str
    expires_at: int


@dataclass(frozen=True, slots=True)
class ConsoleChallenge:
    challenge_id: str
    transaction: dict[str, object]
    transaction_digest: str
    expires_at: int

@dataclass(frozen=True, slots=True)
class ConsoleHandoff:
    handoff_token: str
    expires_at: int


def mutation_form_digest(form: Mapping[str, Sequence[str]]) -> str:
    """Digest exact URL-form fields while excluding the one-use authorization itself."""

    fields: list[list[object]] = []
    for name in sorted(form):
        if not isinstance(name, str) or not name or name == "mutation_token":
            if name == "mutation_token":
                continue
            raise AuthorizationError("console mutation denied")
        values = form[name]
        if isinstance(values, (str, bytes)) or not isinstance(values, Sequence):
            raise AuthorizationError("console mutation denied")
        exact_values = list(values)
        if not exact_values or any(not isinstance(value, str) for value in exact_values):
            raise AuthorizationError("console mutation denied")
        fields.append([name, exact_values])
    return canonical_digest(
        {
            "schema": "agentnet.console.mutation-form.v1",
            "fields": fields,
        }
    )


class ConsoleSessionService:
    """Persists only hashes and encrypted anti-CSRF state.

    ``issue_for_verified_actor`` is a test/trusted-boundary primitive. Production
    browser sessions are issued only after the signed harness handoff and
    workforce OIDC identity have both been verified.
    """

    def __init__(
        self,
        *,
        store: StoreBackend,
        audience: str,
        ttl_seconds: int,
        require: Callable[..., object],
        challenge_ttl_seconds: int = 300,
        handoff_ttl_seconds: int = 120,
        mutation_ttl_seconds: int = 120,
        clock: Callable[[], int] | None = None,
    ) -> None:
        if not callable(require):
            raise ValueError("console session authority checker is required")
        if not 30 <= handoff_ttl_seconds <= challenge_ttl_seconds:
            raise ValueError("console handoff lifetime is invalid")
        if not 30 <= mutation_ttl_seconds <= 300:
            raise ValueError("console mutation authorization lifetime is invalid")
        self.store = store
        self.audience = audience
        self.ttl_seconds = ttl_seconds
        self.challenge_ttl_seconds = challenge_ttl_seconds
        self.handoff_ttl_seconds = handoff_ttl_seconds
        self.mutation_ttl_seconds = mutation_ttl_seconds
        self.require = require
        self.clock = clock or (lambda: int(time.time()))

    @staticmethod
    def _hash(value: str) -> str:
        return hashlib.sha256(value.encode("ascii")).hexdigest()

    @staticmethod
    def _require_human_harness(actor: VerifiedActor) -> None:
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
            or actor.binding_assurance not in {"os_bound", "hardware_bound"}
        ):
            raise AuthenticationError("console session denied")

    def _require_current_actor(self, connection, actor: VerifiedActor, *, now: int) -> None:
        self._require_human_harness(actor)
        try:
            binding = load_credential_binding_from_connection(
                connection, actor.credential_id or ""
            )
            binding.require_active(now=now)
        except AuthenticationError as exc:
            raise AuthenticationError("console session denied") from exc
        if (
            binding.domain_id != actor.domain_id
            or binding.principal_id != actor.principal_id
            or binding.harness_id != actor.harness_id
            or binding.credential_epoch != actor.credential_epoch
            or binding.binding_assurance != actor.binding_assurance
        ):
            raise AuthenticationError("console session denied")
        domain = connection.execute(
            "SELECT policy_revision FROM domains WHERE domain_id=?", (actor.domain_id,)
        ).fetchone()
        if domain is None:
            raise AuthenticationError("console session denied")
        denial, _ = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(domain["policy_revision"]),
            when=datetime.fromtimestamp(now, UTC),
        )
        if denial is not None:
            raise AuthenticationError("console session denied")

    def _require_console_authority(self, actor: VerifiedActor) -> None:
        try:
            self.require(
                actor=actor,
                action="console.session.open",
                resource=f"console-domain:{actor.domain_id}",
            )
        except (AuthenticationError, AuthorizationError) as exc:
            raise AuthenticationError("console session denied") from exc

    def begin_challenge(self, *, actor: VerifiedActor) -> ConsoleChallenge:
        now = self.clock()
        self._require_human_harness(actor)
        challenge_id = str(uuid4())
        nonce = secrets.token_urlsafe(32)
        expires_at = now + self.challenge_ttl_seconds
        transaction = {
            "schema": "agentnet.console.session-challenge.v1",
            "challenge_id": challenge_id,
            "audience": self.audience,
            "domain_id": actor.domain_id,
            "principal_id": actor.principal_id,
            "harness_id": actor.harness_id,
            "credential_id": actor.credential_id,
            "credential_epoch": actor.credential_epoch,
            "binding_assurance": actor.binding_assurance,
            "nonce": nonce,
            "issued_at": now,
            "expires_at": expires_at,
        }
        digest = canonical_digest(transaction)
        with self.store.transaction() as connection:
            self._require_current_actor(connection, actor, now=now)
            connection.execute(
                """INSERT INTO console_session_challenges(
                    challenge_id,domain_id,principal_id,harness_id,credential_id,credential_epoch,
                    binding_assurance,audience,nonce_hash,transaction_digest,state,created_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,'pending',?,?)""",
                (
                    challenge_id,
                    actor.domain_id,
                    actor.principal_id,
                    actor.harness_id,
                    actor.credential_id,
                    actor.credential_epoch,
                    actor.binding_assurance,
                    self.audience,
                    self._hash(nonce),
                    digest,
                    now,
                    expires_at,
                ),
            )
        return ConsoleChallenge(challenge_id, transaction, digest, expires_at)

    def complete_challenge(
        self,
        *,
        actor: VerifiedActor,
        challenge_id: str,
        transaction_digest: str,
    ) -> ConsoleHandoff:
        now = self.clock()
        handoff_token = secrets.token_urlsafe(32)
        handoff_hash = self._hash(handoff_token)
        with self.store.transaction() as connection:
            self._require_current_actor(connection, actor, now=now)
            self._require_console_authority(actor)
            row = connection.execute(
                "SELECT * FROM console_session_challenges WHERE challenge_id=?", (challenge_id,)
            ).fetchone()
            if (
                row is None
                or row["state"] != "pending"
                or int(row["expires_at"]) <= now
                or row["audience"] != self.audience
                or row["domain_id"] != actor.domain_id
                or row["principal_id"] != actor.principal_id
                or row["harness_id"] != actor.harness_id
                or row["credential_id"] != actor.credential_id
                or row["binding_assurance"] != actor.binding_assurance
                or int(row["credential_epoch"]) != actor.credential_epoch
                or not secrets.compare_digest(str(row["transaction_digest"]), transaction_digest)
            ):
                raise AuthenticationError("console session denied")
            expires_at = min(int(row["expires_at"]), now + self.handoff_ttl_seconds)
            updated = connection.execute(
                """UPDATE console_session_challenges
                   SET state='completed',completed_at=?,handoff_hash=?,handoff_expires_at=?
                   WHERE challenge_id=? AND state='pending' AND handoff_hash IS NULL""",
                (now, handoff_hash, expires_at, challenge_id),
            )
            if updated.rowcount != 1:
                raise ConflictError("console challenge was already completed")
        return ConsoleHandoff(handoff_token=handoff_token, expires_at=expires_at)


    def _prepare_session(self, *, actor: VerifiedActor, now: int) -> tuple[IssuedConsoleSession, str]:
        self._require_human_harness(actor)
        session_token = secrets.token_urlsafe(32)
        csrf_token = secrets.token_urlsafe(32)
        session_id = str(uuid4())
        issued = IssuedConsoleSession(
            session_token=session_token,
            csrf_token=csrf_token,
            session_id=session_id,
            expires_at=now + self.ttl_seconds,
        )
        encrypted_csrf = self.store.encrypted_payload({"csrf_token": csrf_token}, session_id)
        return issued, encrypted_csrf

    def _insert_session(
        self,
        connection,
        *,
        actor: VerifiedActor,
        issued: IssuedConsoleSession,
        encrypted_csrf: str,
        now: int,
    ) -> None:
        connection.execute(
            """INSERT INTO console_browser_sessions(
                session_hash,session_id,domain_id,principal_id,harness_id,credential_id,
                credential_epoch,binding_assurance,csrf_secret_encrypted,created_at,
                authenticated_at,expires_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                self._hash(issued.session_token),
                issued.session_id,
                actor.domain_id,
                actor.principal_id,
                actor.harness_id,
                actor.credential_id,
                actor.credential_epoch,
                actor.binding_assurance,
                encrypted_csrf,
                now,
                now,
                issued.expires_at,
            ),
        )


    def issue_for_verified_actor(self, *, actor: VerifiedActor) -> IssuedConsoleSession:
        now = self.clock()
        issued, encrypted_csrf = self._prepare_session(actor=actor, now=now)
        with self.store.transaction() as connection:
            self._require_current_actor(connection, actor, now=now)
            self._insert_session(
                connection,
                actor=actor,
                issued=issued,
                encrypted_csrf=encrypted_csrf,
                now=now,
            )
        return issued

    def authenticate(self, session_token: str) -> ConsoleSessionStatus:
        if not isinstance(session_token, str) or not 32 <= len(session_token) <= 128:
            raise AuthenticationError("console session denied")
        now = self.clock()
        with self.store.transaction(immediate=False) as connection:
            row = connection.execute(
                "SELECT * FROM console_browser_sessions WHERE session_hash=?",
                (self._hash(session_token),),
            ).fetchone()
            if (
                row is None
                or row["revoked_at"] is not None
                or int(row["expires_at"]) <= now
            ):
                raise AuthenticationError("console session denied")
            actor = self._actor_from_row(row)
            self._require_current_actor(connection, actor, now=now)
            self._require_console_authority(actor)
            payload = self.store.decrypted_payload(
                str(row["csrf_secret_encrypted"]), str(row["session_id"])
            )
            csrf_token = payload.get("csrf_token")
            if not isinstance(csrf_token, str):
                raise AuthenticationError("console session denied")
            return ConsoleSessionStatus(actor, str(row["session_id"]), csrf_token, int(row["expires_at"]))

    def issue_mutation_authorization(
        self,
        *,
        session_token: str,
        method: str,
        path: str,
        form: Mapping[str, Sequence[str]],
    ) -> str:
        status = self.authenticate(session_token)
        if "mutation_token" in form:
            raise AuthorizationError("console mutation denied")
        digest = mutation_form_digest(form)
        if method != method.upper() or not path.startswith("/"):
            raise AuthorizationError("console mutation denied")
        now = self.clock()
        token = secrets.token_urlsafe(32)
        session_hash = self._hash(session_token)
        expires_at = min(status.expires_at, now + self.mutation_ttl_seconds)
        with self.store.transaction() as connection:
            session = connection.execute(
                """SELECT revoked_at,expires_at FROM console_browser_sessions
                   WHERE session_hash=? AND session_id=?""",
                (session_hash, status.session_id),
            ).fetchone()
            if (
                session is None
                or session["revoked_at"] is not None
                or int(session["expires_at"]) <= now
                or expires_at <= now
            ):
                raise AuthorizationError("console mutation denied")
            self._require_current_actor(connection, status.actor, now=now)
            self._require_console_authority(status.actor)
            connection.execute(
                """DELETE FROM console_mutation_authorizations
                   WHERE expires_at<=? OR consumed_at IS NOT NULL""",
                (now,),
            )
            connection.execute(
                """INSERT INTO console_mutation_authorizations(
                       authorization_hash,session_hash,method,path,body_sha256,
                       created_at,expires_at
                   ) VALUES(?,?,?,?,?,?,?)""",
                (
                    self._hash(token),
                    session_hash,
                    method,
                    path,
                    digest,
                    now,
                    expires_at,
                ),
            )
        return token

    def require_mutation(
        self,
        *,
        session_token: str,
        authorization_token: str,
        method: str,
        path: str,
        form: Mapping[str, Sequence[str]],
    ) -> ConsoleSessionStatus:
        status = self.authenticate(session_token)
        if (
            not isinstance(authorization_token, str)
            or not 32 <= len(authorization_token) <= 128
            or list(form.get("mutation_token", ())) != [authorization_token]
        ):
            raise AuthorizationError("console mutation denied")
        digest = mutation_form_digest(form)
        if method != method.upper() or not path.startswith("/"):
            raise AuthorizationError("console mutation denied")
        now = self.clock()
        session_hash = self._hash(session_token)
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT a.*,s.revoked_at,s.expires_at AS session_expires_at
                     FROM console_mutation_authorizations a
                     JOIN console_browser_sessions s ON s.session_hash=a.session_hash
                    WHERE a.authorization_hash=?""",
                (self._hash(authorization_token),),
            ).fetchone()
            if (
                row is None
                or row["session_hash"] != session_hash
                or row["consumed_at"] is not None
                or int(row["expires_at"]) <= now
                or row["revoked_at"] is not None
                or int(row["session_expires_at"]) <= now
                or row["method"] != method
                or row["path"] != path
                or not secrets.compare_digest(str(row["body_sha256"]), digest)
            ):
                raise AuthorizationError("console mutation denied")
            self._require_current_actor(connection, status.actor, now=now)
            self._require_console_authority(status.actor)
            updated = connection.execute(
                """UPDATE console_mutation_authorizations SET consumed_at=?
                   WHERE authorization_hash=? AND consumed_at IS NULL""",
                (now, self._hash(authorization_token)),
            )
            if updated.rowcount != 1:
                raise AuthorizationError("console mutation denied")
            self.store.append_audit(
                connection,
                {
                    "action": "console.mutation.authorized",
                    "domain_id": status.actor.domain_id,
                    "principal_id": status.actor.principal_id,
                    "harness_id": status.actor.harness_id,
                    "session_id": status.session_id,
                    "method": method,
                    "path": path,
                    "body_sha256": digest,
                },
            )
        return status
    def rotate(self, session_token: str) -> IssuedConsoleSession:
        status = self.authenticate(session_token)
        now = self.clock()
        issued, encrypted_csrf = self._prepare_session(actor=status.actor, now=now)
        predecessor_hash = self._hash(session_token)
        with self.store.transaction() as connection:
            predecessor = connection.execute(
                """SELECT revoked_at,expires_at FROM console_browser_sessions
                   WHERE session_hash=?""",
                (predecessor_hash,),
            ).fetchone()
            if (
                predecessor is None
                or predecessor["revoked_at"] is not None
                or int(predecessor["expires_at"]) <= now
            ):
                raise AuthenticationError("console session denied")
            self._require_current_actor(connection, status.actor, now=now)
            connection.execute(
                "UPDATE console_browser_sessions SET revoked_at=? WHERE session_hash=?",
                (now, predecessor_hash),
            )
            self._insert_session(
                connection,
                actor=status.actor,
                issued=issued,
                encrypted_csrf=encrypted_csrf,
                now=now,
            )
            connection.execute(
                """UPDATE console_browser_sessions SET rotated_from_hash=?
                   WHERE session_hash=?""",
                (predecessor_hash, self._hash(issued.session_token)),
            )
        return issued


    def revoke(self, session_token: str) -> None:
        now = self.clock()
        with self.store.transaction() as connection:
            connection.execute(
                "UPDATE console_browser_sessions SET revoked_at=? WHERE session_hash=? AND revoked_at IS NULL",
                (now, self._hash(session_token)),
            )

    @staticmethod
    def _actor_from_row(row) -> VerifiedActor:
        return VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=str(row["domain_id"]),
            principal_id=str(row["principal_id"]),
            harness_id=str(row["harness_id"]),
            credential_id=str(row["credential_id"]),
            credential_epoch=int(row["credential_epoch"]),
            binding_assurance=str(row["binding_assurance"]),
        )
class ConsoleOIDCCoordinator:
    """Bind workforce OIDC to one previously completed signed harness challenge."""

    def __init__(
        self,
        *,
        sessions: ConsoleSessionService,
        provider: OIDCProvider,
        preauth_ttl_seconds: int = 300,
    ) -> None:
        self.sessions = sessions
        self.store = sessions.store
        self.provider = provider
        self.preauth_ttl_seconds = preauth_ttl_seconds

    @staticmethod
    def _challenge(value: str) -> str:
        digest = hashlib.sha256(value.encode("ascii")).digest()
        return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")

    def begin(self, *, handoff_token: str) -> ConsoleOIDCBegin:
        if not isinstance(handoff_token, str) or not 32 <= len(handoff_token) <= 128:
            raise AuthenticationError("console session denied")
        initial_now = self.sessions.clock()
        handoff_hash = self.sessions._hash(handoff_token)
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT * FROM console_session_challenges
                   WHERE handoff_hash=?""",
                (handoff_hash,),
            ).fetchone()
            if (
                row is None
                or row["state"] != "completed"
                or row["handoff_consumed_at"] is not None
                or row["handoff_expires_at"] is None
                or int(row["handoff_expires_at"]) <= initial_now
                or int(row["expires_at"]) <= initial_now
            ):
                raise AuthenticationError("console session denied")
            actor = self.sessions._actor_from_row(row)
            self.sessions._require_current_actor(connection, actor, now=initial_now)
            self.sessions._require_console_authority(actor)
            consumed = connection.execute(
                """UPDATE console_session_challenges SET handoff_consumed_at=?
                   WHERE challenge_id=? AND handoff_consumed_at IS NULL""",
                (initial_now, row["challenge_id"]),
            )
            if consumed.rowcount != 1:
                raise AuthenticationError("console session denied")
            challenge_id = str(row["challenge_id"])
            challenge_expires_at = int(row["expires_at"])
            handoff_expires_at = int(row["handoff_expires_at"])

        transaction_id = str(uuid4())
        state = secrets.token_urlsafe(32)
        nonce = secrets.token_urlsafe(32)
        verifier = secrets.token_urlsafe(48)
        preauth = secrets.token_urlsafe(32)
        authorization_url = self.provider.authorization_url(
            state=state,
            nonce=nonce,
            code_challenge=self._challenge(verifier),
        )
        now = self.sessions.clock()
        if now >= challenge_expires_at or now >= handoff_expires_at:
            raise AuthenticationError("console session denied")
        expires_at = min(challenge_expires_at, now + self.preauth_ttl_seconds)
        encrypted_verifier = self.store.encrypted_payload(
            {"code_verifier": verifier}, transaction_id
        )
        with self.store.transaction() as connection:
            challenge = connection.execute(
                """SELECT state,handoff_hash,handoff_consumed_at,expires_at
                     FROM console_session_challenges WHERE challenge_id=?""",
                (challenge_id,),
            ).fetchone()
            if (
                challenge is None
                or challenge["state"] != "completed"
                or challenge["handoff_hash"] != handoff_hash
                or challenge["handoff_consumed_at"] is None
                or int(challenge["expires_at"]) <= now
            ):
                raise AuthenticationError("console session denied")
            connection.execute(
                """INSERT INTO console_oidc_transactions(
                    transaction_id,challenge_id,state_hash,nonce_hash,code_verifier_encrypted,
                    preauth_hash,created_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?)""",
                (
                    transaction_id,
                    challenge_id,
                    self.sessions._hash(state),
                    self.sessions._hash(nonce),
                    encrypted_verifier,
                    self.sessions._hash(preauth),
                    now,
                    expires_at,
                ),
            )
        return ConsoleOIDCBegin(authorization_url, state, preauth, expires_at)

    def complete(self, *, state: str, code: str, preauth_token: str) -> IssuedConsoleSession:
        initial_now = self.sessions.clock()
        if not state or not code or not preauth_token:
            raise AuthenticationError("console session denied")
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT t.*,c.*
                   FROM console_oidc_transactions t
                   JOIN console_session_challenges c ON c.challenge_id=t.challenge_id
                   WHERE t.state_hash=?""",
                (self.sessions._hash(state),),
            ).fetchone()
            if (
                row is None
                or row["consumed_at"] is not None
                or row["exchange_started_at"] is not None
                or int(row["expires_at"]) <= initial_now
                or not secrets.compare_digest(
                    str(row["preauth_hash"]), self.sessions._hash(preauth_token)
                )
            ):
                raise AuthenticationError("console session denied")
            transaction_id = str(row["transaction_id"])
            challenge_id = str(row["challenge_id"])
            principal_id = str(row["principal_id"])
            nonce_hash = str(row["nonce_hash"])
            actor = self.sessions._actor_from_row(row)
            payload = self.store.decrypted_payload(
                str(row["code_verifier_encrypted"]), transaction_id
            )
            verifier = payload.get("code_verifier")
            if not isinstance(verifier, str):
                raise AuthenticationError("console session denied")
            claimed = connection.execute(
                """UPDATE console_oidc_transactions SET exchange_started_at=?
                   WHERE transaction_id=? AND exchange_started_at IS NULL
                     AND consumed_at IS NULL""",
                (initial_now, transaction_id),
            )
            if claimed.rowcount != 1:
                raise AuthenticationError("console session denied")
        result = self.provider.exchange_and_verify(
            code=code,
            code_verifier=verifier,
            expected_nonce_hash=nonce_hash,
        )
        now = self.sessions.clock()
        issued, encrypted_csrf = self.sessions._prepare_session(actor=actor, now=now)
        with self.store.transaction() as connection:
            current = connection.execute(
                """SELECT consumed_at,exchange_started_at,expires_at
                     FROM console_oidc_transactions WHERE transaction_id=?""",
                (transaction_id,),
            ).fetchone()
            principal = connection.execute(
                "SELECT oidc_issuer,oidc_subject,status FROM principals WHERE principal_id=?",
                (principal_id,),
            ).fetchone()
            challenge = connection.execute(
                "SELECT state,expires_at FROM console_session_challenges WHERE challenge_id=?",
                (challenge_id,),
            ).fetchone()
            self.sessions._require_current_actor(connection, actor, now=now)
            self.sessions._require_console_authority(actor)
            if (
                current is None
                or current["consumed_at"] is not None
                or current["exchange_started_at"] is None
                or int(current["expires_at"]) <= now
                or result.expires_at <= now
                or challenge is None
                or challenge["state"] != "completed"
                or int(challenge["expires_at"]) <= now
                or principal is None
                or principal["status"] != "active"
                or principal["oidc_issuer"] != result.identity.issuer
                or principal["oidc_subject"] != result.identity.subject
            ):
                raise AuthenticationError("console session denied")
            updated = connection.execute(
                """UPDATE console_oidc_transactions SET consumed_at=?
                   WHERE transaction_id=? AND exchange_started_at IS NOT NULL
                     AND consumed_at IS NULL""",
                (now, transaction_id),
            )
            challenge_updated = connection.execute(
                """UPDATE console_session_challenges SET state='consumed',consumed_at=?
                   WHERE challenge_id=? AND state='completed'""",
                (now, challenge_id),
            )
            if updated.rowcount != 1 or challenge_updated.rowcount != 1:
                raise AuthenticationError("console session denied")
            self.sessions._insert_session(
                connection,
                actor=actor,
                issued=issued,
                encrypted_csrf=encrypted_csrf,
                now=now,
            )
        return issued




__all__ = [
    "ConsoleChallenge",
    "ConsoleHandoff",
    "ConsoleOIDCBegin",
    "ConsoleOIDCCoordinator",
    "ConsoleSessionService",
    "ConsoleSessionStatus",
    "IssuedConsoleSession",
    "mutation_form_digest",
]
