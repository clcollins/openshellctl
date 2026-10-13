# openshellctl

Lightweight Go CLI and embeddable client for NVIDIA OpenShell gateways, built for HyperShell integration with automatic OIDC token refresh and YAML-templated sandbox configuration processing (sandbox-as-code).
Pure-Go reimplementation of the upstream Rust CLI — no shell-out to ssh, tar, git, or any external binary.

## Build and test

```bash
make build              # build via goreleaser (snapshot)
make test               # unit tests
make lint               # golangci-lint
make test-race          # tests with race detector
make coverage           # coverage report
make test-coverage-threshold  # enforce coverage minimums
make verify             # pin + tidy + generate checks
make test-all           # all of the above
```

Go version is declared in `go.mod` (currently 1.26). CI runs outside GitHub Actions; run `make test-all` locally to reproduce what CI checks (verify, test, lint, image build).

## Project layout

```
cmd/openshellctl/       # main entrypoint
internal/cli/           # cobra command tree, CLI integration tests
pkg/api/v1alpha1/       # API types (Sandbox manifest schema)
pkg/auth/               # OIDC token resolution and refresh
pkg/gateway/            # gRPC gateway client (interface + impl)
pkg/gateway/mock/       # generated mock for gateway.Gateway
pkg/gatewayconfig/      # config file and env resolution
pkg/output/             # table/json/yaml output formatting
pkg/policyyaml/         # sandbox policy parsing and validation
pkg/sandbox/            # sandbox spec merging and construction
pkg/transfer/           # SSH file transfer (upload/download/connect/exec)
internal/version/       # build metadata (version, commit, pin)
docs/plans/             # design docs and implementation plan
hack/                   # verification scripts, pins
hack/parity/            # upstream CLI parity tracking
build/                  # Dockerfile
```

## Code architecture

The codebase follows a **main-driver + pure-functions** pattern:

- **Thin entry points** (`internal/cli/`) wire cobra commands to business logic.
- **Pure functions** contain all decisions and transformations — they take values in and return values out with no side effects. Examples: `sanitizeTarName`, `validateSymlinkTarget`, `parseAndValidateSourcePath`, `planUpload`, `shellEscape`.
- **Thin I/O shells** call the pure functions and handle the actual SSH/gRPC/filesystem interaction. The shells are deliberately small so they don't need complex mocking.
- **Interfaces at boundaries**: `gateway.Gateway` is the primary seam — `pkg/sandbox` and `pkg/gateway` tests use the generated mock. CLI tests inject the same mock through the `cliDeps` context seam (`internal/cli/deps.go`). `withDeps(ctx, cliDeps{Gateway: ..., Target: ..., TokenSource: ...})` attaches test doubles to a context, and `depsFrom(ctx)` reads them back. `resolveAuth`/`dialOrInjected` (`authwiring.go`) are the single decision point that checks for injected deps before doing a real token resolve/dial; `withGatewayTarget` (`sandbox_cmd.go`) composes those two rather than re-checking `cliDeps` itself, so a command never sees one seam honor an injected dep that another ignores. This lets a test run a command end-to-end through `root.Execute()` against a `mock.MockGateway` with no network. The `runCmdWithGateway(t, deps, args...)` test helper (`runcmd_gateway_test.go`) builds the root command, injects `deps` via `withDeps`, and executes it, mirroring the plain `runCmd` helper used for usage-only tests. `fs.FS` with extension interfaces (`LstatFS`, `ReadlinkFS`) abstracts filesystem access for upload logic.

When adding new functionality, follow this pattern: extract every decision into a pure function, test it exhaustively, and keep the I/O shell minimal.

## Development workflow

Follow test-driven development:

1. **Write tests first** based on the desired behavior and edge cases.
2. **Implement the code** to make the tests pass.
3. **Validate** by running `make test` and `make lint`.

## Guiding principles

These apply to every change, not just new features:

- **TDD, strictly.** Write the failing test first, confirm it fails for the
  intended reason, implement the minimum to pass, then refactor. A commit
  whose tests were written after the code is rejected in review.
- **Pure functions first, thin I/O shells second.** Parsing, validation,
  precedence, message formatting, and decision logic are pure functions;
  I/O (filesystem, network, gRPC, terminal) lives in a shell deliberately
  too small to need mocking. Inject `fs.FS`, `func() time.Time`, `getenv`,
  and interfaces at boundaries — see "Code architecture" above for the
  `cliDeps`/`gateway.Gateway` seam this codebase already uses everywhere.
- **Flat structure.** A cobra `RunE` calls at most one orchestration
  function; that function calls pure functions. Two levels deep is the
  target, three is the ceiling and must be justified in the commit message.
  No new package-level mutable state or globals.
- **Hermetic tests, always.** No network, no real filesystem outside
  `t.TempDir()`/`fstest.MapFS`, no host env, no binaries. HTTP endpoints use
  `httptest.Server` only; gRPC uses `mock.MockGateway` or `bufconn`.
- **Typed errors.** New errors are structs implementing `error` with
  `Is`/`Unwrap`, matched with `errors.As`, never by string. Exit-code
  mapping goes in `exitCodeFor` only.
- **Upstream strings are sacred.** Where upstream prints a message,
  reproduce it byte-for-byte except the binary name becomes `openshellctl`.
  Deviations are documented under README "Differences from the upstream
  Rust CLI".
- **No shell-out, ever.** Not to `openshell`, `ssh`, `curl`, `getent`,
  `python`. DNS is `net.Resolver`; HTTP is `net/http`; the OIDC mint is
  `pkg/auth`.
- **Small changes.** One commit = one behaviour change + its tests,
  typically under ~200 changed lines. Split a task further before coding
  if it grows beyond that.

## Review checklist

Run through these dimensions before considering a change done — whether
you're the author or reviewing someone else's diff:

**Per-commit** (one task's diff):

- **Correctness** — does the code do what the commit says; trace every
  branch; check error returns and nil pointers.
- **Tests** — written first; cover each input/output path and error; are
  they hermetic; are tables complete.
- **Idiomatic Go** — `gofumpt`, naming, receiver choice, `%w` error
  wrapping, context first, no unnecessary exports, doc comments on exported
  identifiers.
- **Lint** — `make lint` and `go vet` clean.
- **Intent** — does the diff match what was actually asked for; flag scope
  creep and missing requirements.
- **Security** — secrets never logged or written to disk unless specified;
  file perms `0600`/`0700`; no path traversal via gateway names; TLS
  verification never implicitly disabled; tokens never printed in full.
- **Quality** — readability, function length, the two-level depth rule,
  duplicated logic that should be a shared pure function, message wording
  consistency with upstream.

**Per-PR** (the whole branch diff):

- **Acceptance** — walk the feature's acceptance criteria one by one; cite
  the test or code that satisfies each; fail any unmet item.
- **Parity** — for any command mirroring an upstream `openshell` command,
  diff flags/strings against `hack/parity/`; update it when they drift.
- **Docs** — README/docs updated for every new flag, command, env var, exit
  code, and error hint; examples parse (`go test ./internal/cli/... -run
  TestDocsExamplesParse`).
- **Regression** — `make test-all`, `make test-coverage-threshold`, `make
  verify` all green; no coverage reduction for touched packages.

## Testing requirements

### Coverage

Coverage thresholds are available via `make test-coverage-threshold` (not yet enforced in CI — see the CI-hardening issue #10):
- `pkg/` packages: minimum 85% (enforced, exits non-zero)
- `internal/cli/`: minimum 70% (aspirational, prints warning only)

These thresholds are a **floor, not a target**. Aim to cover every input/output path for a given function. Do not test imported libraries or standard-library behavior — test your code's logic.

### Hermetic tests

All unit tests must be fully hermetic:

- **No network calls**: no HTTP, gRPC, or API requests. Mock all external services via interfaces (e.g., `gateway.Gateway` mock).
- **No host filesystem access**: use `t.TempDir()` for any filesystem operations. Never read from or write to the real filesystem outside the temp directory.
- **No external tooling**: do not shell out to `ssh`, `tar`, `git`, `sh`, or any other binary. All such functionality is implemented in-process (archive/tar, golang.org/x/crypto/ssh, go-gitignore).
- **No host state**: tests must not depend on environment variables, user config, running services, or network availability.

The test SSH server runs in-process over buffered in-memory connections, authenticates with `NoClientAuth`, and implements remote commands in Go against `t.TempDir()` trees.

### Test structure

- Use **table-driven tests** with named subtests (`t.Run`).
- Use `t.Helper()` in test helper functions.
- Test both valid and invalid inputs — every error path a function can produce should have a corresponding test case.
- Place pure-function tests in `*_test.go` alongside the code. The `pure_test.go` convention groups all pure-logic tests in a package; `smoke_test.go` covers I/O integration.

### What to test vs. what not to test

**Test**: every pure function's input/output paths — valid inputs, boundary cases, and each distinct error condition your code produces.

**Do not test**: behavior of imported libraries (e.g., don't test that `archive/tar` correctly reads tar headers, or that `os.OpenRoot` rejects escapes — test that *your code* calls them correctly and handles their return values). Do not test Go standard library contracts.

## Style and conventions

- Go source follows `gofumpt` formatting (enforced by golangci-lint).
- Prefer unexported functions unless the API surface requires export.
- No CGO — the binary is statically linked (`CGO_ENABLED=0`).
- Container engine is `podman` (not docker) for local development; CI uses `docker`.
