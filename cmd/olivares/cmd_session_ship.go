// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const gitpublishTargetsPath = "/v1/m/gitpublish/targets"

// shipReasons are the sentences for the reasons a ship names; any other refusal
// keeps the engine's own words.
var shipReasons = map[string]string{
	"no_branch":               "The session's folder is on no branch (a detached HEAD). Switch it to a branch, then ship again.",
	"no_commit":               "The session's branch has no commit yet. Let the session commit, or change a file, then ship again.",
	"nothing_to_ship":         "The session's commit adds nothing to the branch it would be merged into. There is nothing to ship.",
	"commit_refused":          "The session's folder refused the commit (it is read-only, still working, or your profile has no name and email). Stop the session or fix your profile, then ship again.",
	"binding_not_approved":    "The publication target's credential or repository is no longer approved. Ask an administrator to approve it again in the console under Git publication.",
	"base_not_allowed":        "That base branch is not one of the publication target's merge bases. Pick one with --base.",
	"ref_not_allowed":         "The session's branch cannot be published to that target: it is outside the target's push prefix, or it is a merge base, the default branch or a protected branch.",
	"session_source_refused":  "That session is not in the publication target's workspace.",
	"approval_denied":         "A person did not approve this push, or its approval ended. Ship again to ask for a new review.",
	"approval_unavailable":    "The approval service could not answer. Ship again in a moment.",
	"workflow_change_refused": "The change edits CI configuration, which a push never publishes.",
}

func newSessionShipCmd() *cobra.Command {
	var (
		cfg                                agentClientConfig
		target, message, title, body, base string
		draft                              bool
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "ship <session>",
		Short:       "Commit a session's work, push it and open a pull request",
		Long: "ship does in one action what you would do by hand when a session has finished its work: it\n" +
			"commits every change in the session's folder as you (the folder's ignore rules apply), pushes\n" +
			"that commit to a branch of the session's publication target, and opens a pull request into\n" +
			"the target's merge base. All of it runs on the engine, with the target's own credential;\n" +
			"nothing leaves your machine and no token is needed here.\n\n" +
			"The session's workspace needs one publication target (set up in the console under Git\n" +
			"publication); --target names it when there are several. Shipping again after more work pushes\n" +
			"the new commits to the same branch, and the open pull request follows it.\n\n" +
			"When your organization's approval policy asks for a review of a push, ship stops before the\n" +
			"push, prints the approval and exits with code 11. Another person approves it in the console\n" +
			"under Approvals; run the same command again to push.\n\n" +
			"ship refuses, and says why, when the session's folder is on no branch or has no commit, when\n" +
			"the commit adds nothing, or when the target's credential is not approved. Without --title and\n" +
			"--message, the session's name is both. Cleaning up after the merge is session rm, which\n" +
			"removes the worktree once its branch is merged.",
		Example: "  olivares session ship feature --title \"Add the export button\"\n" +
			"  olivares session ship feature --message \"Fix the flaky test\" --draft\n" +
			"  olivares session ship feature --target 0199b0a1-0000-7000-8000-000000000001",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeFirstSession,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			name := sessionLabel(run)
			if title == "" {
				title = firstLine(message)
			}
			if title == "" {
				title = "Work from " + name
			}
			if message == "" {
				message = title
			}
			targetID, err := shipTarget(cmd, &cfg, run, name, target)
			if err != nil {
				return err
			}
			opID, err := shipOperationID()
			if err != nil {
				return err
			}
			request := map[string]any{"operation_id": opID, "session_run": str(run, "run_ref"), "message": message, "title": title, "body": body, "draft": draft}
			if base != "" {
				request["base"] = base
			}
			_, raw, err := cfg.do(cmd.Context(), "POST", gitpublishTargetsPath+"/"+url.PathEscape(targetID)+"/ships", request, http.StatusOK, http.StatusAccepted)
			if err != nil {
				return shipRefusal(err)
			}
			var result shipResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return fmt.Errorf("the engine's answer to the ship is unreadable: %w", err)
			}
			if err := renderOut(cmd, func(w io.Writer) error { return result.print(w, name) }, json.RawMessage(raw)); err != nil {
				return err
			}
			switch result.State {
			case "shipped":
				return nil
			case "awaiting_approval":
				return exitcode.New(exitcode.ApprovalRequired, errors.New("waiting for another person to approve the push"))
			}
			return exitcode.New(exitcode.Conflict, errors.New("the ship did not finish"))
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "the publication target id (needed when the session's workspace has several)")
	cmd.Flags().StringVar(&message, "message", "", "the commit message (default: the title)")
	cmd.Flags().StringVar(&title, "title", "", "the pull request title (default: the first line of the message, else \"Work from <session>\")")
	cmd.Flags().StringVar(&body, "body", "", "the pull request description")
	cmd.Flags().StringVar(&base, "base", "", "the branch to merge into (default: the target's first merge base)")
	cmd.Flags().BoolVar(&draft, "draft", false, "open the pull request as a draft")
	cfg.addFlags(cmd)
	return cmd
}

// shipResult is the part of the engine's answer the CLI prints.
type shipResult struct {
	State            string `json:"state"`
	Branch           string `json:"branch"`
	Commit           string `json:"commit"`
	Committed        bool   `json:"committed"`
	ApprovalID       string `json:"approval_id"`
	PullRequestURL   string `json:"pull_request_url"`
	PullRequestError string `json:"pull_request_error"`
	Push             *struct {
		State string `json:"state"`
	} `json:"push"`
	PullRequest *struct {
		Observed struct {
			Number int `json:"number"`
		} `json:"observed"`
	} `json:"pull_request"`
}

func (r shipResult) print(w io.Writer, name string) error {
	commit := termSafe(r.Commit)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	branch := termSafe(r.Branch)
	switch r.State {
	case "shipped":
		fmt.Fprintf(w, "Shipped %s: %s at %s is pushed.\n", name, branch, commit)
		switch {
		case r.PullRequestURL != "":
			fmt.Fprintf(w, "Pull request: %s\n", termSafe(r.PullRequestURL))
		case r.PullRequest != nil && r.PullRequest.Observed.Number > 0:
			fmt.Fprintf(w, "Pull request #%d opened.\n", r.PullRequest.Observed.Number)
		}
	case "awaiting_approval":
		fmt.Fprintf(w, "Committed %s at %s. Another person has to approve the push before it goes out (approval %s).\n", branch, commit, termSafe(r.ApprovalID))
		fmt.Fprintf(w, "They decide in the console under Approvals; then run: olivares session ship %s\n", shellWord(name))
	default:
		if r.Push != nil && r.Push.State != "applied" && r.Push.State != "adopted" {
			fmt.Fprintf(w, "The push of %s did not apply (%s). Check the intent in the console under Git publication, then ship again.\n", branch, termSafe(r.Push.State))
			return nil
		}
		fmt.Fprintf(w, "Pushed %s at %s, but the pull request was not opened", branch, commit)
		if r.PullRequestError != "" {
			fmt.Fprintf(w, " (%s)", termSafe(r.PullRequestError))
		}
		fmt.Fprintf(w, ". Ship again to retry it.\n")
	}
	return nil
}

// shipTarget is the publication target the session ships to: the one named, or
// the only one in the session's workspace.
func shipTarget(cmd *cobra.Command, cfg *agentClientConfig, run map[string]any, name, named string) (string, error) {
	if named != "" {
		return named, nil
	}
	_, raw, err := cfg.do(cmd.Context(), "GET", gitpublishTargetsPath, nil)
	if err != nil {
		return "", err
	}
	var list struct {
		Items []struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspace_id"`
			PushPrefix  string `json:"push_prefix"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", fmt.Errorf("the engine's list of publication targets is unreadable: %w", err)
	}
	workspace := str(run, "authz_workspace_id")
	var ids, labels []string
	for _, t := range list.Items {
		if workspace != "" && t.WorkspaceID == workspace {
			ids = append(ids, t.ID)
			labels = append(labels, t.ID+" ("+termSafe(t.PushPrefix)+")")
		}
	}
	switch len(ids) {
	case 0:
		return "", sentence(exitcode.NotFound, "Session %s has no publication target: its workspace has none. Set one up in the console under Git publication, then ship again.", name)
	case 1:
		return ids[0], nil
	}
	return "", sentence(exitcode.Usage, "Session %s can ship to several publication targets: %s. Choose one with --target <id>.", name, strings.Join(labels, ", "))
}

// shipRefusal says why the engine refused a ship, in the CLI's own words when it knows them.
func shipRefusal(err error) error {
	var refusal *apiRefusal
	if errors.As(err, &refusal) {
		if text, ok := shipReasons[refusal.code]; ok {
			return sentence(exitcode.From(err), "%s", text)
		}
	}
	return err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

func shipOperationID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "cli-ship-" + hex.EncodeToString(b[:]), nil
}
