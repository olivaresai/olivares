// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// resolveBoundActor is what the communication kernel does with a step's actor
// before any effect: core/auth resolves the run's binding for the run and its
// account to the exact credential it pins, and that credential must still be
// current. The sessions production composition is exercised in modules/sessions;
// here the doubles use the same core/auth calls so the run's gate, publication
// and receipts are measured against the real Authenticator.
func resolveBoundActor(ctx context.Context, authr *auth.Authenticator, tenant model.TenantID, actor WorkActor) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ref, err := authr.ResolveCredentialBinding(ctx, actor.CredentialBinding, auth.CredentialBindingSubject{
		Tenant: tenant, Kind: auth.CredentialBindingWorkflowRun, Ref: actor.RunID, User: actor.UserIdentity,
	})
	if errors.Is(err, auth.ErrCredentialBindingInvalid) {
		return fmt.Errorf("%w: binding does not resolve", ErrWorkflowReauthenticationRequired)
	}
	if err != nil {
		return err
	}
	if _, err := authr.ResolvePrincipalScope(ctx, ref, tenant); err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			return fmt.Errorf("%w: credential is no longer current", ErrWorkflowReauthenticationRequired)
		}
		return err
	}
	return nil
}

// boundMessageControl is a communication kernel double with durable receipts:
// a repeated idempotency key replays the committed result and adds no effect.
type boundMessageControl struct {
	authr    *auth.Authenticator
	mu       sync.Mutex
	requests []WorkMessageRequest
	receipts map[string]WorkMessageResult
	effects  int
	deny     bool
}

func (c *boundMessageControl) SendWorkMessage(ctx context.Context, tenant model.TenantID, req WorkMessageRequest) (WorkMessageResult, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	if err := resolveBoundActor(ctx, c.authr, tenant, req.Actor); err != nil {
		return WorkMessageResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deny {
		return WorkMessageResult{}, fmt.Errorf("%w: send is not granted", ErrWorkflowEffectDenied)
	}
	if result, ok := c.receipts[req.IdempotencyKey]; ok {
		return result, nil
	}
	c.effects++
	result := WorkMessageResult{
		WorkItemID: req.WorkItemID, MessageID: model.NewID(), CommandID: model.NewID(),
		EventID: model.NewID(), EventSeq: int64(10 + c.effects),
	}
	if c.receipts == nil {
		c.receipts = map[string]WorkMessageResult{}
	}
	c.receipts[req.IdempotencyKey] = result
	return result, nil
}

func (c *boundMessageControl) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// boundAckReader observes acknowledgement under the step's binding; before
// answering it may run a hook, which the effect-boundary test uses to publish a
// reauthorization while the observation is in flight.
type boundAckReader struct {
	authr   *auth.Authenticator
	mu      sync.Mutex
	queries []WorkAckQuery
	answers []bool // per call: acknowledged or pending; pending once exhausted
	hook    func()
}

func (r *boundAckReader) ObserveWorkAck(ctx context.Context, tenant model.TenantID, query WorkAckQuery) (WorkAckObservation, error) {
	r.mu.Lock()
	r.queries = append(r.queries, query)
	hook := r.hook
	r.hook = nil
	ack := false
	if len(r.answers) > 0 {
		ack, r.answers = r.answers[0], r.answers[1:]
	}
	r.mu.Unlock()
	if err := resolveBoundActor(ctx, r.authr, tenant, query.Actor); err != nil {
		return WorkAckObservation{}, err
	}
	if hook != nil {
		hook()
	}
	if !ack {
		return WorkAckObservation{Status: WorkAckPending}, nil
	}
	return WorkAckObservation{Status: WorkAckAcknowledged, AckID: model.NewID(), EventID: model.NewID(), EventSeq: 20}, nil
}

// hookedWorkControl runs onCreate inside the work-create effect, which the
// tests use to rotate the initiator's credential between two steps of one drain.
type hookedWorkControl struct {
	*recordingK4WorkControl
	onCreate func()
}

func (c *hookedWorkControl) Create(ctx context.Context, tenant model.TenantID, req WorkCreateRequest) (WorkCommandResult, error) {
	if c.onCreate != nil {
		c.onCreate()
	}
	return c.recordingK4WorkControl.Create(ctx, tenant, req)
}

type bindingRunFixture struct {
	t        *testing.T
	h        *harness
	mod      *Module
	clock    *manualClock
	gate     *routedGate
	authr    *auth.Authenticator
	root     string
	tenant   model.TenantID
	member   string
	email    string
	message  *boundMessageControl
	acks     *boundAckReader
	work     *hookedWorkControl
	logs     *bytes.Buffer
	workItem model.ID
	channel  model.ID
}

func newBindingRunFixture(t *testing.T) *bindingRunFixture {
	t.Helper()
	f := &bindingRunFixture{
		t: t, clock: newManualClock(), gate: newRoutedGate(), logs: &bytes.Buffer{},
		work:     &hookedWorkControl{recordingK4WorkControl: &recordingK4WorkControl{root: model.NewID()}},
		workItem: model.NewID(), channel: model.NewID(),
	}
	f.message = &boundMessageControl{receipts: map[string]WorkMessageResult{}}
	f.acks = &boundAckReader{}
	f.h, f.mod = newHarness(t, WithClock(f.clock), WithApprovalGate(f.gate),
		WithWorkflowWorkControl(f.work), WithWorkflowMessageControl(f.message), WithWorkflowAckReader(f.acks))
	f.authr = auth.NewAuthenticator(f.h.st, nil)
	f.message.authr, f.acks.authr = f.authr, f.authr
	f.mod.UseWorkflowCredentialBinder(f.authr)
	f.mod.log = slog.New(slog.NewTextHandler(f.logs, nil))
	f.root = f.h.adminLogin()
	f.tenant = f.h.createOrg(f.root, "binding-"+strings.ToLower(model.NewID().String()[:8]))
	f.email = "initiator-" + model.NewID().String()[:8] + "@binding.test"
	f.member = f.h.roleToken(f.root, f.tenant, f.email, auth.RoleAdmin)
	return f
}

// messageStep sends on the fixture's literal WorkItem, or on the WorkItem the
// named work-create step produced.
func (f *bindingRunFixture) messageStep(ref string, deps ...string) map[string]any {
	config := map[string]any{
		"channel_id": f.channel.String(),
		"recipient":  map[string]any{"kind": "user", "ref": model.NewID().String()},
		"body":       "continue " + ref,
	}
	if len(deps) > 0 {
		config["work_item_step_ref"] = deps[0]
	} else {
		config["work_item_id"] = f.workItem.String()
	}
	return step(ref, stepWorkMessage, config, deps...)
}

func (f *bindingRunFixture) waitStep(ref string) map[string]any {
	return step(ref, stepWorkWaitAck, map[string]any{
		"target_kind": "message", "target_id": model.NewID().String(),
		"deadline": model.NewTimestamp(time.Now().Add(24 * time.Hour)).String(),
	})
}

// start runs the workflow to phase 2 as the given caller and returns the
// workflow id, the run id and the raw phase-2 body.
func (f *bindingRunFixture) start(token string, steps ...map[string]any) (string, model.ID, string) {
	f.t.Helper()
	wf := f.h.createWorkflow(token, f.tenant, "wf-"+model.NewID().String(), steps)
	r := f.h.runToPhase2(f.gate, token, f.tenant, wf["id"].(string))
	run := r.body["run"].(map[string]any)
	return wf["id"].(string), model.ID(run["id"].(string)), r.raw
}

func (f *bindingRunFixture) row(run model.ID) (model.Record, []runStepState) {
	f.t.Helper()
	var rec model.Record
	if err := f.h.moduleCtx(f.tenant).Data.View(context.Background(), func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err = repo.Get(context.Background(), run)
		return err
	}); err != nil {
		f.t.Fatalf("read run: %v", err)
	}
	steps, err := decodeRunSteps(rec.String(colWrSteps))
	if err != nil {
		f.t.Fatal(err)
	}
	return rec, steps
}

func (f *bindingRunFixture) mutateRun(run model.ID, edit func(model.Record, []runStepState)) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.h.moduleCtx(f.tenant).Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, run)
		if err != nil {
			return err
		}
		steps, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		edit(rec, steps)
		rec[colWrSteps] = encodeRunSteps(steps)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		f.t.Fatalf("mutate run: %v", err)
	}
}

func (f *bindingRunFixture) stepOf(run model.ID, ref string) runStepState {
	f.t.Helper()
	_, steps := f.row(run)
	for _, s := range steps {
		if s.Ref == ref {
			return s
		}
	}
	f.t.Fatalf("step %s absent", ref)
	return runStepState{}
}

func (f *bindingRunFixture) requireGated(run model.ID, what string) {
	f.t.Helper()
	rec, steps := f.row(run)
	if rec.String(colWrStatus) != runStatusRunning || rec.String(colWrPaused) != pausedReauthRequired ||
		!runReauthenticationGated(rec, steps) {
		f.t.Fatalf("%s: run status %q paused %q steps %s, want a running run gated for reauthentication",
			what, rec.String(colWrStatus), rec.String(colWrPaused), encodeRunSteps(steps)+"\nlogs: "+f.logs.String())
	}
}

func (f *bindingRunFixture) handle(run model.ID) auth.CredentialBinding {
	f.t.Helper()
	rec, _ := f.row(run)
	b, err := auth.ParseCredentialBindingStorage(rec.String(colWrCredentialBinding))
	if err != nil {
		f.t.Fatalf("stored handle: %v", err)
	}
	return b
}

func (f *bindingRunFixture) principal(token string) auth.Principal {
	f.t.Helper()
	p, err := f.authr.Authenticate(context.Background(), token)
	if err != nil {
		f.t.Fatalf("authenticate: %v", err)
	}
	return p
}

func (f *bindingRunFixture) subject(run model.ID, token string) auth.CredentialBindingSubject {
	return auth.CredentialBindingSubject{
		Tenant: f.tenant, Kind: auth.CredentialBindingWorkflowRun, Ref: run, User: f.principal(token).UserID,
	}
}

func (f *bindingRunFixture) resolves(run model.ID, b auth.CredentialBinding) bool {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := f.authr.ResolveCredentialBinding(ctx, b, f.subject(run, f.member))
	return err == nil
}

// refresh rotates the initiator's session: the bound revision stops being
// current, and the refreshed bearer is the initiator's fresh credential.
func (f *bindingRunFixture) refresh() {
	f.t.Helper()
	token, _, err := f.authr.RefreshSession(context.Background(), f.principal(f.member))
	if err != nil {
		f.t.Fatalf("refresh session: %v", err)
	}
	f.member = token
}

func (f *bindingRunFixture) liveBindings(run model.ID) int {
	f.t.Helper()
	live := 0
	if err := f.h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		rows, err := as.(store.AuthCredentialBindingScope).CredentialBindings().Current(
			context.Background(), f.tenant, auth.CredentialBindingWorkflowRun, run)
		live = len(rows)
		return err
	}); err != nil {
		f.t.Fatalf("list bindings: %v", err)
	}
	return live
}

func (f *bindingRunFixture) reauthorize(token, wfID string, run model.ID, planHash string) resp {
	f.t.Helper()
	return f.h.do("POST", "/v1/m/orchestration/workflows/"+wfID+"/runs/"+run.String()+"/reauthorize",
		token, map[string]any{"plan_hash": planHash}, tenantHdr(f.tenant))
}

// reauthorizeOn calls the operation on another Module over the same store: a
// cold restart of the process that owns the run.
func (f *bindingRunFixture) reauthorizeOn(mod *Module, token, wfID string, run model.ID, planHash string) int {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"plan_hash": planHash})
	req := httptest.NewRequest("POST", "/reauthorize", bytes.NewReader(body))
	routes := chi.NewRouteContext()
	routes.URLParams.Add("id", wfID)
	routes.URLParams.Add("run", run.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routes))
	mc := api.ModuleContext{
		Tenant: f.tenant, Principal: f.principal(token), Data: api.NewScopedData(f.h.st, f.tenant),
	}
	rec := httptest.NewRecorder()
	mod.handleReauthorizeWorkflowRun(rec, req, mc)
	return rec.Code
}

func (f *bindingRunFixture) restartedModule() *Module {
	mod := New(WithClock(f.clock), WithApprovalGate(f.gate), WithWorkflowWorkControl(f.work),
		WithWorkflowMessageControl(f.message), WithWorkflowAckReader(f.acks),
		WithTargetBindingKey(NewStaticMACKey([]byte("s467-test-target-binding-key"), "test-key-1")))
	mod.UseData(api.NewModuleData(f.h.st))
	mod.UseWorkflowCredentialBinder(f.authr)
	return mod
}

func TestWorkflowRunBindsItsStartingCredential(t *testing.T) {
	f := newBindingRunFixture(t)
	wfID, run, raw := f.start(f.member, f.messageStep("msg"))
	rec, _ := f.row(run)
	if rec.String(colWrStatus) != runStatusCompleted || f.stepOf(run, "msg").Status != stepStatusMessageSent {
		t.Fatalf("bound run did not complete: %+v", rec)
	}
	b := f.handle(run)
	if b.IsZero() || !f.resolves(run, b) {
		t.Fatal("run creation stored no resolvable binding")
	}
	req := f.message.requests[0]
	if req.Actor.CredentialBinding != b || req.Actor.RunID != run {
		t.Fatal("the step actor does not carry the run's binding")
	}
	want := f.principal(f.member)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The resolved reference also names the binding it was read from; the
	// exact pair reconstructs it to the initiator's exact current session.
	got, err := f.authr.ResolveCredentialBinding(ctx, b, f.subject(run, f.member))
	if err != nil {
		t.Fatalf("binding resolves: %v", err)
	}
	if resolved, err := f.authr.ResolvePrincipalScope(ctx, got, f.tenant); err != nil ||
		resolved.Kind != want.Kind || resolved.CredID != want.CredID || resolved.UserID != want.UserID {
		t.Fatalf("binding reconstructs to %s %s, %v; want the initiator's exact session %s", resolved.Kind, resolved.CredID, err, want.CredID)
	}
	// The handle never reaches the DTO, the list, the logs or the tenant audit.
	list := f.h.do("GET", "/v1/m/orchestration/workflows/"+wfID+"/runs", f.member, nil, tenantHdr(f.tenant))
	var audit strings.Builder
	if err := f.h.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			encoded, _ := json.Marshal(e)
			audit.Write(encoded)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	for name, output := range map[string]string{
		"phase-2 DTO": raw, "run list": list.raw, "logs": f.logs.String(), "tenant audit": audit.String(),
	} {
		if strings.Contains(output, b.StorageValue()) {
			t.Fatalf("the handle reached the %s", name)
		}
	}
	for _, field := range []string{"credential_binding", "actor_credential_binding"} {
		if strings.Contains(raw, field) {
			t.Fatalf("the run DTO projects %s", field)
		}
	}

	// A caller that cannot be bound (the global superadmin) starts a run with no
	// binding; its communication step pauses the run instead of acting.
	_, unbound, _ := f.start(f.root, f.messageStep("msg"))
	if !f.handle(unbound).IsZero() {
		t.Fatal("an unbindable caller's run holds a binding")
	}
	f.requireGated(unbound, "run without a binding")
	if s := f.stepOf(unbound, "msg"); s.Status != stepStatusReauthRequired || s.ReauthResume != stepStatusPending {
		t.Fatalf("unbound step = %+v", s)
	}
	if f.message.effects != 1 {
		t.Fatalf("effects = %d, want only the bound run's", f.message.effects)
	}
}

func TestReauthenticationPauseSurvivesAdvance(t *testing.T) {
	f := newBindingRunFixture(t)
	// The initiator's credential rotates inside the work-create effect; the two
	// message steps of the same drain are refused and the run pauses, while the
	// unrelated work-create keeps its progress.
	stale := "late"
	wfID, run, _ := f.startPausedAfterRefresh()
	f.requireGated(run, "after the refused effect")
	if s := f.stepOf(run, "create"); s.Status != stepStatusWorkApplied {
		t.Fatalf("unrelated step did not keep its progress: %+v", s)
	}
	for _, ref := range []string{"msg", stale} {
		if s := f.stepOf(run, ref); s.Status != stepStatusReauthRequired || s.ReauthResume != stepStatusPending {
			t.Fatalf("step %s = %+v, want reauthentication_required", ref, s)
		}
	}
	// An uncertain claim: the late step committed its effect in an earlier
	// process that died before recording it.
	lateKey := ""
	f.mutateRun(run, func(rec model.Record, steps []runStepState) {
		for i := range steps {
			if steps[i].Ref == stale {
				steps[i].Status, steps[i].ReauthResume = stepStatusExecuting, ""
				steps[i].At = f.clock.Now().String()
				lateKey = workStepIdempotency(rec, steps[i])
			}
		}
	})
	committed := WorkMessageResult{
		WorkItemID: f.work.root, MessageID: model.NewID(), CommandID: model.NewID(),
		EventID: model.NewID(), EventSeq: 99,
	}
	f.message.receipts[lateKey] = committed
	calls := f.message.calls()

	ctx := context.Background()
	mc := f.h.moduleCtx(f.tenant)
	for i := 0; i < 3; i++ {
		f.clock.advance(executingTimeout + time.Second)
		f.mod.AdvanceWorkflowRuns(ctx, mc)
	}
	f.requireGated(run, "after pump ticks")
	f.restartedModule().AdvanceWorkflowRuns(ctx, mc)
	f.requireGated(run, "after a cold restart")
	if f.message.calls() != calls {
		t.Fatalf("a gated run dispatched %d effects", f.message.calls()-calls)
	}
	if s := f.stepOf(run, stale); s.Status != stepStatusExecuting {
		t.Fatalf("the uncertain claim was reset while gated: %+v", s)
	}

	// Only the owning continuation clears the gate. It resumes the refused step
	// with its original key and recovers the uncertain claim from its receipt.
	rec, _ := f.row(run)
	effects := f.message.effects
	if r := f.reauthorize(f.member, wfID, run, rec.String(colWrPlanHash)); r.code != http.StatusOK {
		t.Fatalf("reauthorize = %d %s", r.code, r.raw)
	}
	f.clock.advance(executingTimeout + time.Second)
	f.mod.AdvanceWorkflowRuns(ctx, mc)
	rec, _ = f.row(run)
	if rec.String(colWrStatus) != runStatusCompleted || rec.String(colWrPaused) != "" {
		t.Fatalf("reauthorized run = %q paused %q", rec.String(colWrStatus), rec.String(colWrPaused))
	}
	late := f.stepOf(run, stale)
	if late.Status != stepStatusMessageSent || late.OutputID != committed.MessageID.String() ||
		late.EventSeq != committed.EventSeq {
		t.Fatalf("uncertain claim = %+v, want the committed receipt %+v", late, committed)
	}
	if f.message.effects != effects+1 {
		t.Fatalf("effects after reauthorization = %d, want exactly the refused step's one", f.message.effects-effects)
	}
}

// startPausedAfterRefresh starts create -> {msg, late} and rotates the
// initiator's credential inside the work-create effect, so the message steps
// of the same drain are refused and pause the run.
func (f *bindingRunFixture) startPausedAfterRefresh() (string, model.ID, string) {
	f.t.Helper()
	f.work.onCreate = func() { f.refresh() }
	defer func() { f.work.onCreate = nil }()
	before := f.member
	wfID, run, raw := f.start(before,
		step("create", stepWorkCreate, k4CreateStepConfig()),
		f.messageStep("msg", "create"),
		f.messageStep("late", "create"),
	)
	return wfID, run, raw
}

func TestRebindPublicationCrashAndCAS(t *testing.T) {
	f := newBindingRunFixture(t)
	wfID, run, _ := f.startPausedAfterRefresh()
	f.requireGated(run, "setup")
	original := f.handle(run)
	rec, _ := f.row(run)
	plan := rec.String(colWrPlanHash)

	// Crash after W0: the run only moved its version.
	f.mod.reauthorizeHook = func(stage reauthorizeStage) error {
		if stage == reauthorizeReserved {
			return errors.New("crash")
		}
		return nil
	}
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusServiceUnavailable {
		t.Fatalf("crash after reserve = %d %s", r.code, r.raw)
	}
	f.requireGated(run, "crash after reserve")
	if f.handle(run) != original || f.liveBindings(run) != 1 {
		t.Fatal("a reservation alone changed the binding")
	}

	// Crash after W1: the successor exists, the run still names the superseded
	// original, and nothing authorizes an effect.
	f.mod.reauthorizeHook = func(stage reauthorizeStage) error {
		if stage == reauthorizeSucceeded {
			return errors.New("crash")
		}
		return nil
	}
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusServiceUnavailable {
		t.Fatalf("crash after succession = %d %s", r.code, r.raw)
	}
	f.requireGated(run, "crash after succession")
	if f.handle(run) != original || f.resolves(run, original) {
		t.Fatal("after an unpublished succession the run must name its superseded original")
	}
	if f.liveBindings(run) != 1 {
		t.Fatalf("live bindings = %d, want the one unpublished successor", f.liveBindings(run))
	}
	calls := f.message.calls()
	f.mod.AdvanceWorkflowRuns(context.Background(), f.h.moduleCtx(f.tenant))
	if f.message.calls() != calls {
		t.Fatal("an unpublished successor let the run act")
	}

	// Run CAS loss: the run moves between the succession and its publication.
	f.mod.reauthorizeHook = func(stage reauthorizeStage) error {
		if stage == reauthorizeSucceeded {
			f.mutateRun(run, func(model.Record, []runStepState) {})
		}
		return nil
	}
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusConflict {
		t.Fatalf("publication after a moved run = %d %s", r.code, r.raw)
	}
	f.requireGated(run, "CAS loss")
	f.mod.reauthorizeHook = nil

	// Another account, even a tenant administrator, cannot continue the run.
	admin := f.h.roleToken(f.root, f.tenant, "admin-"+model.NewID().String()[:8]+"@binding.test", auth.RoleOwner)
	if r := f.reauthorize(admin, wfID, run, plan); r.code != http.StatusForbidden {
		t.Fatalf("administrator's own credential = %d %s", r.code, r.raw)
	}
	if r := f.reauthorize(f.member, wfID, run, "not-the-plan"); r.code != http.StatusConflict {
		t.Fatalf("other plan hash = %d %s", r.code, r.raw)
	}
	f.requireGated(run, "refused continuations")

	// Cold recovery on a restarted process: one auditable winner.
	if code := f.reauthorizeOn(f.restartedModule(), f.member, wfID, run, plan); code != http.StatusOK {
		t.Fatalf("cold recovery = %d", code)
	}
	published := f.handle(run)
	if published == original || !f.resolves(run, published) || f.liveBindings(run) != 1 {
		t.Fatal("recovery did not publish exactly one current successor")
	}
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusConflict {
		t.Fatalf("second continuation of an open run = %d %s", r.code, r.raw)
	}
}

func TestConcurrentReauthorizationsPublishOneWinner(t *testing.T) {
	f := newBindingRunFixture(t)
	wfID, run, _ := f.startPausedAfterRefresh()
	rec, _ := f.row(run)
	plan := rec.String(colWrPlanHash)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = f.reauthorize(f.member, wfID, run, plan).code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
		default:
			t.Fatalf("concurrent continuation = %d", code)
		}
	}
	if ok != 1 || f.liveBindings(run) != 1 || !f.resolves(run, f.handle(run)) {
		t.Fatalf("concurrent continuations: %d succeeded, %d live bindings", ok, f.liveBindings(run))
	}
}

func TestSupersededBindingAtEffectBoundaryInTheRun(t *testing.T) {
	f := newBindingRunFixture(t)
	// F2 (r1d): a live claim no longer holds the reauthorization back — a
	// claim's age proves nothing. The supersession moves the proof every effect
	// commits against (TestBindingSupersessionBarrier), and the in-flight claim
	// is left executing: it is recovered with its original key, never reset.
	wfID, run, _ := f.startPausedAfterRefresh()
	rec, _ := f.row(run)
	plan := rec.String(colWrPlanHash)
	f.mutateRun(run, func(_ model.Record, steps []runStepState) {
		steps[len(steps)-1].Status, steps[len(steps)-1].At = stepStatusExecuting, f.clock.Now().String()
	})
	live := f.stepOf(run, "msg")
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusOK {
		t.Fatalf("reauthorize over a live claim = %d %s", r.code, r.raw)
	}
	if s := f.stepOf(run, "msg"); s.Status != stepStatusExecuting || s.At != live.At {
		t.Fatalf("the in-flight claim was reset by the reauthorization: %+v", s)
	}

	// An acknowledgement observed under the old binding while a
	// reauthorization is published is dropped and observed again under the new
	// binding.
	g := newBindingRunFixture(t)
	gwf, grun, _ := g.start(g.member, g.waitStep("wait"))
	if s := g.stepOf(grun, "wait"); s.Status != stepStatusWaitingAck {
		t.Fatalf("wait step = %+v", s)
	}
	old := g.handle(grun)
	grec, _ := g.row(grun)
	// The stale observation answers acknowledged; the one the reauthorization's
	// own drain makes answers pending; the next one, acknowledged.
	g.acks.answers = []bool{true, false, true}
	g.acks.hook = func() {
		g.mutateRun(grun, func(rec model.Record, _ []runStepState) { rec[colWrPaused] = pausedReauthRequired })
		if r := g.reauthorize(g.member, gwf, grun, grec.String(colWrPlanHash)); r.code != http.StatusOK {
			t.Errorf("reauthorize during the observation = %d %s", r.code, r.raw)
		}
	}
	g.mod.AdvanceWorkflowRuns(context.Background(), g.h.moduleCtx(g.tenant))
	if s := g.stepOf(grun, "wait"); s.Status != stepStatusWaitingAck {
		t.Fatalf("an observation under the superseded binding was recorded: %+v", s)
	}
	current := g.handle(grun)
	if current == old || g.resolves(grun, old) {
		t.Fatal("the reauthorization did not supersede the observed binding")
	}
	g.mod.AdvanceWorkflowRuns(context.Background(), g.h.moduleCtx(g.tenant))
	if s := g.stepOf(grun, "wait"); s.Status != stepStatusAcked {
		t.Fatalf("observation under the current binding = %+v", s)
	}
	last := g.acks.queries[len(g.acks.queries)-1]
	if last.Actor.CredentialBinding != current {
		t.Fatal("the repeated observation did not use the current binding")
	}
}

func TestWorkflowPolicyDenialBlocksWithoutPause(t *testing.T) {
	f := newBindingRunFixture(t)
	f.message.deny = true
	_, run, _ := f.start(f.member, f.messageStep("msg"))
	rec, _ := f.row(run)
	if s := f.stepOf(run, "msg"); s.Status != stepStatusBlocked {
		t.Fatalf("denied step = %+v, want blocked", s)
	}
	if rec.String(colWrPaused) != "" || rec.String(colWrStatus) != runStatusFailed {
		t.Fatalf("denied run = %q paused %q, want a failed run without a pause",
			rec.String(colWrStatus), rec.String(colWrPaused))
	}
}
