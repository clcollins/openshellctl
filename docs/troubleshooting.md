# Troubleshooting auth onboarding

This document covers the auth-honesty fixes from ROSAENG-68828: cases where
the CLI used to report success, give no actionable detail, or give the wrong
hint, when it had actually done nothing or hit a different problem than the
hint suggested. All of these are fully reproducible offline — no gateway
connection required.

## Quick reference: symptom → `doctor` check

`openshellctl doctor` (see [the README](../README.md#first-run-openshellctl-doctor)
and [`doctor.md`](doctor.md)) now catches most of what follows *before* you
ever run a command that fails confusingly. This table maps each symptom
below to the check that surfaces it today, or to the fix that made it an
explicit, honest error instead of a `doctor` check:

| Symptom | Cause | Caught by |
|---|---|---|
| Fake success on `token refresh` with nothing configured | `auth.Resolve`'s fallback returned a working-looking no-bearer-token source unconditionally | `doctor`'s **Credentials** check — fails with "no credentials configured" instead of a fake success |
| `--write` silently did nothing | no registered gateway directory to write `oidc_token.json` into | not a `doctor` check — `--write`/`--write-token` is now a usage error (exit 2) naming the fix, on every auth-resolving command |
| Role preflight passed with no gateway role present | checked for "any role", not `openshell-user`/`openshell-admin` specifically | `doctor`'s **Roles** check |
| Audience preflight was effectively dead code | compared only against `--oidc-audience`, which nobody passes to `token show` | `doctor`'s **Audience** check |
| Permission-denied got the same hint as an expired token | both mapped to the same generic "try `token refresh`" hint | not a distinct `doctor` check — `gateway.PermissionDeniedError` now gets its own exit code (7) and hint, but only on a real `sandbox`/`exec`/etc. call; `doctor`'s own gateway calls don't surface it as a separate check |
| Invalid audience/issuer got the generic refresh hint | `hintFor` didn't read the gateway's specific rejection reason | not a `doctor` check — `hintFor` now distinguishes `InvalidAudience`/`InvalidIssuer` from `ExpiredSignature`; `doctor`'s **Audience**/**OIDC config match** checks catch a misconfigured audience/issuer *before* a real call would hit this |
| Endpoint reformatting silently dropped a registered gateway's token/TLS material | exact-string endpoint matching, so `https://host:443` and `https://host` (same gateway) resolved to two different registrations | `doctor`'s **Endpoint URL** check shows the normalized form being used; a token/TLS mismatch itself then surfaces via **Credentials** |
| Vault-sourced config errors (no session, `VAULT_ADDR` unset, secret not found, permission denied) | `--vault-kv-mount`/`--vault-kv-path` set but Vault itself isn't reachable/authorized/populated | `doctor`'s **Credentials** check — `resolveAuth` runs Vault resolution first, so the check's Detail line shows the Vault error verbatim |

## `token refresh` printed a fake success with no credentials configured

**Symptom**: running `openshellctl token refresh` against a gateway with no
static token, no client secret, no OIDC token bundle on disk, and no mTLS
material printed something like:

```
refreshed token for subject "" (expires unknown)
```

— a "success" message with an empty subject and no expiry, because nothing
was actually refreshed.

**Why**: `pkg/auth.Resolve`'s fallback case for an unset or mTLS auth mode
used to return a working-looking `NoAuthSource` unconditionally, even when
the gateway had no TLS material and nothing else was configured. `token
refresh` then happily printed its success message for that empty token.

**The fix**: `Resolve` now only returns that no-bearer-token source when TLS
material is actually present for a resolved mTLS gateway (`ResolveInput.
TLSPresent`). Otherwise it returns `*auth.ErrNoCredentials`, which names the
gateway and lists every place that was checked, and `token refresh` wraps it
as `auth.ErrNothingToRefresh`:

```
$ openshellctl token refresh --gateway-endpoint https://nowhere.invalid
Error: nothing to refresh: no credentials configured for gateway "https://nowhere.invalid" (checked: --token / OPENSHELL_TOKEN, OPENSHELL_OIDC_CLIENT_SECRET (or --client-secret-file), gateways/<name>/metadata.json, gateways/<name>/mtls/)
Hint: no credentials are configured for this gateway. For a service account, export OPENSHELL_OIDC_CLIENT_SECRET (or pass --client-secret-file); for a human, run `openshellctl login` (add -g <name> for a specific gateway) to authenticate via browser.
```

exits with code 3 (see the exit-code table in the main README). The hint
deliberately does not suggest `token refresh` again — re-running the same
command would resolve auth the same way and hit the exact same error, so it
names the two things that actually fix it instead.

A resolved gateway with an explicit `auth_mode: none` or `plaintext` (a
legitimate, intentional no-auth configuration) also returns "nothing to
refresh" rather than a fake success — there is no openshellctl-managed
bearer token for either mode.

> **Note on the fully-unconfigured case**: running `openshellctl token
> refresh` with *zero* flags and no gateway registered at all does not hit
> this path — `gatewayconfig.Resolve` already, correctly, fails earlier with
> its own "no active gateway" error (exit 4) before auth resolution is ever
> attempted. That check's logic is unrelated to this fix and was left as-is;
> its message text did need a small fix of its own, though — it was still
> telling you to run `openshell gateway select`/`openshell gateway add`
> (the upstream Rust binary) instead of `openshellctl` now that those
> commands are native (see [`gateway.md`](gateway.md)).

**Caught by**: `openshellctl doctor`'s **Credentials** check reports this
same "no credentials configured" failure before you ever reach `token
refresh` at all.

## `--write` silently did nothing when there was nowhere to write to

**Symptom**: `openshellctl token refresh --write` (or any other command
passing the persistent `--write-token` flag) against a gateway resolved only
by `--gateway-endpoint` (not a named, registered gateway) printed a warning
and exited 0 — the refreshed token was never actually persisted anywhere.

**Why**: there is no `gateways/<name>/` directory to write `oidc_token.json`
into unless the gateway was registered with `gateway add`. The old code
detected this but only warned.

**The fix**: `--write`/`--write-token` now fail fast with a usage error
naming the fix, on every command that resolves auth, not just `token
refresh`:

```
$ openshellctl token refresh --write --gateway-endpoint https://nowhere.invalid
Error: --write requires a named, registered gateway; register one first: openshellctl gateway add <endpoint> --name <name>
```

exits with code 2. Register the gateway first (see [`gateway.md`](gateway.md)), then retry with `--write`.

This early check is scoped carefully so it never masks a more specific
problem: it only preempts a resolve error when that error is
`*auth.ErrNoCredentials` (itself a "nothing is configured" case, the same
shape as "nowhere to write to"). Any other resolve error is surfaced as-is —
for example `--write --gateway bogus` (a typo'd gateway name) still reports
`Unknown gateway 'bogus'` (exit 4) with its own "list available gateways"
remediation, and `--write` with nothing registered and no flags at all still
reports `No active gateway...` (exit 4), not the generic writer error.

**Caught by**: not a `doctor` check — this is now a usage error (exit 2)
raised before any auth resolution is attempted.

## The role preflight checked "any role", not the role the gateway requires

**Symptom**: `token show`'s preflight warning only fired when a token carried
*no* realm roles at all. A typical human token from Keycloak already carries
`default-roles-<realm>` and `offline_access` — neither is a gateway role —
so this check passed silently, and the very next gateway call still failed
with `permission denied: role "openshell-user" required`.

**The fix**: `PreflightWarnings` now checks specifically for
`openshell-user` or `openshell-admin` (the gateway accepts either — admin is
a superset), not "any role":

```
$ openshellctl token show
...
warning: token roles [default-roles-rosa offline_access] include neither "openshell-user" nor "openshell-admin"; gateway calls will likely fail with permission denied
```

**Caught by**: `openshellctl doctor`'s **Roles** check, before any gateway
call that would actually need the role.

## The audience preflight almost never fired in practice

**Symptom**: `token show`'s audience warning only compared against
`--oidc-audience`, a flag essentially nobody passes to `token show` — the
expected audience actually lives in the gateway's own
`metadata.oidc_audience` (or defaults to `openshell-cli`), so the check was
effectively dead code against a real registered gateway.

**The fix**: the expected audience is now resolved in priority order:
`--oidc-audience` (an explicit override) → the resolved gateway's
`metadata.oidc_audience` → the same `openshell-cli` default
`OIDCClientIDOrDefault` already uses for the client ID.

**Caught by**: `openshellctl doctor`'s **Audience** check.

## A permission-denied response got the same hint as an expired token

**Symptom**: a gateway call failing with a permission-denied response (the
caller is authenticated, but lacks a required role) printed the same
"Hint: try `openshellctl token refresh`" as an actually-expired token —
unhelpful, since obtaining a new token changes nothing about what roles are
granted to the subject.

**The fix**: permission-denied responses (`gateway.PermissionDeniedError`)
now get their own exit code, 7, and a hint naming the two things that
actually resolve it in practice:

```
$ openshellctl sandbox list
Error: permission denied: role "openshell-user" required
Hint: you are authenticated, but the gateway denied this action due to insufficient permissions — obtaining a new token will not help. A human account needs the "openshell-user" realm role ("openshell-admin" also satisfies it); a service account must use its own OIDC client ID, not the shared "openshell-cli" default. Ask an administrator to grant the required role or provision a dedicated client.
```

`token refresh` is no longer suggested for this case.

**Caught by**: not a `doctor` check — `doctor`'s own gateway calls (listing
providers when `--provider`/`-f` is given) surface a permission-denied the
same way, but it isn't a distinct check or exit code there; the distinct
exit code (7) and hint only apply to a real `sandbox create`/`exec`/etc.
call outside `doctor`.

## An invalid-audience/invalid-issuer rejection also got the generic refresh hint

**Symptom**: `gateway.UnauthenticatedError` carries the gateway's own
rejection reason verbatim (e.g. `invalid token: InvalidAudience` /
`InvalidIssuer` / `ExpiredSignature`), but every case got the same "try
`token refresh`" hint — unhelpful for a wrong audience or issuer, since
refreshing reuses the exact same (mis)configuration and fails the same way.

**The fix**: `hintFor` now reads the gateway's reason and gives a distinct
hint for each:

```
$ openshellctl sandbox list
Error: invalid token: InvalidAudience
Hint: the token's audience does not match what the gateway expects. Check --oidc-audience / metadata.oidc_audience against the gateway's /auth/oidc-config before retrying — `openshellctl token refresh` will reuse the same wrong audience.
```

An `ExpiredSignature` rejection still gets the ordinary refresh hint, since
that genuinely is what `token refresh` fixes.

**Caught by**: `openshellctl doctor`'s **Audience**/**OIDC config match**
checks catch a misconfigured audience/issuer ahead of time, against a
registered gateway; `hintFor`'s distinct hints handle the case where it's
only caught live, on an actual gateway call.

## Vault-sourced auth config errors

These apply only when `--vault-kv-mount`/`--vault-kv-path` are set (see
README "4. Vault-sourced config"); without them, none of this code runs and
these errors cannot occur.

**No Vault session**: openshellctl never runs `vault login` itself.

```
$ openshellctl --vault-kv-mount osd-sre --vault-kv-path rosa-agent sandbox list
Error: no Vault token found (checked $VAULT_TOKEN and ~/.vault-token) — run `vault login` first (custom token helpers are not supported)
```

exits with code 3. Run `vault login` (any method) and retry — openshellctl
picks up the resulting `~/.vault-token`, or set `VAULT_TOKEN` directly.

**`VAULT_ADDR` not set**: without this check, the Vault SDK would silently
default to `https://127.0.0.1:8200` and fail with a confusing
connection-refused error instead.

```
$ openshellctl --vault-kv-mount osd-sre --vault-kv-path rosa-agent sandbox list
Error: VAULT_ADDR is not set
Hint: set VAULT_ADDR to your Vault server's address before using --vault-kv-mount/--vault-kv-path.
```

exits with code 3.

**Permission denied reading the secret** (authenticated to Vault, but the
token's policy doesn't grant read access to this path):

```
Error: permission denied reading vault secret osd-sre/rosa-agent
Hint: you are authenticated to Vault, but not authorized to read this secret — grant a policy with read access to osd-sre/data/rosa-agent.
```

exits with code 7 — the same "authenticated but not authorized" bucket as a
gateway permission-denied response. Re-running with a new Vault token will
not help; the policy itself needs the grant.

**Secret not found**:

```
Error: vault secret not found at osd-sre/rosa-agent
Hint: no secret exists at osd-sre/rosa-agent. Double-check --vault-kv-mount/--vault-kv-path, and confirm the mount is actually a KV v2 engine (a KV v1 mount also 404s here).
```

exits with code 4. A KV v1 mount produces this exact same error — openshellctl
only supports KV v2.

**A recognized field is present but isn't a string** (e.g. the secret stores
`oidc-client-id` as a number or nested object instead of a plain string):

```
Error: vault secret field "oidc-client-id" is not a string
```

exits with code 2 — this is a Vault secret authoring problem, not an auth
failure; fix the field's value in Vault.

**Caught by**: `openshellctl doctor`'s **Credentials** check — `resolveAuth`
runs Vault resolution before anything else, so any of the errors above
surface as that check's `Detail` line, verbatim, instead of only appearing
on a later real command.

## Endpoint reformatting silently dropped registered credentials

**Symptom**: a registered gateway's token and TLS material went missing —
not reported as an error, just silently unused — whenever an env var or
flag spelled the same endpoint slightly differently from how `gateway add`
originally wrote it (e.g. `https://gw.example.com` vs. the registered
`https://gw.example.com:443`, or a different letter case in the scheme/host).

**Why**: gateway lookup matched endpoints by exact string equality.
`gateway add` always writes a fully-qualified form (explicit default port
included); a later `--gateway-endpoint`/`OPENSHELL_GATEWAY_ENDPOINT` using
the bare, no-port form (the way these variables are commonly written and
exported) no longer matched that registration at all — openshellctl quietly
fell through to "no gateway found for this endpoint" and tried to proceed
with no credentials, rather than reporting the mismatch.

**The fix**: endpoint matching now normalizes scheme/host case and strips an
explicit default port (`:443` for `https`, `:80` for `http`) before
comparing, so `https://gw.example.com:443` (as `gateway add` writes it) and
`https://gw.example.com` (as commonly exported) resolve to the same
registration. A different port, or a different path/query/fragment, is
still correctly treated as a different gateway — see
[`gateway.md`](gateway.md) for the exact normalization rules.

**Caught by**: `openshellctl doctor`'s **Endpoint URL** check prints both
the as-given and normalized form, so a mismatch is visible immediately; a
credentials/TLS problem that endpoint reformatting *would* have caused now
surfaces through the **Credentials** check instead of silently using no
auth at all.

## `ssh channel closed without exit-status; tunnel: …`

**Symptom**: `sandbox create` or `sandbox connect` exits with code 1 and the
message:

```
Error: ssh channel closed without exit-status; tunnel: <reason>
```

or, without a tunnel error:

```
Error: ssh channel closed without exit-status
```

**Why**: Per the supervisor contract, every supervisor-initiated SSH channel
close sends an `exit-status` request before tearing down the channel. When
openshellctl receives a channel close with no prior `exit-status`, the
transport underneath the SSH connection was lost — the main process may still
be running in the sandbox. The `tunnel:` part names the first error the
gateway tunnel reported on that connection (typically `EOF` or
`connection reset by peer`); its absence means the transport closed without
the tunnel layer reporting an error (e.g. the SSH server half-closed the
connection cleanly, or openshellctl detected the loss above the tunnel).

**What to do**:

- Check your network connection and gateway reachability.
- If using `--no-keep`, openshellctl attempts to delete the sandbox
  automatically (the gateway's ephemeral cleanup hook does not fire on a
  transport loss). Verify with `sandbox list` that the sandbox is gone; if
  not, delete it manually with `sandbox delete`.
- If the problem recurs, run `openshellctl doctor` to check gateway health.
- If the main process was still running and left data, reconnect with
  `sandbox connect`.


