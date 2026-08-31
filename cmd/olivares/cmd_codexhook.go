// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/codex/session"
)

// newCodexHookCmd is the HOOK COMMAND Codex invokes (SG-01-Codex). Codex pipes the hook
// JSON to stdin and reads the decision from stdout; this command forwards the call to the
// governed PEP and relays the verdict, DENY-CLOSED on every failure.
//
// It is the sibling of claude-hook and deliberately NOT the same command with a flag,
// because the two engines differ in the part that matters most: the shape of the answer.
// Codex honors a DIFFERENT output shape per event, and emitting the wrong one produces no
// error and no warning — the verdict is simply ignored. connectors/codex/session owns that
// table; this command owns the process contract around it.
//
// Two things it does that claude-hook does not, both measured against codex-cli 0.145.0:
//
//   - It can exit 2. For an event this build has no verified shape for, the stdout shape is
//     a guess, so the exit code carries the veto as well — with the reason on stderr, which
//     Codex complains about when it is missing. On known events the exit code stays 0.
//   - It never emits permissionDecision "allow". Codex parses that value and rejects it, so
//     an allow is rendered as non-interference rather than as a grant we do not have.
//
// INSTALLATION. Codex reads $CODEX_HOME/hooks.json (measured: NOT hooks/hooks.json, and the
// inline [hooks] table in config.toml is not read at all by this build). `command` must be a
// STRING — with an array the file parses and the hook never runs, which is the most
// expensive silent failure in this surface:
//
//	{"description":"olivares governed hooks","hooks":{
//	  "SessionStart":[{"hooks":[{"type":"command","command":"olivares codex-hook"}]}],
//	  "PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"olivares codex-hook"}]}],
//	  "PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"olivares codex-hook"}]}],
//	  "SessionEnd":[{"hooks":[{"type":"command","command":"olivares codex-hook"}]}]}}
//
// ANTI-TAMPER. A hook the host does not TRUST does not run, and Codex says nothing when it
// skips one (measured: zero events, no warning). So the posture that makes this a control
// rather than a suggestion is allow_managed_hooks_only in the system-tier
// /etc/codex/requirements.toml, which connectors/codex-managed-config authors and whose
// absence now drifts HIGH. That production path is NOT VERIFIED in-container (pack
// SG-01-Codex-a); what IS verified is that without trust the hook silently does not fire.
func newCodexHookCmd() *cobra.Command {
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
		Use:   "codex-hook",
		Short: "Governed PEP hook client for Codex: forward a Codex hook to the control plane and relay the decision (deny-closed)",
		Long: "codex-hook is the managed Codex hook command.\n" +
			"It reads the hook payload from stdin, forwards it to the governed PEP, and writes the\n" +
			"decision to stdout IN THE SHAPE THAT EVENT HONORS — a PreToolUse deny is a\n" +
			"permissionDecision, a PostToolUse deny is a top-level block, and a Stop deny is\n" +
			"continue:false, because on Stop a \"block\" would keep the agent running.\n\n" +
			"It is DENY-CLOSED: if the endpoint is unset, unreachable, errors, or answers with an\n" +
			"empty body, it emits a deny of its own so the agent's tool-call is blocked.\n\n" +
			"Configuration is read from the environment (overridable by flags):\n" +
			"  OLIVARES_CODEX_HOOK_URL      governed PEP endpoint (e.g. http://127.0.0.1:8448/)\n" +
			"  OLIVARES_CODEX_HOOK_TOKEN    the agent's PEP bearer credential\n" +
			"  OLIVARES_CODEX_HOOK_TENANT   the tenant the agent acts in\n" +
			"  OLIVARES_CODEX_HOOK_AGENT    the agent identity hint\n" +
			"  OLIVARES_CODEX_HOOK_ORG      the org identity hint\n" +
			"  OLIVARES_CODEX_HOOK_ACCOUNT  the account identity hint",
		Example: `  # Forward one Codex hook payload using the managed environment
  printf '%s\n' '{"hook_event_name":"PreToolUse","session_id":"019f…","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-1"}' | olivares codex-hook`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := session.ClientConfig{
				Endpoint: resolveServer(),
				Token:    firstNonEmptyEnv(token, "OLIVARES_CODEX_HOOK_TOKEN"),
				Tenant:   firstNonEmptyEnv(tenant, "OLIVARES_CODEX_HOOK_TENANT"),
				Agent:    firstNonEmptyEnv(agent, "OLIVARES_CODEX_HOOK_AGENT"),
				Org:      firstNonEmptyEnv(org, "OLIVARES_CODEX_HOOK_ORG"),
				Account:  firstNonEmptyEnv(account, "OLIVARES_CODEX_HOOK_ACCOUNT"),
				Timeout:  timeout,
			}
			res := session.RunClient(cmd.Context(), cmd.InOrStdin(), cfg)
			// stdout carries the verdict. It is written FIRST and unconditionally: a hook
			// that writes nothing is read by Codex as "no objection", so there is no error
			// path on which staying silent is the right answer.
			if _, err := cmd.OutOrStdout().Write(res.Stdout); err != nil {
				return err
			}
			if res.Stderr != "" {
				_, _ = cmd.ErrOrStderr().Write([]byte(res.Stderr + "\n"))
			}
			if res.ExitCode != 0 {
				// Cobra would print and wrap an error; the exit code IS the signal here,
				// and the reason is already on stderr where Codex looks for it.
				os.Exit(res.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "governed PEP URL (default $OLIVARES_CODEX_HOOK_URL); --server is the canonical spelling")
	// E7: --server reaches this group too, without removing --endpoint.
	resolveServer = addServerAliasFlag(cmd, &endpoint, "endpoint", "OLIVARES_CODEX_HOOK_URL", false)
	cmd.Flags().StringVar(&token, "token", "", "the agent's PEP bearer credential (default $OLIVARES_CODEX_HOOK_TOKEN)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "the tenant the agent acts in (default $OLIVARES_CODEX_HOOK_TENANT)")
	cmd.Flags().StringVar(&agent, "agent", "", "agent identity hint (default $OLIVARES_CODEX_HOOK_AGENT)")
	cmd.Flags().StringVar(&org, "org", "", "org identity hint (default $OLIVARES_CODEX_HOOK_ORG)")
	cmd.Flags().StringVar(&account, "account", "", "account identity hint (default $OLIVARES_CODEX_HOOK_ACCOUNT)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "PEP request timeout")
	return cmd
}
