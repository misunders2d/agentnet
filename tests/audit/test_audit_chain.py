from __future__ import annotations

from agentnet.audit.service import AuditService
from agentnet.security.signatures import P256KeyPair


def test_hash_chain_and_signed_checkpoint(store) -> None:
    with store.transaction() as connection:
        store.append_audit(connection, {"action": "one"})
        store.append_audit(connection, {"action": "two"})
    audit = AuditService(store)
    assert audit.verify() == {"valid": True, "records_checked": 2}
    key = P256KeyPair.generate()
    checkpoint = audit.checkpoint(key)
    audit.verify_checkpoint(key.public_pem, checkpoint)

