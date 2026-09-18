// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
)

// toolInstallEngine builds the engine behind `olivares agent tool`. It is a
// variable so tests can substitute an engine that trusts a fixture signing key
// and a fixture release server. Production wiring trusts the embedded Claude
// release key only; no flag or environment variable reaches this seam.
var toolInstallEngine = func(ctx context.Context) *toolinstall.Engine {
	var verifier toolinstall.SignatureVerifier
	if v, err := toolinstall.NewGPGVerifier(ctx, exec.LookPath); err != nil {
		verifier = toolinstall.UnavailableVerifier{Err: err}
	} else {
		verifier = v
	}
	claude := toolinstall.NewClaude(toolinstall.ClaudeOptions{Verifier: verifier})
	codex := toolinstall.NewCodex(toolinstall.CodexOptions{})
	grok := toolinstall.NewGrok(toolinstall.GrokOptions{})
	cat, err := toolinstall.NewCapabilityCatalog(toolinstall.NewCatalog(claude), codex, grok)
	if err != nil {
		return toolinstall.NewEngine(toolinstall.NewCatalog(claude), toolinstall.EngineOptions{InstallerVersion: version})
	}
	return toolinstall.NewEngineWithCapabilities(cat, toolinstall.EngineOptions{InstallerVersion: version})
}

func newAgentToolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Install and inventory official provider CLIs (Claude Code, Codex, Grok Build)",
		Long: "tool installs an official provider command-line tool into a versioned directory that\n" +
			"Olivares owns and records a typed receipt. It runs entirely on this host: no control\n" +
			"plane, no server, no account. This release installs Claude Code (plan/v1, publisher-signed\n" +
			"manifest) and Codex and Grok Build (plan/v2) for Linux amd64/arm64. macOS and Windows are\n" +
			"release work still ahead.\n\n" +
			"Verification class is per driver and is recorded on the receipt: Claude is publisher-signed\n" +
			"OpenPGP; Codex is mixed-assurance (exact package digest plus publisher-signed subjects) and\n" +
			"origin-install refuses when a subject verifier is unavailable; Grok is origin-only HTTPS\n" +
			"plus a bounded probe, which is not a publisher signature.\n\n" +
			"Channel names belong to the vendor, not to Olivares: the Grok origin publishes stable and\n" +
			"answers 404 for latest, so --version stable is the pointer to ask for. A pointer the origin\n" +
			"does not publish is reported as version_unknown with the URL and the status.\n\n" +
			"What it never does: vendor the CLI into this repository; edit PATH, shell startup files or\n" +
			"your home; read credential values; overwrite or delete an existing version; run a vendor\n" +
			"install script. Probe --version is not authentication. An installed tool is not a launched\n" +
			"session; `olivares agent session create` launches through a provider profile. A managed\n" +
			"install can pin the session runtime when OLIVARES_SESSION_RUNTIME_*_BIN is unset.",
		Example: "  olivares agent tool plan --driver claude --version latest\n" +
			"  olivares agent tool install --driver grok --version stable --yes\n" +
			"  olivares agent tool install --driver codex --version 0.153.4 --yes\n" +
			"  olivares agent tool list -o json\n" +
			"  olivares agent tool detect --driver grok --probe",
	}
	cmd.AddCommand(newAgentToolDetectCmd(), newAgentToolPlanCmd(), newAgentToolInstallCmd(), newAgentToolListCmd())
	return cmd
}

// agentToolTarget is the flag set shared by plan and install.
type agentToolTarget struct {
	driver, version, platform, root, source string
	cmd                                     *cobra.Command
}

func (t *agentToolTarget) addFlags(cmd *cobra.Command) {
	t.cmd = cmd
	cmd.Flags().StringVar(&t.driver, "driver", "claude", "provider tool to install: claude, codex or grok")
	cmd.Flags().StringVar(&t.version, "version", "latest", "exact version (X.Y.Z) or the vendor pointer latest or stable")
	cmd.Flags().StringVar(&t.platform, "platform", "", "target platform key: linux-x64, linux-arm64, linux-x64-musl, linux-arm64-musl (default: this host)")
	addAgentToolRootFlag(cmd, &t.root)
	cmd.Flags().StringVar(&t.source, "source", "", "http(s) base URL of a mirror carrying the vendor's release layout (default: the official origin); the pinned signing key is required either way")
}

func addAgentToolRootFlag(cmd *cobra.Command, root *string) {
	cmd.Flags().StringVar(root, "root", "", "absolute directory that owns installed tools (default <data-dir>/tools, with data-dir from $OLIVARES_DATA_DIR or the installation default)")
}

func (t *agentToolTarget) changed(name string) bool {
	return t.cmd != nil && t.cmd.Flags().Changed(name)
}

// fromPlan fills flags the operator did not pass from an approved plan and
// refuses flags that contradict it, so an approval cannot be replayed against a
// different selection.
func (t *agentToolTarget) fromPlan(p *toolinstall.Plan) error {
	set := func(name string, dst *string, want string) error {
		if t.changed(name) && *dst != want {
			return exitcode.New(exitcode.Usage, fmt.Errorf("--%s %q contradicts the approved plan (%q); pass the flag matching the plan or omit it", name, *dst, want))
		}
		*dst = want
		return nil
	}
	if err := set("driver", &t.driver, p.Driver); err != nil {
		return err
	}
	if err := set("version", &t.version, p.RequestedVersion); err != nil {
		return err
	}
	if err := set("root", &t.root, p.Destination.Root); err != nil {
		return err
	}
	source := ""
	if p.Source.Kind == toolinstall.SourceMirror {
		source = p.Source.BaseURL
	}
	if err := set("source", &t.source, source); err != nil {
		return err
	}
	if t.changed("platform") {
		got, err := toolinstall.ParsePlatform(t.platform)
		if err != nil {
			return exitcode.New(exitcode.Usage, err)
		}
		if got != p.Platform {
			return exitcode.New(exitcode.Usage, fmt.Errorf("--platform %q contradicts the approved plan (%s)", t.platform, p.Platform))
		}
	}
	t.platform = p.VendorPlatform
	return nil
}

func (t *agentToolTarget) request() (toolinstall.Request, error) {
	root, err := resolveAgentToolRoot(t.root)
	if err != nil {
		return toolinstall.Request{}, err
	}
	platform := toolinstall.HostPlatform()
	if t.platform != "" {
		if platform, err = toolinstall.ParsePlatform(t.platform); err != nil {
			return toolinstall.Request{}, exitcode.New(exitcode.Usage, err)
		}
	}
	return toolinstall.Request{Driver: t.driver, Version: t.version, Platform: platform, DestRoot: root, Source: t.source}, nil
}

func (t *agentToolTarget) requestV2() (toolinstall.RequestV2, error) {
	root, err := resolveAgentToolRoot(t.root)
	if err != nil {
		return toolinstall.RequestV2{}, err
	}
	platform := toolinstall.HostPlatform()
	if t.platform != "" {
		if platform, err = toolinstall.ParsePlatform(t.platform); err != nil {
			return toolinstall.RequestV2{}, exitcode.New(exitcode.Usage, err)
		}
	}
	pv, _, err := toolinstall.PlatformV2For(t.driver, platform)
	if err != nil {
		return toolinstall.RequestV2{}, toolInstallExit(err)
	}
	return toolinstall.RequestV2{Driver: t.driver, Version: t.version, Platform: pv, DestRoot: root, Source: t.source}, nil
}

// resolveAgentToolRoot returns the absolute tools root: the flag as given, or
// <data-dir>/tools from the same data-dir resolution every other command uses.
func resolveAgentToolRoot(flag string) (string, error) {
	if flag != "" {
		if !filepath.IsAbs(flag) {
			return "", exitcode.New(exitcode.Usage, fmt.Errorf("--root must be absolute, got %q", flag))
		}
		return filepath.Clean(flag), nil
	}
	dataDir, err := defaultDataDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dataDir) {
		if dataDir, err = filepath.Abs(dataDir); err != nil {
			return "", err
		}
	}
	return filepath.Join(dataDir, "tools"), nil
}

// toolInstallExit maps engine refusals onto the CLI exit contract. Unsupported
// requests are usage errors; an unknown version or a missing verifier is
// indeterminate, never clean; locks, damage and stale plans are conflicts.
func toolInstallExit(err error) error {
	if err == nil || exitcode.Has(err) {
		return err
	}
	switch toolinstall.KindOf(err) {
	case toolinstall.KindInvalidRequest, toolinstall.KindUnsupportedProvider, toolinstall.KindUnsupportedPlatform, toolinstall.KindUnsupportedSource:
		return exitcode.New(exitcode.Usage, err)
	case toolinstall.KindVersionUnknown, toolinstall.KindVerificationUnavailable:
		return exitcode.New(exitcode.Indeterminate, err)
	case toolinstall.KindLocked, toolinstall.KindDamaged, toolinstall.KindConflict, toolinstall.KindPlanChanged:
		return exitcode.New(exitcode.Conflict, err)
	case toolinstall.KindTransport:
		return exitcode.New(exitcode.Server, err)
	}
	return exitcode.New(exitcode.Err, err)
}

func newAgentToolPlanCmd() *cobra.Command {
	var target agentToolTarget
	var out string
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Resolve and verify what install would fetch and where it would place it, without changing anything",
		Long: "plan fetches the vendor's signed release manifest, verifies its signature under the pinned\n" +
			"key, and prints the concrete selection: version, platform, artifact URL, exact size and\n" +
			"SHA-256, signing key, destination and the action install would take (install, noop for a\n" +
			"release already recorded there, or conflict for an occupied destination). It downloads no\n" +
			"artifact, executes nothing and creates no directory. --out writes the plan as JSON with its\n" +
			"digest so `install --plan` can execute exactly this selection later; a changed selection\n" +
			"is refused at install time rather than substituted.",
		Example: "  olivares agent tool plan --driver claude --version latest\n" +
			"  olivares agent tool plan --version 2.1.261 --platform linux-arm64 --out claude-2.1.261.plan.json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng := toolInstallEngine(cmd.Context())
			capab, err := eng.Capability(target.driver)
			if err != nil {
				return toolInstallExit(err)
			}
			if capab == toolinstall.ProviderCapabilityV2 {
				req, err := target.requestV2()
				if err != nil {
					return err
				}
				plan, err := eng.PlanV2(cmd.Context(), req)
				if err != nil {
					return toolInstallExit(err)
				}
				raw, err := toolinstall.MarshalPlanV2(plan)
				if err != nil {
					return err
				}
				if err := writePlanFile(out, raw, plan.Digest, cmd); err != nil {
					return err
				}
				return renderOut(cmd, func(w io.Writer) error { return renderToolPlanV2(w, plan) }, plan)
			}
			req, err := target.request()
			if err != nil {
				return err
			}
			plan, err := eng.Plan(cmd.Context(), req)
			if err != nil {
				return toolInstallExit(err)
			}
			raw, err := toolinstall.MarshalPlan(plan)
			if err != nil {
				return err
			}
			if err := writePlanFile(out, raw, plan.Digest, cmd); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error { return renderToolPlan(w, plan) }, plan)
		},
	}
	target.addFlags(cmd)
	cmd.Flags().StringVar(&out, "out", "", "write the plan JSON (with its digest) to this new file for a later `install --plan`")
	return cmd
}

func newAgentToolInstallCmd() *cobra.Command {
	var target agentToolTarget
	var planFile string
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Verify, download, probe and place one signed release of a provider CLI",
		Long: "install resolves the selection (as plan does), shows it, and after your confirmation or\n" +
			"--yes performs it: manifest signature verified under the pinned key BEFORE the artifact is\n" +
			"requested; artifact downloaded into a private (0700) staging directory with its exact\n" +
			"size and SHA-256 enforced before it is made executable; `<tool> --version` run from an\n" +
			"empty temporary home under a time budget in its own process group; receipt written;\n" +
			"staging set to 0755 and renamed into <root>/<driver>/<version>-<platform>. An identical\n" +
			"release already there is\n" +
			"revalidated (receipt, bytes, retained signed manifest) and reported as noop; other versions\n" +
			"are left untouched beside it. --plan FILE executes a plan written by `plan --out` and\n" +
			"refuses if the vendor's selection changed since. Exit codes: 2 unsupported driver,\n" +
			"platform or source; 8 version unknown or gpg unavailable (nothing was done); 5 another\n" +
			"install holds the lock, the destination is damaged or occupied, or the plan changed;\n" +
			"1 signature, size, hash or probe refusal (staging removed, nothing recorded).",
		Example: "  olivares agent tool install --driver claude --version 2.1.261 --yes\n" +
			"  olivares agent tool install --driver grok --version stable --yes\n" +
			"  olivares agent tool install --plan claude-2.1.261.plan.json\n" +
			"  olivares agent tool install --version stable --root /srv/olivares/tools --yes -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng := toolInstallEngine(cmd.Context())
			if planFile != "" {
				schema, err := peekJSONSchema(planFile)
				if err != nil {
					return exitcode.New(exitcode.Usage, err)
				}
				if schema == toolinstall.PlanSchemaV2 {
					return runAgentToolInstallV2(cmd, eng, &target, planFile, yes)
				}
			}
			if target.driver == toolinstall.DriverCodex || target.driver == toolinstall.DriverGrok {
				return runAgentToolInstallV2(cmd, eng, &target, planFile, yes)
			}
			var approved *toolinstall.Plan
			if planFile != "" {
				f, err := os.Open(planFile) // #nosec G304 -- the operator names the plan file they approved
				if err != nil {
					return exitcode.New(exitcode.Usage, fmt.Errorf("open plan: %w", err))
				}
				approved, err = toolinstall.ReadPlan(f)
				_ = f.Close()
				if err != nil {
					return toolInstallExit(err)
				}
				if err := target.fromPlan(approved); err != nil {
					return err
				}
			}
			req, err := target.request()
			if err != nil {
				return err
			}
			fresh, err := eng.Plan(cmd.Context(), req)
			if err != nil {
				return toolInstallExit(err)
			}
			if err := renderToolPlan(cmd.ErrOrStderr(), fresh); err != nil {
				return err
			}
			if approved != nil {
				if approved.Digest != fresh.Digest {
					return toolInstallExit(&toolinstall.Refusal{Kind: toolinstall.KindPlanChanged, Err: fmt.Errorf(
						"the approved plan (digest %s) no longer matches the vendor's selection (digest %s); review a new plan", approved.Digest[:12], fresh.Digest[:12])})
				}
			} else {
				if err := confirmToolInstall(cmd, yes, fresh); err != nil {
					return err
				}
				approved = fresh
			}
			rec, plan, err := eng.Install(cmd.Context(), req, approved, cmd.ErrOrStderr())
			if err != nil {
				return toolInstallExit(err)
			}
			result := struct {
				Action  string               `json:"action"`
				Receipt *toolinstall.Receipt `json:"receipt"`
				Plan    *toolinstall.Plan    `json:"plan"`
			}{Action: plan.Action, Receipt: rec, Plan: plan}
			return renderOut(cmd, func(w io.Writer) error {
				fmt.Fprintf(w, "%s: %s %s (%s)\n", plan.Action, rec.Driver, rec.Version, rec.VendorPlatform)
				return renderToolReceipt(w, rec)
			}, result)
		},
	}
	target.addFlags(cmd)
	cmd.Flags().StringVar(&planFile, "plan", "", "execute this plan file written by `plan --out`; it is the approval, so no prompt is shown")
	addYesFlag(cmd, &yes)
	return cmd
}

// confirmToolInstall asks before downloading and executing a vendor binary,
// with the same three states as confirmDestructive: --yes proceeds, a terminal
// is asked, a pipe is refused rather than read as consent.
func confirmToolInstall(cmd *cobra.Command, assumeYes bool, plan *toolinstall.Plan) error {
	if assumeYes {
		return nil
	}
	in := cmd.InOrStdin()
	if !interactiveStdin(in) {
		return exitcode.New(exitcode.Usage, fmt.Errorf(
			"refusing to install %s %s without confirmation: this session is not interactive, so nobody can approve the plan above — pass --yes, or write it with `plan --out` and run `install --plan`",
			plan.Driver, plan.Version))
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Download %d bytes from %s, probe them and place the release at %s? [y/N]: ",
		plan.Artifact.Size, plan.Source.ArtifactURL, plan.Destination.ReleaseDir); err != nil {
		return exitcode.New(exitcode.Err, fmt.Errorf("the confirmation prompt could not be shown: %w", err))
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return exitcode.New(exitcode.Usage, errors.New("aborted: the install was not confirmed"))
}

func newAgentToolListCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List installed provider CLI releases under the tools root and re-check their bytes",
		Long: "list reads every release directory under the tools root, loads its receipt strictly,\n" +
			"re-hashes the executable against it and re-verifies the retained signed manifest under the\n" +
			"pinned key, exactly as install's no-op path does. installed means all three agree now;\n" +
			"unverified means bytes and receipt agree but gpg is unavailable to re-check the signature\n" +
			"(provenance is then not reported); damaged names the contradiction. Nothing is repaired or\n" +
			"removed. Unfinished staging directories left by an interrupted install are reported as\n" +
			"leftovers for you to inspect; they are never removed by name.",
		Example: "  olivares agent tool list\n" +
			"  olivares agent tool list --root /srv/olivares/tools -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rootPath, err := resolveAgentToolRoot(root)
			if err != nil {
				return err
			}
			inv, err := toolInstallEngine(cmd.Context()).List(cmd.Context(), rootPath)
			if err != nil {
				return toolInstallExit(err)
			}
			return renderOut(cmd, func(w io.Writer) error { return renderToolInventory(w, inv) }, inv)
		},
	}
	addAgentToolRootFlag(cmd, &root)
	return cmd
}

func newAgentToolDetectCmd() *cobra.Command {
	var driver, root, manifestPath, sigPath string
	var probe bool
	var probePaths []string
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Report provider CLI executables on this host: managed releases, vendor default paths and PATH",
		Long: "detect looks for the driver's executable in the tools root (releases with receipts), the\n" +
			"vendor's default locations under your own home (for Claude: ~/.local/bin/claude and\n" +
			"~/.local/share/claude/versions/*), /usr/bin and /usr/local/bin, and the directories on\n" +
			"your PATH. Each candidate is reported with its resolved path, size and SHA-256, and a match\n" +
			"class: registered (a receipt records it and its retained signed manifest re-verifies now),\n" +
			"manifest-corroborated (its bytes are named by a signed manifest you pass with\n" +
			"--manifest/--manifest-sig, verified under the pinned key), unregistered-observed,\n" +
			"unverified (receipt and bytes agree but gpg is unavailable) or damaged.\n\n" +
			"Nothing is executed unless you ask. --probe runs `<tool> --version` from an empty temporary\n" +
			"home for registered and manifest-corroborated candidates only; an unregistered path runs\n" +
			"only when you name it exactly with --probe-path (repeatable). A damaged candidate is never\n" +
			"executed, named or not, and no candidate runs unless its file and directory are owned by\n" +
			"you or root and writable by nobody else. A requested probe that is refused or fails makes\n" +
			"detect exit 1 with the reason beside the candidate. detect never reads the vendor's\n" +
			"configuration, credentials or session files, and never looks in other accounts' homes.",
		Example: "  olivares agent tool detect\n" +
			"  olivares agent tool detect --probe -o json\n" +
			"  olivares agent tool detect --probe-path /usr/local/bin/claude\n" +
			"  olivares agent tool detect --manifest manifest.json --manifest-sig manifest.json.sig --probe",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rootPath, err := resolveAgentToolRoot(root)
			if err != nil {
				return err
			}
			opts := toolinstall.DetectOptions{Driver: driver, Root: rootPath, PathEnv: os.Getenv("PATH"), Probe: probe, ProbePaths: probePaths}
			for _, p := range probePaths {
				if !filepath.IsAbs(strings.TrimSpace(p)) {
					return exitcode.New(exitcode.Usage, fmt.Errorf("--probe-path %q must be an absolute path: the selection names one exact file", p))
				}
			}
			if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(strings.TrimSpace(home)) {
				opts.Home = filepath.Clean(strings.TrimSpace(home))
			}
			if (manifestPath == "") != (sigPath == "") {
				return exitcode.New(exitcode.Usage, errors.New("--manifest and --manifest-sig go together: corroboration needs the signed manifest AND its detached signature"))
			}
			if manifestPath != "" {
				manifest, err := readBounded(manifestPath, 1<<20)
				if err != nil {
					return exitcode.New(exitcode.Usage, err)
				}
				sig, err := readBounded(sigPath, 64<<10)
				if err != nil {
					return exitcode.New(exitcode.Usage, err)
				}
				opts.Material = &toolinstall.Material{Manifest: manifest, Signature: sig}
			}
			cands, derr := toolInstallEngine(cmd.Context()).Detect(cmd.Context(), opts)
			if derr != nil && toolinstall.KindOf(derr) != toolinstall.KindProbeFailed {
				return toolInstallExit(derr)
			}
			if cands == nil {
				cands = []toolinstall.Candidate{}
			}
			if err := renderListOut(cmd, cands, fmt.Sprintf("no %s executable found under %s, the vendor default paths or PATH", driver, rootPath),
				func(w io.Writer, c toolinstall.Candidate) error { return renderToolCandidate(w, c) }, cands); err != nil {
				return err
			}
			// A probe that was asked for and did not run is a failed request:
			// the candidates are shown above and the exit code says so.
			return toolInstallExit(derr)
		},
	}
	cmd.Flags().StringVar(&driver, "driver", "claude", "provider tool to look for")
	addAgentToolRootFlag(cmd, &root)
	cmd.Flags().BoolVar(&probe, "probe", false, "run `--version` for registered and manifest-corroborated candidates only, from an empty temporary home (an observed path is not permission to run it)")
	cmd.Flags().StringArrayVar(&probePaths, "probe-path", nil, "exact absolute path of one candidate to run `--version` on (repeatable); required for unregistered-observed paths; damaged releases are never run")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "a vendor release manifest to corroborate candidates against (verified with --manifest-sig under the pinned key)")
	cmd.Flags().StringVar(&sigPath, "manifest-sig", "", "the detached OpenPGP signature of --manifest")
	return cmd
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- the operator names the metadata file to corroborate against
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return b, nil
}

func renderToolPlan(w io.Writer, p *toolinstall.Plan) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "plan\t%s\taction: %s\n", p.Digest, p.Action)
	fmt.Fprintf(tw, "driver\t%s %s\tplatform %s (%s), requested %q via %s\n", p.Driver, p.Version, p.VendorPlatform, p.Platform, p.RequestedVersion, p.Channel)
	fmt.Fprintf(tw, "source\t%s\t%s\n", p.Source.Kind, p.Source.BaseURL)
	fmt.Fprintf(tw, "artifact\t%s\t%d bytes, sha256 %s\n", p.Source.ArtifactURL, p.Artifact.Size, p.Artifact.SHA256)
	fmt.Fprintf(tw, "signed\t%s\tprimary key %s, signing key %s, %s\n", p.Provenance.Class, p.Provenance.KeyFingerprint, p.Provenance.SigningKeyFingerprint, p.Provenance.SignatureCreated.Format(time.RFC3339))
	fmt.Fprintf(tw, "verifier\t%s\tmanifest sha256 %s\n", p.Provenance.Verifier, p.Provenance.ManifestSHA256)
	fmt.Fprintf(tw, "destination\t%s\n", p.Destination.Executable)
	fmt.Fprintf(tw, "verified\t%s\n", strings.Join(p.Verified, ", "))
	if p.Existing != nil {
		fmt.Fprintf(tw, "existing\treceipt=%t executable=%t\t%s\n", p.Existing.ReceiptPresent, p.Existing.ExecutablePresent, p.Existing.Note)
		if p.Existing.ExecutableSHA256 != "" {
			fmt.Fprintf(tw, "observed\tsha256 %s\t%d bytes (observed, not verified)\n", p.Existing.ExecutableSHA256, p.Existing.ExecutableSize)
		}
	}
	return tw.Flush()
}

func writePlanFile(out string, raw []byte, digest string, cmd *cobra.Command) error {
	if out == "" {
		return nil
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G304 -- the operator names where their own plan file goes
	if err != nil {
		return exitcode.New(exitcode.Usage, fmt.Errorf("write plan: %w (an existing file is not overwritten)", err))
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "plan written to %s (digest %s)\n", out, digest)
	return nil
}

func peekJSONSchema(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the plan file they approved
	if err != nil {
		return "", fmt.Errorf("open plan: %w", err)
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return "", fmt.Errorf("plan file is not JSON: %w", err)
	}
	return head.Schema, nil
}

func runAgentToolInstallV2(cmd *cobra.Command, eng *toolinstall.Engine, target *agentToolTarget, planFile string, yes bool) error {
	var approved *toolinstall.PlanV2
	if planFile != "" {
		f, err := os.Open(planFile) // #nosec G304 -- the operator names the plan file they approved
		if err != nil {
			return exitcode.New(exitcode.Usage, fmt.Errorf("open plan: %w", err))
		}
		approved, err = toolinstall.ReadPlanV2(f)
		_ = f.Close()
		if err != nil {
			return toolInstallExit(err)
		}
		target.driver = approved.Selection.Driver
		target.version = approved.Selection.RequestedVersion
		target.root = approved.Selection.Destination.Root
	}
	req, err := target.requestV2()
	if err != nil {
		return err
	}
	if approved != nil {
		req.Driver = approved.Selection.Driver
		req.Version = approved.Selection.RequestedVersion
		req.Platform = approved.Selection.Platform
		req.DestRoot = approved.Selection.Destination.Root
	}
	fresh, err := eng.PlanV2(cmd.Context(), req)
	if err != nil {
		return toolInstallExit(err)
	}
	if err := renderToolPlanV2(cmd.ErrOrStderr(), fresh); err != nil {
		return err
	}
	if approved != nil {
		if approved.Digest != fresh.Digest {
			return toolInstallExit(&toolinstall.Refusal{Kind: toolinstall.KindPlanChanged, Err: fmt.Errorf(
				"the approved plan (digest %s) no longer matches the vendor's selection (digest %s); review a new plan", approved.Digest[:12], fresh.Digest[:12])})
		}
	} else {
		if err := confirmToolInstallV2(cmd, yes, fresh); err != nil {
			return err
		}
		approved = fresh
	}
	rec, plan, err := eng.InstallV2(cmd.Context(), req, approved, cmd.ErrOrStderr())
	if err != nil {
		return toolInstallExit(err)
	}
	result := struct {
		Receipt *toolinstall.ReceiptV2 `json:"receipt"`
		Plan    *toolinstall.PlanV2    `json:"plan"`
	}{Receipt: rec, Plan: plan}
	return renderOut(cmd, func(w io.Writer) error {
		fmt.Fprintf(w, "install: %s %s (%s) verification %s\n", rec.Driver, rec.Version, rec.VendorPlatform, rec.VerificationKind)
		return renderToolReceiptV2(w, rec)
	}, result)
}

func confirmToolInstallV2(cmd *cobra.Command, assumeYes bool, plan *toolinstall.PlanV2) error {
	if assumeYes {
		return nil
	}
	in := cmd.InOrStdin()
	if !interactiveStdin(in) {
		return exitcode.New(exitcode.Usage, fmt.Errorf(
			"refusing to install %s %s without confirmation: this session is not interactive — pass --yes, or write it with `plan --out` and run `install --plan`",
			plan.Selection.Driver, plan.Selection.Version))
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Download from %s (verification %s) and place the release at %s? [y/N]: ",
		plan.Selection.Source.Package.URL, plan.Selection.Verification.Kind, plan.Selection.Destination.ReleaseDir); err != nil {
		return exitcode.New(exitcode.Err, fmt.Errorf("the confirmation prompt could not be shown: %w", err))
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return exitcode.New(exitcode.Usage, errors.New("aborted: the install was not confirmed"))
}

func renderToolPlanV2(w io.Writer, p *toolinstall.PlanV2) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "plan\t%s\tschema %s\n", p.Digest, p.Schema)
	fmt.Fprintf(tw, "driver\t%s %s\tplatform %s, requested %q via %s\n", p.Selection.Driver, p.Selection.Version, p.Selection.VendorPlatform, p.Selection.RequestedVersion, p.Selection.Channel)
	fmt.Fprintf(tw, "package\t%s\n", p.Selection.Source.Package.URL)
	fmt.Fprintf(tw, "verification\t%s\tpackage policy %s\n", p.Selection.Verification.Kind, p.Selection.PackagePolicyID)
	fmt.Fprintf(tw, "destination\t%s\n", p.Selection.Destination.Executable)
	return tw.Flush()
}

func renderToolReceiptV2(w io.Writer, r *toolinstall.ReceiptV2) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "executable\t%s\n", r.Destination.Executable)
	fmt.Fprintf(tw, "fetched\t%d bytes, sha256 %s\n", r.FetchedObject.Size, r.FetchedObject.SHA256)
	fmt.Fprintf(tw, "probe\t%s (%d ms)\n", strings.TrimSpace(strings.SplitN(r.Probe.Output, "\n", 2)[0]), r.Probe.DurationMS)
	fmt.Fprintf(tw, "verification\t%s\t%s\n", r.VerificationKind, r.AuthObservation.Note)
	fmt.Fprintf(tw, "installed\t%s by olivares %s, plan digest %s\n", r.InstalledAt.Format(time.RFC3339), r.InstallerVersion, r.PlanDigest)
	fmt.Fprintf(tw, "receipt\t%s\n", filepath.Join(r.Destination.ReleaseDir, toolinstall.ReceiptFile))
	return tw.Flush()
}

func renderToolReceipt(w io.Writer, r *toolinstall.Receipt) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "executable\t%s\n", r.Destination.Executable)
	fmt.Fprintf(tw, "artifact\t%d bytes, sha256 %s\n", r.Artifact.Size, r.Artifact.SHA256)
	fmt.Fprintf(tw, "probe\t%s (%d ms)\n", strings.TrimSpace(strings.SplitN(r.Probe.Output, "\n", 2)[0]), r.Probe.DurationMS)
	fmt.Fprintf(tw, "signed\t%s by %s (signing key %s)\n", r.Provenance.Class, r.Provenance.KeyFingerprint, r.Provenance.SigningKeyFingerprint)
	fmt.Fprintf(tw, "installed\t%s by olivares %s, plan digest %s\n", r.InstalledAt.Format(time.RFC3339), r.InstallerVersion, r.PlanDigest)
	fmt.Fprintf(tw, "receipt\t%s\n", filepath.Join(r.Destination.ReleaseDir, toolinstall.ReceiptFile))
	return tw.Flush()
}

func renderToolInventory(w io.Writer, inv *toolinstall.Inventory) error {
	if !inv.RootExists {
		_, err := fmt.Fprintf(w, "no tools root at %s yet; nothing installed\n", inv.Root)
		return err
	}
	if len(inv.Installed) == 0 {
		fmt.Fprintf(w, "no releases recorded under %s\n", inv.Root)
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "DRIVER\tVERSION\tPLATFORM\tSTATE\tSHA256\tINSTALLED\tEXECUTABLE")
		for _, in := range inv.Installed {
			state := in.State
			if in.Reason != "" {
				state += " (" + in.Reason + ")"
			}
			installed := ""
			if !in.InstalledAt.IsZero() {
				installed = in.InstalledAt.Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", in.Driver, in.Version, in.VendorPlatform, state, shortDigest(in.SHA256), installed, in.Executable)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, lo := range inv.Leftovers {
		fmt.Fprintf(w, "leftover staging: %s (%s)\n", lo.Path, lo.Note)
	}
	for _, u := range inv.Unexpected {
		fmt.Fprintf(w, "unexpected entry (not managed by this tool, left untouched): %s\n", u)
	}
	return nil
}

func renderToolCandidate(w io.Writer, c toolinstall.Candidate) error {
	line := fmt.Sprintf("%s\t%s\t%s", c.Path, c.Origin, c.Match)
	if c.Version != "" {
		line += "\tversion " + c.Version
	}
	if c.SHA256 != "" {
		line += fmt.Sprintf("\t%d bytes sha256 %s", c.Size, c.SHA256)
	}
	if c.IsSymlink {
		line += "\t-> " + c.Resolved
	}
	if c.Note != "" {
		line += "\t" + c.Note
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	if c.ProbeSkipped != "" {
		_, err := fmt.Fprintf(w, "    probe skipped: %s\n", c.ProbeSkipped)
		return err
	}
	if c.ProbeError != "" && c.Probe == nil {
		_, err := fmt.Fprintf(w, "    probe failed: %s\n", c.ProbeError)
		return err
	}
	if c.Probe != nil {
		out := strings.TrimSpace(strings.SplitN(c.Probe.Output, "\n", 2)[0])
		if c.ProbeError != "" {
			_, err := fmt.Fprintf(w, "    probe failed: %s\n", c.ProbeError)
			return err
		}
		_, err := fmt.Fprintf(w, "    probe: %s (%d ms)\n", out, c.Probe.DurationMS)
		return err
	}
	return nil
}

func shortDigest(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
