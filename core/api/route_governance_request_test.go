// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"github.com/olivaresai/olivares/core/store"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

const witnessPerm auth.Permission = "witnessroute:thing:read"

// witnessCapturingModule mounts one governed route whose handler INTERROGATES the witness it was
// handed, because "the field is not zero" is not the property worth holding.
//
// ⛔ THE PROPERTY IS THAT THE WITNESS ANSWERS *THIS* REQUEST'S QUESTION. A witness carries a
// QuestionDigest binding principal, permission, action, tenant and resource, precisely so an
// authentic witness minted for another question cannot be presented for this one. Asserting
// non-zero would pass for a witness transplanted from a neighbouring row, which is the transplant
// QuestionDigest exists to refuse. So the handler rebuilds the question from what it was given —
// mc.Resource is documented as "the exact resource against which the route wrapper authorized" —
// and asks VerifyFor.
type witnessCapturingModule struct {
	requestContext          context.Context
	handlerUnboundedAndLive bool
	reached                 atomic.Int64
	verified                atomic.Int64
}

func (*witnessCapturingModule) APINamespace() string { return "witnessroute" }

func (*witnessCapturingModule) Permissions() []auth.Permission {
	return []auth.Permission{witnessPerm}
}

func (m *witnessCapturingModule) APIRoutes(reg api.RouteRegistrar) {
	pr, ok := reg.(interface {
		HandlePolicy(method, pattern string, perm auth.Permission, meta api.RouteMetadata, h api.ModuleHandler)
	})
	if !ok {
		panic("witnessroute: the registrar does not carry the governed door")
	}
	pr.HandlePolicy("GET", "/thing", witnessPerm, api.RouteMetadata{}, m.handle)
}

func (m *witnessCapturingModule) handle(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.requestContext = r.Context()
	_, finite := r.Context().Deadline()
	m.handlerUnboundedAndLive = !finite && r.Context().Err() == nil
	m.reached.Add(1)
	if mc.Authorization.VerifyFor(time.Now(), auth.Request{
		Principal:  mc.Principal,
		Permission: witnessPerm,
		Tenant:     mc.Tenant,
		Resource:   mc.Resource,
	}) {
		m.verified.Add(1)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// TestAGovernedHandlerReceivesAWitnessThatAnswersItsOwnRequest is the witness for invariant V —
// "the HTTP effect only happens after a witness" — and until this case existed nothing measured it.
//
// ⛔ NOTHING HAD EVER REACHED A GOVERNED HANDLER SUCCESSFULLY. Every other case in this package
// measures a REFUSAL: 503 when the decision cannot be established, the route's own denial, the
// conceal-404. The line that installs the witness on the ModuleContext was therefore never
// executed by any test, and deleting it would have left the suite green.
//
// ⛔ AND IT USES THE REAL PRODUCER, NOT A DOUBLE. o.Authenticator is the *auth.Authenticator the
// harness already builds over the real store, and it is the same type production wires as the
// producer. A fake that returned a hand-made principal would prove the plumbing while saying
// nothing about whether authority can actually be reconstructed at request time, which is the
// half a signature match cannot answer.
func TestAGovernedHandlerReceivesAWitnessThatAnswersItsOwnRequest(t *testing.T) {
	m := &witnessCapturingModule{}
	policy := &decisionHorizonPolicy{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PrincipalEvidenceProducer = o.Authenticator
		policy.store = o.Store
		o.Authorizer = auth.NewAuthorizer(policy)
	}, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "witnessroute")
	bound := witnessTenantPrincipal(t, h, admin, tenant)

	got := h.do("GET", "/v1/m/witnessroute/thing", bound,
		nil, map[string]string{"X-Olivares-Tenant": string(tenant)})

	if got.code != http.StatusOK {
		t.Fatalf("the governed route did not serve: got %d %s\n"+
			"With the real authenticator wired as the evidence producer this request should be "+
			"decided, not deferred. A 503 here means authority could not be reconstructed for "+
			"this principal and tenant, which is the request-time half of K3 and is exactly what "+
			"a signature match cannot tell you.", got.code, got.raw)
	}
	if policy.ctx == nil {
		t.Fatal("typed policy producer was not called")
	}
	if deadline, ok := policy.ctx.Deadline(); !ok || deadline.IsZero() || policy.ctx.Err() != context.Canceled {
		t.Fatal("decision lost finite horizon or was not canceled after evaluation")
	}
	if !m.handlerUnboundedAndLive {
		t.Fatal("governed handler acquired an implicit deadline or canceled context")
	}
	if m.reached.Load() != 1 {
		t.Fatalf("the handler ran %d time(s), want exactly 1", m.reached.Load())
	}
	if m.verified.Load() != 1 {
		t.Fatalf("the handler ran but its witness did NOT verify for its own request: "+
			"reached=%d verified=%d. The field arrived and the question it answers is not this "+
			"one, which is worse than an empty field: it reads like an authorization.",
			m.reached.Load(), m.verified.Load())
	}
	for _, lifetime := range []time.Duration{time.Second, time.Minute} {
		deadline := time.Now().Add(lifetime)
		requestCtx, cancel := context.WithDeadline(context.Background(), deadline)
		request := httptest.NewRequest("GET", "/v1/m/witnessroute/thing", nil).WithContext(requestCtx)
		request.RemoteAddr = "10.0.0.1:1234"
		request.Header.Set("Authorization", "Bearer "+bound)
		request.Header.Set("X-Olivares-Tenant", string(tenant))
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, request)
		if rec.Code != 200 {
			t.Fatalf("explicit deadline request: %d %s", rec.Code, rec.Body.String())
		}
		got, _ := m.requestContext.Deadline()
		if !got.Equal(deadline) || m.requestContext.Err() != nil {
			t.Fatal("decision budget changed or canceled caller-owned handler context")
		}
		decisionDeadline, _ := policy.ctx.Deadline()
		if decisionDeadline.After(deadline) || time.Until(decisionDeadline) > 5*time.Second || policy.ctx.Err() != context.Canceled {
			t.Fatal("decision did not honor the earlier horizon or release its context")
		}
		cancel()
	}

}

// TestAGovernedRouteWithoutReconstructableAuthorityRefusesBeforeTheHandler is the third state, and
// it is the control that makes the case above mean something.
//
// Without it, a server that answered 200 to everything — never consulting evidence at all — would
// satisfy the first case completely. Here the producer cannot resolve the principal's scope, so
// the answer is "I could not establish the decision" and NOT "you may not": 503, and the handler
// is never entered.
func TestAGovernedRouteWithoutReconstructableAuthorityRefusesBeforeTheHandler(t *testing.T) {
	m := &witnessCapturingModule{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PrincipalEvidenceProducer = presentEvidenceProducer{}
	}, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "witnessroute")
	bound := witnessTenantPrincipal(t, h, admin, tenant)

	got := h.do("GET", "/v1/m/witnessroute/thing", bound,
		nil, map[string]string{"X-Olivares-Tenant": string(tenant)})

	if got.code != http.StatusServiceUnavailable {
		t.Fatalf("undecided answered %d, want 503: %s", got.code, got.raw)
	}
	if m.reached.Load() != 0 {
		t.Fatalf("the handler ran %d time(s) on a request whose decision could not be "+
			"established: the effect happened without a witness, which is invariant V broken.",
			m.reached.Load())
	}
}

// witnessTenantPrincipal returns a token for a REAL tenant member, and the reason it exists is a
// defect this file had on its first run.
//
// ⛔ EL SUPERADMIN NO SIRVE, Y SU 503 PARECÍA DEL MECANISMO. La primera versión autenticaba con
// h.adminLogin() y recibía 503; el log del productor dijo por qué: «global superadmin session
// cannot be scoped». La evidencia del principal es TENANT-SCOPED por construcción, así que una
// sesión global no puede tenerla y el productor se niega — correctamente. Un caso montado sobre
// ese principal mide esa negativa para siempre y NUNCA el camino de éxito, mientras su fallo se
// lee como «el mecanismo no funciona». El fixture era el defecto, no el sujeto.
func witnessTenantPrincipal(t *testing.T, h *harness, admin string, tenant model.TenantID) string {
	t.Helper()
	created := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "witness@witnessroute.test", "password": "witnessroute1",
	}, nil)
	if created.code != http.StatusCreated {
		t.Fatalf("create tenant member = %d %s", created.code, created.raw)
	}
	member := h.do("POST", "/v1/memberships", admin, map[string]any{
		"user_id": created.body["id"], "tenant": tenant.String(), "role": auth.RoleViewer,
	}, nil)
	if member.code != http.StatusCreated {
		t.Fatalf("create membership = %d %s", member.code, member.raw)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "witness@witnessroute.test", "password": "witnessroute1",
	}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("tenant member login = %d %s", login.code, login.raw)
	}
	return login.body["token"].(string)
}

// Reads its provenance from the same actual SQLite store as authentication.
// It requires a finite horizon, exposing the production governance prerequisite.
type decisionHorizonPolicy struct {
	store store.Store
	ctx   context.Context
}

func (p *decisionHorizonPolicy) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: true}, nil
}
func (p *decisionHorizonPolicy) EvaluateEvidence(ctx context.Context, req auth.Request) (auth.PolicyEvidenceDecision, error) {
	p.ctx = ctx
	deadline, ok := ctx.Deadline()
	if !ok {
		return auth.PolicyEvidenceDecision{}, nil
	}
	var fact store.AuthorizationFactRef
	var observedAt model.Timestamp
	err := p.store.View(ctx, req.Tenant, func(sc store.Scope) error {
		var err error
		fact, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
		if err == nil {
			observedAt, err = sc.(store.TransactionClock).TransactionNow(ctx)
		}
		return err
	})
	if err != nil {
		return auth.PolicyEvidenceDecision{}, err
	}
	return auth.PolicyEvidenceDecision{ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "fixture_epoch_read"}, Facts: []store.AuthorizationFactRef{fact}, ObservedAt: observedAt.Time(), FreshUntil: deadline}, nil
}
