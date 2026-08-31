// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_claudeagents.go drives the managed-agents console (/v1/m/claude-agents,
// modules/governance/claudeagents.go): the thread events of one managed session,
// and the human decision on a tool the agent asked permission to use.
//
// TWO ROUTES, AND BOTH HAVE A TRAP WORTH NAMING IN THE HELP:
//
//	`sessions events` NEVER FABRICATES. With no live source wired the engine
//	answers an honest empty list; a wired source that cannot answer is a 502,
//	which exits 6 — "the events are UNKNOWN right now, not absent". Those are
//	different facts and this command keeps them different, because a reviewer who
//	reads an upstream outage as "no events" concludes the agent did nothing.
//
//	`sessions tool-confirmation` REQUIRES A HUMAN IDENTITY. A system token cannot
//	confirm a tool, by design: the engine refuses it (403). This CLI does not
//	work around that — it explains it, so an operator who hit it with a machine
//	token knows the remedy is a human credential, not a broader role.
//
// The deny path takes a message, and it is a message a person will read. It must
// not carry a credential: the engine refuses one that does, and this command
// checks nothing of its own — the server-side rule is the boundary.

const claudeAgentsModule = "claude-agents"

func newClaudeAgentsCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "claude-agents",
		Short: "Read a managed agent session's thread events and answer its tool confirmations",
		Long: "claude-agents is the terminal side of the managed-agents console: what a\n" +
			"managed session did, and the human answer to a tool it asked permission for.\n\n" +
			"The read is honest about not knowing: an unwired source returns an empty list,\n" +
			"while a source that failed returns exit 6 — events UNKNOWN, never silently\n" +
			"absent.",
		Example: "  olivares claude-agents sessions events sess-1\n" +
			"  olivares claude-agents sessions tool-confirmation sess-1 --tool-use-id tu-1 --result allow",
	}
	flags.addPersistent(root)
	root.AddCommand(newClaudeAgentsSessionsCmd(&flags))
	return root
}

func newClaudeAgentsSessionsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sessions",
		Short:   "Inspect and answer one managed agent session",
		Long:    "A managed agent session is one Claude Code thread the control plane governs: its events are readable, and its pending tool uses are answerable.",
		Example: "  olivares claude-agents sessions events sess-1",
	}
	cmd.AddCommand(
		newClaudeAgentsEventsCmd(flags),
		newClaudeAgentsToolConfirmationCmd(flags),
	)
	return cmd
}

func newClaudeAgentsEventsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "events <session-id>",
		Short: "List one managed session's thread events",
		Long: "events returns the thread events the wired source reports for one session.\n\n" +
			"EMPTY AND UNKNOWN ARE DIFFERENT ANSWERS. With no source wired the engine\n" +
			"answers an honest empty list and this command exits 0 saying so. A wired source\n" +
			"that could not answer is a 502 and this command exits 6 — the events are\n" +
			"UNKNOWN right now, and reading that as \"the agent did nothing\" is the mistake\n" +
			"the engine refuses to let you make.\n\n" +
			"It is NOT paginated by the engine: the source's whole answer comes back at once.",
		Example: "  olivares claude-agents sessions events sess-1\n" +
			"  olivares claude-agents sessions events sess-1 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: claudeAgentsModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]) + "/events",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res,
				"no thread events for this session (no source is wired, or it reported none)",
				[]string{"created_at", "type", "agent_ref", "peer_ref", "tool_name", "tool_use_id"})
		},
	}
}

func newClaudeAgentsToolConfirmationCmd(flags *authClientFlags) *cobra.Command {
	var (
		toolUseID   string
		result      string
		denyMessage string
	)
	cmd := &cobra.Command{
		Use:   "tool-confirmation <session-id>",
		Short: "Answer a managed agent's pending tool use (allow or deny)",
		Long: "tool-confirmation records the human decision on one tool a managed agent asked\n" +
			"permission to use.\n\n" +
			"A STABLE HUMAN IDENTITY IS REQUIRED. A system token cannot confirm a tool — the\n" +
			"engine refuses it with exit 3, and the remedy is a human credential, not a\n" +
			"broader role. This CLI does not offer a way around that.\n\n" +
			"--deny-message is shown to the agent, so it explains the refusal. Do not put a\n" +
			"credential in it: the engine rejects a message that contains one.",
		Example: "  olivares claude-agents sessions tool-confirmation sess-1 --tool-use-id tu-1 --result allow\n" +
			"  olivares claude-agents sessions tool-confirmation sess-1 --tool-use-id tu-1 --result deny --deny-message \"not in scope for this ticket\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch result {
			case "allow", "deny":
			default:
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("invalid --result %q (use allow or deny)", result))
			}
			// A deny message on an ALLOW is a contradiction, and sending it would
			// record an explanation for a refusal that never happened. Refuse the
			// invocation rather than quietly dropping half of it.
			if result == "allow" && denyMessage != "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--deny-message is only meaningful with --result deny"))
			}
			body := map[string]any{"tool_use_id": toolUseID, "result": result}
			if denyMessage != "" {
				body["deny_message"] = denyMessage
			}
			res, err := agentExecCall{
				flags: flags, module: claudeAgentsModule,
				method: http.MethodPost, path: "/sessions/" + agentExecPathID(args[0]) + "/tool-confirmation",
				body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
	cmd.Flags().StringVar(&toolUseID, "tool-use-id", "", "the pending tool use to answer (required)")
	cmd.Flags().StringVar(&result, "result", "", "allow or deny (required)")
	cmd.Flags().StringVar(&denyMessage, "deny-message", "", "why the tool was denied, shown to the agent")
	_ = cmd.MarkFlagRequired("tool-use-id")
	_ = cmd.MarkFlagRequired("result")
	return cmd
}
