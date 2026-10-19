// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package seed

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// The demo workspace reads as a team's working day (#2268): about ten sessions
// over three or four projects and three engines, with start times, durations and
// event counts that differ, and none of them a single-event row. These cases pin
// that shape on the data the seed emits, not on the table alone.

func TestTeamSessionsAreAWorkingTeamNotABulk(t *testing.T) {
	team := TeamSessions()
	if n := len(team); n < 8 || n > 12 {
		t.Fatalf("%d team sessions, want 8 to 12", n)
	}
	projects, engines, counts, summaries, refs := map[string]bool{}, map[string]bool{}, map[int]bool{}, map[string]bool{}, map[string]bool{}
	for _, s := range team {
		if s.Events < 5 {
			t.Errorf("%s has %d tool calls: a session of a few events is the bulk this seed must not add", s.Ref, s.Events)
		}
		if s.Summary == "" || summaries[s.Summary] {
			t.Errorf("%s has an empty or repeated summary %q", s.Ref, s.Summary)
		}
		if len(s.Tools) == 0 {
			t.Errorf("%s names no tools", s.Ref)
		}
		if len(s.Name()) > 256 {
			t.Errorf("%s: name %q is longer than the 256 bytes the sessions module keeps", s.Ref, s.Name())
		}
		if refs[s.Ref] {
			t.Errorf("session %s listed twice", s.Ref)
		}
		if s.Started <= s.Last {
			t.Errorf("%s started %s before the boot clock and was last seen %s before it", s.Ref, s.Started, s.Last)
		}
		projects[s.Project], engines[s.Engine], counts[s.Events], summaries[s.Summary], refs[s.Ref] = true, true, true, true, true
	}
	if n := len(projects); n < 3 || n > 4 {
		t.Errorf("%d projects, want 3 or 4: %v", n, projects)
	}
	for _, e := range []string{"claude", "codex", "opencode"} {
		if !engines[e] {
			t.Errorf("no %s session: engines %v", e, engines)
		}
	}
	if len(counts) < len(team)-1 {
		t.Errorf("event counts barely vary: %v", counts)
	}
	for _, ref := range []string{SessionLive, SessionEvade, SessionBatch1, SessionBatch2, SessionBatch3, SessionBatch4} {
		if !refs[ref] {
			t.Errorf("the estate's stable session %s is missing from the team", ref)
		}
	}
}

// Working, idle and ended all appear at boot, so the Sessions view is not one
// state: the windows are the sessions module's (2 and 30 minutes).
func TestTeamSessionsSpanEveryState(t *testing.T) {
	var working, idle, ended int
	for _, s := range TeamSessions() {
		switch {
		case s.Last <= 2*time.Minute:
			working++
		case s.Last <= 30*time.Minute:
			idle++
		default:
			ended++
		}
	}
	if working < 2 || working > 4 {
		t.Errorf("%d working at boot, want 2 to 4", working)
	}
	if idle < 2 {
		t.Errorf("%d idle at boot, want at least 2", idle)
	}
	if ended < 3 {
		t.Errorf("%d ended at boot, want at least 3", ended)
	}
}

// The seed emits what the table says, edge for edge, as the connector's own
// resource-less tool edge (kind claude.tool, no ref): inventory catalogs the tool,
// and access-map honestly skips it. An edge that named a resource would also be an
// access, and ten sessions must not add rows to the unexpected-access list.
func TestTeamToolCallsAreEmittedPerSessionWithoutResources(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	byRef := map[string]int{}
	for _, o := range Observations(now) {
		e, ok := o.(model.EdgeObservation)
		if !ok || e.OriginKind != "session" || e.ToolRef == "" || e.ResourceRef != "" {
			continue
		}
		byRef[e.OriginRef]++
		if e.ResourceKind != "claude.tool" {
			t.Errorf("%s: tool call of kind %q, want the connector's claude.tool: an empty kind catalogs a blank resource", e.OriginRef, e.ResourceKind)
		}
		if e.Labels[model.LabelEngine] == "" || e.Labels[model.LabelPosture] != model.PostureObserved {
			t.Errorf("%s: tool call without its engine and observed posture: %v", e.OriginRef, e.Labels)
		}
		if e.Labels[model.LabelProjectFolder] == "" {
			t.Errorf("%s: tool call without its project folder: %v", e.OriginRef, e.Labels)
		}
		if e.ObservedAt.After(now) {
			t.Errorf("%s: tool call from the future: %s", e.OriginRef, e.ObservedAt)
		}
	}
	for _, s := range TeamSessions() {
		if got := byRef[s.Ref]; got != s.Events {
			t.Errorf("%s: %d resource-less tool calls emitted, table says %d", s.Ref, got, s.Events)
		}
	}
}

// The live session's in-flight call stays the last tool call emitted for it: the
// sessions module sets the current action per event in emit order.
func TestLiveSessionKeepsItsInFlightCallLast(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var last model.EdgeObservation
	for _, o := range Observations(now) {
		if e, ok := o.(model.EdgeObservation); ok && e.OriginKind == "session" && e.OriginRef == SessionLive && e.ToolRef != "" {
			last = e
		}
	}
	if last.ToolRef != ToolCreateIss {
		t.Fatalf("last tool call of %s is %q, want %q", SessionLive, last.ToolRef, ToolCreateIss)
	}
}
