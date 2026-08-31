// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	codexsession "github.com/olivaresai/olivares/connectors/codex/session"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// B-05 — a governed Claude session must be distinguishable from an
// ungoverned one.
//
// The PEP could refuse a tool call and published nothing at all: zero edges, and
// the session id it had parsed out of the payload was propagated through two
// functions and dropped. The live view folds engine and posture off an edge's
// labels, so with no edge there was nothing to fold, and a policed session and an
// unwatched one rendered identically.

func testHookInput(event, tool, kind, ref, mode, session string) claude.HookDecisionInput {
	return claude.HookDecisionInput{
		Event: event, SessionID: session, Tool: tool,
		ResourceKind: kind, ResourceRef: ref, Mode: mode,
		At: time.Unix(1750000000, 0).UTC(),
	}
}

// The edge carries BOTH governance labels. A consumer that sees neither cannot
// tell an unlabelled producer from an unenforced one.
func TestClaudeEdgeCarriesEngineAndPosture(t *testing.T) {
	in := testHookInput("PreToolUse", "Bash", "shell", "ls -la", "read", "sess-1")
	edge, ok := claudeEdgeFor(in, true, in.At)
	if !ok {
		t.Fatal("a tool call with a session, a tool and a resource must produce an edge")
	}
	if got := edge.Labels[sdkmodel.LabelEngine]; got != sdkmodel.EngineClaude {
		t.Errorf("engine label = %q, want %q", got, sdkmodel.EngineClaude)
	}
	if got := edge.Labels[sdkmodel.LabelPosture]; got != sdkmodel.PostureEnforced {
		t.Errorf("posture on an enforceable event = %q, want %q", got, sdkmodel.PostureEnforced)
	}
	// The session id is READ, which is the whole defect: it used to die here.
	if edge.OriginRef != "sess-1" {
		t.Errorf("origin ref = %q, want the hook's session id", edge.OriginRef)
	}
	if edge.Confidence != sdkmodel.ConfidenceAttributed {
		t.Errorf("confidence = %v, want attributed (the id came from the payload, not a join)", edge.Confidence)
	}
}

// An event the hook could not have impeded is OBSERVED, never enforced. Claiming
// enforcement on a post-hoc event is the overstatement the column exists to stop.
func TestPostureIsObservedWhenTheHookCannotImpede(t *testing.T) {
	for _, event := range []string{"PostToolUse", "Stop"} {
		if got := postureForClaude(event); got != sdkmodel.PostureObserved {
			t.Errorf("%s: posture = %q, want %q", event, got, sdkmodel.PostureObserved)
		}
	}
	for _, event := range []string{"PreToolUse", "PermissionRequest"} {
		if got := postureForClaude(event); got != sdkmodel.PostureEnforced {
			t.Errorf("%s: posture = %q, want %q", event, got, sdkmodel.PostureEnforced)
		}
	}
}

// A DENIED call still produces an edge, and additionally a finding. What a
// session TRIED to do is the fact an operator needs; dropping denials would leave
// the graph showing only what governance failed to stop.
func TestADeniedCallStillProducesAnEdgeAndAFinding(t *testing.T) {
	in := testHookInput("PreToolUse", "Bash", "shell", "rm -rf /", "write", "sess-2")
	if _, ok := claudeEdgeFor(in, false, in.At); !ok {
		t.Error("a denied call must still produce an edge")
	}
	f, ok := claudeDenyFinding(in, false, "policy forbids destructive shell")
	if !ok {
		t.Fatal("a denied call must produce a finding")
	}
	if f.SubjectKind != "session" || f.SubjectRef != "sess-2" {
		t.Errorf("the finding must be session-scoped, got %s/%s", f.SubjectKind, f.SubjectRef)
	}
	// The reason may quote the operator's policy or the call's arguments: it is
	// hashed, never carried.
	if f.DetailHash == "" {
		t.Error("the finding must carry a detail hash")
	}
	for _, leak := range []string{"rm -rf /", "policy forbids destructive shell"} {
		if containsStr(f.Title, leak) || containsStr(f.DetailHash, leak) {
			t.Errorf("the finding leaks %q", leak)
		}
	}
	// An ALLOWED call produces no deny finding — only the edge.
	if _, ok := claudeDenyFinding(in, true, ""); ok {
		t.Error("an allowed call must not produce a deny finding")
	}
}

// Events that are not a resource access produce no edge: inventing one would
// inflate the session's tool-call count with things that are not tool calls.
func TestNonAccessEventsProduceNoEdge(t *testing.T) {
	cases := map[string]claude.HookDecisionInput{
		"no session":  testHookInput("PreToolUse", "Bash", "shell", "ls", "read", ""),
		"no tool":     testHookInput("PreToolUse", "", "shell", "ls", "read", "s"),
		"no resource": testHookInput("Stop", "Bash", "", "", "read", "s"),
	}
	for name, in := range cases {
		if _, ok := claudeEdgeFor(in, true, in.At); ok {
			t.Errorf("%s: must not produce an edge", name)
		}
	}
}

// The two engines must speak ONE dialect: the consumer folds both through the
// same code path, so a second set of label keys or posture values would be a
// second thing to keep in agreement by inspection.
func TestBothEnginesUseTheSameDeclaredVocabulary(t *testing.T) {
	if codexsession.LabelEngine != sdkmodel.LabelEngine {
		t.Errorf("codex engine label %q != sdk %q", codexsession.LabelEngine, sdkmodel.LabelEngine)
	}
	if codexsession.LabelPosture != sdkmodel.LabelPosture {
		t.Errorf("codex posture label %q != sdk %q", codexsession.LabelPosture, sdkmodel.LabelPosture)
	}
	if codexsession.EngineCodex == sdkmodel.EngineClaude {
		t.Error("the two engines must not share an engine key")
	}
	if codexsession.PostureObserved != sdkmodel.PostureObserved ||
		codexsession.PostureEnforced != sdkmodel.PostureEnforced {
		t.Errorf("posture values diverge: codex %q/%q vs sdk %q/%q",
			codexsession.PostureEnforced, codexsession.PostureObserved,
			sdkmodel.PostureEnforced, sdkmodel.PostureObserved)
	}
}

func containsStr(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
