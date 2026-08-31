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

// cmd_deploy.go drives the agent deployment plane (/v1/m/deploy,
// modules/deploy/deploy.go): versioned definitions, the plan/verify/apply
// discipline, and the retire and rollback that undo a live deployment.
//
// COUNTING DELETES FINDS ONE. THERE ARE FOUR IRREVERSIBLE VERBS.
// `definitions rm` is the only DELETE in this family — and `retire`, `rollback`
// are POSTs that take down or revert a running deployment. A gate built by
// counting HTTP methods would have left three of the four ungated. All four go
// through confirmDestructive.
//
// TWO-PHASE ACTUATION, and the exit code that makes it usable: `apply` and
// `retire` with no --approval-ref open an approval and change NOTHING; the
// engine answers 202 and this command exits 7 carrying the reference. A pipeline
// that treated 202 as success would report a deployment that never happened.
//
// A THIRD REFUSAL WORTH NAMING: apply/retire also require a fresh hardware
// step-up on a HUMAN session. That comes back as a 403 with
// code=step_up_required, and agentExecHTTPError says so instead of letting it
// read as a missing role — the operator's grants are not the problem.

const deployModule = "deploy"

func newDeployCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "deploy",
		Short: "Declare, plan, apply, retire and roll back governed agent deployments",
		Long: "deploy exercises the only plane that acts on customer infrastructure. Its\n" +
			"discipline is plan-before-apply and deny-by-default: a definition is versioned,\n" +
			"a plan is computed and hashed, and nothing is actuated until an approval bound\n" +
			"to THAT plan is presented.\n\n" +
			"Exit codes a pipeline should branch on:\n" +
			"  0  the operation completed\n" +
			"  7  an approval was opened and NOTHING was actuated — re-run with --approval-ref\n" +
			"  5  a kill switch or a state conflict blocked it; nothing was actuated\n" +
			"  3  governance denied it, or your session needs a hardware step-up",
		Example: "  olivares deploy definitions ls\n" +
			"  olivares deploy plan dep-1\n" +
			"  olivares deploy apply dep-1 --approval-ref ap-9\n" +
			"  olivares deploy rollback dep-1 --to-version 4 --yes",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newDeployDefinitionsCmd(&flags),
		newDeployPlanCmd(&flags),
		newDeployVerifyCmd(&flags),
		newDeployApplyCmd(&flags),
		newDeployRetireCmd(&flags),
		newDeployRollbackCmd(&flags),
		newDeployOperationsCmd(&flags),
		newDeployWiringsCmd(&flags),
	)
	return root
}

var deployDefinitionColumns = []string{
	"id", "name", "environment", "subject_ref", "runtime", "target",
	"desired_status", "current_version", "applied_version", "up_to_date",
}

func newDeployDefinitionsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "definitions",
		Short:   "Declare and version deployment definitions",
		Long:    "A definition is the desired state of one deployment: its subject, environment, runtime, target and versioned spec. Every change mints a revision.",
		Example: "  olivares deploy definitions ls",
	}
	cmd.AddCommand(
		newDeployDefinitionsListCmd(flags),
		newDeployDefinitionsGetCmd(flags),
		newDeployDefinitionsCreateCmd(flags),
		newDeployDefinitionsUpdateCmd(flags),
		newDeployDefinitionsRemoveCmd(flags),
		newDeployDefinitionsRevisionsCmd(flags),
	)
	return cmd
}

func newDeployDefinitionsListCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List deployment definitions with their drift",
		Long: "ls lists definitions. `up_to_date` is the column to read: false means the\n" +
			"applied version is behind the current one — a declared change nobody applied.",
		Example: "  olivares deploy definitions ls\n  olivares deploy definitions ls --limit 50 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodGet, path: "/definitions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no deployment definitions declared", deployDefinitionColumns)
		},
	}
	page.add(cmd)
	return cmd
}

func newDeployDefinitionsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one definition with its current spec and real state",
		Long:    "get returns the desired spec and, when the deployment is applied, the real state snapshot the engine last observed.",
		Example: "  olivares deploy definitions get dep-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodGet, path: "/definitions/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newDeployDefinitionsCreateCmd(flags *authClientFlags) *cobra.Command {
	var (
		subjectKind string
		subjectRef  string
		name        string
		environment string
		target      string
		runtime     string
		sourceRef   string
		specFile    string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Declare a deployment definition from a JSON spec",
		Long: "create declares desired state. It does NOT deploy anything: the definition is\n" +
			"recorded at version 1 with nothing applied, and `plan` then `apply` are what\n" +
			"actuate it.\n\n" +
			"--spec-file takes a JSON object ('-' reads stdin) and is validated here, so a\n" +
			"malformed spec is a usage error (exit 2) before a request is sent.",
		Example: "  olivares deploy definitions create --name api --environment prod --subject-ref agent-a --runtime container --target cluster-1 --spec-file spec.json\n" +
			"  olivares deploy definitions create --name api --environment prod --subject-ref agent-a --runtime container --target cluster-1 --spec-file - --source-ref git:abc123",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, err := readAgentExecJSONDocument(cmd, specFile)
			if err != nil {
				return err
			}
			body := map[string]any{
				"subject_kind": subjectKind,
				"subject_ref":  subjectRef,
				"name":         name,
				"environment":  environment,
				"target":       target,
				"runtime":      runtime,
				"spec":         spec,
			}
			if sourceRef != "" {
				body["source_ref"] = sourceRef
			}
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodPost, path: "/definitions", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, deployDefinitionColumns)
		},
	}
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "agent", "what is being deployed")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "the subject's reference (required)")
	cmd.Flags().StringVar(&name, "name", "", "deployment name (required)")
	cmd.Flags().StringVar(&environment, "environment", "", "target environment (required)")
	cmd.Flags().StringVar(&target, "target", "", "the runtime target this deploys onto (required)")
	cmd.Flags().StringVar(&runtime, "runtime", "", "the runtime kind (required)")
	cmd.Flags().StringVar(&sourceRef, "source-ref", "", "provenance reference for the spec, e.g. a commit")
	cmd.Flags().StringVar(&specFile, "spec-file", "", "JSON spec object, '-' for stdin (required)")
	for _, required := range []string{"subject-ref", "name", "environment", "target", "runtime", "spec-file"} {
		_ = cmd.MarkFlagRequired(required)
	}
	return cmd
}

func newDeployDefinitionsUpdateCmd(flags *authClientFlags) *cobra.Command {
	var (
		target    string
		sourceRef string
		note      string
		specFile  string
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Publish a new revision of a definition (PUT)",
		Long: "update mints a NEW REVISION of the desired state. It does not apply it: the\n" +
			"definition's current_version advances while applied_version stays put, so `ls`\n" +
			"reports up_to_date=false until somebody applies it.\n\n" +
			"--spec-file replaces the whole spec. --target and --source-ref are sent only\n" +
			"when you type them, so an update that changes just the spec leaves them alone.",
		Example: "  olivares deploy definitions update dep-1 --spec-file spec.json --note \"bump image\"\n" +
			"  olivares deploy definitions update dep-1 --spec-file - --target cluster-2",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := readAgentExecJSONDocument(cmd, specFile)
			if err != nil {
				return err
			}
			body := map[string]any{"spec": spec}
			patchString(cmd, body, "target", "target", target)
			patchString(cmd, body, "source-ref", "source_ref", sourceRef)
			if note != "" {
				body["note"] = note
			}
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodPut, path: "/definitions/" + agentExecPathID(args[0]), body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, deployDefinitionColumns)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "retarget the deployment")
	cmd.Flags().StringVar(&sourceRef, "source-ref", "", "provenance reference for this revision")
	cmd.Flags().StringVar(&note, "note", "", "why this revision exists (recorded on the revision)")
	cmd.Flags().StringVar(&specFile, "spec-file", "", "JSON spec object, '-' for stdin (required)")
	_ = cmd.MarkFlagRequired("spec-file")
	return cmd
}

func newDeployDefinitionsRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a definition and its revisions (destructive; needs --yes when unattended)",
		Long: "rm deletes the definition. The engine refuses while the deployment is still\n" +
			"applied (exit 5): retire it first, so nothing is ever left running with no\n" +
			"declaration describing it.\n\n" +
			"The confirmation is the CLI's own guard against an unattended invocation. It is\n" +
			"not proof a human is present — that belongs in the approval path — but a cron\n" +
			"job or a `yes |` pipeline does not reach this verb by accident.",
		Example: "  olivares deploy definitions rm dep-1 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete deployment definition %s and its revision history", safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodDelete, path: "/definitions/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecDeleted(cmd, res, "deployment definition "+safeCLIValue(args[0], ""))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func newDeployDefinitionsRevisionsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "revisions <id>",
		Short: "List a definition's revision history",
		Long: "revisions lists every published version with its spec hash, note and author.\n" +
			"It is NOT paginated by the engine: the whole history comes back in one answer.",
		Example: "  olivares deploy definitions revisions dep-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodGet, path: "/definitions/" + agentExecPathID(args[0]) + "/revisions",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this definition has no recorded revisions",
				[]string{"version", "spec_hash", "source_ref", "note", "created_by", "created_at"})
		},
	}
}

// ---- lifecycle -------------------------------------------------------------------

var deployOperationColumns = []string{
	"occurred_at", "definition_id", "op", "status", "from_version", "to_version",
	"gate_status", "approval_ref", "actor",
}

func newDeployPlanCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "plan <id>",
		Short: "Compute the change set an apply WOULD make (nothing is actuated)",
		Long: "plan computes and hashes the change set between the applied version and the\n" +
			"current one. Nothing is actuated. The plan_hash it prints is what an approval\n" +
			"binds to, so an apply carrying an approval for a DIFFERENT plan is refused.",
		Example: "  olivares deploy plan dep-1\n  olivares deploy plan dep-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodPost, path: "/definitions/" + agentExecPathID(args[0]) + "/plan",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newDeployVerifyCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "verify <id>",
		Short:   "Check the real deployment against its declared spec",
		Long:    "verify asks the runtime what is actually deployed and compares it to the declared spec, reporting drift. Nothing is actuated.",
		Example: "  olivares deploy verify dep-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodPost, path: "/definitions/" + agentExecPathID(args[0]) + "/verify",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

// deployMutation is the shared body of the three governed lifecycle verbs. They
// differ only in route, wording and whether the CLI adds its own confirmation.
func deployMutation(cmd *cobra.Command, flags *authClientFlags, id, verb, approvalRef string, extra map[string]any) error {
	body := map[string]any{}
	for k, v := range extra {
		body[k] = v
	}
	if approvalRef != "" {
		body["approval_ref"] = approvalRef
	}
	call := agentExecCall{
		flags: flags, module: deployModule,
		method: http.MethodPost, path: "/definitions/" + agentExecPathID(id) + "/" + verb,
	}
	if len(body) > 0 {
		call.body = body
	}
	res, err := call.do(cmd)
	if err != nil {
		return err
	}
	if res.status == http.StatusAccepted {
		return reportAgentExecPending(cmd, res, verb+" "+safeCLIValue(id, ""))
	}
	return renderAgentExecObject(cmd, flags, res,
		[]string{"op", "status", "version", "plan_hash", "approval_ref", "gate_status", "wirings", "detail"})
}

func newDeployApplyCmd(flags *authClientFlags) *cobra.Command {
	var approvalRef string
	cmd := &cobra.Command{
		Use:   "apply <id>",
		Short: "Actuate the current version through the approval gate (two-phase)",
		Long: "apply is the verb that touches customer infrastructure, and it is deny-closed.\n\n" +
			"  without --approval-ref  an approval is opened and NOTHING is deployed;\n" +
			"                          this command exits 7 with the reference to repeat with.\n" +
			"  with --approval-ref     the engine deploys only if the approval is explicit\n" +
			"                          AND bound to the current plan hash.\n\n" +
			"A retired definition is a conflict (exit 5) — roll it back or update it first.\n" +
			"A human session without a fresh hardware step-up is refused with exit 3, and\n" +
			"this command says that is what happened rather than blaming your role.",
		Example: "  olivares deploy apply dep-1\n  olivares deploy apply dep-1 --approval-ref ap-9",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deployMutation(cmd, flags, args[0], "apply", approvalRef, nil)
		},
	}
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes this apply")
	return cmd
}

func newDeployRetireCmd(flags *authClientFlags) *cobra.Command {
	var (
		approvalRef string
		yes         bool
	)
	cmd := &cobra.Command{
		Use:   "retire <id>",
		Short: "Take a live deployment down (destructive POST; needs --yes when unattended)",
		Long: "retire tears down the running deployment. It is a POST and there is no DELETE\n" +
			"anywhere near it, which is exactly why it is gated here: counting HTTP verbs\n" +
			"would have found this family's only DELETE on `definitions rm` and left the\n" +
			"verb that actually stops production ungated.\n\n" +
			"Like apply it is two-phase: without --approval-ref an approval is opened and\n" +
			"nothing is torn down (exit 7).",
		Example: "  olivares deploy retire dep-1 --yes\n  olivares deploy retire dep-1 --approval-ref ap-9 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"retire deployment %s (this takes the running deployment down)", safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			return deployMutation(cmd, flags, args[0], "retire", approvalRef, nil)
		},
	}
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes this retire")
	addYesFlag(cmd, &yes)
	return cmd
}

func newDeployRollbackCmd(flags *authClientFlags) *cobra.Command {
	var (
		toVersion int64
		note      string
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "rollback <id>",
		Short: "Revert a definition to an earlier version (destructive POST; needs --yes when unattended)",
		Long: "rollback republishes an earlier revision as the current desired state. It\n" +
			"discards the state that was current, which is why it is gated like a delete.\n\n" +
			"A --to-version that is not a real revision is a conflict (exit 5), not a silent\n" +
			"no-op.",
		Example: "  olivares deploy rollback dep-1 --to-version 4 --yes\n" +
			"  olivares deploy rollback dep-1 --to-version 4 --note \"bad image\" --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toVersion <= 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--to-version must be a positive revision number, got %d", toVersion))
			}
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"roll deployment %s back to version %d", safeCLIValue(args[0], ""), toVersion)); err != nil {
				return err
			}
			extra := map[string]any{"to_version": toVersion}
			if note != "" {
				extra["note"] = note
			}
			return deployMutation(cmd, flags, args[0], "rollback", "", extra)
		},
	}
	cmd.Flags().Int64Var(&toVersion, "to-version", 0, "the revision number to restore (required)")
	cmd.Flags().StringVar(&note, "note", "", "why the rollback happened (recorded on the revision)")
	addYesFlag(cmd, &yes)
	_ = cmd.MarkFlagRequired("to-version")
	return cmd
}

// ---- operations and wirings --------------------------------------------------------

func newDeployOperationsCmd(flags *authClientFlags) *cobra.Command {
	var (
		page         agentExecPageFlags
		definitionID string
		op           string
		status       string
	)
	cmd := &cobra.Command{
		Use:   "operations",
		Short: "List the append-only ledger of plan/apply/retire/rollback operations",
		Long: "operations is the record of what was actuated, by whom, under which approval,\n" +
			"and what the gate said. It is the answer to \"who deployed this and who approved\n" +
			"it\".",
		Example: "  olivares deploy operations --limit 50\n" +
			"  olivares deploy operations --definition-id dep-1 --op apply --status blocked",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "definition_id", definitionID)
			setQuery(q, "op", op)
			setQuery(q, "status", status)
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodGet, path: "/operations", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no deploy operations recorded", deployOperationColumns)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&definitionID, "definition-id", "", "only operations on this definition")
	cmd.Flags().StringVar(&op, "op", "", "only operations of this kind")
	cmd.Flags().StringVar(&status, "status", "", "only operations in this status")
	return cmd
}

func newDeployWiringsCmd(flags *authClientFlags) *cobra.Command {
	var (
		page         agentExecPageFlags
		definitionID string
		status       string
	)
	cmd := &cobra.Command{
		Use:   "wirings",
		Short: "List what each deployment is wired to, and how that was attributed",
		Long: "wirings lists the resources a deployment is connected to — the identity it runs\n" +
			"as, the secrets it can reach — with the attribution that says how each wiring\n" +
			"was established.",
		Example: "  olivares deploy wirings\n  olivares deploy wirings --definition-id dep-1 --status active",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "definition_id", definitionID)
			setQuery(q, "status", status)
			res, err := agentExecCall{
				flags: flags, module: deployModule,
				method: http.MethodGet, path: "/wirings", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no deployment wirings recorded",
				[]string{"definition_id", "agent_ref", "identity_ref", "resource_kind",
					"resource_ref", "mode", "status", "attribution"})
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&definitionID, "definition-id", "", "only wirings of this definition")
	cmd.Flags().StringVar(&status, "status", "", "only wirings in this status")
	return cmd
}
