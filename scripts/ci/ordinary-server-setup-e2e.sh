#!/usr/bin/env bash
set -euo pipefail

# Destructive only inside a fresh GitHub-hosted Ubuntu 24.04 runner.
if [[ "${CI:-}" != "true" || "${GITHUB_ACTIONS:-}" != "true" || -z "${RUNNER_TEMP:-}" ]]; then
  echo "ordinary server setup E2E requires an ephemeral GitHub Actions runner" >&2
  exit 2
fi
. /etc/os-release
if [[ "${ID:-}" != "ubuntu" || "${VERSION_ID:-}" != "24.04" ]]; then
  echo "ordinary server setup E2E requires Ubuntu 24.04" >&2
  exit 2
fi
if getent passwd agentnet >/dev/null || getent passwd agentnet-approval >/dev/null ||
   getent group agentnet >/dev/null || getent group agentnet-approval >/dev/null; then
  echo "ordinary server setup E2E requires clean AgentNet identities and groups" >&2
  exit 2
fi
for path in /var/lib/agentnet /var/lib/agentnet-approval /var/lib/agentnet-setup /etc/agentnet-secrets; do
  if sudo test -e "$path"; then
    echo "ordinary server setup E2E requires clean AgentNet state" >&2
    exit 2
  fi
done

WORK="$RUNNER_TEMP/agentnet-ordinary-server-e2e"
INPUTS="$WORK/inputs"
PACK="$WORK/pack"
PLAN="$WORK/plan.json"
APPLY_BLOCKED="$WORK/apply-postgres-blocked.json"
APPLY1="$WORK/apply-1.json"
APPLY2="$WORK/apply-2.json"
NO_PROXY_VALUE="127.0.0.1,localhost,.agentnet.test,core.agentnet.test,approval.agentnet.test"
RUNTIME_PREFIX="/opt/agentnet-e2e"
RUNTIME_PATH="$RUNTIME_PREFIX/bin:/usr/bin:/bin"
HBA_FILE=""
mkdir -p "$INPUTS" "$PACK"
chmod 700 "$WORK" "$INPUTS"

cleanup() {
  set +e
  sudo systemctl disable --now agentnet-core.service agentnet-approval.service >/dev/null 2>&1
  sudo rm -f /etc/systemd/system/agentnet-core.service /etc/systemd/system/agentnet-approval.service
  sudo systemctl daemon-reload >/dev/null 2>&1
  sudo rm -rf /var/lib/agentnet /var/lib/agentnet-approval /var/lib/agentnet-setup /etc/agentnet-secrets
  sudo userdel agentnet-approval >/dev/null 2>&1
  sudo userdel agentnet >/dev/null 2>&1
  sudo groupdel agentnet-approval >/dev/null 2>&1
  sudo groupdel agentnet >/dev/null 2>&1
  sudo rm -f /etc/nginx/sites-enabled/agentnet-e2e /etc/nginx/sites-available/agentnet-e2e
  sudo systemctl reload nginx >/dev/null 2>&1
  sudo sed -i '/# agentnet-e2e$/d' /etc/hosts
  sudo rm -f /usr/local/share/ca-certificates/agentnet-e2e.crt /etc/ssl/certs/agentnet-e2e.crt /etc/ssl/certs/agentnet-e2e.pem /etc/ssl/private/agentnet-e2e.key
  sudo update-ca-certificates >/dev/null 2>&1
  sudo -u postgres dropdb --if-exists agentnet >/dev/null 2>&1
  sudo -u postgres dropuser --if-exists agentnet >/dev/null 2>&1
  if [[ -n "$HBA_FILE" ]] && sudo test -f "$HBA_FILE"; then
    sudo sed -i '/# agentnet-e2e$/d' "$HBA_FILE"
    sudo -u postgres psql -Atq --dbname=postgres -c 'SELECT pg_reload_conf()' >/dev/null 2>&1
  fi
  sudo rm -rf "$RUNTIME_PREFIX"
  rm -rf "$WORK"
}
trap cleanup EXIT

# Install exact packed source under a root-owned system prefix. Service units must
# not depend on setup-node/setup-uv paths under runner home.
PACKED="$(npm pack --ignore-scripts --pack-destination "$PACK" --silent)"
sudo install -o root -g root -m 0755 -d "$RUNTIME_PREFIX/bin"
sudo install -o root -g root -m 0755 "$(command -v node)" "$RUNTIME_PREFIX/bin/node"
sudo install -o root -g root -m 0755 "$(command -v uv)" "$RUNTIME_PREFIX/bin/uv"
sudo -- "$(command -v npm)" install --global --prefix "$RUNTIME_PREFIX" --ignore-scripts --no-audit --no-fund "$PACK/$PACKED" >/dev/null

# Operator-owned local TLS reverse proxy for exact public-route health probes.
echo '127.0.0.1 core.agentnet.test approval.agentnet.test # agentnet-e2e' | sudo tee -a /etc/hosts >/dev/null
cat >"$WORK/openssl.cnf" <<'EOF'
[req]
prompt = no
distinguished_name = subject
x509_extensions = extensions
[subject]
CN = agentnet-e2e
[extensions]
subjectAltName = @alt_names
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
[alt_names]
DNS.1 = core.agentnet.test
DNS.2 = approval.agentnet.test
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$WORK/tls.key" -out "$WORK/tls.crt" -config "$WORK/openssl.cnf" >/dev/null 2>&1
chmod 600 "$WORK/tls.key"
sudo install -o root -g root -m 0644 "$WORK/tls.crt" /usr/local/share/ca-certificates/agentnet-e2e.crt
sudo update-ca-certificates >/dev/null
sudo install -o root -g root -m 0600 "$WORK/tls.key" /etc/ssl/private/agentnet-e2e.key
sudo install -o root -g root -m 0644 "$WORK/tls.crt" /etc/ssl/certs/agentnet-e2e.crt
sudo tee /etc/nginx/sites-available/agentnet-e2e >/dev/null <<'EOF'
server {
  listen 443 ssl;
  server_name core.agentnet.test;
  ssl_certificate /etc/ssl/certs/agentnet-e2e.crt;
  ssl_certificate_key /etc/ssl/private/agentnet-e2e.key;
  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
  }
}
server {
  listen 443 ssl;
  server_name approval.agentnet.test;
  ssl_certificate /etc/ssl/certs/agentnet-e2e.crt;
  ssl_certificate_key /etc/ssl/private/agentnet-e2e.key;
  location / {
    proxy_pass http://127.0.0.1:8090;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
  }
}
EOF
sudo ln -s /etc/nginx/sites-available/agentnet-e2e /etc/nginx/sites-enabled/agentnet-e2e
sudo nginx -t
sudo systemctl restart nginx

# Seven strict owner-only setup inputs. Values are synthetic and never printed.
TOKEN='synthetic-ci-broker-token-0123456789abcdef0123456789'
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
openssl ecparam -name prime256v1 -genkey -noout -out "$WORK/scanner.key" >/dev/null 2>&1
openssl ec -in "$WORK/scanner.key" -pubout -out "$WORK/scanner.pub" >/dev/null 2>&1
python3 - "$WORK/scanner.pub" "$INPUTS/scanner-trust.json" <<'PY'
import json, pathlib, sys
public_key = pathlib.Path(sys.argv[1]).read_text()
pathlib.Path(sys.argv[2]).write_text(json.dumps({
    "trusted_public_keys": {"scanner-key": public_key},
    "required_engine": "synthetic-scanner",
    "required_rules_digest": "a" * 64,
    "required_profile_digest": "b" * 64,
}))
PY
cat >"$INPUTS/server-setup.json" <<EOF
{"schema":"agentnet.server-setup.request.v1","profile":"always_on_server_agent","domain_id":"agentnet.test","service_audience":"urn:agentnet:agentnet.test:corporate-api","runtime_instance_id":"ordinary-server-e2e","core_public_origin":"https://core.agentnet.test","approval_public_origin":"https://approval.agentnet.test","database_url":"postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet","database_url_env":"AGENTNET_DATABASE_URL","core_environment_file":"$INPUTS/core.env","approval_environment_file":"$INPUTS/approval.env","oidc_provider_file":"$INPUTS/core-oidc.json","approval_owner_oidc_file":"$INPUTS/approval-owner-oidc.json","approval_approvers_file":"$INPUTS/approvers.json","scanner_trust_file":"$INPUTS/scanner-trust.json","approval_approver_principal_id":"owner-principal","approval_verifier_id":"approval.agentnet.test"}
EOF
chmod 600 "$INPUTS"/* "$WORK/scanner.key" "$WORK/scanner.pub"

# No privileged or managed-host writes; caller-owned npm runtime is allowed.
env PATH="$RUNTIME_PATH" NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  "$RUNTIME_PREFIX/bin/agentnet" server-agent setup --request "$INPUTS/server-setup.json" >"$PLAN"
jq -e '.schema == "agentnet.server-setup.evidence.v1" and .status == "planned" and .identity_enrolled == false and .authority_granted == false and .prerequisites.postgresql.hba_rule == "local agentnet agentnet peer"' "$PLAN" >/dev/null
for user in agentnet agentnet-approval; do ! getent passwd "$user" >/dev/null; done
for path in /var/lib/agentnet /var/lib/agentnet-approval /var/lib/agentnet-setup /etc/agentnet-secrets; do ! sudo test -e "$path"; done
DIGEST="$(jq -r '.request_digest' "$PLAN")"
[[ "$DIGEST" =~ ^[a-f0-9]{64}$ ]]

# First approved apply may create fixed Core identity plus root-owned setup
# runtime/lock, then must block before AgentNet config/schema/unit/service writes.
set +e
sudo -- env PATH="$RUNTIME_PATH" NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  "$RUNTIME_PREFIX/bin/agentnet" server-agent setup \
  --request "$INPUTS/server-setup.json" --expected-request-digest "$DIGEST" --apply \
  >"$APPLY_BLOCKED"
BLOCKED_EXIT=$?
set -e
[[ "$BLOCKED_EXIT" -eq 1 ]]
jq -e '.schema == "agentnet.server-setup.evidence.v1" and .status == "blocked" and .blocker == "postgres_auth_not_ready" and .identity_enrolled == false and .authority_granted == false' "$APPLY_BLOCKED" >/dev/null
getent passwd agentnet >/dev/null
! getent passwd agentnet-approval >/dev/null
for path in /var/lib/agentnet /var/lib/agentnet-approval /etc/agentnet-secrets; do ! sudo test -e "$path"; done
sudo test -d /var/lib/agentnet-setup/npm-runtime
sudo test -f /var/lib/agentnet-setup/setup.lock
! sudo test -e /var/lib/agentnet-setup/setup.json

# Separate operator-owned PostgreSQL approval boundary: exact role, database,
# unshadowed peer rule, reload, and parsed live-file evidence.
sudo -u postgres createuser --login agentnet
sudo -u postgres createdb --owner=agentnet agentnet
HBA_FILE="$(sudo -u postgres psql -Atq --dbname=postgres -c 'SHOW hba_file')"
sudo sed -i '1ilocal agentnet agentnet peer # agentnet-e2e' "$HBA_FILE"
sudo -u postgres psql -Atq --dbname=postgres -c 'SELECT pg_reload_conf()' | grep -qx 't'
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

# Same approved digest resumes, starts services, and proves exact public health
# without granting identity, authority, or production durability.
sudo -- env PATH="$RUNTIME_PATH" NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  "$RUNTIME_PREFIX/bin/agentnet" server-agent setup \
  --request "$INPUTS/server-setup.json" --expected-request-digest "$DIGEST" --apply --start >"$APPLY1"
jq -e '.status == "waiting_owner_oidc_or_passkey" and .identity_enrolled == false and .authority_granted == false and .production_durability_proven == false' "$APPLY1" >/dev/null
sudo systemctl is-active --quiet agentnet-core.service
sudo systemctl is-active --quiet agentnet-approval.service
env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  curl --fail --silent --show-error https://core.agentnet.test/healthz >/dev/null
env NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  curl --fail --silent --show-error https://approval.agentnet.test/healthz >/dev/null
CORE_CONFIG_1="$(sudo sha256sum /var/lib/agentnet/agentnet.json | cut -d' ' -f1)"
APPROVAL_CONFIG_1="$(sudo sha256sum /var/lib/agentnet-approval/config.json | cut -d' ' -f1)"
CORE_UNIT_1="$(sudo sha256sum /etc/systemd/system/agentnet-core.service | cut -d' ' -f1)"
APPROVAL_UNIT_1="$(sudo sha256sum /etc/systemd/system/agentnet-approval.service | cut -d' ' -f1)"
REVISION_1="$(sudo jq -r '.revision' /var/lib/agentnet-setup/setup.json)"
MARKER_1="$(sudo sha256sum /var/lib/agentnet-setup/setup.json | cut -d' ' -f1)"

# Same-digest retry revalidates realized state; configs/units remain exact.
sudo -- env PATH="$RUNTIME_PATH" NO_PROXY="$NO_PROXY_VALUE" no_proxy="$NO_PROXY_VALUE" \
  "$RUNTIME_PREFIX/bin/agentnet" server-agent setup \
  --request "$INPUTS/server-setup.json" --expected-request-digest "$DIGEST" --apply --start >"$APPLY2"
jq -e '.status == "waiting_owner_oidc_or_passkey" and .identity_enrolled == false and .authority_granted == false' "$APPLY2" >/dev/null
[[ "$(sudo sha256sum /var/lib/agentnet/agentnet.json | cut -d' ' -f1)" == "$CORE_CONFIG_1" ]]
[[ "$(sudo sha256sum /var/lib/agentnet-approval/config.json | cut -d' ' -f1)" == "$APPROVAL_CONFIG_1" ]]
[[ "$(sudo sha256sum /etc/systemd/system/agentnet-core.service | cut -d' ' -f1)" == "$CORE_UNIT_1" ]]
[[ "$(sudo sha256sum /etc/systemd/system/agentnet-approval.service | cut -d' ' -f1)" == "$APPROVAL_UNIT_1" ]]
REVISION_2="$(sudo jq -r '.revision' /var/lib/agentnet-setup/setup.json)"
[[ "$REVISION_2" -eq "$REVISION_1" ]]
[[ "$(sudo sha256sum /var/lib/agentnet-setup/setup.json | cut -d' ' -f1)" == "$MARKER_1" ]]

# Output must not contain synthetic credential values.
! grep -Fq "$TOKEN" "$PLAN" "$APPLY_BLOCKED" "$APPLY1" "$APPLY2"
echo "ordinary server setup E2E: PASS"
