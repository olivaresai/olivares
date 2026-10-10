// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

func TestDecisionBenchmarkHarnessCredential(t *testing.T) {
	h := newDecisionBenchmarkHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	authr := auth.NewAuthenticator(h.st, nil)
	p, err := authr.Authenticate(ctx, h.bearer)
	if err != nil {
		t.Fatalf("authenticate decision benchmark credential: %v", err)
	}
	role, ok := p.RoleIn(h.tenant)
	if p.UserID.IsZero() || p.AgentIdentity != "agent@e2e.test" || !ok || role != auth.RoleEditor {
		t.Fatalf("benchmark credential must identify an editor agent in its tenant: %+v", p)
	}
	ref, ok := p.Ref()
	if !ok {
		t.Fatal("benchmark credential must carry authority evidence for governed decisions")
	}
	if _, err := authr.ResolvePrincipalScope(ctx, ref, h.tenant); err != nil {
		t.Fatalf("resolve benchmark credential authority: %v", err)
	}
}
