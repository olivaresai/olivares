// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command olivares is the single self-hosted binary for Olivares AI: engine,
// embedded web console and store in one artifact. It exposes `quickstart`
// (guided secure first run), `serve` (REST + gRPC, TLS-on-by-default),
// `license`, `audit`, `dr` (disaster-recovery backup/restore) and `version`
// subcommands (plus a hidden web-asset diagnostic). Beta: APIs and the module
// surface may still change before v1.
package main

import (
	"crypto/fips140"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/webui"
)

// Build metadata, overridable at link time, e.g.:
//
//	go build -ldflags "-X main.version=v26.6.0 \
//	  -X main.commit=$(git rev-parse --short HEAD) \
//	  -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	// ExecuteC (not Execute) so the resolved command is available below: it is
	// the only way to classify the two failures cobra reports past every hook.
	cmd, err := newRootCmd().ExecuteC()
	if err == nil {
		return exitcode.OK
	}
	// `security check` signals "this version is affected" as a non-zero exit with
	// its report already on stdout; it is not a failure to explain, so exit
	// quietly (Degraded) rather than print an empty "Error:" line.
	if errors.Is(err, errAffected) {
		return exitcode.Degraded
	}
	code := exitcode.From(err)
	// A missing required flag, or a violated flag group, is a USAGE error — but
	// cobra raises both inside execute() AFTER every hook this tree can install
	// (command.go:1007-1012), as a plain error that exitcode.From can only read
	// as generic. Re-ask cobra's OWN exported validators about the command it
	// resolved: no reimplementation to drift, and if cobra ever stops agreeing
	// the answer stays 1 rather than a wrongly confident 2. Reached only when
	// the invocation already failed, and a command whose RunE ran at all had
	// its required flags satisfied by definition.
	if code == exitcode.Err && cmd != nil {
		if cmd.ValidateRequiredFlags() != nil || cmd.ValidateFlagGroups() != nil {
			code = exitcode.Usage
		}
	}
	// A silent coded error already printed its report (e.g. `status` on a
	// degraded engine) — the wrapper exists only for the exit code.
	if exitcode.Silent(err) {
		return code
	}
	// SilenceErrors (set on the root) stops cobra from printing, so surface the
	// message here — otherwise every command failure exits 1 with NO explanation
	// (e.g. the release-build `license sign` hint would never reach the operator).
	fmt.Fprintln(os.Stderr, "Error:", err)
	return code
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "olivares",
		Short: "Olivares AI — self-hosted engine for enterprise AI",
		Long: "olivares is the single self-hosted binary for Olivares AI: integrate, manage and\n" +
			"secure the AI running in your enterprise — one ground truth: Claude Code at the deepest level, Codex and Grok Build alongside.\n" +
			"Run `olivares quickstart` for a guided, secure-by-default first run.\n\n" +
			"Exit codes:\n" +
			"  0  success\n" +
			"  1  generic error\n" +
			"  2  usage error (unknown flag or bad arguments)\n" +
			"  3  authentication/authorization rejected\n" +
			"  4  entity not found\n" +
			"  5  conflict with current state\n" +
			"  6  server or network failure\n" +
			"  7  degraded (`status` on an engine reporting a FAULT — an install\n" +
			"     that merely has optional capabilities unconfigured exits 0 and\n" +
			"     names them; `security check` on an affected version)\n" +
			"  8  indeterminate — the answer could not be established (`security\n" +
			"     check` on a build that declares no version). NOT a clean result:\n" +
			"     treat it as unanswered, not as safe",
		Example: "  olivares quickstart\n" +
			"  olivares auth login --server https://plane.example.com --token \"$OLIVARES_TOKEN\"\n" +
			"  olivares status\n" +
			"  olivares agent session ls -o json",
		// Wire the stamped build metadata into cobra so `olivares --version`
		// exists and reports the SAME provenance as the `version` subcommand
		// (E2 traceability; the full key/FIPS report stays in `version`).
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Exit contract: a flag-parse failure is a USAGE error — carry the
	// code so scripts can distinguish a typo from a real failure. Cobra prints
	// the usage hint itself; the wrapper only classifies.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return exitcode.New(exitcode.Usage, err)
	})
	// The trailing note is not padding. Cobra renders `(default "text")` off this flag's
	// value, and for REPORT commands that is not what happens: renderReportOut (render.go)
	// deliberately keeps JSON when nobody asked, a decision taken in 41382bdd0 because
	// flipping it broke 13 tests and `audit verify --strict` tells operators to read "the
	// JSON report above". That asymmetry was recorded only in a Go comment, so `-o` still
	// advertised a text default it does not honor on those commands — measured 2026-08-07
	// against the real binary: `olivares license status` prints JSON, `-o text` prints a
	// table. Say it where operators read it. Deliberately no list of which commands: a
	// hand-written one goes stale the day a report command is added.
	root.PersistentFlags().VarP(&outputFlagValue{value: "text"}, "output", "o",
		"global output format: text or json (report commands keep json unless -o is given)")
	_ = root.RegisterFlagCompletionFunc("output", completeOutput)
	root.AddCommand(newQuickstartCmd(), newSetupCmd(), newConfigCmd(), newAuthCmd(), newDBCmd(), newMigrateCmd(), newVersionCmd(), newStatusCmd(), newWebUIFilesCmd(), newServeCmd(), newCollectorCmd(), newLicenseCmd(), newUpgradeCmd(), newReleaseCmd(), newAuditCmd(), newDDILCmd(), newDRCmd(), newOpenAPICmd(), newClaudeHookCmd(), newCodexHookCmd(), newGrokHookCmd(), newHookPEPCmd(), newKeysCmd(), newEvalsCmd(), newAgentCmd(), newWorkCmd(), newCodexCmd(), newMCPCmd(), newThreatIntelCmd(), newHooksCmd(), newSecretsCmd(), newSourcesCmd(), newConnectorCmd(), newSuperadminCmd(), newEventingCmd(), newSecurityCmd(), newFindingsCmd(), newComplianceCmd(), newSupportCmd(), newCompletionCmd(root), newCommandsCmd(), newFirstPartyBinsCmd(), newExtractCmd(), newTokensCmd(), newUsersCmd(), newMembersCmd(), newTenantsCmd(), newGovernanceCmd(), newCapabilitiesCmd())
	// The observe-and-report lane: one top-level command per module namespace it
	// covers, named after the namespace so `olivares <ns>` and /v1/m/<ns>/ are the
	// same word. Their shared transport is cmd_observeplane.go.
	root.AddCommand(newReportingCmd(), newNotifyCmd(), newHealthCmd(), newAccessMapCmd(),
		newObservabilityCmd(), newConsoleViewsCmd(), newAdoptionCmd(), newIdentityCmd(),
		newInventoryCmd(), newPostureCmd())
	// The model stack (C08-02 lot 1): the model estate and its governance, the
	// gateway in front of it, and what it costs.
	root.AddCommand(newModelsCmd(), newInferenceProxyCmd(), newFinOpsCmd())
	// The governed-data lane (C09 lot 2): the corpus, who may reach it, and the
	// admission gate the connectors and MCP servers carrying it pass through.
	root.AddCommand(newKnowledgeCmd(), newSourceScopeCmd(), newCatalogCmd())
	// The agent-execution plane (C09 lot 3): everything that runs an agent or
	// governs its execution — how it is orchestrated, where it is tried out, what
	// is recorded of it, how it is deployed, how it is attacked on purpose, how it
	// speaks, and the Claude-managed policy and sessions behind it.
	root.AddCommand(newOrchestrationCmd(), newSandboxCmd(), newRecordingCmd(), newDeployCmd(),
		newRedteamCmd(), newVoiceCmd(), newClaudePolicyCmd(), newClaudeAgentsCmd())
	// Enterprise-only top-level commands (`enterprise enable/disable/status/promote`).
	// The default (AGPL) build adds none — enterpriseRootCommands returns nil in
	// wire_noenterprise.go, so the community binary never links the activation writer;
	// the -tags enterprise overlay wires the real command group.
	root.AddCommand(enterpriseRootCommands()...)
	groupRootCommands(root)
	hideUnavailableAddOns(root)
	// Last, so it sees every command this build registered: make the exit-code
	// contract above true for the whole tree. Before this, an unknown
	// subcommand inside a group printed the group's help to STDOUT and exited
	// 0 — `olivares agent typo` was indistinguishable from success to `set -e`.
	enforceSubcommandContract(root)
	return root
}

// commandGroups maps every VISIBLE top-level command to its help group:
// with ~30 commands a flat "Available Commands" list buries the daily verbs.
// cmd_help_completeness_test enforces the map stays total, so a new visible
// command cannot land ungrouped.
var commandGroups = map[string]string{
	// Setup & configuration.
	"quickstart": "setup", "setup": "setup", "config": "setup", "auth": "setup", "db": "setup",
	"migrate": "setup", "keys": "setup", "completion": "setup", "connector": "setup",
	// The browser-free first run (C08-01): the identity families an install must
	// have before anything else in this binary can authenticate.
	"tenants": "setup", "users": "setup", "members": "setup", "tokens": "setup",
	// Operate.
	"serve": "operate", "collector": "operate", "agent": "operate", "codex": "operate",
	"eventing": "operate", "sources": "operate", "secrets": "operate", "work": "operate",
	"superadmin": "operate", "support": "operate",
	// Govern.
	"license": "govern", "hookpep": "govern", "claude-hook": "govern", "codex-hook": "govern",
	// grok-hook entra con sus gemelos: llego en el PR #1011 sin grupo, y un comando VISIBLE sin
	// grupo no aparece en la ayuda por temas — existe y no se encuentra.
	"grok-hook": "govern",
	"hooks":     "govern", "evals": "govern", "ddil": "govern", "mcp": "govern",
	"compliance": "govern",
	"models":     "govern", "inference-proxy": "govern",
	// Observe.
	"status": "observe", "audit": "observe", "version": "observe", "openapi": "observe",
	"finops": "observe",
	// Security.
	"security": "security", "findings": "security", "threatintel": "security", "dr": "security",
	// Release.
	"upgrade": "release",
	// The agent-execution plane (C09 lot 3). Split by what an operator is doing:
	// deploying and orchestrating an agent is operating it; recording, voice
	// policy and the Claude-managed surfaces are governing it; and red-teaming it
	// is security work.
	"orchestration": "operate", "sandbox": "operate", "deploy": "operate",
	"recording": "govern", "voice": "govern", "claude-policy": "govern", "claude-agents": "govern",
	"redteam": "security",
	// The observe-and-report lane (C09 lot 4). Grouped by the question each verb
	// answers rather than by the module that serves it: `accessmap` and `posture`
	// are security reads even though they are ordinary module namespaces, and
	// `notify` is an operate verb because authoring a route changes what the
	// estate actually does when something goes wrong.
	"reporting": "observe", "health": "observe", "observability": "observe",
	"adoption": "observe", "inventory": "observe", "consoleviews": "observe",
	"accessmap": "security", "posture": "security",
	"identity": "govern",
	"notify":   "operate",
	// The governed-data lane (C09 lot 2). The split is not clean and the majority
	// verb decides it: `knowledge` is 31 verbs an operator runs on the corpus
	// (ingest, reindex, query, documents, prompts, memory, scans) against 22 that
	// govern it (DLP rules, context policies, data-product lifecycle and
	// contracts, lineage), so it lands in operate. `sourcescope` and `catalog`
	// have no such majority to weigh: every verb in them decides who may reach a
	// source or what may be admitted at all, and two of them (posture approve,
	// admission policy) are the second leg of a dual control.
	"knowledge": "operate", "sourcescope": "govern", "catalog": "govern",
	// El plano de gobierno y la superficie descubierta (C08-02). El criterio es el de este
	// mapa —la PREGUNTA que contesta el verbo, no el módulo que la sirve—, y por eso los dos
	// no caen en el mismo sitio aunque nazcan juntos:
	//
	//   `governance` DECIDE: kill switches, break-glass, aprobaciones, guardian. Va a govern
	//   con `license`, `hookpep` y `compliance`, aunque sus verbos de hoy sean sólo lectura —
	//   lo que se lee ahí es el estado de la aplicación de política.
	//
	//   `capabilities` INVENTARÍA: qué servidores hay conectados y qué herramientas y skills
	//   traen. Es la pregunta de `inventory`, que está en observe. NO va con `mcp` —que sí
	//   está en govern— porque `mcp pins` DECIDE qué puede ejecutarse y esto sólo describe.
	"governance": "govern", "capabilities": "observe",
}

// addOnOnlyCommands are the top-level groups whose every verb answers
// "not available" unless this binary links the commercial add-ons.
var addOnOnlyCommands = []string{"hooks", "threatintel"}

// hideUnavailableAddOns keeps a command group out of `olivares --help` when
// nothing under it can succeed in this build (E6).
//
// Measured on the community artifact: all two `hooks` verbs and all five
// `threatintel` verbs fail, every time, with an error that used to say "build
// with -tags enterprise" — a path that does not exist here either
// (the commercial tree moved to its own distribution). So the root help
// advertised seven verbs that this artifact can only ever refuse, and pointed
// at a repair that does not work.
//
// The size of that missing path, re-measured in because the figure here was
// four times too small: `go build -tags enterprise ./cmd/olivares` reports
// 47 `undefined:` errors over 43 unique symbols in 17 files — but ONLY with
// `-gcflags=-e`. Plain `go build` stops at 10 and prints "too many errors", and
// 10 is what this comment used to record. Anyone re-measuring without the flag
// will reach the same wrong number, so it is written down here once.
//
// What that does NOT mean is that the capability is broken. The same hub revision
// builds GREEN from the enterprise distribution, where the overlay supplies these
// symbols; the errors are the expected shape of an overlay that lives elsewhere,
// not a defect. Since that claim is not a matter of belief: the enterprise
// repository runs `hub-sha-verify`, which builds and tests the overlay against a
// given hub revision and fails when the core breaks it.
//
// They are HIDDEN, not removed. The commands stay registered and invocable, so
// an existing script gets the same honest refusal rather than "unknown
// command", `olivares hooks --help` still documents what the add-on does, and
// the enterprise build (where enterpriseAddOnsLinked is true) lists them
// normally. What goes away is the false offer to a first-time reader.
func hideUnavailableAddOns(root *cobra.Command) {
	if enterpriseAddOnsLinked {
		return
	}
	for _, c := range root.Commands() {
		for _, name := range addOnOnlyCommands {
			if c.Name() == name {
				c.Hidden = true
			}
		}
	}
}

// groupRootCommands registers the help groups and assigns each visible command.
func groupRootCommands(root *cobra.Command) {
	root.AddGroup(
		&cobra.Group{ID: "setup", Title: "Setup & Configuration:"},
		&cobra.Group{ID: "operate", Title: "Operate:"},
		&cobra.Group{ID: "govern", Title: "Govern:"},
		&cobra.Group{ID: "observe", Title: "Observe & Diagnose:"},
		&cobra.Group{ID: "security", Title: "Security:"},
		&cobra.Group{ID: "release", Title: "Release & Upgrade:"},
	)
	root.SetHelpCommandGroupID("setup")
	for _, c := range root.Commands() {
		if g, ok := commandGroups[c.Name()]; ok {
			c.GroupID = g
		}
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the olivares version, build metadata and FIPS 140-3 mode",
		Long: "version prints the binary's semantic version, git commit, build date, OS/arch and Go\n" +
			"runtime, its FIPS 140-3 mode (self-reported, not a validation claim), and the origins +\n" +
			"fingerprints of the independent license and OTA verification keys compiled into it.",
		Example: "  olivares version",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// FIPS 140-3 MODE of this process, self-verified in-process (SCP-09): the
			// module version is the one GOFIPS140 linked at build time, and enabled
			// reports the GODEBUG=fips140 runtime toggle. This states MODE, never a
			// validation claim (docs/SCP-09-FIPS-STIG.md owns the honest wording).
			fips := "off"
			if fips140.Enabled() {
				fips = "on"
			}
			// license-key reports WHICH offline verification key this binary was BUILT
			// with (origin in {release,dev,none,misconfigured} + an 8-hex fingerprint).
			// It is a build-provenance aid, NOT an attestation — a binary self-reports
			// its compiled-in key; trust comes from the signed release pipeline
			// (cosign/SLSA — docs/RELEASE-VERIFICATION.md), not from this string.
			licKey := license.KeyOrigin()
			if fp := license.KeyFingerprint(); fp != "" {
				licKey += "/" + fp
			}
			otaKey := release.KeyOrigin()
			if fp := release.KeyFingerprint(); fp != "" {
				otaKey += "/" + fp
			}
			info := versionOutput{
				Version: version, Commit: commit, Date: date, OS: runtime.GOOS, Arch: runtime.GOARCH,
				Go: runtime.Version(), FIPS: fips, Module: fips140.Version(), LicenseKey: licKey, OTAKey: otaKey,
			}
			if err := renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out,
					"olivares %s (commit %s, built %s, %s/%s, %s, fips140=%s module=%s, license-key=%s, ota-key=%s)\n",
					info.Version, info.Commit, info.Date, info.OS, info.Arch, info.Go,
					info.FIPS, info.Module, info.LicenseKey, info.OTAKey)
				return err
			}, info); err != nil {
				return err
			}
			if w := keyDomainCollisionWarning(); w != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: "+w)
			}
			return nil
		},
	}
}

type versionOutput struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Date       string `json:"date"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Go         string `json:"go"`
	FIPS       string `json:"fips"`
	Module     string `json:"module"`
	LicenseKey string `json:"license_key"`
	OTAKey     string `json:"ota_key"`
}

// keyDomainCollisionWarning reports, AT RUNTIME, that this binary was built with
// the SAME Ed25519 public key as both its license anchor and its OTA anchor — the
// exact key-reuse the trust-domain split exists to prevent. It returns "" when the
// anchors are distinct or when either is absent (a dev build embeds no OTA key;
// that is a different, already-signaled condition — `upgrade` fails closed).
//
// Why a RUNTIME signal and not only the build gate: scripts/check-release-pubkey.sh
// rejects equal anchors, but it only runs on the goreleaser/`task build:repro`
// paths. A direct `go build -tags release -ldflags "-X …license.releasePublicKeyB64=K
// -X …release.artifactVerifyKeyB64=K"` — the recipe documented in both packages and
// used by scripts/fips-verify.sh — bypasses the gate entirely and would ship a
// silently key-reusing binary. With one key in both domains an actor who can make
// the online license Worker sign is one domain-tag mistake away from minting an OTA
// manifest, so the collision must be visible on the artifact itself, not just in the
// pipeline that was supposed to have built it.
func keyDomainCollisionWarning() string {
	lic := license.DefaultPublicKey()
	ota := release.EmbeddedKey()
	if len(lic) == 0 || len(ota) == 0 || !lic.Equal(ota) {
		return ""
	}
	return fmt.Sprintf("this build embeds the SAME key (%s) as its license anchor AND its OTA anchor. "+
		"The two trust domains MUST use independent keypairs: the license private key is online in the "+
		"fulfillment Worker, while the OTA private key must stay off-box. Rebuild with distinct "+
		"OLIVARES_LICENSE_PUBKEY / OLIVARES_OTA_PUBKEY anchors (scripts/check-release-pubkey.sh) "+
		"and do not trust this binary's updates.", release.Fingerprint(ota))
}

// newWebUIFilesCmd is a hidden diagnostic that lists the web assets embedded in
// the binary. It also forces the webui package (and thus the go:embed bundle)
// to be linked into the binary, so the placeholder UI ships from commit one.
func newWebUIFilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "webui-files",
		Short: "List the web UI assets embedded in this binary (diagnostic)",
		Long: "webui-files is a hidden diagnostic that lists every web UI asset embedded in this\n" +
			"binary's go:embed bundle and prints their total count; it also forces the webui\n" +
			"package to link so the bundle ships from commit one.",
		Example: "  olivares webui-files",
		Hidden:  true,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			count := 0
			walkErr := fs.WalkDir(webui.FS(), ".", func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				count++
				_, werr := fmt.Fprintln(out, path)
				return werr
			})
			if walkErr != nil {
				return walkErr
			}
			_, err := fmt.Fprintf(out, "%d embedded web asset(s)\n", count)
			return err
		},
	}
}
