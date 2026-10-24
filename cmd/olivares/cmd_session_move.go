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

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// handoffSummaryFileMax bounds a reviewed summary read from a file, matching what
// the engine accepts.
const handoffSummaryFileMax = 64 << 10

// newSessionMoveCmd is `olivares session move`: a stopped session continues on
// another account of the same tool, in the same session, with its conversation.
func newSessionMoveCmd() *cobra.Command {
	var cfg agentClientConfig
	var account string
	var resume bool
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "move <session> --account <account>",
		Short:       "Move a stopped session to another account of the same tool",
		Long: "move continues a stopped Claude Code session on another account of the same tool, for\n" +
			"example when the first account is out of quota. It carries that one conversation (the\n" +
			"transcript and its subagent files) to the account's home and keeps the same session, its\n" +
			"history and its record, which gains a \"moved\" event. The account's login is never copied,\n" +
			"and the new account's own limits and approvals apply from the move on. Nothing starts: resume\n" +
			"the session next, or pass --resume. The session must be stopped, and the account must be\n" +
			"of the same tool: to continue in another tool, use olivares session handoff.",
		Example: "  olivares session move my-app --account work-2\n" +
			"  olivares session move my-app --account work-2 --resume",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(account) == "" {
				return sentence(exitcode.Usage, "Name the account to move to with --account (olivares provider account ls).")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			target, err := cfg.findToolAccount(cmd.Context(), account)
			if err != nil {
				return err
			}
			base := sessionRunsPath + "/" + url.PathEscape(str(run, "run_ref"))
			_, b, err := cfg.do(cmd.Context(), "POST", base+"/move",
				map[string]any{"provider_profile_ref": target.Ref}, http.StatusOK)
			if err != nil {
				return err
			}
			// The engine answered 200: the move is done and recorded. An answer this
			// client cannot read must not turn that into a failure the person retries.
			moved := map[string]any{}
			if json.Unmarshal(b, &moved) != nil || moved["run_ref"] == nil {
				moved = run
			}
			if resume {
				_, b, err := cfg.do(cmd.Context(), "POST", base+"/resume", nil, http.StatusOK)
				if err != nil {
					// The move is done; only the resume failed, and its own exit class
					// stays (a refusal is a conflict, an unreachable engine is not).
					return sentence(exitcode.From(err), "Moved %s to account %s, but it did not resume: %v. Resume it once that is fixed: olivares session resume %s",
						sessionLabel(run), termSafe(target.Name), err, shellWord(sessionLabel(run)))
				}
				resumed := map[string]any{}
				if json.Unmarshal(b, &resumed) == nil && resumed["run_ref"] != nil {
					moved = resumed
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				if resume {
					_, err := fmt.Fprintf(w, "Moved %s to account %s and resumed it.\n", sessionLabel(moved), termSafe(target.Name))
					return err
				}
				if str(moved, "claude_session_id") == "" {
					// Nothing was carried: it had no conversation yet, and says so.
					fmt.Fprintf(w, "Moved %s to account %s. It has no conversation yet, so it starts again there. ",
						sessionLabel(moved), termSafe(target.Name))
				} else {
					fmt.Fprintf(w, "Moved %s to account %s. ", sessionLabel(moved), termSafe(target.Name))
				}
				_, err := fmt.Fprintf(w, "Resume it: olivares session resume %s\n", shellWord(sessionLabel(moved)))
				return err
			}, moved)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&account, "account", "", "the account to move to (its name or profile reference; see olivares provider account ls)")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume the session on the account right after the move")
	return cmd
}

// newSessionHandoffCmd is `olivares session handoff`. Without --account it only
// shows the summary a handoff would start from; with it, it starts the linked
// session.
func newSessionHandoffCmd() *cobra.Command {
	var cfg agentClientConfig
	var account, summaryFile, name string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "handoff <session> [--account <account>]",
		Short:       "Continue a session in another tool or account as a new, linked session",
		Long: "handoff starts a NEW session in another account, of any tool, that begins from a summary of\n" +
			"this one and has its full transcript attached. Use it to continue in another tool: only a\n" +
			"move keeps the same session, and only inside one tool. The new session is linked to this\n" +
			"one, and this one's record says where it went.\n\n" +
			"Without --account it prints the summary and starts nothing, so you can review it. The summary\n" +
			"is generated from the stored conversation, with no model. Save it, edit it, and start with\n" +
			"--summary-file <file> (or - for standard input); without the file the generated one is used.\n" +
			"A session that was given vault secrets, a repository credential, a managed credential or a\n" +
			"package registry token cannot be handed off. A running session is stopped first (Claude Code,\n" +
			"Codex, Gemini CLI and OpenCode sessions can be handed off); one that cannot be handed off is\n" +
			"not stopped.",
		Example: "  olivares session handoff my-app\n" +
			"  olivares session handoff my-app --account codex-main\n" +
			"  olivares session handoff my-app --account codex-main --summary-file summary.txt --name my-app-codex",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			base := sessionRunsPath + "/" + url.PathEscape(str(run, "run_ref")) + "/handoff"
			if strings.TrimSpace(account) == "" {
				return previewHandoff(cmd, &cfg, base, run)
			}
			target, err := cfg.findToolAccount(cmd.Context(), account)
			if err != nil {
				return err
			}
			body := map[string]any{"provider_profile_ref": target.Ref}
			if strings.TrimSpace(name) != "" {
				body["name"] = strings.TrimSpace(name)
			}
			if summaryFile != "" {
				text, err := readSummaryFile(cmd, summaryFile)
				if err != nil {
					return err
				}
				body["summary"] = text
			}
			_, b, err := cfg.do(cmd.Context(), "POST", base, body, http.StatusCreated, http.StatusAccepted)
			if err != nil {
				return err
			}
			started := map[string]any{}
			if json.Unmarshal(b, &started) != nil || started["run_ref"] == nil {
				// 201 or 202: the session exists. Retrying would start a second one.
				return sentence(exitcode.Conflict, "The engine started the new session, but its answer could not be read. "+
					"Find it with: olivares session ls (the original, %s, now names it in its record)", sessionLabel(run))
			}
			state := str(started, "summary_state")
			err = renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Started %s on account %s from %s.\n", sessionLabel(started), termSafe(target.Name), sessionLabel(run))
				if err != nil || state == "delivered" {
					return err
				}
				switch state {
				case "waits_for_approval":
					_, err = fmt.Fprintln(w, "It waits for an approval before it starts, and its first message is not sent automatically: once it runs, send the text in the -o json output (first_turn) with olivares session send.")
				case "unknown":
					_, err = fmt.Fprintf(w, "Its first message may have arrived. Look at olivares session follow %s before sending it again.\n", shellWord(sessionLabel(started)))
				default:
					_, err = fmt.Fprintf(w, "Its first message did not arrive. The text is in the -o json output (first_turn): send it with olivares session send %s.\n", shellWord(sessionLabel(started)))
				}
				return err
			}, started)
			if err != nil {
				return err
			}
			// A session that started without its summary is not a clean success for a
			// script: the exit says so. Waiting for an approval is an ordinary state.
			if state == "not_delivered" || state == "unknown" {
				return sentence(exitcode.Conflict, "%s started without its first message (%s)", sessionLabel(started), state)
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&account, "account", "", "the account to continue in (its name or profile reference); without it the summary is only shown")
	cmd.Flags().StringVar(&summaryFile, "summary-file", "", "start from this reviewed summary instead of the generated one (- reads standard input)")
	cmd.Flags().StringVar(&name, "name", "", "the new session's name")
	return cmd
}

// previewHandoff prints the summary a handoff would start from.
func previewHandoff(cmd *cobra.Command, cfg *agentClientConfig, base string, run map[string]any) error {
	_, b, err := cfg.do(cmd.Context(), "GET", base, nil, http.StatusOK)
	if err != nil {
		return err
	}
	preview := map[string]any{}
	if err := json.Unmarshal(b, &preview); err != nil {
		return err
	}
	return renderOut(cmd, func(w io.Writer) error {
		fmt.Fprintln(w, termSafe(str(preview, "summary")))
		_, err := fmt.Fprintf(w, "\n%v requests, %v bytes of transcript. Start it: olivares session handoff %s --account <account>\n",
			preview["turns"], preview["transcript_bytes"], shellWord(sessionLabel(run)))
		return err
	}, preview)
}

// readSummaryFile reads the reviewed summary from a file or standard input, bounded
// to what the engine accepts so the refusal is the CLI's own sentence.
func readSummaryFile(cmd *cobra.Command, file string) (string, error) {
	var r io.Reader = cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return "", sentence(exitcode.Usage, "Cannot read %s: %v", termSafe(file), err)
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, handoffSummaryFileMax+1))
	if err != nil {
		return "", sentence(exitcode.Usage, "Cannot read the summary: %v", err)
	}
	if len(raw) > handoffSummaryFileMax {
		return "", sentence(exitcode.Usage, "The summary is longer than %d bytes. Shorten it: it is the new session's first message.", handoffSummaryFileMax)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", sentence(exitcode.Usage, "The summary file is empty.")
	}
	return text, nil
}
