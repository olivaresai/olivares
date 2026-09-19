// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// `olivares policy replay` — reconstruct what a policy decided on a past date
// from the evidence ledger, never from the live policy.
//
// The HTTP surface is /v1/m/governance/decisions/replay. The command is
// named `policy` because that is the operator question ("what did the policy
// decide then"), not because it consults the authoring revision store.

func newPolicyCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:   "policy",
		Short: "Reconstruct historical policy decisions from the evidence ledger",
		Long: "policy answers historical authorization questions from the access-evidence\n" +
			"ledger: the recorded policy version and inputs, never the live PDP. When the\n" +
			"record is not enough it prints COULD NOT RECONSTRUCT and names the missing fact.",
		Example: "  olivares policy replay --at 2026-09-15T12:00:00Z --principal agent-7 --resource public.customers --action SELECT --resource-kind postgres.table\n" +
			"  olivares policy replay --decision-id 01JEXAMPLE",
	}
	flags.addPersistent(root)
	root.AddCommand(newPolicyReplayCmd(flags))
	return root
}

type cliReconstructResult struct {
	Status                string `json:"status"`
	CouldNotReconstruct   bool   `json:"could_not_reconstruct"`
	Missing               string `json:"missing,omitempty"`
	Outcome               string `json:"outcome,omitempty"`
	RecordedOutcome       string `json:"recorded_outcome,omitempty"`
	PolicyVersionID       string `json:"policy_version_id,omitempty"`
	PolicyVersionRecorded bool   `json:"policy_version_recorded"`
	InputsDigest          string `json:"inputs_digest,omitempty"`
	DecisionID            string `json:"decision_id,omitempty"`
	ArtifactID            string `json:"artifact_id,omitempty"`
	Evaluator             string `json:"evaluator,omitempty"`
	At                    string `json:"at,omitempty"`
	UsedLivePolicy        bool   `json:"used_live_policy"`
	ReasonCode            string `json:"reason_code,omitempty"`
}

func newPolicyReplayCmd(flags *authClientFlags) *cobra.Command {
	var (
		at               string
		principal        string
		resource         string
		resourceKind     string
		sourceInstance   string
		action           string
		actionVocabulary string
		decisionID       string
	)
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Replay a past authorization from the ledger, never from the live policy",
		Long: "Reconstruct what the enforcement point decided for a principal, resource and\n" +
			"action at a past instant. The answer is computed from the recorded policy\n" +
			"artifact and inputs. It never consults the currently active authoring revision.\n\n" +
			"When a required fact is absent the command prints COULD NOT RECONSTRUCT and\n" +
			"names the missing fact (authorization_decision, policy_version_id,\n" +
			"policy_artifact, policy_artifact.content, evaluator).\n\n" +
			"Pass --decision-id to reconstruct one stored row. Otherwise --at, --principal\n" +
			"and --action select the latest live_authorization decision for that question\n" +
			"at or before --at. The question digest includes resource_kind; omit it only\n" +
			"when the recorded question omitted it too.",
		Example: "  olivares policy replay --at 2026-09-15T12:00:00Z --principal agent-7 --resource public.customers --action SELECT --resource-kind postgres.table\n" +
			"  olivares policy replay --decision-id 01JEXAMPLE -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]string{}
			if strings.TrimSpace(at) != "" {
				body["at"] = strings.TrimSpace(at)
			}
			if strings.TrimSpace(principal) != "" {
				body["principal"] = strings.TrimSpace(principal)
			}
			if strings.TrimSpace(resource) != "" {
				body["resource"] = strings.TrimSpace(resource)
			}
			if strings.TrimSpace(resourceKind) != "" {
				body["resource_kind"] = strings.TrimSpace(resourceKind)
			}
			if strings.TrimSpace(action) != "" {
				body["action"] = strings.TrimSpace(action)
			}
			if strings.TrimSpace(actionVocabulary) != "" {
				body["action_vocabulary"] = strings.TrimSpace(actionVocabulary)
			}
			if strings.TrimSpace(sourceInstance) != "" {
				body["source_instance"] = strings.TrimSpace(sourceInstance)
			}
			if strings.TrimSpace(decisionID) != "" {
				body["decision_id"] = strings.TrimSpace(decisionID)
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodPost, path: "/decisions/replay",
				body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			var out cliReconstructResult
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				return writeReplayText(w, out)
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "instant to reconstruct (RFC3339); required unless --decision-id is set")
	cmd.Flags().StringVar(&principal, "principal", "", "principal / actor reference recorded on the question")
	cmd.Flags().StringVar(&resource, "resource", "", "resource reference recorded on the question")
	cmd.Flags().StringVar(&resourceKind, "resource-kind", "", "resource kind recorded on the question")
	cmd.Flags().StringVar(&action, "action", "", "exact action / permission recorded on the question")
	cmd.Flags().StringVar(&actionVocabulary, "action-vocabulary", "", "vocabulary of --action (required to match a stored question that named one)")
	cmd.Flags().StringVar(&sourceInstance, "source-instance", "", "origin instance that makes the resource reference canonical")
	cmd.Flags().StringVar(&decisionID, "decision-id", "", "reconstruct this stored decision row")
	return cmd
}

func writeReplayText(w io.Writer, out cliReconstructResult) error {
	if out.CouldNotReconstruct || out.Status == "insufficient" {
		if _, err := fmt.Fprintln(w, "COULD NOT RECONSTRUCT"); err != nil {
			return err
		}
		if out.Missing != "" {
			if _, err := fmt.Fprintf(w, "missing: %s\n", out.Missing); err != nil {
				return err
			}
		}
		return nil
	}
	label := strings.ToUpper(out.Status)
	if label == "" {
		label = "RECONSTRUCTED"
	}
	if _, err := fmt.Fprintln(w, label); err != nil {
		return err
	}
	lines := []struct{ k, v string }{
		{"outcome", out.Outcome},
		{"recorded_outcome", out.RecordedOutcome},
		{"policy_version_id", out.PolicyVersionID},
		{"inputs_digest", out.InputsDigest},
		{"decision_id", out.DecisionID},
		{"evaluator", out.Evaluator},
		{"at", out.At},
	}
	for _, line := range lines {
		if line.v == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", line.k, line.v); err != nil {
			return err
		}
	}
	if out.UsedLivePolicy {
		if _, err := fmt.Fprintln(w, "used_live_policy: true (this is a defect; reconstruction must not consult the live policy)"); err != nil {
			return err
		}
	}
	return nil
}
