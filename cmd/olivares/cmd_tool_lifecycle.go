// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/spf13/cobra"
)

func newToolUpdateCmd() *cobra.Command {
	cmd := newToolInstallCmd()
	cmd.Use = "update <claude|codex|grok|opencode|gemini|ollama>"
	cmd.Short = "Update an agent tool from its verified official release"
	cmd.Long = "Update installs the requested official release through the verified installer.\nExisting versions and workspace pins are retained."
	cmd.Example = "  olivares tool update codex\n  olivares tool update claude --version 2.1.261"
	return cmd
}

func toolDriver(raw string) string {
	driver := strings.ToLower(strings.TrimSpace(raw))
	if driver == "gemini" {
		return "gemini-cli"
	}
	return driver
}

func newToolRemoveCmd() *cobra.Command {
	var cfg agentClientConfig
	var version string
	cmd := &cobra.Command{
		Use: "rm <tool> --version <version>", Aliases: []string{"remove"},
		Short:   "Remove an installed tool version when no session uses it",
		Long:    "Remove an installed tool version from the engine.\nRemoval is refused while a session uses that version.",
		Example: "  olivares tool rm codex --version 0.134.0",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			driver := toolDriver(args[0])
			status, b, err := cfg.do(cmd.Context(), "DELETE", agentToolsPath+"/releases", map[string]any{"driver": driver, "version": version}, http.StatusOK)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return toolHTTPErr(status, b)
			}
			var result map[string]any
			if err := json.Unmarshal(b, &result); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "Removed %s %s.\n", toolName(driver), version)
				return err
			}, result)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&version, "version", "", "exact installed version to remove")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func newToolPinCmd() *cobra.Command {
	var cfg agentClientConfig
	var version, workspace, agent string
	var clear bool
	cmd := &cobra.Command{
		Use:     "pin <tool> --workspace <ref> --version <version>",
		Short:   "Select a session tool version for a workspace or agent",
		Long:    "Select an installed version for this organization's workspace or agent.\nAn agent selection takes precedence. Use --clear to return to the default.",
		Example: "  olivares tool pin codex --workspace default --version 0.134.0\n  olivares tool pin codex --agent my-agent --version 0.134.0\n  olivares tool pin codex --workspace default --clear",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if workspace == "" && agent == "" {
				return fmt.Errorf("choose --workspace or --agent")
			}
			if (!clear && version == "") || (clear && version != "") {
				return fmt.Errorf("choose --version or --clear")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			body := map[string]any{"tenant_id": cfg.tenant, "driver": toolDriver(args[0]), "version": version}
			if workspace != "" {
				body["workspace_ref"] = workspace
			}
			if agent != "" {
				body["agent_ref"] = agent
			}
			status, b, err := cfg.do(cmd.Context(), "PUT", agentToolsPath+"/pins", body, http.StatusOK)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return toolHTTPErr(status, b)
			}
			return renderOut(cmd, func(out io.Writer) error { _, err := fmt.Fprintln(out, "Session tool version saved."); return err }, map[string]any{"ok": true})
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&workspace, "workspace", "", "registered session workspace reference")
	cmd.Flags().StringVar(&agent, "agent", "", "agent ID in this organization")
	cmd.Flags().StringVar(&version, "version", "", "exact installed version for new and resumed sessions")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear this version selection")
	return cmd
}

func doctorToolVersionOverrides(getenv func(string) string) []doctorCheck {
	var checks []doctorCheck
	for _, facts := range driverfacts.All() {
		if facts.RuntimeBinEnv == "" || strings.TrimSpace(getenv(facts.RuntimeBinEnv)) == "" {
			continue
		}
		checks = append(checks, doctorCheck{Name: "tool-version-override-" + facts.Key, Status: "warn", Detail: facts.RuntimeBinEnv + " overrides stored workspace and agent tool versions.", Remediation: "Unset " + facts.RuntimeBinEnv + " to use the version selected in AI tools."})
	}
	return checks
}
