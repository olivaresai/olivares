// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestRuntimeCompanionUsesExactSessionAndCancellation(t *testing.T) {
	f := newOrchestrationFixture(t, []string{"work.read"})
	a := auth.NewAuthenticator(f.h.st, nil)
	p, err := a.Authenticate(t.Context(), f.token)
	if err != nil {
		t.Fatal(err)
	}
	companion, err := f.h.m.RuntimeCompanion(t.Context(), f.tenant, p)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := f.h.m.rt.getLive(f.tenant, f.run)
	rec, err := f.h.m.loadRun(t.Context(), f.tenant, f.run)
	if err != nil {
		t.Fatal(err)
	}
	if companion.Dir != rec.String(colRunWorkspacePath) || companion.Runner != f.h.m.rt.runner || companion.Isolation != IsolationNative || companion.LaunchID != live.launchID {
		t.Fatal("companion did not inherit its agent's runner, folder and generation")
	}
	for _, alter := range []func(*auth.Principal){func(p *auth.Principal) { p.SessionFence++ }, func(p *auth.Principal) { p.SessionRunRef = model.NewID().String() }, func(p *auth.Principal) { p.CredID = model.NewID() }} {
		foreign := p
		alter(&foreign)
		if _, err := f.h.m.RuntimeCompanion(t.Context(), f.tenant, foreign); err == nil {
			t.Fatal("mismatched credential can spawn in session")
		}
	}
	if _, err := f.h.m.RuntimeCompanion(t.Context(), model.TenantID(model.NewID()), p); err == nil {
		t.Fatal("foreign tenant can spawn")
	}
	if _, err := f.h.m.stopRun(context.Background(), f.tenant, f.run, "operator", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	select {
	case <-companion.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("session stop did not cancel companion lifetime")
	}
	if _, err := f.h.m.RuntimeCompanion(t.Context(), f.tenant, p); err == nil {
		t.Fatal("stopped session can spawn")
	}
}
