// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/modules/skills"
)

// sessionBranchList is GET /runs/{ref}/diff: the paths a worktree session's branch
// changed since it left the workspace's current commit.
type sessionBranchList struct {
	Branch    string `json:"branch"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	Truncated bool   `json:"truncated"`
	Files     []struct {
		Path   string `json:"path"`
		Status string `json:"status"`
	} `json:"files"`
}

// sessionBranchFile is GET /runs/{ref}/diff/file: one path's text at the base and at
// the branch tip.
type sessionBranchFile struct {
	Original  string `json:"original"`
	Modified  string `json:"modified"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

// sessionDiffEntry is one changed path with its diff, the form -o json prints.
type sessionDiffEntry struct {
	Path        string `json:"path"`
	Status      string `json:"status"`
	Binary      bool   `json:"binary,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Diff        string `json:"diff,omitempty"`
	DiffOmitted string `json:"diff_omitted,omitempty"`
}

type sessionDiffOutput struct {
	Branch    string             `json:"branch"`
	Base      string             `json:"base"`
	Head      string             `json:"head"`
	Truncated bool               `json:"truncated"`
	Files     []sessionDiffEntry `json:"files"`
}

func newSessionDiffCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "diff <session> [path...]",
		Short: "Show what a worktree session's branch changed",
		Long: "diff prints, as a unified diff, what the branch of a session started with --worktree changed\n" +
			"since it left the workspace's commit: the same changes the console's Session screen shows.\n" +
			"Name one or more paths to print only those. It reads committed work only; uncommitted edits\n" +
			"are not in it. To answer a line, send the agent a message: olivares session send.",
		Example: "  olivares session diff my-app\n" +
			"  olivares session diff my-app core/api/server.go\n" +
			"  olivares session diff my-app -o json",
		Annotations:       map[string]string{"changes_data": "read"},
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out, err := cfg.sessionDiff(cmd, run, args[1:])
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error { return printSessionDiff(w, out) }, out)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

// sessionDiff reads the branch's changed paths, then each wanted path's two texts, and
// renders them as the diff the skills module already writes for pack updates.
func (c *agentClientConfig) sessionDiff(cmd *cobra.Command, run map[string]any, only []string) (sessionDiffOutput, error) {
	base := sessionRunsPath + "/" + url.PathEscape(str(run, "run_ref")) + "/diff"
	_, raw, err := c.do(cmd.Context(), "GET", base, nil)
	if err != nil {
		return sessionDiffOutput{}, err
	}
	var list sessionBranchList
	if err := json.Unmarshal(raw, &list); err != nil {
		return sessionDiffOutput{}, sentence(exitcode.Err, "The engine's list of changes could not be read: %v", err)
	}
	out := sessionDiffOutput{Branch: list.Branch, Base: list.Base, Head: list.Head, Truncated: list.Truncated, Files: []sessionDiffEntry{}}
	wanted := make(map[string]bool, len(only))
	for _, p := range only {
		wanted[p] = true
	}
	budget := skills.MaxDiffTotalBytes
	for _, f := range list.Files {
		if len(only) > 0 && !wanted[f.Path] {
			continue
		}
		delete(wanted, f.Path)
		_, raw, err := c.do(cmd.Context(), "GET", base+"/file?path="+url.QueryEscape(f.Path), nil)
		if err != nil {
			return sessionDiffOutput{}, err
		}
		var file sessionBranchFile
		if err := json.Unmarshal(raw, &file); err != nil {
			return sessionDiffOutput{}, sentence(exitcode.Err, "The engine's text of %s could not be read: %v", termSafe(f.Path), err)
		}
		entry := sessionDiffEntry{Path: f.Path, Status: f.Status, Binary: file.Binary, Truncated: file.Truncated}
		if !file.Binary {
			entry.Diff, entry.DiffOmitted = skills.UnifiedTextDiff(f.Path, []byte(file.Original), []byte(file.Modified),
				f.Status != "added", f.Status != "deleted", &budget)
		}
		out.Files = append(out.Files, entry)
	}
	for _, p := range only {
		if wanted[p] {
			return sessionDiffOutput{}, sentence(exitcode.NotFound, "The branch of %s did not change %s. See what it changed: olivares session diff %s",
				sessionLabel(run), termSafe(p), shellWord(sessionLabel(run)))
		}
	}
	return out, nil
}

// diffOmittedWords say why a changed path has no text diff.
var diffOmittedWords = map[string]string{
	"too_large": "too large to compare",
	"limit":     "left out: the output limit was reached",
}

// printSessionDiff prints the range, then each path's unified diff.
func printSessionDiff(w io.Writer, d sessionDiffOutput) error {
	r := renderTo(w)
	r.Line(fmt.Sprintf("Branch %s: %s..%s", termSafe(d.Branch), shortCommit(d.Base), shortCommit(d.Head)))
	if len(d.Files) == 0 {
		r.Line("The branch has no changes.")
	}
	for _, f := range d.Files {
		switch {
		case f.Binary:
			r.Line(fmt.Sprintf("Binary file %s differs", termSafe(f.Path)))
		case f.DiffOmitted != "":
			r.Line(fmt.Sprintf("%s: no text diff, %s", termSafe(f.Path), diffOmittedWords[f.DiffOmitted]))
		default:
			if _, err := fmt.Fprint(w, termSafe(f.Diff)); err != nil {
				return err
			}
			if f.Truncated {
				r.Line(fmt.Sprintf("%s: only the first part of the file was read", termSafe(f.Path)))
			}
		}
	}
	if d.Truncated {
		r.Line(fmt.Sprintf("The branch changed more paths than the %d listed.", len(d.Files)))
	}
	return nil
}
