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

// cmd_orchestration.go drives the multi-agent orchestration plane
// (/v1/m/orchestration, modules/orchestration/api.go) from a terminal: the live
// communication graph, governed schedules and the DAG workflows.
//
// IT IS THE FAMILY THAT FIXES PARTIAL UPDATE FOR THE WHOLE LOT. Of the ninety-one
// routes in this batch it owns the only two PATCHes, so `schedules update` and
// `workflows update` are where the rule is stated and tested: a field you did not
// name on the command line is NOT SENT. The engine's patch DTOs are
// pointer-per-field precisely so an omitted key leaves the stored value alone
// (schedules.go:89), and a client that always serialized every field would turn
// `--desired-status paused` into "paused, and also reset the cadence to empty".
//
// TWO-PHASE ACTUATION. `schedules fire` and `workflows run` are governed: with no
// --approval-ref the engine OPENS an approval and answers 202, having actuated
// nothing. That is exit 7, not 0 — see reportAgentExecPending.

const orchestrationModule = "orchestration"

func newOrchestrationCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "orchestration",
		Short: "Inspect the agent communication graph and operate governed schedules and workflows",
		Long: "orchestration exercises the multi-agent coordination plane from a terminal:\n" +
			"the live supervisor→worker graph, the governed schedules that decide when an\n" +
			"agent runs, and the DAG workflows that chain several of them.\n\n" +
			"The actuating verbs are two-phase on purpose. `schedules fire` and `workflows\n" +
			"run` without --approval-ref ask governance for a decision and actuate NOTHING;\n" +
			"they exit 7 and print the approval reference to re-run with. Only a call\n" +
			"carrying an approved reference dispatches.\n\n" +
			"Partial updates send only the flags you type: `schedules update` and\n" +
			"`workflows update` never clobber a field you did not name.",
		Example: "  olivares orchestration graph --link-kind delegation\n" +
			"  olivares orchestration schedules ls -o json\n" +
			"  olivares orchestration schedules update sc-1 --desired-status paused\n" +
			"  olivares orchestration workflows run wf-1 --approval-ref ap-9",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newOrchestrationGraphCmd(&flags),
		newOrchestrationNeighborsCmd(&flags),
		newOrchestrationFlowsCmd(&flags),
		newOrchestrationTimelineCmd(&flags),
		newOrchestrationStreamCmd(&flags),
		newOrchestrationDecisionsCmd(&flags),
		newOrchestrationSchedulesCmd(&flags),
		newOrchestrationWorkflowsCmd(&flags),
	)
	return root
}

// ---- the communication graph ---------------------------------------------------

func newOrchestrationGraphCmd(flags *authClientFlags) *cobra.Command {
	var (
		page       agentExecPageFlags
		supervisor string
		worker     string
		linkKind   string
	)
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "List the live agent→agent relations (a privileged, self-audited read)",
		Long: "graph lists the delegation/communication edges the engine has observed, with\n" +
			"each edge's derived schedule health. Reading it is privileged and the engine\n" +
			"audits the read.",
		Example: "  olivares orchestration graph\n" +
			"  olivares orchestration graph --supervisor agent-a --link-kind delegation --limit 50",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "supervisor", supervisor)
			setQuery(q, "worker", worker)
			setQuery(q, "link_kind", linkKind)
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/graph", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecGraph(cmd, flags, res, "no agent relations observed yet")
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&supervisor, "supervisor", "", "only edges whose supervisor is this agent ref")
	cmd.Flags().StringVar(&worker, "worker", "", "only edges whose worker is this agent ref")
	cmd.Flags().StringVar(&linkKind, "link-kind", "", "only edges of this link kind")
	return cmd
}

// renderAgentExecGraph renders the graph envelope, which is NOT the standard list
// envelope: it carries `edges` (plus derived node state) rather than `items`.
func renderAgentExecGraph(cmd *cobra.Command, flags *authClientFlags, res agentExecResult, emptyNote string) error {
	var body struct {
		Edges   []json.RawMessage `json:"edges"`
		Cursor  string            `json:"cursor"`
		HasMore bool              `json:"has_more"`
	}
	if err := res.decode(&body); err != nil {
		return exitcode.New(exitcode.Server, err)
	}
	wrapped := agentExecResult{status: res.status, raw: res.raw}
	rerr := renderOut(cmd, func(out io.Writer) error {
		if len(body.Edges) == 0 {
			_, err := fmt.Fprintln(out, emptyNote)
			return err
		}
		return writeAgentExecTable(out, flags, body.Edges,
			[]string{"supervisor_ref", "worker_ref", "link_kind", "state", "last_seen_at"})
	}, json.RawMessage(wrapped.raw))
	if rerr != nil {
		return rerr
	}
	if body.HasMore || body.Cursor != "" {
		note := "more edges remain"
		if body.Cursor != "" {
			note = fmt.Sprintf("more edges remain; continue with --cursor %s", safeCLIValue(body.Cursor, ""))
		}
		_, err := fmt.Fprintln(cmd.ErrOrStderr(), note)
		return err
	}
	return nil
}

func newOrchestrationNeighborsCmd(flags *authClientFlags) *cobra.Command {
	var direction string
	cmd := &cobra.Command{
		Use:   "neighbors <node>",
		Short: "Show the subgraph around one agent (incoming, outgoing or both)",
		Long: "neighbors returns every relation touching one node. It is NOT paginated by the\n" +
			"engine: the whole neighborhood comes back in one answer, so there is no\n" +
			"--cursor to continue from.",
		Example: "  olivares orchestration neighbors agent-a\n" +
			"  olivares orchestration neighbors agent-a --direction incoming",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch direction {
			case "both", "incoming", "outgoing":
			default:
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("invalid --direction %q (use incoming, outgoing or both)", direction))
			}
			q := url.Values{}
			q.Set("node", args[0])
			q.Set("direction", direction)
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/graph/neighbors", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecGraph(cmd, flags, res, "this node has no observed relations")
		},
	}
	cmd.Flags().StringVar(&direction, "direction", "both", "incoming, outgoing or both")
	return cmd
}

func newOrchestrationFlowsCmd(flags *authClientFlags) *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "flows",
		Short: "List the derived multi-agent flows and their lifecycle state",
		Long: "flows clusters the relation graph into supervisor→workers flows and derives each\n" +
			"one's state at read time. It is NOT paginated by the engine: every flow is\n" +
			"returned, and --state filters them server-side.",
		Example: "  olivares orchestration flows\n" +
			"  olivares orchestration flows --state stalled",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			setQuery(q, "state", state)
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/flows", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no multi-agent flows derived yet",
				[]string{"supervisor_ref", "worker_count", "state", "last_activity_at"})
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "only flows in this lifecycle state")
	return cmd
}

func newOrchestrationTimelineCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "timeline <subject>",
		Short: "Show one subject's merged delegation and fire/miss history",
		Long: "timeline merges a subject's delegation activity with its schedule decisions,\n" +
			"newest first. It is NOT paginated by the engine.",
		Example: "  olivares orchestration timeline agent-a\n" +
			"  olivares orchestration timeline agent-a -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			q.Set("subject", args[0])
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/timeline", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no orchestration history for this subject",
				[]string{"occurred_at", "kind", "op", "counterparty_ref", "detail"})
		},
	}
	return cmd
}

func newOrchestrationStreamCmd(flags *authClientFlags) *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Follow the live communication graph as NDJSON (one object per event)",
		Long: "stream follows the engine's server-sent relation events and prints ONE JSON\n" +
			"OBJECT PER LINE on stdout: {\"event\":\"relation\",\"data\":{…}}. Keep-alives and\n" +
			"notices go to stderr, so stdout stays a clean NDJSON pipe. It runs until the\n" +
			"engine closes the stream or you interrupt it.",
		Example: "  olivares orchestration stream\n" +
			"  olivares orchestration stream --node agent-a",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			setQuery(q, "node", node)
			return streamAgentExecEvents(cmd, flags, orchestrationModule, "/stream", q)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "only events touching this agent ref")
	return cmd
}

func newOrchestrationDecisionsCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:   "decisions",
		Short: "List the append-only fire/miss decision ledger for the tenant",
		Long: "decisions is the append-only record of every schedule fire, miss and refusal,\n" +
			"with the approval reference and gate status that governed it.",
		Example: "  olivares orchestration decisions --limit 100\n" +
			"  olivares orchestration decisions -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/decisions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no orchestration decisions recorded yet",
				[]string{"occurred_at", "schedule_ref", "op", "op_status", "gate_status", "approval_ref"})
		},
	}
	page.add(cmd)
	return cmd
}

// ---- schedules ------------------------------------------------------------------

func newOrchestrationSchedulesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedules",
		Short: "Declare, retarget and fire governed schedules",
		Long: "A schedule is a governed routine: what runs, on what cadence, and whether the\n" +
			"engine may fire it autonomously. Declaring one is write-tier; firing it is\n" +
			"admin-tier and passes the approval gate.",
		Example: "  olivares orchestration schedules ls\n" +
			"  olivares orchestration schedules fire sc-1",
	}
	cmd.AddCommand(
		newOrchestrationSchedulesListCmd(flags),
		newOrchestrationSchedulesGetCmd(flags),
		newOrchestrationSchedulesCreateCmd(flags),
		newOrchestrationSchedulesUpdateCmd(flags),
		newOrchestrationSchedulesFireCmd(flags),
		newOrchestrationSchedulesDecisionsCmd(flags),
		newOrchestrationSchedulesRevisionsCmd(flags),
		newOrchestrationSchedulesRestoreCmd(flags),
	)
	return cmd
}

var orchestrationScheduleColumns = []string{
	"id", "name", "subject_kind", "subject_ref", "trigger_kind", "cadence_spec",
	"desired_status", "health", "last_fired_at",
}

func newOrchestrationSchedulesListCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List the tenant's governed schedules with their derived health",
		Long:    "ls lists every declared schedule. health is derived at read time: a schedule that missed its cadence reads `stalled` even while its desired status is active.",
		Example: "  olivares orchestration schedules ls\n  olivares orchestration schedules ls --limit 20 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/schedules", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no schedules declared", orchestrationScheduleColumns)
		},
	}
	page.add(cmd)
	return cmd
}

func newOrchestrationSchedulesGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one schedule",
		Long:    "get returns one schedule's declared shape and its derived health.",
		Example: "  olivares orchestration schedules get sc-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/schedules/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newOrchestrationSchedulesCreateCmd(flags *authClientFlags) *cobra.Command {
	var (
		name        string
		subjectKind string
		subjectRef  string
		triggerKind string
		cadenceSpec string
		interval    int64
		graceFactor int64
		approvalRef string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Declare a governed schedule",
		Long: "create declares a routine. The declaring principal is frozen on the row as the\n" +
			"accountable owner, and every later patch, restore and fire resolves policy from\n" +
			"that owner's axes — a more privileged principal cannot step outside them.\n\n" +
			"--expected-interval-seconds arms the cadence-miss check and is only meaningful\n" +
			"for a cron trigger. If routine policy requires a human, the engine answers 202\n" +
			"and this command exits 7 with the approval reference to repeat with.",
		Example: "  olivares orchestration schedules create --name nightly --subject-kind agent --subject-ref agent-a --trigger-kind cron --cadence-spec \"0 2 * * *\"\n" +
			"  olivares orchestration schedules create --name nightly --subject-kind agent --subject-ref agent-a --trigger-kind cron --cadence-spec \"0 2 * * *\" --approval-ref ap-9",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{
				"name":         name,
				"subject_kind": subjectKind,
				"subject_ref":  subjectRef,
				"trigger_kind": triggerKind,
			}
			if cadenceSpec != "" {
				body["cadence_spec"] = cadenceSpec
			}
			if interval != 0 {
				body["expected_interval_seconds"] = interval
			}
			if graceFactor != 0 {
				body["grace_factor"] = graceFactor
			}
			if approvalRef != "" {
				body["approval_ref"] = approvalRef
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPost, path: "/schedules", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportAgentExecPending(cmd, res, "schedule declaration")
			}
			return renderAgentExecObject(cmd, flags, res, orchestrationScheduleColumns)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human name for the routine (required)")
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "agent", "what the schedule drives")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "the subject's reference (required)")
	cmd.Flags().StringVar(&triggerKind, "trigger-kind", "cron", "how the routine is triggered")
	cmd.Flags().StringVar(&cadenceSpec, "cadence-spec", "", "the trigger's cadence, e.g. a cron expression")
	cmd.Flags().Int64Var(&interval, "expected-interval-seconds", 0, "arm the cadence-miss check (0 disables it; cron triggers only)")
	cmd.Flags().Int64Var(&graceFactor, "grace-factor", 0, "multiple of the interval tolerated before a miss (engine default when 0)")
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes this declaration")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("subject-ref")
	return cmd
}

func newOrchestrationSchedulesUpdateCmd(flags *authClientFlags) *cobra.Command {
	var (
		desiredStatus string
		subjectRef    string
		cadenceSpec   string
		interval      int64
		graceFactor   int64
		approvalRef   string
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Partially update a schedule — only the flags you type are sent",
		Long: "update is the PATCH verb. It sends ONLY the fields you named on the command\n" +
			"line, so `--desired-status paused` pauses the routine and leaves its cadence,\n" +
			"subject and grace factor exactly as they were.\n\n" +
			"This matters more than it looks: --grace-factor 0 and an omitted --grace-factor\n" +
			"are different requests, and this command keeps them different.",
		Example: "  olivares orchestration schedules update sc-1 --desired-status paused\n" +
			"  olivares orchestration schedules update sc-1 --cadence-spec \"0 3 * * *\" --approval-ref ap-9",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			patchString(cmd, body, "desired-status", "desired_status", desiredStatus)
			patchString(cmd, body, "subject-ref", "subject_ref", subjectRef)
			patchString(cmd, body, "cadence-spec", "cadence_spec", cadenceSpec)
			patchInt64(cmd, body, "expected-interval-seconds", "expected_interval_seconds", interval)
			patchInt64(cmd, body, "grace-factor", "grace_factor", graceFactor)
			if len(body) == 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"nothing to update: name at least one field to change"))
			}
			if approvalRef != "" {
				body["approval_ref"] = approvalRef
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPatch, path: "/schedules/" + agentExecPathID(args[0]), body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportAgentExecPending(cmd, res, "schedule update")
			}
			return renderAgentExecObject(cmd, flags, res, orchestrationScheduleColumns)
		},
	}
	cmd.Flags().StringVar(&desiredStatus, "desired-status", "", "active, paused or retired")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "retarget the routine at another subject")
	cmd.Flags().StringVar(&cadenceSpec, "cadence-spec", "", "replace the cadence expression")
	cmd.Flags().Int64Var(&interval, "expected-interval-seconds", 0, "replace the cadence-miss window (0 disables the check)")
	cmd.Flags().Int64Var(&graceFactor, "grace-factor", 0, "replace the grace factor")
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes this change")
	return cmd
}

func newOrchestrationSchedulesFireCmd(flags *authClientFlags) *cobra.Command {
	var approvalRef string
	cmd := &cobra.Command{
		Use:   "fire <id>",
		Short: "Fire a schedule now, through the approval gate (two-phase)",
		Long: "fire actuates a routine on demand. It is two-phase and deny-closed:\n\n" +
			"  without --approval-ref  the engine opens an approval and dispatches NOTHING.\n" +
			"                          This command exits 7 and prints the reference.\n" +
			"  with --approval-ref     the engine consumes the decision and dispatches only\n" +
			"                          if it is an explicit approval bound to this plan.\n\n" +
			"An estate kill switch outranks an approval: a stopped scope answers 423 and\n" +
			"this command exits 5, having actuated nothing.",
		Example: "  olivares orchestration schedules fire sc-1\n" +
			"  olivares orchestration schedules fire sc-1 --approval-ref ap-9",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			call := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPost, path: "/schedules/" + agentExecPathID(args[0]) + "/fire",
			}
			if approvalRef != "" {
				call.body = map[string]any{"approval_ref": approvalRef}
			}
			res, err := call.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportAgentExecPending(cmd, res, "fire")
			}
			return renderAgentExecObject(cmd, flags, res,
				[]string{"op", "op_status", "gate_status", "approval_ref", "dispatch_ref", "plan_hash", "detail"})
		},
	}
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes this fire")
	return cmd
}

func newOrchestrationSchedulesDecisionsCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "decisions <id>",
		Short:   "List one schedule's append-only fire/miss ledger",
		Long:    "decisions narrows the tenant ledger to a single schedule.",
		Example: "  olivares orchestration schedules decisions sc-1 --limit 50",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/schedules/" + agentExecPathID(args[0]) + "/decisions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this schedule has no recorded decisions",
				[]string{"occurred_at", "op", "op_status", "gate_status", "approval_ref", "dispatch_ref"})
		},
	}
	page.add(cmd)
	return cmd
}

func newOrchestrationSchedulesRevisionsCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "revisions <id>",
		Short:   "List a schedule's revision history",
		Long:    "revisions lists every recorded shape of the routine, newest first, so one of them can be restored by id.",
		Example: "  olivares orchestration schedules revisions sc-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/schedules/" + agentExecPathID(args[0]) + "/revisions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this schedule has no recorded revisions",
				[]string{"id", "created_at", "author", "desired_status", "cadence_spec", "subject_ref"})
		},
	}
	page.add(cmd)
	return cmd
}

func newOrchestrationSchedulesRestoreCmd(flags *authClientFlags) *cobra.Command {
	var (
		revisionID  string
		approvalRef string
	)
	cmd := &cobra.Command{
		Use:   "restore <id>",
		Short: "Re-apply an earlier revision of a schedule",
		Long: "restore replays an earlier shape through the patch verb's exact application\n" +
			"path — same validation, same policy resolution, same consequences. If the\n" +
			"restored shape re-activates a routine that policy gates, the engine answers 202\n" +
			"and this command exits 7.",
		Example: "  olivares orchestration schedules restore sc-1 --revision rev-3\n" +
			"  olivares orchestration schedules restore sc-1 --revision rev-3 --approval-ref ap-9",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"revision_id": revisionID}
			if approvalRef != "" {
				body["approval_ref"] = approvalRef
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPost, path: "/schedules/" + agentExecPathID(args[0]) + "/restore", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportAgentExecPending(cmd, res, "schedule restore")
			}
			return renderAgentExecObject(cmd, flags, res, orchestrationScheduleColumns)
		},
	}
	cmd.Flags().StringVar(&revisionID, "revision", "", "the revision id to re-apply (required)")
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes the restore")
	_ = cmd.MarkFlagRequired("revision")
	return cmd
}

// ---- workflows -------------------------------------------------------------------

func newOrchestrationWorkflowsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Author, dry-run and execute DAG workflows",
		Long: "A workflow is an approved step graph. Editing the graph is write-tier, a\n" +
			"dry-run is a read, and running it is admin-tier through the approval gate.\n" +
			"The graph is edited, validated, hashed and approved AS ONE UNIT: `set-steps`\n" +
			"replaces the whole step list, because a step-by-step edit would let an\n" +
			"approved plan hash drift.",
		Example: "  olivares orchestration workflows ls\n" +
			"  olivares orchestration workflows dry-run wf-1",
	}
	cmd.AddCommand(
		newOrchestrationWorkflowsListCmd(flags),
		newOrchestrationWorkflowsGetCmd(flags),
		newOrchestrationWorkflowsCreateCmd(flags),
		newOrchestrationWorkflowsUpdateCmd(flags),
		newOrchestrationWorkflowsSetStepsCmd(flags),
		newOrchestrationWorkflowsRevisionsCmd(flags),
		newOrchestrationWorkflowsRestoreCmd(flags),
		newOrchestrationWorkflowsDryRunCmd(flags),
		newOrchestrationWorkflowsRunCmd(flags),
		newOrchestrationWorkflowsRunsCmd(flags),
	)
	return cmd
}

var orchestrationWorkflowColumns = []string{
	"id", "name", "enabled", "version", "step_count", "plan_hash", "owner_actor", "updated_at",
}

func newOrchestrationWorkflowsListCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List the tenant's workflows",
		Long:    "ls returns the workflow list shape — names, versions and plan hashes, without the step graphs.",
		Example: "  olivares orchestration workflows ls --limit 20",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/workflows", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no workflows declared", orchestrationWorkflowColumns)
		},
	}
	page.add(cmd)
	return cmd
}

func newOrchestrationWorkflowsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one workflow with its full step graph",
		Long:    "get returns the workflow detail shape, including the canonical step graph the plan hash is computed over. Use -o json to feed the graph back into set-steps.",
		Example: "  olivares orchestration workflows get wf-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/workflows/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newOrchestrationWorkflowsCreateCmd(flags *authClientFlags) *cobra.Command {
	var (
		name        string
		description string
		stepsFile   string
		enabled     bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Declare a workflow from a JSON step graph",
		Long: "create declares a workflow. --steps-file takes a JSON ARRAY of step objects\n" +
			"('-' reads stdin); the engine validates the graph and rejects a cycle, a dangling\n" +
			"reference or an over-large graph before anything is stored.\n\n" +
			"The file is checked for JSON validity here so a typo is a usage error (exit 2)\n" +
			"before a request is sent, not a 400 you have to interpret.",
		Example: "  olivares orchestration workflows create --name nightly --steps-file steps.json\n" +
			"  olivares orchestration workflows get wf-1 -o json | olivares orchestration workflows create --name copy --steps-file -",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			steps, err := readAgentExecJSONArray(cmd, stepsFile, "step")
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "steps": steps}
			if description != "" {
				body["description"] = description
			}
			if cmd.Flags().Changed("enabled") {
				body["enabled"] = enabled
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPost, path: "/workflows", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, orchestrationWorkflowColumns)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human name for the workflow (required)")
	cmd.Flags().StringVar(&description, "description", "", "what this workflow is for")
	cmd.Flags().StringVar(&stepsFile, "steps-file", "", "JSON array of step objects, '-' for stdin (required)")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "declare the workflow enabled")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("steps-file")
	return cmd
}

func newOrchestrationWorkflowsUpdateCmd(flags *authClientFlags) *cobra.Command {
	var (
		name        string
		description string
		enabled     bool
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Partially update a workflow's metadata — only the flags you type are sent",
		Long: "update is the PATCH verb for a workflow's metadata. It does NOT touch the step\n" +
			"graph: that is replaced as one unit by `set-steps`, so an approved plan hash\n" +
			"can never drift under a partial edit.\n\n" +
			"Only the fields you name are sent; `--enabled=false` disables the workflow and\n" +
			"leaves its name and description alone.",
		Example: "  olivares orchestration workflows update wf-1 --enabled=false\n" +
			"  olivares orchestration workflows update wf-1 --description \"nightly rollup\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			patchString(cmd, body, "name", "name", name)
			patchString(cmd, body, "description", "description", description)
			patchBool(cmd, body, "enabled", "enabled", enabled)
			if len(body) == 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"nothing to update: name at least one field to change (the step graph is replaced with `set-steps`)"))
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPatch, path: "/workflows/" + agentExecPathID(args[0]), body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, orchestrationWorkflowColumns)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "rename the workflow")
	cmd.Flags().StringVar(&description, "description", "", "replace the description")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "enable or disable the workflow")
	return cmd
}

func newOrchestrationWorkflowsSetStepsCmd(flags *authClientFlags) *cobra.Command {
	var stepsFile string
	cmd := &cobra.Command{
		Use:   "set-steps <id>",
		Short: "Replace a workflow's whole step graph (PUT — one unit, one hash)",
		Long: "set-steps replaces the ENTIRE step list. It is a PUT, not a patch, and that is\n" +
			"deliberate: the graph is validated, hashed and approved as one unit, so there is\n" +
			"no supported way to edit one step of an approved plan in place.\n\n" +
			"Fetch the current graph with `workflows get <id> -o json` before editing it.",
		Example: "  olivares orchestration workflows set-steps wf-1 --steps-file steps.json\n" +
			"  olivares orchestration workflows set-steps wf-1 --steps-file -",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			steps, err := readAgentExecJSONArray(cmd, stepsFile, "step")
			if err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPut, path: "/workflows/" + agentExecPathID(args[0]) + "/steps",
				body: map[string]any{"steps": steps},
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, orchestrationWorkflowColumns)
		},
	}
	cmd.Flags().StringVar(&stepsFile, "steps-file", "", "JSON array of step objects, '-' for stdin (required)")
	_ = cmd.MarkFlagRequired("steps-file")
	return cmd
}

func newOrchestrationWorkflowsRevisionsCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "revisions <id>",
		Short:   "List a workflow's revision history",
		Long:    "revisions lists every approved shape of the graph, newest first.",
		Example: "  olivares orchestration workflows revisions wf-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/workflows/" + agentExecPathID(args[0]) + "/revisions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this workflow has no recorded revisions",
				[]string{"id", "created_at", "author", "version", "plan_hash", "step_count"})
		},
	}
	page.add(cmd)
	return cmd
}

func newOrchestrationWorkflowsRestoreCmd(flags *authClientFlags) *cobra.Command {
	var revisionID string
	cmd := &cobra.Command{
		Use:     "restore <id>",
		Short:   "Re-apply an earlier revision of a workflow",
		Long:    "restore re-applies an earlier graph through the same validation the authoring verbs use. The restored graph gets a new version and a new plan hash.",
		Example: "  olivares orchestration workflows restore wf-1 --revision rev-2",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPost, path: "/workflows/" + agentExecPathID(args[0]) + "/restore",
				body: map[string]any{"revision_id": revisionID},
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, orchestrationWorkflowColumns)
		},
	}
	cmd.Flags().StringVar(&revisionID, "revision", "", "the revision id to re-apply (required)")
	_ = cmd.MarkFlagRequired("revision")
	return cmd
}

func newOrchestrationWorkflowsDryRunCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "dry-run <id>",
		Short: "Resolve and validate a workflow without executing a single step",
		Long: "dry-run resolves the graph against the current estate and reports what a run\n" +
			"WOULD do. It is a read: no step executes, nothing is dispatched, and it needs\n" +
			"only the workflow read permission.",
		Example: "  olivares orchestration workflows dry-run wf-1\n" +
			"  olivares orchestration workflows dry-run wf-1 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPost, path: "/workflows/" + agentExecPathID(args[0]) + "/dry-run",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newOrchestrationWorkflowsRunCmd(flags *authClientFlags) *cobra.Command {
	var approvalRef string
	cmd := &cobra.Command{
		Use:   "run <id>",
		Short: "Execute a workflow through the approval gate (two-phase)",
		Long: "run executes the approved graph. Like `schedules fire` it is two-phase: with no\n" +
			"--approval-ref the engine opens an approval, runs NOTHING, and this command\n" +
			"exits 7 with the reference to repeat with. A kill switch answers 423 and exits 5.",
		Example: "  olivares orchestration workflows run wf-1\n" +
			"  olivares orchestration workflows run wf-1 --approval-ref ap-9",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			call := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodPost, path: "/workflows/" + agentExecPathID(args[0]) + "/run",
			}
			if approvalRef != "" {
				call.body = map[string]any{"approval_ref": approvalRef}
			}
			res, err := call.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportAgentExecPending(cmd, res, "workflow run")
			}
			return renderAgentExecObject(cmd, flags, res,
				[]string{"id", "workflow_ref", "status", "plan_hash", "approval_ref", "paused_reason", "started_at", "finished_at"})
		},
	}
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes this run")
	return cmd
}

func newOrchestrationWorkflowsRunsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runs",
		Short:   "Inspect a workflow's runs",
		Long:    "runs lists a workflow's executions and shows one run's step timeline.",
		Example: "  olivares orchestration workflows runs ls wf-1",
	}
	cmd.AddCommand(
		newOrchestrationWorkflowsRunsListCmd(flags),
		newOrchestrationWorkflowsRunsGetCmd(flags),
	)
	return cmd
}

func newOrchestrationWorkflowsRunsListCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "ls <workflow-id>",
		Short:   "List one workflow's runs, newest first",
		Long:    "ls lists the executions of one workflow with their status and approval reference.",
		Example: "  olivares orchestration workflows runs ls wf-1 --limit 20",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet, path: "/workflows/" + agentExecPathID(args[0]) + "/runs", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this workflow has never run",
				[]string{"id", "status", "plan_hash", "approval_ref", "paused_reason", "started_at", "finished_at"})
		},
	}
	page.add(cmd)
	return cmd
}

func newOrchestrationWorkflowsRunsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <workflow-id> <run-id>",
		Short: "Show one run's step timeline",
		Long: "get returns one run with its per-step outcomes. The run must belong to the\n" +
			"workflow you name: a run of ANOTHER workflow is reported as not found (exit 4),\n" +
			"never confirmed to exist.",
		Example: "  olivares orchestration workflows runs get wf-1 run-7",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: orchestrationModule,
				method: http.MethodGet,
				path:   "/workflows/" + agentExecPathID(args[0]) + "/runs/" + agentExecPathID(args[1]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}
