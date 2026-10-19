// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func projectEdge(sid, project string) sdkmodel.EdgeObservation {
	e := codexEdge(sid, "Read", "observed")
	e.Labels = map[string]string{labelEngine: "claude"}
	if project != "" {
		e.Labels[labelProjectFolder] = project
	}
	return e
}

// An observed session's folder reaches the row the console reads: the list can name it
// "Claude Code · billing-api" only if the DTO carries the project.
func TestProjectReachesTheLiveView(t *testing.T) {
	t.Parallel()
	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	if err := m.onEdge(ctx, tenant.String(), projectEdge("osn_proj_1", "billing-api")); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	dto := m.toLiveDTO(liveRow(t, m, st, tenant, "osn_proj_1"))
	if dto.Project != "billing-api" {
		t.Errorf("DTO project = %q, want billing-api", dto.Project)
	}
}

// A later fact that names no folder leaves the known project as it was; a new folder
// (the agent changed directory) replaces it.
func TestProjectIsNeverClearedAndFollowsTheLatestFolder(t *testing.T) {
	t.Parallel()
	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	for _, step := range []struct{ project, want string }{
		{"billing-api", "billing-api"},
		{"", "billing-api"},
		{"ledger", "ledger"},
	} {
		if err := m.onEdge(ctx, tenant.String(), projectEdge("osn_proj_2", step.project)); err != nil {
			t.Fatalf("onEdge: %v", err)
		}
		if got := liveRow(t, m, st, tenant, "osn_proj_2").String(colProject); got != step.want {
			t.Errorf("after project %q: stored %q, want %q", step.project, got, step.want)
		}
	}
}

// The label is a producer's word: it is bounded on the way in, as a summary is.
func TestProjectIsBounded(t *testing.T) {
	t.Parallel()
	m, st, tenant, _ := newSess(t)
	if err := m.onEdge(context.Background(), tenant.String(), projectEdge("osn_proj_3", strings.Repeat("x", 4*maxProjectLen))); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	if got := liveRow(t, m, st, tenant, "osn_proj_3").String(colProject); len(got) != maxProjectLen {
		t.Errorf("stored %d bytes, want the %d cap", len(got), maxProjectLen)
	}
}

// An operator's `project` resource attribute is a FinOps dimension on cost samples and
// identity edges, not the session's folder: it must not name the row, or it would race
// with the hook's folder for the same column.
func TestOperatorProjectLabelDoesNotNameTheRow(t *testing.T) {
	t.Parallel()
	m, st, tenant, _ := newSess(t)
	e := codexEdge("osn_proj_4", "Read", "observed")
	e.Labels = map[string]string{labelEngine: "claude", "project": "billing"}
	if err := m.onEdge(context.Background(), tenant.String(), e); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	if got := liveRow(t, m, st, tenant, "osn_proj_4").String(colProject); got != "" {
		t.Errorf("stored project %q from an operator label, want none", got)
	}
}
