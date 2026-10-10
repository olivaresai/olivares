// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The session cockpit lists core Sessions and a run started from the CLI or the
// console had none ("Sessions · 0" while a session ran). Each launch attempt opens its
// own core Session, keyed by its launch id, in the run's authorization workspace; a stop
// completes it, a failure fails it, and a resume opens the next one.
func TestEveryLaunchAttemptOpensAndClosesItsCoreSession(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: "core-session"}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "core-session-"+be.name)
			config, user, _, _ := twoHomes(t)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: user, DisplayName: "A"})
			params := CreateRunParams{
				Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
			}
			run, err := m.createRun(ctx, tenant, params)
			if err != nil {
				t.Fatal(err)
			}
			read := func(id string) (model.Session, model.Record) {
				t.Helper()
				var core model.Session
				var rec model.Record
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(runKind)
					if err != nil {
						return err
					}
					if rec, err = findRunRec(ctx, repo, run.RunRef); err != nil {
						return err
					}
					core, err = sc.Sessions().Get(ctx, model.ID(id))
					return err
				}); err != nil {
					t.Fatalf("core session %q: %v", id, err)
				}
				return core, rec
			}
			current := func(step string) string {
				t.Helper()
				d, err := m.getRun(ctx, tenant, run.RunRef)
				if err != nil || d.CoreSessionID == "" {
					t.Fatalf("%s: run core_session_id = %q (%v)", step, d.CoreSessionID, err)
				}
				core, rec := read(d.CoreSessionID)
				if core.State != model.SessionRunning || core.EndedAt != nil || core.ExternalID != rec.String(colRuntimeLaunchID) ||
					core.WorkspaceID.String() != rec.String(colRunAuthzWorkspaceID) {
					t.Fatalf("%s: core session = %+v, want running, launch %s, workspace %s", step, core, rec.String(colRuntimeLaunchID), rec.String(colRunAuthzWorkspaceID))
				}
				return d.CoreSessionID
			}
			ended := func(step, id string, want model.SessionState) {
				t.Helper()
				if core, _ := read(id); core.State != want || core.EndedAt == nil {
					t.Fatalf("%s: core session = %s ended %v, want %s and ended", step, core.State, core.EndedAt, want)
				}
			}
			first := current("launch")
			if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", "user"); err != nil {
				t.Fatal(err)
			}
			ended("stop", first, model.SessionCompleted)
			if _, err := m.resumeRun(ctx, tenant, run.RunRef, "user:u1", "user", ""); err != nil {
				t.Fatalf("resume: %v", err)
			}
			second := current("resume")
			if second == first {
				t.Fatal("the resumed attempt reuses the first attempt's core Session")
			}
			ended("after the resume", first, model.SessionCompleted)
			if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", "user"); err != nil {
				t.Fatal(err)
			}
			ended("second stop", second, model.SessionCompleted)

			// A launch the runner refuses fails its run and its core Session.
			fr.mu.Lock()
			fr.launchErr = errors.New("spawn refused")
			fr.mu.Unlock()
			if _, err := m.createRun(ctx, tenant, params); err == nil {
				t.Fatal("a refused spawn launched")
			}
			var sessions []model.Session
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				var err error
				sessions, _, err = sc.Sessions().List(ctx, model.Query{Limit: 10})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			failed := 0
			for _, s := range sessions {
				if s.State == model.SessionFailed && s.EndedAt != nil {
					failed++
				}
			}
			if len(sessions) != 3 || failed != 1 {
				t.Fatalf("core sessions = %d (%d failed), want 3 with the refused launch failed", len(sessions), failed)
			}
		})
	}
}

// RuntimeCoreSessionInScope answers through the runtime target validator: the exact
// core Session of the running attempt, and the validator's refusal for a stale target.
func TestRuntimeCoreSessionInScopeIsTheTargetsOwnSession(t *testing.T) {
	runner := &fakeRunner{initSID: "core-session-target"}
	f := newManagedStopFixtureWith(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, WithRunner(runner), WithCredentialSource(staticCred()))
	ctx := context.Background()
	completion, err := f.m.LaunchRunWithCompletion(ctx, f.tenant, CreateRunParams{ProviderProfileRef: ensureRuntimeTestProfileRef(t, f.m, f.tenant), Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:launch", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	original, ok := completion.Identity()
	if !ok {
		t.Fatal("launch omitted original completion")
	}
	d, err := f.m.getRun(ctx, f.tenant, original.RunRef)
	if err != nil || d.CoreSessionID == "" {
		t.Fatalf("run core_session_id = %q (%v)", d.CoreSessionID, err)
	}
	target := RuntimeInputTarget{Tenant: original.Tenant, WorkspaceID: original.WorkspaceID, RunRef: original.RunRef, ExpectedLaunch: original.RuntimeLaunchID, ExpectedSID: original.SessionSID}
	stale := target
	stale.ExpectedLaunch = model.NewID()
	inRuntimeTargetScope(t, f, target, func(sc store.Scope) {
		got, err := f.m.RuntimeCoreSessionInScope(ctx, sc, target)
		if err != nil || got.String() != d.CoreSessionID {
			t.Errorf("core session in scope = %q (%v), want %s", got, err, d.CoreSessionID)
		}
		if got, err := f.m.RuntimeCoreSessionInScope(ctx, sc, stale); !errors.Is(err, ErrRuntimeInputTarget) || got != "" {
			t.Errorf("stale target = %q (%v), want the validator's refusal", got, err)
		}
	})
}

// A work launch's core Session names the agent that holds the work, the
// agent the runtime target's work custody check requires, so the cockpit adopts it as is.
func TestAWorkLaunchCoreSessionNamesItsWorkHolder(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, _ := newManagedStopWorkFixture(t, be.config(t))
			managed, _, _, _ := f.boundRun(t)
			target := runtimeInputTargetForTest(t, f, managed.RunRef)
			if target.Work == nil || target.Work.HolderAgentID.IsZero() {
				t.Fatalf("the bound run's target has no work holder: %+v", target.Work)
			}
			inRuntimeTargetScope(t, f, target, func(sc store.Scope) {
				id, err := f.m.RuntimeCoreSessionInScope(context.Background(), sc, target)
				if err != nil {
					t.Fatalf("core session in scope: %v", err)
				}
				core, err := sc.Sessions().Get(context.Background(), id)
				if err != nil || core.AgentID != target.Work.HolderAgentID || core.ExternalID != target.ExpectedLaunch.String() {
					t.Fatalf("core session = agent %q launch %q (%v), want agent %s launch %s", core.AgentID, core.ExternalID, err, target.Work.HolderAgentID, target.ExpectedLaunch)
				}
			})
		})
	}
}

// inRuntimeTargetScope runs fn in a mutation holding the target's directory authority
// barrier, as a runtime input caller does before the target validator.
func inRuntimeTargetScope(t *testing.T, f *managedStopFixture, target RuntimeInputTarget, fn func(store.Scope)) {
	t.Helper()
	ctx := context.Background()
	authority := runtimeInputAuthorityForTest(t, f, target)
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		now, err := sc.(store.TransactionClock).TransactionNow(ctx)
		if err != nil {
			return err
		}
		bundle, err := authority.AuthorityFor(now.Time(), target)
		if err != nil {
			return err
		}
		if err := sc.(store.DirectoryAuthoritySnapshotLocker).LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
			return err
		}
		fn(sc)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type beforeRunCoreSessionRegistry struct{ store.ExtensionRegistry }

func (r beforeRunCoreSessionRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == runKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name != colRunCoreSessionID {
				fields = append(fields, f)
			}
		}
		d.Fields = fields
		indexes := make([]model.IndexSpec, 0, len(d.Indexes))
		for _, ix := range d.Indexes {
			if ix.Name != "sessions_run_core_session_uniq" {
				indexes = append(indexes, ix)
			}
		}
		d.Indexes = indexes
	}
	return r.ExtensionRegistry.Register(d)
}

// An older database gains the nullable column at boot with nothing to run:
// historical runs keep NULL and no core Session is fabricated for them.
func TestRunCoreSessionNullableUpgradeBothBackends(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			ctx := context.Background()
			old := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error { return old.RegisterSchema(beforeRunCoreSessionRegistry{reg}) })
			tenant := ensureTenant(t, st, "run-core-session-upgrade")
			ref := model.NewID().String()
			if err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(ctx, model.Record{colRunRef: ref, colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateStopped, colLastEventSeq: int64(0)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			if got, err := m.getRun(ctx, tenant, ref); err != nil || got.CoreSessionID != "" || got.ProcessState != stateStopped {
				t.Fatalf("upgraded run = %+v (%v), want stopped with no core session", got, err)
			}
			if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				record, err := findRunRec(ctx, repo, ref)
				if err != nil {
					return err
				}
				if !record.IsNull(colRunCoreSessionID) {
					t.Error("the new nullable column was backfilled")
				}
				sessions, _, err := sc.Sessions().List(ctx, model.Query{Limit: 10})
				if err == nil && len(sessions) != 0 {
					t.Errorf("the upgrade fabricated %d core sessions", len(sessions))
				}
				if _, rerr := m.ReadCoreSessionRunInScope(ctx, sc, model.NewID()); !errors.Is(rerr, store.ErrNotFound) {
					t.Errorf("an upgraded store links an unknown core session: %v", rerr)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The cockpit listed a CLI-launched session with every field Unknown, because
// its row facts come from a private original only cockpit launches write. The public
// read links an engine-opened core Session to its run by exact equality on the stored
// core_session_id: the run, its canonical ID, the attempt's launch id (kept after the
// attempt ended), its workspace, the person who launched it and the model name it was
// launched with; an earlier attempt's Session after a resume, an unknown Session and
// another tenant's are not found.
func TestReadCoreSessionRunInScopeLinksAnEngineOpenedSession(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: "core-session-run"}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "core-session-run-"+be.name)
			other := ensureTenant(t, st, "core-session-run-other-"+be.name)
			config, user, _, _ := twoHomes(t)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: user, DisplayName: "A"})
			run, err := m.createRun(ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref, Model: "claude-opus-5-5",
			})
			if err != nil {
				t.Fatal(err)
			}
			read := func(tn model.TenantID, id string) (CoreSessionRun, error) {
				t.Helper()
				var got CoreSessionRun
				var rerr error
				if err := st.View(ctx, tn, func(sc store.Scope) error {
					got, rerr = m.ReadCoreSessionRunInScope(ctx, sc, model.ID(id))
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return got, rerr
			}
			first, err := m.getRun(ctx, tenant, run.RunRef)
			if err != nil || first.CoreSessionID == "" || first.CanonicalSID == "" {
				t.Fatalf("launched run = %+v (%v)", first, err)
			}
			var launch, workspace string
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec, err := findRunRec(ctx, repo, run.RunRef)
				launch, workspace = rec.String(colRuntimeLaunchID), rec.String(colRunAuthzWorkspaceID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			expectedRole := ""
			want := func(step string, got CoreSessionRun, err error) {
				t.Helper()
				if err != nil || got.Tenant != tenant || got.RunRef != run.RunRef || got.SessionSID != first.CanonicalSID ||
					got.RuntimeLaunchID.String() != launch || got.WorkspaceID.String() != workspace ||
					got.Actor != "user:u1" || got.ModelRef != "claude-opus-5-5" || got.Role != expectedRole {
					t.Fatalf("%s: read = %+v (%v), want run %s sid %s launch %s workspace %s actor user:u1 model claude-opus-5-5", step, got, err, run.RunRef, first.CanonicalSID, launch, workspace)
				}
			}
			got, err := read(tenant, first.CoreSessionID)
			want("running", got, err)
			if got.Work != nil {
				t.Fatalf("a run with no work binding presents work %+v", got.Work)
			}
			for _, tc := range []struct{ name, raw, role string }{
				{"historical", "", ""},
				{"invalid", `{"role":"worker","workspace_id":"invalid"}`, ""},
				{"foreign workspace", `{"role":"worker","workspace_id":"` + model.NewID().String() + `"}`, ""},
				{"orchestrator", `{"role":"orchestrator","workspace_id":"` + workspace + `","grant_id":"` + model.NewID().String() + `","capabilities":["work.read"]}`, "orchestrator"},
				{"worker", `{"role":"worker","workspace_id":"` + workspace + `"}`, "worker"},
			} {
				if err := mutateRunForWorkTest(m, tenant, run.RunRef, func(rec model.Record) { rec[colRunWorkScope] = tc.raw }); err != nil {
					t.Fatal(err)
				}
				expectedRole = tc.role
				got, err = read(tenant, first.CoreSessionID)
				want(tc.name, got, err)
			}
			if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", "user"); err != nil {
				t.Fatal(err)
			}
			got, err = read(tenant, first.CoreSessionID)
			want("after the stop", got, err)
			if _, err := m.resumeRun(ctx, tenant, run.RunRef, "user:u1", "user", ""); err != nil {
				t.Fatalf("resume: %v", err)
			}
			if _, err := read(tenant, first.CoreSessionID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("the first attempt's Session after a resume = %v, want not found", err)
			}
			resumed, _ := m.getRun(ctx, tenant, run.RunRef)
			if got, err := read(tenant, resumed.CoreSessionID); err != nil || got.RunRef != run.RunRef || got.RuntimeLaunchID.String() == launch {
				t.Fatalf("the resumed attempt's Session = %+v (%v), want the run with a new launch", got, err)
			}
			for name, tc := range map[string]struct {
				tn model.TenantID
				id string
			}{"unknown": {tenant, model.NewID().String()}, "other tenant": {other, resumed.CoreSessionID}} {
				if _, err := read(tc.tn, tc.id); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("%s core session = %v, want not found", name, err)
				}
			}
			if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", "user"); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A work-bound run's read carries its complete stored work
	// observation, so a target built only from the read passes the runtime target
	// validator, whose work check refuses a run whose binding and Work differ.
	for _, be := range managedStopBackends(t) {
		t.Run("work/"+be.name, func(t *testing.T) {
			ctx := context.Background()
			f, _ := newManagedStopWorkFixture(t, be.config(t))
			managed, _, _, _ := f.boundRun(t)
			d, err := f.m.getRun(ctx, f.tenant, managed.RunRef)
			if err != nil || d.CoreSessionID == "" {
				t.Fatalf("work run core_session_id = %q (%v)", d.CoreSessionID, err)
			}
			var got CoreSessionRun
			if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
				got, err = f.m.ReadCoreSessionRunInScope(ctx, sc, model.ID(d.CoreSessionID))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			stored := runtimeInputTargetForTest(t, f, managed.RunRef)
			if got.Work == nil || stored.Work == nil || *got.Work != *stored.Work || got.RuntimeLaunchID != stored.ExpectedLaunch || got.SessionSID != stored.ExpectedSID {
				t.Fatalf("work read = %+v work %+v, want the stored observation %+v", got.RuntimeLaunchIdentity, got.Work, stored.Work)
			}
			target := RuntimeInputTarget{Tenant: got.Tenant, WorkspaceID: got.WorkspaceID, RunRef: got.RunRef,
				ExpectedLaunch: got.RuntimeLaunchID, ExpectedSID: got.SessionSID, Work: got.Work}
			inRuntimeTargetScope(t, f, target, func(sc store.Scope) {
				if id, err := f.m.RuntimeCoreSessionInScope(ctx, sc, target); err != nil || id.String() != d.CoreSessionID {
					t.Fatalf("the validator on the read's own target = %q (%v), want %s", id, err, d.CoreSessionID)
				}
			})
			// An incomplete stamp (a launch spec hash alone, one field
			// missing) and a stamp whose item has no lease are unavailable evidence.
			stamp := map[string]any{}
			if err := mutateRunForWorkTest(f.m, f.tenant, managed.RunRef, func(rec model.Record) {
				for _, c := range []string{colRunWorkItemID, colRunWorkLeaseFence, colRunWorkDispatchKey, colRunWorkOwnerEpoch} {
					stamp[c] = rec[c]
				}
			}); err != nil {
				t.Fatal(err)
			}
			other := model.NewID()
			otherKey := runtimeWorkDispatchKey(other, got.Work.OwnerEpoch, got.Work.LeaseFence)
			for name, change := range map[string]func(model.Record){
				"hash only": func(rec model.Record) {
					rec[colRunWorkItemID], rec[colRunWorkLeaseFence], rec[colRunWorkDispatchKey], rec[colRunWorkOwnerEpoch] = nil, nil, nil, nil
				},
				"partial stamp": func(rec model.Record) { rec[colRunWorkOwnerEpoch] = nil },
				"missing lease": func(rec model.Record) {
					rec[colRunWorkItemID], rec[colRunWorkDispatchKey] = other.String(), otherKey[:]
				},
			} {
				if err := mutateRunForWorkTest(f.m, f.tenant, managed.RunRef, func(rec model.Record) {
					for c, v := range stamp {
						rec[c] = v
					}
					change(rec)
				}); err != nil {
					t.Fatal(err)
				}
				var rerr error
				if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
					_, rerr = f.m.ReadCoreSessionRunInScope(ctx, sc, model.ID(d.CoreSessionID))
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if we := asWorkError(rerr); we == nil || we.code != "evidence_unavailable" {
					t.Errorf("%s: read = %v, want evidence_unavailable", name, rerr)
				}
			}
			if err := mutateRunForWorkTest(f.m, f.tenant, managed.RunRef, func(rec model.Record) {
				for c, v := range stamp {
					rec[c] = v
				}
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
