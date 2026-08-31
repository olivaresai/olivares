// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/deploy"
)

// alwaysMint is a trivial in-test short-lived credential source (the live WIF/SPIFFE
// client is here we only need a non-deny source to exercise the seam).
func alwaysMint() executor.CredentialSource {
	return executor.MintFunc(func(_ context.Context, req executor.MintRequest) (executor.Credential, error) {
		return executor.Credential{ID: "test:" + req.Mode.String(), Token: "tkn", NotAfter: time.Now().Add(time.Hour), Scheme: "test"}, nil
	})
}

// fakeObserveBackend is a minimal executor.Backend that returns a configured
// RealState from Observe (the other methods are unused by the Verify path).
type fakeObserveBackend struct{ real executor.RealState }

func (f fakeObserveBackend) Kind() string { return "fake" }
func (f fakeObserveBackend) Plan(context.Context, executor.Desired, executor.Credential) (executor.Plan, error) {
	return executor.Plan{Runtime: "fake"}, nil
}
func (f fakeObserveBackend) DestroyPlan(context.Context, executor.Desired, executor.Credential) (executor.Plan, error) {
	return executor.Plan{Runtime: "fake", Intent: executor.IntentDestroy}, nil
}
func (f fakeObserveBackend) Apply(context.Context, executor.Plan, executor.Credential) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f fakeObserveBackend) Rollback(context.Context, executor.Plan, executor.Credential) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f fakeObserveBackend) Observe(context.Context, executor.Desired, executor.Credential) (executor.RealState, error) {
	return f.real, nil
}

func seamVerify(t *testing.T, real executor.RealState) deploy.ExecResult {
	t.Helper()
	a := &deployExecutor{e: executor.New(executor.WithBackend(fakeObserveBackend{real: real}, "fake"), executor.WithCredentialSource(alwaysMint()))}
	res, err := a.Verify(context.Background(), deploy.ExecRequest{Runtime: "fake", Environment: "prod", SubjectRef: "bot"})
	if err != nil {
		t.Fatalf("verify err = %v", err)
	}
	return res
}

func TestSeamVerifyHonestGapWhenUnobservable(t *testing.T) {
	res := seamVerify(t, executor.RealState{Observable: false, Detail: "daemon unreachable"})
	if len(res.Changes) == 0 {
		t.Fatalf("an unobservable unit must surface a non-sync change (never a silent in-sync)")
	}
}

func TestSeamVerifyNonSyncWithEmptyDrift(t *testing.T) {
	// Observable + !InSync but the backend enumerated no specific drift items: the seam
	// must still surface a non-sync change so the module does not record a false in-sync.
	res := seamVerify(t, executor.RealState{Observable: true, InSync: false})
	if len(res.Changes) == 0 {
		t.Fatalf("Observable && !InSync with empty Drift must still report drift (InSync is authoritative)")
	}
}

func TestSeamVerifyInSyncReportsNoChanges(t *testing.T) {
	res := seamVerify(t, executor.RealState{Observable: true, InSync: true})
	if len(res.Changes) != 0 {
		t.Fatalf("an in-sync unit must report zero changes, got %d", len(res.Changes))
	}
}

// --- loader: credential-source selection + blast-radius policy mapping -----------

func TestCredentialSourceSelectionDenyClosed(t *testing.T) {
	// Anything other than an explicitly-configured "file" source is deny-closed.
	for _, kind := range []string{"", "oidc", "wif", "spiffe"} {
		src := credentialCfgJSON{Kind: kind}.source()
		if _, ok := src.(executor.DenyCredentialSource); !ok {
			t.Fatalf("kind %q must map to the deny-closed default, got %T", kind, src)
		}
	}
	// "file" with no path is still deny-closed (NewFileTokenSource guards it).
	if _, ok := (credentialCfgJSON{Kind: "file"}).source().(executor.DenyCredentialSource); !ok {
		t.Fatalf("file source with no path must be deny-closed")
	}
	// "file" with a path is a real (non-deny) source.
	if _, ok := (credentialCfgJSON{Kind: "file", PathTemplate: "/tmp/{env}.token"}).source().(executor.DenyCredentialSource); ok {
		t.Fatalf("a configured file source must NOT be the deny-closed default")
	}
}

func TestBlastRadiusPolicyMapping(t *testing.T) {
	if _, ok := (*blastRadiusCfgJSON)(nil).policy(); ok {
		t.Fatalf("a nil blast-radius config must yield ok=false (use the default policy)")
	}
	allow := false
	p, ok := (&blastRadiusCfgJSON{MaxApplyDestructive: 3, AllowDestroy: &allow, MaxDestroyItems: 7}).policy()
	if !ok || p.MaxApplyDestructive != 3 || p.AllowDestroy || p.MaxDestroyItems != 7 {
		t.Fatalf("policy mapping wrong: %+v ok=%v", p, ok)
	}
	// AllowDestroy omitted => defaults to true.
	p2, _ := (&blastRadiusCfgJSON{}).policy()
	if !p2.AllowDestroy {
		t.Fatalf("AllowDestroy must default to true when omitted")
	}
}
