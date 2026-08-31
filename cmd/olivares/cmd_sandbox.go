// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_sandbox.go drives the isolated agent sandbox (/v1/m/sandbox,
// modules/sandbox/sandbox.go): operator-authored scenarios, deterministic
// replays of a historical session, and the A/B comparison that gates a deploy.
//
// THE ONE DESTRUCTIVE VERB IS `scenarios archive`, and it is a POST. That is
// worth saying plainly, because counting DELETEs in this family finds ZERO and
// concludes — wrongly — that nothing here is irreversible. Archiving retires a
// fixture other people's comparisons are pinned to, so it goes through
// confirmDestructive like every other irreversible verb in this CLI: --yes, an
// interactive terminal, or a refusal.

const sandboxModule = "sandbox"

func newSandboxCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "sandbox",
		Short: "Run agents against synthetic scenarios and compare two variants",
		Long: "sandbox exercises the isolated evaluation plane: synthetic scenarios an\n" +
			"operator authors, deterministic replays of a recorded session against supplied\n" +
			"mocks, and the A/B comparison whose verdict is the evidence a deploy decision\n" +
			"is made on.\n\n" +
			"A replay with no reconstructable timeline is reported DEGRADED — zero steps,\n" +
			"never a fabricated pass.",
		Example: "  olivares sandbox scenarios ls\n" +
			"  olivares sandbox scenarios run sc-1 --variant candidate\n" +
			"  olivares sandbox compare --scenario-ref sc-1 --baseline-variant v1 --candidate-variant v2",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newSandboxScenariosCmd(&flags),
		newSandboxRunsCmd(&flags),
		newSandboxReplayCmd(&flags),
		newSandboxCompareCmd(&flags),
		newSandboxComparisonsCmd(&flags),
	)
	return root
}

// ---- scenarios --------------------------------------------------------------

func newSandboxScenariosCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "scenarios",
		Short:   "Author, inspect, run and archive sandbox scenarios",
		Long:    "A scenario is a synthetic fixture: the steps to drive an agent through and the mocks its tools answer with. Its spec_hash makes an identical fixture recognizable.",
		Example: "  olivares sandbox scenarios ls --status active",
	}
	cmd.AddCommand(
		newSandboxScenariosListCmd(flags),
		newSandboxScenariosGetCmd(flags),
		newSandboxScenariosCreateCmd(flags),
		newSandboxScenariosArchiveCmd(flags),
		newSandboxScenariosRunCmd(flags),
	)
	return cmd
}

var sandboxScenarioColumns = []string{"id", "name", "subject_kind", "status", "spec_hash"}

func newSandboxScenariosListCmd(flags *authClientFlags) *cobra.Command {
	var (
		page   agentExecPageFlags
		status string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List the tenant's scenarios",
		Long:    "ls lists authored scenarios. --status narrows to one lifecycle state server-side.",
		Example: "  olivares sandbox scenarios ls\n  olivares sandbox scenarios ls --status archived --limit 50",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "status", status)
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodGet, path: "/scenarios", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no scenarios authored", sandboxScenarioColumns)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&status, "status", "", "only scenarios in this status")
	return cmd
}

func newSandboxScenariosGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one scenario with its steps and mocks",
		Long:    "get returns the whole fixture. Use -o json to capture the steps and mocks for editing.",
		Example: "  olivares sandbox scenarios get sc-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodGet, path: "/scenarios/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newSandboxScenariosCreateCmd(flags *authClientFlags) *cobra.Command {
	var (
		name        string
		description string
		subjectKind string
		stepsFile   string
		mocksFile   string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Author a scenario from JSON step and mock files",
		Long: "create records a synthetic fixture. --steps-file and --mocks-file each take a\n" +
			"JSON ARRAY ('-' reads stdin, and only one of them may read it). A name already\n" +
			"in use is a conflict (exit 5), not a silent second copy.",
		Example: "  olivares sandbox scenarios create --name checkout --steps-file steps.json\n" +
			"  olivares sandbox scenarios create --name checkout --steps-file steps.json --mocks-file mocks.json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if stepsFile == "-" && mocksFile == "-" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--steps-file and --mocks-file cannot both read stdin: give at least one a path"))
			}
			body := map[string]any{"name": name}
			if description != "" {
				body["description"] = description
			}
			if subjectKind != "" {
				body["subject_kind"] = subjectKind
			}
			if stepsFile != "" {
				steps, err := readAgentExecJSONArray(cmd, stepsFile, "step")
				if err != nil {
					return err
				}
				body["steps"] = steps
			}
			if mocksFile != "" {
				mocks, err := readAgentExecJSONArray(cmd, mocksFile, "mock")
				if err != nil {
					return err
				}
				body["mocks"] = mocks
			}
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodPost, path: "/scenarios", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, sandboxScenarioColumns)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "scenario name (required, unique per tenant)")
	cmd.Flags().StringVar(&description, "description", "", "what the fixture exercises")
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "", "what kind of subject the scenario drives")
	cmd.Flags().StringVar(&stepsFile, "steps-file", "", "JSON array of step objects, '-' for stdin")
	cmd.Flags().StringVar(&mocksFile, "mocks-file", "", "JSON array of mock objects, '-' for stdin")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newSandboxScenariosArchiveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "archive <id>",
		Short: "Archive a scenario (destructive; needs --yes when unattended)",
		Long: "archive retires a fixture. It is a POST, not a DELETE, and it is still\n" +
			"irreversible: comparisons and runs recorded against this scenario keep pointing\n" +
			"at a fixture nobody can run again.\n\n" +
			"So it is gated like every destructive verb in this CLI: --yes, an interactive\n" +
			"terminal that answers y, or a refusal. A pipe is not consent.",
		Example: "  olivares sandbox scenarios archive sc-1 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes,
				fmt.Sprintf("archive sandbox scenario %s", safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodPost, path: "/scenarios/" + agentExecPathID(args[0]) + "/archive",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, sandboxScenarioColumns)
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func newSandboxScenariosRunCmd(flags *authClientFlags) *cobra.Command {
	var (
		variant  string
		suiteRef string
	)
	cmd := &cobra.Command{
		Use:   "run <id>",
		Short: "Run a scenario against the isolated runner (synchronous)",
		Long: "run drives the scenario's steps through the isolated runner and persists the\n" +
			"run with its per-step outputs. It is synchronous: the command returns the\n" +
			"completed run.\n\n" +
			"With --suite-ref AND a scorer wired, the outputs are also scored. Without a\n" +
			"scorer the run still records its deterministic outputs_hash — the run is\n" +
			"reported unscored, never scored as a pass.",
		Example: "  olivares sandbox scenarios run sc-1\n" +
			"  olivares sandbox scenarios run sc-1 --variant candidate --suite-ref safety-v1",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if variant != "" {
				body["variant"] = variant
			}
			if suiteRef != "" {
				body["suite_ref"] = suiteRef
			}
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodPost, path: "/scenarios/" + agentExecPathID(args[0]) + "/run", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, sandboxRunColumns)
		},
	}
	cmd.Flags().StringVar(&variant, "variant", "", "label this run's variant (used by compare)")
	cmd.Flags().StringVar(&suiteRef, "suite-ref", "", "score the outputs against this evals suite")
	return cmd
}

// ---- runs --------------------------------------------------------------------

var sandboxRunColumns = []string{
	"id", "kind", "scenario_ref", "variant", "status", "steps_total", "steps_ok",
	"steps_error", "score", "passed", "outputs_hash",
}

func newSandboxRunsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runs",
		Short:   "Inspect sandbox runs, their outputs and their live stream",
		Long:    "A run is one execution of a scenario or one replay of a recorded session, with its per-step outputs and deterministic outputs_hash.",
		Example: "  olivares sandbox runs ls --kind replay",
	}
	cmd.AddCommand(
		newSandboxRunsListCmd(flags),
		newSandboxRunsGetCmd(flags),
		newSandboxRunsOutputsCmd(flags),
		newSandboxRunsStreamCmd(flags),
	)
	return cmd
}

func newSandboxRunsListCmd(flags *authClientFlags) *cobra.Command {
	var (
		page        agentExecPageFlags
		kind        string
		scenarioRef string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List sandbox runs",
		Long:    "ls lists runs newest-first. --kind separates scenario runs from replays; --scenario-ref narrows to one fixture.",
		Example: "  olivares sandbox runs ls --limit 20\n  olivares sandbox runs ls --scenario-ref sc-1 --kind scenario",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "kind", kind)
			setQuery(q, "scenario_ref", scenarioRef)
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodGet, path: "/runs", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no sandbox runs recorded", sandboxRunColumns)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&kind, "kind", "", "only runs of this kind")
	cmd.Flags().StringVar(&scenarioRef, "scenario-ref", "", "only runs of this scenario")
	return cmd
}

func newSandboxRunsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one run",
		Long:    "get returns one run's status, step counters, score (when scored) and outputs_hash.",
		Example: "  olivares sandbox runs get run-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodGet, path: "/runs/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newSandboxRunsOutputsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "outputs <id>",
		Short: "List one run's per-step outputs",
		Long: "outputs returns every step's recorded output. It is NOT paginated by the\n" +
			"engine: the whole set comes back in one answer.",
		Example: "  olivares sandbox runs outputs run-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodGet, path: "/runs/" + agentExecPathID(args[0]) + "/outputs",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this run recorded no outputs",
				[]string{"step_index", "step_ref", "status", "output_hash", "duration_ms"})
		},
	}
}

func newSandboxRunsStreamCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stream <id>",
		Short: "Follow a live run as NDJSON (one object per event)",
		Long: "stream follows the engine's server-sent run events and prints ONE JSON OBJECT\n" +
			"PER LINE on stdout. Keep-alives and notices go to stderr.",
		Example: "  olivares sandbox runs stream run-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return streamAgentExecEvents(cmd, flags, sandboxModule,
				"/runs/"+agentExecPathID(args[0])+"/stream", nil)
		},
	}
}

// ---- replay and compare --------------------------------------------------------

func newSandboxReplayCmd(flags *authClientFlags) *cobra.Command {
	var (
		sessionRef string
		mocksFile  string
		suiteRef   string
	)
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Deterministically re-execute a recorded session against supplied mocks",
		Long: "replay reconstructs a historical session's input timeline and re-executes it\n" +
			"against the mocks you supply. The same session_ref and the same mocks always\n" +
			"produce identical outputs — the runner is pure.\n\n" +
			"A session with no reconstructable timeline is reported DEGRADED with zero\n" +
			"steps. That is the honest answer and it is never dressed up as a pass.",
		Example: "  olivares sandbox replay --session-ref sess-1\n" +
			"  olivares sandbox replay --session-ref sess-1 --mocks-file mocks.json --suite-ref safety-v1",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{"session_ref": sessionRef}
			if mocksFile != "" {
				mocks, err := readAgentExecJSONArray(cmd, mocksFile, "mock")
				if err != nil {
					return err
				}
				body["mocks"] = mocks
			}
			if suiteRef != "" {
				body["suite_ref"] = suiteRef
			}
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodPost, path: "/replay", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, sandboxRunColumns)
		},
	}
	cmd.Flags().StringVar(&sessionRef, "session-ref", "", "the recorded session to replay (required)")
	cmd.Flags().StringVar(&mocksFile, "mocks-file", "", "JSON array of mock objects, '-' for stdin")
	cmd.Flags().StringVar(&suiteRef, "suite-ref", "", "score the replayed outputs against this evals suite")
	_ = cmd.MarkFlagRequired("session-ref")
	return cmd
}

func newSandboxCompareCmd(flags *authClientFlags) *cobra.Command {
	var (
		scenarioRef string
		sessionRef  string
		baseline    string
		candidate   string
		suiteRef    string
	)
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Run the same scenario as two variants and record the verdict",
		Long: "compare executes the SAME steps as a baseline and a candidate variant, scores\n" +
			"both (or compares their deterministic outputs_hash when no scorer is wired) and\n" +
			"persists an append-only comparison — the evidence a deploy decision is made on.\n\n" +
			"Give it either --scenario-ref or --session-ref as the source of the steps.",
		Example: "  olivares sandbox compare --scenario-ref sc-1 --baseline-variant v1 --candidate-variant v2\n" +
			"  olivares sandbox compare --session-ref sess-1 --baseline-variant v1 --candidate-variant v2 --suite-ref safety-v1",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (scenarioRef == "") == (sessionRef == "") {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"give exactly one source of steps: --scenario-ref or --session-ref"))
			}
			body := map[string]any{
				"baseline_variant":  baseline,
				"candidate_variant": candidate,
			}
			if scenarioRef != "" {
				body["scenario_ref"] = scenarioRef
			}
			if sessionRef != "" {
				body["session_ref"] = sessionRef
			}
			if suiteRef != "" {
				body["suite_ref"] = suiteRef
			}
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodPost, path: "/compare", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, sandboxComparisonColumns)
		},
	}
	cmd.Flags().StringVar(&scenarioRef, "scenario-ref", "", "compare using this scenario's steps")
	cmd.Flags().StringVar(&sessionRef, "session-ref", "", "compare using this recorded session's steps")
	cmd.Flags().StringVar(&baseline, "baseline-variant", "", "the variant label to treat as the baseline (required)")
	cmd.Flags().StringVar(&candidate, "candidate-variant", "", "the variant label to treat as the candidate (required)")
	cmd.Flags().StringVar(&suiteRef, "suite-ref", "", "score both runs against this evals suite")
	_ = cmd.MarkFlagRequired("baseline-variant")
	_ = cmd.MarkFlagRequired("candidate-variant")
	return cmd
}

var sandboxComparisonColumns = []string{
	"id", "scenario_ref", "verdict", "baseline_score", "candidate_score", "delta",
	"baseline_run_ref", "candidate_run_ref", "occurred_at",
}

func newSandboxComparisonsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "comparisons",
		Short:   "Inspect the append-only A/B comparison ledger",
		Long:    "A comparison is the recorded verdict of one baseline-vs-candidate run pair. It is append-only: it is the evidence a deploy decision cited.",
		Example: "  olivares sandbox comparisons ls --verdict regression",
	}
	cmd.AddCommand(
		newSandboxComparisonsListCmd(flags),
		newSandboxComparisonsGetCmd(flags),
	)
	return cmd
}

func newSandboxComparisonsListCmd(flags *authClientFlags) *cobra.Command {
	var (
		page        agentExecPageFlags
		scenarioRef string
		verdict     string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List recorded comparisons",
		Long:    "ls lists the comparison ledger, optionally narrowed to one scenario or one verdict.",
		Example: "  olivares sandbox comparisons ls\n  olivares sandbox comparisons ls --scenario-ref sc-1 --verdict regression",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "scenario_ref", scenarioRef)
			setQuery(q, "verdict", verdict)
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodGet, path: "/comparisons", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no comparisons recorded", sandboxComparisonColumns)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&scenarioRef, "scenario-ref", "", "only comparisons of this scenario")
	cmd.Flags().StringVar(&verdict, "verdict", "", "only comparisons with this verdict")
	return cmd
}

func newSandboxComparisonsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one comparison",
		Long:    "get returns one comparison with both run references, both scores and the delta.",
		Example: "  olivares sandbox comparisons get cmp-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodGet, path: "/comparisons/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}
