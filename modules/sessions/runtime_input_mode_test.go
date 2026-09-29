// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func runtimeInputModeVersionsForTest(ctx context.Context, sc store.Scope, target RuntimeInputTarget) ([]int64, error) {
	claim, found, err := findClaim(ctx, sc, target.ExpectedSID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("fixture Claim is absent")
	}
	versions := []int64{claim.Int(model.ColVersion)}
	if target.Work != nil {
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return nil, err
		}
		item, err := items.Get(ctx, target.Work.WorkItemID)
		if err != nil {
			return nil, err
		}
		lease, found, err := findWorkLease(ctx, sc, target.Work.WorkItemID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("fixture work lease is absent")
		}
		versions = append(versions, item.Int(model.ColVersion), lease.Int(model.ColVersion))
	}
	return versions, nil
}

// The caller establishes actual authn/entry/run authority before using the seam.
// Expected validation errors deliberately do NOT abort this test transaction:
// a rollback must not conceal an early Claim or work write. The recorder stand-in
// runs only after a known successful commit and successful mode validation.
func runtimeInputModeAdmissionForTest(t *testing.T, f *managedStopFixture, request RuntimeInputRequest, record func()) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := runtimeInputTargetForTest(t, f, request.Target.RunRef)
	var validationErr error
	var before []int64
	err := f.m.data.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		now, err := sc.(store.TransactionClock).TransactionNow(ctx)
		if err != nil {
			return err
		}
		principal := request.Authority.Entry.Principal
		evidence, ok := principal.AuthenticationEvidence()
		if !ok {
			return ErrRuntimeInputAuthority
		}
		authentication, err := evidence.AuthorityFor(now.Time(), principal, f.tenant)
		if err != nil {
			return err
		}
		authorization, err := request.Authority.AuthorityFor(now.Time(), request.Target)
		if err != nil {
			return err
		}
		bundle, err := auth.MergeAuthoritySnapshotBundles(authentication, authorization)
		if err != nil {
			return err
		}
		if err := sc.(store.DirectoryAuthoritySnapshotLocker).LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
			return err
		}
		before, err = runtimeInputModeVersionsForTest(ctx, sc, original)
		if err != nil {
			return err
		}
		validationErr = f.m.ValidateRuntimeInputModeInScope(ctx, sc, request.Target, request.Mode)
		if validationErr != nil {
			after, err := runtimeInputModeVersionsForTest(ctx, sc, original)
			if err != nil {
				return err
			}
			if !slices.Equal(before, after) {
				t.Errorf("refused mode changed Claim/work before commit: %v -> %v", before, after)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if validationErr != nil {
		if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
			after, err := runtimeInputModeVersionsForTest(ctx, sc, original)
			if err == nil && !slices.Equal(before, after) {
				t.Errorf("refused mode committed Claim/work changes: %v -> %v", before, after)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return validationErr
	}
	record()
	return nil
}

func TestRuntimeInputModeStreamJSONBeforeRecording(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, proc, request := runtimeInputRawFixtureWith(t, be.config(t))
			recorded := 0
			record := func() { recorded++ }
			for _, mode := range []RuntimeInputMode{RuntimeInputText, "", "paste", "literal", "agent_message", "RAW"} {
				bad := request
				bad.Mode = mode
				if err := runtimeInputModeAdmissionForTest(t, f, bad, record); !errors.Is(err, ErrRuntimeInputUnsupported) {
					t.Fatalf("mode %q: %v", mode, err)
				}
			}
			if recorded != 0 || proc.sentCount() != 0 {
				t.Fatal("unsupported mode reached recorder or stream-json process")
			}
			if err := runtimeInputModeAdmissionForTest(t, f, request, record); err != nil || recorded != 1 || proc.sentCount() != 0 {
				t.Fatalf("raw preflight must permit recording but never send: records=%d err=%v", recorded, err)
			}
			for _, mutate := range []func(*RuntimeInputTarget){
				func(target *RuntimeInputTarget) { target.ExpectedLaunch = model.NewID() },
				func(target *RuntimeInputTarget) { target.ExpectedSID = newSID() },
			} {
				bad := request
				mutate(&bad.Target)
				if err := runtimeInputModeAdmissionForTest(t, f, bad, record); !errors.Is(err, ErrRuntimeInputTarget) {
					t.Fatalf("stale original was admitted: %v", err)
				}
			}
			if err := mutateRunForWorkTest(f.m, f.tenant, request.Target.RunRef, func(row model.Record) { row[colTransport] = string(TransportRemoteControl) }); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []RuntimeInputMode{RuntimeInputRaw, RuntimeInputText} {
				request.Mode = mode
				if err := runtimeInputModeAdmissionForTest(t, f, request, record); !errors.Is(err, ErrRuntimeInputUnsupported) {
					t.Fatalf("remote-control mode %q: %v", mode, err)
				}
			}
			if recorded != 1 || proc.sentCount() != 0 {
				t.Fatal("stale or remote target reached recording or transport")
			}
		})
	}
}

func TestRuntimeInputModeOwnedDriverBeforeRecording(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			dto, _ := f.launch("mode-preflight-owned-driver")
			target := runtimeInputTargetForTest(t, f, dto.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw}
			path := filepath.Join(f.profile.ConfigHome, codexFixtureRecordFile)
			before := readFixtureRecord(t, path)
			recorded := 0
			record := func() { recorded++ }
			if err := runtimeInputModeAdmissionForTest(t, f, request, record); !errors.Is(err, ErrRuntimeInputUnsupported) || recorded != 0 {
				t.Fatalf("raw bytes were accepted as a provider turn: %v", err)
			}
			request.Mode = RuntimeInputText
			if err := runtimeInputModeAdmissionForTest(t, f, request, record); err != nil || recorded != 1 {
				t.Fatalf("owned-driver Text preflight: %v", err)
			}
			if after := readFixtureRecord(t, path); !reflect.DeepEqual(before, after) {
				t.Fatalf("preflight invoked the driver: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestRuntimeInputModeWorkAndCallerAuthority(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newManagedStopWorkFixture(t, be.config(t))
			dto, _, _, _ := f.boundRun(t)
			target := runtimeInputTargetForTest(t, f, dto.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputText}
			recorded := 0
			record := func() { recorded++ }
			if err := runtimeInputModeAdmissionForTest(t, f, request, record); !errors.Is(err, ErrRuntimeInputUnsupported) {
				t.Fatalf("wrong work input mode: %v", err)
			}
			request.Mode = RuntimeInputRaw
			bad := request
			bad.Authority.RunAuthorization = auth.RouteMutationAuthorization{}
			if err := runtimeInputModeAdmissionForTest(t, f, bad, record); err == nil || recorded != 0 {
				t.Fatal("caller without run authority reached recording")
			}
			if err := runtimeInputModeAdmissionForTest(t, f, request, record); err != nil || recorded != 1 {
				t.Fatalf("valid work raw preflight: %v", err)
			}
			before := runtimeInputClaimVersionForTest(t, f, target)
			p := request.Authority.Entry.Principal
			if err := f.authr.RevokeSession(context.Background(), p, p.CredID); err != nil {
				t.Fatal(err)
			}
			if err := runtimeInputModeAdmissionForTest(t, f, request, record); err == nil || recorded != 1 {
				t.Fatal("revoked caller reached recording")
			}
			if runtimeInputClaimVersionForTest(t, f, target) != before || runner.lastProc().sentCount() != 0 {
				t.Fatal("refused caller touched Claim or preflight sent input")
			}
		})
	}
}

func TestRuntimeInputModeRequiresScopeAndClosedMode(t *testing.T) {
	f, proc, request := runtimeInputRawFixture(t)
	for _, mode := range []RuntimeInputMode{RuntimeInputText, RuntimeInputRaw} {
		if err := f.m.ValidateRuntimeInputModeInScope(context.Background(), nil, request.Target, mode); !errors.Is(err, ErrRuntimeInputTarget) {
			t.Fatalf("missing scope mode %q: %v", mode, err)
		}
	}
	// Unknown discriminants cannot reach the helper, even with no module/scope.
	var absent *Module
	for _, mode := range []RuntimeInputMode{"", "paste", "agent_message"} {
		if err := absent.ValidateRuntimeInputModeInScope(nil, nil, request.Target, mode); !errors.Is(err, ErrRuntimeInputUnsupported) {
			t.Fatalf("open mode discriminant %q: %v", mode, err)
		}
	}
	if proc.sentCount() != 0 {
		t.Fatal("missing scope attempted input")
	}
}
