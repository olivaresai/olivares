// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// crossplaneFakeAPI is a deterministic httptest stand-in for the Kubernetes custom
// resource API serving one configured GVR. It records every request (method, path,
// bearer header, body) so a test can assert auth is used and the token never leaks,
// and it holds one optional XR document keyed by name.
type crossplaneFakeAPI struct {
	mu sync.Mutex

	// gvrPrefix is the collection path the fake serves, e.g.
	// "/apis/platform.acme.io/v1alpha1/xagents".
	gvrPrefix string

	// xr is the stored XR document (nil => 404 on GET). Server-Side Apply replaces it.
	xr map[string]any
	// statusConditions is injected into a GET response's .status.conditions.
	statusConditions []map[string]any

	// behavior toggles
	denyAuth     bool // respond 401 to everything (auth failure)
	getStatus    int  // override GET status (0 => normal)
	applyStatus  int  // override PATCH status (0 => 200/201)
	deleteStatus int  // override DELETE status (0 => 200)

	// recorded requests
	reqs []crossplaneRecordedReq
}

type crossplaneRecordedReq struct {
	method      string
	path        string
	rawQuery    string
	authHeader  string
	contentType string
	body        string
}

func (a *crossplaneFakeAPI) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, crossplaneRecordedReq{
		method:      r.Method,
		path:        r.URL.Path,
		rawQuery:    r.URL.RawQuery,
		authHeader:  r.Header.Get("Authorization"),
		contentType: r.Header.Get("Content-Type"),
		body:        string(body),
	})
}

func (a *crossplaneFakeAPI) requests() []crossplaneRecordedReq {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]crossplaneRecordedReq, len(a.reqs))
	copy(out, a.reqs)
	return out
}

func (a *crossplaneFakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.record(r)
	if a.denyAuth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	named := strings.HasPrefix(r.URL.Path, a.gvrPrefix+"/")
	switch r.Method {
	case http.MethodGet:
		if !named {
			http.Error(w, "collection get not used", http.StatusBadRequest)
			return
		}
		a.serveGet(w)
	case http.MethodPatch:
		a.serveApply(w, r)
	case http.MethodDelete:
		a.serveDelete(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *crossplaneFakeAPI) serveGet(w http.ResponseWriter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.getStatus != 0 {
		w.WriteHeader(a.getStatus)
		return
	}
	if a.xr == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	doc := map[string]any{}
	for k, v := range a.xr {
		doc[k] = v
	}
	if a.statusConditions != nil {
		doc["status"] = map[string]any{"conditions": a.statusConditions}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

func (a *crossplaneFakeAPI) serveApply(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.applyStatus != 0 {
		w.WriteHeader(a.applyStatus)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var obj map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &obj)
	}
	// The body is recorded in record(); here we simply persist it as the new XR.
	if obj != nil {
		a.xr = obj
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if a.xr != nil {
		_ = json.NewEncoder(w).Encode(a.xr)
	}
}

func (a *crossplaneFakeAPI) serveDelete(w http.ResponseWriter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleteStatus != 0 {
		w.WriteHeader(a.deleteStatus)
		return
	}
	if a.xr == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	a.xr = nil
	w.WriteHeader(http.StatusOK)
}

// newCrossplaneFor wires a CrossplaneBackend at the test server with a fixed GVR and a
// Desired naming the XR. The default credential source / cred carries a known token so
// tests can assert it is used as a bearer and never leaks into a returned struct.
func newCrossplaneFor(t *testing.T, srv *httptest.Server) (*CrossplaneBackend, Desired) {
	t.Helper()
	cb := NewCrossplaneBackend(CrossplaneConfig{
		APIServer:  srv.URL,
		APIGroup:   "platform.acme.io",
		APIVersion: "v1alpha1",
		Plural:     "xagents",
		Kind:       "XAgent",
		Namespaced: false,
		Timeout:    time.Minute,
	})
	cb.client = srv.Client() // use the test server's client (no TLS pinning needed)
	d := desired("crossplane")
	d.SubjectRef = "acme-bot"
	d.Image = "registry.example/acme-bot:1.0"
	d.Replicas = 2
	return cb, d
}

func crossplaneConds(synced, ready string) []map[string]any {
	return []map[string]any{
		{"type": "Synced", "status": synced, "reason": "ReconcileSuccess"},
		{"type": "Ready", "status": ready, "reason": "Available"},
	}
}

// --- Plan: 404 => create -------------------------------------------------------------

func TestCrossplanePlanCreateWhenAbsent(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"} // xr nil => 404
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	p, err := cb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if len(p.Diff.Creates) != 1 || p.Diff.Empty() {
		t.Fatalf("a 404 must plan exactly one create, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastAdditive {
		t.Fatalf("a create-only plan must be additive, got %v", p.Diff.BlastRadius)
	}
	if p.Diff.Creates[0].Ref != "acme-bot" {
		t.Fatalf("create ref = %q, want acme-bot", p.Diff.Creates[0].Ref)
	}
}

// --- Plan idempotency: equal spec => empty diff --------------------------------------

func TestCrossplanePlanIdempotentNoop(t *testing.T) {
	// Pre-load the XR with the exact spec the Desired derives, so plan is a noop. The
	// derived spec round-trips through JSON in the fake, so numeric drift (int vs
	// float64) must NOT be reported — crossplaneValuesEqual normalises this.
	d := desired("crossplane")
	d.SubjectRef = "acme-bot"
	d.Image = "registry.example/acme-bot:1.0"
	d.Replicas = 2
	wantSpec := crossplaneDesiredSpec(d)

	api := &crossplaneFakeAPI{
		gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents",
		xr: map[string]any{
			"apiVersion": "platform.acme.io/v1alpha1",
			"kind":       "XAgent",
			"metadata":   map[string]any{"name": "acme-bot"},
			"spec":       wantSpec,
		},
	}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	p, err := cb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("an already-applied spec must yield an EMPTY diff, got %+v", p.Diff)
	}
}

// --- Plan: present with different spec => update -------------------------------------

func TestCrossplanePlanUpdateWhenDrifted(t *testing.T) {
	api := &crossplaneFakeAPI{
		gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents",
		xr: map[string]any{
			"apiVersion": "platform.acme.io/v1alpha1",
			"metadata":   map[string]any{"name": "acme-bot"},
			"spec":       map[string]any{"parameters": map[string]any{"image": "registry.example/acme-bot:OLD", "replicas": 2}},
		},
	}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	p, err := cb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if len(p.Diff.Updates) != 1 || p.Diff.Empty() {
		t.Fatalf("a drifted spec must plan exactly one update, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastMutating {
		t.Fatalf("an update plan must be mutating (not destructive), got %v", p.Diff.BlastRadius)
	}
	for _, it := range p.Diff.Updates {
		if it.Destructive {
			t.Fatalf("a Server-Side Apply update must NOT be marked destructive")
		}
	}
}

// --- Apply: Server-Side Apply PATCH with the right content type and query -----------

func TestCrossplaneApplyServerSideApply(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"} // absent => create path
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)
	cred := mockCred()

	p, err := cb.Plan(context.Background(), d, cred)
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	res, err := cb.Apply(context.Background(), p, cred)
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("apply must report the planned create, got %+v", res.Applied)
	}

	// Find the PATCH (SSA) request and assert its mechanics.
	var patch *crossplaneRecordedReq
	for i := range api.requests() {
		r := api.requests()[i]
		if r.method == http.MethodPatch {
			rr := r
			patch = &rr
		}
	}
	if patch == nil {
		t.Fatalf("apply must issue a PATCH (server-side apply)")
	}
	if patch.contentType != "application/apply-patch+yaml" {
		t.Fatalf("SSA content type = %q, want application/apply-patch+yaml", patch.contentType)
	}
	if !strings.Contains(patch.rawQuery, "fieldManager=olivares-deploy") || !strings.Contains(patch.rawQuery, "force=true") {
		t.Fatalf("SSA query must carry fieldManager + force=true, got %q", patch.rawQuery)
	}
	if !strings.HasSuffix(patch.path, "/xagents/acme-bot") {
		t.Fatalf("SSA must target the named XR, got path %q", patch.path)
	}
	// The body the apiserver receives may reference the spec, but it must NOT carry the
	// credential material.
	if strings.Contains(patch.body, cred.Token) {
		t.Fatalf("apply body must never contain the credential token")
	}
}

// --- Apply idempotency: empty plan changes nothing ----------------------------------

func TestCrossplaneApplyEmptyPlanNoop(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, _ := newCrossplaneFor(t, srv)

	empty := Plan{Runtime: cb.Kind(), Intent: IntentApply, Diff: NewDiff(nil, nil, nil, true, "", "noop")}
	res, err := cb.Apply(context.Background(), empty, mockCred())
	if err != nil {
		t.Fatalf("empty apply err = %v", err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("an empty plan must change nothing, got %+v", res.Applied)
	}
	for _, r := range api.requests() {
		if r.method == http.MethodPatch || r.method == http.MethodDelete {
			t.Fatalf("an empty plan must not issue a mutating call, got %s", r.method)
		}
	}
}

// --- DestroyPlan + destroy apply (DELETE) -------------------------------------------

func TestCrossplaneDestroyPlanAndDelete(t *testing.T) {
	api := &crossplaneFakeAPI{
		gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents",
		xr: map[string]any{
			"apiVersion": "platform.acme.io/v1alpha1",
			"metadata":   map[string]any{"name": "acme-bot"},
			"spec":       map[string]any{"parameters": map[string]any{}},
		},
	}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)
	cred := mockCred()

	p, err := cb.DestroyPlan(context.Background(), d, cred)
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if len(p.Diff.Deletes) != 1 {
		t.Fatalf("destroy plan must contain exactly one delete, got %+v", p.Diff)
	}
	if !p.Diff.Deletes[0].Destructive {
		t.Fatalf("a destroy must be marked Destructive (drives the blast-radius gate)")
	}
	if p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("a delete plan must be destructive, got %v", p.Diff.BlastRadius)
	}

	res, err := cb.Apply(context.Background(), p, cred)
	if err != nil {
		t.Fatalf("destroy apply err = %v", err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("destroy apply must report the delete, got %+v", res.Applied)
	}
	var sawDelete bool
	for _, r := range api.requests() {
		if r.method == http.MethodDelete && strings.HasSuffix(r.path, "/xagents/acme-bot") {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("destroy apply must DELETE the named XR")
	}
}

// --- DestroyPlan idempotency: absent XR => empty diff -------------------------------

func TestCrossplaneDestroyPlanAbsentIsNoop(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"} // xr nil
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	p, err := cb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("retiring an absent XR must be an empty diff, got %+v", p.Diff)
	}
}

// --- Observe: Synced=True & Ready=True => InSync ------------------------------------

func TestCrossplaneObserveInSync(t *testing.T) {
	api := &crossplaneFakeAPI{
		gvrPrefix:        "/apis/platform.acme.io/v1alpha1/xagents",
		xr:               map[string]any{"metadata": map[string]any{"name": "acme-bot"}, "spec": map[string]any{}},
		statusConditions: crossplaneConds("True", "True"),
	}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	rs, err := cb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Exists || !rs.Observable || !rs.InSync {
		t.Fatalf("Synced=True Ready=True must be InSync, got %+v", rs)
	}
	if len(rs.Drift) != 0 {
		t.Fatalf("an in-sync XR must report no drift, got %+v", rs.Drift)
	}
}

// --- Observe: not ready => drift ----------------------------------------------------

func TestCrossplaneObserveDriftWhenNotReady(t *testing.T) {
	api := &crossplaneFakeAPI{
		gvrPrefix:        "/apis/platform.acme.io/v1alpha1/xagents",
		xr:               map[string]any{"metadata": map[string]any{"name": "acme-bot"}, "spec": map[string]any{}},
		statusConditions: crossplaneConds("True", "False"),
	}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	rs, err := cb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Exists || !rs.Observable {
		t.Fatalf("a present XR must be observable, got %+v", rs)
	}
	if rs.InSync {
		t.Fatalf("Ready=False must NOT be reported as in-sync")
	}
	if len(rs.Drift) == 0 {
		t.Fatalf("a not-ready XR must report drift")
	}
	var sawReady bool
	for _, it := range rs.Drift {
		if strings.Contains(it.Detail, "Ready") {
			sawReady = true
		}
	}
	if !sawReady {
		t.Fatalf("drift must name the unready Ready condition, got %+v", rs.Drift)
	}
}

// --- Observe: 404 => Exists:false ---------------------------------------------------

func TestCrossplaneObserveMissingIsHonestGap(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"} // xr nil => 404
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	rs, err := cb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if rs.Exists || rs.InSync {
		t.Fatalf("a 404 must report Exists=false and not in-sync, got %+v", rs)
	}
	if !rs.Observable {
		t.Fatalf("a 404 is still observable (we read a definitive answer), got %+v", rs)
	}
}

// --- Observe: unreachable apiserver => honest gap (Observable=false) ----------------

func TestCrossplaneObserveUnreachableIsObservableFalse(t *testing.T) {
	// Point the backend at a closed server so the transport fails.
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"}
	srv := httptest.NewServer(api)
	cb, d := newCrossplaneFor(t, srv)
	srv.Close() // now unreachable

	rs, err := cb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe of an unreachable apiserver must NOT error (honest gap), got %v", err)
	}
	if rs.Observable {
		t.Fatalf("an unreachable apiserver must be Observable=false (never faked InSync), got %+v", rs)
	}
	if rs.InSync {
		t.Fatalf("an unobservable unit must never be reported InSync")
	}
}

// --- Bearer auth is used, and the token never leaks ----------------------------------

func TestCrossplaneBearerUsedAndTokenNeverLeaks(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"} // create path
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)
	cred := mockCred()

	p, err := cb.Plan(context.Background(), d, cred)
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	res, err := cb.Apply(context.Background(), p, cred)
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	rs, err := cb.Observe(context.Background(), d, cred)
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}

	// 1) every request carried the bearer token in the Authorization header.
	reqs := api.requests()
	if len(reqs) == 0 {
		t.Fatalf("expected the backend to call the apiserver")
	}
	for _, r := range reqs {
		if r.authHeader != "Bearer "+cred.Token {
			t.Fatalf("every API call must carry the bearer credential, got %q", r.authHeader)
		}
	}

	// 2) the token must NEVER appear in any returned struct (Plan handle is internal,
	//    but the public Diff/Result/RealState must be clean).
	leak := func(s string) bool { return strings.Contains(s, cred.Token) }
	for _, it := range p.Diff.Items() {
		if leak(it.Ref) || leak(it.Detail) || leak(it.Kind) {
			t.Fatalf("credential material leaked into a Diff item: %+v", it)
		}
	}
	if leak(p.Diff.Summary) || leak(p.Diff.RollbackHint) {
		t.Fatalf("credential material leaked into the Diff summary/hint")
	}
	for _, it := range res.Applied {
		if leak(it.Ref) || leak(it.Detail) || leak(it.Kind) {
			t.Fatalf("credential material leaked into a Result item: %+v", it)
		}
	}
	if leak(res.Detail) || leak(res.CredentialID) || leak(res.BackendID) {
		t.Fatalf("credential material leaked into the Result")
	}
	if leak(rs.Detail) {
		t.Fatalf("credential material leaked into RealState detail")
	}
	for _, it := range rs.Drift {
		if leak(it.Ref) || leak(it.Detail) {
			t.Fatalf("credential material leaked into RealState drift: %+v", it)
		}
	}
}

// --- the desired XR spec carries secret REFERENCES only, never cleartext ------------

func TestCrossplaneSecretsByReferenceOnly(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)
	d.EnvRefs = []SecretBinding{{Name: "API_KEY", SecretRef: "vault:kv/acme#api_key"}}
	cred := mockCred()

	p, err := cb.Plan(context.Background(), d, cred)
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if _, err := cb.Apply(context.Background(), p, cred); err != nil {
		t.Fatalf("apply err = %v", err)
	}

	// The apply body must contain the secret REFERENCE (the provider resolves it), and
	// it must never appear in any returned struct.
	var patchBody string
	for _, r := range api.requests() {
		if r.method == http.MethodPatch {
			patchBody = r.body
		}
	}
	if !strings.Contains(patchBody, "vault:kv/acme#api_key") {
		t.Fatalf("the apply body must reference the secret by reference, got %q", patchBody)
	}
	// The reference must never surface in the Diff/Result (minimal data: only kind+ref+detail).
	for _, it := range p.Diff.Items() {
		if strings.Contains(it.Ref+it.Detail, "vault:kv/acme#api_key") {
			t.Fatalf("a secret reference must not appear in a Diff item: %+v", it)
		}
	}
}

// --- auth failure surfaces as an error on Plan (deny-closed), not a fake create -----

func TestCrossplanePlanAuthFailureSurfaces(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents", denyAuth: true}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	if _, err := cb.Plan(context.Background(), d, mockCred()); err == nil {
		t.Fatalf("a 401 from the apiserver must surface as an error, never a fake plan")
	}
}

// --- end to end behind the Executor: an additive apply passes the gate --------------

func TestCrossplaneE2EBlastRadiusGate(t *testing.T) {
	// A 404 plans a create (additive, allowed). Assert it passes the gate end-to-end
	// through the Executor and the minted token never reaches the result.
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents"}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	e := New(WithBackend(cb, "crossplane"), WithCredentialSource(mockCredSource(time.Hour)))
	res, err := e.Apply(context.Background(), d)
	if err != nil {
		t.Fatalf("an additive crossplane apply must pass the gate, got %v", err)
	}
	if res.BackendID != "crossplane" {
		t.Fatalf("result backend id = %q, want crossplane", res.BackendID)
	}
	if !strings.HasSuffix(res.CredentialID, ":write") {
		t.Fatalf("apply must use a WRITE-scoped credential, got %q", res.CredentialID)
	}
	// The minted token must never appear in the result.
	if strings.Contains(res.CredentialID+res.Detail, "SECRET-TOKEN") {
		t.Fatalf("credential material leaked into the Executor result: %+v", res)
	}
}

// --- Retire end to end: a deliberate teardown is allowed and DELETEs the XR ----------

func TestCrossplaneRetireE2E(t *testing.T) {
	api := &crossplaneFakeAPI{
		gvrPrefix: "/apis/platform.acme.io/v1alpha1/xagents",
		xr:        map[string]any{"metadata": map[string]any{"name": "acme-bot"}, "spec": map[string]any{}},
	}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb, d := newCrossplaneFor(t, srv)

	e := New(WithBackend(cb, "crossplane"), WithCredentialSource(mockCredSource(time.Hour)))
	if _, err := e.Retire(context.Background(), d); err != nil {
		t.Fatalf("a deliberate retire (teardown) must be allowed by default, got %v", err)
	}
	var sawDelete bool
	for _, r := range api.requests() {
		if r.method == http.MethodDelete {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("retire must DELETE the XR through the Executor")
	}
}

// --- namespaced (Claim) path includes namespaces/<ns> -------------------------------

func TestCrossplaneNamespacedPath(t *testing.T) {
	api := &crossplaneFakeAPI{gvrPrefix: "/apis/platform.acme.io/v1alpha1/namespaces/team-a/agentclaims"}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cb := NewCrossplaneBackend(CrossplaneConfig{
		APIServer:  srv.URL,
		APIGroup:   "platform.acme.io",
		APIVersion: "v1alpha1",
		Plural:     "agentclaims",
		Namespaced: true,
		Namespace:  "team-a",
		Timeout:    time.Minute,
	})
	cb.client = srv.Client()
	d := desired("crossplane")
	d.SubjectRef = "acme-bot"

	if _, err := cb.Plan(context.Background(), d, mockCred()); err != nil {
		t.Fatalf("namespaced plan err = %v", err)
	}
	var sawNS bool
	for _, r := range api.requests() {
		if strings.Contains(r.path, "/namespaces/team-a/agentclaims/acme-bot") {
			sawNS = true
		}
	}
	if !sawNS {
		t.Fatalf("a namespaced Claim must be addressed under /namespaces/<ns>/, got %+v", api.requests())
	}
}

// --- a namespaced config with no namespace fails closed -----------------------------

func TestCrossplaneNamespacedRequiresNamespace(t *testing.T) {
	cb := NewCrossplaneBackend(CrossplaneConfig{
		APIServer:  "https://example.invalid",
		APIGroup:   "platform.acme.io",
		APIVersion: "v1alpha1",
		Plural:     "agentclaims",
		Namespaced: true, // no Namespace
	})
	cb.client = http.DefaultClient
	d := desired("crossplane")
	d.SubjectRef = "acme-bot"
	if _, err := cb.Plan(context.Background(), d, mockCred()); err == nil {
		t.Fatalf("a Namespaced backend with no Namespace must fail closed")
	}
}
