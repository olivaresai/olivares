// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

type countingSemanticEmbedder struct {
	calls int
}

func (e *countingSemanticEmbedder) Embed(context.Context, model.TenantID, []string) ([][]float32, string, error) {
	e.calls++
	return [][]float32{{0.1, 0.2, 0.3}}, "text-embedding-denied", nil
}

func (e *countingSemanticEmbedder) Dim() int            { return 3 }
func (e *countingSemanticEmbedder) AllowsEgress() bool  { return true }
func (e *countingSemanticEmbedder) ModelRef() string    { return "text-embedding-denied" }
func (e *countingSemanticEmbedder) ProviderRef() string { return "openai" }

// regionEmbedder is a model-backed embedder that declares a data-residency region —
// the OPTIONAL Region() capability the knowledge module probes (kb.go embedderRegion).
type regionEmbedder struct{ region string }

func (regionEmbedder) Embed(context.Context, model.TenantID, []string) ([][]float32, string, error) {
	return [][]float32{{1}}, "emb", nil
}
func (regionEmbedder) Dim() int           { return 1 }
func (regionEmbedder) AllowsEgress() bool { return true }
func (regionEmbedder) ModelRef() string   { return "emb" }
func (e regionEmbedder) Region() string   { return e.region }

// TestGovernedKnowledgeEmbedder_ForwardsRegion proves the governance wrapper is
// capability-preserving for the OPTIONAL Region() seam: a residency-locked KB with an
// in-region provider must NOT be refused merely because the embedder is wrapped for
// model-access (the residency gate reads the geo THROUGH the wrapper). An inner without
// Region() yields "" (undeclared → fail-closed), never a panic.
func TestGovernedKnowledgeEmbedder_ForwardsRegion(t *testing.T) {
	g := newGovernedKnowledgeEmbedder(regionEmbedder{region: "us"}, nil, nil, discardLog())
	r, ok := any(g).(interface{ Region() string })
	if !ok {
		t.Fatal("governed embedder must expose Region() so the module residency gate reads the provider geo through the wrapper")
	}
	if got := r.Region(); got != "us" {
		t.Fatalf("Region() through wrapper = %q, want us", got)
	}
	g2 := newGovernedKnowledgeEmbedder(&countingSemanticEmbedder{}, nil, nil, discardLog())
	if got := any(g2).(interface{ Region() string }).Region(); got != "" {
		t.Fatalf("Region() with region-less inner = %q, want empty", got)
	}
}

// The posture is what lets the public status page tell "nobody configured this"
// apart from "this is broken". Only the zero-provider default is benign; every
// other reason, including one nobody has invented yet, is a fault.
func TestKnowledgePostureClassifiesReasons(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   api.CapabilityPosture
	}{
		{reasonEmbeddingsConfigured, api.PostureReady},
		{reasonEmbeddingsMissing, api.PostureNotConfigured},
		{reasonEmbeddingsIncomplete, api.PostureImpaired},
		{reasonKnowledgeUnwired, api.PostureImpaired},
		{"embedding_model_denied_by_model_access", api.PostureImpaired},
		{"embedding_model_access_unreadable", api.PostureImpaired},
		{"a_reason_invented_next_year", api.PostureImpaired},
		{"", api.PostureImpaired},
	} {
		if got := knowledgePostureFor(tc.reason); got != tc.want {
			t.Errorf("knowledgePostureFor(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
}

// The three constructors are the only production builders of the status, so the
// posture they stamp IS the contract the status page reads.
func TestKnowledgeStatusConstructorsCarryPosture(t *testing.T) {
	if got := localHashKnowledgeStatus(reasonEmbeddingsMissing).Posture; got != api.PostureNotConfigured {
		t.Errorf("local-hash with no provider = %q, want not_configured", got)
	}
	if got := localHashKnowledgeStatus(reasonEmbeddingsIncomplete).Posture; got != api.PostureImpaired {
		t.Errorf("local-hash with a half-written provider = %q, want impaired", got)
	}
	if got := semanticKnowledgeStatus(reasonEmbeddingsConfigured).Posture; got != api.PostureReady {
		t.Errorf("semantic = %q, want ready", got)
	}
	if got := semanticDeniedKnowledgeStatus("embedding_model_denied_by_model_access").Posture; got != api.PostureImpaired {
		t.Errorf("semantic denied by model-access = %q, want impaired (the provider IS configured)", got)
	}
	// The nil receiver reports an unwired seam — a composition-root fault.
	var absent *knowledgePlaneStatus
	if got := absent.KnowledgeStatus(context.Background()); got.Posture != api.PostureImpaired || got.Reason != reasonKnowledgeUnwired {
		t.Errorf("unwired status provider = %+v, want impaired/%s", got, reasonKnowledgeUnwired)
	}
}

func TestGovernedKnowledgeEmbedder_ModelAccessDeniedAuditsAndDoesNotFallback(t *testing.T) {
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}

	inner := &countingSemanticEmbedder{}
	status := newKnowledgePlaneStatus(semanticKnowledgeStatus("embeddings_provider_configured"), discardLog())
	gate := &fakeProxyModels{v: models.ModelAccessVerdict{Allowed: false, Reason: "denied by policy"}}
	embedder := newGovernedKnowledgeEmbedder(inner, gate, status, slog.New(slog.NewTextHandler(io.Discard, nil)))
	embedder.UseData(api.NewModuleData(st))

	_, _, err = embedder.Embed(ctx, tenant, []string{"secret corpus"})
	if err == nil {
		t.Fatal("denied embedding model must fail closed")
	}
	if inner.calls != 0 {
		t.Fatalf("semantic provider was called %d times after model-access denial", inner.calls)
	}
	stNow := status.KnowledgeStatus(ctx)
	if stNow.EmbedderKind != "semantic" || stNow.RetrievalSemantic || stNow.Reason != "embedding_model_denied_by_model_access" {
		t.Fatalf("status after denial = %+v", stNow)
	}

	found := 0
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action == "knowledge.embedder.model_access_denied" {
				found++
				if ev.ActorKind != "token" || ev.Actor != "token:"+knowledgeEmbedderServiceID {
					t.Errorf("audit actor = %s/%s, want token/token:%s", ev.ActorKind, ev.Actor, knowledgeEmbedderServiceID)
				}
				if ev.TargetKind != knowledgeEmbedderTargetKind {
					t.Errorf("audit target kind = %s, want %s", ev.TargetKind, knowledgeEmbedderTargetKind)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	if found != 1 {
		t.Fatalf("model-access denial audit events = %d, want 1", found)
	}
}

func TestKnowledgeStatusReportsGuardPublicOnlyDowngrade(t *testing.T) {
	ctx := context.Background()
	ss := sourcescope.New()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, ss.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	ss.UseData(api.NewModuleData(st))
	seedGuardPublicOnly(t, st, tenant, "handbook")

	status := newKnowledgePlaneStatus(semanticKnowledgeStatus("embeddings_provider_configured"), discardLog())
	status.useGuardPostureStore(st, ss.Resolver())
	got := status.KnowledgeStatus(ctx)
	if got.GuardProfile != sourcescope.GuardProfilePublicOnly || got.GuardWarning != "knowledge_guard_public_only_active" || got.GuardDowngradeCount != 1 {
		t.Fatalf("guard status = %+v, want public_only warning count=1", got)
	}
	if len(got.GuardPublicOnlyKBs) != 1 || got.GuardPublicOnlyKBs[0].KBName != "handbook" || got.GuardPublicOnlyKBs[0].TenantSlug != "acme" {
		t.Fatalf("guard public-only KBs = %+v", got.GuardPublicOnlyKBs)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("sourcescope.guard_posture"))
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 10})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if err := repo.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("clear guard posture: %v", err)
	}
	got = status.KnowledgeStatus(ctx)
	if got.GuardProfile != sourcescope.GuardProfileACLAware || got.GuardWarning != "" || got.GuardDowngradeCount != 0 || len(got.GuardPublicOnlyKBs) != 0 {
		t.Fatalf("guard status after re-enable = %+v, want acl-aware no warning", got)
	}
}
