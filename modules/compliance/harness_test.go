// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// fixedClock is a deterministic clock for reproducible timestamps in tests.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() model.Timestamp { return model.NewTimestamp(c.t) }

// movableClock is an advanceable clock for the retention tests: rows are
// stamped by the STORE's own (system) clock, so a test ages them by moving the
// MODULE clock forward instead of sleeping (cutoff = module-now − retention_days).
type movableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *movableClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

func (c *movableClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// stubApprovalGate is a programmable ApprovalGate: tests flip its decision
// (pending → approved with N approvers) and inspect the requests it saw.
type stubApprovalGate struct {
	mu        sync.Mutex
	status    string
	ref       string
	approvers []string
	// persons is the PEOPLE behind those credentials. nil ⇒ one person per credential
	// (the ordinary case every fixture means). setIdentities sets it apart so a test can
	// state the case that matters: several credentials, ONE human.
	persons  []string
	personsX bool
	planHash string // "" ⇒ echo the request's hash (the bound-plan happy path)
	err      error
	reqs     []GateRequest
}

func (g *stubApprovalGate) Authorize(_ context.Context, _ model.TenantID, req GateRequest) (GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = append(g.reqs, req)
	if g.err != nil {
		return GateDecision{}, g.err
	}
	ph := g.planHash
	if ph == "" {
		ph = req.PlanHash
	}
	// A stub configured with N approvers models N distinct PEOPLE, each acting through
	// one credential — so it states both identities. Setting only Approvers would make
	// the fixture assert the very confusion removed (credentials counted as humans),
	// and every quorum assertion below would then be measuring nothing.
	persons := g.approvers
	if g.personsX {
		persons = g.persons
	}
	return GateDecision{
		Status: g.status, ApprovalRef: g.ref, PlanHash: ph,
		Approvers:       append([]string(nil), g.approvers...),
		ApproverPersons: append([]string(nil), persons...),
	}, nil
}

func (g *stubApprovalGate) set(status, ref string, approvers ...string) {
	g.mu.Lock()
	g.status, g.ref, g.approvers, g.persons, g.personsX = status, ref, approvers, nil, false
	g.mu.Unlock()
}

// setIdentities states the two identities APART: which credentials approved, and which
// PEOPLE stood behind them. It exists for the one case the ordinary fixture cannot
// express — one human holding several credentials — which is exactly the case a quorum
// counted on credentials gets wrong.
func (g *stubApprovalGate) setIdentities(status, ref string, credentials, persons []string) {
	g.mu.Lock()
	g.status, g.ref, g.approvers, g.persons, g.personsX = status, ref, credentials, persons, true
	g.mu.Unlock()
}

func (g *stubApprovalGate) requests() []GateRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]GateRequest(nil), g.reqs...)
}

// stubProviderRetention is a wired §7 floor source (the models adapter stand-in).
type stubProviderRetention struct {
	days   int
	source string
}

func (s stubProviderRetention) MaxForcedRetentionDays(context.Context) (int, string) {
	return s.days, s.source
}

// fakeLineage is a wired LineageSource returning n egress signals — exercises the
// residency scan's seam path without coupling the test to module VIII.
type fakeLineage struct{ n int }

func (f fakeLineage) EgressSignals(_ context.Context, _ model.TenantID) ([]EgressSignal, error) {
	out := make([]EgressSignal, 0, f.n)
	for i := 0; i < f.n; i++ {
		out = append(out, EgressSignal{Source: "test", Ref: "lin", Detail: "egress"})
	}
	return out, nil
}

// lineageStandInKind is a minimal stand-in for module VIII's lineage entity, so the
// inline egress-reading path (and the data_lineage capability) are exercised for real
// without importing the knowledge module.
const lineageStandInKind model.Kind = "knowledge.lineage"

func registerLineageStandIn(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:       lineageStandInKind,
		Table:      "knowledge_lineage",
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: "egress", Kind: model.KindBool, Indexed: true},
			{Name: "session_ref", Kind: model.KindText, Nullable: true},
		},
	})
}

// costSampleStandInKind is a minimal stand-in for FinOps' cost-sample read model, so
// the resource_accounting capability (FIN-12) is exercised for real without importing
// the finops module. NOT AppendOnly, mirroring the real descriptor (the read model is
// a mutable ingestion upsert, modules/finops/schema.go) — the sweep must be able
// to Delete its rows.
const costSampleStandInKind model.Kind = "finops.cost_sample"

func registerCostSampleStandIn(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  costSampleStandInKind,
		Table: "finops_cost_sample",
		Fields: []model.FieldSpec{
			{Name: "input_tokens", Kind: model.KindInt},
			{Name: "cost_micro_usd", Kind: model.KindInt},
			// inference_geo mirrors finops' column so the residency pin coherence
			// check (distinctInferenceGeos) has a real geo to read.
			{Name: "inference_geo", Kind: model.KindText, Nullable: true},
			// workspace_ref mirrors finops' attribution dimension so the
			// workspace-geo drift scan can scope observed geos to a workspace.
			{Name: "workspace_ref", Kind: model.KindText, Nullable: true},
			// actor + cost_record_id mirror finops' Attribution columns
			// (modules/finops/schema.go:131,139) so the erasure scrub of the raw
			// developer email — and its propagation to the linked canonical
			// cost_records row — is exercised against real columns.
			{Name: "actor", Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: "cost_record_id", Kind: model.KindText, Nullable: true},
		},
	})
}

// workspaceResidencyStandInKind is a minimal stand-in for the models module's
// per-workspace Anthropic data-residency mirror, so the workspace-geo drift
// branch of the residency scan is exercised for real without importing the models
// module. allowed_geos is comma-separated lowercase; EMPTY = unrestricted/unreported.
const workspaceResidencyStandInKind model.Kind = "models.workspace_residency"

func registerWorkspaceResidencyStandIn(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  workspaceResidencyStandInKind,
		Table: "models_workspace_residency",
		Fields: []model.FieldSpec{
			{Name: "workspace_ref", Kind: model.KindText, Indexed: true},
			{Name: "allowed_geos", Kind: model.KindText},
			{Name: "default_geo", Kind: model.KindText},
			{Name: "workspace_geo", Kind: model.KindText},
			{Name: "as_of", Kind: model.KindText},
		},
	})
}

// gpaiStandInKind is a minimal stand-in for the models module's per-provider GPAI
// posture entity, so the supplier_gpai_posture capability (FIN-13) is exercised
// for real without importing the models module.
const gpaiStandInKind model.Kind = "models.gpai_posture"

func registerGPAIStandIn(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  gpaiStandInKind,
		Table: "models_gpai_posture",
		Fields: []model.FieldSpec{
			{Name: "provider_ref", Kind: model.KindText, Indexed: true},
			{Name: "verified", Kind: model.KindBool, Indexed: true},
		},
	})
}

// modelAdmissionStandInKind / aibomStandInKind are minimal stand-ins for the models
// module's signed-model admission verdict and sealed AIBOM, so the
// signed_model_admission (claim-vs-verified) and model_aibom capabilities are
// exercised for real without importing the models module.
const modelAdmissionStandInKind model.Kind = "models.model_admission"
const aibomStandInKind model.Kind = "models.aibom"

func registerModelSupplyChainStandIns(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  modelAdmissionStandInKind,
		Table: "models_model_admission",
		Fields: []model.FieldSpec{
			{Name: "version_ref", Kind: model.KindText, Indexed: true},
			{Name: "signature_verified", Kind: model.KindBool, Indexed: true},
		},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind: aibomStandInKind, Table: "models_aibom", AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: "owned_ref", Kind: model.KindText, Indexed: true},
			{Name: "content_hash", Kind: model.KindText, Indexed: true},
		},
	})
}

// piiScanStandInKind / dlpRuleStandInKind / dlpEventStandInKind are minimal stand-ins
// for module VIII's PII-discovery scan, DLP rule and DLP enforcement-event
// entities, so the pii_discovery and dlp_enforcement capability probes are exercised
// for real without importing the knowledge module.
const (
	piiScanStandInKind  model.Kind = "knowledge.pii_scan"
	dlpRuleStandInKind  model.Kind = "knowledge.dlp_rule"
	dlpEventStandInKind model.Kind = "knowledge.dlp_event"
)

func registerKnowledgeDLPStandIns(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind: piiScanStandInKind, Table: "knowledge_pii_scan", AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: "scope_kind", Kind: model.KindText, Indexed: true},
			{Name: "docs_with_hits", Kind: model.KindInt},
			{Name: "detector_version", Kind: model.KindText},
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind: dlpRuleStandInKind, Table: "knowledge_dlp_rule",
		Fields: []model.FieldSpec{
			{Name: "class", Kind: model.KindText, Indexed: true},
			{Name: "action", Kind: model.KindText},
		},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind: dlpEventStandInKind, Table: "knowledge_dlp_event", AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: "dlp_action", Kind: model.KindText, Indexed: true},
			{Name: "chunks_withheld", Kind: model.KindInt},
		},
	})
}

// Stand-ins for the purgeable data classes, mirroring the REAL columns of the
// owning modules (an internal design note (not shipped), voice/schema.go, knowledge/schema.go — a
// representative subset incl. every column the registry's age predicate and
// subject mappings touch), so the retention sweep is exercised against real tables
// without importing the sibling modules. finops.cost_sample reuses the FIN-12
// stand-in above.
const (
	sessionsLiveStandInKind     model.Kind = "sessions.live"
	sessionsTimelineStandInKind model.Kind = "sessions.timeline"
	voiceSessionStandInKind     model.Kind = "voice.session"
	knowledgeMemoryStandInKind  model.Kind = "knowledge.memory"
	scopedMemoryStandInKind     model.Kind = "knowledge.memory_scoped"
)

func registerRetentionStandIns(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  sessionsLiveStandInKind,
		Table: "sessions_live",
		Fields: []model.FieldSpec{
			{Name: "session_ref", Kind: model.KindText},
			{Name: "agent_ref", Kind: model.KindText, Nullable: true},
			{Name: "event_count", Kind: model.KindInt},
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  sessionsTimelineStandInKind,
		Table: "sessions_timeline",
		Fields: []model.FieldSpec{
			{Name: "session_ref", Kind: model.KindText, Indexed: true},
			{Name: "at", Kind: model.KindTimestamp},
			{Name: "kind", Kind: model.KindText},
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  voiceSessionStandInKind,
		Table: "voice_session",
		Fields: []model.FieldSpec{
			{Name: "session_ref", Kind: model.KindText},
			{Name: "agent_ref", Kind: model.KindText, Indexed: true},
			{Name: "duration_ms", Kind: model.KindInt},
			// principal_ref mirrors voice's real opener column
			// (modules/voice/schema.go:29) — the user-subject mapping.
			{Name: "principal_ref", Kind: model.KindText, Nullable: true},
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  knowledgeMemoryStandInKind,
		Table: "knowledge_memory",
		Fields: []model.FieldSpec{
			{Name: "agent_ref", Kind: model.KindText, Indexed: true},
			{Name: "mkey", Kind: model.KindText, Indexed: true},
			{Name: "content", Kind: model.KindText},
			{Name: "expires_at", Kind: model.KindTimestamp, Nullable: true, Indexed: true},
		},
	}); err != nil {
		return err
	}
	// the per-user/per-session memory namespace (modules/knowledge/
	// schema.go scopedMemoryKind): the columns the user/session subject mappings
	// (dataclass + erasure target) touch.
	return reg.Register(model.EntityDescriptor{
		Kind:  scopedMemoryStandInKind,
		Table: "knowledge_memory_scoped",
		Fields: []model.FieldSpec{
			{Name: "agent_ref", Kind: model.KindText, Indexed: true},
			{Name: "user_ref", Kind: model.KindText, Indexed: true},
			{Name: "session_ref", Kind: model.KindText, Indexed: true},
			{Name: "mkey", Kind: model.KindText, Indexed: true},
			{Name: "content", Kind: model.KindText},
			{Name: "expires_at", Kind: model.KindTimestamp, Nullable: true, Indexed: true},
		},
	})
}

// Stand-ins for the erasure targets that live outside the registry classes:
// the knowledge document-cascade entities (base/document/chunk + the label,
// mirroring modules/knowledge/schema.go:66-96,147-163 — the columns the cascade
// touches) and the NHI lifecycle overlay (owner_ref/sponsor_ref scrub,
// modules/governance/schema.go:53-56).
const (
	kbStandInKind       model.Kind = "knowledge.base"
	documentStandInKind model.Kind = "knowledge.document"
	chunkStandInKind    model.Kind = "knowledge.chunk"
	labelStandInKind    model.Kind = "knowledge.sensitivity_label"
	nhiLifeStandInKind  model.Kind = "governance.nhi_lifecycle"
)

func registerErasureStandIns(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  kbStandInKind,
		Table: "knowledge_base",
		Fields: []model.FieldSpec{
			{Name: "name", Kind: model.KindText},
			{Name: "owner_ref", Kind: model.KindText, Nullable: true},
			{Name: "doc_count", Kind: model.KindInt},
			{Name: "chunk_count", Kind: model.KindInt},
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  documentStandInKind,
		Table: "knowledge_document",
		Fields: []model.FieldSpec{
			{Name: "kb_ref", Kind: model.KindText, Indexed: true},
			{Name: "title", Kind: model.KindText, Nullable: true},
			{Name: "content_hash", Kind: model.KindText, Nullable: true},
			{Name: "chunk_count", Kind: model.KindInt},
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  chunkStandInKind,
		Table: "knowledge_chunk",
		Fields: []model.FieldSpec{
			{Name: "doc_ref", Kind: model.KindText, Indexed: true},
			{Name: "chunk_index", Kind: model.KindInt},
			{Name: "text", Kind: model.KindText},
			{Name: "embedding", Kind: model.KindBytes, Nullable: true},
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  labelStandInKind,
		Table: "knowledge_sensitivity_label",
		Fields: []model.FieldSpec{
			{Name: "subject_kind", Kind: model.KindText, Indexed: true},
			{Name: "subject_ref", Kind: model.KindText, Indexed: true},
			{Name: "classes", Kind: model.KindJSON, Nullable: true},
			{Name: "max_severity", Kind: model.KindText, Nullable: true},
		},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:  nhiLifeStandInKind,
		Table: "governance_nhi_lifecycle",
		Fields: []model.FieldSpec{
			{Name: "identity_ref", Kind: model.KindText, Indexed: true},
			{Name: "owner_ref", Kind: model.KindText, Nullable: true},
			{Name: "sponsor_ref", Kind: model.KindText, Nullable: true},
		},
	})
}

// recordingSessionStandInKind is a minimal stand-in for the recording module's
// privileged-session entity, so the session_recording capability probe is
// exercised for real without importing the recording module.
const recordingSessionStandInKind model.Kind = "recording.session"

func registerRecordingStandIn(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind: recordingSessionStandInKind, Table: "recording_session",
		Fields: []model.FieldSpec{
			{Name: "subject", Kind: model.KindText, Indexed: true},
			{Name: "status", Kind: model.KindText, Indexed: true},
		},
	})
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	mod      *Module // the module under test (for the exported Go seams, e.g. CheckHold)
	setupTok string

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

// newHarness builds a compliance plane. opts are passed to the module (e.g. a wired
// lineage source); a deterministic clock is always injected.
func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	// The default deterministic clock is PREPENDED so a test-supplied WithClock
	// (e.g. the movableClock) applied later wins.
	opts = append([]Option{WithClock(fixedClock{t: time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)})}, opts...)
	mod := New(opts...)
	h.mod = mod

	// Register the module's schema AND a stand-in knowledge.lineage so both the
	// data_lineage capability and the inline egress scan are exercised against a real store.
	register := func(reg store.ExtensionRegistry) error {
		if err := mod.RegisterSchema(reg); err != nil {
			return err
		}
		if err := registerLineageStandIn(reg); err != nil {
			return err
		}
		if err := registerCostSampleStandIn(reg); err != nil {
			return err
		}
		if err := registerGPAIStandIn(reg); err != nil {
			return err
		}
		if err := registerWorkspaceResidencyStandIn(reg); err != nil {
			return err
		}
		if err := registerModelSupplyChainStandIns(reg); err != nil {
			return err
		}
		if err := registerKnowledgeDLPStandIns(reg); err != nil {
			return err
		}
		if err := registerRetentionStandIns(reg); err != nil {
			return err
		}
		if err := registerErasureStandIns(reg); err != nil {
			return err
		}
		return registerRecordingStandIn(reg)
	}

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, register)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	mod.UseData(api.NewModuleData(st))

	bus := eventbus.NewInProc(eventbus.Options{})
	if _, err := bus.Subscribe([]event.Type{event.TypeFindingReported}, func(_ context.Context, e event.Event) error {
		if f, ok := event.FindingOf(e); ok {
			h.findMu.Lock()
			h.findings = append(h.findings, f)
			h.findMu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx); _ = bus.Close() })

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{mod},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.setupTok = srv, plaintext
	return h
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func (h *harness) do(method, path, token string, body any, hdr map[string]string) resp {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func tenantHdr(t model.TenantID) map[string]string {
	return map[string]string{"X-Olivares-Tenant": t.String()}
}

func (h *harness) adminLogin() string {
	h.t.Helper()
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) createOrg(token, slug string) model.TenantID {
	h.t.Helper()
	r := h.do("POST", "/v1/system/orgs", token, map[string]any{"name": slug, "slug": slug}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org %s = %d %s", slug, r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

func (h *harness) roleToken(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

// --- seed helpers: insert real core/ext rows so capability probes have evidence ----

func (h *harness) seedAgent(tenant model.TenantID, name string) model.ID {
	h.t.Helper()
	var id model.ID
	h.mutate(tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{Name: name, Kind: "claude-code", Status: model.StatusActive})
		id = a.ID
		return err
	})
	return id
}

func (h *harness) seedEdge(tenant model.TenantID, agent model.ID, mode sdkmodel.AccessMode, permitted, observed bool) {
	h.t.Helper()
	now := model.NewTimestamp(time.Now())
	h.mutate(tenant, func(sc store.Scope) error {
		_, err := sc.AccessEdges().Upsert(context.Background(), model.AccessEdge{
			OriginKind: "agent", OriginID: agent, ResourceID: model.NewID(),
			Mode: mode, SignalSource: sdkmodel.SignalOTEL, Confidence: sdkmodel.ConfidenceAttributed,
			Permitted: permitted, Observed: observed, FirstSeen: now, LastSeen: now, OccurrenceCount: 1,
		})
		return err
	})
}

func (h *harness) seedFinding(tenant model.TenantID, subject model.ID, kind string, sev model.Severity) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		_, err := sc.Findings().Create(context.Background(), model.Finding{
			Kind: kind, Severity: sev, Status: model.FindingOpen, Source: "test",
			SubjectKind: "agent", SubjectID: subject, Title: "seed", OccurredAt: model.NewTimestamp(time.Now()),
		})
		return err
	})
}

func (h *harness) seedEval(tenant model.TenantID, subject model.ID) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		_, err := sc.Evals().Create(context.Background(), model.EvalResult{
			Suite: "s", SubjectKind: "agent", SubjectID: subject, Score: 1, Passed: true, OccurredAt: model.NewTimestamp(time.Now()),
		})
		return err
	})
}

// seedCrossOriginAccess seeds the heterogeneous-origin scenario is about: an
// identity with two agents running as it, an OBSERVED agent edge and a PERMITTED
// identity edge on the SAME resource+mode. The raw store Drift reports this single,
// fully-permitted access as TWO false drifts (a false unexpected access on the agent
// key + a false unused grant on the identity key); module III's reconciliation cancels
// both. It is the exact false data the compliance evidence must NOT report.
func (h *harness) seedCrossOriginAccess(tenant model.TenantID) {
	h.t.Helper()
	now := model.NewTimestamp(time.Now())
	res := model.NewID()
	h.mutate(tenant, func(sc store.Scope) error {
		i, err := sc.Identities().Create(context.Background(), model.Identity{Name: "xo-I", Kind: "db_role", ExternalID: "vault-xo"})
		if err != nil {
			return err
		}
		var agentA model.ID
		for idx, ext := range []string{"xo-agent-a", "xo-agent-b"} {
			a, err := sc.Agents().Create(context.Background(), model.Agent{Name: ext, ExternalID: ext, IdentityID: i.ID, Status: model.StatusActive})
			if err != nil {
				return err
			}
			if idx == 0 {
				agentA = a.ID
			}
		}
		if _, err := sc.AccessEdges().Upsert(context.Background(), model.AccessEdge{
			OriginKind: "agent", OriginID: agentA, ResourceID: res, Mode: sdkmodel.ModeRead,
			SignalSource: sdkmodel.SignalPGAudit, Confidence: sdkmodel.ConfidenceAttributed,
			Permitted: false, Observed: true, FirstSeen: now, LastSeen: now, OccurrenceCount: 1,
		}); err != nil {
			return err
		}
		_, err = sc.AccessEdges().Upsert(context.Background(), model.AccessEdge{
			OriginKind: "identity", OriginID: i.ID, ResourceID: res, Mode: sdkmodel.ModeRead,
			SignalSource: sdkmodel.SignalPolicy, Confidence: sdkmodel.ConfidenceAttributed,
			Permitted: true, Observed: false, FirstSeen: now, LastSeen: now, OccurrenceCount: 1,
		})
		return err
	})
}

func (h *harness) seedDeployment(tenant model.TenantID) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		_, err := sc.Deployments().Create(context.Background(), model.Deployment{
			SubjectKind: "agent", SubjectID: model.NewID(), Target: "host", Environment: "prod", Status: "applied",
			Version: "1", DeployedAt: model.NewTimestamp(time.Now()),
		})
		return err
	})
}

func (h *harness) seedIdentityPolicy(tenant model.TenantID) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		if _, err := sc.Identities().Create(context.Background(), model.Identity{Name: "svc", Kind: "iam_principal", Provider: "aws"}); err != nil {
			return err
		}
		_, err := sc.Policies().Create(context.Background(), model.Policy{Name: "p", Kind: "rbac", Enabled: true})
		return err
	})
}

func (h *harness) seedLineageEgress(tenant model.TenantID, n int) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(lineageStandInKind)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			if _, err := repo.Create(context.Background(), model.Record{"egress": true, "session_ref": "s"}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (h *harness) seedCostSample(tenant model.TenantID, n int) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(costSampleStandInKind)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			if _, err := repo.Create(context.Background(), model.Record{"input_tokens": int64(100), "cost_micro_usd": int64(250)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// seedCostSampleGeo seeds one cost-sample row carrying an inference_geo, so the
// residency pin coherence check can observe a (possibly out-of-region) inference geo.
func (h *harness) seedCostSampleGeo(tenant model.TenantID, geo string) {
	h.t.Helper()
	h.seedCostSampleGeoWS(tenant, "", geo)
}

// seedCostSampleGeoWS is the workspace-attributed variant: the OBSERVED side of the
// Workspace-geo drift scan. An empty workspaceRef seeds an unattributed sample
// (the default workspace), which the workspace branch must skip.
func (h *harness) seedCostSampleGeoWS(tenant model.TenantID, workspaceRef, geo string) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(costSampleStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"input_tokens": int64(100), "cost_micro_usd": int64(250),
			"inference_geo": geo, "workspace_ref": workspaceRef,
		})
		return err
	})
}

// seedWorkspaceResidency seeds one models.workspace_residency stand-in row — the
// PERMITTED side of the workspace-geo drift scan. An empty allowedCSV means
// unrestricted/unreported (never a violation).
func (h *harness) seedWorkspaceResidency(tenant model.TenantID, workspaceRef, allowedCSV string) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceResidencyStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"workspace_ref": workspaceRef, "allowed_geos": allowedCSV,
			"default_geo": "us", "workspace_geo": "us", "as_of": "2026-06-10",
		})
		return err
	})
}

// pinOrg sets a tenant's data-residency pin directly via the System path,
// bypassing the API's registry validation so a test can pin without a configured
// region registry on the harness server.
func (h *harness) pinOrg(tenant model.TenantID, region string) {
	h.t.Helper()
	if err := h.st.System(context.Background(), func(sys store.SystemScope) error {
		_, e := sys.SetOrgRegion(context.Background(), tenant, region)
		return e
	}); err != nil {
		h.t.Fatalf("pin org %s to %q: %v", tenant, region, err)
	}
}

func (h *harness) seedGPAIPosture(tenant model.TenantID, providerRef string, verified bool) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(gpaiStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{"provider_ref": providerRef, "verified": verified})
		return err
	})
}

func (h *harness) seedModelAdmission(tenant model.TenantID, versionRef string, verified bool) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(modelAdmissionStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{"version_ref": versionRef, "signature_verified": verified})
		return err
	})
}

func (h *harness) seedAIBOMSeal(tenant model.TenantID, ownedRef string) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(aibomStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{"owned_ref": ownedRef, "content_hash": "deadbeef"})
		return err
	})
}

// seedPIIScan seeds one append-only knowledge.pii_scan stand-in row — the
// evidence that a discovery scan actually ran (counts only, never content).
func (h *harness) seedPIIScan(tenant model.TenantID, docsWithHits int) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(piiScanStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"scope_kind": "kb", "docs_with_hits": int64(docsWithHits), "detector_version": "s27-pii.v1",
		})
		return err
	})
}

// seedDLPRule seeds one knowledge.dlp_rule stand-in row (the DLP policy is
// per sensitivity class; class ids only, never content).
func (h *harness) seedDLPRule(tenant model.TenantID, class, action string) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(dlpRuleStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{"class": class, "action": action})
		return err
	})
}

// seedDLPEvent seeds one append-only knowledge.dlp_event stand-in row — proof the
// Gate fired (action + counts only, never content).
func (h *harness) seedDLPEvent(tenant model.TenantID, action string, withheld int) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(dlpEventStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{"dlp_action": action, "chunks_withheld": int64(withheld)})
		return err
	})
}

// seedExtRows inserts n copies of row into a stand-in ext entity (the store stamps
// created_at/updated_at with ITS clock — the tests age rows by advancing the
// MODULE clock instead).
func (h *harness) seedExtRows(tenant model.TenantID, kind model.Kind, n int, row model.Record) {
	h.t.Helper()
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			r := model.Record{}
			for k, v := range row {
				r[k] = v
			}
			if _, err := repo.Create(context.Background(), r); err != nil {
				return err
			}
		}
		return nil
	})
}

// countExtRows counts the rows of a stand-in ext entity (post-sweep asserts).
func (h *harness) countExtRows(tenant model.TenantID, kind model.Kind) int {
	h.t.Helper()
	n := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 1000})
		n = len(recs)
		return err
	}); err != nil {
		h.t.Fatalf("count %s: %v", kind, err)
	}
	return n
}

func (h *harness) mutate(tenant model.TenantID, fn func(store.Scope) error) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, fn); err != nil {
		h.t.Fatalf("seed mutate: %v", err)
	}
}

// auditActions returns the audit actions recorded for a tenant (for self-audit asserts).
func (h *harness) auditActions(tenant model.TenantID) []string {
	h.t.Helper()
	var actions []string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(e model.AuditEvent) error {
			actions = append(actions, e.Action)
			return nil
		})
	}); err != nil {
		h.t.Fatalf("walk audit: %v", err)
	}
	return actions
}

// auditMetaFor returns the Meta of the FIRST audit event with the given action ({} if found
// with no meta, nil if the action is absent). Walk drops Meta by design (the chain commits to
// MetaDigest, store/audit.go), so this reads the STORED canonical meta string via the
// CanonicalWalker capability and parses it back.
func (h *harness) auditMetaFor(tenant model.TenantID, action string) map[string]any {
	h.t.Helper()
	var meta map[string]any
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		cw, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			h.t.Fatal("audit log does not implement CanonicalWalker")
		}
		return cw.WalkCanonical(context.Background(), 0, func(e model.AuditEvent, metaCanonical string, _ []byte) error {
			if meta == nil && e.Action == action {
				meta = map[string]any{}
				_ = json.Unmarshal([]byte(metaCanonical), &meta)
			}
			return nil
		})
	}); err != nil {
		h.t.Fatalf("walk audit: %v", err)
	}
	return meta
}

func (h *harness) deliveredFindings() []sdkmodel.FindingReport {
	h.findMu.Lock()
	defer h.findMu.Unlock()
	out := make([]sdkmodel.FindingReport, len(h.findings))
	copy(out, h.findings)
	return out
}

func (h *harness) waitFindings() { time.Sleep(20 * time.Millisecond) }

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
