// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The policy engine is the external decision seam. Its version is read from
// the real store; principal reconstruction, mutation proofs and account locks
// are real. The neutral entry question stands in for its eventual owner.
func runtimeInputAuthorityForTest(t *testing.T, f *managedStopFixture, target RuntimeInputTarget) RuntimeInputAuthority {
	t.Helper()
	return runtimeInputAuthorityForActionTest(t, f, target, "input:write")
}

func runtimeInputAuthorityForActionTest(t *testing.T, f *managedStopFixture, target RuntimeInputTarget, action string) RuntimeInputAuthority {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := f.operator("input-operator-"+model.NewID().String()+"@runtime.test", auth.RoleEditor, true, 30*time.Second)
	ref, _ := p.Ref()
	p, err := f.authr.ResolvePrincipalScope(ctx, ref, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	var fact store.AuthorizationFactRef
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		var err error
		fact, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	scoped := &communicationAuthorityScopedEvidenceAuthorizer{decision: auth.ScopedEvidenceDecision{
		Effect:        auth.EffectGrant,
		ResourceGuard: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "resource_guard_clean"},
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "scoped_forbid_absent"},
		Facts:         []store.AuthorizationFactRef{fact}, ObservedAt: now.Add(-time.Second), FreshUntil: now.Add(time.Minute),
	}}
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped))
	entry := auth.Request{Principal: p, Tenant: f.tenant, Permission: permRunWrite,
		Resource: auth.ResourceAttrs{Kind: "run", ID: target.RunRef, WorkspaceID: target.WorkspaceID},
		Route:    auth.RouteMetadata{CedarAction: action, MinimumAAL: auth.AAL3, RequireScopedGrant: true, RBACMinimumRole: auth.RoleEditor}}
	entryProof, err := az.AuthorizeRouteMutation(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	runProof, err := az.AuthorizeRouteMutation(ctx, RuntimeInputRunQuestion(p, target))
	if err != nil {
		t.Fatal(err)
	}
	// This harness freezes the runtime clock at construction; proof issuance
	// used the real authenticator clock and must not appear to be in the future.
	f.clock.set(time.Now().UTC())
	return RuntimeInputAuthority{Entry: entry, EntryAuthorization: entryProof, RunAuthorization: runProof}
}

func runtimeInputTargetForTest(t *testing.T, f *managedStopFixture, runRef string) RuntimeInputTarget {
	t.Helper()
	live, ok := f.m.rt.getLive(f.tenant, runRef)
	if !ok {
		t.Fatal("run is not live")
	}
	var target RuntimeInputTarget
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(context.Background(), repo, runRef)
		if err != nil {
			return err
		}
		target = RuntimeInputTarget{Tenant: f.tenant, WorkspaceID: model.ID(rec.String(colRunAuthzWorkspaceID)), RunRef: runRef, ExpectedLaunch: live.launchID, ExpectedSID: live.claim.SID}
		if !runHasWorkBinding(rec) {
			return nil
		}
		lease, found, err := findWorkLease(context.Background(), sc, model.ID(rec.String(colRunWorkItemID)))
		if err != nil {
			return err
		}
		if !found {
			return errors.New("missing work lease")
		}
		target.Work = &RuntimeInputWork{WorkItemID: model.ID(rec.String(colRunWorkItemID)), LeaseFence: rec.Int(colRunWorkLeaseFence), OwnerEpoch: rec.Int(colRunWorkOwnerEpoch), LeaseExpiresAt: lease.String(colLeaseExpiresAt), HolderSID: lease.String(colLeaseHolderSID), HolderRunRef: lease.String(colLeaseHolderRunRef), HolderAgentID: model.ID(lease.String(colLeaseHolderAgentRef))}
		copy(target.Work.DispatchKey[:], rec.Bytes(colRunWorkDispatchKey))
		copy(target.Work.LaunchSpecHash[:], rec.Bytes(colRunWorkLaunchSpecHash))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestRuntimeInputOriginalTargetAndUnknownAfterWrite(t *testing.T) {
	runner := &workControlRunner{}
	f := newManagedStopFixtureWith(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, WithRunner(runner), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, f.m, context.Background(), f.tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:launch", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	target := runtimeInputTargetForTest(t, f, dto.RunRef)
	authority := runtimeInputAuthorityForTest(t, f, target)
	request := RuntimeInputRequest{Target: target, Authority: authority, Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
	proc := runner.lastProc(t)
	for _, mutate := range []func(*RuntimeInputRequest){
		func(r *RuntimeInputRequest) { r.Target.ExpectedLaunch = model.NewID() },
		func(r *RuntimeInputRequest) { r.Target.WorkspaceID = model.NewID() },
		func(r *RuntimeInputRequest) { r.Target.Tenant = model.NewTenantID() },
		func(r *RuntimeInputRequest) { r.Authority.Entry.Principal.UserID = model.NewID() },
		func(r *RuntimeInputRequest) { r.Authority.RunAuthorization = auth.RouteMutationAuthorization{} },
		func(r *RuntimeInputRequest) { r.Text = "mixed" },
	} {
		bad := request
		mutate(&bad)
		result, err := f.m.DeliverRuntimeInput(context.Background(), bad)
		if err == nil || result.Attempted || proc.sentCount() != 0 {
			t.Fatalf("invalid input crossed: %#v %v", result, err)
		}
	}
	result, err := f.m.DeliverRuntimeInput(context.Background(), request)
	if err != nil || result.Outcome != RuntimeInputAccepted || !result.Attempted || proc.sentCount() != 1 {
		t.Fatalf("lawful input: %#v %v", result, err)
	}
	proc.setSendErrorAfterWrite(errors.New("response lost after write"))
	result, err = f.m.DeliverRuntimeInput(context.Background(), request)
	if err == nil || result.Outcome != RuntimeInputUnknown || !result.Attempted || proc.sentCount() != 2 {
		t.Fatalf("unknown input: %#v %v", result, err)
	}
}

func TestRuntimeInputWorkSealAndNoNewWorkPermission(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newManagedStopWorkFixture(t, be.config(t))
			managed, _, _, _ := f.boundRun(t)
			target := runtimeInputTargetForTest(t, f, managed.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
			for _, mutate := range []func(*RuntimeInputWork){
				func(w *RuntimeInputWork) { w.LeaseFence++ },
				func(w *RuntimeInputWork) { w.OwnerEpoch++ },
				func(w *RuntimeInputWork) { w.WorkItemID = model.NewID() },
				func(w *RuntimeInputWork) { w.HolderSID = "other" },
				func(w *RuntimeInputWork) { w.HolderRunRef = "other" },
				func(w *RuntimeInputWork) { w.HolderAgentID = model.NewID() },
				func(w *RuntimeInputWork) { w.LeaseExpiresAt = "2000-01-01T00:00:00Z" },
				func(w *RuntimeInputWork) { w.DispatchKey[0] ^= 1 },
				func(w *RuntimeInputWork) { w.LaunchSpecHash[0] ^= 1 },
			} {
				bad := request
				work := *target.Work
				bad.Target.Work = &work
				mutate(&work)
				result, err := f.m.DeliverRuntimeInput(context.Background(), bad)
				if err == nil || result.Attempted || runner.lastProc().sentCount() != 0 {
					t.Fatalf("stale work crossed: %#v %v", result, err)
				}
			}
			bad := request
			bad.Target.Work = nil
			if result, err := f.m.DeliverRuntimeInput(context.Background(), bad); err == nil || result.Attempted {
				t.Fatalf("bound run accepted without work: %#v %v", result, err)
			}
			result, err := f.m.DeliverRuntimeInput(context.Background(), request)
			if err != nil || result.Outcome != RuntimeInputAccepted || runner.lastProc().sentCount() != 1 {
				t.Fatalf("editor input required extra work authority: %#v %v", result, err)
			}
		})
	}
}

func runtimeInputRawFixture(t *testing.T) (*managedStopFixture, *workControlProc, RuntimeInputRequest) {
	t.Helper()
	return runtimeInputRawFixtureWith(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
}

func runtimeInputRawFixtureWith(t *testing.T, cfg store.Config) (*managedStopFixture, *workControlProc, RuntimeInputRequest) {
	t.Helper()
	runner := &workControlRunner{}
	f := newManagedStopFixtureWith(t, cfg, WithRunner(runner), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, f.m, context.Background(), f.tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:launch", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	target := runtimeInputTargetForTest(t, f, dto.RunRef)
	return f, runner.lastProc(t), RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
}

func TestRuntimeInputRechecksChangedAccountCredentialAndGrants(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			for _, change := range []string{"credential", "account", "grant"} {
				t.Run(change, func(t *testing.T) {
					f, proc, request := runtimeInputRawFixtureWith(t, be.config(t))
					ctx := context.Background()
					p := request.Authority.Entry.Principal
					var err error
					switch change {
					case "credential":
						err = f.authr.RevokeSession(ctx, p, p.CredID)
					case "account":
						admin := f.admin()
						err = f.st.AuthMutate(ctx, func(sc store.AuthScope) error {
							_, err := f.authr.OffboardFromTenant(ctx, sc, admin, p.UserID, f.tenant, "input authority retirement")
							return err
						})
					case "grant":
						err = f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
							epoch := sc.(store.AuthorizationEpochStore)
							fact, err := epoch.ReadAuthorizationEpoch(ctx)
							if err != nil {
								return err
							}
							_, err = epoch.BumpAuthorizationEpoch(ctx, fact)
							return err
						})
					}
					if err != nil {
						t.Fatal(err)
					}
					result, err := f.m.DeliverRuntimeInput(ctx, request)
					if err == nil || result.Attempted || proc.sentCount() != 0 {
						t.Fatalf("changed %s crossed: %#v %v", change, result, err)
					}
				})
			}
		})
	}
}

func TestRuntimeInputOriginalAdmissionDoesNotFollowAResume(t *testing.T) {
	runner := &fakeRunner{initSID: "input-original-session"}
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
	target := RuntimeInputTarget{Tenant: original.Tenant, WorkspaceID: original.WorkspaceID, RunRef: original.RunRef, ExpectedLaunch: original.RuntimeLaunchID, ExpectedSID: original.SessionSID}
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
		return f.m.ValidateRuntimeInputTargetInScope(ctx, sc, target)
	}); err != nil {
		t.Fatalf("original admission: %v", err)
	}
	waitFor(t, "original resume identity", func() bool { r, _ := f.m.getRun(ctx, f.tenant, original.RunRef); return r.ClaudeSessionID != "" })
	if _, err := f.m.stopRun(ctx, f.tenant, original.RunRef, "user:launch", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	successor, err := f.m.ResumeRunWithCompletion(ctx, f.tenant, original.RunRef, "user:launch", model.ActorUser, "")
	if err != nil {
		t.Fatal(err)
	}
	next, ok := successor.Identity()
	if !ok || next.RuntimeLaunchID == original.RuntimeLaunchID {
		t.Fatal("resume did not issue a distinct completion")
	}
	result, err := f.m.DeliverRuntimeInput(ctx, RuntimeInputRequest{Target: target, Authority: authority, Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)})
	if err == nil || result.Attempted || runner.lastProc().sentCount() != 0 {
		t.Fatalf("old admission reached successor: %#v %v", result, err)
	}
}

func TestRuntimeInputRejectsExpiredOrReleasedWork(t *testing.T) {
	for _, change := range []string{"work-expiry", "work-release", "claim-release"} {
		t.Run(change, func(t *testing.T) {
			f, runner := newManagedStopWorkFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
			managed, _, _, lease := f.boundRun(t)
			target := runtimeInputTargetForTest(t, f, managed.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
			ctx := context.Background()
			live, _ := f.m.rt.getLive(f.tenant, managed.RunRef)
			switch change {
			case "claim-release":
				if err := f.m.Release(ctx, f.tenant, live.claim.SID, live.claim.Holder, live.claim.Fence); err != nil {
					t.Fatal(err)
				}
			case "work-release":
				var version int64
				if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(workItemKind)
					if err != nil {
						return err
					}
					item, err := repo.Get(ctx, lease.WorkItemID)
					version = item.Int(model.ColVersion)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				holder := WorkPrincipal{ActorKind: model.ActorAgent, ActorRef: lease.HolderAgentRef, Actor: lease.HolderAgentRef, SessionID: lease.HolderSID, SessionRunRef: lease.HolderRunRef, SessionFence: live.claim.Fence, PurposeRestricted: true}
				if _, err := f.m.Apply(ctx, f.tenant, holder, WorkCommand{Command: "lease.release", WorkItemID: lease.WorkItemID, HolderSID: lease.HolderSID, HolderRunRef: lease.HolderRunRef, HolderAgentRef: lease.HolderAgentRef, Fence: lease.Fence, ExpectedVersion: version, Reason: "input test release", IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost}); err != nil {
					t.Fatal(err)
				}
			case "work-expiry":
				// Corrupt neither fence nor holder: expire the actual durable lease.
				err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(workLeaseKind)
					if err != nil {
						return err
					}
					row, err := repo.Get(ctx, lease.ID)
					if err != nil {
						return err
					}
					past := time.Now().UTC().Add(-2 * time.Minute)
					row[colLeaseAcquiredAt] = model.NewTimestamp(past).String()
					row[colLeaseRenewedAt] = model.NewTimestamp(past.Add(30 * time.Second)).String()
					row[colLeaseExpiresAt] = model.NewTimestamp(past.Add(time.Minute)).String()
					row[colLeaseRenewalCount] = row.Int(colLeaseRenewalCount) + 1
					_, err = repo.Update(ctx, row)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.m.DeliverRuntimeInput(ctx, request)
			if err == nil || result.Attempted || runner.lastProc().sentCount() != 0 {
				t.Fatalf("ended %s crossed: %#v %v", change, result, err)
			}
		})
	}
}

// Only the commit acknowledgement is faulted. The callback still runs against
// the real engine and commits; even that ambiguity must not start an external write.
type runtimeInputLostCommitData struct {
	api.ModuleData
	once sync.Once
}

func (d *runtimeInputLostCommitData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.ModuleData.Mutate(ctx, tenant, fn)
	if err != nil {
		return err
	}
	lost := false
	d.once.Do(func() { lost = true })
	if lost {
		return store.ErrCommitOutcomeUnknown
	}
	return nil
}

func TestRuntimeInputCancellationAndCommitUncertaintyDoNotWrite(t *testing.T) {
	for _, failure := range []string{"cancelled", "unknown-commit"} {
		t.Run(failure, func(t *testing.T) {
			f, proc, request := runtimeInputRawFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancelled" {
				cancel()
			} else {
				f.m.Data = &runtimeInputLostCommitData{ModuleData: f.m.Data}
			}
			result, err := f.m.DeliverRuntimeInput(ctx, request)
			if err == nil || result.Attempted || proc.sentCount() != 0 {
				t.Fatalf("%s crossed: %#v %v", failure, result, err)
			}
			if failure == "unknown-commit" && (result.Outcome != RuntimeInputUnknown || !errors.Is(err, store.ErrCommitOutcomeUnknown)) {
				t.Fatalf("commit uncertainty collapsed to refusal: %#v %v", result, err)
			}
		})
	}
}

func runtimeInputClaimVersionForTest(t *testing.T, f *managedStopFixture, target RuntimeInputTarget) int64 {
	t.Helper()
	live, ok := f.m.rt.getLive(target.Tenant, target.RunRef)
	if !ok || live.claim.SID == "" {
		t.Fatal("target has no live Claim")
	}
	var version int64
	if err := f.st.View(context.Background(), target.Tenant, func(sc store.Scope) error {
		rec, found, err := findClaim(context.Background(), sc, live.claim.SID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("target Claim is absent")
		}
		version = rec.Int(model.ColVersion)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if version < 1 {
		t.Fatalf("target Claim has no durable version: %d", version)
	}
	return version
}

func TestRuntimeInputWrongSIDRefusesBeforeClaimAndTransport(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, proc, request := runtimeInputRawFixtureWith(t, be.config(t))
			before := runtimeInputClaimVersionForTest(t, f, request.Target)
			for _, sid := range []string{"", "not-a-canonical-session", newSID()} {
				changed := request
				changed.Target.ExpectedSID = sid
				result, err := f.m.DeliverRuntimeInput(context.Background(), changed)
				if !errors.Is(err, ErrRuntimeInputTarget) || result.Attempted || result.Outcome != RuntimeInputRefused || proc.sentCount() != 0 {
					t.Fatalf("foreign SID reached original run: %#v %v", result, err)
				}
				if after := runtimeInputClaimVersionForTest(t, f, request.Target); after != before {
					t.Fatalf("foreign SID changed Claim version: %d -> %d", before, after)
				}
			}
			result, err := f.m.DeliverRuntimeInput(context.Background(), request)
			if err != nil || result.Outcome != RuntimeInputAccepted || !result.Attempted || proc.sentCount() != 1 {
				t.Fatalf("original SID could not deliver: %#v %v", result, err)
			}
		})
	}
}

func TestRuntimeInputTextUsesOwnedDriverAndRefusesRaw(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			dto, _ := f.launch("input-text-owned-driver")
			target := runtimeInputTargetForTest(t, f, dto.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"jsonrpc":"2.0","method":"arbitrary"}`)}
			recordPath := filepath.Join(f.profile.ConfigHome, codexFixtureRecordFile)
			beforePeer := readFixtureRecord(t, recordPath)
			beforeVersion := runtimeInputClaimVersionForTest(t, f, target)
			result, err := f.m.DeliverRuntimeInput(context.Background(), request)
			if !errors.Is(err, ErrRuntimeInputUnsupported) || result.Attempted || result.Outcome != RuntimeInputUnsupported {
				t.Fatalf("raw driver frame: %#v %v", result, err)
			}
			if after := runtimeInputClaimVersionForTest(t, f, target); after != beforeVersion {
				t.Fatalf("unsupported raw input changed Claim version: %d -> %d", beforeVersion, after)
			}
			request.Mode, request.Raw, request.Text = RuntimeInputText, nil, "hello through the owned driver"
			result, err = f.m.DeliverRuntimeInput(context.Background(), request)
			if err != nil || !result.Attempted || result.Outcome != RuntimeInputAccepted {
				t.Fatalf("driver text: %#v %v", result, err)
			}
			// The acknowledged text request orders the child's pipe: its record
			// must contain exactly that new request, with no earlier raw frame.
			peer := readFixtureRecord(t, recordPath)
			wantMethods := append(beforePeer.Methods, codexMethodTurnStart)
			if !slices.Equal(peer.Methods, wantMethods) || !slices.Equal(peer.Inputs, []string{request.Text}) {
				t.Fatalf("unsupported raw frame reached driver: methods=%v inputs=%v", peer.Methods, peer.Inputs)
			}
		})
	}
}

func TestRuntimeInputUnsupportedRawTransportAndTextMode(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, proc, request := runtimeInputRawFixtureWith(t, be.config(t))
			text := request
			text.Mode, text.Raw, text.Text = RuntimeInputText, nil, "text requires a protocol driver"
			beforeVersion := runtimeInputClaimVersionForTest(t, f, request.Target)
			if result, err := f.m.DeliverRuntimeInput(context.Background(), text); !errors.Is(err, ErrRuntimeInputUnsupported) || result.Attempted || result.Outcome != RuntimeInputUnsupported || proc.sentCount() != 0 {
				t.Fatalf("unowned text: %#v %v", result, err)
			}
			if after := runtimeInputClaimVersionForTest(t, f, request.Target); after != beforeVersion {
				t.Fatalf("unsupported text input changed Claim version: %d -> %d", beforeVersion, after)
			}
			if err := mutateRunForWorkTest(f.m, f.tenant, request.Target.RunRef, func(row model.Record) { row[colTransport] = string(TransportRemoteControl) }); err != nil {
				t.Fatal(err)
			}
			beforeVersion = runtimeInputClaimVersionForTest(t, f, request.Target)
			if result, err := f.m.DeliverRuntimeInput(context.Background(), request); !errors.Is(err, ErrRuntimeInputUnsupported) || result.Attempted || result.Outcome != RuntimeInputUnsupported || proc.sentCount() != 0 {
				t.Fatalf("remote input: %#v %v", result, err)
			}
			if after := runtimeInputClaimVersionForTest(t, f, request.Target); after != beforeVersion {
				t.Fatalf("unsupported remote input changed Claim version: %d -> %d", beforeVersion, after)
			}
		})
	}
}

func TestRuntimeInputCancellationAfterWriteRemainsUnknown(t *testing.T) {
	f, proc, request := runtimeInputRawFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc.setAfterSend(cancel)
	proc.setSendErrorAfterWrite(context.Canceled)
	result, err := f.m.DeliverRuntimeInput(ctx, request)
	if !errors.Is(err, ErrRuntimeInputUnknown) || result.Outcome != RuntimeInputUnknown || !result.Attempted || proc.sentCount() != 1 {
		t.Fatalf("post-write cancellation lost uncertainty: %#v %v", result, err)
	}
}

func TestRuntimeInputLostClaimAfterWriteRecordsWorkAmbiguity(t *testing.T) {
	runner := &workControlRunner{}
	f := newManagedStopFixtureWith(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true},
		WithRunner(runner), WithCredentialSource(staticCred()), WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}))
	managed, _, _, _ := f.boundRun(t)
	target := runtimeInputTargetForTest(t, f, managed.RunRef)
	request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
	live, _ := f.m.rt.getLive(f.tenant, managed.RunRef)
	var hookErr error
	runner.lastProc(t).setAfterSend(func() {
		hookErr = f.m.Release(context.Background(), f.tenant, live.claim.SID, live.claim.Holder, live.claim.Fence)
	})
	result, err := f.m.DeliverRuntimeInput(context.Background(), request)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !errors.Is(err, ErrRuntimeInputUnknown) || result.Outcome != RuntimeInputUnknown || !result.Attempted || runner.lastProc(t).sentCount() != 1 {
		t.Fatalf("lost custody became success: %#v %v", result, err)
	}
	if countNamedRunEvents(t, f.st, f.tenant, managed.RunRef, workInputAccepted) != 0 || countNamedRunEvents(t, f.st, f.tenant, managed.RunRef, workInputAmbiguous) != 1 {
		t.Fatal("lost custody was not durably classified ambiguous")
	}
}

func TestRuntimeWorkReplayDoesNotManufactureOriginalCompletion(t *testing.T) {
	f, _ := newManagedStopWorkFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	item, _, owner := f.readyBoundWorkItem(t)
	spec := workLaunchSpec(t, f.m, f.tenant, item, owner)
	first, err := f.m.LaunchForWork(context.Background(), f.tenant, spec)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := first.Completion.Identity()
	if !ok || identity.RunRef != first.RunRef {
		t.Fatal("originating work launch omitted completion")
	}
	replayed, err := f.m.LaunchForWork(context.Background(), f.tenant, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.RunRef != first.RunRef {
		t.Fatal("expected the exact work dispatch replay")
	}
	if _, ok := replayed.Completion.Identity(); ok {
		t.Fatal("dispatch replay manufactured an original completion")
	}
}

// A sound write proof for a different registered action is not input authority.
// No proof field is forged or edited after issuance in this case.
func TestRuntimeInputRejectsSoundMutationProofForAnotherAction(t *testing.T) {
	for _, action := range []string{"run:stop", ""} {
		t.Run("action="+action, func(t *testing.T) {
			f, proc, request := runtimeInputRawFixture(t)
			request.Authority = runtimeInputAuthorityForActionTest(t, f, request.Target, action)
			if _, err := request.Authority.EntryAuthorization.AuthorityFor(time.Now(), request.Authority.Entry); err != nil {
				t.Fatalf("alternate-action control did not carry a sound mutation proof: %v", err)
			}
			result, err := f.m.DeliverRuntimeInput(context.Background(), request)
			if !errors.Is(err, ErrRuntimeInputAuthority) || result.Attempted || proc.sentCount() != 0 {
				t.Fatalf("sound alternate-action proof reached input: %#v %v", result, err)
			}
			request.Authority = runtimeInputAuthorityForTest(t, f, request.Target)
			result, err = f.m.DeliverRuntimeInput(context.Background(), request)
			if err != nil || result.Outcome != RuntimeInputAccepted || !result.Attempted || proc.sentCount() != 1 {
				t.Fatalf("exact input action refused: %#v %v", result, err)
			}
		})
	}
}
