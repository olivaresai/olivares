// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestReadRunWorkspacePathIsConfinedToTheWorkspace: the recorded working
// directory of a run in the named workspace, and one concealed answer for an
// absent, foreign, lineage-unset or unrecorded run.
func TestReadRunWorkspacePathIsConfinedToTheWorkspace(t *testing.T) {
	m, _, tenant, _ := newSess(t)
	alpha, bravo := launchTargetWorkspaces(t, m, tenant)
	at := func(dir string) func(model.Record) {
		return func(rec model.Record) { rec[colRunWorkspacePath] = dir }
	}
	seedLaunchTargetRun(t, m, tenant, alpha, "run-path-alpha", nil, at("/srv/work/alpha"))
	seedLaunchTargetRun(t, m, tenant, bravo, "run-path-bravo", nil, at("/srv/work/bravo"))
	seedLaunchTargetRun(t, m, tenant, alpha, "run-path-unrecorded", nil, nil)
	seedLaunchTargetRun(t, m, tenant, alpha, "run-path-unowned", nil, at("/srv/work/unowned"))
	clearLaunchTargetLineage(t, m, tenant, "run-path-unowned")

	ctx := context.Background()
	got, err := m.ReadRunWorkspacePath(ctx, tenant, alpha, "run-path-alpha")
	if err != nil || got != "/srv/work/alpha" {
		t.Fatalf("own run: %q %v, want /srv/work/alpha", got, err)
	}
	for name, tc := range map[string]struct {
		workspace model.ID
		run       string
	}{
		"foreign workspace": {alpha, "run-path-bravo"},
		"absent":            {alpha, "run-path-missing"},
		"no recorded path":  {alpha, "run-path-unrecorded"},
		"lineage unset":     {alpha, "run-path-unowned"},
		"empty reference":   {alpha, ""},
		"no workspace":      {"", "run-path-alpha"},
	} {
		if got, err := m.ReadRunWorkspacePath(ctx, tenant, tc.workspace, tc.run); !errors.Is(err, store.ErrNotFound) || got != "" {
			t.Errorf("%s: %q %v, want store.ErrNotFound", name, got, err)
		}
	}
}
