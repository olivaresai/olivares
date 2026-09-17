// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The current-profile directions the PUBLIC API cannot reach.
//
// Retirement has an operator route and is exercised over real HTTP next door.
// These two cannot be: a profile row is never deleted by any route — retiring is
// the end of its lifecycle and the row survives on purpose, because the history
// it explains has to keep reading — and a store that cannot answer is not a state
// an operator can request. Both are still reachable in production (an operator
// with database access, a restore that lost a table, a degraded store), and both
// have to fail CLOSED, so they are pinned here at the transaction instead.

// retiredProfileFixture is a live, profiled, Claude-transport run: the RAW input
// plane, which the driver-backed tests next door cannot exercise because a
// protocol driver refuses raw frames for its own reasons.
type retiredProfileFixture struct {
	m      *Module
	st     store.Store
	tenant model.TenantID
	prof   ProviderProfile
	runRef string
	proc   *fakeProc
	live   *liveRun
}

func newRetiredProfileFixture(t *testing.T) *retiredProfileFixture {
	t.Helper()
	ctx := context.Background()
	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
	)
	m.UseExecutionEnvironmentRef(testEnvRef)
	config, home := t.TempDir(), t.TempDir()
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverClaude, ConfigHome: config, UserHome: home,
		DisplayName: "raw-plane", AuthSource: AuthSourceAccountHome,
	})
	run, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:profile-revocation", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref,
	})
	if err != nil {
		t.Fatalf("create profiled run: %v", err)
	}
	live, ok := m.rt.getLive(tenant, run.RunRef)
	if !ok {
		t.Fatal("the profiled run has no live handle")
	}
	proc := runner.lastProc()
	t.Cleanup(func() {
		proc.finish(0)
		select {
		case <-live.finalizedCh:
		case <-time.After(finalizeWaitBudget):
		}
	})
	return &retiredProfileFixture{
		m: m, st: st, tenant: tenant, prof: prof, runRef: run.RunRef, proc: proc, live: live,
	}
}

// deleteProfileRow removes the row no route can remove.
func (fx *retiredProfileFixture) deleteProfileRow(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := fx.m.data.Mutate(ctx, fx.tenant, func(sc store.Scope) error {
		rec, err := findProfileRec(ctx, sc, fx.prof.Ref)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		id, err := model.ParseID(rec.String(model.ColID))
		if err != nil {
			return err
		}
		return repo.Delete(ctx, id)
	}); err != nil {
		t.Fatalf("delete the profile row: %v", err)
	}
}

// TestRawInputRefusesAfterTheProfileIsRetiredOrGone covers the raw NDJSON plane
// and both absence directions, and asserts the only thing that matters at this
// boundary: the process received nothing.
func TestRawInputRefusesAfterTheProfileIsRetiredOrGone(t *testing.T) {
	t.Parallel()

	t.Run("positive control: a current profile still writes", func(t *testing.T) {
		t.Parallel()
		fx := newRetiredProfileFixture(t)
		if err := fx.m.sendInput(context.Background(), fx.tenant, fx.runRef, []byte(`{"type":"ok"}`)); err != nil {
			t.Fatalf("raw input under a CURRENT profile: %v", err)
		}
		if got := fx.proc.sentCount(); got != 1 {
			t.Fatalf("raw input wrote %d time(s), want 1", got)
		}
	})

	t.Run("retired", func(t *testing.T) {
		t.Parallel()
		fx := newRetiredProfileFixture(t)
		ctx := context.Background()
		retired := ProfileRetired
		if _, err := fx.m.PatchProfile(ctx, fx.tenant, fx.prof.Ref, ProfilePatch{State: &retired}); err != nil {
			t.Fatalf("retire the profile: %v", err)
		}
		err := fx.m.sendInput(ctx, fx.tenant, fx.runRef, []byte(`{"type":"revoked"}`))
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusConflict {
			t.Fatalf("raw input under a RETIRED profile = %v, want a 409 refusal", err)
		}
		if got := fx.proc.sentCount(); got != 0 {
			t.Fatalf("a revoked raw input wrote to the process %d time(s)", got)
		}
	})

	t.Run("row gone", func(t *testing.T) {
		t.Parallel()
		fx := newRetiredProfileFixture(t)
		fx.deleteProfileRow(t)
		err := fx.m.sendInput(context.Background(), fx.tenant, fx.runRef, []byte(`{"type":"orphan"}`))
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusConflict {
			t.Fatalf("raw input with NO profile row = %v, want a 409 refusal", err)
		}
		if got := fx.proc.sentCount(); got != 0 {
			t.Fatalf("an orphaned raw input wrote to the process %d time(s)", got)
		}
	})

	t.Run("stop is revoked too, and the runtime can still reap", func(t *testing.T) {
		t.Parallel()
		fx := newRetiredProfileFixture(t)
		ctx := context.Background()
		retired := ProfileRetired
		if _, err := fx.m.PatchProfile(ctx, fx.tenant, fx.prof.Ref, ProfilePatch{State: &retired}); err != nil {
			t.Fatalf("retire the profile: %v", err)
		}
		if _, err := fx.m.stopRun(ctx, fx.tenant, fx.runRef, "user:operator", model.ActorUser); err == nil {
			t.Fatal("the operator stop must refuse once the profile is retired")
		}
		if procIsDone(fx.proc) {
			t.Fatal("a refused stop ended the process")
		}
		// The runtime's own teardown is a different power and still works.
		stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := fx.m.Stop(stopCtx); err != nil {
			t.Fatalf("runtime shutdown after revoking the holder: %v", err)
		}
		if !procIsDone(fx.proc) {
			t.Fatal("the runtime did not reap its own child after the holder was revoked")
		}
	})
}

// procIsDone reports whether the fake child has been ended, reading the flag the
// process itself sets rather than the channel, so it is a state and not a wait.
func procIsDone(p *fakeProc) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done
}

// TestAuthorityKeepsAnUnreadableProfileUnavailableInsteadOfCached is the third
// answer. A store that cannot be read is not permission and not denial: it is
// "I could not look", and the one thing it must never become is a grant issued
// from the identity this launch already had in memory.
func TestAuthorityKeepsAnUnreadableProfileUnavailableInsteadOfCached(t *testing.T) {
	t.Parallel()

	fx := newRetiredProfileFixture(t)
	boom := errors.New("test: the profile table cannot be read")
	fx.m.UseData(&profileReadFailureData{inner: fx.m.data, err: boom})

	err := fx.m.sendInput(context.Background(), fx.tenant, fx.runRef, []byte(`{"type":"unreadable"}`))
	if err == nil {
		t.Fatal("an unreadable profile must not authorise an effect")
	}
	if !errors.Is(err, boom) {
		// It may be wrapped or classified, but the cause has to survive: an outage
		// reported as a clean refusal is the same lie in the other direction.
		t.Fatalf("unreadable-profile input = %v, want the store failure to survive", err)
	}
	if got := fx.proc.sentCount(); got != 0 {
		t.Fatalf("an unreadable profile still wrote to the process %d time(s)", got)
	}
}

// profileReadFailureData fails exactly the provider-profile reads and lets every
// other repository through, so the failure under test is the new one and not a
// store that stopped working entirely.
type profileReadFailureData struct {
	inner interface {
		View(context.Context, model.TenantID, func(store.Scope) error) error
		Mutate(context.Context, model.TenantID, func(store.Scope) error) error
	}
	err error
}

func (d *profileReadFailureData) View(
	ctx context.Context, tenant model.TenantID, fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, func(sc store.Scope) error {
		return fn(&profileReadFailureScope{Scope: sc, err: d.err})
	})
}

func (d *profileReadFailureData) Mutate(
	ctx context.Context, tenant model.TenantID, fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&profileReadFailureScope{Scope: sc, err: d.err})
	})
}

type profileReadFailureScope struct {
	store.Scope
	err error
}

func (s *profileReadFailureScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind == providerProfileKind {
		return nil, s.err
	}
	return s.Scope.Ext(kind)
}
