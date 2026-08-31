// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// mockCredSource mints a short-lived credential valid for ttl. Its ID is
// non-sensitive (carries the env+mode); its token is the (never-recorded) material.
func mockCredSource(ttl time.Duration) CredentialSource {
	return MintFunc(func(_ context.Context, req MintRequest) (Credential, error) {
		return Credential{
			ID:       "mock:" + req.Environment + ":" + req.Mode.String(),
			Token:    "SECRET-TOKEN-" + req.Mode.String(),
			NotAfter: nowFunc().Add(ttl),
			Scheme:   "mock",
		}, nil
	})
}

// fakeBackend is a deterministic in-memory backend for the orchestration tests.
type fakeBackend struct {
	kind        string
	planDiff    Diff
	destroyDiff Diff
	real        RealState
	applyErr    error
	applyCalls  int
	lastCred    Credential
}

func (f *fakeBackend) Kind() string { return f.kind }
func (f *fakeBackend) Plan(_ context.Context, _ Desired, cred Credential) (Plan, error) {
	f.lastCred = cred
	return Plan{Runtime: f.kind, Intent: IntentApply, Diff: f.planDiff, Handle: "h"}, nil
}
func (f *fakeBackend) DestroyPlan(_ context.Context, _ Desired, cred Credential) (Plan, error) {
	f.lastCred = cred
	return Plan{Runtime: f.kind, Intent: IntentDestroy, Diff: f.destroyDiff, Handle: "h"}, nil
}
func (f *fakeBackend) Apply(_ context.Context, p Plan, cred Credential) (Result, error) {
	if f.applyErr != nil {
		return Result{}, f.applyErr
	}
	f.applyCalls++
	f.lastCred = cred
	return Result{Applied: p.Diff.Items(), Detail: "applied"}, nil
}
func (f *fakeBackend) Rollback(context.Context, Plan, Credential) (Result, error) {
	return Result{}, nil
}
func (f *fakeBackend) Observe(_ context.Context, _ Desired, cred Credential) (RealState, error) {
	f.lastCred = cred
	return f.real, nil
}

func additiveDiff() Diff {
	return NewDiff([]ChangeItem{{Action: "create", Kind: "container", Ref: "acme-bot"}}, nil, nil, true, "", "1 create")
}
func destructiveDiff() Diff {
	return NewDiff(nil, nil, []ChangeItem{{Action: "delete", Kind: "container", Ref: "acme-bot", Destructive: true}}, false, "", "1 delete")
}

func desired(runtime string) Desired {
	return Desired{Tenant: "t1", Environment: "prod", Target: runtime + ".host/n1", Runtime: runtime, SubjectKind: "agent", SubjectRef: "acme-bot", Name: "bot", SpecHash: "abc"}
}

// --- credential deny-closed ------------------------------------------------------

func TestDenyCredentialSourceFailsClosed(t *testing.T) {
	_, err := DenyCredentialSource{}.Mint(context.Background(), MintRequest{Mode: ModeWrite})
	if !errors.Is(err, ErrNoCredentialSource) {
		t.Fatalf("deny source must return ErrNoCredentialSource, got %v", err)
	}
}

func TestApplyFailsClosedWithoutCredentialSource(t *testing.T) {
	fb := &fakeBackend{kind: "docker", planDiff: additiveDiff()}
	e := New(WithBackend(fb)) // default credential source is DenyCredentialSource
	_, err := e.Apply(context.Background(), desired("docker"))
	if !errors.Is(err, ErrNoCredentialSource) {
		t.Fatalf("apply without a credential source must fail closed with ErrNoCredentialSource, got %v", err)
	}
	if fb.applyCalls != 0 {
		t.Fatalf("backend.Apply must NEVER run without a credential (got %d calls)", fb.applyCalls)
	}
}

func TestExpiredCredentialRejected(t *testing.T) {
	fb := &fakeBackend{kind: "docker", planDiff: additiveDiff()}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(-time.Minute))) // already expired
	_, err := e.Apply(context.Background(), desired("docker"))
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired credential must be rejected, got %v", err)
	}
	if fb.applyCalls != 0 {
		t.Fatalf("backend.Apply must not run with an expired credential")
	}
}

// --- backend selection -----------------------------------------------------------

func TestNoBackendFailsClosed(t *testing.T) {
	e := New(WithCredentialSource(mockCredSource(time.Hour)))
	_, err := e.Apply(context.Background(), desired("nomad"))
	if !errors.Is(err, ErrNoBackend) {
		t.Fatalf("apply for an un-wired runtime must fail with ErrNoBackend, got %v", err)
	}
}

// --- idempotency + blast radius --------------------------------------------------

func TestApplyIdempotentNoop(t *testing.T) {
	fb := &fakeBackend{kind: "docker", planDiff: NewDiff(nil, nil, nil, true, "", "no changes")}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(time.Hour)))
	res, err := e.Apply(context.Background(), desired("docker"))
	if err != nil {
		t.Fatalf("noop apply err = %v", err)
	}
	if fb.applyCalls != 0 {
		t.Fatalf("an up-to-date spec must be an idempotent noop; backend.Apply ran %d times", fb.applyCalls)
	}
	if !strings.Contains(res.Detail, "desired state") {
		t.Fatalf("noop detail = %q", res.Detail)
	}
}

func TestApplyBlockedByBlastRadius(t *testing.T) {
	fb := &fakeBackend{kind: "docker", planDiff: destructiveDiff()}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(time.Hour))) // default policy: MaxApplyDestructive=0
	_, err := e.Apply(context.Background(), desired("docker"))
	if !errors.Is(err, ErrBlastRadius) {
		t.Fatalf("a destructive apply must be blocked by the blast-radius gate, got %v", err)
	}
	if fb.applyCalls != 0 {
		t.Fatalf("a gate-blocked apply must NEVER reach the backend (got %d calls)", fb.applyCalls)
	}
}

func TestApplyAdditiveSucceedsWithWriteCredential(t *testing.T) {
	fb := &fakeBackend{kind: "docker", planDiff: additiveDiff()}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(time.Hour)))
	res, err := e.Apply(context.Background(), desired("docker"))
	if err != nil {
		t.Fatalf("additive apply err = %v", err)
	}
	if fb.applyCalls != 1 {
		t.Fatalf("backend.Apply calls = %d, want 1", fb.applyCalls)
	}
	if !strings.HasSuffix(fb.lastCred.ID, ":write") {
		t.Fatalf("apply must use a WRITE-scoped credential, got id %q", fb.lastCred.ID)
	}
	if res.CredentialID != fb.lastCred.ID || res.BackendID != "docker" {
		t.Fatalf("result must carry the non-sensitive credential id and backend id, got %+v", res)
	}
}

// --- the credential MATERIAL never leaks into a Result --------------------------

func TestCredentialMaterialNeverInResult(t *testing.T) {
	fb := &fakeBackend{kind: "docker", planDiff: additiveDiff()}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(time.Hour)))
	res, err := e.Apply(context.Background(), desired("docker"))
	if err != nil {
		t.Fatal(err)
	}
	// The id may be recorded; the token material must NOT appear anywhere in Result.
	if strings.Contains(res.CredentialID, "SECRET-TOKEN") || strings.Contains(res.Detail, "SECRET-TOKEN") {
		t.Fatalf("credential material leaked into Result: %+v", res)
	}
	for _, it := range res.Applied {
		if strings.Contains(it.Ref+it.Detail, "SECRET-TOKEN") {
			t.Fatalf("credential material leaked into a change item: %+v", it)
		}
	}
}

// --- verify (read-only) ----------------------------------------------------------

func TestVerifyObservesWithReadCredential(t *testing.T) {
	drift := []ChangeItem{{Action: "update", Kind: "container", Ref: "acme-bot", Detail: "drift"}}
	fb := &fakeBackend{kind: "docker", real: RealState{Exists: true, Observable: true, InSync: false, Drift: drift}}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(time.Hour)))
	rs, err := e.Verify(context.Background(), desired("docker"))
	if err != nil {
		t.Fatal(err)
	}
	if rs.InSync || len(rs.Drift) != 1 {
		t.Fatalf("verify should report drift, got %+v", rs)
	}
	if !strings.HasSuffix(fb.lastCred.ID, ":read") {
		t.Fatalf("verify must use a READ-scoped credential, got id %q", fb.lastCred.ID)
	}
}

// --- retire (governed teardown) --------------------------------------------------

func TestRetireAllowedByDefault(t *testing.T) {
	fb := &fakeBackend{kind: "docker", destroyDiff: destructiveDiff()}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(time.Hour)))
	_, err := e.Retire(context.Background(), desired("docker"))
	if err != nil {
		t.Fatalf("retire (deliberate teardown) should be allowed by default, got %v", err)
	}
	if fb.applyCalls != 1 {
		t.Fatalf("retire must apply the destroy plan once, got %d", fb.applyCalls)
	}
}

func TestRetireBlockedWhenDestroyDisabled(t *testing.T) {
	fb := &fakeBackend{kind: "docker", destroyDiff: destructiveDiff()}
	e := New(WithBackend(fb), WithCredentialSource(mockCredSource(time.Hour)),
		WithBlastRadiusPolicy(BlastRadiusPolicy{AllowDestroy: false}))
	_, err := e.Retire(context.Background(), desired("docker"))
	if !errors.Is(err, ErrBlastRadius) {
		t.Fatalf("retire with AllowDestroy=false must be blocked, got %v", err)
	}
}

// --- blast-radius policy unit ----------------------------------------------------

func TestBlastRadiusPolicyDecide(t *testing.T) {
	p := DefaultBlastRadiusPolicy()
	if d := p.Decide(additiveDiff(), IntentApply); !d.Allowed {
		t.Fatalf("additive apply must be allowed: %s", d.Reason)
	}
	if d := p.Decide(destructiveDiff(), IntentApply); d.Allowed {
		t.Fatalf("destructive apply must be blocked by default")
	}
	if d := p.Decide(destructiveDiff(), IntentDestroy); !d.Allowed {
		t.Fatalf("explicit destroy must be allowed by default: %s", d.Reason)
	}
	raised := BlastRadiusPolicy{MaxApplyDestructive: 5, AllowDestroy: true}
	if d := raised.Decide(destructiveDiff(), IntentApply); !d.Allowed {
		t.Fatalf("destructive apply under a raised threshold must be allowed: %s", d.Reason)
	}
}

func TestNewDiffBlastRadiusClassification(t *testing.T) {
	cases := []struct {
		name string
		diff Diff
		want BlastRadius
	}{
		{"empty", NewDiff(nil, nil, nil, true, "", ""), BlastReadOnly},
		{"create", additiveDiff(), BlastAdditive},
		{"update", NewDiff(nil, []ChangeItem{{Action: "update"}}, nil, true, "", ""), BlastMutating},
		{"delete", destructiveDiff(), BlastDestructive},
		{"replace", NewDiff(nil, []ChangeItem{{Action: "replace", Destructive: true}}, nil, false, "", ""), BlastDestructive},
	}
	for _, c := range cases {
		if got := c.diff.BlastRadius; got != c.want {
			t.Errorf("%s: blast radius = %v, want %v", c.name, got, c.want)
		}
	}
}
