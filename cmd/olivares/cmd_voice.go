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

// cmd_voice.go drives the governed voice plane (/v1/m/voice,
// modules/voice/api.go): live and closed voice sessions, the per-agent policy
// that decides which model and provider a session may use, and the append-only
// decision ledger of every open request.
//
// `sessions open` is governed exactly like orchestration's fire and deploy's
// apply — two-phase, deny-closed, kill-switch first. With no --approval-ref the
// engine records the request and opens an approval WITHOUT starting a session:
// 202, exit 7. That distinction matters more here than almost anywhere else,
// because a voice session that "started" is a live audio path to a customer.
//
// `policies set` is a PUT and replaces the whole policy for one agent. It is not
// a patch and this command does not pretend otherwise — the help says to read
// `policies ls` first.

const voiceModule = "voice"

func newVoiceCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "voice",
		Short: "Inspect governed voice sessions and set the per-agent voice policy",
		Long: "voice exercises the governed voice plane: the sessions themselves, the policy\n" +
			"that constrains which model and provider an agent may speak through, and the\n" +
			"append-only ledger of every open decision.\n\n" +
			"`sessions open` is two-phase: without --approval-ref the engine opens an\n" +
			"approval and starts NO session, and this command exits 7 with the reference.",
		Example: "  olivares voice sessions ls\n" +
			"  olivares voice policies ls -o json\n" +
			"  olivares voice sessions open --session-ref vs-1 --agent-ref agent-a --model-ref m1 --provider-ref p1",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newVoiceSessionsCmd(&flags),
		newVoicePoliciesCmd(&flags),
		newVoiceDecisionsCmd(&flags),
	)
	return root
}

// ---- sessions ---------------------------------------------------------------

var voiceSessionColumns = []string{
	"session_ref", "agent_ref", "model_ref", "provider_ref", "state", "governed",
	"turn_count", "duration_ms", "latency_avg_ms", "last_event_at",
}

var voiceDecisionColumns = []string{
	"occurred_at", "session_ref", "agent_ref", "op", "policy_verdict",
	"gate_status", "op_status", "approval_ref", "actor",
}

func newVoiceSessionsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sessions",
		Short:   "List, follow and open governed voice sessions",
		Long:    "A voice session is one governed audio conversation: which agent spoke, through which model and provider, for how long, and under what policy.",
		Example: "  olivares voice sessions ls --limit 20",
	}
	cmd.AddCommand(
		newVoiceSessionsListCmd(flags),
		newVoiceSessionsGetCmd(flags),
		newVoiceSessionsStreamCmd(flags),
		newVoiceSessionsDecisionsCmd(flags),
		newVoiceSessionsOpenCmd(flags),
	)
	return cmd
}

func newVoiceSessionsListCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List voice sessions with their derived state",
		Long: "ls lists sessions. `state` is derived at read time — live, idle or ended — and\n" +
			"`governed` says whether a policy was in force when the session opened.",
		Example: "  olivares voice sessions ls\n  olivares voice sessions ls --limit 50 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: voiceModule,
				method: http.MethodGet, path: "/sessions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no voice sessions recorded", voiceSessionColumns)
		},
	}
	page.add(cmd)
	return cmd
}

func newVoiceSessionsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <session-ref>",
		Short:   "Show one voice session",
		Long:    "get returns one session with its turn counts, latency figures and the policy that governed it.",
		Example: "  olivares voice sessions get vs-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: voiceModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newVoiceSessionsStreamCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stream <session-ref>",
		Short: "Follow one live voice session as NDJSON (one object per event)",
		Long: "stream follows the engine's server-sent session events and prints ONE JSON\n" +
			"OBJECT PER LINE on stdout. Keep-alives and notices go to stderr.",
		Example: "  olivares voice sessions stream vs-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return streamAgentExecEvents(cmd, flags, voiceModule,
				"/sessions/"+agentExecPathID(args[0])+"/stream", nil)
		},
	}
}

func newVoiceSessionsDecisionsCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "decisions <session-ref>",
		Short:   "List one session's governance decisions",
		Long:    "decisions narrows the tenant ledger to one session: every open request, the policy verdict, the gate status and what was actuated.",
		Example: "  olivares voice sessions decisions vs-1 --limit 50",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: voiceModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]) + "/decisions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this session has no recorded decisions", voiceDecisionColumns)
		},
	}
	page.add(cmd)
	return cmd
}

func newVoiceSessionsOpenCmd(flags *authClientFlags) *cobra.Command {
	var (
		sessionRef  string
		agentRef    string
		modelRef    string
		providerRef string
		approvalRef string
	)
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open a governed voice session through the approval gate (two-phase)",
		Long: "open asks to start a governed voice session. It is deny-closed and two-phase:\n\n" +
			"  without --approval-ref  the policy is evaluated, an approval is opened and NO\n" +
			"                          session starts. This command exits 7 with the reference.\n" +
			"  with --approval-ref     the session opens only if the approval is explicit and\n" +
			"                          bound to this plan.\n\n" +
			"A kill switch outranks everything: a stopped scope answers 423 and this command\n" +
			"exits 5 with no session opened. A model or provider the agent's policy forbids\n" +
			"is refused with the policy verdict named, not a bare 403.",
		Example: "  olivares voice sessions open --session-ref vs-1 --agent-ref agent-a --model-ref m1 --provider-ref p1\n" +
			"  olivares voice sessions open --session-ref vs-1 --agent-ref agent-a --model-ref m1 --provider-ref p1 --approval-ref ap-9",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{
				"session_ref":  sessionRef,
				"agent_ref":    agentRef,
				"model_ref":    modelRef,
				"provider_ref": providerRef,
			}
			if approvalRef != "" {
				body["approval_ref"] = approvalRef
			}
			res, err := agentExecCall{
				flags: flags, module: voiceModule,
				method: http.MethodPost, path: "/sessions/open", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportAgentExecPending(cmd, res, "voice session open")
			}
			return renderAgentExecObject(cmd, flags, res,
				[]string{"op", "op_status", "policy_verdict", "gate_status", "approval_ref", "dispatch_ref", "plan_hash", "detail"})
		},
	}
	cmd.Flags().StringVar(&sessionRef, "session-ref", "", "the session reference to open (required)")
	cmd.Flags().StringVar(&agentRef, "agent-ref", "", "the agent that will speak (required)")
	cmd.Flags().StringVar(&modelRef, "model-ref", "", "the model requested for this session (required)")
	cmd.Flags().StringVar(&providerRef, "provider-ref", "", "the provider requested for this session (required)")
	cmd.Flags().StringVar(&approvalRef, "approval-ref", "", "phase 2: the approval that authorizes this open")
	for _, required := range []string{"session-ref", "agent-ref", "model-ref", "provider-ref"} {
		_ = cmd.MarkFlagRequired(required)
	}
	return cmd
}

// ---- policies ------------------------------------------------------------------

var voicePolicyColumns = []string{
	"id", "agent_ref", "allowed_model_ref", "allowed_provider_ref",
	"max_session_minutes", "max_latency_ms", "set_by", "updated_at",
}

func newVoicePoliciesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "policies",
		Short:   "Read and replace the per-agent voice policy",
		Long:    "A voice policy constrains one agent: which model and provider it may speak through, how long a session may last and what latency is tolerated.",
		Example: "  olivares voice policies ls",
	}
	cmd.AddCommand(newVoicePoliciesListCmd(flags), newVoicePoliciesSetCmd(flags))
	return cmd
}

func newVoicePoliciesListCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List the voice policies in force",
		Long:    "ls lists every per-agent policy with who set it and when.",
		Example: "  olivares voice policies ls\n  olivares voice policies ls --limit 50 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: voiceModule,
				method: http.MethodGet, path: "/policies", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no voice policies set", voicePolicyColumns)
		},
	}
	page.add(cmd)
	return cmd
}

func newVoicePoliciesSetCmd(flags *authClientFlags) *cobra.Command {
	var (
		agentRef    string
		modelRef    string
		providerRef string
		maxMinutes  int64
		maxLatency  int64
		callsFile   string
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Replace one agent's voice policy (PUT — the whole policy)",
		Long: "set REPLACES the policy for one agent. It is a PUT, not a patch: read\n" +
			"`policies ls` first and pass the whole policy you intend, because a limit you\n" +
			"do not pass is not carried over from the stored one.\n\n" +
			"--calls-file supplies the optional call policy as a JSON object ('-' reads\n" +
			"stdin); omit it to leave the policy without one.",
		Example: "  olivares voice policies set --agent-ref agent-a --allowed-model-ref m1 --allowed-provider-ref p1\n" +
			"  olivares voice policies set --agent-ref agent-a --allowed-model-ref m1 --allowed-provider-ref p1 --max-session-minutes 30 --max-latency-ms 800 --calls-file calls.json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if maxMinutes < 0 || maxLatency < 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--max-session-minutes and --max-latency-ms must be zero or positive"))
			}
			body := map[string]any{
				"agent_ref":            agentRef,
				"allowed_model_ref":    modelRef,
				"allowed_provider_ref": providerRef,
				"max_session_minutes":  maxMinutes,
				"max_latency_ms":       maxLatency,
			}
			if callsFile != "" {
				calls, err := readAgentExecJSONDocument(cmd, callsFile)
				if err != nil {
					return err
				}
				body["calls"] = calls
			}
			res, err := agentExecCall{
				flags: flags, module: voiceModule,
				method: http.MethodPut, path: "/policies", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, voicePolicyColumns)
		},
	}
	cmd.Flags().StringVar(&agentRef, "agent-ref", "", "the agent this policy governs (required)")
	cmd.Flags().StringVar(&modelRef, "allowed-model-ref", "", "the only model this agent may speak through (required)")
	cmd.Flags().StringVar(&providerRef, "allowed-provider-ref", "", "the only provider this agent may speak through (required)")
	cmd.Flags().Int64Var(&maxMinutes, "max-session-minutes", 0, "cap a session's length in minutes (0 = no cap)")
	cmd.Flags().Int64Var(&maxLatency, "max-latency-ms", 0, "tolerated latency in milliseconds (0 = no limit)")
	cmd.Flags().StringVar(&callsFile, "calls-file", "", "JSON call-policy object, '-' for stdin")
	for _, required := range []string{"agent-ref", "allowed-model-ref", "allowed-provider-ref"} {
		_ = cmd.MarkFlagRequired(required)
	}
	return cmd
}

// ---- decisions --------------------------------------------------------------------

func newVoiceDecisionsCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:   "decisions",
		Short: "List the append-only voice decision ledger for the tenant",
		Long: "decisions is the record of every open request across all sessions: the policy\n" +
			"verdict, the approval gate's answer, and what was actuated.",
		Example: "  olivares voice decisions --limit 100\n  olivares voice decisions -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: voiceModule,
				method: http.MethodGet, path: "/decisions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no voice decisions recorded", voiceDecisionColumns)
		},
	}
	page.add(cmd)
	return cmd
}
