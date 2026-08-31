// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// fakeJudge is a deterministic test judge: it PASSES when the output contains the
// criterion as a substring, FAILS otherwise — so an llm_judge run is reproducible
// without a real model. It exercises the same Score→verdict mapping as production.
type fakeJudge struct{}

func (fakeJudge) Judge(_ context.Context, _ model.TenantID, req JudgeRequest) (JudgeVerdict, error) {
	if req.Criterion != "" && strings.Contains(req.Output, req.Criterion) {
		return JudgeVerdict{Score: 1.0, Passed: true, Reason: "output satisfied the criterion"}, nil
	}
	return JudgeVerdict{Score: 0.0, Passed: false, Reason: "output did not satisfy the criterion"}, nil
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	setupTok string

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

// newHarness builds an evals plane. A nil judge uses the module default (offline →
// llm_judge skipped); a non-nil judge is wired via WithJudge. Extra options (e.g.
// WithSessionSource) are applied after it.
func newHarness(t *testing.T, judge Judge, extra ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	var opts []Option
	if judge != nil {
		opts = append(opts, WithJudge(judge))
	}
	opts = append(opts, extra...)
	mod := New(opts...)

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, mod.RegisterSchema)
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

// createSuite creates a suite and returns its id.
func (h *harness) createSuite(admin string, tenant model.TenantID, body map[string]any) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/evals/suites", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create suite = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// addCase appends a case to a suite.
func (h *harness) addCase(admin string, tenant model.TenantID, suiteID string, body map[string]any) {
	h.t.Helper()
	r := h.do("POST", "/v1/m/evals/suites/"+suiteID+"/cases", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("add case = %d %s", r.code, r.raw)
	}
}

// coreEvalResults reads the persisted core EvalResults of a suite directly from the
// store (the canonical cross-module artifact).
func (h *harness) coreEvalResults(tenant model.TenantID, suite string) []model.EvalResult {
	h.t.Helper()
	var out []model.EvalResult
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		es, _, err := sc.Evals().List(context.Background(), model.Query{
			Filters: []model.Filter{{Column: "suite", Op: model.OpEq, Value: suite}}, Limit: 1000,
		})
		out = es
		return err
	}); err != nil {
		h.t.Fatalf("read eval results: %v", err)
	}
	return out
}

// coreFindings reads the persisted core findings of a kind directly from the store.
func (h *harness) coreFindings(tenant model.TenantID, kind string) []model.Finding {
	h.t.Helper()
	var out []model.Finding
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		fs, _, err := sc.Findings().List(context.Background(), model.Query{
			Filters: []model.Filter{{Column: "kind", Op: model.OpEq, Value: kind}}, Limit: 1000,
		})
		out = fs
		return err
	}); err != nil {
		h.t.Fatalf("read findings: %v", err)
	}
	return out
}

// seedSession inserts a core Session (and optional finding + cost) directly, for the
// monitor test.
func (h *harness) seedSession(tenant model.TenantID, state model.SessionState, sev model.Severity) model.ID {
	h.t.Helper()
	var sessID model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		s, err := sc.Sessions().Create(context.Background(), model.Session{
			State: state, StartedAt: model.NewTimestamp(time.Now()),
		})
		if err != nil {
			return err
		}
		sessID = s.ID
		if sev != "" {
			if _, err := sc.Findings().Create(context.Background(), model.Finding{
				Kind: "guardrail", Severity: sev, Status: model.FindingOpen, Source: "test",
				SubjectKind: "session", SubjectID: s.ID, Title: "seed", OccurredAt: model.NewTimestamp(time.Now()),
			}); err != nil {
				return err
			}
		}
		_, err = sc.Costs().Create(context.Background(), model.CostRecord{
			SessionID: s.ID, OccurredAt: model.NewTimestamp(time.Now()),
			InputTokens: 100, OutputTokens: 50, CostMicroUSD: 1234,
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed session: %v", err)
	}
	return sessID
}

func (h *harness) deliveredFindings() []sdkmodel.FindingReport {
	h.findMu.Lock()
	defer h.findMu.Unlock()
	out := make([]sdkmodel.FindingReport, len(h.findings))
	copy(out, h.findings)
	return out
}

func (h *harness) waitFindings() {
	time.Sleep(20 * time.Millisecond)
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func floatOf(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}
