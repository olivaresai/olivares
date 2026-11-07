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
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_ask.go is `olivares ask`: Ask Olivares from a terminal (docs/design/ask-olivares-agent-v1.md).
// The engine owns the turn, the closed tool table and every tool call (modules/models/ask_turn.go);
// this is a thin client of it. Reads run in the engine under a principal narrower than this person.
// A write is only a proposal: an approval request bound to the exact action. A person decides it;
// at a terminal this person is asked whether to approve it now, which the engine accepts only when
// they may decide it. Once approved it runs once, here or later with the printed command. The model
// never holds a credential and is never asked to approve anything.

const (
	askPath          = "/v1/m/models/ask"
	askTurnsPath     = askPath + "/turns"
	askApprovalsPath = "/v1/m/governance/approvals"
)

// askView is GET /v1/m/models/ask: whether Ask is on, its models and its tools.
type askView struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model"`
	Tools   []struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Permission string `json:"permission"`
		Allowed    bool   `json:"allowed"`
	} `json:"tools"`
}

// askProposal is a write waiting for a person, bound to its exact action.
type askProposal struct {
	ApprovalID string            `json:"approval_id"`
	Status     string            `json:"status"`
	Tool       string            `json:"tool"`
	Permission string            `json:"permission"`
	Args       map[string]string `json:"args"`
	Command    string            `json:"command"`
}

type askReply struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Steps  []struct {
		Command string `json:"command"`
	} `json:"steps"`
	Proposal *askProposal `json:"proposal,omitempty"`
}

// askResult is what one question came to. Steps are the equivalent commands of every action, in
// order; RunCommand runs a proposal once a person approved it.
type askResult struct {
	Kind       string       `json:"kind"`
	Text       string       `json:"text,omitempty"`
	Reason     string       `json:"reason,omitempty"`
	Tool       string       `json:"tool,omitempty"`
	Steps      []string     `json:"steps"`
	Proposal   *askProposal `json:"proposal,omitempty"`
	RunCommand string       `json:"run_command,omitempty"`
	Ran        bool         `json:"ran"`
	Output     string       `json:"output,omitempty"`
}

// askRunner asks one question. Both `olivares ask` and the Ask row of `olivares ui` use it.
type askRunner struct {
	cfg   *agentClientConfig
	model string
	allow []string
	// confirm asks the person whether to approve and run a proposal now; nil never does.
	confirm func(askProposal) (bool, error)
	// note is told the equivalent command of each action as it happens.
	note func(command string)
}

// askView reads whether Ask is on and the line of tools shown before the first turn.
func (c *agentClientConfig) askView(ctx context.Context, allow []string) (string, error) {
	_, b, err := c.do(ctx, http.MethodGet, askPath, nil, http.StatusOK)
	if err != nil {
		return "", err
	}
	var v askView
	if err := json.Unmarshal(b, &v); err != nil {
		return "", exitcode.New(exitcode.Server, fmt.Errorf("the engine's Ask settings could not be read: %w", err))
	}
	if !v.Enabled {
		return "", exitcode.New(exitcode.Usage, errors.New("Ask Olivares is off: an administrator chooses its model with `olivares models ask bind <routing policy id>`"))
	}
	var parts []string
	for _, t := range v.Tools {
		if !t.Allowed || (len(allow) > 0 && !containsString(allow, t.Permission)) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", t.Name, t.Kind, t.Permission))
	}
	if len(parts) == 0 {
		return "no tools: it answers from the conversation only", nil
	}
	return strings.Join(parts, "; "), nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// run asks one question. The engine runs the reads; a proposal is approved and run only when the
// person says so at a terminal and may decide it.
func (r *askRunner) run(ctx context.Context, prompt string) (askResult, error) {
	res := askResult{Steps: []string{}}
	body := map[string]any{"messages": []map[string]string{{"role": "user", "content": prompt}}}
	if r.model != "" {
		body["model"] = r.model
	}
	if len(r.allow) > 0 {
		body["allow"] = r.allow
	}
	_, b, err := r.cfg.do(ctx, http.MethodPost, askTurnsPath, body, http.StatusOK)
	if err != nil {
		return res, err
	}
	var reply askReply
	if err := json.Unmarshal(b, &reply); err != nil {
		return res, exitcode.New(exitcode.Server, fmt.Errorf("the engine's answer could not be read: %w", err))
	}
	for _, s := range reply.Steps {
		r.step(&res, s.Command)
	}
	res.Kind, res.Text, res.Reason, res.Tool = reply.Kind, reply.Text, reply.Reason, reply.Tool
	switch reply.Kind {
	case "answer", "refused", "limit":
		return res, nil
	case "proposal":
		if reply.Proposal == nil {
			break
		}
		p := *reply.Proposal
		res.Proposal, res.RunCommand = &p, askRunCommand(p)
		r.step(&res, p.Command)
		if r.confirm == nil {
			return res, nil
		}
		ok, err := r.confirm(p)
		if err != nil || !ok {
			return res, err
		}
		if err := r.cfg.askApprove(ctx, p.ApprovalID); err != nil {
			return res, err
		}
		res.Output, err = r.cfg.askRunProposal(ctx, p.ApprovalID, p.Tool, p.Args)
		res.Ran = err == nil
		return res, err
	}
	return res, exitcode.New(exitcode.Server, fmt.Errorf("the engine answered with an unknown turn %q", termSafe(reply.Kind)))
}

func (r *askRunner) step(res *askResult, command string) {
	res.Steps = append(res.Steps, command)
	if r.note != nil {
		r.note(command)
	}
}

// askApprove records this person's own approval, with their own credential, never Ask's. The
// engine refuses it unless they may decide the request.
func (c *agentClientConfig) askApprove(ctx context.Context, approvalID string) error {
	_, _, err := c.do(ctx, http.MethodPost, askApprovalsPath+"/"+url.PathEscape(approvalID)+"/decisions",
		map[string]string{"decision": "approve"}, http.StatusOK)
	return err
}

// askRunProposal runs an approved proposal once. The tool and arguments must be the ones the
// approval is bound to.
func (c *agentClientConfig) askRunProposal(ctx context.Context, approvalID, tool string, args map[string]string) (string, error) {
	if args == nil {
		args = map[string]string{}
	}
	_, b, err := c.do(ctx, http.MethodPost, askPath+"/proposals/"+url.PathEscape(approvalID)+"/run",
		map[string]any{"tool": tool, "args": args}, http.StatusOK)
	if err != nil {
		return "", err
	}
	var out struct {
		Status int    `json:"status"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", exitcode.New(exitcode.Server, fmt.Errorf("the engine's answer could not be read: %w", err))
	}
	if out.Status >= http.StatusBadRequest {
		return out.Output, exitcode.New(exitcode.Err, fmt.Errorf("the approved action was refused (HTTP %d): %s", out.Status, termSafe(out.Output)))
	}
	return out.Output, nil
}

// askRunCommand is the command that runs a proposal once a person approved it.
func askRunCommand(p askProposal) string {
	args := []byte("{}")
	if len(p.Args) > 0 {
		args, _ = json.Marshal(p.Args)
	}
	return "olivares ask --run " + shellWord(p.ApprovalID) + " --tool " + shellWord(p.Tool) + " --args " + shellWord(string(args))
}

func newAskCmd() *cobra.Command {
	var cfg agentClientConfig
	var model, run, tool, args string
	var allow []string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "ask <question>",
		Short:       "Ask Olivares: an assistant that acts for you on the model your administrator chose",
		Long: "ask sends your question to the model an administrator chose for Ask and answers in plain\n" +
			"text. The assistant can use a short, closed list of tools, each an Olivares command you\n" +
			"could run yourself, under your own permissions and no wider. A read runs at once. A write\n" +
			"is only a proposal: an approval request bound to the exact action. At a terminal you are\n" +
			"asked whether to approve and run it now, which works only when you may decide it; otherwise\n" +
			"a person who may decide approves it, and you run it once with the printed command (exit\n" +
			"code 11 until then).\n\n" +
			"Every action prints its equivalent command on stderr, after \"=\". --model picks another\n" +
			"model your administrator allowed. --allow narrows the tools to the ones that use the listed\n" +
			"permissions; it never widens what you may do.",
		Example: "  olivares ask \"which sessions are running?\"\n" +
			"  olivares ask \"start a session called fix-tests\"\n" +
			"  olivares ask \"what is waiting for approval?\" --allow governance:approval:read -o json\n" +
			"  olivares ask --run 018f2a10-0000-7000-8000-000000000003 --tool run.start --args '{\"name\":\"fix-tests\"}'",
		RunE: func(cmd *cobra.Command, posArgs []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx := cmd.Context()
			if run != "" {
				return askRunApproved(cmd, &cfg, run, tool, args, posArgs)
			}
			if len(posArgs) == 0 {
				return exitcode.New(exitcode.Usage, errors.New("ask needs a question"))
			}
			errw := cmd.ErrOrStderr()
			tools, err := cfg.askView(ctx, allow)
			if err != nil {
				return err
			}
			fmt.Fprintf(errw, "Ask can use: %s\n", termSafe(tools))
			in := bufio.NewReader(cmd.InOrStdin())
			terminal := interactiveStdin(cmd.InOrStdin())
			runner := &askRunner{
				cfg: &cfg, model: model, allow: allow,
				note:    func(command string) { fmt.Fprintf(errw, "= %s\n", termSafe(command)) },
				confirm: func(p askProposal) (bool, error) { return askConfirm(in, terminal, errw, p) },
			}
			res, err := runner.run(ctx, strings.Join(posArgs, " "))
			if err != nil {
				return err
			}
			if err := renderOut(cmd, func(w io.Writer) error { return askText(w, res) }, res); err != nil {
				return err
			}
			switch {
			case res.Kind == "refused":
				return exitcode.New(exitcode.Err, errors.New("Ask tried a tool it may not use and was stopped: "+termSafe(res.Reason)))
			case res.Kind == "proposal" && !res.Ran:
				return exitcode.New(exitcode.ApprovalRequired, errors.New("the proposed action waits for a person to approve it"))
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&model, "model", "", "another routing policy id your administrator allowed for Ask (default: the one they chose)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "limit the tools to those that use these permissions, comma separated")
	cmd.Flags().StringVar(&run, "run", "", "run the approved proposal with this approval id, once")
	cmd.Flags().StringVar(&tool, "tool", "", "with --run: the proposal's tool")
	cmd.Flags().StringVar(&args, "args", "", "with --run: the proposal's arguments, as JSON")
	return cmd
}

// askRunApproved is `olivares ask --run`: it runs an approved proposal once.
func askRunApproved(cmd *cobra.Command, cfg *agentClientConfig, approvalID, tool, rawArgs string, posArgs []string) error {
	if len(posArgs) > 0 || tool == "" {
		return exitcode.New(exitcode.Usage, errors.New("--run takes the approval id with --tool and --args, and no question"))
	}
	var args map[string]string
	if rawArgs != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return exitcode.New(exitcode.Usage, fmt.Errorf("--args must be a JSON object of strings: %w", err))
		}
	}
	out, err := cfg.askRunProposal(cmd.Context(), approvalID, tool, args)
	if err != nil {
		return err
	}
	return renderOut(cmd, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, "Done. "+termSafe(out))
		return err
	}, map[string]any{"ran": true, "output": out})
}

// askConfirm shows the exact proposed action and asks at a terminal whether to approve and run it.
// Anywhere else the answer is no: a pipe's end of input is the absence of a person, not consent.
func askConfirm(in *bufio.Reader, terminal bool, errw io.Writer, p askProposal) (bool, error) {
	fmt.Fprintf(errw, "\nProposed action (needs %s), approval %s:\n  %s\n", termSafe(p.Permission), termSafe(p.ApprovalID), termSafe(p.Command))
	if !terminal {
		return false, nil
	}
	fmt.Fprint(errw, "Approve it as yourself and run it now? [y/N]: ")
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(errw)
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// askText prints the human form of a result. The answer is plain text, never interpreted.
func askText(w io.Writer, res askResult) error {
	var line string
	switch res.Kind {
	case "answer":
		line = termSafe(res.Text)
	case "refused":
		line = "Ask tried a tool it may not use: " + termSafe(res.Tool) + " (" + termSafe(res.Reason) + ")"
	case "limit":
		line = termSafe(res.Reason)
	case "proposal":
		if res.Ran {
			line = "Done. " + termSafe(res.Output)
		} else {
			line = "Waiting for approval " + termSafe(res.Proposal.ApprovalID) + ". Once a person approved it, run it with:\n  " + termSafe(res.RunCommand)
		}
	}
	_, err := fmt.Fprintln(w, line)
	return err
}
