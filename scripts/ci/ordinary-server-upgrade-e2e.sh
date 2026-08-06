#!/usr/bin/env bash
set -Eeuo pipefail

# Destructive only inside a fresh GitHub-hosted Ubuntu 24.04 runner. The lane
# installs two independent npm tarballs, realizes the released 0.1.45 service,
# and proves the supported state-preserving 0.1.45 -> 0.1.46 timer repair.
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
PREFIX_0144="/opt/agentnet-upgrade-e2e-0.1.45"
PREFIX_0145="/opt/agentnet-upgrade-e2e-0.1.46"
NO_PROXY_VALUE="127.0.0.1,localhost,.agentnet.test,core.agentnet.test,approval.agentnet.test"
OPT_UID="$(stat -c '%u' /opt)"
OPT_GID="$(stat -c '%g' /opt)"
OPT_MODE="$(stat -c '%a' /opt)"
HBA_FILE=""
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
  local unit
  for unit in agentnet-core.service agentnet-approval.service \
    agentnet-c0-responder.service agentnet-credential-renew.timer \
    agentnet-credential-renew-e2e.timer agentnet-upgrade-e2e.timer; do
    if sudo systemctl cat "$unit" >/dev/null 2>&1; then
      echo "--- $unit ---" >&2
      sudo systemctl show "$unit" \
        --property=LoadState --property=UnitFileState --property=ActiveState \
        --property=SubState --property=Result --property=ExecMainStatus >&2
      sudo journalctl -u "$unit" --no-pager -n 12 >&2 || true
    fi
  done
  return "$status"
}

wait_for_public_health() {
  local url="$1"
  local attempt
  for attempt in $(seq 1 120); do
    if env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
      curl --fail --silent --show-error "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "public origin $url never became healthy" >&2
  env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
    curl --fail --silent --show-error "$url" >&2 || true
  return 1
}

wait_for_renewal_request_after() {
  local previous="$1"
  local request_id=""
  for _ in $(seq 1 120); do
    request_id="$(sudo jq -r '
      select(.schema == "agentnet.credential-renewal-cli-state.v1")
      | .request_id
    ' /var/lib/agentnet/credential-renewal.json 2>/dev/null || true)"
    if [[ "$request_id" =~ ^[0-9a-f-]{36}$ && "$request_id" != "$previous" ]]; then
      printf '%s\n' "$request_id"
      return 0
    fi
    sleep 0.25
  done
  echo "credential renewal did not complete another invocation" >&2
  return 1
}

assert_credential_renewal_recurs() {
  local test_timer="agentnet-credential-renew-e2e.timer"
  local test_path="/run/systemd/system/$test_timer"
  local baseline_request first_request second_request first_next second_next
  baseline_request="$(sudo jq -r '.request_id // ""' /var/lib/agentnet/credential-renewal.json 2>/dev/null || true)"
  sudo systemctl stop agentnet-credential-renew.timer
  sudo sed \
    -e 's/^Description=.*/Description=Accelerated AgentNet credential renewal E2E/' \
    -e 's/^OnActiveSec=.*/OnActiveSec=1s/' \
    -e 's/^OnUnitInactiveSec=1h$/OnUnitInactiveSec=5s/' \
    /etc/systemd/system/agentnet-credential-renew.timer |
    sudo tee "$test_path" >/dev/null
  sudo systemctl daemon-reload
  sudo systemctl start "$test_timer"

  first_request="$(wait_for_renewal_request_after "$baseline_request")"
  first_next="$(sudo systemctl list-timers --all "$test_timer" --output=json |
    jq -er '.[0].next | select(type == "number" and . > 0)')"
  second_request="$(wait_for_renewal_request_after "$first_request")"
  second_next="$(sudo systemctl list-timers --all "$test_timer" --output=json |
    jq -er '.[0].next | select(type == "number" and . > 0)')"
  [[ "$second_request" != "$first_request" ]]
  [[ "$second_next" -gt "$first_next" ]]
  [[ "$(sudo systemctl show agentnet-credential-renew.service --property=Result --value)" == "success" ]]
  [[ "$(sudo systemctl show agentnet-credential-renew.service --property=ExecMainStatus --value)" == "0" ]]

  sudo systemctl stop "$test_timer"
  sudo rm -f "$test_path"
  sudo systemctl daemon-reload
  sudo systemctl start agentnet-credential-renew.timer
  sudo systemctl list-timers --all agentnet-credential-renew.timer --output=json |
    jq -e '.[0].next | type == "number" and . > 0' >/dev/null
}
trap 'report_failure "$LINENO" "$BASH_COMMAND"' ERR

cleanup() {
  set +e
  sudo systemctl start nginx >/dev/null 2>&1
  sudo systemctl stop agentnet-credential-renew-e2e.timer >/dev/null 2>&1
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
  sudo rm -f /run/systemd/system/agentnet-credential-renew-e2e.timer
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

endpoint_lifecycle_snapshot() {
  psql_agentnet -c "COPY (SELECT * FROM endpoint_lifecycle ORDER BY domain_id,harness_id) TO STDOUT WITH (FORMAT csv, FORCE_QUOTE *)" | sha256sum | cut -d' ' -f1
}


assert_schema_seven_source() {
  [[ "$(schema_version)" == "7" ]]
  [[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM schema_migrations')" == "7" ]]
  [[ "$(psql_agentnet -c 'SELECT MIN(version)||'\''|'\''||MAX(version) FROM schema_migrations')" == "1|7" ]]
  [[ "$(psql_agentnet -c "SELECT to_regclass('public.endpoint_lifecycle') IS NOT NULL")" == "t" ]]
}


# Install exact released and candidate packed bytes into independent immutable
# roots. The candidate must be the packed 0.1.46 tree, never an implicit cwd.
[[ "$(node -p "require('./package.json').version")" == "0.1.46" ]]
sudo chown root:root /opt
sudo chmod 0755 /opt
RELEASED_TARBALL="$(npm pack @misunders2d/agentnet@0.1.45 --ignore-scripts --pack-destination "$PACK" --silent)"
CANDIDATE_TARBALL="$(npm pack --ignore-scripts --pack-destination "$PACK" --silent)"
[[ "$RELEASED_TARBALL" == "misunders2d-agentnet-0.1.45.tgz" ]]
[[ "$CANDIDATE_TARBALL" == "misunders2d-agentnet-0.1.46.tgz" ]]
[[ "$(sha256sum "$PACK/$RELEASED_TARBALL" | cut -d' ' -f1)" != "$(sha256sum "$PACK/$CANDIDATE_TARBALL" | cut -d' ' -f1)" ]]
install_runtime "$PREFIX_0144" "$PACK/$RELEASED_TARBALL" "0.1.45"
install_runtime "$PREFIX_0145" "$PACK/$CANDIDATE_TARBALL" "0.1.46"

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

# Realize exact public 0.1.45 bytes and its schema-v7 five-unit marker.
PLAN_0144="$WORK/plan-0.1.45.json"
APPLY_0144="$WORK/apply-0.1.45.json"
APPLY_BOUND_0144="$WORK/apply-bound-0.1.45.json"
plan_setup "$PREFIX_0144" "$PLAN_0144"
jq -e '.status == "planned" and .identity_enrolled == false' "$PLAN_0144" >/dev/null
DIGEST_0144="$(jq -r '.request_digest' "$PLAN_0144")"
[[ "$DIGEST_0144" =~ ^[a-f0-9]{64}$ ]]
apply_setup "$PREFIX_0144" "$DIGEST_0144" "$APPLY_0144"
jq -e '.status == "waiting_owner_oidc_or_passkey" and .identity_enrolled == false' "$APPLY_0144" >/dev/null
sudo jq -e '.package_version == "0.1.45" and .artifact_mode == "disabled" and (.units | length) == 5' /var/lib/agentnet-setup/setup.json >/dev/null
assert_schema_seven_source
sudo test ! -e /var/lib/agentnet-setup/upgrade.json

# Use only the released packaged Python runtime to create one cryptographically
# completed isolated enrollment and one durable self-addressed message. This is
# synthetic CI state, not production enrollment evidence.
PACKAGE_ROOT_0144="$PREFIX_0144/lib/node_modules/@misunders2d/agentnet"
INSTALL_ID_0144="$(printf '%s' "$PACKAGE_ROOT_0144" | sha256sum | cut -c1-12)"
# The Core unit points AGENTNET_NPM_RUNTIME_DIR at the Core data root's
# npm-runtime directory, which the launcher uses verbatim as the environment
# root. Core is intentionally still inactive here, so materialize exactly that
# agentnet-owned runtime the same way the unit would.
CORE_RUNTIME_0144="/var/lib/agentnet/npm-runtime"
PYTHON_0144="$CORE_RUNTIME_0144/bin/python"
AGENTNET_0144="$CORE_RUNTIME_0144/bin/agentnet"
sudo -u agentnet env \
  PATH="$PREFIX_0144/bin:/usr/bin:/bin" \
  AGENTNET_NPM_RUNTIME_DIR="$CORE_RUNTIME_0144" \
  AGENTNET_UV="$PREFIX_0144/bin/uv" \
  NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  "$PREFIX_0144/bin/node" "$(launcher "$PREFIX_0144")" --version >/dev/null
sudo test -x "$PYTHON_0144"
sudo test -x "$AGENTNET_0144"
# The later enrolled-state apply starts the C0 responder under its own service
# account. Warm that exact package runtime now so network/bootstrap latency
# cannot consume the bounded systemd reconciliation window and masquerade as a
# product start failure.
C0_RUNTIME_0144="/var/lib/agentnet-c0/npm-runtime"
sudo -u agentnet-c0 env \
  HOME=/var/lib/agentnet-c0 \
  XDG_STATE_HOME=/var/lib/agentnet-c0/.local/state \
  XDG_CACHE_HOME=/var/lib/agentnet-c0/.cache \
  PATH="$PREFIX_0144/bin:/usr/bin:/bin" \
  AGENTNET_NPM_RUNTIME_DIR="$C0_RUNTIME_0144" \
  AGENTNET_UV="$PREFIX_0144/bin/uv" \
  NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  "$PREFIX_0144/bin/node" "$(launcher "$PREFIX_0144")" --version >/dev/null
sudo test -x "$C0_RUNTIME_0144/bin/agentnet"
cat >"$WORK/seed-released-state.py" <<'PY'
from __future__ import annotations

import json
import time
from pathlib import Path

from agentnet.approval import IndependentApprovalVerifier, TrustedApprover, create_independent_approval_receipt
from agentnet.core.app import CommunicationCore
from agentnet.identity.enrollment import ENROLLMENT_APPROVAL_PURPOSE, EnrollmentService, VerifiedOIDCIdentity
from agentnet.operations.config import RuntimeProfile
from agentnet.operations.config_migration import load_config_json
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
    identity = VerifiedOIDCIdentity(
        issuer="https://accounts.example",
        subject="owner-subject",
        verified_email="owner@agentnet.test",
    )
    approver_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="owner-principal",
        domain_id="agentnet.test",
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({ENROLLMENT_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver},
        verifier_id="approval.agentnet.test",
    )
    enrollment = EnrollmentService(
        core.store,
        verifier,
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        binding_assurance="os_bound",
        credential_ttl=86400,
        clock=lambda: now,
    )
    key = P256KeyPair.generate()
    with core.store.transaction() as connection:
        challenge = enrollment._begin_in_transaction(
            connection,
            domain_id="agentnet.test",
            identity=identity,
            harness_kind="server",
            harness_name="ordinary-server-upgrade-e2e",
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
            "agentnet.enrollment.pop.v1",
            challenge.signed_fields(),
        ),
        approval=approval,
    )
    actor = completed.actor
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
        )
        + "\n",
        encoding="utf-8",
    )
    identity_path.chmod(0o600)
    print(
        json.dumps(
            {
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
            },
            sort_keys=True,
        )
    )
finally:
    core.close()
PY
sudo install -o agentnet -g agentnet -m 0700 "$WORK/seed-released-state.py" /var/lib/agentnet/seed-released-state.py
# Core resolves its DSN, OIDC secret, and envelope key from the environment
# file the unit loads. Source it as root, then drop to the service account so
# no secret ever reaches argv or the job log.
run_as_core_service() {
  sudo bash -c '
    set -euo pipefail
    set -a
    . /etc/agentnet-secrets/core.env
    set +a
    exec setpriv --reuid=agentnet --regid=agentnet --init-groups \
      env PYTHONDONTWRITEBYTECODE=1 "$@"
  ' bash "$@"
}
run_as_core_service "$PYTHON_0144" /var/lib/agentnet/seed-released-state.py \
  >"$WORK/released-fixture.json"
sudo rm -f /var/lib/agentnet/seed-released-state.py
HARNESS_ID="$(jq -r '.harness_id' "$WORK/released-fixture.json")"
CREDENTIAL_ID="$(jq -r '.credential_id' "$WORK/released-fixture.json")"
[[ "$HARNESS_ID" =~ ^[0-9a-f-]{36}$ ]]
[[ "$CREDENTIAL_ID" =~ ^[0-9a-f-]{36}$ ]]
sudo systemctl stop agentnet-core.service
[[ "$(sudo systemctl show agentnet-core.service --property=ActiveState --value)" == "inactive" ]]
# The stopped Core holds the instance lease until it releases or the lease
# expires; activation is a second holder of the same exact instance.
LEASE_RELEASED=false
for _ in $(seq 1 300); do
  if [[ "$(sudo -u postgres psql -Atq --dbname=agentnet -c \
    "SELECT COUNT(*) FROM runtime_leases WHERE lease_name='server-agent.instance:ordinary-server-upgrade-e2e' AND expires_at > EXTRACT(EPOCH FROM now())")" == "0" ]]; then
    LEASE_RELEASED=true
    break
  fi
  sleep 1
done
if [[ "$LEASE_RELEASED" != "true" ]]; then
  echo "runtime lease was never released after stopping Core" >&2
  sudo -u postgres psql --dbname=agentnet -c \
    "SELECT lease_name,owner_id,fence,acquired_at,heartbeat_at,expires_at FROM runtime_leases" >&2
  exit 1
fi
run_as_core_service "$AGENTNET_0144" server-agent activate \
  --config /var/lib/agentnet/agentnet.json \
  --identity /var/lib/agentnet/server-agent-identity.json >"$WORK/activate-0.1.45.json"
jq -e --arg harness "$HARNESS_ID" --arg credential "$CREDENTIAL_ID" \
  '.activated == true and .harness_id == $harness and .credential_id == $credential and .authority_granted == false' \
  "$WORK/activate-0.1.45.json" >/dev/null
apply_setup "$PREFIX_0144" "$DIGEST_0144" "$APPLY_BOUND_0144"
jq -e '.status == "operational" and .identity_enrolled == true and .authority_granted == false' "$APPLY_BOUND_0144" >/dev/null
sudo grep -Fxq 'OnUnitActiveSec=1h' /etc/systemd/system/agentnet-credential-renew.timer
sudo grep -Fxq 'Persistent=true' /etc/systemd/system/agentnet-credential-renew.timer
! sudo grep -Fq 'OnUnitInactiveSec=' /etc/systemd/system/agentnet-credential-renew.timer

# Freeze the exact released identity, schema catalog, marker, and private
# identity material before the candidate changes any package-owned bytes.
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM enrollment_challenges WHERE consumed_at IS NOT NULL')" == "1" ]]
IDENTITY_0144="$(identity_snapshot)"
ENDPOINT_LIFECYCLE_0144="$(endpoint_lifecycle_snapshot)"
CATALOG_0144="$(migration_catalog 7 | sha256sum | cut -d' ' -f1)"
MARKER_0144="$(sudo sha256sum /var/lib/agentnet-setup/setup.json | cut -d' ' -f1)"
REVISION_0144="$(sudo jq -r '.revision' /var/lib/agentnet-setup/setup.json)"
IDENTITY_FILE_0144="$(sudo sha256sum /var/lib/agentnet/server-agent-identity.json | cut -d' ' -f1)"
KEY_FILE_0144="$(sudo sha256sum /var/lib/agentnet/guided-join.key.pem | cut -d' ' -f1)"
OLD_CORE_PID="$(sudo systemctl show agentnet-core.service --property=MainPID --value)"
OLD_APPROVAL_PID="$(sudo systemctl show agentnet-approval.service --property=MainPID --value)"
[[ "$OLD_CORE_PID" =~ ^[1-9][0-9]*$ ]]
[[ "$OLD_APPROVAL_PID" =~ ^[1-9][0-9]*$ ]]

PLAN_0145="$WORK/plan-0.1.46.json"
APPLY_0145="$WORK/apply-0.1.46.json"
plan_setup "$PREFIX_0145" "$PLAN_0145"
DIGEST_0145="$(jq -r '.request_digest' "$PLAN_0145")"
[[ "$DIGEST_0145" =~ ^[a-f0-9]{64}$ ]]
[[ "$DIGEST_0145" != "$DIGEST_0144" ]]
apply_setup "$PREFIX_0145" "$DIGEST_0145" "$APPLY_0145"
jq -e '
  .status == "operational"
  and .identity_enrolled == true
  and .authority_granted == false
  and .endpoint_lifecycle == null
' "$APPLY_0145" >/dev/null
sudo jq -e --arg previous "$MARKER_0144" --argjson revision "$REVISION_0144" '
  .package_version == "0.1.46"
  and .artifact_mode == "disabled"
  and (.units | length) == 5
  and .revision == ($revision + 1)
  and .previous_marker_digest == $previous
' /var/lib/agentnet-setup/setup.json >/dev/null
assert_schema_seven_source
[[ "$(migration_catalog 7 | sha256sum | cut -d' ' -f1)" == "$CATALOG_0144" ]]
[[ "$(identity_snapshot)" == "$IDENTITY_0144" ]]
[[ "$(endpoint_lifecycle_snapshot)" == "$ENDPOINT_LIFECYCLE_0144" ]]
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM principals')" == "1" ]]
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM harnesses')" == "1" ]]
[[ "$(psql_agentnet -c 'SELECT COUNT(*) FROM credentials')" == "1" ]]
[[ "$(sudo sha256sum /var/lib/agentnet/server-agent-identity.json | cut -d' ' -f1)" == "$IDENTITY_FILE_0144" ]]
[[ "$(sudo sha256sum /var/lib/agentnet/guided-join.key.pem | cut -d' ' -f1)" == "$KEY_FILE_0144" ]]
sudo grep -Fxq 'OnActiveSec=5min' /etc/systemd/system/agentnet-credential-renew.timer
sudo grep -Fxq 'OnUnitInactiveSec=1h' /etc/systemd/system/agentnet-credential-renew.timer
! sudo grep -Fq 'OnUnitActiveSec=' /etc/systemd/system/agentnet-credential-renew.timer
! sudo grep -Fq 'OnBootSec=' /etc/systemd/system/agentnet-credential-renew.timer
! sudo grep -Fq 'Persistent=' /etc/systemd/system/agentnet-credential-renew.timer
assert_credential_renewal_recurs
sudo test ! -e /var/lib/agentnet-setup/upgrade.json

# Managed listeners now belong only to the corrected package runtime.
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
  "$PLAN_0145" "$PLAN_0145.stderr" "$APPLY_0145" "$APPLY_0145.stderr"
rm -rf "$INPUTS" "$WORK/seed-released-state.py" "$WORK/root-ca.key" "$WORK/tls.key" "$WORK/tls.csr"
[[ ! -e "$INPUTS" && ! -e "$WORK/root-ca.key" && ! -e "$WORK/tls.key" ]]
unset TOKEN

echo "ordinary server 0.1.45 to 0.1.46 timer upgrade E2E: PASS"
