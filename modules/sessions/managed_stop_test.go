// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_test.go (P2 / W2) — the managed Stop through the REAL module, the
// REAL store, the REAL authenticator/authorizer and a REAL owned child process.
//
// Nothing here is a double. The principal is reconstructed by
// ResolvePrincipalScope from a session this test logged in and elevated; the
// authorization is the product's AuthorizeRouteMutation; the run is a launched
// process whose exit the runtime observes; and the journal row is read back from
// the engine's own evidence relation. A refusal case asserts BOTH halves — the
// answer AND that no journal row exists — because a refusal that quietly burned a
// single-use identity is the failure this surface exists to prevent.

// managedStopFixture is one composed module over one store.
type managedStopFixture struct {
	t         *testing.T
	m         *Module
	st        store.Store
	tenant    model.TenantID
	authr     *auth.Authenticator
	authz     *auth.Authorizer
	workspace model.ID
	profile   ProviderProfile
	clock     *testClock
	boundSeq  int
}

// newManagedStopFixture composes the module against cfg and wires the managed
// Stop's real ports.
//
// ⛔ THE MODULE CLOCK IS REAL TIME HERE, and it is not a convenience. The claim's
// lease deadline is stamped from the module clock and compared against the
// STORE's TransactionNow — by the engine's own claim qualification as well as by
// this module. baseTime, the fixed fake instant the other runtime tests use,
// makes every claim read as lapsed against a live database clock, which is a
// harness artifact and not a behavior.
func newManagedStopFixture(t *testing.T, cfg store.Config) *managedStopFixture {
	t.Helper()
	return newManagedStopFixtureWith(t, cfg,
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2*time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
	)
}

// newManagedStopFixtureWith composes the module with an explicit option set, so a
// work-bound case can supply the K1 ports and a runner of its own.
func newManagedStopFixtureWith(t *testing.T, cfg store.Config, opts ...Option) *managedStopFixture {
	t.Helper()
	clk := &testClock{now: time.Now().UTC()}
	m := New(append([]Option{WithClock(clk), WithManagedStopAdmissionTimeout(30 * time.Second)}, opts...)...)
	ctx := context.Background()
	st, err := engine.Open(ctx, cfg, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	m.UseExecutionEnvironmentRef(testEnvRef)

	f := &managedStopFixture{
		t: t, m: m, st: st, tenant: tenant, clock: clk,
		authr: auth.NewAuthenticator(st, nil),
		authz: auth.NewAuthorizer(nil),
	}
	m.UseManagedStopAuthority(f.authr, f.authz, st.Leader())
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		def, derr := sc.DefaultWorkspace(ctx)
		f.workspace = def.ID
		return derr
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	if _, _, err := f.authr.BootstrapSuperadminOwning(ctx, "root@w2.test", "rootpassword1", tenant); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	config, home := t.TempDir(), t.TempDir()
	f.profile = mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: config, UserHome: home,
		DisplayName: "codex-w2", AuthSource: AuthSourceAccountHome,
	})
	return f
}

// readiness is the store's own answer to "can a managed Stop bind at all?".
func (f *managedStopFixture) readiness() store.CustodialReadiness {
	f.t.Helper()
	r, ok := f.st.(store.CustodialEffectReadiness)
	if !ok {
		f.t.Fatal("the store exposes no custodial readiness")
	}
	return r.ManagedStopReadiness(context.Background())
}

// admin authenticates the bootstrap superadmin, which is the only principal that
// may create users and grant memberships.
func (f *managedStopFixture) admin() auth.Principal {
	f.t.Helper()
	ctx := context.Background()
	token, _, err := f.authr.Login(ctx, "root@w2.test", "rootpassword1", "127.0.0.1")
	if err != nil {
		f.t.Fatalf("admin login: %v", err)
	}
	p, err := f.authr.Authenticate(ctx, token)
	if err != nil {
		f.t.Fatalf("admin authenticate: %v", err)
	}
	return p
}

// operator creates a real member, logs it in and optionally runs the step-up
// ceremony. It returns the AUTHENTICATED principal a mounted route hands the
// module; the module reconstructs the evidence itself, under its own admission
// window. Resolving here too only proves the fixture's session is live.
func (f *managedStopFixture) operator(email, role string, elevate bool, deadline time.Duration) auth.Principal {
	f.t.Helper()
	ctx := context.Background()
	admin := f.admin()
	user, err := f.authr.CreateUser(ctx, admin, auth.NewUser{Email: email, Password: "operatorpass1"})
	if err != nil {
		f.t.Fatalf("create user %s: %v", email, err)
	}
	if _, err := f.authr.GrantMembership(ctx, admin, user.ID, f.tenant, role, ""); err != nil {
		f.t.Fatalf("grant %s to %s: %v", role, email, err)
	}
	token, _, err := f.authr.Login(ctx, email, "operatorpass1", "127.0.0.1")
	if err != nil {
		f.t.Fatalf("login %s: %v", email, err)
	}
	p, err := f.authr.Authenticate(ctx, token)
	if err != nil {
		f.t.Fatalf("authenticate %s: %v", email, err)
	}
	if elevate {
		if _, err := f.authr.ElevateSession(ctx, p, "webauthn", auth.AAL3); err != nil {
			f.t.Fatalf("elevate %s: %v", email, err)
		}
		// The ceremony UPDATES the session row, so the reference captured before it
		// names a version that no longer exists. Re-authenticating is what a real
		// caller does on its next request, and skipping it made this fixture report
		// "unauthenticated" for a session that was perfectly live.
		if p, err = f.authr.Authenticate(ctx, token); err != nil {
			f.t.Fatalf("re-authenticate %s: %v", email, err)
		}
	}
	ref, ok := p.Ref()
	if !ok {
		f.t.Fatalf("%s carries no credential reference", email)
	}
	wctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	if _, err := f.authr.ResolvePrincipalScope(wctx, ref, f.tenant); err != nil {
		f.t.Fatalf("resolve %s: %v", email, err)
	}
	return p
}

// launch starts a real supervised child and registers its teardown.
func (f *managedStopFixture) launch(thread string) (runDTO, *liveRun) {
	f.t.Helper()
	setCodexFixture(f.t, f.profile, codexFixture{ThreadID: thread, Account: "apikey"})
	dto, err := codexLaunch(f.t, f.m, f.tenant, f.profile)
	if err != nil {
		f.t.Fatalf("launch %s: %v", thread, err)
	}
	lr, ok := f.m.rt.getLive(f.tenant, dto.RunRef)
	if !ok {
		f.t.Fatalf("launch %s produced no live handle", thread)
	}
	reapOwnedChild(f.t, lr)
	return dto, lr
}

// managedStopCall is one invocation exactly as the ratified signature takes it:
// the cockpit question beside the request, never inside it.
type managedStopCall struct {
	Question ManagedStopQuestion
	ManagedStopRequest
}

// request builds a complete, lawful managed-stop request for one run.
func (f *managedStopFixture) request(p auth.Principal, dto runDTO, launch model.ID, opID string) managedStopCall {
	return managedStopCall{
		Question: f.cockpitQuestion(dto.RunRef, f.workspace),
		ManagedStopRequest: ManagedStopRequest{
			Principal:      p,
			RunRef:         dto.RunRef,
			ExpectedLaunch: launch,
			OperationID:    opID,
			Reason:         "operator stop",
			Link:           ManagedStopLink{CoreSessionID: "core-1", SessionSID: "sid-1", LinkGeneration: 1},
		},
	}
}

// cockpitQuestion is the W2 STAND-IN for question 1 (G6): sessions:live:read with
// the run:stop metadata, against the stored workspace a route would have checked.
// The real private stop permission, scoped grant and stored-link verification
// belong to W4 and are not established by these tests.
func (f *managedStopFixture) cockpitQuestion(runRef string, workspace model.ID) ManagedStopQuestion {
	return ManagedStopQuestion{
		Permission: permLiveRead,
		Resource: auth.ResourceAttrs{
			Kind: permLiveRead.Resource(), ID: runRef, WorkspaceID: workspace,
		},
		Route: managedRunStopMetadata,
	}
}

// call runs one managed Stop under a two-minute caller lifetime, which is what a
// mounted route's own deadline looks like.
func (f *managedStopFixture) call(c managedStopCall) (ManagedStopResult, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return f.m.StopManagedRun(ctx, f.tenant, c.Question, c.ManagedStopRequest)
}

// journal reads the engine's own row for one client operation id.
func (f *managedStopFixture) journal(opID string) (model.EvidenceOperation, bool) {
	f.t.Helper()
	ctx := context.Background()
	var (
		op    model.EvidenceOperation
		found bool
	)
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		got, err := sc.EvidenceOperations().Get(ctx, managedStopSurfacePrefix+opID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		op, found = got, true
		return nil
	}); err != nil {
		f.t.Fatalf("read journal %s: %v", opID, err)
	}
	return op, found
}

// runState reads the durable lifecycle of one run.
func (f *managedStopFixture) runState(runRef string) string {
	f.t.Helper()
	ctx := context.Background()
	var state string
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		state = rec.String(colState)
		return nil
	}); err != nil {
		f.t.Fatalf("read run %s: %v", runRef, err)
	}
	return state
}

// refuseWithNoJournalRow is the assertion every no-effect refusal shares.
func (f *managedStopFixture) refuseWithNoJournalRow(
	res ManagedStopResult, err error, opID string, want ManagedStopOutcome,
) {
	f.t.Helper()
	if err != nil {
		f.t.Fatalf("%s: unexpected error %v (result %+v)", want, err, res)
	}
	if res.Outcome != want {
		f.t.Fatalf("outcome = %q (%s), want %q", res.Outcome, res.Detail, want)
	}
	if res.Attempted {
		f.t.Fatalf("%s reported the process boundary crossed", want)
	}
	if res.Settlement != "" {
		f.t.Fatalf("%s settled %q; a refusal settles nothing", want, res.Settlement)
	}
	if op, found := f.journal(opID); found {
		f.t.Fatalf("%s wrote journal row %+v; a no-effect refusal burns no identity", want, op)
	}
}

func managedStopBackends(t *testing.T) []struct {
	name   string
	config func(*testing.T) store.Config
} {
	t.Helper()
	backends := []struct {
		name   string
		config func(*testing.T) store.Config
	}{{"sqlite", func(*testing.T) store.Config {
		return store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
	}}}
	if enginetest.PostgresAvailable(t) {
		backends = append(backends, struct {
			name   string
			config func(*testing.T) store.Config
		}{"postgres", func(t *testing.T) store.Config {
			pg := enginetest.IsolatedPostgres(t)
			return store.Config{
				Engine: store.EnginePostgres, DSN: pg.App,
				AdminDSN: pg.Admin, OwnerDSN: pg.Owner, Debug: true,
			}
		}})
	} else {
		t.Log("Postgres NOT exercised: no configured disposable server")
	}
	return backends
}

// TestManagedStopReadinessFollowsTheRunDescriptor is the W1 interplay: the engine
// reported relation_invalid for every ordinary build precisely because
// sessions.run declared no authorization lineage. This asserts the lineage W2
// adds is what turns the capability on.
func TestManagedStopReadinessFollowsTheRunDescriptor(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			got := f.readiness()
			if !got.Ready {
				t.Fatalf("readiness = %+v, want ready: the run descriptor now declares its lineage", got)
			}
			t.Logf("managed stop readiness on %s = %+v", be.name, got)
		})
	}
}

// TestManagedStopLawfulPositive is the whole path: three questions, one claim, a
// real process ended, and a recorded settlement.
func TestManagedStopLawfulPositive(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("positive@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-positive")
			req := f.request(op, dto, lr.launchID, "positive-1")

			res, err := f.call(req)
			if err != nil {
				t.Fatalf("StopManagedRun: %v (%+v)", err, res)
			}
			if res.Outcome != ManagedStopStopped {
				t.Fatalf("outcome = %q (%s), want stopped", res.Outcome, res.Detail)
			}
			if !res.Attempted {
				t.Fatal("a lawful stop must report the process boundary crossed")
			}
			if res.ProcessOutcome != managedStopExitObserved || res.Observation != obsProcessExitObserved {
				t.Fatalf("process = %q, observation %q; want the exact-launch observed exit",
					res.ProcessOutcome, res.Observation)
			}
			if res.CredentialRevocation != managedStopRevokeNotUsed {
				t.Fatalf("credential revocation = %q; this launch held no runtime credential",
					res.CredentialRevocation)
			}
			if res.Settlement != model.EvidenceOpCompleted {
				t.Fatalf("settlement = %q, want completed", res.Settlement)
			}
			if res.OperationRef != managedStopSurfacePrefix+"positive-1" {
				t.Fatalf("operation ref = %q", res.OperationRef)
			}
			journalRow, found := f.journal("positive-1")
			if !found {
				t.Fatal("the lawful stop recorded no journal row")
			}
			if journalRow.State != model.EvidenceOpCompleted ||
				journalRow.Surface != store.ManagedStopSurface ||
				journalRow.Action != store.ManagedStopAction {
				t.Fatalf("journal row = %+v", journalRow)
			}
			if journalRow.ClaimEvidenceRef == "" || journalRow.OutcomeEvidenceRef == "" {
				t.Fatalf("journal row is not anchored on both halves: %+v", journalRow)
			}
			if state := f.runState(dto.RunRef); state != stateStopped {
				t.Fatalf("run state = %q, want stopped", state)
			}
			if processRunning(lr.proc.PID()) {
				t.Fatal("the supervised child is still running after a stopped verdict")
			}
			t.Logf("lawful stop on %s: %+v / journal %+v", be.name, res, journalRow)
		})
	}
}

// TestManagedStopReplayDoesNotRedispatch is the single-use guarantee: the same
// operation id answers from the recorded row and crosses nothing.
func TestManagedStopReplayDoesNotRedispatch(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("replay@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-replay")
			req := f.request(op, dto, lr.launchID, "replay-1")
			if res, err := f.call(req); err != nil || res.Outcome != ManagedStopStopped {
				t.Fatalf("first stop = %+v, %v", res, err)
			}
			before, _ := f.journal("replay-1")

			res, err := f.call(req)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			if res.Outcome != ManagedStopStopped || res.Settlement != model.EvidenceOpCompleted {
				t.Fatalf("replay = %+v, want the recorded completed settlement", res)
			}
			if res.ProcessOutcome != "" || res.CredentialRevocation != "" {
				t.Fatalf("replay reported effect halves %q/%q; nothing was dispatched",
					res.ProcessOutcome, res.CredentialRevocation)
			}
			if res.Observation != obsProcessExitObserved {
				t.Fatalf("replay observation = %q, want the exact-launch P1 beside the settled row", res.Observation)
			}
			after, _ := f.journal("replay-1")
			if after.Version != before.Version || after.OutcomeEvidenceRef != before.OutcomeEvidenceRef {
				t.Fatalf("the replay moved the journal row: %+v -> %+v", before, after)
			}
			t.Logf("replay on %s answered from the row without writing: %+v", be.name, res)
		})
	}
}

// TestManagedStopReconcileNeverAdopts: reconciliation reads and never claims,
// never settles and never signals.
func TestManagedStopReconcileNeverAdopts(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("reconcile@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-reconcile")
			req := f.request(op, dto, lr.launchID, "reconcile-1")

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			res, err := f.m.ReconcileManagedStop(ctx, f.tenant, req.Question, req.ManagedStopRequest)
			if err != nil {
				t.Fatalf("reconcile before any claim: %v", err)
			}
			if res.Outcome != ManagedStopUnknown {
				t.Fatalf("reconcile = %q (%s), want unknown for an unclaimed operation",
					res.Outcome, res.Detail)
			}
			if _, found := f.journal("reconcile-1"); found {
				t.Fatal("reconciliation wrote a journal row")
			}
			if state := f.runState(dto.RunRef); state != stateRunning {
				t.Fatalf("reconciliation changed the run to %q", state)
			}
			if !processRunning(lr.proc.PID()) {
				t.Fatal("reconciliation ended the supervised child")
			}
		})
	}
}

// TestManagedStopRefusals walks every no-effect refusal the closed vocabulary
// names and proves each one burns no journal identity.
func TestManagedStopRefusals(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			editor := f.operator("refuse-editor@w2.test", auth.RoleEditor, true, 2*time.Minute)

			t.Run("launch_superseded", func(t *testing.T) {
				dto, _ := f.launch("thread-superseded")
				req := f.request(editor, dto, model.NewID(), "refuse-superseded")
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "refuse-superseded", ManagedStopLaunchSuperseded)
				if state := f.runState(dto.RunRef); state != stateRunning {
					t.Fatalf("a superseded-launch refusal changed the run to %q", state)
				}
			})

			t.Run("concealed_foreign_workspace", func(t *testing.T) {
				dto, lr := f.launch("thread-foreign")
				req := f.request(editor, dto, lr.launchID, "refuse-foreign")
				req.Question.Resource.WorkspaceID = model.NewID()
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "refuse-foreign", ManagedStopConcealed)
			})

			t.Run("concealed_absent_run", func(t *testing.T) {
				req := f.request(editor, runDTO{RunRef: "no-such-run"}, model.NewID(), "refuse-absent")
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "refuse-absent", ManagedStopConcealed)
			})

			t.Run("work_lease_unexpected", func(t *testing.T) {
				dto, lr := f.launch("thread-unbound")
				req := f.request(editor, dto, lr.launchID, "refuse-unexpected")
				req.Work = &ManagedStopWork{
					WorkItemID: model.NewID(), LeaseFence: 1, OwnerEpoch: 1,
					LeaseExpiresAt: "2026-09-14T00:00:00Z",
					HolderSID:      "sid", HolderRunRef: dto.RunRef,
				}
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "refuse-unexpected", ManagedStopWorkLeaseUnexpected)
			})

			t.Run("step_up_required", func(t *testing.T) {
				flat := f.operator("flat@w2.test", auth.RoleEditor, false, 2*time.Minute)
				if flat.AAL >= auth.AAL3 {
					t.Fatalf("an un-elevated session carries AAL %d", flat.AAL)
				}
				dto, lr := f.launch("thread-stepup")
				req := f.request(flat, dto, lr.launchID, "refuse-stepup")
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "refuse-stepup", ManagedStopStepUpRequired)
			})

			t.Run("forbidden_viewer", func(t *testing.T) {
				viewer := f.operator("viewer@w2.test", auth.RoleViewer, true, 2*time.Minute)
				dto, lr := f.launch("thread-viewer")
				req := f.request(viewer, dto, lr.launchID, "refuse-viewer")
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "refuse-viewer", ManagedStopForbidden)
			})

			t.Run("canceled_caller", func(t *testing.T) {
				dto, lr := f.launch("thread-canceled")
				req := f.request(editor, dto, lr.launchID, "refuse-canceled")
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				res, err := f.m.StopManagedRun(ctx, f.tenant, req.Question, req.ManagedStopRequest)
				f.refuseWithNoJournalRow(res, err, "refuse-canceled", ManagedStopCanceled)
				if !processRunning(lr.proc.PID()) {
					t.Fatal("a canceled caller ended the child")
				}
			})

			t.Run("malformed_request", func(t *testing.T) {
				dto, lr := f.launch("thread-malformed")
				req := f.request(editor, dto, lr.launchID, "")
				res, err := f.m.StopManagedRun(context.Background(), f.tenant, req.Question, req.ManagedStopRequest)
				if err == nil {
					t.Fatal("a request with no operation id was accepted")
				}
				if res.Outcome != ManagedStopConcealed {
					t.Fatalf("outcome = %q", res.Outcome)
				}
			})
		})
	}
}

// TestManagedStopUnwiredRefusesBeforeAnyRead is the deny-closed composition: no
// ports, no questions, no reads, no rows.
func TestManagedStopUnwiredRefusesBeforeAnyRead(t *testing.T) {
	f := newManagedStopFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	op := f.operator("unwired@w2.test", auth.RoleEditor, true, 2*time.Minute)
	dto, lr := f.launch("thread-unwired")
	f.m.managedStop = nil

	res, err := f.call(f.request(op, dto, lr.launchID, "unwired-1"))
	if !errors.Is(err, ErrManagedStopUnwired) {
		t.Fatalf("err = %v, want ErrManagedStopUnwired", err)
	}
	if res.Outcome != ManagedStopUnwired {
		t.Fatalf("outcome = %q, want unwired", res.Outcome)
	}
	if _, found := f.journal("unwired-1"); found {
		t.Fatal("an unwired module wrote a journal row")
	}
	if !processRunning(lr.proc.PID()) {
		t.Fatal("an unwired module ended the child")
	}
}

// TestManagedStopOperationRebind is X3: one operation id names ONE effect. A
// second call under the same id with different semantics is a conflict, and the
// recorded row is not disturbed.
func TestManagedStopOperationRebind(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("rebind@w2.test", auth.RoleEditor, true, 2*time.Minute)
			first, lr1 := f.launch("thread-rebind-1")
			if res, err := f.call(f.request(op, first, lr1.launchID, "rebind-1")); err != nil ||
				res.Outcome != ManagedStopStopped {
				t.Fatalf("first stop = %+v, %v", res, err)
			}
			before, _ := f.journal("rebind-1")

			second, lr2 := f.launch("thread-rebind-2")
			res, err := f.call(f.request(op, second, lr2.launchID, "rebind-1"))
			if err != nil {
				t.Fatalf("rebind: %v", err)
			}
			if res.Outcome != ManagedStopOperationConflict {
				t.Fatalf("outcome = %q (%s), want an operation conflict", res.Outcome, res.Detail)
			}
			after, _ := f.journal("rebind-1")
			if after.Version != before.Version || after.EffectDigest != before.EffectDigest {
				t.Fatalf("the rebind moved the recorded row: %+v -> %+v", before, after)
			}
			if state := f.runState(second.RunRef); state != stateRunning {
				t.Fatalf("the rebind stopped the second run (%q)", state)
			}
			if !processRunning(lr2.proc.PID()) {
				t.Fatal("the rebind ended the second child")
			}
		})
	}
}

// TestManagedStopClaimLapseRetiresAndCommitsNothingElse is D4a: a lapsed
// admission claim commits its retirement ALONE. No stop, no journal row, and the
// child stays alive because nothing authorized ending it.
func TestManagedStopClaimLapseRetiresAndCommitsNothingElse(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("lapse@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-lapse")
			ctx := context.Background()
			// Expire the admission claim in the durable row, which is what a heartbeat
			// that stopped arriving produces.
			if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
				rec, found, err := findClaim(ctx, sc, lr.claim.SID)
				if err != nil || !found {
					t.Fatalf("find claim: %v found=%t", err, found)
				}
				rec[colLeaseExpires] = model.NewTimestamp(time.Now().Add(-time.Hour)).String()
				repo, err := sc.Ext(claimKind)
				if err != nil {
					return err
				}
				_, err = repo.Update(ctx, rec)
				return err
			}); err != nil {
				t.Fatalf("expire the claim: %v", err)
			}

			res, err := f.call(f.request(op, dto, lr.launchID, "lapse-1"))
			if err != nil {
				t.Fatalf("StopManagedRun: %v", err)
			}
			if res.Outcome != ManagedStopClaimRetired {
				t.Fatalf("outcome = %q (%s), want the retirement", res.Outcome, res.Detail)
			}
			if res.Attempted {
				t.Fatal("a lapsed claim crossed the process boundary")
			}
			if _, found := f.journal("lapse-1"); found {
				t.Fatal("a lapsed claim burned a journal identity")
			}
			// The retirement COMMITTED: that is the one write this path is allowed.
			if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
				rec, found, err := findClaim(ctx, sc, lr.claim.SID)
				if err != nil || !found {
					t.Fatalf("re-read claim: %v found=%t", err, found)
				}
				if state := rec.String(colClaimState); state != claimExpired {
					t.Fatalf("claim state = %q, want the durable retirement", state)
				}
				return nil
			}); err != nil {
				t.Fatalf("verify retirement: %v", err)
			}
			if state := f.runState(dto.RunRef); state != stateRunning {
				t.Fatalf("a lapsed claim changed the run to %q", state)
			}
			if !processRunning(lr.proc.PID()) {
				t.Fatal("a lapsed claim ended the child")
			}
		})
	}
}

// TestManagedStopNotLocallySupervised: a run this runtime does not hold is not
// stopped by proxy. The refusal is C2's, and the journal stays empty.
func TestManagedStopNotLocallySupervised(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("remote@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-remote")
			launch := lr.launchID
			// Drop the in-memory registration WITHOUT touching the durable row: this
			// is exactly what a second node sees for a run another node supervises.
			f.m.rt.mu.Lock()
			delete(f.m.rt.live, liveKey(f.tenant, dto.RunRef))
			f.m.rt.mu.Unlock()

			res, err := f.call(f.request(op, dto, launch, "remote-1"))
			f.refuseWithNoJournalRow(res, err, "remote-1", ManagedStopNotLocallySupervised)
			if state := f.runState(dto.RunRef); state != stateRunning {
				t.Fatalf("run state = %q", state)
			}
		})
	}
}

// TestManagedStopAuthorizationOutcomesFollowTheRatifiedMap pins correction 1 §5.4:
// each authorization source identity maps to its own refusal, wrapped or not, and
// nothing that was not decided ever reads as a denial.
func TestManagedStopAuthorizationOutcomesFollowTheRatifiedMap(t *testing.T) {
	cases := []struct {
		err  error
		want ManagedStopOutcome
	}{
		{auth.ErrRouteDenied, ManagedStopForbidden},
		{fmt.Errorf("route: %w", auth.ErrRouteDenied), ManagedStopForbidden},
		{auth.ErrScopedGrantRequired, ManagedStopScopedGrantRequired},
		{fmt.Errorf("route: %w", auth.ErrScopedGrantRequired), ManagedStopScopedGrantRequired},
		{auth.ErrStepUpRequired, ManagedStopStepUpRequired},
		{auth.ErrRouteUndecided, ManagedStopAuthorityUnavailable},
		{auth.ErrAuthorizerUnavailable, ManagedStopUnwired},
		{context.Canceled, ManagedStopCanceled},
		{fmt.Errorf("resolve: %w", context.DeadlineExceeded), ManagedStopCanceled},
		{errors.New("principal evidence is unavailable"), ManagedStopAuthorityUnavailable},
	}
	for _, c := range cases {
		if got := managedStopAuthOutcome(c.err); got != c.want {
			t.Errorf("managedStopAuthOutcome(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
