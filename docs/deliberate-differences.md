# Deliberate differences from upstream

This document records places where openshellctl's behaviour intentionally
diverges from the upstream Rust CLI (`openshell`) or from `ssh`. Each entry
states the difference, the reason, and where in the code it is implemented.

## Terminal mode reset after interactive attach

**Difference**: When `sandbox create` or `sandbox connect` ends an interactive
session (exit or Ctrl-P Ctrl-Q detach), openshellctl writes ANSI/VT100 escape
sequences to the local terminal before returning. The Rust CLI and `ssh` do
not send these sequences.

**Sequences sent** (each idempotent on VTE/xterm-family terminals):

| Sequence | Effect |
|---|---|
| `\033[?1000l` | Disable X10 mouse button events |
| `\033[?1002l` | Disable button-event mouse tracking |
| `\033[?1003l` | Disable any-event mouse tracking |
| `\033[?1006l` | Disable SGR extended mouse encoding |
| `\033[?1004l` | Disable focus-in/focus-out reporting |
| `\033[?2004l` | Disable bracketed-paste mode |
| `\033[?1049l` | Leave alternate screen, restore saved cursor position |
| `\033[?25h`   | Show cursor |

**Rationale**: A remote program that enables mouse reporting, alternate screen,
or bracketed paste does not always clean up after itself — particularly when
the session ends due to a transport error or a signal. Without the reset, the
local terminal is left in a garbled state (invisible cursor, mouse clicks
producing escape characters in the next command, etc.). The sequences are
idempotent, so sending them after a clean exit is harmless.

**Caveat**: `\033[?1049l` exits the alternate screen unconditionally. When
run from inside a **tmux pane** or a **terminal multiplexer**, the sequences
are interpreted by the pane's own emulator before reaching the outer terminal,
so the outer program's screen is unaffected. When run from inside a
**terminal-emulator window** that itself displays a full-screen TUI (e.g.
neovim spawned directly in a terminal, not in a tmux pane), `?1049l` will
switch that TUI back to its normal buffer when the attach ends. To avoid this,
use `--detach` to skip the attach entirely.

**Implementation**: `terminalResetSequence()` and `restoreWith()` in
`internal/cli/sandbox_connect.go`; called from `cliTerminal.MakeRaw`'s
restore closure only when the local endpoint is a TTY.
