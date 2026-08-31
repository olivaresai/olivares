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

// nomadFakeServer is a deterministic in-memory Nomad API for the backend tests. It
// drives /v1/job/<id> (GET), /v1/job/<id>/plan (POST), /v1/job/<id> (POST register),
// and DELETE — and records every X-Nomad-Token header it receives so a test can prove
// the token IS sent on the wire but NEVER leaks into a returned struct.
type nomadFakeServer struct {
	mu sync.Mutex

	// exists controls whether GET /v1/job/<id> returns a job (true) or 404.
	exists bool
	// jobStatus + groupCount shape the GET response body for an existing job.
	jobStatus  string
	groupCount int
	// planType is the Diff.Type the plan endpoint returns ("Added"/"Edited"/"Deleted"/"None").
	planType string
	// getStatus, if non-zero, overrides the GET status (e.g. 500 for an honest gap).
	getStatus int

	// recorded calls
	seenTokens []string // X-Nomad-Token on every request
	methods    []string // METHOD path of every request
	planBodies [][]byte // raw bodies POSTed to /plan
	registered [][]byte // raw bodies POSTed to register
	deleted    []string // raw URLs DELETEd
}

func (s *nomadFakeServer) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenTokens = append(s.seenTokens, r.Header.Get("X-Nomad-Token"))
	s.methods = append(s.methods, r.Method+" "+r.URL.Path)
}

func (s *nomadFakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	// plan endpoint: /v1/job/<id>/plan
	mux.HandleFunc("/v1/job/", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		switch {
		case strings.HasSuffix(r.URL.Path, "/plan") && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.planBodies = append(s.planBodies, body)
			pt := s.planType
			s.mu.Unlock()
			if pt == "" {
				pt = "None"
			}
			resp := map[string]any{
				"Diff":           map[string]any{"Type": pt, "ID": "bot"},
				"JobModifyIndex": 7,
			}
			nomadWriteJSON(w, http.StatusOK, resp)
		case r.Method == http.MethodGet:
			s.mu.Lock()
			exists, st, status, gc := s.exists, s.jobStatus, s.getStatus, s.groupCount
			s.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("job not found"))
				return
			}
			job := map[string]any{
				"ID":     "bot",
				"Name":   "bot",
				"Status": st,
				"TaskGroups": []map[string]any{
					{"Name": "bot", "Count": gc},
				},
			}
			nomadWriteJSON(w, http.StatusOK, job)
		case r.Method == http.MethodPost: // register
			body, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.registered = append(s.registered, body)
			s.mu.Unlock()
			nomadWriteJSON(w, http.StatusOK, map[string]any{"EvalID": "e1"})
		case r.Method == http.MethodDelete:
			s.mu.Lock()
			s.deleted = append(s.deleted, r.URL.String())
			s.mu.Unlock()
			nomadWriteJSON(w, http.StatusOK, map[string]any{"EvalID": "e2"})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func nomadWriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// nomadTestBackend wires a NomadBackend at an httptest server's URL. The server is
// plain HTTP, so we replace the backend's TLS doer with one over the default client
// pointed at the test server — exercising the REAL apiRequest/doAPI/header path.
func nomadTestBackend(t *testing.T, srv *httptest.Server) *NomadBackend {
	t.Helper()
	b := NewNomadBackend(NomadConfig{BaseURL: srv.URL, Namespace: "default", Timeout: time.Minute})
	// Point the production doer at the test server's (HTTP) client so the full
	// apiRequest -> doAPI -> header path runs without TLS.
	b.client = nomadHTTPDoer{httpClient: srv.Client()}
	return b
}

func nomadDesired() Desired {
	d := desired("nomad")
	d.Target = "nomad.region/global"
	d.SubjectRef = "bot"
	d.Image = "ghcr.io/acme/bot:1.2.3"
	d.Replicas = 2
	return d
}

// --- create -----------------------------------------------------------------------

func TestNomadPlanCreate(t *testing.T) {
	fs := &nomadFakeServer{exists: false, planType: "Added"}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	p, err := b.Plan(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if p.Diff.Empty() {
		t.Fatalf("a missing job must plan a create, got empty diff")
	}
	if len(p.Diff.Creates) != 1 || p.Diff.Creates[0].Action != "create" {
		t.Fatalf("expected 1 create, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastAdditive {
		t.Fatalf("a create-only diff must be Additive, got %v", p.Diff.BlastRadius)
	}
	if p.Diff.Creates[0].Kind != nomadJobKind || p.Diff.Creates[0].Ref != "bot" {
		t.Fatalf("change item must carry kind+ref, got %+v", p.Diff.Creates[0])
	}
	// the plan request body must have asked for a Diff
	if len(fs.planBodies) != 1 {
		t.Fatalf("plan must POST exactly one plan request, got %d", len(fs.planBodies))
	}
	var pr nomadPlanRequest
	if err := json.Unmarshal(fs.planBodies[0], &pr); err != nil {
		t.Fatalf("plan body is not a plan request: %v", err)
	}
	if !pr.Diff {
		t.Fatalf("plan request must set Diff:true")
	}
	if pr.Job.ID != "bot" || len(pr.Job.TaskGroups) != 1 || pr.Job.TaskGroups[0].Count != 2 {
		t.Fatalf("plan job must carry id+count from desired, got %+v", pr.Job)
	}
}

func TestNomadApplyCreateRegisters(t *testing.T) {
	fs := &nomadFakeServer{exists: false, planType: "Added"}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	p, err := b.Plan(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	if len(fs.registered) != 1 {
		t.Fatalf("apply must register exactly once, got %d", len(fs.registered))
	}
	if len(res.Applied) != 1 || res.Applied[0].Action != "create" {
		t.Fatalf("result must report the applied create, got %+v", res.Applied)
	}
	// the registered body must carry the desired image (referenced, non-secret) and count
	var reg nomadRegisterRequest
	if err := json.Unmarshal(fs.registered[0], &reg); err != nil {
		t.Fatalf("register body malformed: %v", err)
	}
	if reg.Job.TaskGroups[0].Count != 2 {
		t.Fatalf("registered job count = %d, want 2", reg.Job.TaskGroups[0].Count)
	}
	img, _ := reg.Job.TaskGroups[0].Tasks[0].Config["image"].(string)
	if img != "ghcr.io/acme/bot:1.2.3" {
		t.Fatalf("registered task image = %q", img)
	}
}

// --- noop (idempotency) -----------------------------------------------------------

func TestNomadPlanNoopWhenUpToDate(t *testing.T) {
	// job exists and the native plan reports no diff => empty Diff (idempotent noop)
	fs := &nomadFakeServer{exists: true, jobStatus: "running", groupCount: 2, planType: "None"}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	p, err := b.Plan(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("an already-applied spec must yield an EMPTY diff, got %+v", p.Diff)
	}
	if p.Handle != "" {
		t.Fatalf("a noop plan must carry no handle to apply, got %q", p.Handle)
	}
	// applying an empty plan changes nothing
	res, err := b.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply of noop err = %v", err)
	}
	if len(fs.registered) != 0 || len(fs.deleted) != 0 {
		t.Fatalf("applying an empty plan must not register or delete anything")
	}
	if len(res.Applied) != 0 {
		t.Fatalf("noop apply must report no applied changes, got %+v", res.Applied)
	}
}

func TestNomadPlanEditedIsUpdate(t *testing.T) {
	fs := &nomadFakeServer{exists: true, jobStatus: "running", groupCount: 1, planType: "Edited"}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	p, err := b.Plan(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Diff.Updates) != 1 || p.Diff.Updates[0].Action != "update" {
		t.Fatalf("an Edited plan must map to 1 update, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastMutating {
		t.Fatalf("an update diff must be Mutating, got %v", p.Diff.BlastRadius)
	}
}

// --- destroy ----------------------------------------------------------------------

func TestNomadDestroyPlanAndApply(t *testing.T) {
	fs := &nomadFakeServer{exists: true, jobStatus: "running", groupCount: 2}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	p, err := b.DestroyPlan(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if len(p.Diff.Deletes) != 1 || !p.Diff.Deletes[0].Destructive {
		t.Fatalf("destroy of an existing job must be 1 Destructive delete, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("a delete diff must be Destructive, got %v", p.Diff.BlastRadius)
	}
	if p.Intent != IntentDestroy {
		t.Fatalf("destroy plan intent = %v, want IntentDestroy", p.Intent)
	}

	res, err := b.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("destroy apply err = %v", err)
	}
	if len(fs.deleted) != 1 {
		t.Fatalf("destroy apply must DELETE exactly once, got %d", len(fs.deleted))
	}
	if !strings.Contains(fs.deleted[0], "purge=true") {
		t.Fatalf("destroy must purge the job, DELETE url = %q", fs.deleted[0])
	}
	if len(res.Applied) != 1 || res.Applied[0].Action != "delete" {
		t.Fatalf("destroy result must report the delete, got %+v", res.Applied)
	}
}

func TestNomadDestroyPlanNoopWhenAbsent(t *testing.T) {
	fs := &nomadFakeServer{exists: false}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	p, err := b.DestroyPlan(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("destroy of an absent job must be an empty diff (already gone), got %+v", p.Diff)
	}
}

// --- observe ----------------------------------------------------------------------

func TestNomadObserveInSync(t *testing.T) {
	fs := &nomadFakeServer{exists: true, jobStatus: "running", groupCount: 2}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	rs, err := b.Observe(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Observable || !rs.Exists || !rs.InSync {
		t.Fatalf("a running job at the desired count must be in-sync, got %+v", rs)
	}
	if len(rs.Drift) != 0 {
		t.Fatalf("an in-sync job must report no drift, got %+v", rs.Drift)
	}
}

func TestNomadObserveDriftOnCount(t *testing.T) {
	// running but at the wrong count => drift (not in-sync)
	fs := &nomadFakeServer{exists: true, jobStatus: "running", groupCount: 1}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	rs, err := b.Observe(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Observable || rs.InSync || len(rs.Drift) != 1 {
		t.Fatalf("a count mismatch must report drift, got %+v", rs)
	}
}

func TestNomadObserveAbsent(t *testing.T) {
	fs := &nomadFakeServer{exists: false}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	rs, err := b.Observe(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Observable {
		t.Fatalf("a 404 is an OBSERVABLE absence, not a gap: %+v", rs)
	}
	if rs.Exists || rs.InSync || len(rs.Drift) != 1 || rs.Drift[0].Action != "create" {
		t.Fatalf("an absent job must report create drift, got %+v", rs)
	}
}

func TestNomadObserveUnreachableIsHonestGap(t *testing.T) {
	// a 500 on GET must be an HONEST gap (Observable:false), never a faked in-sync
	fs := &nomadFakeServer{exists: true, getStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	rs, err := b.Observe(context.Background(), nomadDesired(), mockCred())
	if err != nil {
		t.Fatalf("observe must not error on an unreachable unit, it reports a gap: %v", err)
	}
	if rs.Observable {
		t.Fatalf("an unreadable unit must be Observable:false (honest gap), got %+v", rs)
	}
	if rs.InSync {
		t.Fatalf("an unobservable unit must NEVER be reported in-sync")
	}
}

// --- credential handling ----------------------------------------------------------

func TestNomadSendsTokenHeader(t *testing.T) {
	fs := &nomadFakeServer{exists: false, planType: "Added"}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	cred := mockCred() // Token = "SECRET-TOKEN-write"
	if _, err := b.Plan(context.Background(), nomadDesired(), cred); err != nil {
		t.Fatal(err)
	}
	if len(fs.seenTokens) == 0 {
		t.Fatalf("no requests were recorded")
	}
	for i, tok := range fs.seenTokens {
		if tok != cred.Token {
			t.Fatalf("request %d (%s) carried X-Nomad-Token %q, want the minted token", i, fs.methods[i], tok)
		}
	}
}

// TestNomadTokenNeverInReturnedStructs is the load-bearing security assertion: the
// credential MATERIAL must never appear in any returned Plan/Result/RealState.
func TestNomadTokenNeverInReturnedStructs(t *testing.T) {
	cred := mockCred()
	token := cred.Token
	if token == "" {
		t.Fatalf("test precondition: cred must have a token")
	}

	// create + apply
	fsCreate := &nomadFakeServer{exists: false, planType: "Added"}
	srvC := httptest.NewServer(fsCreate.handler())
	defer srvC.Close()
	bC := nomadTestBackend(t, srvC)
	pc, err := bC.Plan(context.Background(), nomadDesired(), cred)
	if err != nil {
		t.Fatal(err)
	}
	nomadAssertNoToken(t, "create plan", pc, token)
	rc, err := bC.Apply(context.Background(), pc, cred)
	if err != nil {
		t.Fatal(err)
	}
	nomadAssertNoTokenResult(t, "create result", rc, token)

	// destroy + apply
	fsDel := &nomadFakeServer{exists: true, jobStatus: "running", groupCount: 2}
	srvD := httptest.NewServer(fsDel.handler())
	defer srvD.Close()
	bD := nomadTestBackend(t, srvD)
	pd, err := bD.DestroyPlan(context.Background(), nomadDesired(), cred)
	if err != nil {
		t.Fatal(err)
	}
	nomadAssertNoToken(t, "destroy plan", pd, token)
	rd, err := bD.Apply(context.Background(), pd, cred)
	if err != nil {
		t.Fatal(err)
	}
	nomadAssertNoTokenResult(t, "destroy result", rd, token)

	// observe
	rs, err := bD.Observe(context.Background(), nomadDesired(), cred)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rs.Drift {
		if strings.Contains(it.Ref+it.Detail+it.Kind, token) {
			t.Fatalf("observe: credential material leaked into drift item %+v", it)
		}
	}
	if strings.Contains(rs.Detail, token) {
		t.Fatalf("observe: credential material leaked into RealState.Detail")
	}
}

// nomadAssertNoToken checks a Plan (and its handle/diff) carries no credential material.
func nomadAssertNoToken(t *testing.T, label string, p Plan, token string) {
	t.Helper()
	if strings.Contains(p.Handle, token) {
		t.Fatalf("%s: credential material leaked into Plan.Handle", label)
	}
	if strings.Contains(p.Diff.Summary+p.Diff.RollbackHint, token) {
		t.Fatalf("%s: credential material leaked into Diff summary/hint", label)
	}
	for _, it := range p.Diff.Items() {
		if strings.Contains(it.Ref+it.Detail+it.Kind, token) {
			t.Fatalf("%s: credential material leaked into change item %+v", label, it)
		}
	}
}

func nomadAssertNoTokenResult(t *testing.T, label string, r Result, token string) {
	t.Helper()
	if strings.Contains(r.Detail+r.BackendID+r.CredentialID, token) {
		t.Fatalf("%s: credential material leaked into Result meta", label)
	}
	for _, it := range r.Applied {
		if strings.Contains(it.Ref+it.Detail+it.Kind, token) {
			t.Fatalf("%s: credential material leaked into applied item %+v", label, it)
		}
	}
}

// --- end-to-end through the governed Executor -------------------------------------

// TestNomadCreateThroughExecutorE2E wires the backend behind the Executor and proves an
// additive create flows through the blast-radius gate and registers under a WRITE cred.
func TestNomadCreateThroughExecutorE2E(t *testing.T) {
	fs := &nomadFakeServer{exists: false, planType: "Added"}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	e := New(WithBackend(b), WithCredentialSource(mockCredSource(time.Hour)))
	res, err := e.Apply(context.Background(), nomadDesired())
	if err != nil {
		t.Fatalf("e2e apply err = %v", err)
	}
	if res.BackendID != "nomad" {
		t.Fatalf("result backend id = %q, want nomad", res.BackendID)
	}
	if !strings.HasSuffix(res.CredentialID, ":write") {
		t.Fatalf("apply must use a WRITE-scoped credential, got id %q", res.CredentialID)
	}
	if len(fs.registered) != 1 {
		t.Fatalf("e2e apply must register the job once, got %d", len(fs.registered))
	}
}

// TestNomadDeleteBlockedByGateE2E proves a plan whose native diff is a delete is
// blocked by the blast-radius gate before any register/delete reaches the API.
func TestNomadDeleteBlockedByGateE2E(t *testing.T) {
	// An existing job whose plan reports Deleted (the rare "job would be removed" apply).
	fs := &nomadFakeServer{exists: true, jobStatus: "running", groupCount: 2, planType: "Deleted"}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	b := nomadTestBackend(t, srv)

	e := New(WithBackend(b), WithCredentialSource(mockCredSource(time.Hour)))
	_, err := e.Apply(context.Background(), nomadDesired())
	if err == nil {
		t.Fatalf("a destructive apply must be blocked by the blast-radius gate")
	}
	if len(fs.registered) != 0 || len(fs.deleted) != 0 {
		t.Fatalf("a gate-blocked plan must never reach register/delete")
	}
}

// TestNomadBadCABundleFailsClosed proves a malformed CA bundle is surfaced (the backend
// never silently runs without a pinned server) rather than panicking at construction.
func TestNomadBadCABundleFailsClosed(t *testing.T) {
	b := NewNomadBackend(NomadConfig{BaseURL: "https://nomad.invalid:4646", CABundle: []byte("not a pem")})
	_, err := b.Plan(context.Background(), nomadDesired(), mockCred())
	if err == nil {
		t.Fatalf("a malformed CA bundle must surface an error on use, not run unpinned")
	}
}

// TestNomadRollbackHonestLimitation proves rollback reports its honest limitation
// rather than faking a state operation.
func TestNomadRollbackHonestLimitation(t *testing.T) {
	b := NewNomadBackend(NomadConfig{BaseURL: "https://nomad.internal:4646"})
	_, err := b.Rollback(context.Background(), Plan{Runtime: nomadKind}, mockCred())
	if err == nil {
		t.Fatalf("nomad rollback must honestly report it is a re-register, not silently succeed")
	}
}
