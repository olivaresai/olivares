// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// A supervised session parks each tool call on a person's approval (a governance
// approval, the one the console's approvals page answers) and the engine denies the call
// when nobody answers in time. A turn the CLI shows asks the engine whether such a request
// is waiting and puts it in front of the person: a question at a terminal, the verbs that
// answer anywhere else. Every answer names the request it answers, so a vote never lands
// on a request the person did not read.

const sessionApprovalsPath = "/v1/m/governance/approvals"

// toolApprovalPoll is how often a shown turn asks the engine whether the session waits on a tool approval.
var toolApprovalPoll = time.Second

var (
	// errRequestGone closes a question the engine no longer waits on.
	errRequestGone = errors.New("the request is no longer waiting for an answer: it was answered elsewhere or its deadline passed")
	// errAnswerUnread marks a vote the engine took whose reply could not be read.
	errAnswerUnread = errors.New("the engine took your answer but its reply could not be read")
)

// approvalRequestText is what the person decides on: the engine's reason and the reviewed
// tool and text. A call with a description has the agent's own words as its reason and the
// command only in the review, so the review is always shown, even where it repeats the reason:
// a test for "the reason already carries it" would let a short command hide inside a sentence.
func approvalRequestText(a cliApproval) string {
	text := strings.TrimRight(approvalField(a.Reason), "\n")
	if a.Review == nil || strings.TrimSpace(a.Review.Text) == "" {
		return text
	}
	return text + "\n" + approvalField(a.Review.Tool) + ": " + approvalField(a.Review.Text)
}

// approvalField shows s as it is, or quoted when it holds a rune that is not printable
// (newline and tab apart). termSafe deletes an escape sequence without a trace, an
// unterminated one takes the rest of the line with it, and bash still runs what follows an
// ESC byte: the person would approve a command shorter than the one that runs. A quoted
// field shows every byte, as \x1b, and also the invisible runes such as U+202E.
func approvalField(s string) string {
	if strings.ContainsFunc(s, func(r rune) bool { return r != '\n' && r != '\t' && !unicode.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}

// answerVerbs names the two commands that answer one request of one session.
func answerVerbs(run map[string]any, id string) string {
	name, ref := shellWord(sessionLabel(run)), shellWord(id)
	return fmt.Sprintf("olivares session approve %s %s  or  olivares session deny %s %s", name, ref, name, ref)
}

// pendingToolApproval reads one approval and refuses one nobody can decide any more.
func (c *agentClientConfig) pendingToolApproval(ctx context.Context, id string) (cliApproval, error) {
	var a cliApproval
	_, b, err := c.do(ctx, "GET", sessionApprovalsPath+"/"+url.PathEscape(id), nil)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	if a.Status != "" && a.Status != "pending" {
		return a, sentence(exitcode.Conflict, "The request %s is already %s; there is nothing to decide.", termSafe(id), termSafe(a.Status))
	}
	return a, nil
}

// decideToolApproval records one vote ("approve" or "reject") on a tool approval and
// returns where the request stands afterwards.
func (c *agentClientConfig) decideToolApproval(ctx context.Context, id, decision, note string) ([]byte, cliApproval, error) {
	var after cliApproval
	_, b, err := c.do(ctx, "POST", sessionApprovalsPath+"/"+url.PathEscape(id)+"/decisions",
		map[string]any{"decision": decision, "note": note})
	if err != nil {
		return nil, after, err
	}
	if err := json.Unmarshal(b, &after); err != nil {
		return b, after, fmt.Errorf("%w: %v", errAnswerUnread, err)
	}
	return b, after, nil
}

// watchToolApproval runs beside a shown turn. The returned stop ends it and waits for it,
// so nothing is written after the command returns.
func (c *agentClientConfig) watchToolApproval(ctx context.Context, cmd *cobra.Command, run map[string]any) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.pollToolApprovals(ctx, cmd, run)
	}()
	return func() {
		cancel()
		<-done
	}
}

// pollToolApprovals offers each request the session raises once it has been shown. A failed
// check of the session is reported once and polling goes on, because the turn's own stream
// is what ends the command when the engine is gone; a request that could not be read is
// reported once with its verbs and asked again.
func (c *agentClientConfig) pollToolApprovals(ctx context.Context, cmd *cobra.Command, run map[string]any) {
	ref, errw := str(run, "run_ref"), cmd.ErrOrStderr()
	var offered, unread string
	warned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(toolApprovalPoll):
		}
		now, err := c.findSession(ctx, ref)
		if err != nil {
			if ctx.Err() == nil && !warned {
				warned = true
				fmt.Fprintf(errw, "Could not check %s for approval requests: %s\n", sessionLabel(run), termSafe(err.Error()))
			}
			continue
		}
		id := str(now, "pending_approval_ref")
		if id == "" || id == offered {
			continue
		}
		shown, err := c.askToolApproval(ctx, cmd, now, id)
		if shown {
			offered = id
		}
		if err == nil || ctx.Err() != nil || (!shown && unread == id) {
			continue
		}
		if !shown {
			unread = id
		}
		fmt.Fprintf(errw, "%s\n", termSafe(err.Error()))
	}
}

// askToolApproval shows one request and asks at a terminal; without one it names the verbs.
// shown is false only when the request could not be read, so it is offered again.
func (c *agentClientConfig) askToolApproval(ctx context.Context, cmd *cobra.Command, run map[string]any, id string) (shown bool, err error) {
	verbs := answerVerbs(run, id)
	a, err := c.pendingToolApproval(ctx, id)
	if err != nil {
		return false, fmt.Errorf("could not read request %s (%w); it is asked again. To answer it now: %s", termSafe(id), err, verbs)
	}
	errw := cmd.ErrOrStderr()
	if _, err := fmt.Fprintf(errw, "\n%s\n", approvalRequestText(a)); err != nil {
		return true, fmt.Errorf("could not show the request: %w", err)
	}
	in := cmd.InOrStdin()
	if !interactiveStdin(in) {
		fmt.Fprintf(errw, "%s waits for your answer: %s\n", sessionLabel(run), verbs)
		return true, nil
	}
	if f, ok := in.(*os.File); ok {
		if err := discardTypedAhead(f); err != nil {
			fmt.Fprintf(errw, "%s waits for your answer, not asked here because earlier keys could not be cleared (%s): %s\n",
				sessionLabel(run), termSafe(err.Error()), verbs)
			return true, nil
		}
	}
	if _, err := fmt.Fprint(errw, "Allow? [y/N]: "); err != nil {
		return true, fmt.Errorf("could not ask: %w", err)
	}
	line, err := c.readAnswer(ctx, in, str(run, "run_ref"), id)
	if err != nil {
		fmt.Fprintln(errw)
		if errors.Is(err, errRequestGone) || ctx.Err() != nil {
			return true, err
		}
		return true, fmt.Errorf("no answer was read (%w); nothing was recorded. Answer it with: %s", err, verbs)
	}
	decision, done := "reject", "Denied"
	if answer := strings.ToLower(strings.TrimSpace(line)); answer == "y" || answer == "yes" {
		decision, done = "approve", "Allowed"
	}
	_, after, err := c.decideToolApproval(ctx, id, decision, "")
	if errors.Is(err, errAnswerUnread) {
		return true, err
	}
	if err != nil {
		return true, fmt.Errorf("your answer was not recorded (%w); answer it with: %s", err, verbs)
	}
	fmt.Fprintln(errw, approvalOutcome(done, after))
	return true, nil
}

// readAnswer reads the person's line. On a terminal it waits in steps of toolApprovalPoll and
// gives the question up when ctx ends or the session no longer waits on id, so a request
// answered in the console does not leave a question open. A read that fails with nothing
// typed is an error, never a refusal.
func (c *agentClientConfig) readAnswer(ctx context.Context, in io.Reader, runRef, id string) (string, error) {
	if f, ok := in.(*os.File); ok {
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			ready, err := waitReadable(f, toolApprovalPoll)
			if err != nil {
				return "", err
			}
			if ready {
				break
			}
			if now, err := c.findSession(ctx, runRef); err == nil && str(now, "pending_approval_ref") != id {
				return "", errRequestGone
			}
		}
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", err
	}
	return line, nil
}

// newSessionDecisionCmd is `session approve` and `session deny`: they answer one named
// request of the session, the one the person read, and refuse any other.
func newSessionDecisionCmd(use, short, decision, done string) *cobra.Command {
	var (
		cfg  agentClientConfig
		note string
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         use + " <session> <request>",
		Short:       short,
		Long: use + " answers the tool request a session is waiting on. A supervised session asks\n" +
			"before each action and the engine denies the call if nobody answers in time. A session\n" +
			"shown by start, send or follow asks at a terminal, and elsewhere prints this command with\n" +
			"the request's id filled in. The command answers only that request: it refuses one the\n" +
			"session no longer waits on.",
		Example:           "  olivares session " + use + " my-app 01890000-0000-7000-8000-000000000001 --note \"checked the path\"",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeSessionThenNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			id := args[1]
			switch ref := str(run, "pending_approval_ref"); {
			case ref == "":
				return sentence(exitcode.Conflict, "%s has no tool request waiting for an answer.", sessionLabel(run))
			case ref != id:
				return sentence(exitcode.Conflict, "%s is not waiting on request %s now. See what it waits on: olivares session follow %s",
					sessionLabel(run), termSafe(id), shellWord(sessionLabel(run)))
			}
			a, err := cfg.pendingToolApproval(cmd.Context(), id)
			if err != nil {
				return err
			}
			b, after, err := cfg.decideToolApproval(cmd.Context(), id, decision, note)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "%s\n%s: %s\n", approvalOutcome(done, after), sessionLabel(run),
					strings.ReplaceAll(approvalRequestText(a), "\n", " · "))
				return err
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&note, "note", "", "why you decided (kept with your answer)")
	return cmd
}

// completeSessionThenNothing completes the session name; the request id is not guessable.
func completeSessionThenNothing(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeSessions(cmd, args, toComplete)
}
