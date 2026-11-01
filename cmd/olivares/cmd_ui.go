// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// cmd_ui.go is the frame of CLI-CONCEPT §5: the same installation state the bare
// `olivares` prints, laid out as a menu for a person at a terminal. It chooses
// nothing itself — the rows name the commands that do the work, and Enter shows
// the command instead of running it.

func newUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "ui",
		Short:       "Open the interactive main menu on a terminal",
		Long: "Open the main menu: this installation's state, the next step, and the\n" +
			"commands to start with. It needs a terminal of at least 80 x 24\n" +
			"and honors NO_COLOR. Every row names the command that does the work.\n" +
			"Under Security you can approve or reject a request, use Stop all (it asks\n" +
			"for a reason and the word stop) and read and verify the audit ledger.\n" +
			"Without a terminal, the frame refuses and names the commands to use.",
		Example: "  olivares ui",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, out, err := uiTerminal(cmd)
			if err != nil {
				return err
			}
			report := rootStatus(cmd.Context())
			frame := mainMenuFrame(report)
			tools := wireToolScreens(&frame, report)
			wireSecurityScreens(&frame, cmd, tools)
			if tools != nil {
				frame.AfterSignal = func() { tools.sayKept(cmd.ErrOrStderr()) }
			}
			code, err := tui.Run(in, out, frame)
			tools.sayKept(cmd.ErrOrStderr())
			return uiExit(code, err)
		},
	}
	cmd.AddCommand(newUISessionCmd())
	return cmd
}

// uiTerminal is the start every `ui` screen shares. It returns the command's
// streams once both are a terminal of at least 80 x 24, or the exit-2 sentence
// that names the commands to use instead.
func uiTerminal(cmd *cobra.Command) (in, out *os.File, err error) {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return nil, nil, exitcode.New(exitcode.Usage, tui.ErrNotTerminal)
	}
	out, ok = cmd.OutOrStdout().(*os.File)
	if !ok {
		return nil, nil, exitcode.New(exitcode.Usage, tui.ErrNotTerminal)
	}
	// Refuse before probing the engine: a pipe or a script gets the
	// sentence, not a two-second status read it cannot see.
	if err := tui.CheckTerminal(in, out); err != nil {
		return nil, nil, exitcode.New(exitcode.Usage, err)
	}
	// Refuse a small terminal before the probes as well: the sentence
	// is the whole answer, and it should not wait on the engine.
	if err := tui.CheckSize(in, out); err != nil {
		if errors.Is(err, tui.ErrTooSmall) {
			return nil, nil, exitcode.New(exitcode.Usage, err)
		}
		return nil, nil, err
	}
	return in, out, nil
}

// runOlivares runs `olivares <argv>` in this process and returns what it printed.
// It is a fresh root command, so the flags, the hooks and the exit codes are the
// ones a person gets from the command line, and every screen that calls it makes
// the same API request as the command, which the audit ledger records alike.
func runOlivares(ctx context.Context, argv []string) (stdout, stderr string, err error) {
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(argv)
	err = root.ExecuteContext(ctx)
	return strings.TrimSpace(out.String()), strings.TrimSpace(errOut.String()), err
}

// uiExit turns what a screen returned into the command's result.
func uiExit(code int, err error) error {
	if err != nil {
		if errors.Is(err, tui.ErrNotTerminal) || errors.Is(err, tui.ErrTooSmall) {
			return exitcode.New(exitcode.Usage, err)
		}
		return err
	}
	if code != exitcode.OK {
		// q and Ctrl-D exit 0; Ctrl-C exits 130, as it prints no text of its own.
		return exitcode.New(code, nil)
	}
	return nil
}

// mainMenuFrame is the main menu of CLI-CONCEPT §5.1: where this installation
// stands and the six ways into it. The labels are the contract's words; the
// commands are the real verbs, so a renamed command shows up in the pty test.
func mainMenuFrame(report rootStatusReport) tui.Frame {
	// The context name, engine URL and next step come from configuration and
	// the engine, so oneRow keeps them from driving the terminal or breaking
	// the single row they are shown on.
	heading := "Olivares AI"
	if report.Context != "" {
		heading += " · " + oneRow(report.Context)
	}
	footer, role := "No engine found on this machine", termrender.RoleWarn
	switch {
	case report.Engine != "" && report.Reachable:
		footer, role = oneRow(report.Engine)+" (running)", termrender.RoleOK
	case report.Engine != "":
		footer, role = oneRow(report.Engine)+" (not answering)", termrender.RoleWarn
	}
	next := oneRow(report.Next)
	items := []tui.MenuItem{
		{Label: "Get started", Detail: "Next: " + next, Command: next},
		{Label: "AI tools", Detail: "install, sign in, API keys", Command: "olivares tool ls"},
		{Label: "Sessions", Detail: "start, attach, stop", Command: "olivares session ls"},
		{Label: "Security", Detail: "approvals and policies", Command: "olivares governance approvals ls"},
		{Label: "Settings", Detail: "this installation", Command: "olivares config"},
	}
	// The edition's own screens come before the full list, which stays last.
	items = append(items, thisEdition.uiScreens.get()...)
	items = append(items, tui.MenuItem{Label: "All commands", Detail: "the full reference", Command: "olivares --help"})
	return tui.Frame{
		Heading:    heading,
		Right:      editionLabel(thisEdition.name),
		Items:      items,
		Footer:     footer + " · version " + version,
		FooterRole: role,
	}
}

// wireToolScreens opens the Get started and AI tools rows on their screens. They
// need an engine that answers and a saved sign-in; without either the rows keep
// naming the command (olivares login, olivares doctor) that gets there. It returns
// the screens' state, which says what they kept once the terminal is restored, or
// nil when nothing was wired.
func wireToolScreens(frame *tui.Frame, report rootStatusReport) *uiTools {
	if !report.Reachable || report.SignedIn == "" {
		return nil
	}
	tools := &uiTools{cfg: agentClientConfig{timeout: uiRequestTimeout}}
	if err := tools.cfg.resolve(); err != nil {
		return nil
	}
	for i := range frame.Items {
		switch frame.Items[i].Label {
		case "Get started":
			frame.Items[i].Open = tools.open("Get started", true)
		case "AI tools":
			frame.Items[i].Open = tools.open("AI tools", false)
		}
	}
	return tools
}

// uiRequestTimeout is the time one engine request of a screen may take, as
// --timeout's default does for a command.
const uiRequestTimeout = 30 * time.Second

// oneRow is termSafe for a cell that is one row of the frame: the newline and
// tab termSafe keeps for session output become spaces, and what a person cannot
// see goes: format characters (zero-width, right-to-left overrides, isolates,
// marks), line and paragraph separators, and bytes that are not UTF-8. The two
// joiners stay, because some scripts spell names with them.
func oneRow(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) && r != '\u200c' && r != '\u200d':
			return -1
		}
		return r
	}, termSafe(strings.ToValidUTF8(s, "")))
}

// plainName is what a driver id or a provider reference looks like. Anything else
// is the engine's text, not a name to put into a command a person may paste.
var plainName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// editionLabel is how the frame names the build it is running.
func editionLabel(name string) string {
	switch name {
	case "community":
		return "Community"
	case "enterprise":
		return "Business"
	default:
		return name
	}
}
