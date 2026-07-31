#!/usr/bin/env bash
set -euo pipefail

# Destructive only inside a fresh GitHub-hosted Ubuntu 24.04 runner. This lane
# deliberately creates the released 0.1.33 failed-latch state through real
# package execution; it never fabricates marker, journal, unit, or database
# state.
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
PREFIX_0131="/opt/agentnet-upgrade-e2e-0.1.31"
PREFIX_0133="/opt/agentnet-upgrade-e2e-0.1.33"
PREFIX_0134="/opt/agentnet-upgrade-e2e-0.1.34"
NO_PROXY_VALUE="127.0.0.1,localhost,.agentnet.test,core.agentnet.test,approval.agentnet.test"
HOST_SYSTEM_PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
OPT_UID="$(stat -c '%u' /opt)"
OPT_GID="$(stat -c '%g' /opt)"
OPT_MODE="$(stat -c '%a' /opt)"
HBA_FILE=""
mkdir -p "$INPUTS" "$PACK"
chmod 700 "$WORK" "$INPUTS"

cleanup() {
  set +e
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
  sudo rm -f /usr/local/share/ca-certificates/agentnet-upgrade-e2e-root.crt /etc/ssl/certs/agentnet-upgrade-e2e.crt /etc/ssl/private/agentnet-upgrade-e2e.key
  sudo update-ca-certificates >/dev/null 2>&1
  sudo -u postgres dropdb --if-exists agentnet >/dev/null 2>&1
  sudo -u postgres dropuser --if-exists agentnet >/dev/null 2>&1
  if [[ -n "$HBA_FILE" ]] && sudo test -f "$HBA_FILE"; then
    sudo sed -i '/# agentnet-upgrade-e2e$/d' "$HBA_FILE"
    sudo -u postgres psql -Atq --dbname=postgres -c 'SELECT pg_reload_conf()' >/dev/null 2>&1
  fi
  sudo rm -rf "$PREFIX_0131" "$PREFIX_0133" "$PREFIX_0134"
  sudo chown "$OPT_UID:$OPT_GID" /opt
  sudo chmod "$OPT_MODE" /opt
  rm -rf "$WORK"
}
trap cleanup EXIT

run_evidence() {
  local output="$1"
  shift
  local exit_code=0
  "$@" >"$output" || exit_code=$?
  if [[ "$exit_code" -ne 0 ]]; then
    jq -c '{schema, status, blocker, message}' "$output" >&2 || true
    return "$exit_code"
  fi
}

install_runtime() {
  local prefix="$1"
  local package_spec="$2"
  sudo install -o root -g root -m 0755 -d "$prefix/bin"
  sudo install -o root -g root -m 0755 "$(command -v node)" "$prefix/bin/node"
  sudo install -o root -g root -m 0755 "$(command -v uv)" "$prefix/bin/uv"
  sudo -- sh -c 'umask 022; exec "$@"' sh \
    "$(command -v npm)" install --global --prefix "$prefix" \
    --bin-links=false --umask=0022 --ignore-scripts --no-audit --no-fund \
    "$package_spec" >/dev/null
  local package_root="$prefix/lib/node_modules/@misunders2d/agentnet"
  sudo chmod 0755 \
    "$prefix/lib" \
    "$prefix/lib/node_modules" \
    "$prefix/lib/node_modules/@misunders2d" \
    "$package_root"
  sudo chown -Rh root:root "$prefix"
  sudo test ! -e "$prefix/bin/agentnet"
  sudo test "$(sudo jq -r '.version' "$package_root/package.json")" = "$(basename "$prefix" | sed 's/^agentnet-upgrade-e2e-//')"
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

# Exact released package roots and the current source candidate are installed
# independently so every plan binds its own immutable runtime path and bytes.
sudo chown root:root /opt
sudo chmod 0755 /opt
install_runtime "$PREFIX_0131" "@misunders2d/agentnet@0.1.31"
install_runtime "$PREFIX_0133" "@misunders2d/agentnet@0.1.33"
CANDIDATE_TARBALL="$(npm pack --ignore-scripts --pack-destination "$PACK" --silent)"
install_runtime "$PREFIX_0134" "$PACK/$CANDIDATE_TARBALL"

# Operator-owned local TLS routes.
echo '127.0.0.1 core.agentnet.test approval.agentnet.test # agentnet-upgrade-e2e' | sudo tee -a /etc/hosts >/dev/null
cat >"$WORK/openssl.cnf" <<'EOF'
[req]
prompt = no
distinguished_name = subject
req_extensions = request_extensions
[subject]
CN = agentnet-upgrade-e2e
[request_extensions]
subjectAltName = @alt_names
[extensions]
subjectAltName = @alt_names
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid,issuer
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
  location / { proxy_pass http://127.0.0.1:8080; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto https; }
}
server {
  listen 443 ssl;
  server_name approval.agentnet.test;
  ssl_certificate /etc/ssl/certs/agentnet-upgrade-e2e.crt;
  ssl_certificate_key /etc/ssl/private/agentnet-upgrade-e2e.key;
  location / { proxy_pass http://127.0.0.1:8090; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto https; }
}
EOF
sudo ln -s /etc/nginx/sites-available/agentnet-upgrade-e2e /etc/nginx/sites-enabled/agentnet-upgrade-e2e
sudo nginx -t
sudo systemctl restart nginx

# Exact released communication-only request-v2 inputs.
TOKEN='synthetic-upgrade-broker-token-0123456789abcdef0123456789'
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
{"approvers":[{"principal_id":"owner-principal","authority_kind":"human","domain_id":"agentnet.test","allowed_purposes":["authorization.bootstrap_plan.approve","authorization.elevation.approve","identity.credential.recover.approve","identity.enrollment.approve","identity.harness.revoke.approve","organization.relationship.accept"],"oidc_issuer":"https://accounts.example","oidc_subject":"owner-subject"}]}
EOF
cat >"$INPUTS/server-setup.json" <<EOF
{"schema":"agentnet.server-setup.request.v2","profile":"always_on_server_agent","artifact_mode":"disabled","domain_id":"agentnet.test","service_audience":"urn:agentnet:agentnet.test:corporate-api","runtime_instance_id":"ordinary-server-upgrade-e2e","core_public_origin":"https://core.agentnet.test","approval_public_origin":"https://approval.agentnet.test","database_url":"postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet","database_url_env":"AGENTNET_DATABASE_URL","core_environment_file":"$INPUTS/core.env","approval_environment_file":"$INPUTS/approval.env","oidc_provider_file":"$INPUTS/core-oidc.json","approval_owner_oidc_file":"$INPUTS/approval-owner-oidc.json","approval_approvers_file":"$INPUTS/approvers.json","approval_approver_principal_id":"owner-principal","approval_verifier_id":"approval.agentnet.test"}
EOF
chmod 600 "$INPUTS"/*

# Operator-owned PostgreSQL prerequisite.
sudo -u postgres createuser --login agentnet
sudo -u postgres createdb --owner=agentnet agentnet
HBA_FILE="$(sudo -u postgres psql -Atq --dbname=postgres -c 'SHOW hba_file')"
sudo sed -i '1ilocal agentnet agentnet peer # agentnet-upgrade-e2e' "$HBA_FILE"
sudo -u postgres psql -Atq --dbname=postgres -c 'SELECT pg_reload_conf()' | grep -qx 't'
for _ in $(seq 1 50); do
  if sudo -u postgres psql -Atq --dbname=postgres -c \
    "SELECT pg_conf_load_time() >= (pg_stat_file(current_setting('hba_file'))).modification" | grep -qx 't'; then
    break
  fi
  sleep 0.2
done

# 0.1.31 creates the real released two-unit communication-only state.
PLAN_0131="$WORK/plan-0.1.31.json"
APPLY_0131="$WORK/apply-0.1.31.json"
plan_setup "$PREFIX_0131" "$PLAN_0131"
DIGEST_0131="$(jq -r '.request_digest' "$PLAN_0131")"
apply_setup "$PREFIX_0131" "$DIGEST_0131" "$APPLY_0131"
jq -e '.status == "waiting_owner_oidc_or_passkey" and .identity_enrolled == false and .authority_granted == false' "$APPLY_0131" >/dev/null
sudo jq -e '.package_version == "0.1.31" and .artifact_mode == "disabled" and (.units | length) == 2' /var/lib/agentnet-setup/setup.json >/dev/null
sudo test ! -e /var/lib/agentnet-setup/upgrade.json
sudo systemctl is-active --quiet agentnet-core.service
sudo systemctl is-active --quiet agentnet-approval.service

# Exact public 0.1.33 must naturally reproduce the released failure after it
# commits the five-unit marker and retained forward-only journal.
PLAN_0133="$WORK/plan-0.1.33.json"
APPLY_0133="$WORK/apply-0.1.33.json"
plan_setup "$PREFIX_0133" "$PLAN_0133"
DIGEST_0133="$(jq -r '.request_digest' "$PLAN_0133")"
set +e
apply_setup "$PREFIX_0133" "$DIGEST_0133" "$APPLY_0133"
EXIT_0133=$?
set -e
[[ "$EXIT_0133" -eq 1 ]]
jq -e '.status == "blocked" and .blocker == "service_runtime_binding" and .identity_enrolled == false and .authority_granted == false' "$APPLY_0133" >/dev/null
sudo jq -e '.package_version == "0.1.33" and (.units | length) == 5' /var/lib/agentnet-setup/setup.json >/dev/null
sudo test -f /var/lib/agentnet-setup/upgrade.json
[[ "$(sudo systemctl show agentnet-approval.service --property=ActiveState --value)" == "failed" ]]
[[ "$(sudo systemctl show agentnet-approval.service --property=Result --value)" == "exit-code" ]]
[[ "$(sudo systemctl show agentnet-approval.service --property=ExecMainStatus --value)" == "143" ]]
[[ "$(sudo -u agentnet psql -Atq --dbname=agentnet -c "SELECT value FROM metadata WHERE key='schema_version'")" == "4" ]]

# Current candidate must consume that exact retained state, clear the failed
# latch, migrate schema 4->5, converge five units, and start the unenrolled
# profile without changing the operator request.
PLAN_0134="$WORK/plan-0.1.34.json"
APPLY_0134="$WORK/apply-0.1.34.json"
RETRY_0134="$WORK/retry-0.1.34.json"
plan_setup "$PREFIX_0134" "$PLAN_0134"
DIGEST_0134="$(jq -r '.request_digest' "$PLAN_0134")"
apply_setup "$PREFIX_0134" "$DIGEST_0134" "$APPLY_0134"
jq -e '.status == "waiting_owner_oidc_or_passkey" and .identity_enrolled == false and .authority_granted == false' "$APPLY_0134" >/dev/null
sudo jq -e '.package_version == "0.1.34" and (.units | length) == 5' /var/lib/agentnet-setup/setup.json >/dev/null
sudo test ! -e /var/lib/agentnet-setup/upgrade.json
[[ "$(sudo -u agentnet psql -Atq --dbname=agentnet -c "SELECT value FROM metadata WHERE key='schema_version'")" == "5" ]]
[[ "$(sudo -u agentnet psql -Atq --dbname=agentnet -c 'SELECT COALESCE(MAX(version),0) FROM schema_migrations')" == "5" ]]
sudo systemctl is-active --quiet agentnet-core.service
sudo systemctl is-enabled --quiet agentnet-core.service
sudo systemctl is-active --quiet agentnet-approval.service
sudo systemctl is-enabled --quiet agentnet-approval.service
! sudo systemctl is-failed --quiet agentnet-approval.service
[[ "$(sudo systemctl show agentnet-c0-responder.service --property=ActiveState --value)" == "inactive" ]]
[[ "$(sudo systemctl show agentnet-c0-responder.service --property=UnitFileState --value)" == "disabled" ]]
[[ "$(sudo systemctl show agentnet-credential-renew.service --property=ActiveState --value)" == "inactive" ]]
[[ "$(sudo systemctl show agentnet-credential-renew.service --property=UnitFileState --value)" == "static" ]]
[[ "$(sudo systemctl show agentnet-credential-renew.timer --property=ActiveState --value)" == "inactive" ]]
[[ "$(sudo systemctl show agentnet-credential-renew.timer --property=UnitFileState --value)" == "disabled" ]]
env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" curl --fail --silent --show-error https://core.agentnet.test/healthz >/dev/null
env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" curl --fail --silent --show-error https://approval.agentnet.test/healthz >/dev/null

MARKER_0134="$(sudo sha256sum /var/lib/agentnet-setup/setup.json | cut -d' ' -f1)"
REVISION_0134="$(sudo jq -r '.revision' /var/lib/agentnet-setup/setup.json)"
apply_setup "$PREFIX_0134" "$DIGEST_0134" "$RETRY_0134"
jq -e '.status == "waiting_owner_oidc_or_passkey" and any(.steps[]; .id == "setup_marker" and .status == "already_satisfied")' "$RETRY_0134" >/dev/null
[[ "$(sudo sha256sum /var/lib/agentnet-setup/setup.json | cut -d' ' -f1)" == "$MARKER_0134" ]]
[[ "$(sudo jq -r '.revision' /var/lib/agentnet-setup/setup.json)" == "$REVISION_0134" ]]
! grep -Fq "$TOKEN" "$PLAN_0131" "$APPLY_0131" "$PLAN_0133" "$APPLY_0133" "$PLAN_0134" "$APPLY_0134" "$RETRY_0134"

echo "ordinary server upgrade E2E: PASS"
