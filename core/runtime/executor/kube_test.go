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
	"testing"
	"time"
)

// kubeAPIStub is a deterministic in-memory Kubernetes API: it answers the single
// Deployment resource path with a scripted GET (present or 404) and records every
// request so a test can assert the bearer header, the SSA content type, and the
// methods/paths used. It NEVER touches a real cluster.
type kubeAPIStub struct {
	present     bool   // does the Deployment exist (GET => 200 vs 404)?
	liveImage   string // image of the present object
	liveRepl    int    // replicas of the present object
	liveCommand []string

	// recorded requests
	gotAuth        []string // Authorization header per request
	gotMethods     []string
	gotPaths       []string // path + raw query
	gotContentType []string
	gotBodies      []string

	patchStatus  int // status to return for PATCH (default 200)
	deleteStatus int // status to return for DELETE (default 200)
}

func (s *kubeAPIStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body string
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}
		s.gotAuth = append(s.gotAuth, r.Header.Get("Authorization"))
		s.gotMethods = append(s.gotMethods, r.Method)
		s.gotPaths = append(s.gotPaths, r.URL.Path+"?"+r.URL.RawQuery)
		s.gotContentType = append(s.gotContentType, r.Header.Get("Content-Type"))
		s.gotBodies = append(s.gotBodies, body)

		switch r.Method {
		case http.MethodGet:
			if !s.present {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":404}`))
				return
			}
			repl := s.liveRepl
			live := map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata":   map[string]any{"name": "acme-bot", "namespace": "agents"},
				"spec": map[string]any{
					"replicas": repl,
					"template": map[string]any{
						"spec": map[string]any{
							"containers": []map[string]any{
								{"image": s.liveImage, "command": s.liveCommand},
							},
						},
					},
				},
			}
			b, _ := json.Marshal(live)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		case http.MethodPatch:
			code := s.patchStatus
			if code == 0 {
				code = http.StatusOK
			}
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"kind":"Deployment"}`))
		case http.MethodDelete:
			code := s.deleteStatus
			if code == 0 {
				code = http.StatusOK
			}
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Success"}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// newKubeFor wires a KubeBackend at a stub server with an injected client (the
// httptest client trusts the stub's TLS cert) and a desired spec targeting the
// "agents" namespace / "acme-bot" deployment.
func newKubeFor(t *testing.T, srv *httptest.Server) (*KubeBackend, Desired) {
	t.Helper()
	kb := NewKubeBackend(KubeConfig{APIBaseURL: srv.URL, Timeout: time.Minute})
	kb.client = srv.Client() // inject the test client (trusts the stub cert)
	d := desired("k8s")
	d.Target = "k8s.namespace/agents"
	d.SubjectRef = "acme-bot"
	d.Image = "registry.example.com/acme-bot:v2"
	d.Replicas = 3
	d.EnvRefs = []SecretBinding{{Name: "DB_PASSWORD", SecretRef: "k8s:db-creds/password"}}
	return kb, d
}

func kubeServer(t *testing.T, stub *kubeAPIStub) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(stub.handler())
	t.Cleanup(srv.Close)
	return srv
}

// --- Plan: 404 => create ---------------------------------------------------------

func TestKubePlanCreateOn404(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if len(p.Diff.Creates) != 1 || !kubeNoUpdates(p.Diff) || len(p.Diff.Deletes) != 0 {
		t.Fatalf("404 must yield exactly one create, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastAdditive {
		t.Fatalf("a create-only plan must be Additive, got %v", p.Diff.BlastRadius)
	}
	if p.Diff.Creates[0].Ref != "agents/acme-bot" {
		t.Fatalf("create ref = %q, want agents/acme-bot", p.Diff.Creates[0].Ref)
	}
	// The saved handle is the SSA manifest body and must reference the secret by
	// the k8s-native secretKeyRef — never as a cleartext "value".
	if !strings.Contains(p.Handle, "secretKeyRef") {
		t.Fatalf("env must be bound by secretKeyRef reference: %s", p.Handle)
	}
	if strings.Contains(p.Handle, "\"value\":") {
		t.Fatalf("env must NEVER be a cleartext value in the manifest: %s", p.Handle)
	}
	if strings.Contains(p.Handle, mockCred().Token) {
		t.Fatalf("the bearer token must never appear in the saved manifest handle")
	}
}

// helper used above: there is no Diff.Updates_isEmpty in the contract; define a
// local assertion shim prefixed so it does not collide. (kept tiny + readable)
func kubeNoUpdates(d Diff) bool { return len(d.Updates) == 0 }

// --- Apply: Server-Side Apply with the apply-patch content type ------------------

func TestKubeApplyServerSideApply(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	res, err := kb.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("apply must report the planned create, got %+v", res.Applied)
	}

	// Find the PATCH (the SSA call).
	var sawPatch bool
	for i, m := range stub.gotMethods {
		if m != http.MethodPatch {
			continue
		}
		sawPatch = true
		if stub.gotContentType[i] != "application/apply-patch+yaml" {
			t.Fatalf("SSA must use the apply-patch content type, got %q", stub.gotContentType[i])
		}
		if !strings.Contains(stub.gotPaths[i], "fieldManager=olivares-deploy") {
			t.Fatalf("SSA must set fieldManager=olivares-deploy, path = %q", stub.gotPaths[i])
		}
		if !strings.Contains(stub.gotPaths[i], "force=true") {
			t.Fatalf("SSA must set force=true, path = %q", stub.gotPaths[i])
		}
		if !strings.HasPrefix(stub.gotPaths[i], "/apis/apps/v1/namespaces/agents/deployments/acme-bot") {
			t.Fatalf("SSA must PATCH the deployment path, got %q", stub.gotPaths[i])
		}
	}
	if !sawPatch {
		t.Fatalf("apply must issue a PATCH (server-side apply)")
	}
}

// --- the bearer token: in the Authorization header, NEVER in a returned struct ---

func TestKubeBearerInHeaderNeverInStruct(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)
	cred := mockCred()

	p, err := kb.Plan(context.Background(), d, cred)
	if err != nil {
		t.Fatal(err)
	}
	res, err := kb.Apply(context.Background(), p, cred)
	if err != nil {
		t.Fatal(err)
	}

	// Every request to the API must carry the bearer in the Authorization header.
	wantAuth := "Bearer " + cred.Token
	for i, a := range stub.gotAuth {
		if a != wantAuth {
			t.Fatalf("request %d (%s) must carry the bearer in Authorization, got %q", i, stub.gotMethods[i], a)
		}
	}

	// The token must NEVER appear in any returned struct (Plan diff/handle, Result).
	leak := func(s string) bool { return strings.Contains(s, cred.Token) }
	if leak(p.Handle) || leak(p.Diff.Summary) {
		t.Fatalf("credential material leaked into the Plan")
	}
	for _, it := range p.Diff.Items() {
		if leak(it.Ref) || leak(it.Detail) || leak(it.Kind) {
			t.Fatalf("credential material leaked into a Plan change item: %+v", it)
		}
	}
	if leak(res.Detail) || leak(res.CredentialID) {
		t.Fatalf("credential material leaked into the Result")
	}
	for _, it := range res.Applied {
		if leak(it.Ref) || leak(it.Detail) || leak(it.Kind) {
			t.Fatalf("credential material leaked into a Result change item: %+v", it)
		}
	}

	// The token must also never appear in the request BODY (it is auth, not payload).
	for i, b := range stub.gotBodies {
		if strings.Contains(b, cred.Token) {
			t.Fatalf("credential material leaked into request %d body (%s)", i, stub.gotMethods[i])
		}
	}
}

// --- Plan: present-equal => empty diff (idempotent noop) -------------------------

func TestKubePlanNoopWhenEqual(t *testing.T) {
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v2", liveRepl: 3}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)
	d.Command = "" // no command desired; live has none

	p, err := kb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("a present, equal deployment must yield an EMPTY diff (noop), got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastReadOnly {
		t.Fatalf("a noop must be read-only, got %v", p.Diff.BlastRadius)
	}

	// Applying the noop plan must change nothing (no PATCH).
	res, err := kb.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply noop err = %v", err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("applying a noop must change nothing, got %+v", res.Applied)
	}
	for _, m := range stub.gotMethods {
		if m == http.MethodPatch || m == http.MethodDelete {
			t.Fatalf("a noop apply must never mutate (saw %s)", m)
		}
	}
}

// --- Plan: present-differ => update ----------------------------------------------

func TestKubePlanUpdateWhenDiffers(t *testing.T) {
	// live image + replicas differ from desired (v2 / 3).
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v1", liveRepl: 1}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if len(p.Diff.Updates) != 1 || len(p.Diff.Creates) != 0 || len(p.Diff.Deletes) != 0 {
		t.Fatalf("a drifted deployment must yield exactly one update, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastMutating {
		t.Fatalf("an update-only plan must be Mutating, got %v", p.Diff.BlastRadius)
	}
	if p.Diff.Updates[0].Destructive {
		t.Fatalf("an in-place image/replicas update must NOT be Destructive")
	}
	if !strings.Contains(p.Diff.Updates[0].Detail, "image") && !strings.Contains(p.Diff.Updates[0].Detail, "replicas") {
		t.Fatalf("update detail should name the drift, got %q", p.Diff.Updates[0].Detail)
	}
}

// --- DestroyPlan + delete apply (Foreground propagation) -------------------------

func TestKubeDestroyPlanAndDelete(t *testing.T) {
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v2", liveRepl: 3}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if len(p.Diff.Deletes) != 1 || !p.Diff.Deletes[0].Destructive {
		t.Fatalf("destroy plan must be one Destructive delete, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("a destroy plan must be Destructive, got %v", p.Diff.BlastRadius)
	}
	if p.Intent != IntentDestroy {
		t.Fatalf("destroy plan intent must be IntentDestroy")
	}

	res, err := kb.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("destroy apply err = %v", err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("destroy apply must report the delete, got %+v", res.Applied)
	}
	// The DELETE must use Foreground propagation on the deployment path.
	var sawDelete bool
	for i, m := range stub.gotMethods {
		if m != http.MethodDelete {
			continue
		}
		sawDelete = true
		if !strings.Contains(stub.gotPaths[i], "propagationPolicy=Foreground") {
			t.Fatalf("delete must use Foreground propagation, path = %q", stub.gotPaths[i])
		}
		if !strings.HasPrefix(stub.gotPaths[i], "/apis/apps/v1/namespaces/agents/deployments/acme-bot") {
			t.Fatalf("delete must hit the deployment path, got %q", stub.gotPaths[i])
		}
	}
	if !sawDelete {
		t.Fatalf("destroy apply must issue a DELETE")
	}
}

// --- DestroyPlan when absent => empty (idempotent) -------------------------------

func TestKubeDestroyPlanAbsentIsEmpty(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("destroying an absent deployment must be an empty diff, got %+v", p.Diff)
	}
}

// --- Observe: 404, equal, drift, unreachable ------------------------------------

func TestKubeObserveAbsent(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	rs, err := kb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if rs.Exists || !rs.Observable || rs.InSync || len(rs.Drift) != 1 {
		t.Fatalf("a 404 must be Exists:false, Observable:true, InSync:false, drift=[create], got %+v", rs)
	}
	if rs.Drift[0].Action != "create" {
		t.Fatalf("absent drift must be a create, got %q", rs.Drift[0].Action)
	}
}

func TestKubeObserveInSync(t *testing.T) {
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v2", liveRepl: 3}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	rs, err := kb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Exists || !rs.Observable || !rs.InSync || len(rs.Drift) != 0 {
		t.Fatalf("an equal deployment must be Exists:true, Observable:true, InSync:true, got %+v", rs)
	}
}

func TestKubeObserveDrift(t *testing.T) {
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v1", liveRepl: 1}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	rs, err := kb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Exists || !rs.Observable || rs.InSync || len(rs.Drift) != 1 {
		t.Fatalf("a drifted deployment must report Observable drift, got %+v", rs)
	}
	if rs.Drift[0].Action != "update" {
		t.Fatalf("drift action must be update, got %q", rs.Drift[0].Action)
	}
}

func TestKubeObserveUnreachableIsHonestGap(t *testing.T) {
	srv := kubeServer(t, &kubeAPIStub{present: false})
	kb, d := newKubeFor(t, srv)
	srv.Close() // make the API unreachable

	rs, err := kb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe of an unreachable API must NOT error (it is a gap), got %v", err)
	}
	if rs.Observable {
		t.Fatalf("an unreachable API must be Observable:false (an honest gap), got %+v", rs)
	}
	if rs.InSync {
		t.Fatalf("an unobservable unit must NEVER be reported InSync")
	}
}

// --- connection resolution fails closed (no ambient kubeconfig) ------------------

func TestKubeRefusesWithoutAPIBaseURL(t *testing.T) {
	kb := NewKubeBackend(KubeConfig{}) // no APIBaseURL
	d := desired("k8s")
	d.Target = "k8s.namespace/agents"
	if _, err := kb.Plan(context.Background(), d, mockCred()); err == nil {
		t.Fatalf("plan must fail closed without a configured APIBaseURL (no ambient kubeconfig)")
	}
}

func TestKubeRefusesWithoutNamespace(t *testing.T) {
	srv := kubeServer(t, &kubeAPIStub{present: false})
	kb := NewKubeBackend(KubeConfig{APIBaseURL: srv.URL})
	kb.client = srv.Client()
	d := desired("k8s")
	d.Target = "k8s.namespace" // no namespace, no DefaultNamespace
	if _, err := kb.Plan(context.Background(), d, mockCred()); err == nil {
		t.Fatalf("plan must fail closed without a namespace (no implicit default)")
	}
}

func TestKubeDefaultNamespaceFallback(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb := NewKubeBackend(KubeConfig{APIBaseURL: srv.URL, DefaultNamespace: "fallback-ns", Timeout: time.Minute})
	kb.client = srv.Client()
	d := desired("k8s")
	d.Target = "k8s.namespace" // no namespace in target -> use the default
	d.SubjectRef = "acme-bot"
	d.Image = "img:v1"

	if _, err := kb.Plan(context.Background(), d, mockCred()); err != nil {
		t.Fatalf("plan with DefaultNamespace fallback err = %v", err)
	}
	if len(stub.gotPaths) == 0 || !strings.Contains(stub.gotPaths[0], "/namespaces/fallback-ns/") {
		t.Fatalf("plan must use the DefaultNamespace, paths = %v", stub.gotPaths)
	}
}

// --- Kind + idempotency of re-plan after apply -----------------------------------

func TestKubeKind(t *testing.T) {
	if NewKubeBackend(KubeConfig{}).Kind() != "k8s" {
		t.Fatalf("Kind must be k8s")
	}
}

// TestKubeReplanAfterConvergeIsNoop proves idempotency: once the live object equals
// desired, a re-plan yields an empty diff (the same spec applied twice converges).
func TestKubeReplanAfterConvergeIsNoop(t *testing.T) {
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v2", liveRepl: 3}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("re-plan of an already-converged deployment must be empty, got %+v", p.Diff)
	}
	if !kubeNoUpdates(p.Diff) {
		t.Fatalf("noop must have no updates")
	}
}

// --- E2E behind the Executor: create is additive (allowed), retire (destroy)
//     is allowed by default; the credential material never surfaces. -------------

func TestKubeCreateThroughExecutor(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)
	e := New(WithBackend(kb), WithCredentialSource(mockCredSource(time.Hour)))

	res, err := e.Apply(context.Background(), d)
	if err != nil {
		t.Fatalf("create through executor err = %v", err)
	}
	if res.BackendID != "k8s" {
		t.Fatalf("result backend id = %q, want k8s", res.BackendID)
	}
	if !strings.HasSuffix(res.CredentialID, ":write") {
		t.Fatalf("apply must use a WRITE-scoped credential, got %q", res.CredentialID)
	}
	// The Executor mints "SECRET-TOKEN-write"; it must never surface in the Result.
	if strings.Contains(res.Detail, "SECRET-TOKEN") || strings.Contains(res.CredentialID, "SECRET-TOKEN") {
		t.Fatalf("credential material leaked into the Result: %+v", res)
	}
}

func TestKubeRetireThroughExecutor(t *testing.T) {
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v2", liveRepl: 3}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)
	e := New(WithBackend(kb), WithCredentialSource(mockCredSource(time.Hour)))

	if _, err := e.Retire(context.Background(), d); err != nil {
		t.Fatalf("retire (deliberate teardown) must be allowed by default, got %v", err)
	}
	var sawDelete bool
	for _, m := range stub.gotMethods {
		if m == http.MethodDelete {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("retire must DELETE the deployment")
	}
}

// --- Rollback re-applies the prior manifest; a destroy handle is an honest limit -

func TestKubeRollbackReappliesManifest(t *testing.T) {
	stub := &kubeAPIStub{present: false}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kb.Rollback(context.Background(), p, mockCred()); err != nil {
		t.Fatalf("rollback of a forward plan (re-apply the manifest) must succeed, got %v", err)
	}
	var sawPatch bool
	for i, m := range stub.gotMethods {
		if m == http.MethodPatch {
			sawPatch = true
			if stub.gotContentType[i] != "application/apply-patch+yaml" {
				t.Fatalf("rollback re-apply must use the apply-patch content type")
			}
		}
	}
	if !sawPatch {
		t.Fatalf("rollback must re-apply via PATCH (server-side apply)")
	}
}

func TestKubeRollbackDestroyIsHonestLimitation(t *testing.T) {
	stub := &kubeAPIStub{present: true, liveImage: "registry.example.com/acme-bot:v2", liveRepl: 3}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kb.Rollback(context.Background(), p, mockCred()); err == nil {
		t.Fatalf("rollback of a destroy plan has no prior manifest; it must report an honest limitation")
	}
}

// --- SSA error surfaces honestly (non-2xx) --------------------------------------

func TestKubeApplyNon2xxIsError(t *testing.T) {
	stub := &kubeAPIStub{present: false, patchStatus: http.StatusConflict}
	srv := kubeServer(t, stub)
	kb, d := newKubeFor(t, srv)

	p, err := kb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kb.Apply(context.Background(), p, mockCred()); err == nil {
		t.Fatalf("a non-2xx SSA response must surface as an honest error, not a faked success")
	}
}
