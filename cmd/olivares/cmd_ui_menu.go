// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// cmd_ui_menu.go is the Sessions and Settings rows of the main menu. Sessions
// lists the engine's sessions and hands the terminal to the session shell of
// CLI-CONCEPT §5.4 (`olivares ui session`); Settings shows what `olivares config
// effective` prints beside where this installation points.

// wireSessionScreens opens the Sessions row. It needs what the tools screens need
// (an engine that answers and a saved sign-in), so it shares their state.
func wireSessionScreens(frame *tui.Frame, tools *uiTools) {
	if tools == nil {
		return
	}
	for i := range frame.Items {
		if frame.Items[i].Label == "Sessions" {
			frame.Items[i].Open = tools.sessions
		}
	}
}

// wireSettingsScreen opens the Settings row. It reads this computer only, so it
// needs no engine.
func wireSettingsScreen(frame *tui.Frame, report rootStatusReport) {
	for i := range frame.Items {
		if frame.Items[i].Label == "Settings" {
			frame.Items[i].Open = func(h *tui.Host) string { return settings(h, report) }
		}
	}
}

// sessions is the Open of the Sessions row: the list is read without blocking the
// keys, and a row ends the menu and opens the session shell on that session.
func (u *uiTools) sessions(h *tui.Host) string {
	const heading = "Sessions"
	menu := h.PushMenu(tui.Frame{Heading: heading, Footer: "Reading the engine…", Hints: screenHints})
	go func() {
		runs, err := u.cfg.listRuns(context.Background(), nil)
		h.Post(func() {
			if !h.Holds(menu) {
				return
			}
			if err != nil {
				menu.SetFrame(readFailedFrame(heading, "", err))
				return
			}
			menu.SetFrame(u.sessionsFrame(runs))
		})
	}()
	return ""
}

// sessionsFrame is the list: a new session first, then each session the engine
// named by a reference this CLI can use.
func (u *uiTools) sessionsFrame(runs []map[string]any) tui.Frame {
	items := []tui.MenuItem{{
		Label: "New session", Detail: "in this folder, with the defaults",
		Command: "olivares ui session new", Open: u.shell(uiSessionNew),
	}}
	for _, run := range runs {
		ref := str(run, "run_ref")
		if !runRefPattern.MatchString(ref) {
			continue
		}
		detail := uiStateWord(str(run, "state")) + " · " + sessionDriver(run)
		items = append(items, tui.MenuItem{
			Label: oneRow(sessionLabel(run)), Detail: oneRow(detail),
			Command: "olivares ui session " + shellWord(sessionLabel(run)), Open: u.shell(ref),
		})
	}
	return tui.Frame{
		Heading: "Sessions", Items: items, Hints: screenHints,
		Footer: fmt.Sprintf("%d sessions on this engine", len(items)-1),
	}
}

// shell is the Open of a row that opens the session shell: the menu ends, and the
// command runs the shell on the terminal the menu released (see uiTools.launch).
func (u *uiTools) shell(name string) func(*tui.Host) string {
	return func(h *tui.Host) string {
		u.launch = func(in, out *os.File) error {
			return runSessionShell(context.Background(), in, out, &u.cfg, nil, name)
		}
		h.Exit()
		return ""
	}
}

// settings is the Open of the Settings row.
func settings(h *tui.Host, report rootStatusReport) string {
	lines := []string{
		"  Context       " + valueOr(oneRow(report.Context), "none"),
		"  Engine        " + valueOr(oneRow(report.Engine), "none found on this machine"),
		"  Signed in as  " + valueOr(oneRow(report.SignedIn), "no one"),
		"  Edition       " + editionLabel(thisEdition.name) + " · version " + version,
		"",
		"  Configuration this computer reads (secrets hidden):",
	}
	out, _, err := runOlivares(context.Background(), []string{"config", "effective"})
	switch {
	case err != nil:
		lines = append(lines, errorLines(err)...)
	case out == "":
		lines = append(lines, "  Nothing is set: the defaults apply.")
	default:
		for _, line := range strings.Split(out, "\n") {
			lines = append(lines, "  "+oneRow(line))
		}
	}
	h.PushPanel(&tui.Panel{
		Heading: "Settings", Lines: lines, Command: "olivares config effective",
		Footer: "Enter goes back", FooterRole: termrender.RoleNone,
	})
	return ""
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
