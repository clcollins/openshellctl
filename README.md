# openshellctl

A CLI tool for running sandboxes on [HyperShell](https://github.com/openshift-online/hypershell) — Red Hat's managed [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) gateways — from a laptop, a CI job, or a Kubernetes CronJob, with the same YAML manifest in every case.

openshellctl exists because getting an agent sandbox running on HyperShell from automation used to mean a bash prelude: fetch credentials from Vault, write the gateway's `metadata.json`, mint a token, hope it hasn't expired by cleanup time, then hand-assemble a long `sandbox create` command. openshellctl folds all of that into the binary:

- **Sandboxes as code.** A `kind: Sandbox` YAML manifest holds the image, command, providers, env, resources, uploads and session behaviour. `sandbox create -f`, `sandbox delete -f`, `sandbox get -f` and every other `-f` command read the same file, from disk, stdin or an `http(s)://` URL. See [Manifest format](#manifest-format).
- **Templates, not copies.** Manifests take `${NAME}`/`${PROMPT}`-style parameters filled from `-p KEY=VALUE` or `--param-file`, `oc process` style, so one checked-in template serves every run. Ready-made ones live in [`templates/`](templates/). See [Manifest template parameters](#manifest-template-parameters).
- **Vault-backed, zero-prelude auth.** Point it at a Vault KV v2 secret and it fills in the gateway endpoint, OIDC issuer, client ID and client secret itself; a service account authenticates with `client_credentials` and the token is checked on every request and re-minted before it expires, so an hour-long job's `--no-keep` cleanup still has a valid token. See [Authentication](#authentication).
- **Nothing to install alongside it.** Pure Go, single static binary, no shell-out to `openshell`, `ssh`, `tar`, `git` or `vault`. It is a standalone replacement for the upstream Rust CLI, not a wrapper around it, and its `pkg/` packages are importable for operators and controllers.

## Installation

```bash
go install github.com/openshift-online/openshellctl/cmd/openshellctl@latest
```

Or build from source:

```bash
git clone https://github.com/openshift-online/openshellctl.git
cd openshellctl
make build          # binary at ./openshellctl
```

## Quick start

Three complete, runnable paths from a clean machine to a working sandbox,
depending on where your credentials live. The Service account and Human
account paths below end with `openshellctl doctor` printing all-green; the
Vault path can be checked the same way by handing `doctor` the same
`--vault-*` flags. Run `doctor` any time something looks wrong — see
["First run: `openshellctl doctor`"](#first-run-openshellctl-doctor) below
for what a failure looks like and its exact next command. For running these
same commands from a Kubernetes CronJob instead of a shell, see
[`docs/ci.md`](docs/ci.md).

### Vault + template (the HyperShell CI path)

The shortest path when the gateway's service-account credentials are already
in Vault. No `gateway add`, no hand-written `sandbox create` flags: one Vault
session, one template, two parameters.

```bash
export VAULT_ADDR=https://vault.example.com
vault login -method=oidc        # openshellctl reads the resulting token; it never logs in itself

openshellctl --vault-kv-mount secret --vault-kv-path myapp --vault-field-prefix myapp \
  sandbox create -f templates/noninteractive.yaml \
  -p NAME=ROSAENG-12345 \
  -p PROMPT="retrieve and implement ROSAENG-12345" \
  --no-keep
```

> **Note:** every `--vault-*` flag above has a Cobra/Viper-style env var
> equivalent (`OPENSHELL_VAULT_KV_MOUNT`, `OPENSHELL_VAULT_KV_PATH`,
> `OPENSHELL_VAULT_FIELD_PREFIX` — same pattern as every global flag; see
> [Global flags](#global-flags)). Export them once per shell or CI job and
> every later command drops the three flags entirely:
> ```bash
> export OPENSHELL_VAULT_KV_MOUNT=secret
> export OPENSHELL_VAULT_KV_PATH=myapp
> export OPENSHELL_VAULT_FIELD_PREFIX=myapp
> openshellctl sandbox create -f templates/noninteractive.yaml -p NAME=... -p PROMPT="..." --no-keep
> ```

The secret at your `--vault-kv-path` supplies `<prefix>-gateway-endpoint`,
`<prefix>-oidc-client-id`, `<prefix>-oidc-client-secret` and the rest, where
`<prefix>` is whatever you pass to `--vault-field-prefix` (`myapp` in
the example above, but your own mount, path and prefix can be anything);
anything you set yourself via flag or `OPENSHELL_*` env var still wins. See
["4. Vault-sourced config"](#4-vault-sourced-config). The template is one of
two checked in under [`templates/`](templates/) — `interactive.yaml`
(attach, `noKeep`) and `noninteractive.yaml` (`--print`, `approvalMode:
auto`) — and `-p` fills its `${NAME}` and `${PROMPT}` placeholders; see
["Manifest template parameters"](#manifest-template-parameters).

> **Always `export` a credential, never assign it bare.** `VAR=$(...)`
> (no `export`) sets a shell-local variable openshellctl's child process
> never sees — every `OPENSHELL_*` variable below needs `export OPENSHELL_
> FOO=...` (or `export` on its own line afterward), not a bare assignment.
> This single mistake is the most common cause of `doctor` reporting
> "no credentials configured" even though the value is sitting right there
> in your shell history.

### Service account (CI / fire-and-forget)

For automation: no browser, no interactive login, a client secret does all
the work.

```bash
export OPENSHELL_GATEWAY_ENDPOINT=https://gateway.example.com
export OPENSHELL_OIDC_CLIENT_ID=<client-id>
export OPENSHELL_OIDC_CLIENT_SECRET=<client-secret>

# Registers the gateway and authenticates via client credentials in one step
# (gateway add detects the client secret and skips the browser automatically).
openshellctl gateway add "$OPENSHELL_GATEWAY_ENDPOINT" --name my-gw

openshellctl doctor                           # expect all-green
openshellctl sandbox create -f sandbox.yaml --no-keep
```

### Human account (interactive)

```bash
openshellctl gateway add https://gateway.example.com --name my-gw
openshellctl gateway login                    # opens a browser for OIDC auth

openshellctl doctor                           # expect all-green
openshellctl sandbox create --from python
openshellctl sandbox connect my-sandbox
```

### Everyday commands

Once a gateway is registered and authenticated (either path above):

```bash
# Run a one-shot command
openshellctl sandbox exec --name my-sandbox -- python3 -c "print('hello')"

# Upload code, run it
openshellctl sandbox upload my-sandbox ./myproject /workspace
openshellctl sandbox exec --name my-sandbox -- python3 /workspace/main.py

# Clean up
openshellctl sandbox delete my-sandbox
```

## First run: `openshellctl doctor`

Before creating a sandbox on a new machine, run `doctor` — it checks endpoint reachability, DNS, credentials, token audience/roles/expiry, and OIDC config drift in one pass, with the exact next command for anything missing.

All-green (gateway already registered via `gateway add`, three service-account env vars exported — `OIDC config match` needs a registered gateway's metadata to compare against; an endpoint-only invocation with nothing registered would skip that one line instead):

```
$ openshellctl doctor
✓ Endpoint URL       http://gw.example.com
✓ DNS                gw.example.com -> [10.0.0.1]
✓ HTTP reachability  http://gw.example.com/auth/oidc-config reachable (issuer=https://issuer.example.com)
✓ Credentials        minted token for subject "svc-account"
✓ Audience           token audience [openshell-cli] includes "openshell-cli"
✓ Roles              token roles [openshell-user]
✓ Expiry             token expires at 2026-10-09T12:00:00Z (in 23h59m59s)
✓ OIDC config match  registered metadata matches /auth/oidc-config
- Providers          no --provider/-f given
```

Missing the secret (a real run, `OPENSHELL_OIDC_CLIENT_SECRET` unset):

```
$ openshellctl doctor --gateway-endpoint https://gw.example.com
✓ Endpoint URL       https://gw.example.com (normalized: https://gw.example.com)
✓ DNS                gw.example.com -> [10.0.0.1]
✗ HTTP reachability  GET https://gw.example.com/auth/oidc-config: dial tcp: connect: connection refused
  Hint: check the gateway is reachable (try: curl -v https://gw.example.com/auth/oidc-config), or pass --gateway-insecure for an internal CA
✗ Credentials        no credentials configured for gateway "https://gw.example.com" (checked: --token / OPENSHELL_TOKEN, OPENSHELL_OIDC_CLIENT_SECRET (or --client-secret-file), gateways/<name>/metadata.json, gateways/<name>/mtls/)
  Hint: for a service account, export OPENSHELL_OIDC_CLIENT_SECRET (or pass --client-secret-file); for a human, run `openshellctl login`
- Audience           skipped: Credentials check failed
- Roles              skipped: Credentials check failed
- Expiry             skipped: Credentials check failed
- OIDC config match  no discovered OIDC config to compare
- Providers          no --provider/-f given
$ echo $?
3
```

Every failing line gets a `Hint:` with the exact next command — see [`docs/doctor.md`](docs/doctor.md) for the full list of checks, their skip rules, and what each hint means.

## Authentication

openshellctl resolves authentication in priority order — the first match wins:

### 1. Personal login (interactive use)

Register a gateway and log in — both natively, no upstream binary required:

```bash
openshellctl gateway add <endpoint>      # registers the gateway and sets it active
openshellctl gateway login               # opens a browser for OIDC authentication
openshellctl sandbox list                # uses your login token automatically
```

`gateway add` discovers the OIDC issuer from `<endpoint>/auth/oidc-config` automatically; pass `--oidc-issuer` to override it or to register a gateway that doesn't answer that endpoint but is OIDC-configured some other way. If a client secret is already set (`OPENSHELL_OIDC_CLIENT_SECRET`) when you run `gateway add`, it authenticates immediately via client credentials instead of opening a browser — see "2. Service account" below.

openshellctl reads `oidc_token.json` from `$XDG_CONFIG_HOME/openshell/gateways/<name>/`. If the access token is expired and a refresh token is present, openshellctl exchanges it for a new one automatically.

### 2. Service account (CI / fire-and-forget)

Set a client secret, then `gateway add` authenticates via `client_credentials` as part of registration — no browser, no login step:

```bash
export OPENSHELL_OIDC_CLIENT_SECRET=<secret>
openshellctl gateway add <endpoint> --name <name>
# ✓ Gateway '<name>' added and set as active
#   Endpoint: <endpoint>
#   Auth: oidc
#
# ✓ Authenticated via client credentials

openshellctl sandbox create -f job.yaml --no-keep
# Token re-mints transparently — the --no-keep cleanup always has a valid token
```

Already-registered gateways re-authenticate the same way via `sandbox create`/`sandbox list`/etc. directly — `gateway add` is only needed once, at registration time:

```bash
# Optional overrides (defaults come from gateway metadata):
# export OPENSHELL_OIDC_CLIENT_ID=<client-id>
# export OPENSHELL_OIDC_ISSUER=<issuer-url>

openshellctl sandbox create -f job.yaml --no-keep
```

This is the recommended method for automation, CI pipelines, and any non-interactive use. Unlike the upstream Rust CLI (which bakes the token once at startup), openshellctl refreshes per-request, so long-running jobs never fail with `ExpiredSignature`.

### 3. Static bearer token

For quick testing or external token management:

```bash
openshellctl --token <JWT> sandbox list
# or
export OPENSHELL_TOKEN=<JWT>
```

This overrides all other auth methods. The token is used as-is with no refresh — if it expires, commands will fail.

### 4. Vault-sourced config

This is how HyperShell service accounts are meant to be used: the gateway's
endpoint, OIDC issuer, client ID and client secret all sit in one Vault KV v2
secret, and openshellctl reads them directly. Nothing is exported into the
shell, nothing is written to disk, and a CronJob needs only a Vault session.
Point openshellctl at the secret instead of exporting the fields yourself:

```bash
vault login -method=oidc   # openshellctl never does this itself — it expects a session to already exist

openshellctl --vault-kv-mount secret --vault-kv-path myapp --vault-field-prefix myapp \
  sandbox list

# or, once per shell/job, via env vars:
export OPENSHELL_VAULT_KV_MOUNT=secret
export OPENSHELL_VAULT_KV_PATH=myapp
export OPENSHELL_VAULT_FIELD_PREFIX=myapp
openshellctl sandbox create -f templates/noninteractive.yaml -p NAME=my-run -p PROMPT="..." --no-keep
```

This reads the secret at `secret/myapp` and, for each of `gateway`,
`gateway-endpoint`, `oidc-issuer`, `oidc-client-id`, `oidc-client-secret`,
`oidc-audience`, `oidc-scopes`, looks for a field named
`<prefix>-<field>` (here, `myapp-oidc-client-id`,
`myapp-oidc-client-secret`, and so on) and uses it only where you
haven't already set that value via flag or `OPENSHELL_*` env var — Vault is
always the lowest-priority source, never an override. `--vault-field-prefix`
is optional; omit it if your secret's fields aren't prefixed.

openshellctl reads `VAULT_ADDR` (and the rest of the standard `VAULT_*` env
vars) itself; it never runs `vault login` — run that yourself first, the same
way you would for any other `vault kv get`. The token comes from `VAULT_TOKEN`
if set, otherwise `~/.vault-token` (the file `vault login` leaves behind).

The static `--token`/`OPENSHELL_TOKEN` bearer token is **not** one of the
Vault-fillable fields — it's a short-lived JWT, not something to cache in
Vault, and `--vault-kv-mount`/`--vault-kv-path` must be set together (one
without the other is a usage error).

### How auth retry works

Every API call goes through an automatic retry: if the gateway returns `Unauthenticated`, openshellctl invalidates its cached token and retries once with a fresh one. For service accounts this means a full re-mint; for personal logins it means a refresh-token exchange. This is why `--no-keep` cleanup works reliably even after hour-long sessions.

### Token management

```bash
openshellctl token show               # display token subject, expiry, roles
openshellctl token refresh             # force a token refresh
openshellctl token refresh --write     # refresh and persist to oidc_token.json
openshellctl token inspect <JWT>       # decode a JWT's claims (no sig verify)
```

`--write` is for interop, not for openshellctl itself: every openshellctl command (including `provider`/`sandbox`/etc.) resolves and re-mints auth transparently per call, with no disk read or write involved. `token refresh --write` exists solely to persist a Rust-CLI-schema `oidc_token.json` so a *separate* process — like the upstream `openshell` binary — can pick up the same credentials. You never need to run it before another openshellctl command.

If a command fails with an expired or invalid token, openshellctl prints:

```
Error: invalid token: ExpiredSignature
Hint: try `openshellctl token refresh` to obtain a new token.
```

## Configuration

Configuration is resolved in priority order:

1. CLI flags (highest)
2. Environment variables (`OPENSHELL_*`)
3. Config file (`$XDG_CONFIG_HOME/openshellctl/config.yaml`, override with `--config`)
4. Gateway config dirs (`$XDG_CONFIG_HOME/openshell/gateways/<name>/`)
5. Vault KV secret (gateway/OIDC fields only, opt-in via `--vault-kv-mount`/`--vault-kv-path` — see "4. Vault-sourced config" above)

### Global flags

These flags apply to every command:

| Flag | Env var | Description |
|------|---------|-------------|
| `--gateway`, `-g` | `OPENSHELL_GATEWAY` | Gateway name (selects config directory) |
| `--gateway-endpoint` | `OPENSHELL_GATEWAY_ENDPOINT` | Gateway gRPC endpoint URL |
| `--gateway-insecure` | `OPENSHELL_GATEWAY_INSECURE` | Skip TLS verification (gRPC dial, `gateway add`'s OIDC-discovery probe, and `-f` manifest URL fetches — the flag name says "gateway", but a `-f` manifest host is not the gateway; this is a consequence of sharing `http.DefaultTransport`, not a separate opt-in) |
| `--workspace` | `OPENSHELL_WORKSPACE` | Workspace name (default: `default`) |
| `--token` | `OPENSHELL_TOKEN` | Bearer token (overrides OIDC) |
| `--config` | — | Config file path |
| `--verbose`, `-v` | — | Increase verbosity (repeat for more) |
| `--no-color` | `NO_COLOR` | Disable coloured output |
| `--oidc-issuer` | — | OIDC issuer URL override |
| `--oidc-client-id` | — | OIDC client ID override |
| `--oidc-audience` | — | OIDC audience override |
| `--oidc-scopes` | — | OIDC scopes (space-separated) |
| `--client-secret-file` | — | File containing the OIDC client secret |
| `--token-leeway` | — | Token expiry leeway (default: `30s`) |
| `--write-token` | — | Write refreshed tokens to disk (Rust CLI schema) |
| `--vault-kv-mount` | `OPENSHELL_VAULT_KV_MOUNT` | Vault KV v2 mount holding auth config (must be set together with `--vault-kv-path`) |
| `--vault-kv-path` | `OPENSHELL_VAULT_KV_PATH` | Path within `--vault-kv-mount` holding auth config |
| `--vault-field-prefix` | `OPENSHELL_VAULT_FIELD_PREFIX` | Prefix prepended to each field name read from the Vault secret |

## Commands

### `sandbox create`

Create a sandbox and optionally connect to it. For running this from a
Kubernetes CronJob instead of interactively, see
[`docs/ci.md`](docs/ci.md).

```bash
# Create from a community image
openshellctl sandbox create --from python
openshellctl sandbox create --from python --name my-sandbox

# Create with GPU
openshellctl sandbox create --from pytorch --gpu       # driver default count
openshellctl sandbox create --from pytorch --gpu 2     # explicit count

# Create from a full image reference
openshellctl sandbox create --from ghcr.io/org/image:tag

# Create from a manifest file (everything in one file)
openshellctl sandbox create -f sandbox.yaml
cat sandbox.yaml | openshellctl sandbox create -f -

# Create from a manifest hosted at an http(s):// URL — e.g. a manifest kept
# in a git repo or object store, fetched the same way kubectl/oc do for
# "apply -f <url>" (see "Manifest URLs" below for the trust-boundary note;
# a1b2c3d below stands in for a real pinned commit SHA)
openshellctl sandbox create -f https://raw.githubusercontent.com/org/repo/a1b2c3d/sandbox.yaml

# Create from a templated manifest file (see "Manifest template parameters")
openshellctl sandbox create -f sandbox.yaml \
  -p NAME=my-run \
  -p PROMPT="retrieve and implement ROSAENG-74244"

# Create with uploads and port forwarding
openshellctl sandbox create --from python \
  --upload ./src:/workspace \
  --upload ./data:/data \
  --forward 8080 \
  --no-git-ignore

# Create and run a command
openshellctl sandbox create --from python -- python3 main.py

# Create without attaching
openshellctl sandbox create --from python --detach

# Output as JSON/YAML instead of table
openshellctl sandbox create --from python -o json
```

#### Create flags

| Flag | Description |
|------|-------------|
| `--from` | Image reference or community name (e.g. `python`, `pytorch`) |
| `--name` | Sandbox name (auto-generated if omitted; max 19 chars) |
| `-f`, `--file` | Manifest YAML file (`-` for stdin, or an `http(s)://` URL) |
| `-p`, `--param` | Template parameter `KEY=VALUE`, substituted for `${KEY}` in the `-f` manifest (repeatable) |
| `--param-file` | File of `KEY=VALUE` template parameters, one per line, `#` comments allowed (repeatable) |
| `--parameters` | Print the `-f` manifest's `${KEY}` parameter names, one per line, and exit without creating anything |
| `--ignore-unknown-parameters` | Do not error when `--param`/`--param-file` supplies a key the manifest does not reference |
| `--gpu [COUNT]` | Request GPU resources (bare = driver default, or specify count) |
| `--cpu` | CPU request (e.g. `500m`, `2`) |
| `--memory` | Memory request (e.g. `1Gi`, `512Mi`) |
| `--env` | Environment variable `KEY=VALUE` (repeatable) |
| `--label` | Label `key=value` (repeatable) |
| `--tty` | Force pseudo-terminal allocation |
| `--no-tty` | Disable pseudo-terminal (last-wins with `--tty`) |
| `--upload` | Upload `local[:dest]` after create (repeatable) |
| `--no-git-ignore` | Disable `.gitignore` filtering for uploads |
| `--forward` | Forward `[bind:]port` to sandbox |
| `--detach` | Return after create without attaching |
| `--provider` | Provider to attach (repeatable) |
| `--auto-providers` | Enable auto provider detection |
| `--no-auto-providers` | Disable auto provider detection |
| `--no-credential-warnings` | Suppress credential exposure warnings |
| `--approval-mode` | Proposal approval mode: `manual` (default) or `auto` |
| `--no-keep` | Delete the sandbox after the session ends |
| `--driver-config-json` | Driver config as inline JSON |
| `--policy` | Sandbox policy file (env: `OPENSHELL_SANDBOX_POLICY`) |
| `--replace` | Delete and wait for an existing sandbox with the same name before creating |
| `--replace-timeout` | Timeout waiting for the existing sandbox to be deleted (default `5m`) |
| `-o`, `--output` | Output format: `table` (default), `json`, `yaml` |

#### Manifest format

A manifest file (`-f`) lets you put everything — sandbox spec, command, and session behavior — into one YAML file:

```yaml
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sop-improve
  labels:
    team: sre-platform
spec:
  image: quay.io/redhat-services-prod/rosa-tenant/rosa-agent/rosa-agent:latest
  command: ["claude", "/job-sop-improve"]
  providerRefs:
    - name: rosa-general-vertex
    - name: rosa-agent-github
    - name: rosa-agent-jira
  env:
    ANTHROPIC_BASE_URL: https://inference.local
    ANTHROPIC_API_KEY: unused
    JIRA_EMAIL: sd-sre-platform+rosa-agent@redhat.com
    JIRA_BASE_URL: https://redhat.atlassian.net
  resources:
    cpu: "2"
    memory: 4Gi
    gpu:
      count: 1
  upload:
    - local: ./src
      dest: /workspace
  sessionOpts:
    noKeep: true
    forward: "8080"
    approvalMode: auto
    output: table
```

Then create and delete are both one-liners:

```bash
openshellctl sandbox create -f sop-improve.yaml
openshellctl sandbox delete -f sop-improve.yaml
```

**`spec.command`** works like a Kubernetes pod spec — it's the command (with arguments) to run inside the sandbox. If omitted, the sandbox's default entrypoint (`/bin/bash -l`) is used.

**`spec.sessionOpts`** groups client-side session behavior:

| Field | Type | Description |
|-------|------|-------------|
| `noKeep` | bool | Delete the sandbox when the session ends |
| `detach` | bool | Return after create, do not attach |
| `forward` | string | `[bind:]port` to forward to the sandbox |
| `approvalMode` | string | `manual` (default) or `auto` |
| `output` | string | `table` (default), `json`, or `yaml` |
| `replace` | bool | Delete and wait for an existing sandbox with the same name before creating — requires `metadata.name` |
| `replaceTimeout` | string | Duration (e.g. `"5m"`) bounding `replace`'s wait; default `5m` |

These fields are ignored by the operator — they control what the CLI does before and after creation (`replace`/`replaceTimeout` run before; the rest run after). CLI flags always override manifest values.

**`spec.policy`** (inline map) and **`spec.policyFile`** (path string) are mutually exclusive. `spec.policyFile` is resolved relative to the manifest file's directory; when the manifest is read from stdin or from an `http(s)://` URL, `policyFile` must be an absolute path.

#### Manifest URLs

`-f`/`--file` also accepts an `http(s)://` URL, on every command that accepts `-f` (not just `sandbox create`) — fetched via `net/http` before decoding, mirroring the `kubectl apply -f <url>` / `oc create -f <url>` convention:

```bash
openshellctl sandbox create -f https://raw.githubusercontent.com/org/repo/main/sandbox.yaml
openshellctl sandbox get -f https://internal.example.com/manifests/sb.yaml
```

The response is capped at 1 MiB and the request times out after 10s; a non-2xx response is an error naming the URL and status code, never decoded as a manifest. Redirects follow Go's default `http.Client` policy except for one refusal: an `https://` URL redirecting to a plain `http://` URL is rejected, since following it would let a man-in-the-middle on the second hop rewrite the manifest in flight. A URL containing userinfo (`https://user:pass@host/...`) is rejected outright — `net/http` would otherwise silently attach that as a Basic `Authorization` header, and no credential (gateway OIDC bearer token, Vault token, or otherwise) is ever attached to a manifest-fetch request regardless. Authenticated manifest URLs (one requiring its own bearer token or basic auth) are not supported.

**Trust boundary:** a manifest controls `spec.upload[].local` (which local files get uploaded), `spec.command` and `spec.env` (what runs inside the sandbox, with what environment), and `spec.policy`/`spec.policyFile` and `providerRefs` (what the sandbox is allowed to do and reach) — so whoever controls a `-f` URL's content controls all of that. Prefer a URL pinned to an immutable commit SHA (as in the example above, `.../org/repo/a1b2c3d/sandbox.yaml`) over a branch or tag ref that can change underneath you. A `--file-sha256`-style content-verification flag is a natural follow-up (not implemented here) for pinning to a specific manifest's bytes rather than trusting the host.

`spec.upload[].local` paths are resolved relative to the CLI's current working directory, not the manifest's location — true for a local-file manifest too, and unchanged by URL support; a URL manifest has no "manifest directory" a relative upload path could even mean.

#### Manifest template parameters

A manifest can declare `${KEY}` references anywhere in a string value (not in map keys), and `sandbox create -f` substitutes them before the manifest is validated — mirroring `oc process`'s `${PARAMETER_NAME}` convention. This is for the handful of values that are *not* stable run-to-run: a reused manifest's `metadata.name` (which must be unique per workspace) and the prompt passed to a long-running agent command, for example:

```yaml
# sandbox.yaml
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: ${NAME}
spec:
  image: quay.io/redhat-services-prod/rosa-tenant/rosa-agent/rosa-agent:latest
  command:
    - claude
    - --dangerously-skip-permissions
    - "${PROMPT}"
```

```bash
openshellctl sandbox create -f sandbox.yaml \
  -p NAME=my-run \
  -p PROMPT="retrieve and implement ROSAENG-74244"

# One KEY=VALUE per line instead of repeated -p; '#' comments and blank
# lines are ignored. A -p value always overrides the same key from a file
# (with a warning); a later --param-file overrides an earlier one.
openshellctl sandbox create -f sandbox.yaml --param-file params.txt

# List the manifest's parameter names (sorted) without creating anything.
openshellctl sandbox create -f sandbox.yaml --parameters
```

Rules:

- `KEY` matches `[A-Za-z_][A-Za-z0-9_]*`; a reference may appear more than once and may be embedded in literal text (e.g. `"${NAME}-worker"`).
- Every `${KEY}` left unresolved after substitution is an error listing every missing key — a literal `${KEY}` is never sent to the gateway. This applies even with **no** `-p`/`--param-file` given at all: a manifest containing a real `${KEY}` reference always needs it resolved, the same tradeoff `oc process` makes for any `${PARAMETER}` with no declared default (this card has no `parameters:`/defaults block).
- A `-p`/`--param-file` key the manifest does not reference is an error unless `--ignore-unknown-parameters` is set.
- `$KEY` (no braces) and `$${KEY}` are never substituted — `$${KEY}` is the literal text `$${KEY}` in the output; there is no other escape mechanism.
- A manifest with no `${...}` references at all, and no `-p`/`--param-file`/`--parameters` given, behaves exactly as without this feature (no YAML re-serialization, so original formatting/comments are untouched).
- Substitution only reaches string-valued scalars. A non-string field (e.g. `spec.resources.cpu`, `sessionOpts.noKeep`) keeps its `${KEY}` text as a literal string after substitution — `cpu: "${CPU}"` decodes as the string `"2"` (fine, `cpu` is already a string field), but `noKeep: ${KEEP}` fails manifest decoding, since a boolean field can't hold the substituted string. Template only string fields.
- `--parameters` cannot be combined with `-p`/`--param-file` — it's a usage error, matching `oc process`'s rejection of the same combination, rather than silently ignoring the supplied values.
- Substituted values are never logged, at any `-v` verbosity — only parameter *names* ever appear in output or error text.

### `sandbox get`

Show details of a single sandbox.

```bash
openshellctl sandbox get my-sandbox
openshellctl sandbox get my-sandbox -o json
openshellctl sandbox get my-sandbox -o yaml
openshellctl sandbox get -f sandbox.yaml       # read name from manifest
openshellctl sandbox get                       # uses last-used sandbox
```

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |
| `-o`, `--output` | Output format: `table` (default), `json`, `yaml` |

### `sandbox list`

List sandboxes in the current workspace.

```bash
openshellctl sandbox list
openshellctl sandbox list -o json
openshellctl sandbox list --all-workspaces
openshellctl sandbox list --names          # one name per line
openshellctl sandbox list --ids            # one ID per line
openshellctl sandbox list --selector "gpu=true"
openshellctl sandbox list --limit 10 --offset 20
```

| Flag | Description |
|------|-------------|
| `-o`, `--output` | Output format: `table` (default), `json`, `yaml` |
| `--all-workspaces` | List across all workspaces |
| `--names` | Print names only (one per line) |
| `--ids` | Print IDs only (one per line) |
| `--selector` | Label selector filter |
| `--limit` | Max results (default: 100) |
| `--offset` | Result offset for pagination |

### `sandbox delete`

Delete one or more sandboxes. Supports `-f` to read the sandbox name from a manifest, like `kubectl delete -f`.

```bash
openshellctl sandbox delete my-sandbox
openshellctl sandbox delete sb-1 sb-2 sb-3
openshellctl sandbox delete -f sandbox.yaml          # reads name from manifest
openshellctl sandbox delete --all
openshellctl sandbox delete my-sandbox --wait
openshellctl sandbox delete my-sandbox --wait --wait-timeout 2m
```

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |
| `--all` | Delete all sandboxes in the workspace |
| `--wait` | Wait for deletion to complete |
| `--wait-timeout` | Wait timeout (default: `5m`) |

### `sandbox stop` / `sandbox start`

Stop or start a sandbox.

```bash
openshellctl sandbox stop my-sandbox
openshellctl sandbox start my-sandbox
openshellctl sandbox stop -f sandbox.yaml    # read name from manifest
openshellctl sandbox start -f sandbox.yaml
openshellctl sandbox stop                    # uses last-used sandbox
openshellctl sandbox start                   # uses last-used sandbox
```

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |

### `sandbox exec`

Run a command inside a sandbox. The command must follow `--`.

```bash
openshellctl sandbox exec --name my-sandbox -- ls -la
openshellctl sandbox exec --name my-sandbox -- python3 -c "print('hello')"

# With environment variables
openshellctl sandbox exec --name my-sandbox --env FOO=bar -- env

# With working directory
openshellctl sandbox exec --name my-sandbox --workdir /workspace -- make build

# With timeout (seconds)
openshellctl sandbox exec --name my-sandbox --timeout 60 -- long-running-task

# Force/disable TTY
openshellctl sandbox exec --name my-sandbox --tty -- bash
openshellctl sandbox exec --name my-sandbox --no-tty -- cat /etc/os-release

# Interactive mode (stdin is forwarded when --tty + terminal detected)
openshellctl sandbox exec --name my-sandbox --tty -- bash
```

| Flag | Description |
|------|-------------|
| `-n`, `--name` | Sandbox name (defaults to last-used) |
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |
| `--env` | Environment variable `KEY=VALUE` (repeatable) |
| `--workdir` | Working directory inside the sandbox |
| `--timeout` | Timeout in seconds (0 = none) |
| `--tty` | Force pseudo-terminal allocation |
| `--no-tty` | Disable pseudo-terminal (last-wins with `--tty`) |

The process exit code matches the remote command's exit code.

### `sandbox connect`

Open an interactive shell session to a sandbox.

```bash
openshellctl sandbox connect my-sandbox
openshellctl sandbox connect -f sandbox.yaml   # read name from manifest
openshellctl sandbox connect                   # uses last-used sandbox
```

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |

Detach from the session with **Ctrl-P Ctrl-Q** (the connection closes cleanly and exits 0).

### `sandbox upload`

Upload files or directories to a sandbox.

```bash
openshellctl sandbox upload my-sandbox ./myproject
openshellctl sandbox upload my-sandbox ./myproject /workspace
openshellctl sandbox upload my-sandbox ./file.txt /remote/path/file.txt
openshellctl sandbox upload -f sandbox.yaml ./src /workspace
```

Arguments are `NAME LOCAL_PATH [DEST]`. If `DEST` is omitted, files are placed in the sandbox's home directory. Directory uploads use scp-like semantics (`./myproject` → `~/myproject/`).

By default, `.gitignore` rules are respected for directory uploads. Use `--no-git-ignore` to include all files.

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |
| `--no-git-ignore` | Disable `.gitignore` filtering for directory uploads |

### `sandbox download`

Download files or directories from a sandbox.

```bash
openshellctl sandbox download my-sandbox /workspace/output
openshellctl sandbox download my-sandbox /workspace/output ./local-dir
openshellctl sandbox download -f sandbox.yaml /workspace/output
```

Arguments are `NAME SANDBOX_PATH [DEST]`. If `DEST` is omitted, files are placed in the current directory.

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |

### `sandbox ssh-config`

Print an SSH configuration block for use with the upstream `openshell` binary.

```bash
openshellctl sandbox ssh-config my-sandbox
openshellctl sandbox ssh-config -f sandbox.yaml   # read name from manifest
openshellctl sandbox ssh-config                   # uses last-used sandbox
```

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |

This outputs a block suitable for `~/.ssh/config`, using `openshellctl ssh-proxy` as the ProxyCommand.

### `sandbox provider`

Manage providers attached to a sandbox.

```bash
# List providers
openshellctl sandbox provider list my-sandbox
openshellctl sandbox provider list -f sandbox.yaml

# Attach a provider
openshellctl sandbox provider attach my-sandbox github

# Detach a provider
openshellctl sandbox provider detach my-sandbox github
```

Attach and detach use optimistic concurrency — if another operation modifies the sandbox concurrently, you will see:

```
Failed to attach provider: sandbox was modified by another operation.
Please retry the command.
```

### `provider`

Manage provider configs (credentials/config shared across sandboxes), as opposed to `sandbox provider` above, which only attaches/detaches an existing provider to one sandbox. This is a top-level command.

```bash
# Create a provider
openshellctl provider create --name my-github --type github --credential GITHUB_TOKEN
openshellctl provider create --name my-github --type github --credential TOKEN=ghp_xxx --config org=acme

# Fetch, list, update, delete
openshellctl provider get my-github
openshellctl provider list
openshellctl provider list --names
openshellctl provider update my-github --credential TOKEN=ghp_yyy
openshellctl provider delete my-github
```

`--credential KEY[=VALUE]` either sets the credential inline (`KEY=VALUE`) or, given a bare `KEY`, reads the value from that environment variable — so `--credential GITHUB_TOKEN` reads `$GITHUB_TOKEN` rather than putting the secret on the command line. `--config KEY=VALUE` sets non-secret config. `--credential-expires-at KEY=TIMESTAMP` accepts epoch milliseconds or RFC3339; a timestamp of `0` clears a previously-set expiry. `--global-profile` uses a platform-scoped provider profile instead of a workspace-scoped one. `provider update` only touches the credentials/config you pass — anything not named stays as-is.

`--from-existing`, `--from-gcloud-adc`, and `--runtime-credentials` are not yet implemented in openshellctl and return a usage error naming the equivalent `openshell` invocation.

`provider list-profiles`, `provider profile`, and `provider refresh` are not yet implemented in openshellctl either — each prints a message pointing you at the upstream `openshell` binary rather than silently no-opping.

### `logs`

View sandbox logs. This is a top-level command (alias: `lg`).

```bash
openshellctl logs my-sandbox
openshellctl logs -f sandbox.yaml              # read name from manifest
openshellctl logs my-sandbox --tail            # stream live
openshellctl logs my-sandbox -n 50             # last 50 lines
openshellctl logs my-sandbox --since 5m        # last 5 minutes
openshellctl logs my-sandbox --level error     # errors only
openshellctl logs my-sandbox --source gateway  # gateway logs only
```

| Flag | Description |
|------|-------------|
| `-f`, `--file` | Manifest file to read sandbox name from (`-` for stdin, or an `http(s)://` URL) |
| `--tail` | Stream live logs |
| `-n` | Number of log lines (default: 200) |
| `--since` | Show logs from this duration ago (e.g. `5m`, `1h`, `30s`) |
| `--level` | Minimum level: `error`, `warn`, `info`, `debug`, `trace` |
| `--source` | Filter by source: `gateway`, `sandbox`, or `all` (default) |

### `gateway` (alias `gw`)

Register a gateway without the upstream Rust binary, and manage registrations.

```bash
# Register a gateway — discovers the OIDC issuer automatically from
# <endpoint>/auth/oidc-config; authenticates immediately if a client secret
# is set (OPENSHELL_OIDC_CLIENT_SECRET), otherwise opens a browser.
openshellctl gateway add https://gw.example.com:443 --name my-gw

# Register against a gateway whose OIDC issuer can't be auto-discovered.
openshellctl gateway add https://gw.example.com:443 --name my-gw \
  --oidc-issuer https://issuer.example.com

# List, switch, log out of, and remove registrations.
openshellctl gateway list
openshellctl gateway select my-gw
openshellctl gateway logout       # clears the cached token, keeps the registration
openshellctl gateway remove my-gw

# Log in via OIDC browser flow (same as the root `login` command).
openshellctl gateway login
```

| Flag | Description |
|------|-------------|
| `--name` | Gateway name (`gateway add`; default: derived from the endpoint host) |
| `--force` | `gateway add`: overwrite an existing registration under the same name |
| `--no-browser` | Skip the browser-login fallback when no client secret is available (env `OPENSHELL_NO_BROWSER`, real boolean — `0`/`false` means browser allowed) |
| `--oidc-issuer` | OIDC issuer URL override — skips discovery (global flag, also used by `token`/`whoami`) |
| `--oidc-client-id`, `--oidc-audience`, `--oidc-scopes` | OIDC overrides (global flags) |
| `-o`, `--output` | `gateway list` format: `table` (default), `json`, `yaml` |

`gateway add` only registers OIDC gateways. A gateway that doesn't answer `/auth/oidc-config` and has no `--oidc-issuer` override fails with a clear error rather than being silently misregistered. If authentication fails after registration (wrong secret, unreachable issuer), the registration is automatically rolled back — a corrected retry doesn't need a manual `gateway remove` first. See [`docs/gateway.md`](docs/gateway.md) for the metadata schema and the full list of differences from the upstream Rust CLI.

### `doctor`

Connectivity/auth preflight — one line per check, with the exact next command for any failure. See [First run: `openshellctl doctor`](#first-run-openshellctl-doctor) above and [`docs/doctor.md`](docs/doctor.md) for every check, its skip rule, and its hint.

```bash
openshellctl doctor                              # all 9 checks (Providers shown as skipped)
openshellctl doctor --provider my-openai         # also check a provider exists
openshellctl doctor -f sandbox.yaml -o json      # providerRefs from a manifest; machine-readable output
```

| Flag | Description |
|------|-------------|
| `-o`, `--output` | Format: `table` (default), `json`, `yaml` |
| `--provider` | Provider name/type to check exists on the gateway (repeatable); wins over `-f` when both are given |
| `-f`, `--file` | Manifest file to read `providerRefs` from, when `--provider` is not given (`-` for stdin, or an `http(s)://` URL) |

### `token`

Inspect and manage the gateway authentication token.

```bash
openshellctl token show                # show token metadata
openshellctl token refresh             # force refresh
openshellctl token refresh --write     # refresh and persist
openshellctl token inspect <JWT>       # decode JWT claims
```

### `policy lint`

Client-side validation of a sandbox policy file.

```bash
openshellctl policy lint policy.yaml
```

Checks cover process uid/gid ranges, filesystem path constraints, middleware selectors, endpoint ports, and TCP access rules. The gateway's server-side validation remains authoritative.

### `version`

Print build information.

```bash
openshellctl version
```

Shows: version, commit, OpenShell pin version, and SDK version.

### `completion`

Generate shell completion scripts.

```bash
openshellctl completion bash
openshellctl completion zsh
openshellctl completion fish
openshellctl completion powershell
```

## Aliases

| Command | Alias |
|---------|-------|
| `sandbox` | `sb` |
| `logs` | `lg` |
| `gateway` | `gw` |

```bash
openshellctl sb list          # same as: openshellctl sandbox list
openshellctl lg my-sandbox    # same as: openshellctl logs my-sandbox
openshellctl gw list          # same as: openshellctl gateway list
```

## Last-used sandbox

Commands that take a sandbox name remember the last-used sandbox per gateway and workspace. If you omit the name argument, the last-used sandbox is used automatically:

```bash
openshellctl sandbox exec --name my-sandbox -- whoami   # saves my-sandbox
openshellctl sandbox exec -- whoami                     # reuses my-sandbox
openshellctl sandbox connect                            # reuses my-sandbox
openshellctl logs                                       # reuses my-sandbox
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic / RPC error |
| 2 | Usage error (bad flags, validation failure, invalid argument rejected by gateway) |
| 3 | Authentication failure (expired/invalid/missing credentials — nothing that re-authenticating can't fix) |
| 4 | Not found (sandbox, gateway) |
| 5 | Conflict (already exists, concurrent modification) |
| 6 | Provisioning failure or timeout |
| 7 | Permission denied (authenticated, but not authorized — `token refresh` will not help) |
| N | Remote process exit code (from `exec`/`connect`/`create` with attached command) |

See [`docs/troubleshooting.md`](docs/troubleshooting.md) for the common onboarding errors behind codes 2/3/7 and exactly what changed to make them honest.

## Environment variables

| Variable | Description |
|----------|-------------|
| `OPENSHELL_GATEWAY` | Default gateway name |
| `OPENSHELL_GATEWAY_ENDPOINT` | Gateway endpoint URL |
| `OPENSHELL_GATEWAY_INSECURE` | Skip TLS verification (`true`/`1`) |
| `OPENSHELL_WORKSPACE` | Default workspace |
| `OPENSHELL_TOKEN` | Bearer token (overrides all other auth) |
| `OPENSHELL_OIDC_CLIENT_SECRET` | OIDC client secret (enables service account auth) |
| `OPENSHELL_OIDC_CLIENT_ID` | OIDC client ID override (default from gateway metadata) |
| `OPENSHELL_OIDC_ISSUER` | OIDC issuer URL override (default from gateway metadata) |
| `OPENSHELL_SANDBOX_POLICY` | Default sandbox policy file path |
| `NO_COLOR` | Disable coloured output (any value) |
| `OPENSHELL_NO_BROWSER` | Boolean (`1`/`true`, case-insensitive): when a client secret is unavailable, `gateway add` registers the gateway and prints a login hint instead of opening a browser |

## Differences from the upstream Rust CLI

openshellctl is a compatible reimplementation with these intentional differences:

- **Sandboxes as code**: `-f` manifests (`kind: Sandbox`), `${KEY}` template parameters with `-p`/`--param-file`, and `http(s)://` manifest URLs are openshellctl-only — the upstream CLI takes everything as flags. See [Manifest format](#manifest-format).
- **Vault-sourced auth config**: `--vault-kv-mount`/`--vault-kv-path`/`--vault-field-prefix` fill gateway and OIDC settings from a Vault KV v2 secret. See ["4. Vault-sourced config"](#4-vault-sourced-config).
- **Pure Go, zero dependencies**: no shell-out to `ssh`, `tar`, `git`, or any other binary — including the upstream `openshell` CLI. All SSH transfer, tar streaming, and gitignore filtering are implemented in-process. Single static binary.
- **Per-request auth with automatic retry**: tokens are resolved per-RPC (not baked in at startup). On `Unauthenticated`, the token is refreshed (or re-minted for service accounts) and the request retried. Long-running sessions and `--no-keep` cleanup work reliably even after token expiry.
- **Embeddable**: `pkg/gateway`, `pkg/transfer`, `pkg/sandbox`, and `pkg/auth` are importable Go packages with interface-driven I/O seams, suitable for use in operators and controllers.
- **No interactive spinner**: provisioning progress uses plain text output instead of terminal spinners.
- **`policy lint` is a separate top-level command** (not under `sandbox`).
- **Gitignore filtering**: uses compiled `.gitignore` rules instead of shelling out to `git ls-files`. Tracked-but-ignored files are excluded (matching untracked behaviour) rather than re-included.
- **`gateway add` registers OIDC gateways only**: no mTLS or non-OIDC ("edge") gateway registration. When a client secret is absent and `OPENSHELL_NO_BROWSER` is set, `gateway add` registers the gateway and prints a hint instead of opening a browser (upstream always opens one). See [`docs/gateway.md`](docs/gateway.md).
- **Endpoint matching tolerates formatting differences**: upstream only trims a trailing slash. openshellctl also ignores scheme/host case and an explicit default port (`:443`/`:80`), so `https://host:443` (as `gateway add` writes it) still matches `https://host/` (as an exported env var commonly reads). A different port, or a different path/query/fragment, is still a different gateway. See [`docs/gateway.md`](docs/gateway.md).

## License

See [LICENSE](LICENSE).
