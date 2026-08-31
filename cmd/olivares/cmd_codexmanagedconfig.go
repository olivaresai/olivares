// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	codexmanagedconfig "github.com/olivaresai/olivares/connectors/codex-managed-config"
)

// cmd_codexmanagedconfig.go renders the OpenAI Codex managed-config files that constrain a
// fleet's Codex usage (gap G4/C2) — the Codex sibling of `olivares agent
// managed-settings` (which renders Claude Code's managed-settings.json). It turns a
// governance-authored Policy JSON into the two distributable TOML artifacts:
//
//   - requirements.toml   — admin-enforced CONSTRAINTS users cannot override (the
//     non-overridable layer): allowed approval policies / sandbox modes / web-search
//     modes, remote-control + managed-hooks-only lockdowns, deny_read, [features] pins,
//     the [mcp_servers] allowlist, the guardian (automatic-review) policy.
//   - managed_config.toml — managed DEFAULTS (the starting values, same schema as the
//     user config.toml): approval_policy, sandbox_mode, web_search, network_access,
//     experimental_network egress, and the [otel] telemetry pins.
//
// Placement (VERIFIED 2026-06-20). Distribute the rendered files to the SYSTEM tier, or
// base64-encode them into the macOS MDM payload:
//
//	requirements.toml    Unix /etc/codex/requirements.toml      · macOS MDM com.openai.codex:requirements_toml_base64
//	managed_config.toml  Unix /etc/codex/managed_config.toml    · macOS MDM com.openai.codex:config_toml_base64
//
// DENY-CLOSED: the rendered files are VALIDATED before they are written; a policy that
// would emit an invalid constraint (an unknown enum value, an MCP entry with no identity)
// FAILS the render rather than distributing a governance file that enforces nothing.
func newCodexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Author OpenAI Codex governance artifacts (managed config)",
		Long: "codex renders the configuration files that put an OpenAI Codex install under the\n" +
			"same governance Policy the rest of the fleet answers to, so the policy is\n" +
			"authored once and expressed per vendor rather than maintained twice.\n\n" +
			"It writes artifacts; it does not talk to a control plane.",
		Example: "  olivares codex managed-config --policy policy.json --validate",
	}
	cmd.AddCommand(newCodexManagedConfigCmd())
	return cmd
}

// newCodexManagedConfigCmd renders requirements.toml + managed_config.toml from a Policy JSON.
func newCodexManagedConfigCmd() *cobra.Command {
	var (
		policyPath       string
		requirementsOut  string
		managedConfigOut string
		validateOnly     bool
	)
	cmd := &cobra.Command{
		Use:   "managed-config",
		Short: "Render the Codex requirements.toml + managed_config.toml from a governance Policy JSON",
		Long: "managed-config renders the two Codex managed-config artifacts from a governance-authored\n" +
			"Policy JSON ({\"requirements\": {...}, \"managed_config\": {...}}; read from --policy or stdin):\n" +
			"requirements.toml (non-overridable constraints) and managed_config.toml (managed defaults).\n" +
			"Both are validated before writing — an invalid policy fails the render (deny-closed). Place\n" +
			"the output at the system tier (Linux: /etc/codex/) or base64-encode it into the macOS MDM\n" +
			"payload (com.openai.codex:requirements_toml_base64 / config_toml_base64).",
		Example: `  # Validate without writing files
  olivares codex managed-config --policy policy.json --validate

  # Render both system-tier files
  olivares codex managed-config --policy policy.json \
    --requirements-out /etc/codex/requirements.toml \
    --managed-config-out /etc/codex/managed_config.toml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := readPolicyInput(cmd, policyPath)
			if err != nil {
				return err
			}
			var pol codexmanagedconfig.Policy
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&pol); err != nil {
				return fmt.Errorf("invalid policy JSON: %w", err)
			}

			reqTOML, err := codexmanagedconfig.RenderRequirements(pol)
			if err != nil {
				return fmt.Errorf("render requirements.toml: %w", err)
			}
			mcTOML, err := codexmanagedconfig.RenderManagedConfig(pol)
			if err != nil {
				return fmt.Errorf("render managed_config.toml: %w", err)
			}
			reqAuthored := strings.TrimSpace(string(reqTOML)) != ""
			mcAuthored := strings.TrimSpace(string(mcTOML)) != ""
			if !reqAuthored && !mcAuthored {
				return fmt.Errorf("the policy authors no requirements and no managed defaults (nothing to render)")
			}
			// Deny-closed: never emit a governance file that does not validate.
			if reqAuthored {
				if issues := codexmanagedconfig.ValidateRequirementsTOML(reqTOML); len(issues) > 0 {
					return fmt.Errorf("authored requirements.toml is invalid:\n  - %s", strings.Join(issues, "\n  - "))
				}
			}
			if mcAuthored {
				if issues := codexmanagedconfig.ValidateManagedConfigTOML(mcTOML); len(issues) > 0 {
					return fmt.Errorf("authored managed_config.toml is invalid:\n  - %s", strings.Join(issues, "\n  - "))
				}
			}
			if validateOnly {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "ok: policy renders to valid Codex managed-config TOML")
				return nil
			}

			if reqAuthored {
				if err := emitTOML(cmd, "requirements.toml", reqTOML, requirementsOut); err != nil {
					return err
				}
			}
			if mcAuthored {
				if err := emitTOML(cmd, "managed_config.toml", mcTOML, managedConfigOut); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&policyPath, "policy", "-", "path to the governance Policy JSON ('-' = stdin)")
	cmd.Flags().StringVar(&requirementsOut, "requirements-out", "-", "output path for requirements.toml ('-' = stdout, prefixed with a header when both files go to stdout)")
	cmd.Flags().StringVar(&managedConfigOut, "managed-config-out", "-", "output path for managed_config.toml ('-' = stdout)")
	cmd.Flags().BoolVar(&validateOnly, "validate", false, "validate the policy renders to valid TOML, but write nothing")
	return cmd
}

// readPolicyInput reads the policy JSON from a file path or stdin ('-'/”).
func readPolicyInput(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "" || path == "-" {
		b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4<<20))
		if err != nil {
			return nil, fmt.Errorf("read policy from stdin: %w", err)
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			return nil, fmt.Errorf("no policy provided (pass --policy <file> or pipe JSON on stdin)")
		}
		return b, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied policy path
	if err != nil {
		return nil, fmt.Errorf("read policy file: %w", err)
	}
	return b, nil
}

// emitTOML writes rendered TOML to a path, or to stdout with a `# <name>` header (so a
// reader can tell the two files apart when both stream to stdout).
func emitTOML(cmd *cobra.Command, name string, content []byte, out string) error {
	if out == "" || out == "-" {
		w := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(w, "# %s\n", name)
		_, err := w.Write(append(content, '\n'))
		return err
	}
	return os.WriteFile(out, content, 0o644) //nolint:gosec // operator-distributed managed file (world-readable by design)
}
