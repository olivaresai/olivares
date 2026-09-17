// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ataFacts reads the CURRENT business directory generation and the current H of
// each supplied User through the real read capabilities, so no test hardcodes a
// generation the fixture happens to produce.
func ataFacts(t *testing.T, s *sqlStore, tenant model.TenantID, ids ...model.ID) store.AuthoritySnapshotBundle {
	t.Helper()
	var b store.AuthoritySnapshotBundle
	if err := s.AuthView(context.Background(), func(as store.AuthScope) error {
		epoch, err := as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(context.Background(), tenant)
		if err != nil {
			return err
		}
		b.Facts = []store.AuthorizationFactRef{epoch}
		for _, id := range ids {
			h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(context.Background(), id)
			if err != nil {
				return err
			}
			b.UserAuthorities = append(b.UserAuthorities, h)
		}
		return nil
	}); err != nil {
		t.Fatalf("read current authority: %v", err)
	}
	return b
}

// ataObserve returns the current business generation and the H versions, so a
// test can prove admission alone bumped neither.
func ataObserve(t *testing.T, s *sqlStore, tenant model.TenantID, ids ...model.ID) (int64, []int64) {
	t.Helper()
	b := ataFacts(t, s, tenant, ids...)
	versions := make([]int64, 0, len(b.UserAuthorities))
	for _, h := range b.UserAuthorities {
		versions = append(versions, h.Version)
	}
	return b.Facts[0].Version, versions
}

// ataMarker is the non-directory auth-partition write the barrier admits. The
// invitation repository is a plain typed repository: it is not directory tracked,
// so committing one proves the admitted transaction could really mutate the auth
// partition without a directory write.
func ataMarker(ctx context.Context, as store.AuthScope, tenant model.TenantID, label string) (model.ID, error) {
	inv, err := as.Invites().Create(ctx, model.UserInvite{
		Email: label + "@example.test", TargetTenantID: tenant, Role: "viewer",
		Selector: label, SecretHash: []byte(label), ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
	})
	return inv.ID, err
}

func ataCountMarkers(t *testing.T, s *sqlStore, selector string) int {
	t.Helper()
	var n int
	if err := s.AuthView(context.Background(), func(as store.AuthScope) error {
		found, _, err := as.Invites().List(context.Background(), model.Query{
			Filters: []model.Filter{{Column: "selector", Op: model.OpEq, Value: selector}}, Limit: 10,
		})
		n = len(found)
		return err
	}); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	return n
}

// TestAuthTenantAuthorityPositive is oracle 1: a complete human bundle admits a
// real auth-partition write plus its audit event, admission alone bumps neither H
// nor the business generation, and the event keeps the supplied actor.
func TestAuthTenantAuthorityPositive(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			epochBefore, hBefore := ataObserve(t, s, tenant, admin.ID)

			var markerID model.ID
			actor := "user:" + admin.ID.String()
			// Every bundle is read BEFORE the write transaction opens: SQLite admits
			// one writer, so opening a read transaction inside the callback would
			// deadlock the fixture rather than test anything.
			live := ataFacts(t, s, tenant, admin.ID)
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				barrier, ok := as.(store.AuthTenantAuthorityBarrier)
				if !ok {
					t.Fatal("AuthScope lacks the barrier capability")
				}
				if err := barrier.LockAuthTenantAuthority(ctx, tenant, live); err != nil {
					return err
				}
				// After a successful return the callback sees SYSTEM again.
				if got := as.(*authScope).ts.tenant; got != model.SystemTenantID {
					t.Errorf("scope left borrowed: tenant=%s", got)
				}
				id, err := ataMarker(ctx, as, tenant, "ata-positive")
				if err != nil {
					return err
				}
				markerID = id
				_, err = as.Audit().Append(ctx, model.AuditDraft{
					Actor: actor, ActorKind: model.ActorUser,
					Action: "user.invite.retry", TargetKind: "core.user_invite", TargetID: id,
				})
				return err
			}); err != nil {
				t.Fatalf("admitted mutation: %v", err)
			}

			if got := ataCountMarkers(t, s, "ata-positive"); got != 1 {
				t.Fatalf("committed markers=%d, want 1", got)
			}
			epochAfter, hAfter := ataObserve(t, s, tenant, admin.ID)
			if epochAfter != epochBefore {
				t.Errorf("admission bumped the business generation: %d -> %d", epochBefore, epochAfter)
			}
			if len(hAfter) != len(hBefore) || hAfter[0] != hBefore[0] {
				t.Errorf("admission bumped H: %v -> %v", hBefore, hAfter)
			}
			// The actor is the supplied principal, never a SYSTEM impersonation.
			seen := false
			if err := s.AuthView(ctx, func(as store.AuthScope) error {
				return as.Audit().Walk(ctx, 0, func(e model.AuditEvent) error {
					if e.TargetID != markerID {
						return nil
					}
					seen = true
					if e.Actor != actor {
						t.Errorf("audit actor=%q, want %q", e.Actor, actor)
					}
					if e.ActorKind != model.ActorUser {
						t.Errorf("audit actor kind=%q, want %q", e.ActorKind, model.ActorUser)
					}
					return nil
				})
			}); err != nil {
				t.Fatalf("read audit: %v", err)
			}
			if !seen {
				t.Error("admitted audit event is absent")
			}
		})
	}
}

// TestAuthTenantAuthorityTokenShape is oracle 2: a bundle with no H pins its full
// business facts and invents no human fence.
func TestAuthTenantAuthorityTokenShape(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant := tenants[0]
			b := ataFacts(t, s, tenant)
			if len(b.Facts) != 1 || len(b.UserAuthorities) != 0 {
				t.Fatalf("token bundle shape: %+v", b)
			}
			epochBefore, _ := ataObserve(t, s, tenant)
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				if err := as.(store.AuthTenantAuthorityBarrier).LockAuthTenantAuthority(ctx, tenant, b); err != nil {
					return err
				}
				if held := as.(*authScope).ts.directoryWriter.heldUsers; len(held) != 0 {
					t.Errorf("token bundle reserved %d User fences", len(held))
				}
				_, err := ataMarker(ctx, as, tenant, "ata-token")
				return err
			}); err != nil {
				t.Fatalf("token admission: %v", err)
			}
			if got := ataCountMarkers(t, s, "ata-token"); got != 1 {
				t.Errorf("token markers=%d, want 1", got)
			}
			if after, _ := ataObserve(t, s, tenant); after != epochBefore {
				t.Errorf("token admission bumped generation %d -> %d", epochBefore, after)
			}
			_ = users
		})
	}
}

// TestAuthTenantAuthorityStaleAndGrammar is oracle 3: stale H and stale E are
// ErrConflict from the barrier, a swallowed admission failure cannot commit, and
// every grammar refusal leaves no marker.
func TestAuthTenantAuthorityStaleAndGrammar(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant, other, admin := tenants[0], tenants[1], users[0]
			live := ataFacts(t, s, tenant, admin.ID)

			staleH := ataFacts(t, s, tenant, admin.ID)
			staleH.UserAuthorities[0].Version++
			staleE := ataFacts(t, s, tenant, admin.ID)
			staleE.Facts[0].Version++

			for name, bundle := range map[string]store.AuthoritySnapshotBundle{
				"stale-H": staleH, "stale-E": staleE,
			} {
				t.Run(name, func(t *testing.T) {
					err := s.AuthMutate(ctx, func(as store.AuthScope) error {
						return as.(store.AuthTenantAuthorityBarrier).
							LockAuthTenantAuthority(ctx, tenant, bundle)
					})
					if !errors.Is(err, store.ErrConflict) {
						t.Fatalf("err=%v, want ErrConflict through the envelope", err)
					}
				})
			}

			// A callback that deliberately discards the admission failure must not
			// commit its marker: the poisoned tracker refuses the commit.
			t.Run("swallowed", func(t *testing.T) {
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					_ = as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, staleH)
					_, _ = ataMarker(ctx, as, tenant, "ata-swallowed")
					return nil
				})
				if err == nil {
					t.Fatal("swallowed admission failure committed")
				}
				if got := ataCountMarkers(t, s, "ata-swallowed"); got != 0 {
					t.Errorf("markers=%d after swallowed failure, want 0", got)
				}
			})

			missingH := ataFacts(t, s, tenant)
			missingH.UserAuthorities = []store.UserAuthorityFactRef{{UserID: model.NewID(), Version: 1}}
			missingE := ataFacts(t, s, tenant, admin.ID)
			missingE.Facts[0].ID = model.ID(other)
			overBudget := ataFacts(t, s, tenant, admin.ID)
			for i := 0; i < authTenantAuthorityUserBudget; i++ {
				overBudget.UserAuthorities = append(overBudget.UserAuthorities,
					store.UserAuthorityFactRef{UserID: model.NewID(), Version: 1})
			}
			duplicate := ataFacts(t, s, tenant, admin.ID)
			duplicate.Facts = append(duplicate.Facts, duplicate.Facts[0])
			unsupported := ataFacts(t, s, tenant, admin.ID)
			unsupported.Facts[0].Kind = "core.user_invite"
			malformed := ataFacts(t, s, tenant, admin.ID)
			malformed.Facts[0].Version = 0
			hInFacts := ataFacts(t, s, tenant, admin.ID)
			hInFacts.Facts = append(hInFacts.Facts,
				store.AuthorizationFactRef{Kind: model.UserAuthorityKind, ID: admin.ID, Version: 1})
			leaseInvalid := ataFacts(t, s, tenant, admin.ID)
			leased, err := store.NewLeaseFenceAuthorizationFactRef(
				model.DirectoryEpochKind, model.ID(tenant), leaseInvalid.Facts[0].Version,
				"subject", 1, model.NewTimestamp(time.Now().Add(time.Hour)))
			if err != nil {
				t.Fatal(err)
			}
			leaseInvalid.Facts[0] = leased
			empty := store.AuthoritySnapshotBundle{}
			conflictingH := ataFacts(t, s, tenant, admin.ID)
			conflictingH.UserAuthorities = append(conflictingH.UserAuthorities,
				store.UserAuthorityFactRef{UserID: admin.ID, Version: conflictingH.UserAuthorities[0].Version + 1})

			refusals := map[string]struct {
				tenant model.TenantID
				bundle store.AuthoritySnapshotBundle
			}{
				"missing-H":      {tenant, missingH},
				"cross-tenant-E": {tenant, missingE},
				"over-budget-H":  {tenant, overBudget},
				"duplicate-fact": {tenant, duplicate},
				"unsupported":    {tenant, unsupported},
				"malformed":      {tenant, malformed},
				"H-inside-facts": {tenant, hInFacts},
				"lease-mismatch": {tenant, leaseInvalid},
				"empty-facts":    {tenant, empty},
				"conflicting-H":  {tenant, conflictingH},
				"system-tenant":  {model.SystemTenantID, live},
				"zero-tenant":    {model.TenantID(""), live},
			}
			for name, c := range refusals {
				t.Run(name, func(t *testing.T) {
					selector := "ata-refuse-" + name
					err := s.AuthMutate(ctx, func(as store.AuthScope) error {
						if err := as.(store.AuthTenantAuthorityBarrier).
							LockAuthTenantAuthority(ctx, c.tenant, c.bundle); err != nil {
							return err
						}
						_, err := ataMarker(ctx, as, c.tenant, selector)
						return err
					})
					if err == nil {
						t.Fatal("refusal case succeeded")
					}
					if got := ataCountMarkers(t, s, selector); got != 0 {
						t.Errorf("markers=%d, want 0", got)
					}
				})
			}
		})
	}
}

// TestAuthTenantAuthorityOrderRefusals is oracle 4: View, duplicate acquisition,
// prior tenant authority, prior directory-writer use and prior audit order each
// produce the documented refusal.
func TestAuthTenantAuthorityOrderRefusals(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			live := ataFacts(t, s, tenant, admin.ID)

			t.Run("view", func(t *testing.T) {
				err := s.AuthView(ctx, func(as store.AuthScope) error {
					barrier, ok := as.(store.AuthTenantAuthorityBarrier)
					if !ok {
						t.Fatal("read view lost the assertion; callers could not fail closed")
					}
					return barrier.LockAuthTenantAuthority(ctx, tenant, live)
				})
				if !errors.Is(err, store.ErrReadOnly) {
					t.Fatalf("view err=%v, want ErrReadOnly", err)
				}
				if got := ataCountMarkers(t, s, "ata-view"); got != 0 {
					t.Errorf("view wrote %d markers", got)
				}
			})

			t.Run("duplicate", func(t *testing.T) {
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					b := as.(store.AuthTenantAuthorityBarrier)
					if err := b.LockAuthTenantAuthority(ctx, tenant, live); err != nil {
						return err
					}
					second := b.LockAuthTenantAuthority(ctx, tenant, live)
					if !errors.Is(second, errAuthTenantAuthorityRepeated) {
						t.Errorf("second acquisition err=%v", second)
					}
					return second
				})
				if err == nil {
					t.Fatal("duplicate acquisition committed")
				}
			})

			t.Run("prior-tenant-authority", func(t *testing.T) {
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					// Stand in for an earlier tenant authority acquisition in the
					// same transaction; the barrier must refuse rather than take a
					// second, later authority.
					as.(*authScope).ts.authorityLocked = true
					got := as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, live)
					if !errors.Is(got, errAuthTenantAuthorityRepeated) {
						t.Errorf("prior authority err=%v", got)
					}
					return got
				})
				if err == nil {
					t.Fatal("prior authority committed")
				}
			})

			t.Run("prior-directory-use", func(t *testing.T) {
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					// A real earlier wrapper: the auth audit log admits itself
					// through the global writer before any audit lock.
					if _, err := as.Audit().Append(ctx, model.AuditDraft{
						Actor: "user:" + admin.ID.String(), ActorKind: model.ActorUser,
						Action: "test.prior.directory",
					}); err != nil {
						return err
					}
					got := as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, live)
					if !errors.Is(got, errAuthTenantAuthorityOrder) {
						t.Errorf("prior directory use err=%v", got)
					}
					return got
				})
				if err == nil {
					t.Fatal("prior directory use committed")
				}
			})

			t.Run("prior-audit-order", func(t *testing.T) {
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					// The tracker's own record of the known inverse order.
					as.(*authScope).ts.directoryWriter.noteAudit()
					got := as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, live)
					if !errors.Is(got, errAuthTenantAuthorityOrder) {
						t.Errorf("audit-before-directory err=%v", got)
					}
					return got
				})
				if err == nil {
					t.Fatal("audit-before-directory committed")
				}
			})

			// A pre-admission refusal must leave a USABLE transaction: it performs
			// no source write and poisons nothing.
			t.Run("pre-admission-does-not-poison", func(t *testing.T) {
				bad := ataFacts(t, s, tenant, admin.ID)
				bad.Facts[0].Version = 0
				if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					if err := as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, bad); err == nil {
						t.Fatal("malformed input was admitted")
					}
					sc := as.(*authScope).ts
					if sc.authorityLocked {
						t.Error("pre-admission refusal set authorityLocked")
					}
					if sc.directoryWriter.poisoned != nil {
						t.Error("pre-admission refusal poisoned the tracker")
					}
					if sc.directoryWriter.locked {
						t.Error("pre-admission refusal took directory admission")
					}
					_, err := ataMarker(ctx, as, tenant, "ata-after-refusal")
					return err
				}); err != nil {
					t.Fatalf("transaction unusable after a pre-admission refusal: %v", err)
				}
				if got := ataCountMarkers(t, s, "ata-after-refusal"); got != 1 {
					t.Errorf("markers=%d, want 1", got)
				}
			})
		})
	}
}

// TestAuthTenantAuthorityRestoration is oracle 5: SYSTEM isolation after success
// and after an ordinary stale failure, and every injected binding fault prevents
// commit even when the callback swallows the error.
func TestAuthTenantAuthorityRestoration(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			live := ataFacts(t, s, tenant, admin.ID)

			assertSystemIsolation := func(t *testing.T, as store.AuthScope) {
				t.Helper()
				sc := as.(*authScope).ts
				if sc.tenant != model.SystemTenantID {
					t.Fatalf("logical scope=%s, want SYSTEM", sc.tenant)
				}
				presented, err := readUserAuthorityPresentation(ctx, sc.tx, s.dia)
				if err != nil {
					t.Fatalf("read presentation: %v", err)
				}
				if presented != model.SystemTenantID.String() {
					t.Fatalf("SQL presentation=%s, want SYSTEM", presented)
				}
				// A retained auth repository must still be a SYSTEM repository: the
				// business tenant's own invitations must not be visible through it.
				found, _, err := as.Invites().List(ctx, model.Query{
					Filters: []model.Filter{{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()}},
					Limit:   5,
				})
				if err != nil {
					t.Fatalf("list through retained repository: %v", err)
				}
				for _, inv := range found {
					if inv.TenantID != model.SystemTenantID {
						t.Errorf("business row leaked: invite %s in tenant %s", inv.ID, inv.TenantID)
					}
				}
			}

			t.Run("isolation-after-success", func(t *testing.T) {
				if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					if err := as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, live); err != nil {
						return err
					}
					assertSystemIsolation(t, as)
					return nil
				}); err != nil {
					t.Fatalf("admitted transaction: %v", err)
				}
			})

			t.Run("isolation-after-stale", func(t *testing.T) {
				stale := ataFacts(t, s, tenant, admin.ID)
				stale.Facts[0].Version++
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					if got := as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, stale); !errors.Is(got, store.ErrConflict) {
						t.Errorf("stale err=%v", got)
					}
					assertSystemIsolation(t, as)
					return nil
				})
				if err == nil {
					t.Fatal("poisoned transaction committed")
				}
			})

			// Every injected binding fault must prevent commit even though the
			// callback below swallows the error. Each seam is unexported and nil
			// outside this test.
			faults := []struct {
				name string
				arm  func(t *testing.T, tx *txSeam)
			}{
				{
					name: "bind-failure",
					arm: func(t *testing.T, _ *txSeam) {
						authTenantAuthorityBindTestHook = func() error {
							return errors.New("injected bind failure")
						}
						t.Cleanup(func() { authTenantAuthorityBindTestHook = nil })
					},
				},
				{
					name: "restore-failure",
					arm: func(t *testing.T, _ *txSeam) {
						authTenantAuthorityRestoreTestHook = func(context.Context, model.TenantID) error {
							return errors.New("injected restore failure")
						}
						t.Cleanup(func() { authTenantAuthorityRestoreTestHook = nil })
					},
				},
				{
					name: "wrong-restored-presentation",
					arm: func(t *testing.T, seam *txSeam) {
						// The substituted restore lands on a DIFFERENT tenant, so the
						// post-restore re-read observes a value that is not the
						// captured SYSTEM one and must poison the transaction.
						authTenantAuthorityRestoreTestHook = func(ctx context.Context, _ model.TenantID) error {
							return s.dia.BindTenant(ctx, seam.tx, tenants[1])
						}
						t.Cleanup(func() { authTenantAuthorityRestoreTestHook = nil })
					},
				},
				{
					name: "cancellation-during-borrow",
					arm: func(t *testing.T, seam *txSeam) {
						authTenantAuthorityBorrowedTestHook = func(ctx context.Context) error {
							seam.cancel()
							return context.Canceled
						}
						t.Cleanup(func() { authTenantAuthorityBorrowedTestHook = nil })
					},
				},
			}
			for _, f := range faults {
				t.Run(f.name, func(t *testing.T) {
					selector := "ata-fault-" + f.name
					callCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					seam := &txSeam{cancel: cancel}
					f.arm(t, seam)
					err := s.AuthMutate(callCtx, func(as store.AuthScope) error {
						seam.tx = as.(*authScope).ts.tx
						// The callback deliberately swallows whatever happened.
						_ = as.(store.AuthTenantAuthorityBarrier).
							LockAuthTenantAuthority(callCtx, tenant, live)
						_, _ = ataMarker(context.Background(), as, tenant, selector)
						return nil
					})
					if err == nil {
						t.Fatalf("%s: swallowed fault committed", f.name)
					}
					if got := ataCountMarkers(t, s, selector); got != 0 {
						t.Errorf("%s: markers=%d, want 0", f.name, got)
					}
				})
			}
		})
	}
}

// txSeam lets a fault case reach the transaction the callback is running on and
// cancel its context, without any exported or runtime-configurable seam.
type txSeam struct {
	tx     *sql.Tx
	sc     *tenantScope
	cancel context.CancelFunc
}

// TestAuthTenantAuthorityRefusesBorrowedAuditCache proves the guard that keeps a
// FUTURE audited authority descriptor from leaking borrowed audit state. No
// currently allowlisted descriptor is descriptor-Audited, so the only honest way
// to exercise the guard is to make the borrowed interval do what such a
// descriptor would do: cache an audit log while the business tenant is bound.
func TestAuthTenantAuthorityRefusesBorrowedAuditCache(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			live := ataFacts(t, s, tenant, admin.ID)

			seam := &txSeam{}
			authTenantAuthorityBorrowedTestHook = func(context.Context) error {
				// Exactly what sc.repo(desc) would do for an audited descriptor.
				seam.sc.auditLog()
				return nil
			}
			t.Cleanup(func() { authTenantAuthorityBorrowedTestHook = nil })

			var borrowedTenant model.TenantID
			err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				seam.sc = as.(*authScope).ts
				// The callback deliberately swallows the refusal.
				_ = as.(store.AuthTenantAuthorityBarrier).
					LockAuthTenantAuthority(ctx, tenant, live)
				if seam.sc.audit != nil {
					borrowedTenant = seam.sc.audit.tenant
				}
				_, _ = ataMarker(ctx, as, tenant, "ata-audit-cache")
				return nil
			})
			if err == nil {
				t.Fatal("a borrowed audit cache was allowed to commit")
			}
			if !borrowedTenant.IsZero() {
				t.Errorf("borrowed audit log escaped to the callback, pinned to %s", borrowedTenant)
			}
			if got := ataCountMarkers(t, s, "ata-audit-cache"); got != 0 {
				t.Errorf("markers=%d, want 0", got)
			}
		})
	}
}

// TestAuthTenantAuthoritySQLiteWriterAdmission is oracle 6's SQLite half. SQLite
// admits ONE writer, so this deliberately does not claim to reproduce the
// PostgreSQL row-lock schedule: it asserts the real property this engine has,
// namely that a second auth writer cannot interleave with an admitted one and
// runs only after it finishes.
func TestAuthTenantAuthoritySQLiteWriterAdmission(t *testing.T) {
	ctx := context.Background()
	s, _, users, tenants := f2aFreshTarget(t, store.EngineSQLite)
	tenant, admin, target := tenants[0], users[0], users[1]
	live := ataFacts(t, s, tenant, admin.ID)

	admitted := make(chan struct{})
	secondStarted := make(chan struct{})
	// The contender parks on <-admitted; release it unconditionally so an early
	// failure cannot strand it until the outer go test timeout.
	var admitOnce sync.Once
	releasePeers := func() { admitOnce.Do(func() { close(admitted) }) }
	t.Cleanup(releasePeers)
	var (
		wg sync.WaitGroup
		// secondEntered is sampled while the first writer is still INSIDE its
		// callback. A flag set after that callback returns cannot prove ordering:
		// the contender may correctly enter after commit but before the flag is set.
		secondEntered     atomic.Bool
		secondEnteredHeld bool
		secondErr         error
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-admitted
		close(secondStarted)
		secondErr = s.AuthMutate(ctx, func(as store.AuthScope) error {
			secondEntered.Store(true)
			fresh, err := as.Users().Get(ctx, target.ID)
			if err != nil {
				return err
			}
			fresh.DisplayName = "sqlite-second"
			_, err = as.Users().Update(ctx, fresh)
			return err
		})
	}()

	if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := as.(store.AuthTenantAuthorityBarrier).
			LockAuthTenantAuthority(ctx, tenant, live); err != nil {
			return err
		}
		releasePeers()
		<-secondStarted
		// Give the contender a real opportunity to interleave, then sample INSIDE
		// this callback, while this writer still holds the transaction. On a
		// correct single-writer engine the contender cannot have entered.
		for i := 0; i < 50 && !secondEntered.Load(); i++ {
			time.Sleep(2 * time.Millisecond)
		}
		secondEnteredHeld = secondEntered.Load()
		_, err := ataMarker(ctx, as, tenant, "ata-sqlite-admission")
		return err
	}); err != nil {
		t.Fatalf("admitted transaction: %v", err)
	}
	wg.Wait()

	if secondErr != nil {
		t.Fatalf("second writer never proceeded: %v", secondErr)
	}
	if secondEnteredHeld {
		t.Error("a second auth writer entered while the admitted transaction held the writer")
	}
	if !secondEntered.Load() {
		t.Error("the second writer never ran, so serialization was not exercised")
	}
	if got := ataCountMarkers(t, s, "ata-sqlite-admission"); got != 1 {
		t.Errorf("markers=%d, want 1", got)
	}
}

// ataEntryPresentationCase is the ATA1-I1 regression: an AuthMutate whose LOGICAL
// scope is SYSTEM but whose ACTUAL SQL presentation is a business tenant, or whose
// entry presentation cannot be read at all. The bundle is complete and valid, so
// nothing but the entry state can explain the refusal.
//
// The check must happen before directoryWriteTracker.prepare, which rebinds the
// transaction to the writer's permanent presentation and would silently repair the
// mismatch. The assertions below therefore prove ZERO admission — no global
// directory lock, no reserved H, no bumped tenant — rather than merely proving a
// non-nil error.
func ataEntryPresentationCase(t *testing.T, engine store.Engine, unreadable bool) {
	t.Helper()
	ctx := context.Background()
	s, _, users, tenants := f2aFreshTarget(t, engine)
	tenant, admin, wrong := tenants[0], users[0], tenants[1]
	live := ataFacts(t, s, tenant, admin.ID)
	selector := "ata-entry-presentation"
	if unreadable {
		selector = "ata-entry-unreadable"
	}

	var (
		refusal       error
		logicalTenant model.TenantID
		admitted      bool
		heldUsers     int
		bumpedTenants int
		authorityFlag bool
	)
	err := s.AuthMutate(ctx, func(as store.AuthScope) error {
		sc := as.(*authScope).ts
		if unreadable {
			authTenantAuthorityEntryReadTestHook = func() error {
				return errors.New("injected entry presentation read failure")
			}
			t.Cleanup(func() { authTenantAuthorityEntryReadTestHook = nil })
		} else {
			// Move ONLY the actual SQL presentation. The logical field stays
			// SYSTEM, which is exactly the state the old code could not see.
			if err := s.dia.BindTenant(ctx, sc.tx, wrong); err != nil {
				return err
			}
		}
		// The callback deliberately swallows the refusal and writes anyway.
		refusal = as.(store.AuthTenantAuthorityBarrier).
			LockAuthTenantAuthority(ctx, tenant, live)
		logicalTenant = sc.tenant
		admitted = sc.directoryWriter.locked
		heldUsers = len(sc.directoryWriter.heldUsers)
		bumpedTenants = len(sc.directoryWriter.bumped)
		authorityFlag = sc.authorityLocked
		_, _ = ataMarker(ctx, as, tenant, selector)
		return nil
	})

	if refusal == nil {
		t.Fatal("a wrong entry presentation was admitted")
	}
	if !errors.Is(refusal, store.ErrDirectoryUnavailable) {
		t.Errorf("refusal=%v, want an ErrDirectoryUnavailable entry refusal", refusal)
	}
	// Zero admission: the refusal landed before prepare could normalize anything.
	if admitted {
		t.Error("the wrong entry presentation reached global directory admission")
	}
	if heldUsers != 0 {
		t.Errorf("reserved %d User authorities before refusing", heldUsers)
	}
	if bumpedTenants != 0 {
		t.Errorf("bumped %d tenants before refusing", bumpedTenants)
	}
	if authorityFlag {
		t.Error("an entry refusal consumed the authority acquisition")
	}
	// The logical scope is untouched: nothing rebound it, in either direction.
	if logicalTenant != model.SystemTenantID {
		t.Errorf("logical scope=%s, want SYSTEM", logicalTenant)
	}
	// Swallowing the refusal must not let the marker commit.
	if err == nil {
		t.Fatal("a swallowed entry refusal committed")
	}
	if got := ataCountMarkers(t, s, selector); got != 0 {
		t.Errorf("markers=%d after a swallowed entry refusal, want 0", got)
	}
}

// TestAuthTenantAuthorityRefusesWrongEntryPresentation closes ATA1-I1 for the
// mismatch case on both engines.
func TestAuthTenantAuthorityRefusesWrongEntryPresentation(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) { ataEntryPresentationCase(t, engine, false) })
	}
}

// TestAuthTenantAuthorityRefusesUnreadableEntryPresentation closes ATA1-I1 for the
// read-failure case through the bounded local seam.
func TestAuthTenantAuthorityRefusesUnreadableEntryPresentation(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) { ataEntryPresentationCase(t, engine, true) })
	}
}

// TestAuthTenantAuthorityMalformedInputDoesNotPoisonValidPresentation preserves
// the OTHER half of the contract: on a valid entry presentation, ordinary
// malformed input is still a precise, non-poisoning refusal.
func TestAuthTenantAuthorityMalformedInputDoesNotPoisonValidPresentation(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			bad := ataFacts(t, s, tenant, admin.ID)
			bad.Facts[0].Version = 0
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				sc := as.(*authScope).ts
				if err := as.(store.AuthTenantAuthorityBarrier).
					LockAuthTenantAuthority(ctx, tenant, bad); err == nil {
					t.Fatal("malformed input was admitted")
				}
				if sc.bindingPoison != nil {
					t.Errorf("malformed input poisoned a valid presentation: %v", sc.bindingPoison)
				}
				if sc.directoryWriter.poisoned != nil {
					t.Errorf("malformed input poisoned the tracker: %v", sc.directoryWriter.poisoned)
				}
				_, err := ataMarker(ctx, as, tenant, "ata-malformed-usable")
				return err
			}); err != nil {
				t.Fatalf("transaction unusable after malformed input: %v", err)
			}
			if got := ataCountMarkers(t, s, "ata-malformed-usable"); got != 1 {
				t.Errorf("markers=%d, want 1", got)
			}
		})
	}
}
