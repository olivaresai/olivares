// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// These are the AGPL composition-root adapters that make the source-scope gates
// REAL: they bridge the models module's ScopeGate and the knowledge module's
// RetrievalScopeGate ports to the sourcescope resolver, which decides via the
// grant engine + containment (model B) and selects the scoped credential. They
// live here, in /cmd (AGPL), for the same reason the knowledge retrieval guard does:
// the bridge wires an AGPL module port to another AGPL module's resolver, and the
// composition root is where cross-module wiring belongs (ARCHITECTURE.md). Both are
// DENY-CLOSED: a resolver error surfaces as a gate error, which each module treats as
// a deny (an unreadable scope state never authorizes a source).

// modelsScopeGate adapts the sourcescope resolver to models.ScopeGate: "may this
// session use this model?" against the model→workspace/agent-group binding.
type modelsScopeGate struct {
	resolver *sourcescope.Resolver
	log      *slog.Logger
}

var _ models.ScopeGate = modelsScopeGate{}

func (g modelsScopeGate) Allowed(ctx context.Context, tenant model.TenantID, q models.ScopeQuery) (models.ScopeVerdict, error) {
	dec, err := g.resolver.ResolveForSession(ctx, tenant, q.Principal, q.SessionRef, sourcescope.SourceModel, q.ModelRef)
	if err != nil {
		// Deny-closed: the models gate treats a gate error as a dropped candidate.
		return models.ScopeVerdict{}, err
	}
	return models.ScopeVerdict{Allowed: dec.Allowed, Reason: dec.Reason}, nil
}

// modelsActorScopeResolver adapts the sourcescope resolver to models.ActorScopeResolver:
// it resolves the acting session's scope (workspace + agent-groups) that the
// model-access decision matches its grants against. It reuses the SAME server-side actor
// resolution the model ScopeGate above relies on (the values are the store's, not the
// caller's). DENY-CLOSED: a resolver error surfaces to the model-access gate, which
// treats an unreadable actor scope as a deny.
type modelsActorScopeResolver struct {
	resolver *sourcescope.Resolver
	log      *slog.Logger
}

var _ models.ActorScopeResolver = modelsActorScopeResolver{}

func (g modelsActorScopeResolver) Resolve(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef string) (models.ActorScope, error) {
	ws, groups, err := g.resolver.ResolveActorScopeForPrincipal(ctx, tenant, principal, sessionRef)
	if err != nil {
		return models.ActorScope{}, err // deny-closed
	}
	return models.ActorScope{Workspace: ws, AgentGroups: groups}, nil
}

// knowledgeScopeGate adapts the sourcescope resolver to knowledge.RetrievalScopeGate:
// "may this agent retrieve from this knowledge base?" against the KB→scope binding.
type knowledgeScopeGate struct {
	resolver *sourcescope.Resolver
	log      *slog.Logger
}

var _ knowledge.RetrievalScopeGate = knowledgeScopeGate{}

func (g knowledgeScopeGate) Allowed(ctx context.Context, tenant model.TenantID, principal auth.Principal, agentRef, kbRef string) (bool, string, error) {
	dec, err := g.resolver.ResolveForAgent(ctx, tenant, principal, agentRef, sourcescope.SourceKnowledge, kbRef)
	if err != nil {
		return false, "", err // deny-closed
	}
	return dec.Allowed, dec.Reason, nil
}
