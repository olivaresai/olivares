// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// waitPipeline uses the same attach stream, turn codec and runtime controls as
// interactive sessions. A completed turn ends this pipeline's owned process.
func (c *agentClientConfig) waitPipeline(cmd *cobra.Command, run map[string]any, prompt string, cap int64) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	ref := str(run, "run_ref")
	if ref == "" {
		return sentence(exitcode.Server, "The engine returned no session reference.")
	}
	path := sessionRunsPath + "/" + url.PathEscape(ref)
	finish := func(state string, code int, last string, now map[string]any) error {
		if state != "waiting_approval" {
			b, err := c.stopPipelineProcess(ctx, path)
			if err != nil {
				return err
			}
			var stopped map[string]any
			if err = decodeWorkJSON(b, &stopped); err != nil {
				return err
			}
			switch str(stopped, "state") {
			case "stopped", "failed", "cleaned", "declined", "expired":
			default:
				return sentence(exitcode.Indeterminate, "The engine did not confirm that the session stopped.")
			}
			// Retain the last measured usage if a control reply omits it.
			for k, v := range stopped {
				now[k] = v
			}
			if limited, limitedCode := pipelineRunOutcome(now); limited == "cost_cap" || limited == "timed_out" || limited == "cost_unavailable" || limited == "failed" {
				state, code = limited, limitedCode
			}
			if cap > 0 && state != "timed_out" && state != "failed" {
				if cost, ok := pipelineCost(now); ok && cost >= cap {
					state, code = "cost_cap", exitcode.CostCap
				} else if !ok && state == "finished" {
					state, code = "cost_unavailable", exitcode.Indeterminate
				}
			}
		}
		doc := map[string]any{"run_ref": ref, "state": state, "last_message": last,
			"input_tokens": now["input_tokens"], "output_tokens": now["output_tokens"], "cost_micro_usd": now["cost_micro_usd"]}
		for _, key := range []string{"reason", "limit"} {
			if value, ok := now[key]; ok {
				doc[key] = value
			}
		}
		if state == "waiting_approval" {
			approval := str(now, "pending_approval_ref")
			if approval == "" {
				approval = str(now, "approval_ref")
			}
			if approval != "" {
				doc["approval_ref"] = approval
			}
		}
		if err := renderOut(cmd, func(w io.Writer) error { _, err := fmt.Fprintf(w, "%s: %s\n", ref, state); return err }, doc); err != nil {
			return err
		}
		if code != 0 {
			return exitcode.New(code, nil)
		}
		return nil
	}
	read := func() (map[string]any, error) {
		_, b, err := c.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		var now map[string]any
		err = decodeWorkJSON(b, &now)
		if err == nil && now == nil {
			err = sentence(exitcode.Server, "The engine returned no session state.")
		}
		return now, err
	}
	if str(run, "state") == sessionWaitingApproval {
		return finish("waiting_approval", exitcode.ApprovalRequired, "", run)
	}
	if !sessionShowsTurns(run) {
		return sentence(exitcode.Usage, "--wait needs a session tool with turn results.")
	}
	frames, errs := c.openFrames(ctx, ref, 0)
	var lastSeq int64
	if strings.TrimSpace(prompt) != "" {
		var err error
		lastSeq, err = settleReplay(frames, errs)
		if err != nil {
			now, readErr := read()
			if readErr != nil {
				return readErr
			}
			if state, code := pipelineRunOutcome(now); state != "" {
				return finish(state, code, "", now)
			}
			return err
		}
		if err = c.postTurn(ctx, run, prompt, nil); err != nil {
			return err
		}
	}
	view := newSessionView(io.Discard)
	view.driver, view.engineSupplied, view.noOneAsked = sessionDriver(run), engineSupplied(run), noOneAsked(run)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case f, ok := <-frames:
			if !ok {
				now, err := read()
				if err != nil {
					return err
				}
				state, code := pipelineRunOutcome(now)
				if state == "" {
					state, code = "failed", exitcode.SessionFailed
				}
				return finish(state, code, view.lastReply, now)
			}
			if f.Seq <= lastSeq {
				continue
			}
			ended := view.render(f.Line)
			if !ended {
				continue
			}
			now, err := read()
			if err != nil {
				return err
			}
			state, code := pipelineRunOutcome(now)
			if state == "" {
				state = "finished"
				if view.failed {
					state, code = "failed", exitcode.SessionFailed
				}
				if view.limited {
					state, code = "limited", exitcode.SessionLimit
				}
			}

			return finish(state, code, view.lastReply, now)
		case err := <-errs:
			if err != nil && !errors.Is(err, context.Canceled) {
				now, readErr := read()
				if readErr == nil {
					if state, code := pipelineRunOutcome(now); state != "" {
						return finish(state, code, view.lastReply, now)
					}
				}
				return err
			}
		case <-ticker.C:
			now, err := read()
			if err != nil {
				return err
			}
			if state, code := pipelineRunOutcome(now); state != "" {
				return finish(state, code, view.lastReply, now)
			}
		case <-ctx.Done():
			// Use an independent bounded request to end the owned process after cancellation.
			stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, _, err := c.do(stopCtx, "POST", path+"/stop", nil)
			stop()
			if err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}

func pipelineRunOutcome(now map[string]any) (string, int) {
	if str(now, "state") == sessionWaitingApproval || str(now, "pending_approval_ref") != "" {
		return "waiting_approval", exitcode.ApprovalRequired
	}
	reason := str(now, "reason")
	switch {
	case strings.HasPrefix(reason, "run cost unavailable"):
		return "cost_unavailable", exitcode.Indeterminate
	case strings.HasPrefix(reason, "run cost cap reached"):
		return "cost_cap", exitcode.CostCap
	case strings.HasPrefix(reason, "run timeout reached"):
		return "timed_out", exitcode.RunTimeout
	case now["limit"] != nil || strings.Contains(reason, "max session duration reached"):
		return "limited", exitcode.SessionLimit
	}
	switch str(now, "state") {
	case "failed", "declined", "expired":
		return "failed", exitcode.SessionFailed
	case "stopped", "cleaned":
		return "stopped", exitcode.SessionLimit
	}
	return "", 0
}

func pipelineCost(now map[string]any) (int64, bool) {
	n, ok := now["cost_micro_usd"].(json.Number)
	if !ok {
		return 0, false
	}
	value, err := n.Int64()
	return value, err == nil && value >= 0
}

// An engine-owned stop can retire its Claim before publishing the final
// row. Reconcile that bounded transition, then ask the stop endpoint once more:
// GET alone cannot prove that a terminal row's child has been collected.
func (c *agentClientConfig) stopPipelineProcess(ctx context.Context, path string) ([]byte, error) {
	_, body, err := c.do(ctx, "POST", path+"/stop", nil)
	if err == nil || (exitcode.From(err) != exitcode.Auth && exitcode.From(err) != exitcode.Conflict) {
		return body, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, latest, readErr := c.do(waitCtx, "GET", path, nil)
		if readErr != nil {
			return nil, err
		}
		var now map[string]any
		if decodeWorkJSON(latest, &now) != nil {
			return nil, err
		}
		switch str(now, "state") {
		case "stopped", "failed", "cleaned", "declined", "expired":
			_, body, retryErr := c.do(waitCtx, "POST", path+"/stop", nil)
			return body, retryErr
		}
		select {
		case <-waitCtx.Done():
			return nil, err
		case <-ticker.C:
		}
	}
}
