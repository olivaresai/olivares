// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/claude"
)

// newClaudeHookCmd is the managed HOOK COMMAND the Claude Code PreToolUse/PostToolUse
// hooks invoke. Claude Code pipes the hook JSON to stdin and reads the decision
// from stdout; this command forwards the call to the governed PEP endpoint and relays the
// verdict, DENY-CLOSED on any failure. It is distributed via managed-settings (see
// the governed hooks PEP contract) and reads its configuration from the
// environment so the managed-settings hook block is a single static line.
//
// Anti-tamper note: Claude Code hooks have NO native tamper protection — a user with
// write access to a lower-precedence settings file could add or alter hooks. The
// mitigation is to ship THIS hook in the enterprise MANAGED settings tier (the highest,
// non-overridable precedence) and to set allowManagedHooksOnly, so only managed hooks
// run. That is an operator/managed-settings posture, documented in the
// contract; this command is the governed endpoint client it points at.
func newClaudeHookCmd() *cobra.Command {
	var (
		endpoint string
		token    string
		tenant   string
		agent    string
		org      string
		account  string
		timeout  time.Duration
	)
	// resolveServer applies the --server/--endpoint precedence (E7); it is
	// assigned once the flags exist, and the RunE below closes over it.
	var resolveServer func() string
	cmd := &cobra.Command{
		Use:   "claude-hook",
		Short: "Governed PEP hook client: forward a Claude Code hook to the control plane and relay the decision (deny-closed)",
		Long: "claude-hook is the managed Claude Code PreToolUse/PostToolUse hook command.\n" +
			"It reads the hook payload from stdin, forwards it to the governed PEP endpoint, and\n" +
			"writes the allow/deny/ask decision to stdout. It is DENY-CLOSED: if the endpoint is\n" +
			"unset, unreachable or errors, it emits a deny so the agent's tool-call is blocked.\n\n" +
			"Configuration is read from the environment (overridable by flags):\n" +
			"  OLIVARES_HOOK_PEP_URL      governed PEP endpoint (e.g. http://127.0.0.1:8447/)\n" +
			"  OLIVARES_HOOK_PEP_TOKEN    the agent's PEP bearer credential\n" +
			"  OLIVARES_HOOK_PEP_TENANT   the tenant the agent acts in\n" +
			"  OLIVARES_HOOK_PEP_AGENT    the agent identity hint (firm-attribution refinement)\n" +
			"  OLIVARES_HOOK_PEP_ORG      the org identity hint\n" +
			"  OLIVARES_HOOK_PEP_ACCOUNT  the account identity hint",
		Example: `  # Forward one Claude Code hook payload using the managed environment
  printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := claude.HookClientConfig{
				Endpoint: resolveServer(),
				Token:    firstNonEmptyEnv(token, "OLIVARES_HOOK_PEP_TOKEN"),
				Tenant:   firstNonEmptyEnv(tenant, "OLIVARES_HOOK_PEP_TENANT"),
				Agent:    firstNonEmptyEnv(agent, "OLIVARES_HOOK_PEP_AGENT"),
				Org:      firstNonEmptyEnv(org, "OLIVARES_HOOK_PEP_ORG"),
				Account:  firstNonEmptyEnv(account, "OLIVARES_HOOK_PEP_ACCOUNT"),
				Timeout:  timeout,
				// La causa del deny-closed va a STDERR, nunca a stdout: stdout lleva el JSON que Claude
				// Code interpreta, y su campo de razón es un contrato. Sin esto, un certificado que no
				// verifica y un puerto cerrado eran el mismo mensaje.
				Diag: cmd.ErrOrStderr(),
			}
			// The decision travels in stdout, never the exit code (how Claude Code reads a
			// hook). A write error is the only failure surfaced.
			return claude.RunHookClient(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cfg)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "governed PEP URL (default $OLIVARES_HOOK_PEP_URL); --server is the canonical spelling")
	// E7: --server reaches this group too, without removing --endpoint.
	resolveServer = addServerAliasFlag(cmd, &endpoint, "endpoint", "OLIVARES_HOOK_PEP_URL", false)
	cmd.Flags().StringVar(&token, "token", "", "the agent's PEP bearer credential (default $OLIVARES_HOOK_PEP_TOKEN)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "the tenant the agent acts in (default $OLIVARES_HOOK_PEP_TENANT)")
	cmd.Flags().StringVar(&agent, "agent", "", "agent identity hint (default $OLIVARES_HOOK_PEP_AGENT)")
	cmd.Flags().StringVar(&org, "org", "", "org identity hint (default $OLIVARES_HOOK_PEP_ORG)")
	cmd.Flags().StringVar(&account, "account", "", "account identity hint (default $OLIVARES_HOOK_PEP_ACCOUNT)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "PEP request timeout")
	return cmd
}

// firstNonEmptyEnv returns flagVal if non-empty, else the first non-empty named
// environment variable.
func firstNonEmptyEnv(flagVal string, envs ...string) string {
	if flagVal != "" {
		return flagVal
	}
	for _, env := range envs {
		if value := os.Getenv(env); value != "" {
			return value
		}
	}
	return ""
}

func resolveTenant(flagVal string) (string, error) {
	tenant := firstNonEmptyEnv(flagVal, "OLIVARES_TENANT", "OLIVARES_HOOK_PEP_TENANT")
	if tenant == "" {
		return "", errors.New("tenant required: pass --tenant or set $OLIVARES_TENANT")
	}
	return tenant, nil
}
