// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// cmd_ui_ask.go is the Ask Olivares row of `olivares ui`: the menu ends and the terminal
// asks questions one per line, through the same runner as `olivares ask`, until an empty
// line or the end of input. A proposed write is shown with its approval and, when the
// person says yes, approved as them and run once.

// askTurnTimeout bounds one question: a local model may take a while to answer.
const askTurnTimeout = 5 * time.Minute

// wireAskScreen opens the Ask Olivares row. It needs what the tools screens need.
func wireAskScreen(frame *tui.Frame, tools *uiTools) {
	if tools == nil {
		return
	}
	for i := range frame.Items {
		if frame.Items[i].Label == "Ask Olivares" {
			frame.Items[i].Open = func(h *tui.Host) string {
				tools.launch = func(in, out *os.File) error {
					cfg := tools.cfg
					cfg.timeout = askTurnTimeout
					return runAskShell(context.Background(), in, out, &cfg, interactiveStdin(in))
				}
				h.Exit()
				return ""
			}
		}
	}
}

// runAskShell asks the questions read from in and prints each answer on out.
func runAskShell(ctx context.Context, in io.Reader, out io.Writer, cfg *agentClientConfig, terminal bool) error {
	tools, err := cfg.askView(ctx, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Ask Olivares. It can use: %s\nAn empty line ends.\n", termSafe(tools))
	lines := bufio.NewReader(in)
	runner := &askRunner{
		cfg:     cfg,
		note:    func(command string) { fmt.Fprintf(out, "= %s\n", termSafe(command)) },
		confirm: func(p askProposal) (bool, error) { return askConfirm(lines, terminal, out, p) },
	}
	for {
		fmt.Fprint(out, "\nask> ")
		line, err := lines.ReadString('\n')
		question := strings.TrimSpace(line)
		if question == "" {
			fmt.Fprintln(out)
			return nil
		}
		res, rerr := runner.run(ctx, question)
		if rerr != nil {
			fmt.Fprintln(out, "error: "+termSafe(rerr.Error()))
		} else if perr := askText(out, res); perr != nil {
			return perr
		}
		if err != nil {
			return nil // the end of input after a last question
		}
	}
}
