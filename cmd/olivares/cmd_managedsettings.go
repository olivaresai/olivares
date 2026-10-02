// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/managedsettings"
	"github.com/olivaresai/olivares/modules/sessions"
)

// cmd_managedsettings.go renders the Claude Code managed-settings.json that makes an
// OPERATED session's tool-calls PEP-governed. It is the STATIC half of the PEP
// injection: a non-overridable managed PreToolUse hook that
// runs `olivares claude-hook` before every tool, so the launched `claude` consults the
// governed PEP. The PER-SESSION half (the loopback URL + tenant/agent + bearer) is the
// OLIVARES_HOOK_PEP_* env the runner injects at launch (sessiongov.go) — it is NOT in
// this file, so the file is deployment-static and the operator distributes it once.
// Managed-only hosts use this managed hook. Other hosts use the protected
// per-session --settings hook with user/project/local setting sources excluded.
//
// Placement (the file must live at the OS-policy MANAGED tier, NOT under the session's
// HOME — the managed tier is the highest, non-overridable precedence, the anti-tamper
// property; a session cannot disable its own PEP hook):
//
//	Linux:   /etc/claude-code/managed-settings.json
//	macOS:   /Library/Application Support/ClaudeCode/managed-settings.json
//	Windows: C:\ProgramData\ClaudeCode\managed-settings.json
//
// DENY-CLOSED: a hook with no command fails the render (never a hollow PEP); without
// the per-session OLIVARES_HOOK_PEP_* env the hook runs but is deny-closed (every
// tool-call denied) — the session can think but not act until PEP-provisioned.

// newAgentManagedSettingsCmd renders the managed-settings.json for governed sessions.
func newAgentManagedSettingsCmd() *cobra.Command {
	var (
		pepCommand   string
		out          string
		matcher      string
		timeoutSecs  int
		redact       bool
		noHook       bool
		otelEndpoint string
		gatewayURL   string
	)
	cmd := &cobra.Command{
		Use:   "managed-settings",
		Short: "Render the Claude Code managed-settings.json that governs operated sessions (PEP hook)",
		Long: "managed-settings renders the non-overridable Claude Code managed-settings.json that makes an\n" +
			"operated session's tool-calls pass the governed PEP: a managed PreToolUse hook running\n" +
			"`olivares claude-hook`. User and project hooks are restricted to preserve the PEP's\n" +
			"approved tool input. Place the output at the OS-policy managed path (Linux: /etc/claude-code/\n" +
			"managed-settings.json). The per-session PEP endpoint/bearer is injected as the\n" +
			"OLIVARES_HOOK_PEP_* environment at launch (governed by Olivares), not written here.",
		Example: `  # Write the managed policy to the Linux system path
  sudo olivares agent managed-settings --out /etc/claude-code/managed-settings.json

  # Preview a policy that also pins the governed inference gateway
  olivares agent managed-settings --gateway-base-url https://olivares.internal/v1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pol := managedsettings.Policy{AllowManagedHooksOnly: true}
			if !noHook {
				defaultClient := !cmd.Flags().Changed("pep-command")
				hookTimeout := timeoutSecs
				if defaultClient && cmd.Flags().Changed("timeout") {
					// Published explicit timeouts remain valid. Leave time for a
					// refusal to reach stdout before Claude discards a timed-out hook.
					clientTimeout := 5 * time.Second
					if hookTimeout > 0 && hookTimeout <= 5 {
						clientTimeout = time.Duration(hookTimeout) * time.Second / 2
					}
					pepCommand = "olivares claude-hook --timeout " + clientTimeout.String()
					if hookTimeout > 0 && hookTimeout <= int(sessions.ClaudeHookPEPClientTimeout/time.Second) {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: approvals needing longer than the explicit %ds hook timeout are denied (client deadline %s).\n", hookTimeout, clientTimeout)
					}
				}
				if !defaultClient && !cmd.Flags().Changed("timeout") {
					// Explicit custom PEP commands keep their existing timeout.
					hookTimeout = 5
				}
				hooks, err := managedsettings.PEPHook(managedsettings.PEPHookConfig{
					Command:     pepCommand,
					Matcher:     matcher, // "" = every tool (deny-closed coverage)
					TimeoutSecs: hookTimeout,
					Redact:      redact,
				})
				if err != nil {
					return err
				}
				if defaultClient {
					for event, matchers := range hooks {
						for i := range matchers {
							// PEPHook shares its command slice between events.
							// Each invocation needs its own immutable event pin.
							commands := append([]managedsettings.HookCommand(nil), matchers[i].Hooks...)
							for j := range commands {
								commands[j].Command += " --hook-event " + event
							}
							matchers[i].Hooks = commands
						}
						hooks[event] = matchers
					}
				}
				pol.Hooks = hooks
			}
			if otelEndpoint != "" {
				// The sanctioned OBSERVE path: turn Claude Code's OTEL export on toward the
				// control-plane collector, content capture OFF (minimal-data default).
				pol.Env = managedsettings.TelemetryEnv(managedsettings.TelemetryConfig{Endpoint: otelEndpoint})
			}
			gatewayURL = strings.TrimSpace(gatewayURL)
			if gatewayURL != "" {
				if pol.Env == nil {
					pol.Env = make(map[string]string)
				}
				// The endpoint-managed env tier is non-overridable by a user/project
				// setting. Pinning the governed gateway here closes the documented
				// ANTHROPIC_BASE_URL route-around for deployments claiming proxy PEP.
				pol.Env[managedsettings.EnvBaseURL] = gatewayURL
			}
			if bypassed, reason := managedsettings.ServerTierBypassed(os.LookupEnv); bypassed {
				ambient := strings.TrimSpace(os.Getenv(managedsettings.EnvBaseURL))
				if gatewayURL == "" || ambient == "" || ambient != gatewayURL {
					// Never print the URL: it may contain inline credentials.
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: governed managed-settings detected an unmanaged inference route override: %s\n", reason)
				}
			}
			b, err := managedsettings.Render(pol)
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, werr := cmd.OutOrStdout().Write(append(b, '\n'))
				return werr
			}
			return os.WriteFile(out, b, 0o644)
		},
	}
	cmd.Flags().StringVar(&pepCommand, "pep-command", "olivares claude-hook --timeout "+sessions.ClaudeHookPEPClientTimeout.String(), "the managed PEP-client command (explicit custom commands are preserved)")
	cmd.Flags().StringVar(&out, "out", "-", "output path ('-' = stdout)")
	cmd.Flags().StringVar(&matcher, "matcher", "", "tool-name matcher for the PEP hook (\"\" = all tools)")
	cmd.Flags().IntVar(&timeoutSecs, "timeout", int(sessions.ClaudeHookPEPCommandTimeout/time.Second), "outer hook timeout in seconds (omitted: 180s with a 120s client; explicit: published 5s client shortened if needed)")
	cmd.Flags().BoolVar(&redact, "redact", true, "also install the paired PostToolUse output-redaction hook")
	cmd.Flags().BoolVar(&noHook, "no-hook", false, "render env/telemetry only, no PEP hook")
	cmd.Flags().StringVar(&otelEndpoint, "otel-endpoint", "", "managed OTEL collector endpoint (enables the sanctioned telemetry env)")
	cmd.Flags().StringVar(&gatewayURL, "gateway-base-url", "", "managed ANTHROPIC_BASE_URL pin for the governed Olivares inference gateway")
	return cmd
}
