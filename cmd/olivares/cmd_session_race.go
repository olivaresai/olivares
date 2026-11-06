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
	"os"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/spf13/cobra"
)

func newSessionRaceCmd() *cobra.Command {
	var cfg agentClientConfig
	var tools, promptFile, permission string
	cmd := &cobra.Command{
		Use:         "race [folder] --tools claude,codex --prompt <file>",
		Short:       "Compare one prompt across tools in separate worktrees",
		Long:        "Start each tool in its own worktree of the same repository commit. Each session keeps its normal approvals, audit and provider binding. The prompt is queued until the session may receive it.\n\nOpen the printed comparison link to inspect each diff and cost. Commit any uncommitted edits with the session's Git controls, then choose Keep this one. Keeping stops the sessions, fast-forwards the workspace to the selected branch, and discards the other worktrees only after the merge succeeds. Diverged or dirty work is preserved.\n\nIf a launch or message fails, already-created sessions are preserved and their comparison link is printed. --prompt - reads standard input.",
		Example:     "  olivares session race my-app --tools claude,codex --prompt ./prompt.md\n  olivares session race --tools claude,codex,grok --prompt -",
		Args:        cobra.MaximumNArgs(1),
		Annotations: map[string]string{"changes_data": "write"},
		RunE: func(cmd *cobra.Command, args []string) error {
			names := strings.Split(tools, ",")
			seen := map[string]bool{}
			if len(names) < 2 {
				return sentence(exitcode.Usage, "Choose at least two tools with --tools claude,codex.")
			}
			for i, tool := range names {
				tool = strings.ToLower(strings.TrimSpace(tool))
				if tool == "" || driverConfigHome(tool, "/") == "" || seen[tool] {
					return sentence(exitcode.Usage, "Name each supported tool once in --tools: claude,codex,grok,opencode,gemini-cli,cursor.")
				}
				names[i], seen[tool] = tool, true
			}
			if _, ok := sessions.ResolvePermission(permission); !ok {
				return sentence(exitcode.Usage, "Unknown permission %q.", permission)
			}
			if promptFile == "" {
				return sentence(exitcode.Usage, "Choose the prompt file with --prompt, or --prompt - for standard input.")
			}
			var reader io.Reader = cmd.InOrStdin()
			if promptFile != "-" {
				f, err := os.Open(promptFile)
				if err != nil {
					return sentence(exitcode.Usage, "Cannot read prompt: %v", err)
				}
				defer f.Close()
				reader = f
			}
			const maxPrompt = 128 * 1024
			raw, err := io.ReadAll(io.LimitReader(reader, maxPrompt+1))
			if err != nil {
				return sentence(exitcode.Usage, "Cannot read prompt: %v", err)
			}
			if len(raw) > maxPrompt || !utf8.Valid(raw) || strings.TrimSpace(string(raw)) == "" {
				return sentence(exitcode.Usage, "The prompt must contain text, valid UTF-8, at most 128 KiB.")
			}
			folder := "."
			if len(args) > 0 {
				folder = args[0]
			}
			if _, err = sessionFolder(folder); err != nil {
				return err
			}
			if err = cfg.resolve(); err != nil {
				return err
			}
			runs := []map[string]any{}
			refs := []string{}
			comparison := ""
			render := func() error {
				return renderOut(cmd, func(w io.Writer) error {
					waiting := false
					for _, run := range runs {
						state := str(run, "state")
						if state == sessionWaitingApproval {
							state, waiting = "waits for approval", true
						}
						if _, err := fmt.Fprintf(w, "%s (%s): %s [%s]\n", termSafe(sessionLabel(run)), termSafe(sessionDriver(run)), termSafe(state), termSafe(str(run, "run_ref"))); err != nil {
							return err
						}
					}
					if waiting {
						if _, err := fmt.Fprintln(w, "Approvals:", termSafe(cfg.approvalsPage())); err != nil {
							return err
						}
					}
					_, err := fmt.Fprintln(w, "Compare:", termSafe(comparison))
					return err
				}, map[string]any{"runs": runs, "comparison_url": comparison})
			}
			// A partial failure never discards work or hides the references that did start.
			fail := func(cause error) error {
				if len(runs) > 0 {
					if err := render(); err != nil {
						return fmt.Errorf("%w; could not print started sessions: %v", cause, err)
					}
				}
				return cause
			}
			base := ""
			for _, tool := range names {
				childCfg := &agentClientConfig{}
				var run map[string]any
				start := newSessionStartCmdWithResult(childCfg, nil, func(created map[string]any) error { run = created; return nil })
				// Keep the resolved connection and original credential flag provenance. No subprocess or credential argv.
				*childCfg = cfg
				start.SetContext(cmd.Context())
				start.SetErr(cmd.ErrOrStderr())
				start.SetIn(cmd.InOrStdin())
				flags := []string{"--tool", tool, "--name", "race-" + tool, "--worktree"}
				if cmd.Flags().Changed("permission") {
					flags = append(flags, "--permission", permission)
				}
				if base != "" {
					flags = append(flags, "--worktree-from", base)
				}
				if err := start.ParseFlags(flags); err != nil {
					return fail(err)
				}
				if err := start.RunE(start, []string{folder}); err != nil {
					return fail(err)
				}
				ref := str(run, "run_ref")
				if ref == "" {
					return fail(sentence(exitcode.Server, "The engine returned no session reference."))
				}
				runs = append(runs, run)
				refs = append(refs, ref)
				comparison = strings.TrimRight(cfg.server, "/") + "/agentops?race=" + url.QueryEscape(strings.Join(refs, ","))
				if base == "" {
					_, body, err := cfg.do(cmd.Context(), http.MethodGet, sessionRunsPath+"/"+url.PathEscape(ref)+"/diff", nil)
					if err != nil {
						return fail(err)
					}
					var diff sessionBranchList
					if err := json.Unmarshal(body, &diff); err != nil {
						return fail(err)
					}
					base = diff.Base
					if base == "" {
						return fail(sentence(exitcode.Server, "The engine returned no repository base commit."))
					}
				}
			}
			// Queue only after every branch has started at the captured common base. This
			// also persists the first turn while a launch waits for its normal approval.
			for _, ref := range refs {
				if _, _, err := cfg.do(cmd.Context(), http.MethodPost, sessionRunsPath+"/"+url.PathEscape(ref)+"/queue", map[string]any{"text": string(raw)}, http.StatusCreated); err != nil {
					return fail(err)
				}
			}
			return render()
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&tools, "tools", "", "comma-separated tools to compare")
	cmd.Flags().StringVar(&promptFile, "prompt", "", "prompt file; - reads standard input")
	cmd.Flags().StringVar(&permission, "permission", sessions.DefaultPermission, "normal session permission preset for every candidate")
	return cmd
}
