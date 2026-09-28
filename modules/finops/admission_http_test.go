// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// THE ADMISSION ROUTES. A request reaches the handler the module mounts for its method
// and pattern, under the tenant the route wrapper resolved, and every assertion reads
// the status and the body a client receives. The fixture is the settlement one: budget B
// and seat limit S of actor "a".
// -----------------------------------------------------------------------------

// admissionRoute is one route the module mounts: the permission it requires and its
// handler.
type admissionRoute struct {
	perm    auth.Permission
	handler api.ModuleHandler
}

// admissionRouteTable records the routes APIRoutes mounts, by method and pattern.
type admissionRouteTable map[string]admissionRoute

func (rt admissionRouteTable) Handle(method, pattern string, perm auth.Permission, handler api.ModuleHandler) {
	rt[method+" "+pattern] = admissionRoute{perm: perm, handler: handler}
}

func (rt admissionRouteTable) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, handler api.ModuleHandler) {
	rt[method+" "+pattern] = admissionRoute{perm: perm, handler: handler}
}

// admissionAnswer is what a client receives.
type admissionAnswer struct {
	code int
	body map[string]any
	raw  string
}

// message is the error message of an error body.
func (a admissionAnswer) message() string {
	e, _ := a.body["error"].(map[string]any)
	msg, _ := e["message"].(string)
	return msg
}

// serveAdmission sends one request with body as its JSON document, or with no body when
// body is nil, to the handler m mounts for method and pattern, under tenant.
func serveAdmission(t *testing.T, m *Module, tenant model.TenantID, method, pattern string, body any) admissionAnswer {
	t.Helper()
	return serveAdmissionIn(context.Background(), t, m, tenant, method, pattern, body)
}

// serveAdmissionIn is serveAdmission with the request carrying ctx.
func serveAdmissionIn(ctx context.Context, t *testing.T, m *Module, tenant model.TenantID, method, pattern string, body any) admissionAnswer {
	t.Helper()
	routes := admissionRouteTable{}
	m.APIRoutes(routes)
	route, ok := routes[method+" "+pattern]
	if !ok {
		t.Fatalf("the module mounts no %s %s", method, pattern)
	}
	var payload io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode the request body: %v", err)
		}
		payload = bytes.NewReader(raw)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/v1/m/finops"+pattern, payload).WithContext(ctx)
	route.handler(rec, req, api.ModuleContext{Tenant: tenant})
	out := admissionAnswer{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// reserveBody is the reserve document of req.
func reserveBody(req AdmissionRequest) map[string]any {
	body := map[string]any{
		"scope": req.Scope, "estimate_micro_usd": req.EstimateMicroUSD, "idempotency_key": req.IdempotencyKey,
	}
	if req.ActorRef != "" {
		body["actor_ref"] = req.ActorRef
	}
	return body
}

// TestAdmissionHTTPVerticalSlice: the five admission routes are mounted, each with the
// budget permission it needs, and one hold travels through them: reserved, handed again
// to a retry, a cap refused with 402, committed, left committed by a later release, and
// read back by the reconciliation and the job.
func TestAdmissionHTTPVerticalSlice(t *testing.T) {
	forEachAdmissionEngine(t, runAdmissionHTTPVerticalSlice)
}

func runAdmissionHTTPVerticalSlice(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	routes := admissionRouteTable{}
	m.APIRoutes(routes)
	for key, want := range map[string]auth.Permission{
		"POST /admission/reserve":       permBudgetWrite,
		"POST /admission/commit":        permBudgetWrite,
		"POST /admission/release":       permBudgetWrite,
		"GET /admission/reconciliation": permBudgetRead,
		"POST /admission/reconcile":     permBudgetWrite,
	} {
		if got, ok := routes[key]; !ok || got.perm != want {
			t.Errorf("%s is mounted=%v with %q, want %q", key, ok, got.perm, want)
		}
	}

	req := seatRequest("model_gateway/http-1")
	reserve := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", reserveBody(req))
	h, _ := reserve.body["handle"].(string)
	if reserve.code != http.StatusOK || reserve.body["allowed"] != true || h == "" {
		t.Fatalf("reserve = %d %s, want 200, admitted under a handle", reserve.code, reserve.raw)
	}
	retry := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", reserveBody(req))
	if retry.code != http.StatusOK || retry.body["replayed"] != true || retry.body["handle"] != h {
		t.Fatalf("retry = %d %s, want 200 replaying %s", retry.code, retry.raw, h)
	}

	over := AdmissionRequest{Scope: AdmissionScopeModelGateway, EstimateMicroUSD: 19 * oneUSD, IdempotencyKey: "model_gateway/http-2"}
	deny := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", reserveBody(over))
	if deny.code != http.StatusPaymentRequired || deny.body["allowed"] != false || deny.body["action"] != "block" {
		t.Fatalf("a request over the cap = %d %s, want 402 block", deny.code, deny.raw)
	}

	clk.advance(10 * time.Second)
	commit := serveAdmission(t, m, tenant, http.MethodPost, "/admission/commit", map[string]any{"handle": h, "actual_micro_usd": measured})
	if commit.code != http.StatusOK || commit.body["committed"] != true || commit.body["handle"] != h {
		t.Fatalf("commit = %d %s, want 200 committed", commit.code, commit.raw)
	}
	release := serveAdmission(t, m, tenant, http.MethodPost, "/admission/release", map[string]any{"handle": h})
	if release.code != http.StatusOK || release.body["released"] != true {
		t.Fatalf("a release after the commit = %d %s, want 200 and nothing written", release.code, release.raw)
	}
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, holdID(h))
	assertRowsUnder(t, st, tenant, holdID(h), 2, resvStateCommitted, measured, clk.t)

	recon := serveAdmission(t, m, tenant, http.MethodGet, "/admission/reconciliation", nil)
	if recon.code != http.StatusOK || recon.body["committed"] != float64(2) || recon.body["active"] != float64(0) {
		t.Fatalf("reconciliation = %d %s, want 200 with two committed rows and none active", recon.code, recon.raw)
	}
	for _, counter := range []string{"owed_remaining", "legacy_pending", "legacy_owes_release", "unresolved", "undecodable", "frontier_blocked", "corrupt"} {
		if recon.body[counter] != float64(0) {
			t.Errorf("reconciliation %s = %v, want 0", counter, recon.body[counter])
		}
	}
	job := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reconcile", nil)
	if job.code != http.StatusOK || job.body["drift"] != false {
		t.Fatalf("reconcile = %d %s, want 200 with no drift", job.code, job.raw)
	}
}

// TestAdmissionReserveBodyIsTheNamesTheSchemaPublishes: the reserve document is decoded
// with unknown fields refused, so the names the schema publishes are the contract. The
// attribution travels under its snake_case names, and a multi-word Go field name is
// refused; encoding/json matches names without regard to case, so a one-word field such
// as Team is accepted under either spelling.
func TestAdmissionReserveBodyIsTheNamesTheSchemaPublishes(t *testing.T) {
	forEachAdmissionEngine(t, runAdmissionReserveBodyIsTheNamesTheSchemaPublishes)
}

func runAdmissionReserveBodyIsTheNamesTheSchemaPublishes(t *testing.T, cfg store.Config) {
	m, _, tenant, _ := settlementFixture(t, cfg)
	published := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", map[string]any{
		"scope": "model_gateway", "idempotency_key": "model_gateway/names-1", "estimate_micro_usd": int64(1_000),
		"dims": map[string]any{
			"provider_ref": "anthropic", "model_ref": "m-1", "workspace_ref": "w-1", "user_group_refs": []string{"g-1"},
		},
	})
	if published.code != http.StatusOK || published.body["allowed"] != true {
		t.Fatalf("the published names = %d %s, want 200 admitted", published.code, published.raw)
	}
	goNames := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", map[string]any{
		"scope": "model_gateway", "idempotency_key": "model_gateway/names-2",
		"dims": map[string]any{"ProviderRef": "anthropic"},
	})
	if goNames.code != http.StatusBadRequest {
		t.Fatalf("a multi-word Go field name = %d %s, want 400", goNames.code, goNames.raw)
	}
}

// TestHTTPMapsSettlementConflictTo409: a commit of a committed hold with another amount
// is answered 409 with the conflict's own message, and writes nothing; the same amount
// again is answered 200.
func TestHTTPMapsSettlementConflictTo409(t *testing.T) {
	forEachAdmissionEngine(t, runHTTPMapsSettlementConflictTo409)
}

func runHTTPMapsSettlementConflictTo409(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	h := reserveOK(t, m, tenant, seatRequest("model_gateway/k")).Handle
	if first := serveAdmission(t, m, tenant, http.MethodPost, "/admission/commit", map[string]any{"handle": h, "actual_micro_usd": measured}); first.code != http.StatusOK {
		t.Fatalf("commit = %d %s, want 200", first.code, first.raw)
	}
	committed := clk.t
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
	clk.advance(time.Minute)

	other := serveAdmission(t, m, tenant, http.MethodPost, "/admission/commit", map[string]any{"handle": h, "actual_micro_usd": measured + 1})
	if other.code != http.StatusConflict || other.message() != ErrSettlementConflict.Error() {
		t.Fatalf("a commit with another amount = %d %s, want 409 %q", other.code, other.raw, ErrSettlementConflict)
	}
	again := serveAdmission(t, m, tenant, http.MethodPost, "/admission/commit", map[string]any{"handle": h, "actual_micro_usd": measured})
	if again.code != http.StatusOK {
		t.Fatalf("the same commit again = %d %s, want 200", again.code, again.raw)
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	assertRowsUnder(t, st, tenant, holdID(h), 2, resvStateCommitted, measured, committed)
}

// TestHTTPMapsPendingIntentTo409: a commit or a release of a hold whose admission is
// still a claim — before its create, or after it and before the publication — is
// answered 409 with the pending refusal's own message, and writes nothing.
func TestHTTPMapsPendingIntentTo409(t *testing.T) {
	forEachAdmissionEngine(t, runHTTPMapsPendingIntentTo409)
}

func runHTTPMapsPendingIntentTo409(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := settlementFixture(t, cfg)
	claimed := stagePendingClaim(t, m, tenant, seatRequest("model_gateway/claimed"))
	created := stageClaimWithHold(t, m, tenant, seatRequest("model_gateway/created"))
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)

	for _, h := range []holdID{claimed, created} {
		for _, call := range []struct {
			pattern string
			body    map[string]any
		}{
			{"/admission/commit", map[string]any{"handle": h.String(), "actual_micro_usd": measured}},
			{"/admission/release", map[string]any{"handle": h.String()}},
		} {
			got := serveAdmission(t, m, tenant, http.MethodPost, call.pattern, call.body)
			if got.code != http.StatusConflict || got.message() != ErrAdmissionPending.Error() {
				t.Errorf("%s of %s = %d %s, want 409 %q", call.pattern, h, got.code, got.raw, ErrAdmissionPending)
			}
		}
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
}

// TestHTTPMapsEveryOtherAdmissionRefusal: each remaining refusal has its own status, and
// none of them is the 409 of a settlement conflict. Input that cannot be acted on is 400.
// An admission row that fails its integrity check is 500 with the fixed sentence of the
// integrity failure and no stored value, for the reserve and for a settlement alike: it
// is a fault of the stored data that a retry after repair clears, not a conflict. A hold
// that belongs to the attempt lifecycle is 409 with the typed refusal. A store that
// cannot be read refuses with 503, unless the request opted into admission without a
// hold, which is then 200.
func TestHTTPMapsEveryOtherAdmissionRefusal(t *testing.T) {
	forEachAdmissionEngine(t, runHTTPMapsEveryOtherAdmissionRefusal)
}

func runHTTPMapsEveryOtherAdmissionRefusal(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := settlementFixture(t, cfg)
	h := reserveOK(t, m, tenant, seatRequest("model_gateway/k")).Handle

	for _, call := range []struct {
		name    string
		pattern string
		body    map[string]any
	}{
		{"a malformed handle", "/admission/commit", map[string]any{"handle": "x1", "actual_micro_usd": measured}},
		{"a negative amount", "/admission/commit", map[string]any{"handle": h, "actual_micro_usd": -1}},
		{"a malformed handle released", "/admission/release", map[string]any{"handle": "x1"}},
		{"an unknown scope", "/admission/reserve", map[string]any{"scope": "elsewhere", "idempotency_key": "k"}},
		{"an unpublished commit field", "/admission/commit", map[string]any{"handle": h, "actual_micro_usd": measured, "hold": h}},
		{"an unpublished release field", "/admission/release", map[string]any{"handle": h, "note": "x"}},
	} {
		if got := serveAdmission(t, m, tenant, http.MethodPost, call.pattern, call.body); got.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", call.name, got.code, got.raw)
		}
	}

	// Two rows name one hold: neither can settle it, and the key of a row whose seat slot
	// is no identity is refused in every posture.
	shared := newHoldID()
	seedAdmission(t, st, tenant, admissionRecordFor(seatRequest("model_gateway/k1"), admStatePending, shared, "", baseTime))
	seedAdmission(t, st, tenant, admissionRecordFor(seatRequest("model_gateway/k2"), admStateReserved, shared, "", baseTime))
	corrupt := seatRequest("model_gateway/k3")
	rec := admissionRecordFor(corrupt, admStateReserved, newHoldID(), "", baseTime)
	rec[colAdmSpendHandle] = "x1"
	seedAdmission(t, st, tenant, rec)
	for _, call := range []struct {
		pattern string
		body    map[string]any
	}{
		{"/admission/commit", map[string]any{"handle": shared.String(), "actual_micro_usd": measured}},
		{"/admission/release", map[string]any{"handle": shared.String()}},
	} {
		got := serveAdmission(t, m, tenant, http.MethodPost, call.pattern, call.body)
		if got.code != http.StatusInternalServerError || got.message() != ErrAdmissionIntegrity.Error() {
			t.Errorf("%s of a hold two rows name = %d %s, want 500 %q", call.pattern, got.code, got.raw, ErrAdmissionIntegrity)
		}
	}
	for _, posture := range []string{"deny", "allow"} {
		body := reserveBody(corrupt)
		body["unreachable"] = posture
		got := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", body)
		if got.code != http.StatusInternalServerError || got.body["allowed"] != false || got.body["reason"] != ReasonAdmissionIntegrity {
			t.Errorf("a reserve of a corrupt key under %s = %d %s, want 500 refused for integrity", posture, got.code, got.raw)
		}
	}

	linked := reserveOK(t, m, tenant, seatRequest("model_gateway/linked")).Handle
	linkToAttempt(t, st, tenant, holdID(linked))
	lifecycle := attemptErr(errCodeLifecycleAPIRequired, nil).Error()
	for _, call := range []struct {
		pattern string
		body    map[string]any
	}{
		{"/admission/commit", map[string]any{"handle": linked, "actual_micro_usd": measured}},
		{"/admission/release", map[string]any{"handle": linked}},
	} {
		got := serveAdmission(t, m, tenant, http.MethodPost, call.pattern, call.body)
		if got.code != http.StatusConflict || got.message() != lifecycle {
			t.Errorf("%s of a hold the lifecycle owns = %d %s, want 409 %q", call.pattern, got.code, got.raw, lifecycle)
		}
	}

	unwired := New()
	unwired.clock = m.clock
	for _, want := range []struct {
		posture string
		code    int
		allowed bool
	}{
		{"deny", http.StatusServiceUnavailable, false},
		{"allow", http.StatusOK, true},
	} {
		posture := want.posture
		body := reserveBody(seatRequest("model_gateway/unread"))
		body["unreachable"] = posture
		got := serveAdmission(t, unwired, tenant, http.MethodPost, "/admission/reserve", body)
		if got.code != want.code || got.body["allowed"] != want.allowed || got.body["handle"] != nil {
			t.Errorf("a reserve on an unreadable store under %s = %d %s, want %d allowed=%v with no handle", posture, got.code, got.raw, want.code, want.allowed)
		}
	}

	// Under an activation frontier the attempt lifecycle owns the ledger. A reserve is
	// refused as a cap refuses, 402 block with the frontier's reason; a settlement of a
	// hold taken before the frontier is the lifecycle's typed 409.
	fm, _, ftenant, _ := settlementFixture(t, cfg)
	WithAttemptEvidenceVerifier(newLabVerifier(ftenant))(fm)
	before := reserveOK(t, fm, ftenant, seatRequest("model_gateway/before-frontier")).Handle
	if _, err := fm.BeginLifecycleActivation(context.Background(), ftenant, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("quiescence"), labEvidence("caller-readiness")},
	}); err != nil {
		t.Fatalf("begin the frontier: %v", err)
	}
	frontierReason := frontierReasonFor(attemptErr(errCodeLifecycleAPIRequired, nil))
	under := serveAdmission(t, fm, ftenant, http.MethodPost, "/admission/reserve", reserveBody(seatRequest("model_gateway/after-frontier")))
	if under.code != http.StatusPaymentRequired || under.body["allowed"] != false || under.body["action"] != "block" || under.body["reason"] != frontierReason {
		t.Errorf("a reserve under a frontier = %d %s, want 402 block %q", under.code, under.raw, frontierReason)
	}
	settle := serveAdmission(t, fm, ftenant, http.MethodPost, "/admission/commit", map[string]any{"handle": before, "actual_micro_usd": measured})
	if settle.code != http.StatusConflict || settle.message() != lifecycle {
		t.Errorf("a commit under a frontier = %d %s, want 409 %q", settle.code, settle.raw, lifecycle)
	}
}

// storeSentinel is text a driver error carries about where the store is and who reads
// it. No answer and no log line may carry it.
const storeSentinel = "sentinel-budget-store.internal:5432 user=finops_reader"

// admittedUnestablished is the reason an answer admitted with no hold under the allow
// posture carries: fixed, and free of any stored or store-reported value.
const admittedUnestablished = "admission could not be established; admitted without a hold (unreachable=allow)"

// sentinelFaultData is the read-side counterpart of faultData: every read of the marked
// caller fails with a store error whose text carries storeSentinel, the way an
// unreachable database is reported, and every other statement reaches the real store.
type sentinelFaultData struct {
	api.ModuleData
}

func (d sentinelFaultData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if marked(ctx) {
		return fmt.Errorf("dial budget store %s: %w", storeSentinel, store.ErrStoreUnavailable)
	}
	return d.ModuleData.View(ctx, tenant, fn)
}

// TestHTTPRecordsAnUnestablishedAdmissionInBothPostures: when the admission cannot be
// established, both postures record it and neither puts the store's text on the wire or
// in the log. Deny refuses with 503 and audits the refusal. Allow admits with no hold;
// the answer carries the fixed reason that says so, and the engine log carries one ERROR
// line with the tenant, the scope, posture=allow outcome=admitted and the failure class.
func TestHTTPRecordsAnUnestablishedAdmissionInBothPostures(t *testing.T) {
	forEachAdmissionEngine(t, runHTTPRecordsAnUnestablishedAdmissionInBothPostures)
}

func runHTTPRecordsAnUnestablishedAdmissionInBothPostures(t *testing.T, cfg store.Config) {
	for _, posture := range []string{"deny", "allow"} {
		t.Run(posture, func(t *testing.T) {
			m, st, tenant, _ := settlementFixture(t, cfg)
			m.UseData(sentinelFaultData{ModuleData: m.data})
			logs := captureLog(m)
			body := reserveBody(seatRequest("model_gateway/unestablished"))
			body["unreachable"] = posture
			got := serveAdmissionIn(pausedCtx(context.Background()), t, m, tenant, http.MethodPost, "/admission/reserve", body)

			if strings.Contains(got.raw, storeSentinel) {
				t.Errorf("the answer carries the store's text: %s", got.raw)
			}
			if strings.Contains(logs.String(), storeSentinel) {
				t.Errorf("the log carries the store's text:\n%s", logs.String())
			}
			var errorLines []string
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, "level=ERROR") {
					errorLines = append(errorLines, l)
				}
			}
			switch posture {
			case "deny":
				if got.code != http.StatusServiceUnavailable || got.body["allowed"] != false || got.body["reason"] != ReasonStoreUnreachable {
					t.Errorf("deny = %d %s, want 503 refused %q", got.code, got.raw, ReasonStoreUnreachable)
				}
				if len(errorLines) != 0 {
					t.Errorf("deny: the refusal is audited, and the log carries no ERROR line: %q", errorLines)
				}
				if !auditHolds(t, st, tenant, auditActionAdmissionDenied) {
					t.Errorf("deny: the refusal left no %s audit row", auditActionAdmissionDenied)
				}
			case "allow":
				if got.code != http.StatusOK || got.body["allowed"] != true || got.body["handle"] != nil || got.body["reason"] != admittedUnestablished {
					t.Errorf("allow = %d %s, want 200 admitted with no handle and the reason %q", got.code, got.raw, admittedUnestablished)
				}
				if len(errorLines) != 1 {
					t.Fatalf("allow: %d ERROR line(s), want one:\n%s", len(errorLines), logs.String())
				}
				for _, want := range []string{"posture=allow outcome=admitted", "failure_class=unreachable",
					"tenant=" + tenant.String(), "scope=" + AdmissionScopeModelGateway} {
					if !strings.Contains(errorLines[0], want) {
						t.Errorf("allow: the ERROR line lacks %q: %s", want, errorLines[0])
					}
				}
			}
		})
	}
}

// TestHTTPAdmitsAReadableAdmissionWithoutAReason is the positive control of the allow
// leg above: the same fixture and the same read-fault wrapper, with the request unmarked,
// so the store is read. The admission is established: it answers 200 with a handle, no
// reason key, and the engine log carries no ERROR line.
func TestHTTPAdmitsAReadableAdmissionWithoutAReason(t *testing.T) {
	forEachAdmissionEngine(t, runHTTPAdmitsAReadableAdmissionWithoutAReason)
}

func runHTTPAdmitsAReadableAdmissionWithoutAReason(t *testing.T, cfg store.Config) {
	m, _, tenant, _ := settlementFixture(t, cfg)
	m.UseData(sentinelFaultData{ModuleData: m.data})
	logs := captureLog(m)
	body := reserveBody(seatRequest("model_gateway/readable"))
	body["unreachable"] = "allow"
	got := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", body)

	if handle, _ := got.body["handle"].(string); got.code != http.StatusOK || got.body["allowed"] != true || handle == "" {
		t.Errorf("allow on a readable store = %d %s, want 200 admitted with a handle", got.code, got.raw)
	}
	if reason, has := got.body["reason"]; has {
		t.Errorf("an established admission carries the reason %q: %s", reason, got.raw)
	}
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "level=ERROR") {
			t.Errorf("an established admission logged an ERROR line: %s", l)
		}
	}
}

// auditHolds reports whether the tenant's audit chain holds a row with action.
func auditHolds(t *testing.T, st store.Store, tenant model.TenantID, action string) bool {
	t.Helper()
	found := false
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(e model.AuditEvent) error {
			found = found || e.Action == action
			return nil
		})
	}); err != nil {
		t.Fatalf("walk the audit chain: %v", err)
	}
	return found
}

// TestAdmissionReconciliationWireNamesAreTheConsoles: the reconciliation answer's JSON
// names, every one set, are exactly the fields of the console's AdmissionReconciliation
// (web/src/features/finops/types.ts), and a field the console marks optional is one the
// answer omits when empty.
func TestAdmissionReconciliationWireNamesAreTheConsoles(t *testing.T) {
	raw, err := json.Marshal(AdmissionReconciliation{FindingRef: "f", Note: "n"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw, _ = json.Marshal(AdmissionReconciliation{})
	var bare map[string]any
	_ = json.Unmarshal(raw, &bare)

	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "features", "finops", "types.ts"))
	if err != nil {
		t.Fatalf("read the console's types: %v", err)
	}
	block := string(src)
	start := strings.Index(block, "export interface AdmissionReconciliation {")
	if start < 0 {
		t.Fatal("types.ts declares no AdmissionReconciliation")
	}
	block = block[start:]
	block = block[strings.Index(block, "{")+1 : strings.Index(block, "}")]
	var console, optional []string
	for _, l := range strings.Split(block, "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		if trimmed, opt := strings.CutSuffix(name, "?"); opt {
			name = trimmed
			optional = append(optional, name)
		}
		console = append(console, name)
	}
	wire, omitted := sortedNames(full), []string{}
	for _, k := range wire {
		if _, ok := bare[k]; !ok {
			omitted = append(omitted, k)
		}
	}
	sort.Strings(console)
	sort.Strings(optional)
	if !reflect.DeepEqual(wire, console) {
		t.Fatalf("the answer's names %v are not the console's %v", wire, console)
	}
	if !reflect.DeepEqual(omitted, optional) {
		t.Fatalf("the names the answer omits when empty %v are not the console's optional %v", omitted, optional)
	}
}

// sortedNames returns the keys of a decoded JSON object, sorted.
func sortedNames(obj map[string]any) []string {
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestAdmissionHTTPThroughTheAuthenticatedServer: the routes answer through the engine's
// server, with a session from the login, the role's permissions and the tenant header.
// An editor reserves and commits a hold; a viewer reads the reconciliation and is refused
// the reserve.
func TestAdmissionHTTPThroughTheAuthenticatedServer(t *testing.T) {
	m := New()
	srv := newAdmissionServer(t, m)
	admin := srv.login("root@x.io", "supersecret1")
	tenant := srv.createOrg(admin, "admission")
	editor := srv.roleToken(admin, tenant, "ed@x.io", "editor")
	viewer := srv.roleToken(admin, tenant, "vi@x.io", "viewer")

	if got := srv.do(http.MethodPost, "/v1/m/finops/budgets", editor, tenant, map[string]any{
		"name": "cap", "enabled": true, "dimension": "global", "period": "monthly",
		"limit_micro_usd": 2 * oneUSD, "action": "block",
	}); got.code != http.StatusCreated {
		t.Fatalf("create the budget = %d %s", got.code, got.raw)
	}
	reserve := map[string]any{"scope": "model_gateway", "estimate_micro_usd": oneUSD, "idempotency_key": "model_gateway/server-1"}
	if got := srv.do(http.MethodPost, "/v1/m/finops/admission/reserve", viewer, tenant, reserve); got.code != http.StatusForbidden {
		t.Fatalf("a viewer's reserve = %d %s, want 403", got.code, got.raw)
	}
	held := srv.do(http.MethodPost, "/v1/m/finops/admission/reserve", editor, tenant, reserve)
	h, _ := held.body["handle"].(string)
	if held.code != http.StatusOK || held.body["allowed"] != true || h == "" {
		t.Fatalf("an editor's reserve = %d %s, want 200 under a handle", held.code, held.raw)
	}
	if got := srv.do(http.MethodGet, "/v1/m/finops/admission/reconciliation", viewer, tenant, nil); got.code != http.StatusOK || got.body["active"] != float64(1) {
		t.Fatalf("a viewer's reconciliation = %d %s, want 200 with one active row", got.code, got.raw)
	}
	if got := srv.do(http.MethodPost, "/v1/m/finops/admission/commit", editor, tenant, map[string]any{"handle": h, "actual_micro_usd": oneUSD / 2}); got.code != http.StatusOK || got.body["committed"] != true {
		t.Fatalf("an editor's commit = %d %s, want 200", got.code, got.raw)
	}
	if got := srv.do(http.MethodGet, "/v1/m/finops/admission/reconciliation", viewer, tenant, nil); got.code != http.StatusOK || got.body["committed"] != float64(1) || got.body["active"] != float64(0) {
		t.Fatalf("the reconciliation after the commit = %d %s, want one committed row and none active", got.code, got.raw)
	}
}

// admissionServer is the engine's API server over a SQLite store, with m mounted.
type admissionServer struct {
	t        *testing.T
	srv      *api.Server
	setupTok string
}

func newAdmissionServer(t *testing.T, m *Module) *admissionServer {
	t.Helper()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &admissionServer{t: t, srv: srv, setupTok: plaintext}
}

// do sends one request through the server's handler, with a bearer token and, when
// tenant is set, the tenant header.
func (s *admissionServer) do(method, path, token string, tenant model.TenantID, body any) admissionAnswer {
	s.t.Helper()
	var payload io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("encode the request body: %v", err)
		}
		payload = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, payload)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	rec := httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(rec, req)
	out := admissionAnswer{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// login completes the first-run setup as the administrator and returns its session.
func (s *admissionServer) login(email, password string) string {
	s.t.Helper()
	if got := s.do(http.MethodPost, "/v1/setup", "", "", map[string]any{"token": s.setupTok, "email": email, "password": password}); got.code != http.StatusCreated {
		s.t.Fatalf("setup = %d %s", got.code, got.raw)
	}
	return s.session(email, password)
}

func (s *admissionServer) session(email, password string) string {
	s.t.Helper()
	got := s.do(http.MethodPost, "/v1/auth/login", "", "", map[string]any{"email": email, "password": password})
	token, _ := got.body["token"].(string)
	if got.code != http.StatusOK || token == "" {
		s.t.Fatalf("login %s = %d %s", email, got.code, got.raw)
	}
	return token
}

func (s *admissionServer) createOrg(admin, slug string) model.TenantID {
	s.t.Helper()
	got := s.do(http.MethodPost, "/v1/system/orgs", admin, "", map[string]any{"name": slug, "slug": slug})
	id, _ := got.body["tenant_id"].(string)
	if got.code != http.StatusCreated || id == "" {
		s.t.Fatalf("create org %s = %d %s", slug, got.code, got.raw)
	}
	return model.TenantID(id)
}

// roleToken creates a user with role in tenant and returns the user's session.
func (s *admissionServer) roleToken(admin string, tenant model.TenantID, email, role string) string {
	s.t.Helper()
	// The account is created with its membership: a tenant never joins an account
	// that already exists without its holder's consent.
	got := s.do(http.MethodPost, "/v1/users", admin, "", map[string]any{
		"email": email, "password": "memberpass1", "tenant": tenant.String(), "role": role,
	})
	if uid, _ := got.body["id"].(string); got.code != http.StatusCreated || uid == "" {
		s.t.Fatalf("create %s as %s = %d %s", email, role, got.code, got.raw)
	}
	return s.session(email, "memberpass1")
}

// TestAdmissionHTTPSettlesAPersistedSpendHandle: a row an earlier build wrote with a
// spend_handle stays readable over the wire, which carries one handle. Its retry is
// handed the budget hold alone, a settlement document that names a second handle is
// refused and writes nothing, and the seat hold, sent as the one handle, settles the
// rows under both holds and the row itself. The reconciliation reads the row as sound.
func TestAdmissionHTTPSettlesAPersistedSpendHandle(t *testing.T) {
	forEachAdmissionEngine(t, runAdmissionHTTPSettlesAPersistedSpendHandle)
}

func runAdmissionHTTPSettlesAPersistedSpendHandle(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/legacy-pair")
	dated := baseTime.Add(-60 * time.Second)
	h1, h2 := legacyPair(t, st, tenant, req, budget, limit, dated, dated.Add(reservationTTL), dated.Add(reservationTTL), true)
	if row := admissionRowOf(t, m, tenant, req.IdempotencyKey); row.spendHandle != h2 {
		t.Fatalf("fixture: the row's spend_handle is %q, want %s", row.spendHandle, h2)
	}

	retry := serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", reserveBody(req))
	if retry.code != http.StatusOK || retry.body["replayed"] != true || retry.body["handle"] != h1.String() {
		t.Fatalf("a retry of the pair = %d %s, want 200 replaying %s", retry.code, retry.raw, h1)
	}
	if _, two := retry.body["spend_handle"]; two {
		t.Fatalf("the answer carries a second handle: %s", retry.raw)
	}

	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
	for _, pattern := range []string{"/admission/commit", "/admission/release"} {
		body := map[string]any{"handle": h1.String(), "spend_handle": h2.String()}
		if pattern == "/admission/commit" {
			body["actual_micro_usd"] = measured
		}
		if got := serveAdmission(t, m, tenant, http.MethodPost, pattern, body); got.code != http.StatusBadRequest {
			t.Errorf("%s naming two handles = %d %s, want 400", pattern, got.code, got.raw)
		}
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))

	clk.advance(10 * time.Second)
	commit := serveAdmission(t, m, tenant, http.MethodPost, "/admission/commit", map[string]any{"handle": h2.String(), "actual_micro_usd": measured})
	if commit.code != http.StatusOK {
		t.Fatalf("a commit of the seat hold = %d %s, want 200", commit.code, commit.raw)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateCommitted || row.handle != h1 || row.spendHandle != h2 {
		t.Fatalf("the pair's row is %s naming %q and %q, want committed naming both", row.state, row.handle, row.spendHandle)
	}
	assertRowsUnder(t, st, tenant, h1, 1, resvStateCommitted, measured, clk.t)
	assertRowsUnder(t, st, tenant, h2, 1, resvStateCommitted, measured, clk.t)

	recon := serveAdmission(t, m, tenant, http.MethodGet, "/admission/reconciliation", nil)
	if recon.code != http.StatusOK || recon.body["corrupt"] != float64(0) || recon.body["committed"] != float64(2) {
		t.Fatalf("reconciliation = %d %s, want 200, two committed rows and no corrupt row", recon.code, recon.raw)
	}
}
