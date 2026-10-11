// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

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
