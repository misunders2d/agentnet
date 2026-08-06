#!/usr/bin/env bash
set -Eeuo pipefail

# Destructive only inside a fresh GitHub-hosted Ubuntu 24.04 runner. The lane
# installs two independent npm tarballs, realizes the released 0.1.44 service,
# and proves the sole supported 0.1.44/schema-6 -> 0.1.45/schema-7 transition.
if [[ "${CI:-}" != "true" || "${GITHUB_ACTIONS:-}" != "true" || -z "${RUNNER_TEMP:-}" ]]; then
  echo "ordinary server upgrade E2E requires an ephemeral GitHub Actions runner" >&2
  exit 2
fi
. /etc/os-release
if [[ "${ID:-}" != "ubuntu" || "${VERSION_ID:-}" != "24.04" ]]; then
  echo "ordinary server upgrade E2E requires Ubuntu 24.04" >&2
  exit 2
fi
if getent passwd agentnet >/dev/null || getent passwd agentnet-approval >/dev/null ||
   getent passwd agentnet-c0 >/dev/null || getent group agentnet >/dev/null ||
   getent group agentnet-approval >/dev/null || getent group agentnet-c0 >/dev/null; then
  echo "ordinary server upgrade E2E requires clean AgentNet identities and groups" >&2
  exit 2
fi
for path in /var/lib/agentnet /var/lib/agentnet-approval /var/lib/agentnet-c0 /var/lib/agentnet-setup /etc/agentnet-secrets; do
  if sudo test -e "$path"; then
    echo "ordinary server upgrade E2E requires clean AgentNet state" >&2
    exit 2
  fi
done

WORK="$RUNNER_TEMP/agentnet-ordinary-server-upgrade-e2e"
INPUTS="$WORK/inputs"
PACK="$WORK/pack"
PREFIX_0144="/opt/agentnet-upgrade-e2e-0.1.44"
PREFIX_0145="/opt/agentnet-upgrade-e2e-0.1.45"
NO_PROXY_VALUE="127.0.0.1,localhost,.agentnet.test,core.agentnet.test,approval.agentnet.test"
OPT_UID="$(stat -c '%u' /opt)"
OPT_GID="$(stat -c '%g' /opt)"
OPT_MODE="$(stat -c '%a' /opt)"
HBA_FILE=""
INJECT_PID=""
TOKEN='synthetic-upgrade-broker-token-0123456789abcdef0123456789'
mkdir -p "$INPUTS" "$PACK"
chmod 700 "$WORK" "$INPUTS" "$PACK"

# Ephemeral-runner diagnostics only. Never prints inputs, secrets, or keys.
report_failure() {
  local status=$?
  local line="$1"
  local command="$2"
  echo "ordinary server upgrade E2E: failed at line $line with status $status: $command" >&2
  local evidence
  for evidence in "$WORK"/*.stderr "$WORK"/*.json; do
    if [[ -s "$evidence" ]]; then
      echo "--- ${evidence##*/} ---" >&2
      tail -c 2000 "$evidence" >&2
      echo >&2
    fi
  done
  return "$status"
}
trap 'report_failure "$LINENO" "$BASH_COMMAND"' ERR

cleanup() {
  set +e
  if [[ -n "$INJECT_PID" ]]; then
    kill "$INJECT_PID" >/dev/null 2>&1
    wait "$INJECT_PID" >/dev/null 2>&1
  fi
  sudo systemctl start nginx >/dev/null 2>&1
  sudo systemctl disable --now \
    agentnet-core.service \
    agentnet-approval.service \
    agentnet-c0-responder.service \
    agentnet-credential-renew.timer >/dev/null 2>&1
  sudo systemctl stop agentnet-credential-renew.service >/dev/null 2>&1
  sudo systemctl reset-failed \
    agentnet-core.service \
    agentnet-approval.service \
    agentnet-c0-responder.service \
    agentnet-credential-renew.service \
    agentnet-credential-renew.timer >/dev/null 2>&1
  sudo rm -f \
    /etc/systemd/system/agentnet-core.service \
    /etc/systemd/system/agentnet-approval.service \
    /etc/systemd/system/agentnet-c0-responder.service \
    /etc/systemd/system/agentnet-credential-renew.service \
    /etc/systemd/system/agentnet-credential-renew.timer
  sudo systemctl daemon-reload >/dev/null 2>&1
  sudo rm -rf /var/lib/agentnet /var/lib/agentnet-approval /var/lib/agentnet-c0 /var/lib/agentnet-setup /etc/agentnet-secrets
  sudo userdel agentnet-c0 >/dev/null 2>&1
  sudo userdel agentnet-approval >/dev/null 2>&1
  sudo userdel agentnet >/dev/null 2>&1
  sudo groupdel agentnet-c0 >/dev/null 2>&1
  sudo groupdel agentnet-approval >/dev/null 2>&1
  sudo groupdel agentnet >/dev/null 2>&1
  sudo rm -f /etc/nginx/sites-enabled/agentnet-upgrade-e2e /etc/nginx/sites-available/agentnet-upgrade-e2e
  sudo systemctl reload nginx >/dev/null 2>&1
  sudo sed -i '/# agentnet-upgrade-e2e$/d' /etc/hosts
  sudo rm -f \
    /usr/local/share/ca-certificates/agentnet-upgrade-e2e-root.crt \
    /etc/ssl/certs/agentnet-upgrade-e2e.crt \
    /etc/ssl/certs/agentnet-upgrade-e2e.pem \
    /etc/ssl/private/agentnet-upgrade-e2e.key
  sudo update-ca-certificates >/dev/null 2>&1
  sudo -u postgres dropdb --if-exists agentnet >/dev/null 2>&1
  sudo -u postgres dropuser --if-exists agentnet >/dev/null 2>&1
  if [[ -n "$HBA_FILE" ]] && sudo test -f "$HBA_FILE"; then
    sudo sed -i '/# agentnet-upgrade-e2e$/d' "$HBA_FILE"
    sudo -u postgres psql -Atq --dbname=postgres -c 'SELECT pg_reload_conf()' >/dev/null 2>&1
  fi
  sudo rm -rf "$PREFIX_0144" "$PREFIX_0145"
  sudo chown "$OPT_UID:$OPT_GID" /opt
  sudo chmod "$OPT_MODE" /opt
  rm -rf "$WORK"
}
trap cleanup EXIT

run_evidence() {
  local output="$1"
  local stderr_output="$output.stderr"
  shift
  local exit_code=0
  "$@" >"$output" 2>"$stderr_output" || exit_code=$?
  if [[ "$exit_code" -ne 0 ]]; then
    jq -c '{schema, status, blocker, message}' "$output" >&2 || true
    return "$exit_code"
  fi
}

install_runtime() {
  local prefix="$1"
  local package_tarball="$2"
  local expected_version="$3"
  sudo install -o root -g root -m 0755 -d "$prefix/bin"
  sudo install -o root -g root -m 0755 "$(command -v node)" "$prefix/bin/node"
  sudo install -o root -g root -m 0755 "$(command -v uv)" "$prefix/bin/uv"
  sudo -- sh -c 'umask 022; exec "$@"' sh \
    "$(command -v npm)" install --global --prefix "$prefix" \
    --bin-links=false --umask=0022 --ignore-scripts --no-audit --no-fund \
    "$package_tarball" >/dev/null
  local package_root="$prefix/lib/node_modules/@misunders2d/agentnet"
  sudo chmod 0755 \
    "$prefix/lib" \
    "$prefix/lib/node_modules" \
    "$prefix/lib/node_modules/@misunders2d" \
    "$package_root"
  sudo chown -Rh root:root "$prefix"
  sudo test ! -e "$prefix/bin/agentnet"
  sudo test "$(sudo jq -r '.version' "$package_root/package.json")" = "$expected_version"
}

launcher() {
  local prefix="$1"
  printf '%s\n' "$prefix/lib/node_modules/@misunders2d/agentnet/npm/bin/agentnet.mjs"
}

plan_setup() {
  local prefix="$1"
  local output="$2"
  run_evidence "$output" env \
    PATH="$prefix/bin:/usr/bin:/bin" \
    NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
    "$prefix/bin/node" "$(launcher "$prefix")" \
    server-agent setup --request "$INPUTS/server-setup.json"
}

apply_setup() {
  local prefix="$1"
  local digest="$2"
  local output="$3"
  run_evidence "$output" sudo -- env \
    PATH="$prefix/bin:/usr/bin:/bin" \
    AGENTNET_UV="$prefix/bin/uv" \
    NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
    "$prefix/bin/node" "$(launcher "$prefix")" \
    server-agent setup --request "$INPUTS/server-setup.json" \
    --expected-request-digest "$digest" --apply --start
}

psql_agentnet() {
  sudo -u agentnet psql -X -Atq --dbname=agentnet "$@"
}

schema_version() {
  psql_agentnet -c "SELECT value FROM metadata WHERE key='schema_version'"
}

migration_catalog() {
  local maximum="$1"
  psql_agentnet -c "COPY (SELECT version,name,checksum FROM schema_migrations WHERE version <= $maximum ORDER BY version) TO STDOUT WITH (FORMAT csv, FORCE_QUOTE *)"
}

identity_snapshot() {
  psql_agentnet -c "COPY (SELECT d.domain_id,d.status,d.policy_revision,d.revocation_epoch,d.created_at,p.principal_id,p.oidc_issuer,p.oidc_subject,p.verified_email,p.status,p.created_at,h.harness_id,h.kind,h.display_name,h.status,h.binding_assurance,h.capabilities_json,h.credential_epoch,h.created_at,c.credential_id,c.key_id,c.public_key_pem,c.status,c.epoch,c.not_before,c.expires_at,e.challenge_id,e.transaction_digest,e.approved_receipt,e.consumed_at FROM domains d JOIN principals p ON p.domain_id=d.domain_id JOIN harnesses h ON h.principal_id=p.principal_id JOIN credentials c ON c.harness_id=h.harness_id JOIN enrollment_challenges e ON e.domain_id=d.domain_id ORDER BY p.principal_id,h.harness_id,c.credential_id,e.challenge_id) TO STDOUT WITH (FORMAT csv, FORCE_QUOTE *)" | sha256sum | cut -d' ' -f1
}

message_snapshot() {
  psql_agentnet -c "COPY (SELECT e.event_id,e.domain_id,e.actor_json,e.event_type,e.classification,e.payload_encrypted,e.payload_digest,e.envelope_digest,e.envelope_json,e.idempotency_key,e.acceptance_fact,e.created_at,e.delivery_expires_at,e.effect_deadline,e.retention_delete_at,e.legal_hold,e.policy_revision,e.credential_epoch,r.recipient_id,r.cursor,r.current_fact,r.updated_at,r.attempts,rc.receipt_id,rc.fact,rc.owner_actor_json,rc.event_digest,rc.detail_json,rc.signature,rc.created_at FROM events e JOIN recipients r ON r.event_id=e.event_id JOIN receipts rc ON rc.event_id=e.event_id ORDER BY e.event_id,r.recipient_id,rc.receipt_id) TO STDOUT WITH (FORMAT csv, FORCE_QUOTE *)" | sha256sum | cut -d' ' -f1
}

security_snapshot() {
  local maximum_sequence="$1"
  psql_agentnet -c "COPY (SELECT sequence,occurred_at,record_json,previous_hash,record_hash FROM audit_log WHERE sequence <= $maximum_sequence ORDER BY sequence) TO STDOUT WITH (FORMAT csv, FORCE_QUOTE *)" | sha256sum | cut -d' ' -f1
}

database_fingerprint() {
  sudo -u agentnet pg_dump \
    --no-owner --no-privileges \
    --exclude-table-data=public.runtime_leases \
    --restrict-key=AgentNetUpgradeState0144 \
    --dbname=agentnet | sha256sum | cut -d' ' -f1
}

managed_files_fingerprint() {
  sudo sha256sum \
    /var/lib/agentnet/agentnet.json \
    /var/lib/agentnet/oidc-enrollment.json \
    /var/lib/agentnet/server-agent-identity.json \
    /var/lib/agentnet/guided-join.key.pem \
    /var/lib/agentnet-approval/config.json \
    /etc/agentnet-secrets/core.env \
    /etc/agentnet-secrets/approval.env \
    /etc/systemd/system/agentnet-core.service \
    /etc/systemd/system/agentnet-approval.service \
    /etc/systemd/system/agentnet-c0-responder.service \
    /etc/systemd/system/agentnet-credential-renew.service \
    /etc/systemd/system/agentnet-credential-renew.timer | sha256sum | cut -d' ' -f1
}

assert_schema_six_source() {
  [[ "$(schema_version)" == "6" ]]
  [[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM schema_migrations')" == "6" ]]
  [[ "$(psql_agentnet -c 'SELECT MIN(version)||'\''|'\''||MAX(version) FROM schema_migrations')" == "1|6" ]]
  [[ "$(psql_agentnet -c "SELECT to_regclass('public.endpoint_lifecycle') IS NULL")" == "t" ]]
}

assert_preserved_snapshots() {
  [[ "$(identity_snapshot)" == "$IDENTITY_0144" ]]
  [[ "$(message_snapshot)" == "$MESSAGES_0144" ]]
  [[ "$(security_snapshot "$AUDIT_MAX_0144")" == "$SECURITY_0144" ]]
  [[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM principals')" == "1" ]]
  [[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM harnesses')" == "1" ]]
  [[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM credentials')" == "1" ]]
  [[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM events')" == "1" ]]
  [[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM recipients')" == "1" ]]
}

assert_source_rollback() {
  sudo jq -e '.package_version == "0.1.44" and .artifact_mode == "disabled" and (.units | length) == 5' /var/lib/agentnet-setup/setup.json >/dev/null
  [[ "$(sudo sha256sum /var/lib/agentnet-setup/setup.json | cut -d' ' -f1)" == "$MARKER_0144" ]]
  [[ "$(managed_files_fingerprint)" == "$FILES_0144" ]]
  assert_schema_six_source
  [[ "$(migration_catalog 6 | sha256sum | cut -d' ' -f1)" == "$CATALOG_0144" ]]
  [[ "$(database_fingerprint)" == "$DATABASE_0144" ]]
  assert_preserved_snapshots
  sudo test ! -e /var/lib/agentnet-setup/upgrade.json
  sudo systemctl is-active --quiet agentnet-core.service
  sudo systemctl is-active --quiet agentnet-approval.service
}

# Install exact released and candidate packed bytes into independent immutable
# roots. The candidate must be the packed 0.1.45 tree, never an implicit cwd.
[[ "$(node -p "require('./package.json').version")" == "0.1.45" ]]
sudo chown root:root /opt
sudo chmod 0755 /opt
RELEASED_TARBALL="$(npm pack @misunders2d/agentnet@0.1.44 --ignore-scripts --pack-destination "$PACK" --silent)"
CANDIDATE_TARBALL="$(npm pack --ignore-scripts --pack-destination "$PACK" --silent)"
[[ "$RELEASED_TARBALL" == "misunders2d-agentnet-0.1.44.tgz" ]]
[[ "$CANDIDATE_TARBALL" == "misunders2d-agentnet-0.1.45.tgz" ]]
[[ "$(sha256sum "$PACK/$RELEASED_TARBALL" | cut -d' ' -f1)" != "$(sha256sum "$PACK/$CANDIDATE_TARBALL" | cut -d' ' -f1)" ]]
install_runtime "$PREFIX_0144" "$PACK/$RELEASED_TARBALL" "0.1.44"
install_runtime "$PREFIX_0145" "$PACK/$CANDIDATE_TARBALL" "0.1.45"

# Operator-owned disposable local TLS routes.
echo '127.0.0.1 core.agentnet.test approval.agentnet.test # agentnet-upgrade-e2e' | sudo tee -a /etc/hosts >/dev/null
cat >"$WORK/openssl.cnf" <<'EOF'
[req]
prompt = no
distinguished_name = dn
req_extensions = extensions
[dn]
CN = core.agentnet.test
[extensions]
subjectAltName = @alt_names
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
[alt_names]
DNS.1 = core.agentnet.test
DNS.2 = approval.agentnet.test
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -sha256 \
  -subj '/CN=AgentNet upgrade E2E root' \
  -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -addext 'subjectKeyIdentifier=hash' \
  -keyout "$WORK/root-ca.key" -out "$WORK/root-ca.crt" >/dev/null 2>&1
openssl req -new -newkey rsa:2048 -nodes -sha256 \
  -keyout "$WORK/tls.key" -out "$WORK/tls.csr" -config "$WORK/openssl.cnf" >/dev/null 2>&1
openssl x509 -req -days 1 -sha256 \
  -in "$WORK/tls.csr" -CA "$WORK/root-ca.crt" -CAkey "$WORK/root-ca.key" \
  -CAcreateserial -extfile "$WORK/openssl.cnf" -extensions extensions \
  -out "$WORK/tls.crt" >/dev/null 2>&1
sudo install -o root -g root -m 0644 "$WORK/root-ca.crt" /usr/local/share/ca-certificates/agentnet-upgrade-e2e-root.crt
sudo update-ca-certificates >/dev/null
sudo install -o root -g root -m 0600 "$WORK/tls.key" /etc/ssl/private/agentnet-upgrade-e2e.key
sudo install -o root -g root -m 0644 "$WORK/tls.crt" /etc/ssl/certs/agentnet-upgrade-e2e.crt
sudo tee /etc/nginx/sites-available/agentnet-upgrade-e2e >/dev/null <<'EOF'
server {
  listen 443 ssl;
  server_name core.agentnet.test;
  ssl_certificate /etc/ssl/certs/agentnet-upgrade-e2e.crt;
  ssl_certificate_key /etc/ssl/private/agentnet-upgrade-e2e.key;
  location / { proxy_pass http://127.0.0.1:8080; }
}
server {
  listen 443 ssl;
  server_name approval.agentnet.test;
  ssl_certificate /etc/ssl/certs/agentnet-upgrade-e2e.crt;
  ssl_certificate_key /etc/ssl/private/agentnet-upgrade-e2e.key;
  location / { proxy_pass http://127.0.0.1:8090; }
}
EOF
sudo ln -s /etc/nginx/sites-available/agentnet-upgrade-e2e /etc/nginx/sites-enabled/agentnet-upgrade-e2e
sudo nginx -t
sudo systemctl restart nginx

# Disposable request inputs contain synthetic CI-only credentials and no
# repository, organization, environment, or long-lived secret.
cat >"$INPUTS/core.env" <<EOF
AGENTNET_DATABASE_URL=postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet
AGENTNET_CORE_OIDC_CLIENT_SECRET=synthetic-ci-secret
AGENTNET_APPROVAL_CORE_TOKEN=$TOKEN
EOF
cat >"$INPUTS/approval.env" <<EOF
AGENTNET_APPROVAL_OIDC_CLIENT_SECRET=synthetic-ci-secret
AGENTNET_APPROVAL_CORE_TOKEN=$TOKEN
EOF
cat >"$INPUTS/core-oidc.json" <<'EOF'
{"issuer":"https://accounts.example","client_id":"core-client","redirect_uri":"https://core.agentnet.test/v1/enrollment/oidc/callback","token_endpoint_auth_method":"client_secret_post","client_secret_env":"AGENTNET_CORE_OIDC_CLIENT_SECRET","allowed_endpoint_origins":["https://accounts.example"],"allowed_signing_algorithms":["RS256"],"binding_assurance":"hardware_bound"}
EOF
cat >"$INPUTS/approval-owner-oidc.json" <<'EOF'
{"issuer":"https://accounts.example","client_id":"approval-client","redirect_uri":"https://approval.agentnet.test/v1/approval/owner/oidc/callback","token_endpoint_auth_method":"client_secret_post","client_secret_env":"AGENTNET_APPROVAL_OIDC_CLIENT_SECRET","allowed_endpoint_origins":["https://accounts.example"],"allowed_signing_algorithms":["RS256"]}
EOF
cat >"$INPUTS/approvers.json" <<'EOF'
{"approvers":[{"principal_id":"owner-principal","authority_kind":"human","domain_id":"agentnet.test","allowed_purposes":["authorization.bootstrap_plan.approve","authorization.communication_scope.approve","authorization.elevation.approve","identity.credential.recover.approve","identity.enrollment.approve","identity.harness.revoke.approve","organization.relationship.accept"],"oidc_issuer":"https://accounts.example","oidc_subject":"owner-subject"}]}
EOF
cat >"$INPUTS/server-setup.json" <<EOF
{"schema":"agentnet.server-setup.request.v2","profile":"always_on_server_agent","artifact_mode":"disabled","domain_id":"agentnet.test","service_audience":"urn:agentnet:agentnet.test:corporate-api","runtime_instance_id":"ordinary-server-upgrade-e2e","core_public_origin":"https://core.agentnet.test","approval_public_origin":"https://approval.agentnet.test","database_url":"postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet","database_url_env":"AGENTNET_DATABASE_URL","core_environment_file":"$INPUTS/core.env","approval_environment_file":"$INPUTS/approval.env","oidc_provider_file":"$INPUTS/core-oidc.json","approval_owner_oidc_file":"$INPUTS/approval-owner-oidc.json","approval_approvers_file":"$INPUTS/approvers.json","approval_approver_principal_id":"owner-principal","approval_verifier_id":"approval.agentnet.test"}
EOF
chmod 600 "$INPUTS"/*

# Disposable PostgreSQL prerequisite.
sudo -u postgres createuser --login agentnet
sudo -u postgres createdb --owner=agentnet agentnet
HBA_FILE="$(sudo -u postgres psql -Atq --dbname=postgres -c 'SHOW hba_file')"
sudo sed -i '1ilocal agentnet agentnet peer # agentnet-upgrade-e2e' "$HBA_FILE"
sudo -u postgres psql -Atq --dbname=postgres -c 'SELECT pg_reload_conf()' | grep -qx 't'
# The agentnet OS account does not exist until the released package creates it,
# so peer readiness is proven from the operator role, not by impersonation.
CONFIG_LOADED=false
for _ in $(seq 1 50); do
  if sudo -u postgres psql -Atq --dbname=postgres -c \
    "SELECT pg_conf_load_time() >= (pg_stat_file(current_setting('hba_file'))).modification" \
    | grep -qx 't'; then
    CONFIG_LOADED=true
    break
  fi
  sleep 0.2
done
[[ "$CONFIG_LOADED" == "true" ]]
sudo -u postgres psql -Atq --dbname=postgres -c \
  "SELECT count(*) FROM pg_hba_file_rules WHERE type='local' AND database=ARRAY['agentnet'] AND user_name=ARRAY['agentnet'] AND auth_method='peer' AND error IS NULL" \
  | grep -qx '1'

# Realize exact public 0.1.44 bytes and its schema-v6 five-unit marker.
PLAN_0144="$WORK/plan-0.1.44.json"
APPLY_0144="$WORK/apply-0.1.44.json"
APPLY_BOUND_0144="$WORK/apply-bound-0.1.44.json"
plan_setup "$PREFIX_0144" "$PLAN_0144"
jq -e '.status == "planned" and .identity_enrolled == false' "$PLAN_0144" >/dev/null
DIGEST_0144="$(jq -r '.request_digest' "$PLAN_0144")"
[[ "$DIGEST_0144" =~ ^[a-f0-9]{64}$ ]]
apply_setup "$PREFIX_0144" "$DIGEST_0144" "$APPLY_0144"
jq -e '.status == "waiting_owner_oidc_or_passkey" and .identity_enrolled == false' "$APPLY_0144" >/dev/null
sudo jq -e '.package_version == "0.1.44" and .artifact_mode == "disabled" and (.units | length) == 5' /var/lib/agentnet-setup/setup.json >/dev/null
assert_schema_six_source
sudo test ! -e /var/lib/agentnet-setup/upgrade.json

# Use only the released packaged Python runtime to create one cryptographically
# completed isolated enrollment and one durable self-addressed message. This is
# synthetic CI state, not production enrollment evidence.
PACKAGE_ROOT_0144="$PREFIX_0144/lib/node_modules/@misunders2d/agentnet"
INSTALL_ID_0144="$(printf '%s' "$PACKAGE_ROOT_0144" | sha256sum | cut -c1-12)"
PYTHON_0144="/var/lib/agentnet/npm-runtime/0.1.44-$INSTALL_ID_0144/bin/python"
AGENTNET_0144="/var/lib/agentnet/npm-runtime/0.1.44-$INSTALL_ID_0144/bin/agentnet"
sudo test -x "$PYTHON_0144"
sudo test -x "$AGENTNET_0144"
cat >"$WORK/seed-released-state.py" <<'PY'
from __future__ import annotations

import json
import time
from datetime import UTC, datetime, timedelta
from pathlib import Path

from agentnet.approval import IndependentApprovalVerifier, TrustedApprover, create_independent_approval_receipt
from agentnet.authorization.communication_scope import COMMUNICATION_SCOPE_ACTIONS
from agentnet.core.app import CommunicationCore
from agentnet.identity.enrollment import ENROLLMENT_APPROVAL_PURPOSE, EnrollmentService, VerifiedOIDCIdentity
from agentnet.messaging.events import new_event
from agentnet.operations.config import RuntimeProfile
from agentnet.operations.config_migration import load_config_json
from agentnet.protocol.models import Classification, EventType
from agentnet.security.signatures import P256KeyPair

config_path = Path("/var/lib/agentnet/agentnet.json")
identity_path = Path("/var/lib/agentnet/server-agent-identity.json")
key_path = Path("/var/lib/agentnet/guided-join.key.pem")
config = load_config_json(config_path.read_text(encoding="utf-8")).model_copy(
    update={"runtime_instance_id": "ordinary-server-upgrade-e2e-seed"}
)
core = CommunicationCore.open(config, validate_deployment_identity=False)
try:
    now = int(time.time())
    approver_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="owner-principal",
        domain_id="agentnet.test",
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({ENROLLMENT_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver}, verifier_id="approval.agentnet.test"
    )
    enrollment = EnrollmentService(
        core.store,
        verifier,
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        binding_assurance="os_bound",
        credential_ttl=86400,
        clock=lambda: now,
    )
    def enroll(harness_name: str) -> tuple[object, object]:
        key = P256KeyPair.generate()
        with core.store.transaction() as connection:
            challenge = enrollment._begin_in_transaction(
                connection,
                domain_id="agentnet.test",
                identity=identity,
                harness_kind="server",
                harness_name=harness_name,
                public_key_pem=key.public_pem,
                now=now,
            )
        approval = create_independent_approval_receipt(
            approver_key,
            approver=approver,
            verifier_id=verifier.verifier_id,
            approval_purpose=ENROLLMENT_APPROVAL_PURPOSE,
            canonical_transaction=challenge.canonical_transaction,
            issued_at=now,
            authenticated_at=now,
            expires_at=now + 300,
        )
        completed = enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=key.sign(
                "agentnet.enrollment.pop.v1", challenge.signed_fields()
            ),
            approval=approval,
        )
        return completed.actor, key

    identity = VerifiedOIDCIdentity(
        issuer="https://accounts.example",
        subject="owner-subject",
        verified_email="owner@agentnet.test",
    )
    actor, key = enroll("ordinary-server-upgrade-e2e")
    fresh_actor, _fresh_key = enroll("ordinary-server-upgrade-e2e-fresh")

    # Synthetic committed v0.1.44 communication authority.  Real completion
    # needs an interactive owner passkey, so the exact committed row shape is
    # written directly; the v6->v7 authority migration under test is the
    # released product code, not this fixture.
    scope_id = "scope-upgrade-e2e-v6"
    with core.store.transaction() as connection:
        domain = connection.execute(
            "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=%s",
            ("agentnet.test",),
        ).fetchone()
        policy_revision = int(domain["policy_revision"])
        connection.execute(
            """INSERT INTO communication_scopes(
                scope_id,profile,profile_version,domain_id,principal_id,owner_harness_id,
                fresh_harness_id,owner_credential_id,fresh_credential_id,
                owner_credential_epoch,fresh_credential_epoch,domain_revocation_epoch,
                policy_revision,actor_binding_json,canonical_scope_preimage_json,
                final_approval_transaction_json,scope_digest,transaction_digest,
                begin_idempotency_key_sha256,state,created_at,approval_expires_at,
                approval_create_idempotency_key,approval_create_request_digest,
                approval_request_id,approval_issued_at,completion_reserved_at,
                completion_idempotency_key_sha256,completion_request_digest,
                approval_receipt_id,approval_receipt_digest,committed_at,
                committed_result_encrypted,committed_result_digest,audit_record_hash
            ) VALUES(%s,%s,1,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,'{}','{}','{}',
                %s,%s,%s,'committed',%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)""",
            (
                scope_id,
                "same-principal-full-communication:v1",
                "agentnet.test",
                actor.principal_id,
                actor.harness_id,
                fresh_actor.harness_id,
                actor.credential_id,
                fresh_actor.credential_id,
                int(actor.credential_epoch),
                int(fresh_actor.credential_epoch),
                int(domain["revocation_epoch"]),
                policy_revision,
                "b" * 64,
                "c" * 64,
                "d" * 64,
                now - 300,
                now + 3_600,
                "approval-create-upgrade-e2e",
                "e" * 64,
                "approval-request-upgrade-e2e",
                now - 240,
                now - 180,
                "f" * 64,
                "1" * 64,
                "approval-receipt-upgrade-e2e",
                "2" * 64,
                now - 120,
                "encrypted-result",
                "3" * 64,
                "4" * 64,
            ),
        )
        ordinal = 0
        for harness_id in (actor.harness_id, fresh_actor.harness_id):
            for action in sorted(COMMUNICATION_SCOPE_ACTIONS):
                ordinal += 1
                entitlement_id = f"entitlement-upgrade-e2e-{ordinal}"
                connection.execute(
                    "INSERT INTO entitlements(entitlement_id,domain_id,principal_id,action,"
                    "resource_pattern,expires_at,revoked_at,revision) "
                    "VALUES(%s,%s,%s,%s,'*',NULL,NULL,%s)",
                    (
                        entitlement_id,
                        "agentnet.test",
                        actor.principal_id,
                        action,
                        policy_revision,
                    ),
                )
                connection.execute(
                    """INSERT INTO communication_scope_items(
                        scope_id,item_ordinal,item_id,entitlement_id,harness_id,action,
                        resource_pattern,item_json,expires_at
                    ) VALUES(%s,%s,%s,%s,%s,%s,'*','{}',NULL)""",
                    (
                        scope_id,
                        ordinal,
                        f"item-upgrade-e2e-{ordinal}",
                        entitlement_id,
                        harness_id,
                        action,
                    ),
                )

    key_path.write_bytes(key.private_pem)
    key_path.chmod(0o600)
    identity_path.write_text(
        json.dumps(
            {
                "schema": "agentnet.identity-profile.v1",
                "server_base_url": "https://core.agentnet.test",
                "audience": "urn:agentnet:agentnet.test:corporate-api",
                "actor": actor.model_dump(mode="json"),
                "private_key_path": str(key_path),
            },
            sort_keys=True,
            separators=(",", ":"),
        ) + "\n",
        encoding="utf-8",
    )
    identity_path.chmod(0o600)
    event = new_event(
        domain_id="agentnet.test",
        actor=actor,
        event_id="00000000-0000-4000-8000-000000000144",
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"schema": "agentnet.upgrade-e2e.message.v1", "text": "preserve exact released message"},
        idempotency_key="ordinary-server-upgrade-e2e-0.1.44",
        recipients=(actor.harness_id,),
        retention_delete_at=datetime.now(UTC) + timedelta(days=1),
        policy_revision=1,
    )
    accepted = core.mailboxes.accept(event)
    print(
        json.dumps(
            {
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
                "fresh_harness_id": fresh_actor.harness_id,
                "communication_scope_id": scope_id,
                "event_id": accepted["event_id"],
            },
            sort_keys=True,
        )
    )
finally:
    core.close()
PY
sudo install -o agentnet -g agentnet -m 0700 "$WORK/seed-released-state.py" /var/lib/agentnet/seed-released-state.py
sudo -u agentnet env PYTHONDONTWRITEBYTECODE=1 "$PYTHON_0144" /var/lib/agentnet/seed-released-state.py >"$WORK/released-fixture.json"
sudo rm -f /var/lib/agentnet/seed-released-state.py
HARNESS_ID="$(jq -r '.harness_id' "$WORK/released-fixture.json")"
CREDENTIAL_ID="$(jq -r '.credential_id' "$WORK/released-fixture.json")"
FRESH_HARNESS_ID="$(jq -r '.fresh_harness_id' "$WORK/released-fixture.json")"
SCOPE_ID="$(jq -r '.communication_scope_id' "$WORK/released-fixture.json")"
[[ "$HARNESS_ID" =~ ^[0-9a-f-]{36}$ ]]
[[ "$CREDENTIAL_ID" =~ ^[0-9a-f-]{36}$ ]]
[[ "$FRESH_HARNESS_ID" =~ ^[0-9a-f-]{36}$ ]]
[[ "$FRESH_HARNESS_ID" != "$HARNESS_ID" ]]
[[ "$SCOPE_ID" == "scope-upgrade-e2e-v6" ]]
sudo systemctl stop agentnet-core.service
[[ "$(sudo systemctl show agentnet-core.service --property=ActiveState --value)" == "inactive" ]]
sudo -u agentnet env PYTHONDONTWRITEBYTECODE=1 \
  "$AGENTNET_0144" server-agent activate \
  --config /var/lib/agentnet/agentnet.json \
  --identity /var/lib/agentnet/server-agent-identity.json >"$WORK/activate-0.1.44.json"
jq -e --arg harness "$HARNESS_ID" --arg credential "$CREDENTIAL_ID" \
  '.activated == true and .harness_id == $harness and .credential_id == $credential and .authority_granted == false' \
  "$WORK/activate-0.1.44.json" >/dev/null
apply_setup "$PREFIX_0144" "$DIGEST_0144" "$APPLY_BOUND_0144"
jq -e '.status == "operational" and .identity_enrolled == true and .authority_granted == false' "$APPLY_BOUND_0144" >/dev/null

# Capture nonempty released identity/message/security state and exact rollback
# baselines before any candidate plan or mutation.
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM enrollment_challenges WHERE consumed_at IS NOT NULL')" == "2" ]]
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM events')" == "1" ]]
[[ "$(psql_agentnet -c "SELECT COUNT(*) FROM communication_scopes WHERE scope_id='$SCOPE_ID' AND state='committed'")" == "1" ]]
[[ "$(psql_agentnet -c "SELECT COUNT(*) FROM communication_scope_items WHERE scope_id='$SCOPE_ID'")" == "38" ]]
AUDIT_MAX_0144="$(psql_agentnet -c 'SELECT MAX(sequence) FROM audit_log')"
[[ "$AUDIT_MAX_0144" =~ ^[1-9][0-9]*$ ]]
IDENTITY_0144="$(identity_snapshot)"
MESSAGES_0144="$(message_snapshot)"
SECURITY_0144="$(security_snapshot "$AUDIT_MAX_0144")"
CATALOG_0144="$(migration_catalog 6 | sha256sum | cut -d' ' -f1)"
MARKER_0144="$(sudo sha256sum /var/lib/agentnet-setup/setup.json | cut -d' ' -f1)"
REVISION_0144="$(sudo jq -r '.revision' /var/lib/agentnet-setup/setup.json)"
FILES_0144="$(managed_files_fingerprint)"
DATABASE_0144="$(database_fingerprint)"
OLD_CORE_PID="$(sudo systemctl show agentnet-core.service --property=MainPID --value)"
OLD_APPROVAL_PID="$(sudo systemctl show agentnet-approval.service --property=MainPID --value)"
[[ "$OLD_CORE_PID" =~ ^[1-9][0-9]*$ ]]
[[ "$OLD_APPROVAL_PID" =~ ^[1-9][0-9]*$ ]]

PLAN_0145="$WORK/plan-0.1.45.json"
APPLY_ROLLBACK="$WORK/apply-rollback-0.1.45.json"
APPLY_0145="$WORK/apply-0.1.45.json"
plan_setup "$PREFIX_0145" "$PLAN_0145"
DIGEST_0145="$(jq -r '.request_digest' "$PLAN_0145")"
[[ "$DIGEST_0145" =~ ^[a-f0-9]{64}$ ]]
[[ "$DIGEST_0145" != "$DIGEST_0144" ]]
assert_source_rollback

# Inject one deterministic post-migration public-health failure without changing
# package bytes. Observing committed schema 7 proves the failure crossed the
# migration boundary; stopping the disposable nginx route then forces rollback.
(
  for _ in $(seq 1 2400); do
    if [[ "$(schema_version 2>/dev/null || true)" == "7" ]]; then
      sudo systemctl stop nginx
      : >"$WORK/schema-seven-observed"
      exit 0
    fi
    sleep 0.05
  done
  exit 1
) &
INJECT_PID="$!"
ROLLBACK_EXIT=0
apply_setup "$PREFIX_0145" "$DIGEST_0145" "$APPLY_ROLLBACK" || ROLLBACK_EXIT=$?
wait "$INJECT_PID"
INJECT_PID=""
[[ "$ROLLBACK_EXIT" -eq 1 ]]
[[ -f "$WORK/schema-seven-observed" ]]
jq -e '.schema == "agentnet.server-setup.evidence.v1" and .status == "blocked" and .blocker == "service_health" and .identity_enrolled == true and .authority_granted == false' "$APPLY_ROLLBACK" >/dev/null
sudo systemctl start nginx
assert_source_rollback
env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" curl --fail --silent --show-error https://core.agentnet.test/healthz >/dev/null
env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" curl --fail --silent --show-error https://approval.agentnet.test/healthz >/dev/null

# Retry the same immutable candidate bytes and prove the exact successful
# marker/catalog transition, preservation, and endpoint restart postcondition.
apply_setup "$PREFIX_0145" "$DIGEST_0145" "$APPLY_0145"
jq -e --arg endpoint "$HARNESS_ID" '
  .status == "operational"
  and .identity_enrolled == true
  and .authority_granted == false
  and .endpoint_lifecycle.endpoint_id == $endpoint
  and .endpoint_lifecycle.state == "restart_required"
  and .endpoint_lifecycle.public_url == "https://core.agentnet.test"
  and .endpoint_lifecycle.identity_created == false
' "$APPLY_0145" >/dev/null
sudo jq -e --arg previous "$MARKER_0144" --argjson revision "$REVISION_0144" '
  .package_version == "0.1.45"
  and .artifact_mode == "disabled"
  and (.units | length) == 5
  and .revision == ($revision + 1)
  and .previous_marker_digest == $previous
' /var/lib/agentnet-setup/setup.json >/dev/null
[[ "$(schema_version)" == "7" ]]
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM schema_migrations')" == "7" ]]
[[ "$(psql_agentnet -c 'SELECT MIN(version)||'\''|'\''||MAX(version) FROM schema_migrations')" == "1|7" ]]
[[ "$(migration_catalog 6 | sha256sum | cut -d' ' -f1)" == "$CATALOG_0144" ]]
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM schema_migrations WHERE version=7 AND length(name)>0 AND checksum ~ '\''^[a-f0-9]{64}$'\''')" == "1" ]]
[[ "$(psql_agentnet -c "SELECT COUNT(*) FROM endpoint_lifecycle WHERE domain_id='agentnet.test' AND harness_id='$HARNESS_ID' AND principal_id=(SELECT principal_id FROM harnesses WHERE harness_id='$HARNESS_ID') AND current_credential_id='$CREDENTIAL_ID' AND harness_kind='server' AND profile_key='ordinary-server-upgrade-e2e' AND state='restart_required' AND adapter_generation=1 AND mailbox_cursor=(SELECT MAX(cursor) FROM recipients) AND capability_root_digest IS NULL AND process_measurement IS NULL AND state_reason='explicit_user_restart_required' AND revision=2")" == "1" ]]
# The v6 committed communication authority must survive as exact usable v7
# collaboration authority bound to the same two enrolled harnesses.
[[ "$(psql_agentnet -c "SELECT COUNT(*) FROM collaboration_scopes WHERE scope_id='$SCOPE_ID' AND source_communication_scope_id='$SCOPE_ID' AND domain_id='agentnet.test' AND scope_kind='direct' AND state='active' AND state_reason='migrated_v6_communication_scope' AND owner_harness_id='$HARNESS_ID' AND owner_principal_id=(SELECT principal_id FROM harnesses WHERE harness_id='$HARNESS_ID') AND revoked_at IS NULL AND expires_at IS NULL")" == "1" ]]
[[ "$(psql_agentnet -c "SELECT COUNT(*) FROM collaboration_scope_members WHERE scope_id='$SCOPE_ID' AND state='active' AND authority_kind='principal' AND harness_id IN ('$HARNESS_ID','$FRESH_HARNESS_ID')")" == "2" ]]
[[ "$(psql_agentnet -c "SELECT role FROM collaboration_scope_members WHERE scope_id='$SCOPE_ID' AND harness_id='$HARNESS_ID'")" == "owner" ]]
[[ "$(psql_agentnet -c "SELECT role FROM collaboration_scope_members WHERE scope_id='$SCOPE_ID' AND harness_id='$FRESH_HARNESS_ID'")" == "member" ]]
[[ "$(psql_agentnet -c "SELECT allowed_actions_json::text LIKE '%message.send%' AND allowed_actions_json::text LIKE '%obligation.respond%' FROM collaboration_scopes WHERE scope_id='$SCOPE_ID'")" == "t" ]]
[[ "$(psql_agentnet -c "SELECT COUNT(*) FROM communication_scopes WHERE scope_id='$SCOPE_ID' AND state='committed'")" == "1" ]]
assert_preserved_snapshots
sudo test ! -e /var/lib/agentnet-setup/upgrade.json

# The managed listeners must belong only to new unit PIDs. No released process,
# released socket owner, upgrade journal, or temporary input secret may remain.
NEW_CORE_PID="$(sudo systemctl show agentnet-core.service --property=MainPID --value)"
NEW_APPROVAL_PID="$(sudo systemctl show agentnet-approval.service --property=MainPID --value)"
[[ "$NEW_CORE_PID" =~ ^[1-9][0-9]*$ && "$NEW_CORE_PID" != "$OLD_CORE_PID" ]]
[[ "$NEW_APPROVAL_PID" =~ ^[1-9][0-9]*$ && "$NEW_APPROVAL_PID" != "$OLD_APPROVAL_PID" ]]
! sudo test -e "/proc/$OLD_CORE_PID"
! sudo test -e "/proc/$OLD_APPROVAL_PID"
[[ "$(sudo ss -H -ltnp 'sport = :8080' | wc -l)" == "1" ]]
[[ "$(sudo ss -H -ltnp 'sport = :8090' | wc -l)" == "1" ]]
sudo ss -H -ltnp 'sport = :8080' | grep -Fq "pid=$NEW_CORE_PID"
sudo ss -H -ltnp 'sport = :8090' | grep -Fq "pid=$NEW_APPROVAL_PID"
sudo sh -c 'tr "\0" "\n" <"/proc/$1/cmdline"' sh "$NEW_CORE_PID" | grep -Fq "$PREFIX_0145"
sudo sh -c 'tr "\0" "\n" <"/proc/$1/cmdline"' sh "$NEW_APPROVAL_PID" | grep -Fq "$PREFIX_0145"
! sudo sh -c 'tr "\0" "\n" <"/proc/$1/environ"' sh "$NEW_CORE_PID" | grep -Fq "$PREFIX_0144"
! sudo sh -c 'tr "\0" "\n" <"/proc/$1/environ"' sh "$NEW_APPROVAL_PID" | grep -Fq "$PREFIX_0144"
! grep -Fq "$TOKEN" \
  "$PLAN_0144" "$PLAN_0144.stderr" "$APPLY_0144" "$APPLY_0144.stderr" \
  "$APPLY_BOUND_0144" "$APPLY_BOUND_0144.stderr" \
  "$PLAN_0145" "$PLAN_0145.stderr" \
  "$APPLY_ROLLBACK" "$APPLY_ROLLBACK.stderr" \
  "$APPLY_0145" "$APPLY_0145.stderr"
rm -rf "$INPUTS" "$WORK/seed-released-state.py" "$WORK/root-ca.key" "$WORK/tls.key" "$WORK/tls.csr"
[[ ! -e "$INPUTS" && ! -e "$WORK/root-ca.key" && ! -e "$WORK/tls.key" ]]
unset TOKEN

echo "ordinary server 0.1.44 to 0.1.45 upgrade E2E: PASS"
