// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type credentialEvidenceFixture struct {
	st      store.Store
	tenantA model.TenantID
	tenantB model.TenantID
	tenantC model.TenantID
	user    model.User
	session model.AuthSession
	token   model.APIToken
}

func newCredentialEvidenceFixture(t *testing.T, suffix string) credentialEvidenceFixture {
	t.Helper()
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	fixture := credentialEvidenceFixture{
		st:      st,
		tenantA: provisionTenant(t, st, "credential-evidence-a-"+suffix),
		tenantB: provisionTenant(t, st, "credential-evidence-b-"+suffix),
		tenantC: provisionTenant(t, st, "credential-evidence-c-"+suffix),
	}

	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		fixture.user, err = as.Users().Create(ctx, model.User{
			Email:  "credential-evidence-" + suffix + "@example.test",
			Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		for _, tenant := range []model.TenantID{fixture.tenantA, fixture.tenantB} {
			if _, err := as.Memberships().Create(ctx, model.Membership{
				UserID: fixture.user.ID, TargetTenantID: tenant, Role: "viewer",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed credential evidence user/memberships: %v", err)
	}

	beforeCreate := credentialEvidenceEpochs(t, st, fixture.tenants()...)
	expires := model.NewTimestamp(time.Now().UTC().Add(time.Hour))
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		fixture.session, err = as.Sessions().Create(ctx, model.AuthSession{
			UserID: fixture.user.ID, Selector: "session-" + suffix,
			SecretHash: []byte("session-secret-" + suffix), ExpiresAt: expires,
			CreatedIP: "127.0.0.1", AAL: 1, AMR: []string{"pwd"},
		})
		if err != nil {
			return err
		}
		tokenExpiry := model.NewTimestamp(time.Now().UTC().Add(2 * time.Hour))
		fixture.token, err = as.Tokens().Create(ctx, model.APIToken{
			Name: "token-" + suffix, UserID: fixture.user.ID,
			Selector: "token-" + suffix, SecretHash: []byte("token-secret-" + suffix),
			BoundTenantID: fixture.tenantA, Role: "viewer", ExpiresAt: &tokenExpiry,
		})
		return err
	}); err != nil {
		t.Fatalf("seed credential evidence rows: %v", err)
	}
	credentialEvidenceWantEpochs(t, st, beforeCreate)
	return fixture
}

func (f credentialEvidenceFixture) tenants() []model.TenantID {
	return []model.TenantID{f.tenantA, f.tenantB, f.tenantC}
}

func TestCredentialWritersFenceInvalidationBeforeSourceAcrossTenants(t *testing.T) {
	ctx := context.Background()
	f := newCredentialEvidenceFixture(t, "causal")

	// Rotating the session bearer fences every tenant where its user can act.
	beforeSession := credentialEvidenceEpochs(t, f.st, f.tenants()...)
	sessionCalls := 0
	var ordering []string
	directoryWriterAfterLockTestHook = func() {
		ordering = append(ordering, "lock")
	}
	installCredentialEvidenceHook(t, func(ctx context.Context, tracker *directoryWriteTracker) error {
		sessionCalls++
		if !tracker.locked {
			return errors.New("before-source hook ran without the directory lock")
		}
		want := cloneCredentialEvidenceEpochs(beforeSession)
		// One observation, not two. The retired prelude produced a first call that
		// asserted the epochs were UNCHANGED before anything had happened -- the
		// test's precondition counted as a case. What the test is for lives here:
		// the affected tenants are already bumped when the source is still
		// untouched, and every other tenant in the fixture is NOT bumped, because
		// want is a clone of them all and only these two are advanced.
		switch sessionCalls {
		case 1:
			ordering = append(ordering, "bump")
			want[f.tenantA]++
			want[f.tenantB]++
		default:
			return fmt.Errorf("unexpected session before-source call %d", sessionCalls)
		}
		if err := credentialEvidenceHookWantEpochs(ctx, tracker, want); err != nil {
			return err
		}
		if sessionCalls == 1 {
			var selector string
			query := tracker.dia.Rebind("SELECT selector FROM " +
				directoryWriterRelation(tracker.dia, authSessionDescriptor.Table) +
				" WHERE tenant_id = ? AND id = ?")
			if err := tracker.tx.QueryRowContext(
				ctx, query, model.SystemTenantID.String(), f.session.ID.String(),
			).Scan(&selector); err != nil {
				return fmt.Errorf("read session before source update: %w", err)
			}
			if selector != f.session.Selector {
				return fmt.Errorf("session selector before source update = %q, want %q",
					selector, f.session.Selector)
			}
		}
		return nil
	})
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		current.Selector = "session-causal-rotated"
		current.SecretHash = []byte("session-causal-secret-rotated")
		f.session, err = as.Sessions().Update(ctx, current)
		if err != nil {
			return err
		}
		ordering = append(ordering, "source")
		return nil
	}); err != nil {
		clearCredentialEvidenceHook()
		t.Fatalf("rotate session credential: %v", err)
	}
	clearCredentialEvidenceHook()
	if sessionCalls != 1 {
		t.Fatalf("session before-source hook calls = %d, want 1", sessionCalls)
	}
	if want := []string{"lock", "bump", "source"}; !slices.Equal(
		ordering, want,
	) {
		t.Fatalf("directory writer order = %v, want %v", ordering, want)
	}
	wantAfterSession := cloneCredentialEvidenceEpochs(beforeSession)
	wantAfterSession[f.tenantA]++
	wantAfterSession[f.tenantB]++
	credentialEvidenceWantEpochs(t, f.st, wantAfterSession)

	// Delete is also an invalidation: the row must still exist when both tenant
	// epochs have already advanced.
	beforeDelete := credentialEvidenceEpochs(t, f.st, f.tenants()...)
	deleteCalls := 0
	installCredentialEvidenceHook(t, func(ctx context.Context, tracker *directoryWriteTracker) error {
		deleteCalls++
		want := cloneCredentialEvidenceEpochs(beforeDelete)
		if deleteCalls == 1 {
			want[f.tenantA]++
			want[f.tenantB]++
		}
		if err := credentialEvidenceHookWantEpochs(ctx, tracker, want); err != nil {
			return err
		}
		if deleteCalls == 1 {
			var count int
			query := tracker.dia.Rebind("SELECT COUNT(*) FROM " +
				directoryWriterRelation(tracker.dia, authSessionDescriptor.Table) +
				" WHERE tenant_id = ? AND id = ?")
			if err := tracker.tx.QueryRowContext(
				ctx, query, model.SystemTenantID.String(), f.session.ID.String(),
			).Scan(&count); err != nil {
				return fmt.Errorf("count session before source delete: %w", err)
			}
			if count != 1 {
				return fmt.Errorf("session rows before source delete = %d, want 1", count)
			}
		}
		return nil
	})
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.Sessions().Delete(ctx, f.session.ID)
	}); err != nil {
		clearCredentialEvidenceHook()
		t.Fatalf("delete session credential: %v", err)
	}
	clearCredentialEvidenceHook()
	if deleteCalls != 1 {
		t.Fatalf("session delete before-source hook calls = %d, want 1", deleteCalls)
	}
	wantAfterDelete := cloneCredentialEvidenceEpochs(beforeDelete)
	wantAfterDelete[f.tenantA]++
	wantAfterDelete[f.tenantB]++
	credentialEvidenceWantEpochs(t, f.st, wantAfterDelete)
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		_, err := as.Sessions().Get(ctx, f.session.ID)
		return err
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted session read error = %v, want ErrNotFound", err)
	}

	// Moving a token fences both its old and new binding before the source row
	// changes, while the unrelated session tenant remains unchanged.
	beforeToken := credentialEvidenceEpochs(t, f.st, f.tenants()...)
	tokenCalls := 0
	installCredentialEvidenceHook(t, func(ctx context.Context, tracker *directoryWriteTracker) error {
		tokenCalls++
		want := cloneCredentialEvidenceEpochs(beforeToken)
		if tokenCalls == 1 {
			want[f.tenantA]++
			want[f.tenantC]++
		}
		if err := credentialEvidenceHookWantEpochs(ctx, tracker, want); err != nil {
			return err
		}
		if tokenCalls == 1 {
			var bound string
			query := tracker.dia.Rebind("SELECT bound_tenant_id FROM " +
				directoryWriterRelation(tracker.dia, apiTokenDescriptor.Table) +
				" WHERE tenant_id = ? AND id = ?")
			if err := tracker.tx.QueryRowContext(
				ctx, query, model.SystemTenantID.String(), f.token.ID.String(),
			).Scan(&bound); err != nil {
				return fmt.Errorf("read token before source update: %w", err)
			}
			if bound != f.tenantA.String() {
				return fmt.Errorf("token binding before source update = %q, want %q",
					bound, f.tenantA)
			}
		}
		return nil
	})
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Tokens().Get(ctx, f.token.ID)
		if err != nil {
			return err
		}
		current.BoundTenantID = f.tenantC
		current.Role = "editor"
		f.token, err = as.Tokens().Update(ctx, current)
		return err
	}); err != nil {
		clearCredentialEvidenceHook()
		t.Fatalf("move token credential: %v", err)
	}
	clearCredentialEvidenceHook()
	if tokenCalls != 1 {
		t.Fatalf("token before-source hook calls = %d, want 1", tokenCalls)
	}
	wantAfterToken := cloneCredentialEvidenceEpochs(beforeToken)
	wantAfterToken[f.tenantA]++
	wantAfterToken[f.tenantC]++
	credentialEvidenceWantEpochs(t, f.st, wantAfterToken)

	beforeTokenDelete := credentialEvidenceEpochs(t, f.st, f.tenants()...)
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.Tokens().Delete(ctx, f.token.ID)
	}); err != nil {
		t.Fatalf("delete token credential: %v", err)
	}
	wantAfterTokenDelete := cloneCredentialEvidenceEpochs(beforeTokenDelete)
	wantAfterTokenDelete[f.tenantC]++
	credentialEvidenceWantEpochs(t, f.st, wantAfterTokenDelete)
}

func TestCredentialSessionInvalidationFencesGroupOnlyTenant(t *testing.T) {
	ctx := context.Background()
	f := newCredentialEvidenceFixture(t, "group-only")
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		group, err := as.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: f.tenantC,
			DisplayName:    "credential-evidence-group-only",
		})
		if err != nil {
			return err
		}
		_, err = as.GroupMembers().Create(ctx, model.UserGroupMember{
			GroupID: group.ID,
			UserID:  f.user.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("seed group-only tenant: %v", err)
	}

	before := credentialEvidenceEpochs(t, f.st, f.tenants()...)
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		current.Selector = "session-group-only-rotated"
		_, err = as.Sessions().Update(ctx, current)
		return err
	}); err != nil {
		t.Fatalf("invalidate group-reachable session: %v", err)
	}
	want := cloneCredentialEvidenceEpochs(before)
	want[f.tenantA]++
	want[f.tenantB]++
	want[f.tenantC]++
	credentialEvidenceWantEpochs(t, f.st, want)

	beforeDelete := credentialEvidenceEpochs(t, f.st, f.tenants()...)
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.Sessions().Delete(ctx, f.session.ID)
	}); err != nil {
		t.Fatalf("delete group-reachable session: %v", err)
	}
	wantDelete := cloneCredentialEvidenceEpochs(beforeDelete)
	wantDelete[f.tenantA]++
	wantDelete[f.tenantB]++
	wantDelete[f.tenantC]++
	credentialEvidenceWantEpochs(t, f.st, wantDelete)
}

func TestCredentialSessionMoveFencesOldAndNewUsers(t *testing.T) {
	ctx := context.Background()
	f := newCredentialEvidenceFixture(t, "session-user-move")
	var next model.User
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		next, err = as.Users().Create(ctx, model.User{
			Email: "credential-evidence-session-next@example.test", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		_, err = as.Memberships().Create(ctx, model.Membership{
			UserID: next.ID, TargetTenantID: f.tenantC, Role: "viewer",
		})
		return err
	}); err != nil {
		t.Fatalf("seed next session User: %v", err)
	}

	before := credentialEvidenceEpochs(t, f.st, f.tenants()...)
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		current.UserID = next.ID
		_, err = as.Sessions().Update(ctx, current)
		return err
	}); err != nil {
		t.Fatalf("move session User: %v", err)
	}
	want := cloneCredentialEvidenceEpochs(before)
	want[f.tenantA]++
	want[f.tenantB]++
	want[f.tenantC]++
	credentialEvidenceWantEpochs(t, f.st, want)
}

func TestCredentialUnboundSuperadminStaysUnfenced(t *testing.T) {
	ctx := context.Background()
	f := newCredentialEvidenceFixture(t, "superadmin-off")
	before := credentialEvidenceEpochs(t, f.st, f.tenants()...)

	var token model.APIToken
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		token, err = as.Tokens().Create(ctx, model.APIToken{
			Name: "unbound-superadmin", UserID: f.user.ID,
			Selector: "unbound-superadmin", SecretHash: []byte("unbound-superadmin-secret"),
			IsSuperadmin: true,
		})
		return err
	}); err != nil {
		t.Fatalf("create unbound superadmin token: %v", err)
	}
	credentialEvidenceWantEpochs(t, f.st, before)

	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Tokens().Get(ctx, token.ID)
		if err != nil {
			return err
		}
		current.Revoked = true
		_, err = as.Tokens().Update(ctx, current)
		return err
	}); err != nil {
		t.Fatalf("revoke unbound superadmin token: %v", err)
	}
	credentialEvidenceWantEpochs(t, f.st, before)

	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.Tokens().Delete(ctx, token.ID)
	}); err != nil {
		t.Fatalf("delete unbound superadmin token: %v", err)
	}
	credentialEvidenceWantEpochs(t, f.st, before)
}

func TestCredentialRenewalOnlyDoesNotBumpDirectoryEpoch(t *testing.T) {
	ctx := context.Background()
	f := newCredentialEvidenceFixture(t, "renewal")
	before := credentialEvidenceEpochs(t, f.st, f.tenants()...)

	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		current.ExpiresAt = model.NewTimestamp(current.ExpiresAt.Time().Add(time.Hour))
		f.session, err = as.Sessions().Update(ctx, current)
		return err
	}); err != nil {
		t.Fatalf("extend session expiry: %v", err)
	}
	credentialEvidenceWantEpochs(t, f.st, before)
	if f.session.Version != 2 {
		t.Fatalf("renewed session version = %d, want 2", f.session.Version)
	}

	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Tokens().Get(ctx, f.token.ID)
		if err != nil {
			return err
		}
		extended := model.NewTimestamp(current.ExpiresAt.Time().Add(time.Hour))
		lastUsed := model.NewTimestamp(time.Now().UTC())
		current.ExpiresAt = &extended
		current.LastUsedAt = &lastUsed
		f.token, err = as.Tokens().Update(ctx, current)
		return err
	}); err != nil {
		t.Fatalf("extend token expiry: %v", err)
	}
	credentialEvidenceWantEpochs(t, f.st, before)
	if f.token.Version != 2 {
		t.Fatalf("renewed token version = %d, want 2", f.token.Version)
	}
}

func TestCredentialWriterFailuresRollbackFenceAndSource(t *testing.T) {
	ctx := context.Background()
	f := newCredentialEvidenceFixture(t, "rollback")
	before := credentialEvidenceEpochs(t, f.st, f.tenants()...)

	// A discarded source conflict must poison AuthMutate and roll back the
	// already prepared tenant fences.
	conflictCalls := 0
	installCredentialEvidenceHook(t, func(ctx context.Context, tracker *directoryWriteTracker) error {
		conflictCalls++
		// One observation: the retired prelude used to spend the first one asserting
		// nothing had changed yet. Affected tenants are bumped here, and every other
		// tenant stays put because want clones them all.
		want := cloneCredentialEvidenceEpochs(before)
		if conflictCalls == 1 {
			want[f.tenantA]++
			want[f.tenantB]++
		}
		return credentialEvidenceHookWantEpochs(ctx, tracker, want)
	})
	err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		current.Selector = "discarded-conflict"
		current.Version++
		_, _ = as.Sessions().Update(ctx, current)
		return nil
	})
	if !errors.Is(err, store.ErrConflict) {
		clearCredentialEvidenceHook()
		t.Fatalf("discarded session conflict = %v, want ErrConflict poison", err)
	}
	clearCredentialEvidenceHook()
	if conflictCalls != 1 {
		t.Fatalf("conflict before-source hook calls = %d, want 1", conflictCalls)
	}
	credentialEvidenceWantEpochs(t, f.st, before)
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		if got.Selector != f.session.Selector || got.Version != f.session.Version {
			t.Fatalf("session after discarded conflict = %+v, want unchanged", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("read session after discarded conflict: %v", err)
	}

	// A later callback failure rolls back both a successful source update and
	// the fence that preceded it.
	injected := errors.New("rollback after token invalidation")
	err = f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Tokens().Get(ctx, f.token.ID)
		if err != nil {
			return err
		}
		current.Revoked = true
		if _, err := as.Tokens().Update(ctx, current); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("post-update rollback error = %v, want injected cause", err)
	}
	credentialEvidenceWantEpochs(t, f.st, before)
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.Tokens().Get(ctx, f.token.ID)
		if err != nil {
			return err
		}
		if got.Revoked || got.Version != f.token.Version {
			t.Fatalf("token after callback rollback = %+v, want live version %d",
				got, f.token.Version)
		}
		return nil
	}); err != nil {
		t.Fatalf("read token after callback rollback: %v", err)
	}
}

func TestCredentialPartialEpochFailureRollsBackEarlierBumpAndSource(t *testing.T) {
	ctx := context.Background()
	f := newCredentialEvidenceFixture(t, "partial-bump")
	first, missing := f.tenantA, f.tenantB
	if missing.String() < first.String() {
		first, missing = missing, first
	}
	beforeFirst := credentialEvidenceEpochs(t, f.st, first)[first]
	directorySnapshotCorruptEpoch(t, ctx, f.st, missing, "absent")

	err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		current.Selector = "session-partial-bump-rotated"
		_, _ = as.Sessions().Update(ctx, current)
		return nil
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("discarded partial bump error = %v, want ErrDirectoryUnavailable poison", err)
	}
	if got := credentialEvidenceEpochs(t, f.st, first)[first]; got != beforeFirst {
		t.Fatalf("earlier tenant epoch after partial failure = %d, want %d", got, beforeFirst)
	}
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.Sessions().Get(ctx, f.session.ID)
		if err != nil {
			return err
		}
		if got.Selector != f.session.Selector || got.Version != f.session.Version {
			t.Fatalf("source after partial bump failure = %+v, want unchanged", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("read source after partial bump failure: %v", err)
	}
}

func TestCredentialDeleteAllowsAlreadyMissingAuthoritySources(t *testing.T) {
	ctx := context.Background()

	t.Run("missing User", func(t *testing.T) {
		f := newCredentialEvidenceFixture(t, "missing-user")
		if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			ts := as.(*authScope).ts
			query := ts.s.dia.Rebind("DELETE FROM " +
				directoryWriterRelation(ts.s.dia, userDescriptor.Table) +
				" WHERE tenant_id = ? AND id = ?")
			result, err := ts.tx.ExecContext(
				ctx, query, model.SystemTenantID.String(), f.user.ID.String(),
			)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if rows != 1 {
				return fmt.Errorf("raw missing-User fixture deleted %d rows, want 1", rows)
			}
			return nil
		}); err != nil {
			t.Fatalf("remove User before credential cleanup: %v", err)
		}
		if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
			memberships, _, err := as.Memberships().List(ctx, model.Query{
				Filters: []model.Filter{{
					Column: "user_id", Op: model.OpEq, Value: f.user.ID.String(),
				}},
				Limit: 10,
			})
			if err != nil {
				return err
			}
			got := make([]model.TenantID, 0, len(memberships))
			for _, membership := range memberships {
				got = append(got, membership.TargetTenantID)
			}
			slices.Sort(got)
			want := []model.TenantID{f.tenantA, f.tenantB}
			slices.Sort(want)
			if !slices.Equal(got, want) {
				return fmt.Errorf("memberships after User disappearance = %v, want %v", got, want)
			}
			return nil
		}); err != nil {
			t.Fatalf("prove durable tenant discovery after User disappeared: %v", err)
		}

		beforeSession := credentialEvidenceEpochs(t, f.st, f.tenants()...)
		if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			return as.Sessions().Delete(ctx, f.session.ID)
		}); err != nil {
			t.Fatalf("delete session after User disappeared: %v", err)
		}
		wantSession := cloneCredentialEvidenceEpochs(beforeSession)
		wantSession[f.tenantA]++
		wantSession[f.tenantB]++
		credentialEvidenceWantEpochs(t, f.st, wantSession)

		beforeToken := credentialEvidenceEpochs(t, f.st, f.tenants()...)
		if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			return as.Tokens().Delete(ctx, f.token.ID)
		}); err != nil {
			t.Fatalf("delete token after User disappeared: %v", err)
		}
		wantToken := cloneCredentialEvidenceEpochs(beforeToken)
		wantToken[f.tenantA]++
		credentialEvidenceWantEpochs(t, f.st, wantToken)
	})

	t.Run("missing token parent", func(t *testing.T) {
		f := newCredentialEvidenceFixture(t, "missing-parent")
		var child model.APIToken
		if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			expires := model.NewTimestamp(time.Now().UTC().Add(time.Hour))
			var err error
			child, err = as.Tokens().Create(ctx, model.APIToken{
				Name: "child", UserID: f.user.ID, Selector: "child-missing-parent",
				SecretHash: []byte("child-secret"), BoundTenantID: f.tenantA,
				Role: "viewer", ExpiresAt: &expires, ParentTokenID: f.token.ID,
			})
			return err
		}); err != nil {
			t.Fatalf("create child token: %v", err)
		}
		if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			return as.Tokens().Delete(ctx, f.token.ID)
		}); err != nil {
			t.Fatalf("delete parent token: %v", err)
		}

		beforeChild := credentialEvidenceEpochs(t, f.st, f.tenants()...)
		if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			return as.Tokens().Delete(ctx, child.ID)
		}); err != nil {
			t.Fatalf("delete child after parent disappeared: %v", err)
		}
		wantChild := cloneCredentialEvidenceEpochs(beforeChild)
		wantChild[f.tenantA]++
		credentialEvidenceWantEpochs(t, f.st, wantChild)
	})
}

func TestCredentialRenewalOnlyClassificationIsNarrow(t *testing.T) {
	now := model.NewTimestamp(time.Now().UTC())
	later := model.NewTimestamp(now.Time().Add(time.Hour))
	base := model.BaseFields{
		ID: model.NewID(), TenantID: model.SystemTenantID,
		CreatedAt: now, UpdatedAt: now, Version: 3,
	}
	session := model.AuthSession{
		BaseFields: base, UserID: model.NewID(), Selector: "session-selector",
		SecretHash: []byte("session-secret"), ExpiresAt: later, CreatedIP: "127.0.0.1",
		AAL: 1, AMR: []string{"pwd"},
	}
	extendedSession := session
	extendedSession.ExpiresAt = model.NewTimestamp(later.Time().Add(time.Hour))
	if !authSessionRenewalOnly(session, extendedSession) {
		t.Fatal("session expiry extension was not classified as renewal-only")
	}
	deletedAt := now
	for _, tc := range []struct {
		name   string
		mutate func(*model.AuthSession)
	}{
		{name: "id", mutate: func(v *model.AuthSession) { v.ID = model.NewID() }},
		{name: "tenant", mutate: func(v *model.AuthSession) { v.TenantID = model.NewTenantID() }},
		{name: "created-at", mutate: func(v *model.AuthSession) { v.CreatedAt = later }},
		{name: "updated-at", mutate: func(v *model.AuthSession) { v.UpdatedAt = later }},
		{name: "version", mutate: func(v *model.AuthSession) { v.Version++ }},
		{name: "deleted-at", mutate: func(v *model.AuthSession) { v.DeletedAt = &deletedAt }},
		{name: "user", mutate: func(v *model.AuthSession) { v.UserID = model.NewID() }},
		{name: "selector", mutate: func(v *model.AuthSession) { v.Selector = "rotated" }},
		{name: "secret", mutate: func(v *model.AuthSession) { v.SecretHash = []byte("rotated") }},
		{name: "shorter expiry", mutate: func(v *model.AuthSession) { v.ExpiresAt = now }},
		{name: "zero expiry", mutate: func(v *model.AuthSession) { v.ExpiresAt = model.Timestamp{} }},
		{name: "revocation", mutate: func(v *model.AuthSession) { v.Revoked = true }},
		{name: "created-ip", mutate: func(v *model.AuthSession) { v.CreatedIP = "192.0.2.1" }},
		{name: "assurance", mutate: func(v *model.AuthSession) { v.AAL = 3 }},
		{name: "amr", mutate: func(v *model.AuthSession) { v.AMR = []string{"webauthn"} }},
		{name: "aal expiry", mutate: func(v *model.AuthSession) { v.AALExpiresAt = &later }},
	} {
		t.Run("session "+tc.name, func(t *testing.T) {
			candidate := session
			tc.mutate(&candidate)
			if authSessionRenewalOnly(session, candidate) {
				t.Fatal("authority change was classified as renewal-only")
			}
		})
	}

	tokenExpiry := later
	token := model.APIToken{
		BaseFields: base, Name: "token", UserID: model.NewID(), Selector: "token-selector",
		SecretHash: []byte("token-secret"), BoundTenantID: model.NewTenantID(), Role: "viewer",
		ExpiresAt: &tokenExpiry,
	}
	extendedToken := token
	extendedTokenExpiry := model.NewTimestamp(later.Time().Add(time.Hour))
	lastUsed := now
	extendedToken.ExpiresAt = &extendedTokenExpiry
	extendedToken.LastUsedAt = &lastUsed
	if !authTokenRenewalOnly(token, extendedToken) {
		t.Fatal("token expiry/last-used extension was not classified as renewal-only")
	}
	removedExpiry := token
	removedExpiry.ExpiresAt = nil
	if !authTokenRenewalOnly(token, removedExpiry) {
		t.Fatal("token expiry removal was not classified as non-narrowing renewal")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*model.APIToken)
	}{
		{name: "id", mutate: func(v *model.APIToken) { v.ID = model.NewID() }},
		{name: "base tenant", mutate: func(v *model.APIToken) { v.TenantID = model.NewTenantID() }},
		{name: "created-at", mutate: func(v *model.APIToken) { v.CreatedAt = later }},
		{name: "updated-at", mutate: func(v *model.APIToken) { v.UpdatedAt = later }},
		{name: "version", mutate: func(v *model.APIToken) { v.Version++ }},
		{name: "deleted-at", mutate: func(v *model.APIToken) { v.DeletedAt = &deletedAt }},
		{name: "name", mutate: func(v *model.APIToken) { v.Name = "renamed" }},
		{name: "user", mutate: func(v *model.APIToken) { v.UserID = model.NewID() }},
		{name: "selector", mutate: func(v *model.APIToken) { v.Selector = "rotated" }},
		{name: "secret", mutate: func(v *model.APIToken) { v.SecretHash = []byte("rotated") }},
		{name: "bound tenant", mutate: func(v *model.APIToken) { v.BoundTenantID = model.NewTenantID() }},
		{name: "role", mutate: func(v *model.APIToken) { v.Role = "editor" }},
		{name: "superadmin", mutate: func(v *model.APIToken) { v.IsSuperadmin = true }},
		{name: "shorter expiry", mutate: func(v *model.APIToken) { v.ExpiresAt = &now }},
		{name: "zero expiry", mutate: func(v *model.APIToken) {
			zero := model.Timestamp{}
			v.ExpiresAt = &zero
		}},
		{name: "revocation", mutate: func(v *model.APIToken) { v.Revoked = true }},
		{name: "purpose", mutate: func(v *model.APIToken) { v.Purpose = "runtime" }},
		{name: "audience", mutate: func(v *model.APIToken) { v.Audience = "urn:example" }},
		{name: "act-as", mutate: func(v *model.APIToken) { v.ActAsUserID = model.NewID() }},
		{name: "parent", mutate: func(v *model.APIToken) { v.ParentTokenID = model.NewID() }},
		{name: "scope", mutate: func(v *model.APIToken) { v.Scope = "read" }},
		{name: "agent ref", mutate: func(v *model.APIToken) { v.AgentRef = "agent-1" }},
		{name: "session ref", mutate: func(v *model.APIToken) { v.SessionRef = "session-1" }},
		{name: "workspace", mutate: func(v *model.APIToken) { v.WorkspaceID = model.NewID() }},
		{name: "session run", mutate: func(v *model.APIToken) { v.SessionRunRef = "run-1" }},
		{name: "session fence", mutate: func(v *model.APIToken) { v.SessionFence = 2 }},
	} {
		t.Run("token "+tc.name, func(t *testing.T) {
			candidate := token
			tc.mutate(&candidate)
			if authTokenRenewalOnly(token, candidate) {
				t.Fatal("authority change was classified as renewal-only")
			}
		})
	}
}

func installCredentialEvidenceHook(
	t *testing.T,
	hook func(context.Context, *directoryWriteTracker) error,
) {
	t.Helper()
	if directoryWriterBeforeSourceTestHook != nil {
		t.Fatal("directory writer before-source hook already installed")
	}
	directoryWriterBeforeSourceTestHook = hook
	t.Cleanup(clearCredentialEvidenceHook)
}

func clearCredentialEvidenceHook() {
	directoryWriterAfterLockTestHook = nil
	directoryWriterBeforeSourceTestHook = nil
}

func credentialEvidenceEpochs(
	t *testing.T,
	st store.Store,
	tenants ...model.TenantID,
) map[model.TenantID]int64 {
	t.Helper()
	out := make(map[model.TenantID]int64, len(tenants))
	for _, tenant := range tenants {
		if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
			epoch, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(context.Background())
			if err != nil {
				return err
			}
			out[tenant] = epoch.Version
			return nil
		}); err != nil {
			t.Fatalf("read directory epoch for %s: %v", tenant, err)
		}
	}
	return out
}

func credentialEvidenceWantEpochs(
	t *testing.T,
	st store.Store,
	want map[model.TenantID]int64,
) {
	t.Helper()
	tenants := make([]model.TenantID, 0, len(want))
	for tenant := range want {
		tenants = append(tenants, tenant)
	}
	got := credentialEvidenceEpochs(t, st, tenants...)
	for tenant, version := range want {
		if got[tenant] != version {
			t.Errorf("directory epoch for %s = %d, want %d", tenant, got[tenant], version)
		}
	}
}

func credentialEvidenceHookWantEpochs(
	ctx context.Context,
	tracker *directoryWriteTracker,
	want map[model.TenantID]int64,
) error {
	for tenant, version := range want {
		epoch, found, err := readDirectoryEpochRow(ctx, tracker.tx, tracker.dia, tenant)
		if err != nil || !found {
			return fmt.Errorf("read pre-source epoch for %s: found=%t err=%v", tenant, found, err)
		}
		if epoch.Version != version {
			return fmt.Errorf("pre-source epoch for %s = %d, want %d",
				tenant, epoch.Version, version)
		}
	}
	return nil
}

func cloneCredentialEvidenceEpochs(
	in map[model.TenantID]int64,
) map[model.TenantID]int64 {
	out := make(map[model.TenantID]int64, len(in))
	for tenant, version := range in {
		out[tenant] = version
	}
	return out
}
