// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

func TestUIWithoutATerminalNamesTheCommands(t *testing.T) {
	cmd := newUICmd()
	cmd.SetArgs([]string{}) // empty, not nil: nil makes cobra read os.Args
	cmd.SetIn(bytes.NewReader(nil))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("olivares ui accepted a non-terminal")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("olivares ui exit code = %d, want %d", got, exitcode.Usage)
	}
	if !errors.Is(err, tui.ErrNotTerminal) {
		t.Fatalf("olivares ui error = %v, want the not-a-terminal sentence", err)
	}
	for _, want := range []string{"olivares status", "olivares tool ls", "olivares session start <folder>"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
}

// commandResolves reports whether printed names a command of root. Find hands
// back the nearest parent plus the words it could not match, with no error, so
// a misspelled leaf ("session lss") must also fail on a leftover word that is
// not a flag ("--help" stays in the leftover words by design). A command that
// has no subcommands takes its leftover words as arguments, and its own
// argument rule says whether they fit ("tool login codex", "provider get prv_01").
func commandResolves(root *cobra.Command, printed string) error {
	found, rest, err := root.Find(strings.Fields(strings.TrimPrefix(printed, "olivares")))
	if err != nil {
		return err
	}
	if found == nil {
		return errors.New("no command found")
	}
	var args []string
	for _, word := range rest {
		if strings.HasPrefix(word, "-") {
			continue
		}
		if found.HasSubCommands() {
			return fmt.Errorf("%q is not a subcommand of %q", word, found.CommandPath())
		}
		args = append(args, word)
	}
	return found.ValidateArgs(args)
}

func TestCommandResolvesRejectsAMisspelledLeaf(t *testing.T) {
	root := newRootCmd()
	for _, printed := range []string{"olivares session lss", "olivares tool lss", "olivares governance approvals lss"} {
		if err := commandResolves(root, printed); err == nil {
			t.Errorf("%q resolved, want a refusal for the misspelled leaf", printed)
		}
	}
	// A command without subcommands takes words as arguments only as its own rule allows.
	for _, printed := range []string{"olivares tool login", "olivares tool login codex extra", "olivares doctor now"} {
		if err := commandResolves(root, printed); err == nil {
			t.Errorf("%q resolved, want a refusal for arguments the command does not take", printed)
		}
	}
	for _, printed := range []string{"olivares session ls", "olivares --help", "olivares tool login codex"} {
		if err := commandResolves(root, printed); err != nil {
			t.Errorf("%q: %v", printed, err)
		}
	}
}

// The context name, engine URL and next step are one row each: a newline or a
// tab in the user's own config must not break the heading or the footer.
func TestMainMenuFrameCellsStayOnOneRow(t *testing.T) {
	frame := mainMenuFrame(rootStatusReport{
		Context: "work\nestate\tone",
		Engine:  "http://127.0.0.1:7788\n",
		Next:    "olivares quickstart\t",
	})
	cells := map[string]string{
		"heading": frame.Heading,
		"footer":  frame.Footer,
		"detail":  frame.Items[0].Detail,
		"command": frame.Items[0].Command,
	}
	for name, cell := range cells {
		if strings.ContainsAny(cell, "\n\t") {
			t.Errorf("%s spans more than one row: %q", name, cell)
		}
	}
	if !strings.Contains(frame.Heading, "work estate one") {
		t.Errorf("heading = %q, want the name with spaces for the control characters", frame.Heading)
	}
}

func TestMainMenuFrameNamesRealCommands(t *testing.T) {
	root := newRootCmd()
	frame := mainMenuFrame(rootStatusReport{Next: "olivares quickstart"})
	if want := 6 + len(thisEdition.uiScreens.get()); len(frame.Items) != want {
		t.Fatalf("main menu has %d rows, want %d", len(frame.Items), want)
	}
	for _, item := range frame.Items {
		if err := commandResolves(root, item.Command); err != nil {
			t.Errorf("%q points at no command: %v", item.Command, err)
		}
	}
	if got := frame.Items[0].Command; got != "olivares quickstart" {
		t.Fatalf("Get started runs %q, want the report's next step", got)
	}
	if got := frame.Items[0].Detail; !strings.Contains(got, "olivares quickstart") {
		t.Fatalf("Get started detail = %q, want the next step", got)
	}
}

// The menu shows the next step whole, as "= <command>" (after Enter, "Use: <command>")
// on a row of its own, so every value rootNextFromTools can return for every tool
// in sessionToolOrder must fit tui.MinWidth after its 2-column indent and the
// longer of the two prefixes. Signed-in, signed-out and unknown-sign-in tools are
// each tried: they produce different commands.
func TestMainMenuNextStepFitsTheRowForEveryTool(t *testing.T) {
	const menuNoticePrefix = "  Use: "
	yes, no := true, false
	for _, driver := range sessionToolOrder {
		for name, signedIn := range map[string]*bool{"signed in": &yes, "signed out": &no, "unknown": nil} {
			next := rootNextFromTools([]toolRow{{Driver: driver, Installed: true, SignedIn: signedIn}})
			frame := mainMenuFrame(rootStatusReport{Next: next})
			if got := frame.Items[0].Command; got != next {
				t.Errorf("%s %s: Get started shows %q, want the whole next step %q", driver, name, got, next)
			}
			if n := utf8.RuneCountInString(menuNoticePrefix + next); n > tui.MinWidth {
				t.Errorf("%s %s: %q is %d columns with its indent and marker, want at most %d", driver, name, next, n, tui.MinWidth)
			}
		}
	}
	// No tool at all is its own branch of rootNextFromTools.
	none := rootNextFromTools(nil)
	if n := utf8.RuneCountInString(menuNoticePrefix + none); n > tui.MinWidth {
		t.Errorf("%q is %d columns with its indent and marker, want at most %d", none, n, tui.MinWidth)
	}
	for _, item := range mainMenuFrame(rootStatusReport{Next: "olivares quickstart"}).Items {
		if n := utf8.RuneCountInString(menuNoticePrefix + item.Command); n > tui.MinWidth {
			t.Errorf("%q is %d columns with its indent and marker, want at most %d", item.Command, n, tui.MinWidth)
		}
	}
}

func TestMainMenuFooterFollowsTheEngineState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report rootStatusReport
		want   string
		role   termrender.Role
	}{
		{"nothing installed", rootStatusReport{}, "No engine found on this machine", termrender.RoleWarn},
		{"running", rootStatusReport{Engine: "http://127.0.0.1:7788", Reachable: true}, "http://127.0.0.1:7788 (running)", termrender.RoleOK},
		{"not answering", rootStatusReport{Engine: "http://127.0.0.1:7788"}, "http://127.0.0.1:7788 (not answering)", termrender.RoleWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := mainMenuFrame(tc.report)
			if !strings.Contains(frame.Footer, tc.want) {
				t.Fatalf("footer = %q, want %q", frame.Footer, tc.want)
			}
			if !strings.Contains(frame.Footer, "version ") {
				t.Fatalf("footer = %q, want the version", frame.Footer)
			}
			if frame.FooterRole != tc.role {
				t.Fatalf("footer role = %v, want %v", frame.FooterRole, tc.role)
			}
		})
	}
}

func TestMainMenuHeadingNamesTheContext(t *testing.T) {
	frame := mainMenuFrame(rootStatusReport{Context: "lab", Next: "olivares login"})
	if !strings.Contains(frame.Heading, "Olivares AI") || !strings.Contains(frame.Heading, "lab") {
		t.Fatalf("heading = %q, want the product and the context", frame.Heading)
	}
	if frame.Right != editionLabel(thisEdition.name) {
		t.Fatalf("right label = %q, want the edition", frame.Right)
	}
}

func TestEditionLabel(t *testing.T) {
	if got := editionLabel("community"); got != "Community" {
		t.Fatalf("community label = %q", got)
	}
	if got := editionLabel("enterprise"); got != "Business" {
		t.Fatalf("enterprise label = %q", got)
	}
}
