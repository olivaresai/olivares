// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_redteam.go drives the adversarial probe battery (/v1/m/redteam,
// modules/redteam/redteam.go) from a terminal: the probe catalog, the consent
// ceremony that authorizes a target, and the scored runs.
//
// THE CONSENT GATE IS THE WHOLE POINT OF THIS FAMILY, and this CLI does not
// weaken it anywhere:
//
//   - REGISTRATION IS NOT CONSENT. `targets register` records a target; the
//     engine still refuses to scan it (403) until `targets authorize` says so.
//     Two commands, because they are two decisions.
//
//   - ONLY YOUR OWN ESTATE. The engine refuses an agent_ref that does not
//     resolve in this tenant's inventory (HTTP 422). That is classified as a
//     usage error (exit 2) so a pipeline stops instead of retrying, and the
//     message is the engine's own.
//
//   - `targets authorize` GOES THROUGH confirmDestructive, and it is the one
//     verb here that does. It destroys nothing — it grants permission to attack
//     a live agent, which is the dual-use red line this product draws. Doing
//     that from an unattended cron job by accident is precisely the failure the
//     confirmation exists to prevent. `targets revoke` is NOT gated: refusing
//     consent is the safe direction and should never need a ceremony.
//
//   - `runs launch` is NOT gated, deliberately. It is the everyday CI verb, and
//     the control that matters already ran: an unauthorized target is refused by
//     the engine, and a revocation mid-run discards the assessment rather than
//     scoring it.

const redteamModule = "redteam"

func newRedteamCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "redteam",
		Short: "Run the consent-gated adversarial battery against your own agents",
		Long: "redteam scores YOUR OWN agents against an adversarial probe battery — prompt\n" +
			"injection, jailbreak, exfiltration, tool poisoning — mapped to OWASP and MITRE\n" +
			"ATLAS.\n\n" +
			"It is consent-gated in two steps on purpose. Registering a target records it;\n" +
			"authorizing it is a separate, confirmed decision, and until it is made the\n" +
			"engine refuses to run a single probe. A target whose agent is not in your own\n" +
			"inventory is refused outright.",
		Example: "  olivares redteam catalog\n" +
			"  olivares redteam targets register --agent-ref agent-a --name \"support bot\"\n" +
			"  olivares redteam targets authorize rt-1 --yes\n" +
			"  olivares redteam runs launch --target-ref rt-1",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newRedteamCatalogCmd(&flags),
		newRedteamTargetsCmd(&flags),
		newRedteamRunsCmd(&flags),
	)
	return root
}

func newRedteamCatalogCmd(flags *authClientFlags) *cobra.Command {
	var suite string
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "List the probe battery and its OWASP/ATLAS coverage",
		Long: "catalog reports every probe the build carries, grouped by attack family, with\n" +
			"the OWASP and MITRE ATLAS identifiers each one maps to and the taxonomy version\n" +
			"those identifiers came from.\n\n" +
			"It is NOT paginated by the engine: the whole battery comes back in one answer.",
		Example: "  olivares redteam catalog\n  olivares redteam catalog --suite injection -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			setQuery(q, "suite", suite)
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodGet, path: "/catalog", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var body struct {
				Total  int               `json:"total"`
				Probes []json.RawMessage `json:"probes"`
			}
			// An unreadable body is a SERVER failure (exit 6), the same code the
			// other four envelope readers in this lot give the identical
			// condition — renderAgentExecList (cmd_agentexec.go:432),
			// renderAgentExecGraph (cmd_orchestration.go:121),
			// renderRecordingReplay (cmd_recording.go:254) and `claude-policy
			// distribution` (cmd_claudepolicy.go:511). Returned bare it exited 1,
			// which a script cannot tell from a usage mistake it made itself.
			if derr := res.decode(&body); derr != nil {
				return exitcode.New(exitcode.Server, derr)
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(body.Probes) == 0 {
					_, werr := fmt.Fprintln(out, "this build carries no probes for that suite")
					return werr
				}
				if _, werr := fmt.Fprintf(out, "%d probe(s)\n", body.Total); werr != nil {
					return werr
				}
				return writeAgentExecTable(out, flags, body.Probes,
					[]string{"id", "family", "severity", "owasp", "atlas", "surface", "title"})
			}, res.jsonValue())
		},
	}
	cmd.Flags().StringVar(&suite, "suite", "", "only probes of this suite")
	return cmd
}

// ---- targets -------------------------------------------------------------------

var redteamTargetColumns = []string{
	"id", "agent_ref", "name", "endpoint", "scope", "authorized", "authorized_by", "authorized_at", "status",
}

func newRedteamTargetsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "targets",
		Short: "Register agents as red-team targets and grant or withdraw consent",
		Long: "A target is an agent in YOUR inventory that may be probed. Registering it is\n" +
			"not consent: `authorize` is the separate decision that lets a run touch it, and\n" +
			"`revoke` withdraws it again.",
		Example: "  olivares redteam targets ls --status authorized",
	}
	cmd.AddCommand(
		newRedteamTargetsListCmd(flags),
		newRedteamTargetsGetCmd(flags),
		newRedteamTargetsRegisterCmd(flags),
		newRedteamTargetsAuthorizeCmd(flags),
		newRedteamTargetsRevokeCmd(flags),
	)
	return cmd
}

func newRedteamTargetsListCmd(flags *authClientFlags) *cobra.Command {
	var (
		page   agentExecPageFlags
		status string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List registered red-team targets and their consent state",
		Long:    "ls lists targets. `authorized` is the column that decides whether a run may touch one; `authorized_by` and `authorized_at` say who consented and when.",
		Example: "  olivares redteam targets ls\n  olivares redteam targets ls --status authorized --limit 50",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "status", status)
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodGet, path: "/targets", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no red-team targets registered", redteamTargetColumns)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&status, "status", "", "only targets in this status")
	return cmd
}

func newRedteamTargetsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one target and its consent record",
		Long:    "get returns one target with the full consent record: who authorized it, when, and for what scope.",
		Example: "  olivares redteam targets get rt-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodGet, path: "/targets/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newRedteamTargetsRegisterCmd(flags *authClientFlags) *cobra.Command {
	var (
		agentRef string
		name     string
		endpoint string
		scope    string
	)
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register an agent from your inventory as a red-team target",
		Long: "register records a target. IT DOES NOT AUTHORIZE ANYTHING: the target starts\n" +
			"unauthorized and the engine refuses every run against it until `authorize`.\n\n" +
			"--agent-ref must resolve to an agent in THIS tenant's inventory. Anything else\n" +
			"is refused with exit 2 and the engine's own words — only governed agents in\n" +
			"your own estate can be red-teamed.",
		Example: "  olivares redteam targets register --agent-ref agent-a --name \"support bot\"\n" +
			"  olivares redteam targets register --agent-ref agent-a --name \"support bot\" --endpoint https://agent.internal --scope staging",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{"agent_ref": agentRef, "name": name}
			if endpoint != "" {
				body["endpoint"] = endpoint
			}
			if scope != "" {
				body["scope"] = scope
			}
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodPost, path: "/targets", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if rerr := renderAgentExecObject(cmd, flags, res, redteamTargetColumns); rerr != nil {
				return rerr
			}
			_, err = fmt.Fprintln(cmd.ErrOrStderr(),
				"registered, NOT authorized: no probe will run until `redteam targets authorize` consents")
			return err
		},
	}
	cmd.Flags().StringVar(&agentRef, "agent-ref", "", "an agent in this tenant's inventory (required)")
	cmd.Flags().StringVar(&name, "name", "", "human name for the target (required)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "where the target is reachable")
	cmd.Flags().StringVar(&scope, "scope", "", "the scope consent will be limited to")
	_ = cmd.MarkFlagRequired("agent-ref")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newRedteamTargetsAuthorizeCmd(flags *authClientFlags) *cobra.Command {
	var (
		scope string
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "authorize <id>",
		Short: "Consent to red-teaming this target (confirmed; needs --yes when unattended)",
		Long: "authorize is the consent ceremony. After it, the battery may run real\n" +
			"adversarial probes against a live agent — that is the dual-use line this\n" +
			"product draws, so the CLI asks before crossing it.\n\n" +
			"The confirmation guards against an UNATTENDED invocation reaching this verb: a\n" +
			"pipe is answered by EOF, which is not consent. It is not proof a human is\n" +
			"present — that belongs to the control plane's approval path — but a cron job\n" +
			"does not authorize an attack by accident.\n\n" +
			"--scope narrows what the consent covers. Withdraw it with `targets revoke`.",
		Example: "  olivares redteam targets authorize rt-1 --yes\n" +
			"  olivares redteam targets authorize rt-1 --scope staging --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			what := fmt.Sprintf("authorize adversarial red-team probing of target %s", safeCLIValue(args[0], ""))
			if scope != "" {
				what += fmt.Sprintf(" within scope %s", safeCLIValue(scope, ""))
			}
			if err := confirmDestructive(cmd, yes, what); err != nil {
				return err
			}
			return redteamSetAuthorization(cmd, flags, args[0], true, scope)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "limit the consent to this scope")
	addYesFlag(cmd, &yes)
	return cmd
}

func newRedteamTargetsRevokeCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Withdraw consent to red-team this target",
		Long: "revoke withdraws consent. Every subsequent run against the target is refused,\n" +
			"and a run already in flight discards its assessment rather than scoring it.\n\n" +
			"It needs no --yes: refusing consent is the safe direction, and a ceremony in\n" +
			"front of it would only make stopping an attack harder than starting one.",
		Example: "  olivares redteam targets revoke rt-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return redteamSetAuthorization(cmd, flags, args[0], false, "")
		},
	}
	return cmd
}

// redteamSetAuthorization posts the consent decision. Both directions go through
// one function so the two commands cannot drift into sending different shapes.
func redteamSetAuthorization(cmd *cobra.Command, flags *authClientFlags, id string, authorized bool, scope string) error {
	body := map[string]any{"authorized": authorized}
	if scope != "" {
		body["scope"] = scope
	}
	res, err := agentExecCall{
		flags: flags, module: redteamModule,
		method: http.MethodPost, path: "/targets/" + agentExecPathID(id) + "/authorize", body: body,
	}.do(cmd)
	if err != nil {
		return err
	}
	return renderAgentExecObject(cmd, flags, res, redteamTargetColumns)
}

// ---- runs ----------------------------------------------------------------------

var redteamRunColumns = []string{
	"id", "target_ref", "suite", "status", "total", "passed", "failed",
	"errors", "skipped", "score", "started_at", "finished_at",
}

func newRedteamRunsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runs",
		Short:   "Launch and inspect scored red-team runs",
		Long:    "A run is one execution of the battery against one authorized target, scored per attack family and mapped to OWASP failures.",
		Example: "  olivares redteam runs ls --target-ref rt-1",
	}
	cmd.AddCommand(
		newRedteamRunsListCmd(flags),
		newRedteamRunsGetCmd(flags),
		newRedteamRunsResultsCmd(flags),
		newRedteamRunsLaunchCmd(flags),
	)
	return cmd
}

func newRedteamRunsListCmd(flags *authClientFlags) *cobra.Command {
	var (
		page      agentExecPageFlags
		targetRef string
		suite     string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List red-team runs and their scores",
		Long:    "ls lists runs newest-first with their pass/fail counters and aggregate score.",
		Example: "  olivares redteam runs ls\n  olivares redteam runs ls --target-ref rt-1 --suite injection --limit 20",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "target_ref", targetRef)
			setQuery(q, "suite", suite)
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodGet, path: "/runs", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no red-team runs recorded", redteamRunColumns)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&targetRef, "target-ref", "", "only runs against this target")
	cmd.Flags().StringVar(&suite, "suite", "", "only runs of this suite")
	return cmd
}

func newRedteamRunsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one run's scorecard",
		Long:    "get returns the run with its per-family scores and OWASP failure counts. Use -o json for the full breakdown.",
		Example: "  olivares redteam runs get run-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodGet, path: "/runs/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newRedteamRunsResultsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "results <id>",
		Short: "List one run's per-probe results",
		Long: "results returns every probe's outcome for one run, with the OWASP and ATLAS\n" +
			"identifiers it maps to.\n\n" +
			"It is NOT paginated by the engine: the whole result set comes back in one\n" +
			"answer.",
		Example: "  olivares redteam runs results run-1\n  olivares redteam runs results run-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodGet, path: "/runs/" + agentExecPathID(args[0]) + "/results",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this run recorded no probe results",
				[]string{"probe_id", "family", "outcome", "severity", "owasp", "atlas"})
		},
	}
}

func newRedteamRunsLaunchCmd(flags *authClientFlags) *cobra.Command {
	var (
		targetRef string
		suite     string
	)
	cmd := &cobra.Command{
		Use:   "launch",
		Short: "Run the battery against an authorized target",
		Long: "launch runs the probes and records a scorecard.\n\n" +
			"It carries no confirmation of its own, and that is deliberate: this is the\n" +
			"verb CI calls, and the decision that matters was already made and recorded by\n" +
			"`targets authorize`. An unauthorized target is refused (exit 3), and consent\n" +
			"withdrawn mid-run discards the assessment instead of scoring it.",
		Example: "  olivares redteam runs launch --target-ref rt-1\n" +
			"  olivares redteam runs launch --target-ref rt-1 --suite injection",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{"target_ref": targetRef}
			if suite != "" {
				body["suite"] = suite
			}
			res, err := agentExecCall{
				flags: flags, module: redteamModule,
				method: http.MethodPost, path: "/runs", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, redteamRunColumns)
		},
	}
	cmd.Flags().StringVar(&targetRef, "target-ref", "", "the authorized target to probe (required)")
	cmd.Flags().StringVar(&suite, "suite", "", "run only this suite of the battery")
	_ = cmd.MarkFlagRequired("target-ref")
	return cmd
}
