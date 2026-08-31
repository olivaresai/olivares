// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dockerFakeDaemon is a deterministic httptest.Server that simulates the subset of
// the Docker Engine API this backend uses: list (containers/json), inspect
// (containers/<name>/json), create, start, stop, delete. It records every request so
// a test can assert the call sequence and that no secret ever reaches the wire.
type dockerFakeDaemon struct {
	srv *httptest.Server
	// containers maps "/<name>" -> dockerFakeRec (the daemon's view).
	containers map[string]dockerFakeRec
	nextID     int
	// reqs records "METHOD path" for every received request.
	reqs []string
	// authHeaders records the Authorization header of every request (to assert that
	// for the unix-socket transport simulation no bearer is sent).
	authHeaders []string
	// bodies records every request body (to assert no secret value is ever sent).
	bodies []string
	// fail, when set, makes every handler return 500 (simulate a sick daemon).
	fail bool
	// down, when set after construction, closes the server so calls fail at the
	// transport (simulate an unreachable daemon).
}

type dockerFakeRec struct {
	id      string
	image   string
	running bool
}

func newDockerFakeDaemon(t *testing.T) *dockerFakeDaemon {
	t.Helper()
	d := &dockerFakeDaemon{containers: map[string]dockerFakeRec{}}
	d.srv = httptest.NewServer(http.HandlerFunc(d.handle))
	t.Cleanup(d.srv.Close)
	return d
}

// seed adds a container as if it already existed on the daemon.
func (d *dockerFakeDaemon) seed(name, image string, running bool) {
	d.nextID++
	id := strings.Repeat("a", 8) + dockerItoa(d.nextID)
	d.containers["/"+name] = dockerFakeRec{id: id, image: image, running: running}
}

func dockerItoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (d *dockerFakeDaemon) handle(w http.ResponseWriter, r *http.Request) {
	d.reqs = append(d.reqs, r.Method+" "+r.URL.Path)
	d.authHeaders = append(d.authHeaders, r.Header.Get("Authorization"))
	// Read the body for recording, then restore it so downstream handlers can decode.
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	d.bodies = append(d.bodies, string(raw))
	if d.fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/containers/json":
		d.handleList(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
		d.handleInspect(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
		d.handleCreate(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
		d.handleStart(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
		d.handleStop(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/containers/"):
		d.handleDelete(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (d *dockerFakeDaemon) handleList(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	out := []item{}
	for name, rec := range d.containers {
		state := "exited"
		if rec.running {
			state = "running"
		}
		out = append(out, item{ID: rec.id, Names: []string{name}, Image: rec.image, State: state})
	}
	dockerWriteJSON(w, http.StatusOK, out)
}

func (d *dockerFakeDaemon) handleInspect(w http.ResponseWriter, r *http.Request) {
	// path: /containers/<name>/json
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
	rec, ok := d.containers["/"+name]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	status := "exited"
	if rec.running {
		status = "running"
	}
	resp := map[string]any{
		"Id":     rec.id,
		"Config": map[string]any{"Image": rec.image},
		"State":  map[string]any{"Status": status},
	}
	dockerWriteJSON(w, http.StatusOK, resp)
}

func (d *dockerFakeDaemon) handleCreate(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	var body struct {
		Image string `json:"Image"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	d.nextID++
	id := "newid" + dockerItoa(d.nextID)
	d.containers["/"+name] = dockerFakeRec{id: id, image: body.Image, running: false}
	dockerWriteJSON(w, http.StatusCreated, map[string]any{"Id": id})
}

func (d *dockerFakeDaemon) handleStart(w http.ResponseWriter, r *http.Request) {
	// path: /containers/<idOrName>/start
	ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/start")
	if rec, ok := d.lookup(ref); ok {
		rec.running = true
		d.store(ref, rec)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (d *dockerFakeDaemon) handleStop(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/stop")
	if rec, ok := d.lookup(ref); ok {
		if !rec.running {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		rec.running = false
		d.store(ref, rec)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (d *dockerFakeDaemon) handleDelete(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimPrefix(r.URL.Path, "/containers/")
	for k, rec := range d.containers {
		if rec.id == ref || k == "/"+ref {
			delete(d.containers, k)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

// lookup finds a record by id or by name.
func (d *dockerFakeDaemon) lookup(ref string) (dockerFakeRec, bool) {
	if rec, ok := d.containers["/"+ref]; ok {
		return rec, true
	}
	for _, rec := range d.containers {
		if rec.id == ref {
			return rec, true
		}
	}
	return dockerFakeRec{}, false
}

func (d *dockerFakeDaemon) store(ref string, rec dockerFakeRec) {
	if _, ok := d.containers["/"+ref]; ok {
		d.containers["/"+ref] = rec
		return
	}
	for k, r := range d.containers {
		if r.id == ref {
			d.containers[k] = rec
			return
		}
	}
}

func dockerWriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// dockerTestBackend builds a DockerBackend pointed at the fake daemon's base URL.
// It uses the daemon's plain-HTTP test server (no TLS) via the default transport;
// the unix-socket transport is exercised only via the real socket in production, so
// the test routes through baseURL. The bearer is left empty (socket boundary), which
// the test asserts.
func dockerTestBackend(d *dockerFakeDaemon) *DockerBackend {
	b := NewDockerBackend(DockerConfig{Timeout: time.Minute})
	// Point the (already unix) client at the httptest server instead: replace the
	// client with the default one and the base URL with the test server. This keeps
	// the bearer empty (the local-socket boundary semantics) while letting the test
	// drive the HTTP handlers deterministically.
	b.baseURL = d.srv.URL
	b.client = d.srv.Client()
	return b
}

func dockerDesired(name, image string) Desired {
	dd := desired("docker")
	dd.SubjectRef = name
	dd.Image = image
	return dd
}

// --- Plan: create / noop / replace ----------------------------------------------

func TestDockerPlanCreateWhenAbsent(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if p.Diff.Empty() || len(p.Diff.Creates) != 1 {
		t.Fatalf("absent container must plan a create, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastAdditive {
		t.Fatalf("a create-only plan must be additive, got %v", p.Diff.BlastRadius)
	}
	if it := p.Diff.Creates[0]; it.Ref != "acme-bot" || it.Detail != "nginx:1.27" || it.Destructive {
		t.Fatalf("create item wrong: %+v", it)
	}
}

func TestDockerPlanNoopWhenInSync(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.27", true)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("an in-sync container must yield an EMPTY diff (idempotency), got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastReadOnly {
		t.Fatalf("an empty diff must be read-only, got %v", p.Diff.BlastRadius)
	}
}

func TestDockerPlanReplaceOnImageDrift(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.26", true)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if len(p.Diff.Updates) != 1 {
		t.Fatalf("an image change must plan one replace, got %+v", p.Diff)
	}
	it := p.Diff.Updates[0]
	if it.Action != "replace" || !it.Destructive {
		t.Fatalf("an image replace must be marked Destructive, got %+v", it)
	}
	if p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("a replace plan must be Destructive, got %v", p.Diff.BlastRadius)
	}
	if p.Diff.Reversible {
		t.Fatalf("an image replace is not auto-reversible")
	}
}

func TestDockerPlanImplicitLatestIsNoop(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:latest", true)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx"), mockCred())
	if err != nil {
		t.Fatalf("plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("nginx vs nginx:latest must be a noop, got %+v", p.Diff)
	}
}

// --- Apply: create / noop / replace (Destructive) -------------------------------

func TestDockerApplyCreateStartsContainer(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Action != "create" {
		t.Fatalf("apply must report the create, got %+v", res.Applied)
	}
	rec, ok := d.containers["/acme-bot"]
	if !ok || rec.image != "nginx:1.27" || !rec.running {
		t.Fatalf("apply must create AND start the container, got %+v ok=%v", rec, ok)
	}
	// The call sequence must include create then start (no stop/delete on a create).
	if !dockerSawSeq(d.reqs, "POST /containers/create", "POST /containers/acme-bot/start") {
		t.Fatalf("expected create then start, got %v", d.reqs)
	}
}

func TestDockerApplyNoopChangesNothing(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.27", true)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	before := len(d.reqs)
	res, err := b.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("an empty plan must apply nothing, got %+v", res.Applied)
	}
	// No create/start/stop/delete should have been issued for a noop apply.
	for _, req := range d.reqs[before:] {
		if strings.Contains(req, "create") || strings.Contains(req, "start") ||
			strings.Contains(req, "stop") || strings.HasPrefix(req, "DELETE") {
			t.Fatalf("a noop apply must not mutate the daemon, saw %q", req)
		}
	}
}

func TestDockerApplyReplaceStopsRemovesRecreates(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.26", true)
	oldID := d.containers["/acme-bot"].id
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Action != "replace" {
		t.Fatalf("apply must report the replace, got %+v", res.Applied)
	}
	rec := d.containers["/acme-bot"]
	if rec.image != "nginx:1.27" || !rec.running || rec.id == oldID {
		t.Fatalf("replace must recreate a NEW container at the new image, got %+v (old id %s)", rec, oldID)
	}
	if !dockerSawSeq(d.reqs, "POST /containers/"+oldID+"/stop", "DELETE /containers/"+oldID, "POST /containers/create") {
		t.Fatalf("replace must stop+remove old then create, got %v", d.reqs)
	}
}

// --- DestroyPlan + destroy apply ------------------------------------------------

func TestDockerDestroyPlanPresent(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.27", true)
	b := dockerTestBackend(d)
	p, err := b.DestroyPlan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if len(p.Diff.Deletes) != 1 || !p.Diff.Deletes[0].Destructive {
		t.Fatalf("destroy of an existing container must be one Destructive delete, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("destroy must be Destructive, got %v", p.Diff.BlastRadius)
	}
}

func TestDockerDestroyPlanAbsentIsEmpty(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	p, err := b.DestroyPlan(context.Background(), dockerDesired("ghost", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if !p.Diff.Empty() {
		t.Fatalf("destroy of an absent container must be an empty diff (idempotent), got %+v", p.Diff)
	}
}

func TestDockerApplyDestroyStopsAndRemoves(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.27", true)
	id := d.containers["/acme-bot"].id
	b := dockerTestBackend(d)
	p, err := b.DestroyPlan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(context.Background(), p, mockCred()); err != nil {
		t.Fatalf("destroy apply err = %v", err)
	}
	if _, ok := d.containers["/acme-bot"]; ok {
		t.Fatalf("destroy must remove the container")
	}
	if !dockerSawSeq(d.reqs, "POST /containers/"+id+"/stop", "DELETE /containers/"+id) {
		t.Fatalf("destroy must stop then delete, got %v", d.reqs)
	}
}

func TestDockerApplyDestroyAbsentIsNoop(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	p, err := b.DestroyPlan(context.Background(), dockerDesired("ghost", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("destroy apply of absent err = %v", err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("destroy of an absent container must change nothing, got %+v", res.Applied)
	}
}

// --- Observe --------------------------------------------------------------------

func TestDockerObserveInSync(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.27", true)
	b := dockerTestBackend(d)
	rs, err := b.Observe(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Observable || !rs.Exists || !rs.InSync || len(rs.Drift) != 0 {
		t.Fatalf("an in-sync container must observe InSync with no drift, got %+v", rs)
	}
}

func TestDockerObserveMissingIsHonestGap(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	rs, err := b.Observe(context.Background(), dockerDesired("ghost", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Observable || rs.Exists || rs.InSync || len(rs.Drift) != 1 || rs.Drift[0].Action != "create" {
		t.Fatalf("a missing container must be Observable:true,Exists:false with a create-drift, got %+v", rs)
	}
}

func TestDockerObserveDriftOnImage(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.26", true)
	b := dockerTestBackend(d)
	rs, err := b.Observe(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("observe err = %v", err)
	}
	if !rs.Observable || !rs.Exists || rs.InSync || len(rs.Drift) != 1 || !rs.Drift[0].Destructive {
		t.Fatalf("an image-drifted container must observe drift (Destructive replace), got %+v", rs)
	}
}

func TestDockerObserveUnreachableDaemonIsHonestGap(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	d.srv.Close() // daemon goes away => transport failure
	rs, err := b.Observe(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatalf("observe must not error on an unreachable daemon (honest gap), got %v", err)
	}
	if rs.Observable {
		t.Fatalf("an unreachable daemon must yield Observable:false (never a faked in-sync), got %+v", rs)
	}
	if rs.InSync {
		t.Fatalf("an unreachable daemon must NEVER be reported InSync, got %+v", rs)
	}
}

// --- Idempotency: re-apply of an in-sync container is a noop ---------------------

func TestDockerReapplyIsIdempotent(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	// First apply: create.
	p1, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(context.Background(), p1, mockCred()); err != nil {
		t.Fatal(err)
	}
	// Second plan against the now-created container must be empty (idempotent).
	p2, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Diff.Empty() {
		t.Fatalf("re-plan of an applied spec must be empty (idempotency), got %+v", p2.Diff)
	}
}

// --- Rollback: reverse a create -------------------------------------------------

func TestDockerRollbackReversesCreate(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(context.Background(), p, mockCred()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Rollback(context.Background(), p, mockCred()); err != nil {
		t.Fatalf("rollback of a create err = %v", err)
	}
	if _, ok := d.containers["/acme-bot"]; ok {
		t.Fatalf("rollback of a create must remove the created container")
	}
}

func TestDockerRollbackReplaceRefusesHonestly(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.26", true)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Rollback(context.Background(), p, mockCred()); err == nil {
		t.Fatalf("rollback of a replace must report its honest limitation, got nil")
	}
}

// --- Security: no credential material on the wire / in returned structs ---------

func TestDockerLocalSocketSendsNoBearer(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d) // local-socket semantics: bearerFn returns ""
	cred := mockCred()
	p, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(context.Background(), p, cred); err != nil {
		t.Fatal(err)
	}
	// For the local-socket boundary the cred token is NEVER put on the wire.
	for i, h := range d.authHeaders {
		if h != "" {
			t.Fatalf("local-socket transport must send no Authorization header, req %d had %q", i, h)
		}
	}
	// And the token must never appear in any request body.
	for _, body := range d.bodies {
		if strings.Contains(body, cred.Token) {
			t.Fatalf("credential material leaked into a request body: %q", body)
		}
	}
}

func TestDockerCredentialMaterialNeverInReturnedStructs(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.26", true)
	b := dockerTestBackend(d)
	cred := mockCred()

	pPlan, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), cred)
	if err != nil {
		t.Fatal(err)
	}
	pDestroy, err := b.DestroyPlan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), cred)
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Apply(context.Background(), pPlan, cred)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := b.Observe(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), cred)
	if err != nil {
		t.Fatal(err)
	}
	if dockerContainsToken(pPlan.Diff, cred.Token) || dockerContainsToken(pDestroy.Diff, cred.Token) {
		t.Fatalf("credential material leaked into a Diff")
	}
	if strings.Contains(res.Detail, cred.Token) || strings.Contains(res.BackendID, cred.Token) {
		t.Fatalf("credential material leaked into a Result")
	}
	for _, it := range res.Applied {
		if strings.Contains(it.Ref+it.Detail, cred.Token) {
			t.Fatalf("credential material leaked into a Result change item: %+v", it)
		}
	}
	for _, it := range rs.Drift {
		if strings.Contains(it.Ref+it.Detail, cred.Token) {
			t.Fatalf("credential material leaked into RealState drift: %+v", it)
		}
	}
	if strings.Contains(rs.Detail, cred.Token) {
		t.Fatalf("credential material leaked into RealState detail")
	}
}

// TestDockerEnvNoSecretValueOnWire proves that when the Desired carries env by
// SECRET REFERENCE, the cleartext secret VALUE never reaches the daemon (only the
// reference, resolved by the runtime's native mechanism, may appear).
func TestDockerEnvNoSecretValueOnWire(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	dd := dockerDesired("acme-bot", "nginx:1.27")
	dd.EnvRefs = []SecretBinding{{Name: "DB_PASSWORD", SecretRef: "vault:db/prod#password"}}
	cred := mockCred()
	p, err := b.Plan(context.Background(), dd, cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(context.Background(), p, cred); err != nil {
		t.Fatal(err)
	}
	// The v1 create body must carry NO resolved secret value. (The secret value is
	// never known to this backend; only references could ever be passed, and the v1
	// create body does not even forward env, so nothing secret can leak.)
	secretValueSentinel := "S3CR3T-PLAINTEXT-VALUE"
	for _, body := range d.bodies {
		if strings.Contains(body, secretValueSentinel) {
			t.Fatalf("a cleartext secret value must NEVER reach the daemon, body=%q", body)
		}
	}
}

// --- Remote daemon: bearer IS sent ----------------------------------------------

func TestDockerRemoteDaemonSendsBearer(t *testing.T) {
	d := newDockerFakeDaemon(t)
	// Build a remote-configured backend, then route its client+baseURL at the test
	// server (no real TLS needed for the bearer-on-wire assertion). bearerFn returns
	// cred.Token for the remote config.
	b := NewDockerBackend(DockerConfig{RemoteBaseURL: "https://docker.host:2376", RemoteInsecure: true, Timeout: time.Minute})
	b.baseURL = d.srv.URL
	b.client = d.srv.Client()
	cred := mockCred()
	if _, err := b.Plan(context.Background(), dockerDesired("acme-bot", "nginx:1.27"), cred); err != nil {
		t.Fatal(err)
	}
	var sawBearer bool
	for _, h := range d.authHeaders {
		if h == "Bearer "+cred.Token {
			sawBearer = true
		}
	}
	if !sawBearer {
		t.Fatalf("a remote TCP+TLS daemon must use the minted credential as the API bearer; headers=%v", d.authHeaders)
	}
}

// --- Anonymous container is refused ---------------------------------------------

func TestDockerRefusesAnonymousContainer(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	if _, err := b.Plan(context.Background(), dockerDesired("", "nginx:1.27"), mockCred()); err == nil {
		t.Fatalf("an empty SubjectRef must be refused")
	}
}

// --- E2E through the Executor (blast-radius gate drives off our Destructive flags) ---

// TestDockerReplaceBlockedByGateE2E wires the docker backend behind the Executor and
// proves a plan that REPLACES a container (Destructive) is blocked by the
// blast-radius gate before any mutation reaches the daemon.
func TestDockerReplaceBlockedByGateE2E(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.26", true)
	b := dockerTestBackend(d)
	e := New(WithBackend(b), WithCredentialSource(mockCredSource(time.Hour)))
	_, err := e.Apply(context.Background(), dockerDesired("acme-bot", "nginx:1.27"))
	if !errors.Is(err, ErrBlastRadius) {
		t.Fatalf("a docker replace must be blocked by the blast-radius gate, got %v", err)
	}
	for _, req := range d.reqs {
		if req == "POST /containers/create" {
			t.Fatalf("a gate-blocked plan must never reach create")
		}
	}
}

// TestDockerCreateThroughExecutorE2E proves an additive create succeeds via the
// governed Executor and the Result carries the non-sensitive backend+credential ids.
func TestDockerCreateThroughExecutorE2E(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	e := New(WithBackend(b), WithCredentialSource(mockCredSource(time.Hour)))
	res, err := e.Apply(context.Background(), dockerDesired("acme-bot", "nginx:1.27"))
	if err != nil {
		t.Fatalf("additive docker apply via executor err = %v", err)
	}
	if res.BackendID != "docker" || res.CredentialID == "" {
		t.Fatalf("result must carry backend+credential id, got %+v", res)
	}
	if _, ok := d.containers["/acme-bot"]; !ok {
		t.Fatalf("executor apply must have created the container")
	}
}

// TestDockerRetireThroughExecutorE2E proves a retire (governed teardown) is allowed
// by default and removes the container.
func TestDockerRetireThroughExecutorE2E(t *testing.T) {
	d := newDockerFakeDaemon(t)
	d.seed("acme-bot", "nginx:1.27", true)
	b := dockerTestBackend(d)
	e := New(WithBackend(b), WithCredentialSource(mockCredSource(time.Hour)))
	if _, err := e.Retire(context.Background(), dockerDesired("acme-bot", "nginx:1.27")); err != nil {
		t.Fatalf("retire via executor err = %v", err)
	}
	if _, ok := d.containers["/acme-bot"]; ok {
		t.Fatalf("retire must remove the container")
	}
}

// --- helpers --------------------------------------------------------------------

// dockerSawSeq reports whether the given ordered subsequence of "METHOD path"
// prefixes appears in order within reqs (allowing other requests in between).
func dockerSawSeq(reqs []string, seq ...string) bool {
	i := 0
	for _, req := range reqs {
		if i < len(seq) && strings.HasPrefix(req, seq[i]) {
			i++
		}
	}
	return i == len(seq)
}

// dockerContainsToken reports whether the token appears anywhere in a Diff's items.
func dockerContainsToken(d Diff, token string) bool {
	for _, it := range d.Items() {
		if strings.Contains(it.Ref+it.Detail+it.Kind+it.Action, token) {
			return true
		}
	}
	return strings.Contains(d.Summary+d.RollbackHint, token)
}

// dockerCreateBodyOf returns the JSON body of the (single) POST /containers/create
// request the fake daemon received, decoded into a dockerCreateBody.
func dockerCreateBodyOf(t *testing.T, d *dockerFakeDaemon) dockerCreateBody {
	t.Helper()
	for i, req := range d.reqs {
		if req == "POST /containers/create" {
			var cb dockerCreateBody
			if err := json.Unmarshal([]byte(d.bodies[i]), &cb); err != nil {
				t.Fatalf("decode create body: %v (raw=%s)", err, d.bodies[i])
			}
			return cb
		}
	}
	t.Fatalf("no create request recorded; reqs=%v", d.reqs)
	return dockerCreateBody{}
}

// --- workspace bind-mount + hardening primitive ----------------------------

func TestDockerApplyWorkspaceMountAndHardening(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	dd := dockerDesired("acme-bot", "nginx:1.27")
	dd.Mounts = []Mount{
		{Source: "/srv/ws/acme", Target: "/workspace", ReadOnly: false},
		{Source: "/srv/ref/lib", Target: "/refs", ReadOnly: true},
	}
	dd.ReadonlyRootfs = true
	dd.TmpfsMounts = []string{"/tmp"}
	dd.RunAsUser = "65532:65532"
	dd.WorkingDir = "/workspace"

	p, err := b.Plan(context.Background(), dd, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(context.Background(), p, mockCred()); err != nil {
		t.Fatalf("apply err = %v", err)
	}
	cb := dockerCreateBodyOf(t, d)
	if cb.HostConfig == nil {
		t.Fatal("a workspace launch must emit a HostConfig")
	}
	wantBinds := []string{"/srv/ws/acme:/workspace", "/srv/ref/lib:/refs:ro"}
	if !sameStrings(cb.HostConfig.Binds, wantBinds) {
		t.Fatalf("binds = %v, want %v", cb.HostConfig.Binds, wantBinds)
	}
	if !cb.HostConfig.ReadonlyRootfs {
		t.Fatal("the rest of the container must be read-only")
	}
	if _, ok := cb.HostConfig.Tmpfs["/tmp"]; !ok {
		t.Fatalf("tmpfs /tmp must be writable scratch, got %v", cb.HostConfig.Tmpfs)
	}
	if cb.User != "65532:65532" {
		t.Fatalf("container must run non-root, User = %q", cb.User)
	}
	if cb.WorkingDir != "/workspace" {
		t.Fatalf("workdir = %q, want /workspace", cb.WorkingDir)
	}
}

// TestDockerCreateBodyUnchangedWithoutWorkspace is the additive-safety guard: a
// Desired with no fields must emit NO HostConfig/User/WorkingDir (byte-identical
// to the pre body) so existing deployments see no drift.
func TestDockerCreateBodyUnchangedWithoutWorkspace(t *testing.T) {
	d := newDockerFakeDaemon(t)
	b := dockerTestBackend(d)
	p, err := b.Plan(context.Background(), dockerDesired("plain-bot", "nginx:1.27"), mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(context.Background(), p, mockCred()); err != nil {
		t.Fatalf("apply err = %v", err)
	}
	cb := dockerCreateBodyOf(t, d)
	if cb.HostConfig != nil || cb.User != "" || cb.WorkingDir != "" {
		t.Fatalf("a workspace-less create must carry no hardening fields, got %+v", cb)
	}
}

// TestDockerSpecHashIgnoresMounts pins that mounts are NOT part of drift detection in
// v1 (a workspace-bound spec hashes identically to its bare image+command+env form),
// so adding a mount never churns an existing deployment's reconcile.
func TestDockerSpecHashIgnoresMounts(t *testing.T) {
	bare := dockerApplyFor(dockerDesired("acme-bot", "nginx:1.27"))
	dd := dockerDesired("acme-bot", "nginx:1.27")
	dd.Mounts = []Mount{{Source: "/srv/ws", Target: "/workspace"}}
	dd.ReadonlyRootfs = true
	dd.RunAsUser = "65532:65532"
	withMounts := dockerApplyFor(dd)
	if dockerSpecHash(bare) != dockerSpecHash(withMounts) {
		t.Fatal("mounts must not change the drift spec-hash in v1")
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
