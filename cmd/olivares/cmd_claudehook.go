// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/connectors/claude"
)

// The managed hook command uses this process-initialization timestamp, before
// command setup or hook input. Programmatic command tests start at construction.
var claudeHookProcessStartedAt = time.Now()

// newClaudeHookCmd forwards Claude Code's hook input to the engine and returns
// an explicit permission decision. Launched sessions pin the engine-assigned
// endpoint in their protected run settings; the bearer stays in the child env.
func newClaudeHookCmd() *cobra.Command {
	startedAt := time.Now()
	if len(os.Args) > 1 && os.Args[1] == "claude-hook" {
		startedAt = claudeHookProcessStartedAt
	}
	var (
		endpoint  string
		token     string
		tenant    string
		agent     string
		org       string
		account   string
		hookEvent string
		timeout   time.Duration
	)
	// resolveServer applies the --server/--endpoint precedence (E7); it is
	// assigned once the flags exist, and the RunE below closes over it.
	var resolveServer func() string
	cmd := &cobra.Command{
		Use:   "claude-hook",
		Short: "Governed PEP hook client: forward a Claude Code hook to the engine and relay the decision (deny-closed)",
		Long: "claude-hook is the managed Claude Code PreToolUse/PostToolUse hook command.\n" +
			"It reads the hook payload from stdin, forwards it to the governed PEP endpoint, and\n" +
			"writes the allow/deny/ask decision to stdout. It is DENY-CLOSED: if the endpoint is\n" +
			"unset, unreachable or errors, it emits a deny so the agent's tool-call is blocked.\n\n" +
			"Olivares installs this command in each session's protected settings file, which pins\n" +
			"the endpoint assigned by the engine. The session bearer stays in its environment.\n\n" +
			"Standalone configuration (overridable by flags):\n" +
			"  OLIVARES_HOOK_PEP_URL      PEP endpoint assigned by the engine\n" +
			"  OLIVARES_HOOK_PEP_TOKEN    the agent's PEP bearer credential\n" +
			"  OLIVARES_HOOK_PEP_TENANT   the tenant the agent acts in\n" +
			"  OLIVARES_HOOK_PEP_AGENT    the agent identity hint (firm-attribution refinement)\n" +
			"  OLIVARES_HOOK_PEP_ORG      the org identity hint\n" +
			"  OLIVARES_HOOK_PEP_ACCOUNT  the account identity hint",
		Example: `  # Forward one Claude Code hook payload using the managed environment
  printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clientTimeout := timeout
			if !cmd.Flags().Changed("timeout") {
				// The published managed command has no flags and a 5s outer
				// timeout. Leave headroom for its denial to reach stdout.
				clientTimeout /= 2
			}
			cfg := claude.HookClientConfig{
				Endpoint:      resolveServer(),
				Token:         firstNonEmptyEnv(token, "OLIVARES_HOOK_PEP_TOKEN"),
				Tenant:        firstNonEmptyEnv(tenant, "OLIVARES_HOOK_PEP_TENANT"),
				Agent:         firstNonEmptyEnv(agent, "OLIVARES_HOOK_PEP_AGENT"),
				Org:           firstNonEmptyEnv(org, "OLIVARES_HOOK_PEP_ORG"),
				Account:       firstNonEmptyEnv(account, "OLIVARES_HOOK_PEP_ACCOUNT"),
				Timeout:       clientTimeout,
				StartedAt:     startedAt,
				ExpectedEvent: hookEvent,
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
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "whole hook deadline, including stdin and filesystem resolution")
	cmd.Flags().StringVar(&hookEvent, "hook-event", "", "invoking event pinned by managed settings for deadline refusals")
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

// resolveTenant reads the tenant from the flag or the environment, and refuses
// when neither names one.
//
// The refusal is a USAGE error, and classifying it HERE rather than at each
// caller is the whole point: this one free function is the tenant gate for 19
// CLI call sites across 7 files, and every one of them returns its error
// unchanged. Before this, all 19 exited 1 — "generic failure with no more
// specific classification" — while a missing required flag on a sibling command
// exited 2 through cobra's own machinery, so a script could not tell "you forgot
// --tenant" from "the ledger is broken". The sentence is unchanged: exitcode
// carries the code and delegates Error() to the wrapped error, so the two tests
// that compare this string still compare the same string.
func resolveTenant(flagVal string) (string, error) {
	tenant := firstNonEmptyEnv(flagVal, "OLIVARES_TENANT", "OLIVARES_HOOK_PEP_TENANT")
	if tenant == "" {
		tenant = localContextTenant()
	}
	if tenant == "" {
		return "", exitcode.New(exitcode.Usage, errors.New("tenant required: pass --tenant or set $OLIVARES_TENANT"))
	}
	return tenant, nil
}
