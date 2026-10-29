package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"

	"github.com/openshift-online/openshellctl/pkg/api/v1alpha1"
	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/policyyaml"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
	"github.com/openshift-online/openshellctl/pkg/transfer"
)

func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }
func bytesReader(b []byte) io.Reader      { return bytes.NewReader(b) }

func newSandboxCreateCommand() *cobra.Command {
	var (
		name             string
		from             string
		file             string
		cpu              string
		memory           string
		providers        []string
		labels           []string
		envs             []string
		approvalMode     string
		format           string
		editor           string
		noKeep           bool
		keep             bool
		noCredWarn       bool
		uploads          []string
		noGitIgnore      bool
		forward          string
		detach           bool
		driverConfigJSON string
		policyFile       string
		replace          bool
		replaceTimeout   time.Duration
		params           []string
		paramFiles       []string
		printParameters  bool
		ignoreUnknown    bool
	)
	ttyState := &ttyTriState{}
	autoProvState := &ttyTriState{}
	gpuFlag := &gpuRequestFlag{}

	c := &cobra.Command{
		Use:   "create [-- COMMAND...]",
		Short: "Create a sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			if editor != "" {
				return &UsageError{Err: fmt.Errorf("--editor is not supported by openshellctl")}
			}

			flags, err := buildCreateFlags(cmd, createFlagInput{
				name: name, from: from, cpu: cpu, memory: memory,
				providers: providers, labels: labels, env: envs,
				approvalMode: approvalMode, format: format,
				noKeep: noKeep, keep: keep, noCredWarn: noCredWarn,
				command: commandArgs(cmd, args),
				uploads: uploads, noGitIgnore: noGitIgnore,
				forward: forward, detach: detach,
				gpu: gpuFlag, tty: ttyState, autoProviders: autoProvState,
				driverConfigJSON: driverConfigJSON, policyFile: policyFile,
				replace: replace, replaceTimeout: replaceTimeout,
			})
			if err != nil {
				return err
			}

			var manifest *v1alpha1.Sandbox
			if file != "" {
				m, printedOnly, lerr := loadManifestWithParams(cmd, osFS{}, file, templateFlags{
					params:                  params,
					paramFiles:              paramFiles,
					printParameters:         printParameters,
					ignoreUnknownParameters: ignoreUnknown,
				})
				if lerr != nil {
					return lerr
				}
				if printedOnly {
					return nil
				}
				manifest = m
			} else if printParameters || len(params) > 0 || len(paramFiles) > 0 || ignoreUnknown {
				return &UsageError{Err: fmt.Errorf(
					"--param/--param-file/--parameters/--ignore-unknown-parameters require -f/--file")}
			}

			// flagExplicit is always true when flagPolicy != nil in production; the parameter exists for the test contract.
			pol, perr := resolveCreatePolicy(flags.Policy, policyFile != "", manifest, file, os.Getenv)
			if perr != nil {
				return perr
			}
			flags.Policy = pol

			req, err := sandbox.MergeManifestAndFlags(manifest, flags)
			if err != nil {
				return err
			}
			if err := validateCreateRequest(req); err != nil {
				return err
			}

			return withGatewayTarget(cmd, func(gw gateway.Gateway, target *gatewayconfig.Target) error {
				ws := workspace()

				// Determine TTY state once; used both to set the proto field and
				// to decide whether to attach after Create.
				stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
				stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))

				// Tell the gateway to hold the main process until the SSH subsystem
				// attach arrives — but only when we will actually attach. We attach
				// when: output is table, not detaching, and either !Keep (always
				// attaches regardless of TTY) or both stdin and stdout are TTYs.
				willAttach := shouldAttachAfterCreate(req, stdinTTY, stdoutTTY)
				if willAttach {
					req.AwaitMainProcessAttachment = true
				}
				// Mark the sandbox ephemeral so the gateway cleans it up when the
				// session ends; the explicit DeleteSandbox call below is skipped.
				if !req.Keep {
					req.RetentionEphemeral = true
				}

				deps := sandbox.CreateDeps{
					GW:          gw,
					Env:         os.Getenv,
					Stderr:      cmd.ErrOrStderr(),
					Sink:        newPlainSink(cmd.ErrOrStderr()),
					Watch:       req.Output == "table",
					IdleTimeout: provisionTimeout(),
				}
				res, err := sandbox.Create(cmd.Context(), deps, req, resolveTTY(req))
				if err != nil {
					return err
				}
				sb := res.Sandbox

				// (5) Save last_sandbox when the sandbox will persist.
				if req.Keep || req.Forward != nil {
					saveLastSandbox(target, ws, sb.Name)
				}

				// (6) Approval mode update (non-manual).
				if req.ApprovalMode != "" && req.ApprovalMode != "manual" {
					if cerr := setApprovalMode(cmd.Context(), gw, ws, sb.Name, req.ApprovalMode); cerr != nil {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to set approval mode: %v\n", cerr)
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "You can set it manually with: openshellctl sandbox config update --approval-mode %s %s\n", req.ApprovalMode, sb.Name)
					}
				}

				// (8) Uploads.
				if len(req.Uploads) > 0 {
					if err := runCreateUploads(cmd.Context(), gw, ws, sb.Name, req.Uploads, req, cmd.ErrOrStderr()); err != nil {
						// If we set AwaitMainProcessAttachment, the gateway is holding the
						// main process open waiting for an attach that will never come.
						// Best-effort delete so the gateway releases it.
						deleteIfAwaiting(gw, ws, sb.Name, req, cmd.ErrOrStderr())
						return err
					}
				}

				// (9) Forward.
				if req.Forward != nil {
					forwardCtx, forwardCancel := context.WithCancel(cmd.Context())
					defer forwardCancel()
					if err := startForward(forwardCtx, gw, ws, sb.Name, req.Forward, cmd.ErrOrStderr()); err != nil {
						deleteIfAwaiting(gw, ws, sb.Name, req, cmd.ErrOrStderr())
						return err
					}
				}

				// (10) Output/attach policy.
				if req.Output != "table" {
					return renderSandbox(cmd, sb, req.Output)
				}

				if !willAttach {
					return nil
				}

				// Attach via connect.
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Attaching to sandbox %s...\n", sb.Name)
				tc := transfer.New(gw, nil, wallClock{})
				if d, ok := depsFrom(cmd.Context()); ok && d.Transfer != nil {
					tc = d.Transfer
				}
				useTTY := stdinTTY && stdoutTTY
				t := &cliTerminal{
					stdinFd:  int(os.Stdin.Fd()),
					stdoutFd: int(os.Stdout.Fd()),
					stdin:    os.Stdin,
					stdout:   cmd.OutOrStdout(),
					stderr:   cmd.ErrOrStderr(),
					isTTY:    stdinTTY,
				}
				code, cerr := tc.ConnectMainProcess(cmd.Context(), ws, sb.Name, useTTY, t)

				if !req.Keep {
					// RetentionEphemeral: the gateway cleans up the sandbox when the
					// main process exits normally (exit-status delivered). Skip the
					// explicit delete to avoid a spurious NotFound error.
					//
					// Exception: any non-nil error from ConnectMainProcess means the
					// gateway did NOT fire ephemeral cleanup:
					//   - *transfer.AttachClosedError — SSH channel closed without
					//     exit-status; per the supervisor contract (ssh.rs:1391-1427),
					//     every supervisor-initiated close sends exit-status, so its
					//     absence means the transport died while the main process may
					//     still be running. Gateway-side cleanup does not fire.
					//   - Other errors — SSH dial failure, context cancel, etc.; the
					//     main process may never have started.
					isTransportErr := cerr != nil
					if !req.RetentionEphemeral || isTransportErr {
						if _, derr := gw.DeleteSandbox(context.Background(), ws, sb.Name); derr != nil {
							_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Failed to delete sandbox %s: %v\n", sb.Name, derr)
						} else if !req.RetentionEphemeral {
							_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Deleted sandbox %s\n", sb.Name)
						}
					}
				} else {
					saveLastSandbox(target, ws, sb.Name)
				}

				if cerr != nil {
					return cerr
				}
				return exitCodeError(code)
			})
		},
	}
	f := c.Flags()
	f.StringVar(&name, "name", "", "sandbox name")
	f.StringVar(&from, "from", "", "image reference or community name")
	f.StringVarP(&file, "file", "f", "", "manifest file (- for stdin)")
	f.StringVar(&cpu, "cpu", "", "CPU request")
	f.StringVar(&memory, "memory", "", "memory request")
	f.StringSliceVar(&providers, "provider", nil, "provider to attach (repeatable)")
	f.StringSliceVar(&labels, "label", nil, "label key=value (repeatable)")
	f.StringSliceVar(&envs, "env", nil, "env KEY=VALUE (repeatable)")
	f.StringVar(&approvalMode, "approval-mode", "manual", "proposal approval mode: manual|auto")
	f.StringVarP(&format, "output", "o", "", "output format: table|json|yaml")
	f.StringVar(&editor, "editor", "", "(unsupported)")
	f.BoolVar(&noKeep, "no-keep", false, "delete the sandbox after the session")
	f.BoolVar(&keep, "keep", true, "keep the sandbox after the session")
	_ = f.MarkHidden("keep")
	_ = f.MarkDeprecated("keep", "sandboxes are kept by default; use --no-keep to delete")
	f.BoolVar(&noCredWarn, "no-credential-warnings", false, "suppress credential warnings")
	f.StringSliceVar(&uploads, "upload", nil, "upload local[:dest] (repeatable)")
	f.BoolVar(&noGitIgnore, "no-git-ignore", false, "disable .gitignore filtering for uploads")
	f.StringVar(&forward, "forward", "", "forward [bind_address:]port to sandbox")
	f.BoolVar(&detach, "detach", false, "detach after create (do not attach)")
	f.StringVar(&driverConfigJSON, "driver-config-json", "", "driver config as JSON")
	f.StringVar(&policyFile, "policy", "", "sandbox policy file (env OPENSHELL_SANDBOX_POLICY)")
	f.Var(gpuFlag, "gpu", "request GPU resources (optional count)")
	f.Lookup("gpu").NoOptDefVal = "bare"
	f.Var(ttyStateFlag{ttyState, true}, "tty", "force a pseudo-terminal")
	f.Var(ttyStateFlag{ttyState, false}, "no-tty", "disable pseudo-terminal allocation")
	f.Lookup("tty").NoOptDefVal = "true"
	f.Lookup("no-tty").NoOptDefVal = "true"
	f.Var(ttyStateFlag{autoProvState, true}, "auto-providers", "enable auto provider detection")
	f.Var(ttyStateFlag{autoProvState, false}, "no-auto-providers", "disable auto provider detection")
	f.Lookup("auto-providers").NoOptDefVal = "true"
	f.Lookup("no-auto-providers").NoOptDefVal = "true"
	f.BoolVar(&replace, "replace", false, "delete and wait for an existing sandbox with the same name before creating")
	f.DurationVar(&replaceTimeout, "replace-timeout", 0, "timeout waiting for the existing sandbox to be deleted (default 5m)")
	f.StringArrayVarP(&params, "param", "p", nil, "template parameter KEY=VALUE, substituted for ${KEY} in the -f manifest (repeatable)")
	f.StringArrayVar(&paramFiles, "param-file", nil, "file of KEY=VALUE template parameters, one per line, '#' comments allowed (repeatable)")
	f.BoolVar(&printParameters, "parameters", false, "print the -f manifest's ${KEY} parameter names, one per line, and exit without creating anything")
	f.BoolVar(&ignoreUnknown, "ignore-unknown-parameters", false, "do not error when --param/--param-file supplies a key the manifest does not reference")
	return c
}

type createFlagInput struct {
	name, from, cpu, memory  string
	providers, labels, env   []string
	approvalMode, format     string
	noKeep, keep, noCredWarn bool
	command                  []string
	uploads                  []string
	noGitIgnore              bool
	forward                  string
	detach                   bool
	gpu                      *gpuRequestFlag
	tty                      *ttyTriState
	autoProviders            *ttyTriState
	driverConfigJSON         string
	policyFile               string
	replace                  bool
	replaceTimeout           time.Duration
}

func buildCreateFlags(cmd *cobra.Command, in createFlagInput) (sandbox.CreateFlags, error) {
	f := sandbox.CreateFlags{
		Name:                 in.name,
		CPU:                  in.cpu,
		Memory:               in.memory,
		Providers:            in.providers,
		Output:               in.format,
		Command:              in.command,
		NoCredentialWarnings: in.noCredWarn,
		Workspace:            workspace(),
		Detach:               in.detach,
		Replace:              in.replace,
		ReplaceTimeout:       in.replaceTimeout,
	}
	// Only propagate --approval-mode when the user explicitly passed it;
	// the cobra default "manual" must not silently overwrite a manifest's
	// approval-mode via MergeManifestAndFlags's "non-empty flag wins" rule.
	if cmd.Flags().Changed("approval-mode") {
		f.ApprovalMode = in.approvalMode
	}
	if in.tty != nil {
		f.TTY = in.tty.Value()
	}
	if in.autoProviders != nil {
		f.AutoProviders = in.autoProviders.Value()
	}
	if in.noKeep {
		no := false
		f.Keep = &no
	}
	if in.gpu != nil && in.gpu.set {
		f.GPU = in.gpu.gpu
	}
	if len(in.labels) > 0 {
		m, err := sandbox.ParseLabels(in.labels)
		if err != nil {
			return f, &UsageError{Err: err}
		}
		f.Labels = m
	}
	if len(in.env) > 0 {
		m, err := sandbox.ParseEnvPairs(in.env)
		if err != nil {
			return f, &UsageError{Err: err}
		}
		f.Env = m
	}
	if in.from != "" {
		img, err := sandbox.ResolveImage(in.from, "", os.Stat)
		if err != nil {
			return f, &UsageError{Err: err}
		}
		f.Image = img
	}
	if in.forward != "" {
		spec, err := sandbox.ParseForwardSpec(in.forward)
		if err != nil {
			return f, &UsageError{Err: err}
		}
		f.Forward = &spec
	}
	if len(in.uploads) > 0 {
		for _, u := range in.uploads {
			local, dest := splitColonSpec(u)
			gitignore := !in.noGitIgnore
			f.Uploads = append(f.Uploads, v1alpha1.Upload{
				Local:     local,
				Dest:      dest,
				GitIgnore: &gitignore,
			})
		}
	}
	if in.driverConfigJSON != "" {
		var dc map[string]any
		if err := json.Unmarshal([]byte(in.driverConfigJSON), &dc); err != nil {
			return f, &UsageError{Err: fmt.Errorf("invalid --driver-config-json: %w", err)}
		}
		f.DriverConfig = dc
	}
	if in.policyFile != "" {
		pol, err := loadPolicy(in.policyFile)
		if err != nil {
			return f, err
		}
		f.Policy = pol
	}
	return f, nil
}

// gpuRequestFlag is a pflag.Value for --gpu [COUNT]. Bare --gpu (no value) sets
// gpu to an empty GPU (driver default); --gpu N sets Count.
type gpuRequestFlag struct {
	set bool
	gpu *v1alpha1.GPU
}

func (g *gpuRequestFlag) String() string {
	if g.gpu == nil {
		return ""
	}
	if g.gpu.Count == nil {
		return "bare"
	}
	return strconv.FormatUint(uint64(*g.gpu.Count), 10)
}

func (g *gpuRequestFlag) Type() string { return "gpuRequest" }

func (g *gpuRequestFlag) Set(s string) error {
	g.set = true
	if s == "bare" || s == "" {
		g.gpu = &v1alpha1.GPU{}
		return nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return fmt.Errorf("--gpu count must be a positive integer, got %q", s)
	}
	count := uint32(n)
	g.gpu = &v1alpha1.GPU{Count: &count}
	return nil
}

// commandArgs returns the trailing COMMAND after "--".
func commandArgs(cmd *cobra.Command, args []string) []string {
	if cmd.ArgsLenAtDash() >= 0 {
		return args[cmd.ArgsLenAtDash():]
	}
	return nil
}

// validateCreateRequest runs the authoritative cpu/memory validators at merge.
func validateCreateRequest(r *sandbox.CreateRequest) error {
	if r.CPU != "" {
		if err := sandbox.ValidateCPU(r.CPU); err != nil {
			return &UsageError{Err: err}
		}
	}
	if r.Memory != "" {
		if err := sandbox.ValidateMemory(r.Memory); err != nil {
			return &UsageError{Err: err}
		}
	}
	if r.Replace && r.Name == "" {
		return &UsageError{Err: fmt.Errorf(
			"--replace requires a name — pass --name, a manifest with metadata.name, or --from <image> --name <name>")}
	}
	return nil
}

// resolveTTY resolves the effective TTY value. Explicit --tty/--no-tty wins;
// when unset, default to true if both stdin and stdout are terminals (matching
// upstream openshell behavior — run.rs allocates a PTY when the local terminal
// is interactive).
func resolveTTY(r *sandbox.CreateRequest) bool {
	if r.TTY != nil {
		return *r.TTY
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// loadManifest decodes a -f manifest (- = stdin) and validates it.
//
// Deliberately NOT routed through loadManifestWithParams/the paramtemplate
// pipeline: this function is the shared entry point for every -f consumer
// other than `sandbox create` itself (doctor.go, namesFromManifest in
// sandbox_lifecycle.go — used by get/connect/upload/download/ssh-config/
// logs/stop/start/delete/provider), and ROSAENG-74244's "Out of scope" list
// is explicit: "Any command other than sandbox create." Templating must
// never leak into those commands — a manifest with incidental,
// non-template "${...}"-shaped text (e.g. a shell command embedding
// "${HOME}") that decodes fine today via `doctor -f`/`sandbox delete -f`
// must keep decoding fine after this change. Only sandbox create's own RunE
// calls loadManifestWithParams, with the real --param/--param-file/
// --parameters/--ignore-unknown-parameters flags.
func loadManifest(cmd *cobra.Command, path string) (*v1alpha1.Sandbox, error) {
	data, err := readManifestBytes(cmd, newManifestHTTPClient(), path)
	if err != nil {
		return nil, err
	}
	return decodeManifestBytes(data)
}

// loadPolicy reads a policy YAML file and parses it to the SDK type.
func loadPolicy(path string) (*types.SandboxPolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &UsageError{Err: fmt.Errorf("failed to read policy file %q: %w", path, err)}
	}
	p, err := policyyaml.Parse(data)
	if err != nil {
		return nil, &UsageError{Err: fmt.Errorf("invalid policy file %q: %w", path, err)}
	}
	return p, nil
}

// resolveManifestPolicy converts a manifest's spec.policy (inline) or
// spec.policyFile (path) to the SDK type. Returns nil when neither is set.
// The policyFile path is resolved relative to the manifest file's directory.
// When the manifest is read from stdin OR from a URL, policyFile must be an
// absolute path: there is no local directory to resolve a relative path
// against (filepath.Join(filepath.Dir(manifestPath), path) against a URL
// would silently produce a garbage local path, not an error), so a URL
// manifest gets the same treatment stdin already does.
func resolveManifestPolicy(m *v1alpha1.Sandbox, manifestPath string) (*types.SandboxPolicy, error) {
	if m.Spec.Policy != nil {
		p, err := policyyaml.ParseInline(m.Spec.Policy)
		if err != nil {
			return nil, &UsageError{Err: fmt.Errorf("spec.policy: %w", err)}
		}
		return p, nil
	}
	if m.Spec.PolicyFile != "" {
		path := m.Spec.PolicyFile
		if !filepath.IsAbs(path) {
			switch classifyManifestSource(manifestPath) {
			case sourceStdin:
				return nil, &UsageError{Err: fmt.Errorf("spec.policyFile %q must be an absolute path when the manifest is read from stdin", path)}
			case sourceURL:
				return nil, &UsageError{Err: fmt.Errorf("spec.policyFile %q must be an absolute path when the manifest is read from a URL", path)}
			}
			if manifestPath != "" {
				path = filepath.Join(filepath.Dir(manifestPath), path)
			}
		}
		return loadPolicy(path)
	}
	return nil, nil
}

// resolveCreatePolicy merges the policy from the --policy flag (or env var
// fallback) with the manifest's policy fields. Precedence:
//   - explicit --policy flag + manifest policy → error (mutually exclusive)
//   - env var OPENSHELL_SANDBOX_POLICY + manifest policy → manifest wins
//   - manifest policy alone → manifest
//   - flag or env alone → flag/env
//   - none → nil
func resolveCreatePolicy(
	flagPolicy *types.SandboxPolicy,
	flagExplicit bool,
	manifest *v1alpha1.Sandbox,
	manifestPath string,
	env func(string) string,
) (*types.SandboxPolicy, error) {
	var manifestPolicy *types.SandboxPolicy
	if manifest != nil {
		p, err := resolveManifestPolicy(manifest, manifestPath)
		if err != nil {
			return nil, err
		}
		manifestPolicy = p
	}

	if manifestPolicy != nil && flagPolicy != nil && flagExplicit {
		return nil, &UsageError{Err: fmt.Errorf("--policy flag and manifest policy (spec.policy/spec.policyFile) are mutually exclusive")}
	}

	if manifestPolicy != nil {
		return manifestPolicy, nil
	}

	if flagPolicy != nil {
		return flagPolicy, nil
	}

	envPath := ""
	if env != nil {
		envPath = env("OPENSHELL_SANDBOX_POLICY")
	}
	if envPath != "" {
		return loadPolicy(envPath)
	}

	return nil, nil
}

// setApprovalMode calls UpdateConfig to set proposal_approval_mode. Failure is
// a warning (not fatal), per the spec (§5.5 step 6).
func setApprovalMode(ctx context.Context, gw gateway.Gateway, ws, name, mode string) error {
	_, err := gw.UpdateConfig(ctx, ws, &types.ConfigUpdate{
		Name:       name,
		SettingKey: "proposal_approval_mode",
		SettingValue: &types.SettingValue{
			Type:      types.SettingValueString,
			StringVal: mode,
		},
	})
	return err
}

// runCreateUploads runs uploads for a create command (step 8).
func runCreateUploads(ctx context.Context, gw gateway.Gateway, ws, sbName string, ups []v1alpha1.Upload, req *sandbox.CreateRequest, stderr io.Writer) error {
	for i, u := range ups {
		abs, err := filepath.Abs(u.Local)
		if err != nil {
			return fmt.Errorf("failed to resolve upload path %q: %w", u.Local, err)
		}
		fsys := transfer.OSFS("/")
		fsPath := strings.TrimPrefix(abs, "/")

		gitignore := true
		if u.GitIgnore != nil {
			gitignore = *u.GitIgnore
		}

		tc := transfer.New(gw, fsys, wallClock{})
		prefix := ""
		if len(ups) > 1 {
			prefix = fmt.Sprintf("[%d/%d] ", i+1, len(ups))
		}
		if err := tc.Upload(ctx, ws, sbName, fsPath, u.Dest, gitignore, func(msg string) {
			_, _ = fmt.Fprintf(stderr, "  • %s%s\n", prefix, msg)
		}); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(stderr, "  ✓ Files uploaded")
	return nil
}

// deleteIfAwaiting does a best-effort sandbox delete when
// req.AwaitMainProcessAttachment is set. It is called on error paths between
// sandbox creation and the ConnectMainProcess call: without a delete the
// gateway holds the main process open indefinitely waiting for an attach that
// will never arrive. Uses context.Background() so a cancelled cmd.Context()
// does not prevent cleanup.
func deleteIfAwaiting(gw gateway.Gateway, ws, name string, req *sandbox.CreateRequest, stderr io.Writer) {
	if !req.AwaitMainProcessAttachment {
		return
	}
	if _, err := gw.DeleteSandbox(context.Background(), ws, name); err != nil {
		_, _ = fmt.Fprintf(stderr, "Warning: failed to clean up sandbox %s after aborted attach: %v\n", name, err)
	}
}

// shouldAttachAfterCreate reports whether the CLI will attach to the sandbox's
// main process via ConnectMainProcess immediately after creation. The same
// predicate controls whether AwaitMainProcessAttachment is set on the create
// request; both must agree or the gateway may hold the process indefinitely.
func shouldAttachAfterCreate(req *sandbox.CreateRequest, stdinTTY, stdoutTTY bool) bool {
	return !req.Detach && req.Output == "table" && (!req.Keep || (stdinTTY && stdoutTTY))
}

// startForward starts port forwarding in the background (step 9).
func startForward(ctx context.Context, gw gateway.Gateway, ws, sbName string, spec *sandbox.ForwardSpec, stderr io.Writer) error {
	_, err := gw.TCPListen(ctx, ws, sbName, uint32(spec.Port), uint32(spec.Port), spec.Bind)
	if err != nil {
		return fmt.Errorf("failed to start forward: %w", err)
	}
	_, _ = fmt.Fprintf(stderr, "  ✓ Forwarding port %d to sandbox %s in the background\n", spec.Port, sbName)
	_, _ = fmt.Fprintln(stderr)
	_, _ = fmt.Fprintf(stderr, "  Access at: %s\n", spec.AccessURL())
	_, _ = fmt.Fprintf(stderr, "  Stop with: openshell forward stop %d %s\n", spec.Port, sbName)
	return nil
}
