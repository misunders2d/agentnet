"""AES-256-GCM envelope for local conformance data at rest."""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from agentnet.errors import AuthenticationError
from agentnet.security.signatures import b64url_decode, b64url_encode, canonical_json


class LocalEnvelopeCipher:
    """Local software-key custody, honestly labeled as a lab tier."""

    def __init__(self, key: bytes) -> None:
        if len(key) != 32:
            raise ValueError("AES-256-GCM requires a 32-byte key")
        self._cipher = AESGCM(key)

    @classmethod
    def from_key_file(cls, path: Path, *, create: bool = True) -> "LocalEnvelopeCipher":
        if os.name == "nt":
            from agentnet.windows_security import read_private_file, write_private_file

            if not path.exists():
                if not create:
                    raise FileNotFoundError(path)
                write_private_file(path, os.urandom(32))
            return cls(read_private_file(path, max_bytes=32))
        if not path.exists():
            if not create:
                raise FileNotFoundError(path)
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            try:
                os.write(descriptor, os.urandom(32))
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
        mode = path.stat().st_mode & 0o777
        if mode & 0o077:
            raise PermissionError(f"local key file must be owner-only, got {oct(mode)}")
        return cls(path.read_bytes())

    def encrypt_json(self, value: Any, *, purpose: str) -> str:
        nonce = os.urandom(12)
        plaintext = canonical_json({"value": value})
        ciphertext = self._cipher.encrypt(nonce, plaintext, purpose.encode("utf-8"))
        return "v1." + b64url_encode(nonce) + "." + b64url_encode(ciphertext)

    def decrypt_json(self, token: str, *, purpose: str) -> Any:
        try:
            version, encoded_nonce, encoded_ciphertext = token.split(".", 2)
            if version != "v1":
                raise ValueError("unknown envelope version")
            plaintext = self._cipher.decrypt(
                b64url_decode(encoded_nonce),
                b64url_decode(encoded_ciphertext),
                purpose.encode("utf-8"),
            )
            return json.loads(plaintext)["value"]
        except Exception as exc:
            raise AuthenticationError("encrypted record failed authentication") from exc

