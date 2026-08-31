// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

const (
	knowledgeEmbedderServiceRole = "knowledge_embedder"
	knowledgeEmbedderServiceID   = "knowledge-embedder"
	knowledgeEmbedderTargetKind  = model.Kind("knowledge.embedder")
)

// The knowledge-plane posture reasons. They are a PUBLIC contract: they ride
// GET /status as knowledge_status_reason and `olivares status` prints them, so
// the strings are fixed and classified in exactly one place
// (knowledgePostureFor).
const (
	// reasonEmbeddingsConfigured — a model-backed provider resolved; retrieval is semantic.
	reasonEmbeddingsConfigured = "embeddings_provider_configured"
	// reasonEmbeddingsMissing — no embeddings provider was configured at ALL. The
	// product's deliberate default: zero-egress local-hash, lexical retrieval.
	reasonEmbeddingsMissing = "embeddings_provider_missing"
	// reasonEmbeddingsIncomplete — an operator STARTED configuring a provider (or
	// pinned one) and the block is unusable. Declared intent that is not being
	// honored: a fault to fix, never the pristine default.
	reasonEmbeddingsIncomplete = "embeddings_config_incomplete"
	// reasonKnowledgeUnwired — the status provider itself is absent (a wiring bug).
	reasonKnowledgeUnwired = "knowledge_status_provider_unwired"
)

// knowledgePostureFor classifies a reason into the posture the public status page
// renders. Only the zero-provider DEFAULT is benign; every other reason — a
// half-written provider block, a policy denial, an unreadable gate, an unwired
// seam — is a fault and keeps the degraded signal. Unknown reasons fall to
// impaired on purpose: a reason added later can only ever cost a louder signal,
// never a quieter one.
func knowledgePostureFor(reason string) api.CapabilityPosture {
	switch reason {
	case reasonEmbeddingsConfigured:
		return api.PostureReady
	case reasonEmbeddingsMissing:
		return api.PostureNotConfigured
	default:
		return api.PostureImpaired
	}
}

type knowledgePlaneStatus struct {
	mu           sync.RWMutex
	cur          api.KnowledgeStatus
	log          *slog.Logger
	st           store.Store
	guardPosture *sourcescope.Resolver
}

func newKnowledgePlaneStatus(initial api.KnowledgeStatus, log *slog.Logger) *knowledgePlaneStatus {
	if initial.EmbedderKind == "" {
		initial = localHashKnowledgeStatus(reasonEmbeddingsMissing)
	}
	return &knowledgePlaneStatus{cur: initial, log: log}
}

// localHashKnowledgeStatus reports the lexical fallback. The posture comes from
// the reason: no provider at all is the deliberate default (not_configured),
// while a broken or half-written one is a fault (impaired).
func localHashKnowledgeStatus(reason string) api.KnowledgeStatus {
	return api.KnowledgeStatus{EmbedderKind: "local-hash", RetrievalSemantic: false, Reason: reason, Posture: knowledgePostureFor(reason), GuardProfile: sourcescope.GuardProfileACLAware}
}

func semanticKnowledgeStatus(reason string) api.KnowledgeStatus {
	return api.KnowledgeStatus{EmbedderKind: "semantic", RetrievalSemantic: true, Reason: reason, Posture: api.PostureReady, GuardProfile: sourcescope.GuardProfileACLAware}
}

// semanticDeniedKnowledgeStatus reports a provider that IS configured but is not
// being used — denied by model-access, or its verdict unreadable. That is a fault
// on a provisioned plane, never the unprovisioned default.
func semanticDeniedKnowledgeStatus(reason string) api.KnowledgeStatus {
	return api.KnowledgeStatus{EmbedderKind: "semantic", RetrievalSemantic: false, Reason: reason, Posture: api.PostureImpaired, GuardProfile: sourcescope.GuardProfileACLAware}
}

func (s *knowledgePlaneStatus) KnowledgeStatus(ctx context.Context) api.KnowledgeStatus {
	if s == nil {
		return localHashKnowledgeStatus(reasonKnowledgeUnwired)
	}
	s.mu.RLock()
	cur := s.cur
	st := s.st
	guardPosture := s.guardPosture
	s.mu.RUnlock()
	cur.GuardProfile = sourcescope.GuardProfileACLAware
	cur.GuardWarning = ""
	cur.GuardDowngradeCount = 0
	cur.GuardPublicOnlyKBs = nil
	if st == nil || guardPosture == nil {
		return cur
	}
	downgrades, err := activeGuardDowngrades(ctx, st, guardPosture)
	if err != nil {
		cur.GuardWarning = "knowledge_guard_posture_status_unreadable"
		if s.log != nil {
			s.log.Warn("knowledge status: guard posture read failed", "err", err)
		}
		return cur
	}
	if len(downgrades) > 0 {
		cur.GuardProfile = sourcescope.GuardProfilePublicOnly
		cur.GuardWarning = "knowledge_guard_public_only_active"
		cur.GuardDowngradeCount = len(downgrades)
		cur.GuardPublicOnlyKBs = downgrades
	}
	return cur
}

func (s *knowledgePlaneStatus) set(next api.KnowledgeStatus) {
	if s == nil {
		return
	}
	if next.EmbedderKind == "" {
		next.EmbedderKind = "local-hash"
	}
	if next.GuardProfile == "" {
		next.GuardProfile = sourcescope.GuardProfileACLAware
	}
	s.mu.Lock()
	s.cur = next
	s.mu.Unlock()
}

func (s *knowledgePlaneStatus) useGuardPostureStore(st store.Store, r *sourcescope.Resolver) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.st = st
	s.guardPosture = r
	s.mu.Unlock()
}

func activeGuardDowngrades(ctx context.Context, st store.Store, r *sourcescope.Resolver) ([]api.KnowledgeGuardDowngrade, error) {
	if st == nil || r == nil {
		return nil, nil
	}
	var orgs []model.Org
	if err := st.System(ctx, func(sys store.SystemScope) error {
		var err error
		orgs, err = sys.ListOrgs(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	var out []api.KnowledgeGuardDowngrade
	for _, org := range orgs {
		tenant := model.TenantID(org.ID)
		if tenant.IsZero() || tenant.IsSystem() {
			continue
		}
		postures, err := r.ListGuardPostures(ctx, tenant)
		if err != nil {
			return nil, err
		}
		for _, gp := range postures {
			if gp.Profile != sourcescope.GuardProfilePublicOnly {
				continue
			}
			out = append(out, api.KnowledgeGuardDowngrade{
				TenantID:   org.ID.String(),
				TenantSlug: org.Slug,
				KBName:     gp.SourceRef,
				Profile:    gp.Profile,
				Reason:     gp.Reason,
				UpdatedBy:  gp.UpdatedBy,
			})
		}
	}
	return out, nil
}

type providerRefEmbedder interface {
	ProviderRef() string
}

// governedKnowledgeEmbedder makes the embedding model pass through model-access.
// The knowledge module only has tenant+text at the Embedder seam, so /cmd evaluates a
// stable service subject (`role:knowledge_embedder`) for that tenant. Operators can
// allow or forbid the embedding model exactly like any other model. A denial returns
// an error and is audited; it never falls through to local-hash as if semantic
// retrieval had succeeded.
type governedKnowledgeEmbedder struct {
	inner  knowledge.Embedder
	gate   modelAccessGate
	status *knowledgePlaneStatus
	log    *slog.Logger

	mu   sync.RWMutex
	data api.ModuleData
}

func newGovernedKnowledgeEmbedder(inner knowledge.Embedder, gate modelAccessGate, status *knowledgePlaneStatus, log *slog.Logger) *governedKnowledgeEmbedder {
	return &governedKnowledgeEmbedder{inner: inner, gate: gate, status: status, log: log}
}

func (g *governedKnowledgeEmbedder) UseData(data api.ModuleData) {
	g.mu.Lock()
	g.data = data
	g.mu.Unlock()
}

func (g *governedKnowledgeEmbedder) Embed(ctx context.Context, tenant model.TenantID, texts []string) ([][]float32, string, error) {
	if g == nil || g.inner == nil {
		return nil, "", fmt.Errorf("knowledge: semantic embedder not wired")
	}
	if err := g.checkModelAccess(ctx, tenant); err != nil {
		return nil, "", err
	}
	vecs, modelRef, err := g.inner.Embed(ctx, tenant, texts)
	if err == nil && g.status != nil {
		g.status.set(semanticKnowledgeStatus("embeddings_provider_configured"))
	}
	return vecs, modelRef, err
}

func (g *governedKnowledgeEmbedder) Dim() int {
	if g == nil || g.inner == nil {
		return 0
	}
	return g.inner.Dim()
}

func (g *governedKnowledgeEmbedder) AllowsEgress() bool {
	return g != nil && g.inner != nil && g.inner.AllowsEgress()
}

func (g *governedKnowledgeEmbedder) ModelRef() string {
	if g == nil || g.inner == nil {
		return ""
	}
	return g.inner.ModelRef()
}

// Region forwards the inner embedder's declared data-residency region (the
// inference_geo) so the knowledge module's residency-egress gate (kb.go embedderRegion)
// still sees the provider's geo THROUGH this governance wrapper. Region() is an OPTIONAL
// capability the module probes by interface assertion; if the wrapper hid it, a
// residency-locked KB with a correctly-configured in-region provider (OLIVARES_EMBEDDINGS
// _GEO) would be wrongly refused (embedderRegion reads "" and fails closed). The wrapper
// must stay capability-preserving.
func (g *governedKnowledgeEmbedder) Region() string {
	if g == nil || g.inner == nil {
		return ""
	}
	if r, ok := g.inner.(interface{ Region() string }); ok {
		return r.Region()
	}
	return ""
}

func (g *governedKnowledgeEmbedder) checkModelAccess(ctx context.Context, tenant model.TenantID) error {
	if g.gate == nil {
		return nil
	}
	principal := auth.ScopedPrincipal(model.ID(knowledgeEmbedderServiceID), "knowledge embedder", tenant, knowledgeEmbedderServiceRole)
	v, err := g.gate.EvaluateModelAccess(ctx, tenant, principal, "", g.providerRef(), g.ModelRef(), "")
	if err != nil {
		reason := "embedding_model_access_unreadable"
		g.status.set(semanticDeniedKnowledgeStatus(reason))
		g.auditModelAccessDenial(ctx, tenant, principal, reason)
		if g.log != nil {
			g.log.Warn("knowledge: embedding model-access unreadable; denying semantic embedder (deny-closed)", "model", g.ModelRef(), "err", err)
		}
		return fmt.Errorf("knowledge: embedding model-access check failed (deny-closed): %w", err)
	}
	if !v.Allowed {
		reason := "embedding_model_denied_by_model_access"
		g.status.set(semanticDeniedKnowledgeStatus(reason))
		g.auditModelAccessDenial(ctx, tenant, principal, reason)
		if g.log != nil {
			g.log.Warn("knowledge: embedding model denied by model-access; semantic embedder not used", "model", g.ModelRef(), "reason", v.Reason)
		}
		return fmt.Errorf("knowledge: embedding model %q denied by model-access", g.ModelRef())
	}
	return nil
}

func (g *governedKnowledgeEmbedder) providerRef() string {
	if p, ok := g.inner.(providerRefEmbedder); ok {
		return p.ProviderRef()
	}
	return ""
}

func (g *governedKnowledgeEmbedder) auditModelAccessDenial(ctx context.Context, tenant model.TenantID, principal auth.Principal, reason string) {
	g.mu.RLock()
	data := g.data
	g.mu.RUnlock()
	if data == nil {
		if g.log != nil {
			g.log.Warn("knowledge: could not audit embedding model-access denial; data handle not wired")
		}
		return
	}
	err := data.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      principal.Actor(),
			ActorKind:  principal.ActorKind(),
			Action:     "knowledge.embedder.model_access_denied",
			TargetKind: knowledgeEmbedderTargetKind,
			Meta: map[string]any{
				"model":    g.ModelRef(),
				"provider": g.providerRef(),
				"reason":   reason,
			},
		})
		return err
	})
	if err != nil && g.log != nil {
		g.log.Warn("knowledge: failed to audit embedding model-access denial", "err", err)
	}
}
