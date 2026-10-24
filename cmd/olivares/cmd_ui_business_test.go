// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

func mainMenuLabels(frame tui.Frame) []string {
	labels := make([]string, 0, len(frame.Items))
	for _, item := range frame.Items {
		labels = append(labels, item.Label)
	}
	return labels
}

// A build that adds no screens draws the six rows of CLI-CONCEPT §5.1 and no
// Business entry (issue 2574: "On Community the screens are not drawn").
func TestMainMenuFrameWithoutBusinessScreensDrawsNoBusinessEntry(t *testing.T) {
	asEdition(t, "community")
	thisEdition.uiScreens = nil
	got := mainMenuLabels(mainMenuFrame(rootStatusReport{Next: "olivares quickstart"}))
	want := []string{"Get started", "AI tools", "Sessions", "Security", "Settings", "All commands"}
	if !slices.Equal(got, want) {
		t.Fatalf("main menu rows = %q, want %q", got, want)
	}
}

// The rows an edition adds sit before "All commands", which stays the last row.
func TestMainMenuFrameDrawsTheEditionsScreensBeforeAllCommands(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.uiScreens = func() []tui.MenuItem {
		return []tui.MenuItem{
			{Label: "Organization", Detail: "departments, groups, rules", Command: "olivares workspaces tree"},
			{Label: "Cockpit", Detail: "sessions side by side", Command: "olivares session ls"},
		}
	}
	got := mainMenuLabels(mainMenuFrame(rootStatusReport{Next: "olivares quickstart"}))
	want := []string{"Get started", "AI tools", "Sessions", "Security", "Settings", "Organization", "Cockpit", "All commands"}
	if !slices.Equal(got, want) {
		t.Fatalf("main menu rows = %q, want %q", got, want)
	}
}

// Drawing no Business screen leaves the Business commands as they were: each
// answers the Business sentence with exit code 9 (installEditionAnswers). The
// DDIL group refuses before it reads a flag or the engine, so it needs no sign-in.
func TestCommunityBusinessCommandsStillAnswerExit9(t *testing.T) {
	asEdition(t, "community")
	thisEdition.uiScreens = nil
	assertDDILRefusedEverywhere(t)
}
