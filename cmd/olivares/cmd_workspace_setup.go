// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func newWorkspaceSetupCmd(approve bool) *cobra.Command {
	var cfg agentClientConfig
	var command, hash string
	name, short := "setup", "Read or store a folder's setup command (empty --command clears it)"
	long := "setup shows the setup command stored on a folder with its SHA-256 and approval, or stores one with --command. An empty --command clears it.\n\n" +
		"Storing a command does not approve or run it. A --worktree session start is refused until a person approves the SHA-256 of the exact text with approve-setup."
	example := "  olivares agent workspace setup acme\n  olivares agent workspace setup acme --command \"npm ci\""
	if approve {
		name, short = "approve-setup", "Approve the SHA-256 of the exact setup command you reviewed"
		long = "approve-setup approves the setup command stored on a folder by the SHA-256 of its exact text.\n\n" +
			"Read the command and its hash with setup first. Changing the text by one character voids the approval, and an agent identity cannot approve."
		example = "  olivares agent workspace approve-setup acme --sha256 <hash shown by setup>"
	}
	cmd := &cobra.Command{
		Use: name + " <ref>", Short: short, Long: long, Example: example, Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"changes_data": "write"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !approve && cmd.Flags().Changed("command") && !utf8.ValidString(command) {
				return sentence(exitcode.Usage, "The setup command must be valid UTF-8 text.")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			method, path := http.MethodGet, "/v1/m/sessions/workspaces/"+url.PathEscape(args[0])
			var body any
			if approve {
				method = http.MethodPost
				path += "/setup-command/approve"
				body = map[string]string{"sha256": hash}
			} else if cmd.Flags().Changed("command") {
				method = http.MethodPut
				path += "/setup-command"
				body = map[string]string{"command": command}
			}
			_, b, err := cfg.do(cmd.Context(), method, path, body, http.StatusOK)
			if err != nil {
				return err
			}
			var ws struct {
				Command  string `json:"setup_command"`
				Hash     string `json:"setup_command_sha256"`
				Approved bool   `json:"setup_approved"`
				Approval *struct {
					Actor string `json:"actor"`
					At    string `json:"at"`
				} `json:"setup_approval"`
			}
			if err := json.Unmarshal(b, &ws); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if ws.Command == "" {
					_, err := fmt.Fprintln(out, "No setup command.")
					return err
				}
				approval := "required"
				if ws.Approved && ws.Approval != nil {
					approval = "approved by " + termSafe(ws.Approval.Actor) + " at " + termSafe(ws.Approval.At)
				}
				_, err := fmt.Fprintf(out, "Command: %q\nSHA-256: %s\nApproval: %s\n", ws.Command, termSafe(ws.Hash), approval)
				return err
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	if approve {
		cmd.Flags().StringVar(&hash, "sha256", "", "SHA-256 of the exact command text reviewed (shown by workspace setup)")
		_ = cmd.MarkFlagRequired("sha256")
	} else {
		cmd.Flags().StringVar(&command, "command", "", "store this exact setup command; does not approve or execute it")
	}
	return cmd
}
