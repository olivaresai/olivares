// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import "testing"

func TestConformance_FakeClaude(t *testing.T) {
	RunConformance(t, NewFake(KindClaude))
}

func TestConformance_FakeCodex(t *testing.T) {
	RunConformance(t, NewFake(KindCodex))
}

func TestConformance_FakeGrok(t *testing.T) {
	RunConformance(t, NewFake(KindGrok))
}

func TestFakeResumeRefusesEmptyConversation(t *testing.T) {
	d := NewFake(KindClaude)
	if _, err := d.Resume(t.Context(), ResumeRequest{}); err == nil {
		t.Fatal("empty conversation must refuse")
	}
}
