# 0004 — openshellctl target OpenShell version and support policy

Status: decided, 2026-10-13
Tracked: ROSAENG-74481 (UPG-S3)
Supersedes: plan 0002 §10 forward-compat notes (which noted 0.1.0-pre.1 but made no commitment)

## Context

openshellctl is pinned at OpenShell SDK v0.0.116 (hack/openshell-pin). Upstream has
released v0.1.3 with breaking wire-protocol and policy changes. rosa-agent's gateway
will cut over to 0.1.x (RA-5c), at which point any openshellctl build that only speaks
0.0.x will stop working.

## Decisions

### 1. Dual 0.0.x / 0.1.x support

openshellctl will support both 0.0.x and 0.1.x gateways simultaneously. Not 0.1.x-only.

**Why:** rosa-agent's gateway cutover (RA-5c) has its own timeline. Users on the current
0.0.x gateway must not break before that cutover happens. openshellctl detects the
gateway version at connect time and adapts behaviour accordingly.

### 2. Target version and release numbering

Target: upstream v0.1.3 (latest release at time of decision).

openshellctl release numbering mirrors upstream — the release that achieves 0.1.x parity
ships as openshellctl v0.1.3.

### 3. Switch timing

rosa-agent's gateway cutover (RA-5c) is the hard gate. openshellctl's 0.1.x release
must exist and be deployed before that happens. Backward compatibility with the current
0.0.x gateway stays until the cutover completes.

### 4. Policy rules

Adopt all v0.1.x policy rules. The server enforces them anyway — carrying stale 0.0.x
rules would cause silent rejections.

### 5. Final 0.0.x release contents (UPG-00)

The last 0.0.x release ships all M0 cards:

- Epic 1 (credentials and path safety): AUTH-01, AUTH-02, AUTH-03, AUTH-05, AUTH-06
- Epic 2 (sandbox data safety): SBX-01, SBX-04, SBX-11, SBX-14

Releases are managed by Konflux, tag-driven. Release process setup owned by Chris Collins.

### 6. ErrGatewayTooOld exit code

Exit code 1 (generic failure). No dedicated exit code — this is not a condition callers
should programmatically distinguish from other fatal errors.

### 7. --color flag / OPENSHELL_COLOR

No. openshellctl does not implement `--color WHEN` or `OPENSHELL_COLOR`.

This is an intentional divergence from upstream. Document in the divergence register
(hack/parity/) so the scheduled parity evaluation does not re-report it.

**Rationale:** openshellctl already uses stderr-is-a-TTY detection for colour. A manual
override adds flag surface and test matrix for negligible user benefit in the SRE
automation context.

### 8. --limit / --offset flags

Drop outright. No deprecated aliases — no one is using them yet.

These flags exist in upstream's `sandbox list` but are not implemented in the current
codebase. Removing them now avoids carrying dead surface into 0.1.x.

## Downstream cards affected

These decisions feed directly into:

- **UPG-00** — final 0.0.x release scope (decision 5)
- **UPG-05** — 0.1.x policy adoption (decision 4)
- **UPG-10** — gateway version detection and dual support (decisions 1, 3, 6)
- **UPG-11** — 0.1.x CLI parity delta (decisions 7, 8)
