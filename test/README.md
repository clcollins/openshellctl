# test/

End-to-end UX tests for openshellctl. These validate the "three command"
getting-started path that a new user follows on first contact with the tool:

```
export VAULT_ADDR=https://vault.example.com
vault login -method=oidc
openshellctl sandbox list
```

The test runs those three commands inside a Fedora container, with `vault login`
running interactively — Vault prints a URL and the SRE opens it in a browser to
authenticate. On success the script verifies both coordinate sources that work
without CLI flags.

## What sandbox list needs

`openshellctl sandbox list` with no flags only consults Vault if it knows
where to look — the KV mount, path, and field prefix. These coordinates come
from exactly one of three sources:

1. **CLI flags** (`--vault-kv-mount`, `--vault-kv-path`, `--vault-field-prefix`)
2. **`OPENSHELL_VAULT_*` environment variables** (`OPENSHELL_VAULT_KV_MOUNT`,
   `OPENSHELL_VAULT_KV_PATH`, `OPENSHELL_VAULT_FIELD_PREFIX`) — the image
   bakes these in so a user who has only run `vault login` can run
   `sandbox list` without any flags
3. **Persisted `gateways/<name>/vault_auth.json`** — written by
   `openshellctl gateway add --vault-kv-*` and reused automatically for the
   active gateway on later commands

With none of the three, Vault is never read and the command fails immediately
with `No active gateway`.

The test covers:

- **Source 2** — `openshellctl sandbox list` with `OPENSHELL_VAULT_*` env vars
  (baked into the image); must exit 0
- **Source 3** — registers a gateway in a fresh `$HOME` with env vars unset via
  `gateway add --vault-kv-*`, then runs bare `sandbox list` from that `$HOME`;
  asserts `vault_auth.json` was written correctly and the command exits 0;
  cleans up with `gateway remove`

## Files

| File | Purpose |
|------|---------|
| `Containerfile.e2e` | Fedora-based image with vault and a pinned openshellctl release pre-installed |
| `e2e-ux-test.sh` | Test script installed into the image |

## Why Fedora?

The base image is `registry.fedoraproject.org/fedora` rather than a minimal
or production-optimised base. This is intentional: these are _UX_ tests. We
want to mirror the environment a typical user or contributor actually sits in
front of — a full general-purpose Linux distro with standard tooling. Using
Fedora here catches packaging or PATH surprises that a stripped-down base
would hide.

Vault is installed from the HashiCorp dnf repository (same source as
`clcollins/toolbox-devtools`) with `openbao` excluded so it does not shadow
`/usr/bin/vault`. `openshellctl` is installed from the GitHub release tarball
with a pinned sha256 checksum, mirroring the same pattern.

## Makefile targets

```
make e2e             # build image + run the test (vault login -method=oidc inside container)
make e2e-image       # build the image only
make e2e-shell       # interactive shell in the image for manual exploration
make e2e-checksum    # print the sha256 for the pinned openshellctl version
make e2e-bump        # update the pinned version + checksum in the Makefile
```

### make e2e

Requires `VAULT_ADDR`:

```
make e2e VAULT_ADDR=https://vault.example.com
# or
export VAULT_ADDR=https://vault.example.com
make e2e
```

The container runs `vault login -method=oidc` without a TTY using the
same approach as [openshift/ocm-container's ports feature][ocm-ports] and
[openshift/osdctl's `buildOIDCArgs`][osdctl-vault]:

```
vault login -method=oidc -no-print \
  skip_browser=true listenaddress=0.0.0.0 \
  port=8250 callbackport=<host-port>
```

- `skip_browser=true` — Vault prints the URL instead of launching a browser
- `listenaddress=0.0.0.0` — the OIDC callback listener inside the container
  accepts connections from any interface
- `port=8250` — the OIDC listener port inside the container
- `callbackport=<host-port>` — the port Vault tells the browser to redirect to
  after authentication; `make e2e` publishes this via
  `-p 127.0.0.1:$(E2E_VAULT_CALLBACK_PORT):8250`

The SRE opens the printed URL in a browser. The browser redirects to
`http://localhost:<E2E_VAULT_CALLBACK_PORT>/oidc/callback` after
authentication; the host-side port-forward delivers that callback into the
container. Vault writes `~/.vault-token` inside the container. No token is
passed on the command line or echoed by `make`.

**Port note**: `E2E_VAULT_CALLBACK_PORT` defaults to `8250` (same default as
ocm-container). The Vault OIDC role's `allowed_redirect_uris` must include
`http://localhost:<port>/oidc/callback`. A non-default port requires the Vault
OIDC role to be updated to permit that redirect URI.

**Re-use of existing session**: if `vault token lookup` succeeds when the
script starts, the login step is skipped. This makes `make e2e-shell` +
manual re-run inside the container fast.

**Token file**: `~/.vault-token` is not mounted from the host. Single-file
bind mounts break vault's atomic rename on re-authentication; this is why
[ocm-container deprecated token-file mounts][ocm-osdctl-docs].

[ocm-ports]: https://github.com/openshift/ocm-container/blob/main/pkg/features/ports/ports.go
[osdctl-vault]: https://github.com/openshift/osdctl/blob/main/pkg/utils/vault.go
[ocm-osdctl-docs]: https://github.com/openshift/ocm-container/blob/main/docs/features/osdctl.md

## Note: the image tests the released tarball, not the PR's own code

`make e2e` downloads and verifies the pinned `E2E_OPENSHELLCTL_VERSION` release
tarball (currently `v0.1.5-rc.2`). A regression introduced on a feature branch
will not cause `make e2e` to fail — it tests the released binary, not the
branch's build. This is correct for "does the RC a coworker will install work",
but not for CI gating of the branch itself. A follow-up can add an
`e2e-local` target that accepts `dist/openshellctl_linux_amd64_v1/openshellctl`
from `make build` via a build-arg or bind mount. Note that `.containerignore`
currently excludes `dist/`, so `e2e-local` would need to remove that exclusion
or pass the binary via a different mechanism.

## Updating the pinned openshellctl version

```
make e2e-bump E2E_OPENSHELLCTL_VERSION=0.1.6-rc.1
```

This fetches the new version's checksums file from GitHub, extracts the
`openshellctl_Linux_x86_64.tar.gz` sha256, and rewrites both
`E2E_OPENSHELLCTL_VERSION` and `E2E_OPENSHELLCTL_CHECKSUM` in the Makefile
in one step.
