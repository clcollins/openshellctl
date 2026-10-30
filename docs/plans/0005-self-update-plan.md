# 0005 — update notice and `openshellctl update` (self-update)

Status: accepted; implemented in feat/0005-self-update. Base branch: PR #58 head (`4ea8271`, "style(cli): gofumpt create_helpers_test.go") — all file paths and line references below are against that tree; branch from it so the series applies with no conflicts once #58 merges.
Module: `github.com/openshift-online/openshellctl` (`pkg/selfupdate` new, `internal/version`, `internal/cli`, `.goreleaser.yaml`)
Author's intent: hand this to an implementing agent as-is. Every decision below was checked against the current source of osdctl, srepd, the GitHub CLI and the published openshellctl releases; where this plan departs from osdctl/srepd it says why.

## 0. Scope

**In scope**

1. **Update notice.** Every `openshellctl` command, after it finishes, prints a one-line notice to stderr when a newer release exists. The check is silent on any failure: no network, DNS failure, GitHub API down or slow, rate-limited, unparsable response. It never delays a command by more than a fixed small budget.
2. **`openshellctl update [--version vX.Y.Z] [--prerelease] [--check]`.** Resolves the target release (latest *stable* by default — pre-releases and drafts are never selected unless named with `--version` or opted into with `--prerelease`), downloads the correct archive for the running OS/arch, verifies its SHA-256 against the release's checksum file **before** extracting anything, extracts only the `openshellctl` entry, and atomically replaces the running executable.
3. **Release-side changes** needed for (1) and (2) to be trustworthy: mark `-rc.N` tags as GitHub pre-releases, and sign the checksum file.

**Out of scope**

- Automatic/unattended updating. The notice only tells; `update` only runs when asked.
- Windows (`.goreleaser.yaml` builds linux/darwin only; the replace logic is POSIX rename). Add Windows later behind a `goos` switch if a build is ever added.
- Package-manager installs (brew/rpm). None exist today; the `installMethod` hook in §3.4 leaves room for them exactly as osdctl does.
- Updating the upstream `openshell` Rust binary. Upstream has no self-update command (checked `crates/openshell-cli/src/main.rs` at the v0.1.5 pin — top-level commands are Sandbox, Forward, Service, Logs, Policy, Settings, Rule, Provider, Profile, Workspace, Gateway, Status, Whoami, Doctor, Term, Completions, SshProxy), so `update` is an openshellctl-only extension and goes into the divergence register (§6).

## 1. What osdctl and srepd do, and what we keep or change

| Concern | osdctl (`cmd/upgrade.go`, `pkg/utils/version.go`, `cmd/cmd.go`) | srepd (`pkg/tui/update.go`, `cmd/update.go`) | openshellctl (this plan) |
|---|---|---|---|
| Where the notice runs | `PersistentPreRun`, **synchronously**, 10 s HTTP timeout — every command can stall up to 10 s offline | Async `tea.Cmd` in the TUI; CLI path has no notice | Background goroutine started in `PersistentPreRunE`, result collected **after** the command in `Execute()`; 24 h on-disk cache; total wall-clock budget ≈ 2 s (§3.2) |
| Behaviour on failure | Prints two `WARN:` lines to stderr | Silent (debug log) | Silent — explicit requirement |
| Version source | `/releases/latest` only | `/releases/latest` only | `/releases?per_page=30` once, select client-side (§2.3) — needed to (a) ignore pre-releases even if mis-flagged, (b) offer rc→rc notices to users already on an rc |
| Version comparison | `coreos/go-semver`, panics on `dev` | Hand-rolled dotted-int compare; `-rc.1` suffix parsed by `Sscanf` as `1`, pre-release ordering wrong | `golang.org/x/mod/semver` (Go team, no transitive deps, correct pre-release ordering) |
| Checksum verification | **None** | **None** | SHA-256 against goreleaser's `openshellctl_<ver>_checksums.txt`, verified before extraction (§3.3); signature verification of the checksum file in a second phase (§5) |
| Archive handling | Streams tar from HTTP straight into a file; no size cap; matches entry by name | Same, writes `<exe>.tmp` next to the binary | Spool archive to disk → verify digest → extract only the regular-file entry named `openshellctl`, with a size cap, rejecting symlinks/dirs |
| Replace | `os.Rename` into `dir(exe)/osdctl` from a temp dir under `$HOME` | `os.Rename(<exe>.tmp, exe)` | Temp file in `dir(realExe)` (same filesystem, so rename is atomic), mode copied from the existing binary, `os.Rename` over `realExe` where `realExe = EvalSymlinks(os.Executable())` |
| Pre-releases | GitHub's `/latest` excludes them (only if the release is flagged) | Same | Never selected by default, flagged **or not** (semver pre-release check client-side), `--prerelease` / `--version` opt in |
| Managed installs | `InstallMethod` ldflag → refuse with instruction | None | Same hook (`installMethod` ldflag), set to `container` by `build/Containerfile` |
| Opt-out | `--skip-version-check/-S` flag | — | `--no-update-check` persistent flag + `OPENSHELL_NO_UPDATE_CHECK` env (viper, same as every other persistent flag); also skipped when not a TTY or under CI (§3.2) |

Industry references used for the notice design: GitHub CLI `internal/update/update.go` (24 h window, YAML state file written `0600`, `GH_NO_UPDATE_NOTIFIER`, check only when stdout *and* stderr are TTYs and not CI, "dev" never triggers a notice).

## 2. Decisions

### 2.1 Library vs. in-house

**In-house, stdlib + `golang.org/x/mod/semver`.** Reasons: AGENTS.md ("pure functions first, thin I/O shells", "hermetic tests", small dependency surface); linux/darwin only, so the hard part of the self-update libraries (Windows replace, rollback) buys nothing; the two reference tools are in-house too. `creativeprojects/go-selfupdate` (v1.7.0 at time of writing) would be the choice if a library is preferred — it has a `ChecksumValidator{UniqueFilename: "..."}` for goreleaser checksum files and an `ECDSAValidator` — but it pulls in the GitHub, GitLab and Gitea API clients. Decision can be reversed in review without changing the command surface.

### 2.2 Version identity of the running binary

`internal/version.Version` is `dev` unless goreleaser/Make injected it. Users who followed README's `go install …@latest` get `dev` today even though `debug.ReadBuildInfo().Main.Version` holds the real module version. Add a fallback: when the ldflag value is `dev` and `Main.Version` is a valid semver (not `(devel)`), report that. This is a small, independent improvement and the first commit.

Classification (pure function `selfupdate.ParseCurrent(v string) Current`):

- valid semver (`v0.1.5-rc.1`, `v0.1.5`) → comparable; `IsPrerelease` from `semver.Prerelease`.
- anything else (`dev`, `4ea8271`, git-describe `v0.1.3-rc.1-12-g4ea8271-dirty`) → `Unknown`. Notice is suppressed; `update` still works and says `current build is not a release (dev); installing vX`.

### 2.3 Release selection (pure function)

```go
// SelectRelease picks the release `update`/the notice should target.
//   allowPrerelease=false: newest non-draft, non-prerelease (by GitHub flag AND by semver) release.
//   allowPrerelease=true : newest non-draft release of any kind.
// For the notice: allowPrerelease = current.IsPrerelease, so a user on v0.1.5-rc.1
// hears about v0.1.5-rc.2 and about v0.1.5; a user on v0.1.5 hears only about v0.1.6.
func SelectRelease(rels []Release, allowPrerelease bool) (Release, bool)
```

Input is `GET https://api.github.com/repos/openshift-online/openshellctl/releases?per_page=30` with `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `User-Agent: openshellctl/<version>`. `--version vX` uses `GET …/releases/tags/vX` instead (accepts `X` or `vX`, normalised to `vX`). If `GITHUB_TOKEN` is set in the environment it is sent as `Authorization: Bearer` **only** for `update`, never for the background notice (a notice must not spend or leak anything).

Why not `/releases/latest`: the currently published `v0.0.116-rc.1` is **not** flagged pre-release on GitHub (it shows as "Latest"; `v0.1.3-rc.1` is flagged) because `.goreleaser.yaml` has no `release.prerelease: auto`. `/latest` would therefore return an rc. Client-side semver filtering makes the tool correct regardless of flags; §4 fixes the flags too.

### 2.4 Asset naming

From `.goreleaser.yaml` and the published `v0.1.3-rc.1` assets:

- archive: `openshellctl_{Linux|Darwin}_{x86_64|arm64}.tar.gz` (`title .Os`, `amd64→x86_64`)
- checksums: `openshellctl_<version-without-v>_checksums.txt` (goreleaser default `{{ .ProjectName }}_{{ .Version }}_checksums.txt`), lines `<sha256-hex>  <filename>`

Pure functions: `AssetName(goos, goarch string) (string, error)` (error for unsupported pairs), `ChecksumAssetName(tag string) string`, `ParseChecksums(r io.Reader) (map[string]string, error)`, `FindAsset(rel Release, name string) (Asset, bool)`. Download URLs are taken from the release JSON (`browser_download_url`), not templated — avoids the osdctl template-drift failure mode, and makes the `--version` path identical to the latest path.

### 2.5 Same-version and downgrade

- target == current → `openshellctl is already at vX` , exit 0, nothing downloaded.
- `--version` older than current → allowed (that is what the flag is for — rollback), printed as `downgrading vY → vX`.
- default resolution never downgrades: if `SelectRelease` returns something ≤ current, print "already up to date".

### 2.6 Where things live

```
pkg/selfupdate/            # new; importable, no cobra/viper
  release.go               # Release/Asset types, SelectRelease, ParseCurrent, AssetName, ChecksumAssetName
  github.go                # Fetcher interface + GitHubFetcher (net/http, injected *http.Client, base URL)
  checksums.go             # ParseChecksums, VerifyFile(path, wantHex) error
  archive.go               # ExtractBinary(archivePath, dst string, maxSize int64) error
  replace.go               # ResolveExecutable(getExe, evalSymlinks), Replace(newFile, exePath) error
  notice.go                # Cache{CheckedAt, LatestTag}, ShouldCheck(...), NoticeText(...), cache load/save (fs seam)
  errors.go                # typed errors (ErrNoRelease, ErrAssetMissing, ErrChecksumMismatch, ErrChecksumEntryMissing, ErrUnsupportedPlatform, ErrManagedInstall, ErrReplaceDenied)
  *_test.go                # httptest.Server for GitHub, t.TempDir() for files
internal/cli/update.go     # cobra wiring only: flags → selfupdate.Options → one orchestration call
internal/cli/root.go       # --no-update-check persistent flag; start/collect the notice
internal/version/version.go# Main.Version fallback, InstallMethod
cmd/openshellctl/main.go   # installMethod ldflag symbol
```

Two-level rule: `update`'s `RunE` calls `selfupdate.Run(ctx, opts, deps)`; `Run` calls the pure functions and the thin I/O shells above. No new package globals — the fetcher, `http.Client`, `fs`, `getenv`, `now`, `goos/goarch`, `getExecutable` are all fields of a `Deps` struct with production defaults built in `internal/cli`.

## 3. Behaviour contracts

### 3.1 `openshellctl update`

```
Usage: openshellctl update [flags]

Flags:
      --version string   release tag to install (e.g. v0.1.5 or 0.1.5); default: latest stable release
      --prerelease       allow the default resolution to pick a pre-release
      --check            resolve and report only; do not download or replace
  -y, --yes              reserved; not needed today (update is already explicit) — do NOT add unless asked
```

(`--check` overlaps the notice but is useful in CI: `openshellctl update --check` exits 0 and prints `vCUR → vNEW available` or `up to date`. Decide in review whether a distinct exit code — e.g. 10 "update available" — is wanted; default is 0 either way, matching `gh`.)

Sequence in `selfupdate.Run`:

1. `installMethod != ""` → `ErrManagedInstall` with the per-method instruction (`container`: "rebuild the image with a newer OPENSHELLCTL_VERSION").
2. Resolve target: `--version` → `/releases/tags/<tag>`; else list → `SelectRelease`. `ErrNoRelease` if nothing qualifies (e.g. no stable release exists yet and no `--prerelease` — see §7 Q1).
3. Compare with `ParseCurrent(version.Version)` per §2.5. Print `openshellctl vCUR → vNEW` (stderr). Stop here on `--check`.
4. `AssetName(goos, goarch)`; `FindAsset` for both the archive and `ChecksumAssetName(tag)`; `ErrAssetMissing` names the exact expected filename.
5. Download checksums (≤ 64 KiB cap) → `ParseChecksums` → entry for the archive name, else `ErrChecksumEntryMissing`.
6. Download archive to `os.CreateTemp(dir(realExe), ".openshellctl-update-*")` streaming through `sha256`; cap at 256 MiB; compare hex → `ErrChecksumMismatch{Want, Got}` (temp removed). **Nothing is gunzipped before this point.**
7. (Phase 2, §5) verify the checksum file's signature before step 6 is trusted.
8. `ExtractBinary(tempArchive, tempBin, 256 MiB)`: walk tar, accept only `hdr.Typeflag == tar.TypeReg && hdr.Name == "openshellctl"` (also `./openshellctl`), `io.LimitReader`, write `0700` temp in the same dir, then `chmod` to the existing binary's mode.
9. `Replace(tempBin, realExe)` = `os.Rename`. `EACCES/EPERM` → `ErrReplaceDenied{Path}` with hint `re-run with sufficient permissions to write <path>, or reinstall`. No sudo escalation, ever.
10. Print `updated openshellctl vCUR → vNEW (<path>)`; refresh the notice cache so the next command doesn't re-announce.

Timeouts for `update`: API 15 s, each download 120 s, all honouring `ctx` (SIGINT cleans up temp files via `defer`).

Exit codes: all `selfupdate` typed errors → 1 (generic), `UsageError` → 2. Add one `errors.As` branch in `exitCodeFor` only if review wants a dedicated code; default is none (same reasoning as plan 0004 §6).

### 3.2 Update notice

Conditions, all pure (`ShouldCheck(in CheckInputs) bool`):

- not `--no-update-check` / `OPENSHELL_NO_UPDATE_CHECK`
- command is not `update`, `version`, `completion`, `__complete`, `help`
- stderr **and** stdout are TTYs (avoids polluting piped JSON/YAML and cron logs — the rosa-agent cron jobs are the main non-TTY consumer)
- not CI (`CI` env non-empty — the GitHub CLI's heuristic; keep it to that single variable)
- `ParseCurrent` is a real semver (never for `dev`)
- cache (`$XDG_CACHE_HOME/openshellctl/update-check.json`, else `$HOME/.cache/openshellctl/…`, written `0600`, dir `0700`) is absent, unreadable, or `CheckedAt` older than 24 h. Format: `{"checked_at": RFC3339, "latest_tag": "vX"}`. An unreadable/corrupt cache is treated as absent and overwritten — never an error.

Mechanics:

- `root.PersistentPreRunE` (already exists for `applyGatewayInsecureTransport`) starts a goroutine with its own `context.WithTimeout(ctx, 2*time.Second)` and a dedicated `http.Client{Timeout: 2s}`; result goes to a buffered channel of size 1 stored on the command context (same pattern as `cliDeps`).
- `Execute()` in `root.go`, after `root.Execute()` returns (success **or** failure — cobra skips `PersistentPostRun` on error, so the collection lives in `Execute`), does a non-blocking `select` with a tiny grace wait (≤ 250 ms) for the channel. Most commands take longer than the check, so the notice costs nothing; a fast `openshellctl version`-class command pays at most the grace wait. Anything not ready is dropped.
- Printing happens only after the command has fully returned, so it can never land inside a raw-mode `connect`/`exec -t` session.
- Notice text (stderr, one line, respects `--no-color`):
  `A newer openshellctl is available: v0.1.5-rc.1 → v0.1.5. Run "openshellctl update" to install it.`
  If the cache already holds `latest_tag` newer than current, the notice prints from the cache without a network call (gh does the same).

### 3.3 Checksum/security properties — honest statement

- SHA-256 over TLS from github.com protects against truncated/corrupted downloads and a swapped asset where the attacker could not also rewrite `checksums.txt`. It does **not** protect against a compromised GitHub account or a malicious release — the checksum file ships from the same place as the archive. That stronger property needs a signature (§5).
- Tar extraction never trusts `hdr.Name` as a path (only an exact-name match is extracted into a path we chose), never follows symlinks, caps size; gzip is only opened after the digest matched.
- No shell-out, including no `exec` of the new binary for a post-install `version` self-test (AGENTS.md "No shell-out, ever"). The digest check is the integrity gate.
- `GITHUB_TOKEN` is only ever sent to `api.github.com`/`github.com` hosts taken from the release JSON after host validation (`FindAsset` rejects a `browser_download_url` whose host is not `github.com` or `objects.githubusercontent.com`; redirects to `*.githubusercontent.com` are the normal CDN hop and do not carry the token because `net/http` strips `Authorization` on cross-host redirects anyway — the validation is belt-and-braces).

### 3.4 Managed-install hook

`cmd/openshellctl/main.go` gains `installMethod = ""` (ldflag `main.installMethod`), copied into `internal/version.InstallMethod` via `Set`. `build/Containerfile` passes `-X main.installMethod=container`. goreleaser builds leave it empty.

## 4. Release-side changes (same PR series, separate commits)

1. `.goreleaser.yaml`: add
   ```yaml
   release:
     github: {owner: openshift-online, name: openshellctl}
     prerelease: auto        # -rc.N tags become GitHub pre-releases
     draft: false
   checksum:
     name_template: "{{ .ProjectName }}_{{ .Version }}_checksums.txt"   # make the implicit default explicit; the client hard-codes this shape
     algorithm: sha256
   ```
2. Re-flag the existing `v0.0.116-rc.1` release as a pre-release in the GitHub UI (manual, one-time) so `/latest` stops pointing at it for other consumers (install scripts, humans).
3. README: new "Updating" section; add `update` to "Differences from the upstream Rust CLI"; keep `go install` as an alternative.

## 5. Phase 2 — signed checksums (recommended, separate PR)

Goal: `update` refuses an archive whose `checksums.txt` is not signed by the project.

Option A (recommended now): **cosign key pair** (`cosign generate-key-pair`, ECDSA P-256). goreleaser:
```yaml
signs:
  - cmd: cosign
    artifacts: checksum
    signature: "${artifact}.sig"
    args: ["sign-blob", "--key=env://COSIGN_PRIVATE_KEY", "--output-signature=${signature}", "--yes", "${artifact}"]
```
Verification in Go is **stdlib only**: embed the PEM public key, `ecdsa.VerifyASN1(pub, sha256(checksumsBytes), base64decode(sigFile))`. No sigstore dependency. Cost: one private key to hold (today: the releaser's machine/password manager; later: the Konflux/CI secret store) and a documented rotation procedure (new pubkey ships in a release signed by the old key).

Option B (later): **cosign keyless** (Fulcio cert + Rekor). Only sensible once releases are cut by a workload with a stable OIDC identity (GitHub Actions or Konflux); verification in-process needs `sigstore-go`, a large dependency, and identity pinning (`--certificate-identity-regexp`, `--certificate-oidc-issuer`) that would currently be a personal email. Revisit when the Konflux release path (internal `konflux-release-data` MR) is live.

Until Phase 2 lands, `update` prints one line after a successful install: `note: release signatures are not yet verified (checksum only)`. Remove the line in Phase 2.

## 6. Parity / docs bookkeeping

- `hack/parity/openshellctl_extensions.json`: add a `"commands"` (or equivalent) note for `update` and the `--no-update-check` persistent flag so the scheduled parity evaluation doesn't re-report them. Check `parity_flags_test.go` loader tolerates the new key before adding it (it currently only reads `subcommands`).
- `internal/cli/docs_test.go` extracts every `openshellctl …` line from fenced blocks in docs and validates it against the command tree — the README "Updating" section's examples must parse (`openshellctl update`, `openshellctl update --version v0.1.5`, `openshellctl update --check`).
- `the13Subcommands` in `parity_test.go` is unaffected (`update` is top-level and not a parity target).

## 7. Open questions for the requester

1. **No stable release exists yet.** Every published tag is `-rc.N`, and the release policy (plan 0004 / team decision) says no non-rc release until 1:1 upstream parity. With §2.3 semantics an rc user still gets rc→rc notices and `update` works for them; a `dev` user running plain `update` gets `ErrNoRelease` ("no stable release published; use --prerelease or --version"). Confirm that is the wanted behaviour rather than silently defaulting to the newest rc while no stable exists.
2. **Notice gating on TTY.** The strict gh rule (stdout *and* stderr TTY) means `openshellctl sandbox list -o json | jq` never shows the notice. Alternative: stderr-TTY only. Default in this plan: both, to be safe for the cron jobs.
3. **Distinct exit code for `update --check` when an update is available** (useful for scripts) — yes/no.
4. **Phase 2 timing/key custody** (§5): go ahead with a key pair now, or wait for CI-driven releases and do keyless.

## 8. Commit series (TDD, each ≤ ~200 lines incl. tests, in this order)

| # | Commit | Tests first |
|---|---|---|
| 1 | `feat(version): fall back to module version from build info; add InstallMethod` | `TestGet_FallsBackToMainVersion`, `TestGet_KeepsDevWhenMainIsDevel`, `TestSet_InstallMethod` |
| 2 | `feat(selfupdate): release model, ParseCurrent, SelectRelease` | table tests: stable-only, prerelease-allowed, drafts skipped, mis-flagged rc filtered by semver, empty list, ordering by semver not `published_at` |
| 3 | `feat(selfupdate): asset and checksum naming, ParseChecksums` | linux/darwin × amd64/arm64 names, unsupported → `ErrUnsupportedPlatform`, tag with/without `v`, checksum parsing (two-space and ` *` binary-mode separators, blank/CRLF lines, malformed hex rejected) |
| 4 | `feat(selfupdate): GitHub fetcher` | `httptest.Server`: list, by-tag, 404 → `ErrNoRelease`, 403 rate-limit → typed error, non-JSON, header assertions (Accept, UA, no Authorization unless token given), context cancellation |
| 5 | `feat(selfupdate): download with streaming sha256 and size cap` | matching digest, mismatch → `ErrChecksumMismatch` and temp removed, over-cap aborts, 5xx |
| 6 | `feat(selfupdate): ExtractBinary` | archive with LICENSE+README+binary extracts only binary; `./openshellctl` prefix; symlink entry named `openshellctl` rejected; dir entry rejected; missing entry → typed error; size cap; mode copied |
| 7 | `feat(selfupdate): ResolveExecutable and Replace` | symlink `~/bin/openshellctl → /opt/x/openshellctl` resolves to target; rename over existing; `EACCES` → `ErrReplaceDenied` with path; temp cleaned on failure (all in `t.TempDir()`) |
| 8 | `feat(selfupdate): Run orchestration` | fake `Fetcher`+`MapFS`-backed deps: happy path, `--check` stops before download, same-version no-op, downgrade via `--version`, managed install refusal, `dev` current with/without `--version` |
| 9 | `feat(cli): openshellctl update command` | `runCmd` usage tests (`--version` normalisation, unknown flag → exit 2), end-to-end through `root.Execute()` with injected deps, help text snapshot, `docs_test` passes with README examples |
| 10 | `feat(selfupdate): notice cache and ShouldCheck` | 24 h window boundary, corrupt cache treated as absent, `0600`/`0700` perms, TTY/CI/env/command exclusions table |
| 11 | `feat(cli): background update notice in root Execute` | notice printed after command output (ordering test with a slow fake fetcher: notice dropped when over budget; printed when ready), not printed for `update`/`version`, respects `--no-update-check` and `OPENSHELL_NO_UPDATE_CHECK`, never printed on `dev` |
| 12 | `build: goreleaser prerelease auto, explicit checksum template; Containerfile installMethod=container` | `make verify` + a `hack/` check that `.goreleaser.yaml` checksum template matches `selfupdate.ChecksumAssetName` (string equality on a rendered sample) |
| 13 | `docs: README Updating section, divergence register entry, this plan → status accepted` | `docs_test`, parity tests |

Phase 2 (§5) is its own series: `feat(selfupdate): verify checksum signature (ECDSA P-256, embedded key)` + goreleaser `signs` + key-rotation doc.

## 9. Pre-PR validation fan-out (per AGENTS.md)

security (tar handling, temp-file perms, token scoping, no InsecureSkipVerify path reachable from `update` — note `--gateway-insecure` mutates `http.DefaultTransport`; the updater must build its own `http.Client` from a **fresh** `http.Transport` clone taken *before* `applyGatewayInsecureTransport` runs, or simply refuse to run `update` when `--gateway-insecure` is set), lint (`gofumpt`, `gosec` annotations only where justified), idiomatic (typed errors with `Is/Unwrap`, `%w`), intent (nothing beyond this plan; no `--yes`, no auto-update), tests (hermetic: no `api.github.com`, no `$HOME`).

## Sources

- osdctl: `cmd/upgrade.go`, `pkg/utils/version.go`, `cmd/cmd.go` (`versionCheck`, `getSkipVersionCommands`), `.goreleaser.yml` (`checksum.name_template: sha256sum.txt`) — github.com/openshift/osdctl `main`
- srepd: `pkg/tui/update.go` (`checkForUpdate`, `isNewerVersion`, `selfUpdate`, `RunSelfUpdate`), `cmd/update.go`, `.goreleaser.yaml` — github.com/openshift-online/srepd `main`
- GitHub CLI: `internal/update/update.go` (24 h window, `GH_NO_UPDATE_NOTIFIER`, TTY+CI gating, `hashicorp/go-version`) — github.com/cli/cli `trunk`
- openshellctl releases page and `expanded_assets/v0.1.3-rc.1` (asset names; `v0.0.116-rc.1` shown as "Latest", `v0.1.3-rc.1` flagged pre-release)
- openshellctl PR #58 tree: `.goreleaser.yaml`, `Makefile`, `cmd/openshellctl/main.go`, `internal/version/version.go`, `internal/cli/{root,version,deps,exitcode,parity_test,docs_test}.go`, `pkg/gatewayconfig/paths.go`, `hack/parity/openshellctl_extensions.json`, `build/Containerfile`, `AGENTS.md`
- NVIDIA/OpenShell `crates/openshell-cli/src/main.rs` (no self-update command upstream), `install.sh` (`openshell-checksums-sha256.txt` verification pattern)
- `creativeprojects/go-selfupdate` on pkg.go.dev (v1.7.0; `ChecksumValidator`, `ECDSAValidator`)
- GitHub REST API: `GET /repos/{owner}/{repo}/releases`, `/releases/tags/{tag}`, `X-GitHub-Api-Version: 2022-11-28`
