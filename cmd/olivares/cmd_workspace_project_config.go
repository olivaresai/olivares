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
	"sort"

	"github.com/spf13/cobra"
)

// newWorkspaceProjectConfigCmd is "workspace project-config <ref>": the agent
// configuration a folder's repository carries that runs programs or redirects
// traffic, read and never executed, with the SHA-256 a start waits for.
func newWorkspaceProjectConfigCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "project-config <ref>",
		Short:       "List the folder's repo-resident agent configuration that runs programs or redirects traffic",
		Long: "project-config lists the agent configuration the workspace's repository carries that runs programs or redirects traffic.\n" +
			"The files are read and never executed. When it lists anything, the report ends with the SHA-256 a session start\n" +
			"waits for and whether that exact content is approved; approve it with approve-project-config.",
		Example: "  olivares agent workspace project-config ws-123\n" +
			"  olivares agent workspace project-config ws-123 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			_, b, err := cfg.do(cmd.Context(), http.MethodGet, "/v1/m/sessions/workspaces/"+url.PathEscape(args[0])+"/project-config", nil, http.StatusOK)
			if err != nil {
				return err
			}
			return printProjectConfig(cmd, b)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

// newWorkspaceApproveProjectConfigCmd is "workspace approve-project-config".
func newWorkspaceApproveProjectConfigCmd() *cobra.Command {
	var cfg agentClientConfig
	var hash string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "approve-project-config <ref>",
		Short:       "Approve the SHA-256 of the exact project agent configuration you reviewed",
		Long: "approve-project-config records your approval of one project agent configuration, identified by its SHA-256.\n" +
			"A session whose tool would honor that configuration does not start until the hash shown by project-config is approved;\n" +
			"a change to the flagged configuration gives a new hash and needs a new review and approval.",
		Example: "  olivares agent workspace approve-project-config ws-123 --sha256 <sha256>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			_, b, err := cfg.do(cmd.Context(), http.MethodPost, "/v1/m/sessions/workspaces/"+url.PathEscape(args[0])+"/project-config/approve", map[string]string{"sha256": hash}, http.StatusOK)
			if err != nil {
				return err
			}
			var approval struct {
				SHA256 string `json:"sha256"`
				Actor  string `json:"actor"`
				At     string `json:"at"`
			}
			if err := json.Unmarshal(b, &approval); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "Approved %s by %s at %s.\n", termSafe(approval.SHA256), termSafe(approval.Actor), termSafe(approval.At))
				return err
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&hash, "sha256", "", "SHA-256 of the exact configuration reviewed (shown by workspace project-config)")
	_ = cmd.MarkFlagRequired("sha256")
	return cmd
}

func printProjectConfig(cmd *cobra.Command, raw []byte) error {
	var report struct {
		Files []struct {
			File       string         `json:"file"`
			Flagged    map[string]any `json:"flagged"`
			Unreadable string         `json:"unreadable"`
			Refused    string         `json:"refused"`
		} `json:"files"`
		SHA256   string `json:"sha256"`
		Approved bool   `json:"approved"`
		Approval *struct {
			Actor string `json:"actor"`
			At    string `json:"at"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return err
	}
	return renderOut(cmd, func(out io.Writer) error {
		if len(report.Files) == 0 {
			_, err := fmt.Fprintln(out, "No agent configuration that runs programs or redirects traffic.")
			return err
		}
		for _, f := range report.Files {
			fmt.Fprintln(out, termSafe(f.File))
			if f.Unreadable != "" {
				fmt.Fprintf(out, "  cannot be read: %s\n", termSafe(f.Unreadable))
			}
			if f.Refused != "" {
				fmt.Fprintf(out, "  cannot be approved: %s\n", termSafe(f.Refused))
			}
			keys := make([]string, 0, len(f.Flagged))
			for key := range f.Flagged {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				value, _ := json.Marshal(f.Flagged[key])
				fmt.Fprintf(out, "  %s: %s\n", termSafe(key), termSafe(string(value)))
			}
		}
		approval := "required before a session reads it"
		switch {
		case report.Approved && report.Approval != nil:
			approval = "approved by " + termSafe(report.Approval.Actor) + " at " + termSafe(report.Approval.At)
		case report.Approval != nil:
			approval = "required: the last approval covers other content"
		}
		_, err := fmt.Fprintf(out, "SHA-256: %s\nApproval: %s\n", termSafe(report.SHA256), approval)
		return err
	}, json.RawMessage(raw))
}
