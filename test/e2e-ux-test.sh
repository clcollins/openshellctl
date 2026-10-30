#!/usr/bin/env bash
# Validate the openshellctl "three command" getting-started path:
#
#   export VAULT_ADDR=https://vault.example.com
#   vault login -method=oidc
#   openshellctl sandbox list
#
# This script runs inside the CI image. It performs vault login -method=oidc
# interactively — Vault prints a URL; the SRE opens it in their browser to
# complete authentication. Once logged in, the script validates that
# openshellctl sandbox list works with no flags and no prior configuration
# beyond the OPENSHELL_VAULT_* env vars baked into the image.
#
# Also validates source 3 (persisted vault_auth.json): registers a gateway
# in a fresh $HOME using gateway add --vault-kv-*, then runs a bare sandbox
# list with OPENSHELL_VAULT_* unset to confirm only the persisted file is used.
#
# Required: VAULT_ADDR must be set in the environment (pass via Makefile or
# export before running make e2e).
set -euo pipefail

: "${VAULT_ADDR:?VAULT_ADDR is required. Run: make e2e VAULT_ADDR=https://vault.example.com}"
: "${OPENSHELL_VAULT_KV_MOUNT:?baked into the image; see test/Containerfile.e2e}"
: "${OPENSHELL_VAULT_KV_PATH:?baked into the image; see test/Containerfile.e2e}"
: "${OPENSHELL_VAULT_FIELD_PREFIX:?baked into the image; see test/Containerfile.e2e}"

# VAULT_CALLBACK_PORT: host port published to container port 8250 for the OIDC
# redirect URI. Defaults to 8250 (matching E2E_VAULT_CALLBACK_PORT in Makefile).
VAULT_CALLBACK_PORT="${VAULT_CALLBACK_PORT:-8250}"

PREFIX="${OPENSHELL_VAULT_FIELD_PREFIX}-"

log()  { printf '\n==> %s\n' "$*"; }
pass() { printf 'PASS: %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

log "versions"
vault version
openshellctl version

# ---------------------------------------------------------------------------
# Step 1: vault login -method=oidc
#
# Follows the pattern from openshift/osdctl pkg/utils/vault.go buildOIDCArgs:
#   skip_browser=true    — print URL instead of exec'ing a browser
#   listenaddress=0.0.0.0 — accept the callback on all interfaces inside the container
#   port=8250             — the OIDC listener port inside the container
#   callbackport=<host>   — the host port the browser's redirect arrives on
#                          (published by `podman run -p 127.0.0.1:<host>:8250`)
#
# This is the same port-forwarding approach as openshift/ocm-container's ports
# feature (pkg/features/ports/ports.go), which writes the host port to
# /tmp/vault_callback_port. Here we receive it via VAULT_CALLBACK_PORT env.
#
# A 5-minute timeout matches osdctl's vaultLoginTimeout.
# ---------------------------------------------------------------------------
if vault token lookup >/dev/null 2>&1; then
  pass "existing Vault session valid; skipping login"
else
  log "vault login -method=oidc (open the URL below in your browser)"
  timeout 300 vault login -method=oidc -no-print \
    skip_browser=true listenaddress=0.0.0.0 \
    port=8250 callbackport="${VAULT_CALLBACK_PORT}" \
    || fail "vault login failed or timed out — is host port ${VAULT_CALLBACK_PORT} published to container port 8250? (make e2e uses -p 127.0.0.1:${VAULT_CALLBACK_PORT}:8250)"
  [[ -s "$HOME/.vault-token" ]] || fail "vault login did not write $HOME/.vault-token"
  pass "$HOME/.vault-token written"
fi

# ---------------------------------------------------------------------------
# Verify the Vault secret is readable with the expected field layout
# ---------------------------------------------------------------------------
log "vault kv get ${OPENSHELL_VAULT_KV_MOUNT}/${OPENSHELL_VAULT_KV_PATH}"
set +e
ENDPOINT="$(vault kv get -field="${PREFIX}gateway-endpoint" \
    "${OPENSHELL_VAULT_KV_MOUNT}/${OPENSHELL_VAULT_KV_PATH}" 2>&1)"
VAULT_KV_RC=$?
set -e
[[ $VAULT_KV_RC -eq 0 ]] \
  || fail "cannot read ${PREFIX}gateway-endpoint from Vault (exit ${VAULT_KV_RC}): ${ENDPOINT}"
pass "secret readable; endpoint=${ENDPOINT}"

# ---------------------------------------------------------------------------
# Source 2 — OPENSHELL_VAULT_* env: openshellctl sandbox list
# Exactly as a new user who has exported the env vars and run vault login
# would type it: no flags, no gateway add.
# ---------------------------------------------------------------------------
log "openshellctl sandbox list (source 2: OPENSHELL_VAULT_* env)"
set +e
OUT="$(openshellctl sandbox list 2>&1)"
RC=$?
set -e
printf '%s\n' "$OUT"
echo "exit=$RC"
[[ $RC -eq 0 ]] || {
  log "doctor (diagnostics)"
  openshellctl doctor || true
  fail "sandbox list exited $RC"
}
pass "sandbox list succeeded (OPENSHELL_VAULT_* env coordinates)"

# ---------------------------------------------------------------------------
# Source 3: persisted vault_auth.json via gateway add --vault-kv-*
#
# Register a gateway in a fresh $HOME with OPENSHELL_VAULT_* unset, so the
# flags alone write vault_auth.json. Then run bare sandbox list in that same
# $HOME with env still unset — only the persisted file supplies coordinates.
# Gateway is removed at the end (also wired into EXIT trap so a failure still
# cleans up).
# ---------------------------------------------------------------------------
LIVE_HOME="$(mktemp -d)"
# Carry the Vault session via the token file (same discovery path openshellctl
# uses), not VAULT_TOKEN, so ~/.vault-token discovery is what is exercised.
cp "$HOME/.vault-token" "$LIVE_HOME/.vault-token"

LIVE_GW_NAME="e2e-$(date +%s)"
LIVE_GW_REGISTERED=0
trap 'if [[ $LIVE_GW_REGISTERED -eq 1 ]]; then
        env -u OPENSHELL_VAULT_KV_MOUNT -u OPENSHELL_VAULT_KV_PATH \
          -u OPENSHELL_VAULT_FIELD_PREFIX \
          HOME="$LIVE_HOME" \
          openshellctl gateway remove "$LIVE_GW_NAME" 2>/dev/null || true
      fi' EXIT

log "gateway add in fresh HOME with OPENSHELL_VAULT_* unset (tests vault_auth.json persistence)"
env -u OPENSHELL_VAULT_KV_MOUNT -u OPENSHELL_VAULT_KV_PATH -u OPENSHELL_VAULT_FIELD_PREFIX \
  HOME="$LIVE_HOME" \
  openshellctl gateway add "$ENDPOINT" \
    --name              "$LIVE_GW_NAME" \
    --vault-kv-mount    "${OPENSHELL_VAULT_KV_MOUNT}" \
    --vault-kv-path     "${OPENSHELL_VAULT_KV_PATH}" \
    --vault-field-prefix "${OPENSHELL_VAULT_FIELD_PREFIX}" \
  || fail "gateway add failed"
LIVE_GW_REGISTERED=1

VAULT_AUTH_FILE="$LIVE_HOME/.config/openshell/gateways/$LIVE_GW_NAME/vault_auth.json"
[[ -f "$VAULT_AUTH_FILE" ]] \
  || fail "gateway add did not write $VAULT_AUTH_FILE"
jq -e --arg m "${OPENSHELL_VAULT_KV_MOUNT}" '.mount == $m' "$VAULT_AUTH_FILE" >/dev/null \
  || fail "vault_auth.json mount field mismatch"
jq -e --arg p "${OPENSHELL_VAULT_KV_PATH}" '.path == $p'   "$VAULT_AUTH_FILE" >/dev/null \
  || fail "vault_auth.json path field mismatch"
jq -e --arg fp "${OPENSHELL_VAULT_FIELD_PREFIX}" '.field_prefix == $fp' "$VAULT_AUTH_FILE" >/dev/null \
  || fail "vault_auth.json field_prefix mismatch"
pass "vault_auth.json written with correct mount/path/field_prefix"

log "openshellctl sandbox list in fresh HOME, OPENSHELL_VAULT_* unset (source 3: vault_auth.json)"
set +e
OUT3="$(env -u OPENSHELL_VAULT_KV_MOUNT -u OPENSHELL_VAULT_KV_PATH -u OPENSHELL_VAULT_FIELD_PREFIX \
        HOME="$LIVE_HOME" \
        openshellctl sandbox list 2>&1)"
RC3=$?
set -e
printf '%s\n' "$OUT3"
echo "exit=$RC3"
[[ $RC3 -eq 0 ]] \
  || fail "sandbox list failed using persisted vault_auth.json (exit $RC3)"
pass "sandbox list succeeded using only persisted vault_auth.json (source 3)"

log "gateway remove (cleanup)"
env -u OPENSHELL_VAULT_KV_MOUNT -u OPENSHELL_VAULT_KV_PATH -u OPENSHELL_VAULT_FIELD_PREFIX \
  HOME="$LIVE_HOME" \
  openshellctl gateway remove "$LIVE_GW_NAME" \
  || fail "gateway remove failed"
LIVE_GW_REGISTERED=0
[[ ! -f "$VAULT_AUTH_FILE" ]] \
  || fail "vault_auth.json still present after gateway remove"
pass "gateway removed cleanly"

log "ALL CHECKS PASSED"
