// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// dabSpoolTarget is f2aFreshTarget with the global audit spool budget ENABLED.
// Budgeting is what makes an audit append take the single shared usage row
// (audit.go lockSpoolUsage, `WHERE id = 1 FOR UPDATE`), which is the row a
// SYSTEM auth writer and a business writer can contend over across partitions.
// The budget is deliberately enormous: this fixture exercises the shared LOCK,
// never the exhaustion policy.
func dabSpoolTarget(t *testing.T, engine store.Engine) (*sqlStore, []model.User, []model.TenantID) {
	t.Helper()
	ctx := context.Background()
	cfg := store.Config{
		Engine: engine,
		DSN:    filepath.Join(t.TempDir(), "directory-authority.db"),
		// Enabled global spool budgeting; see above.
		AuditSpoolMaxBytes: largeAuditSpoolBudget,
	}
	if engine == store.EnginePostgres {
		pg := isolatedPGSplit(t)
		cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
	}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tenants := []model.TenantID{
		provisionTenant(t, raw, "dab-a"),
		provisionTenant(t, raw, "dab-b"),
	}
	var users []model.User
	if err := raw.AuthMutate(ctx, func(as store.AuthScope) error {
		for i := 0; i < 2; i++ {
			u, err := as.Users().Create(ctx, model.User{
				Email: "dab-" + string(rune('a'+i)) + "@example.test", Status: model.StatusActive,
			})
			if err != nil {
				return err
			}
			users = append(users, u)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err != nil {
		t.Fatal(err)
	}
	raw, err = Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw.(*sqlStore), users, tenants
}

// dabMarker is the ordinary business write the barrier admits. The provider
// repository is a plain typed repository: it is NOT directory tracked, so
// committing one proves the admitted transaction could really mutate the
// business tenant without any directory write of its own.
func dabMarker(ctx context.Context, sc store.Scope, label string) (model.ID, error) {
	p, err := sc.Providers().Create(ctx, model.Provider{
		Name: label, Kind: "openai", Status: model.StatusActive,
	})
	return p.ID, err
}

func dabCountMarkers(t *testing.T, s *sqlStore, tenant model.TenantID, label string) int {
	t.Helper()
	var n int
	if err := s.View(context.Background(), tenant, func(sc store.Scope) error {
		found, _, err := sc.Providers().List(context.Background(), model.Query{
			Filters: []model.Filter{{Column: "name", Op: model.OpEq, Value: label}}, Limit: 10,
		})
		n = len(found)
		return err
	}); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	return n
}

func dabBarrier(t *testing.T, sc store.Scope) store.DirectoryAuthoritySnapshotLocker {
	t.Helper()
	barrier, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
	if !ok {
		t.Fatal("ordinary scope lacks the directory-authority barrier")
	}
	return barrier
}

// TestDirectoryAuthorityPositive is oracle 1: a complete human bundle admits a
// real business write plus its tenant audit event in the declared order,
// admission alone bumps neither H nor the business generation, and the event
// keeps the supplied actor.
func TestDirectoryAuthorityPositive(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, users, tenants := dabSpoolTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			epochBefore, hBefore := ataObserve(t, s, tenant, admin.ID)

			var markerID model.ID
			actor := "user:" + admin.ID.String()
			// The bundle is read BEFORE the write transaction opens: SQLite admits
			// one writer, so opening a read transaction inside the callback would
			// deadlock the fixture rather than test anything.
			live := ataFacts(t, s, tenant, admin.ID)
			if err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
				if err := dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live); err != nil {
					return err
				}
				ts := sc.(*tenantScope)
				if ts.tenant != tenant {
					t.Errorf("scope tenant moved to %s", ts.tenant)
				}
				if !ts.directoryWriter.locked {
					t.Error("successful admission did not retain the directory lock")
				}
				if !ts.authorityLocked {
					t.Error("successful admission did not consume the authority acquisition")
				}
				if got := len(ts.directoryWriter.bumped); got != 0 {
					t.Errorf("admission bumped %d tenants", got)
				}
				id, err := dabMarker(ctx, sc, "dab-positive")
				if err != nil {
					return err
				}
				markerID = id
				// The tenant audit append comes LAST, after admission and the
				// product row, which is the order the barrier exists to impose.
				_, err = sc.Audit().Append(ctx, model.AuditDraft{
					Actor: actor, ActorKind: model.ActorUser,
					Action: "provider.create", TargetKind: "core.provider", TargetID: id,
				})
				if err != nil {
					return err
				}
				if ts.directoryWriter.auditBeforeDirectory {
					t.Error("the admitted audit append was recorded as audit-before-directory")
				}
				return nil
			}); err != nil {
				t.Fatalf("admitted mutation: %v", err)
			}

			if got := dabCountMarkers(t, s, tenant, "dab-positive"); got != 1 {
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
			if err := s.View(ctx, tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(ctx, 0, func(e model.AuditEvent) error {
					if e.TargetID != markerID {
						return nil
					}
					seen = true
					if e.Actor != actor || e.ActorKind != model.ActorUser {
						t.Errorf("audit actor=%q kind=%q, want %q/%q",
							e.Actor, e.ActorKind, actor, model.ActorUser)
					}
					if e.TenantID != tenant {
						t.Errorf("audit event tenant=%s, want %s", e.TenantID, tenant)
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

// TestDirectoryAuthorityTokenShape is oracle 2: a bundle with no H pins its full
// business facts and invents no human fence. The entry presentation check still
// runs, which is the whole reason it cannot be delegated to the bundle: the
// bundle's own binding re-read is never reached for this shape.
func TestDirectoryAuthorityTokenShape(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, tenants := dabSpoolTarget(t, engine)
			tenant := tenants[0]
			b := ataFacts(t, s, tenant)
			if len(b.Facts) != 1 || len(b.UserAuthorities) != 0 {
				t.Fatalf("token bundle shape: %+v", b)
			}
			epochBefore, _ := ataObserve(t, s, tenant)
			if err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
				if err := dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, b); err != nil {
					return err
				}
				if held := sc.(*tenantScope).directoryWriter.heldUsers; len(held) != 0 {
					t.Errorf("token bundle reserved %d User fences", len(held))
				}
				_, err := dabMarker(ctx, sc, "dab-token")
				return err
			}); err != nil {
				t.Fatalf("token admission: %v", err)
			}
			if got := dabCountMarkers(t, s, tenant, "dab-token"); got != 1 {
				t.Errorf("token markers=%d, want 1", got)
			}
			if after, _ := ataObserve(t, s, tenant); after != epochBefore {
				t.Errorf("token admission bumped generation %d -> %d", epochBefore, after)
			}
		})
	}
}

// TestDirectoryAuthorityStaleAndGrammar is oracle 3: stale H and stale E are
// ErrConflict through the envelope, a swallowed post-admission failure cannot
// commit, and every grammar refusal leaves no marker.
func TestDirectoryAuthorityStaleAndGrammar(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, users, tenants := dabSpoolTarget(t, engine)
			tenant, other, admin := tenants[0], tenants[1], users[0]

			staleH := ataFacts(t, s, tenant, admin.ID)
			staleH.UserAuthorities[0].Version++
			staleE := ataFacts(t, s, tenant, admin.ID)
			staleE.Facts[0].Version++

			for name, bundle := range map[string]store.AuthoritySnapshotBundle{
				"stale-H": staleH, "stale-E": staleE,
			} {
				t.Run(name, func(t *testing.T) {
					err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
						return dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, bundle)
					})
					if !errors.Is(err, store.ErrConflict) {
						t.Fatalf("err=%v, want ErrConflict through the envelope", err)
					}
				})
			}

			// A callback that deliberately discards a POST-ADMISSION failure must
			// not commit its marker: the poisoned tracker refuses the commit.
			t.Run("swallowed-post-admission", func(t *testing.T) {
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					ts := sc.(*tenantScope)
					_ = dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, staleH)
					if ts.directoryWriter.poisoned == nil {
						t.Error("a post-admission failure did not poison the tracker")
					}
					if !ts.authorityLocked {
						t.Error("a post-admission failure left the authority acquisition unconsumed")
					}
					_, _ = dabMarker(ctx, sc, "dab-swallowed")
					return nil
				})
				if err == nil {
					t.Fatal("swallowed admission failure committed")
				}
				if got := dabCountMarkers(t, s, tenant, "dab-swallowed"); got != 0 {
					t.Errorf("markers=%d after swallowed failure, want 0", got)
				}
			})

			missingH := ataFacts(t, s, tenant)
			missingH.UserAuthorities = []store.UserAuthorityFactRef{{UserID: model.NewID(), Version: 1}}
			crossTenantE := ataFacts(t, s, tenant, admin.ID)
			crossTenantE.Facts[0].ID = model.ID(other)
			overBudget := ataFacts(t, s, tenant, admin.ID)
			for i := 0; i < directoryAuthorityUserBudget; i++ {
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
			conflictingH := ataFacts(t, s, tenant, admin.ID)
			conflictingH.UserAuthorities = append(conflictingH.UserAuthorities,
				store.UserAuthorityFactRef{
					UserID: admin.ID, Version: conflictingH.UserAuthorities[0].Version + 1,
				})
			systemH := ataFacts(t, s, tenant)
			systemH.UserAuthorities = []store.UserAuthorityFactRef{
				{UserID: model.ID(model.SystemTenantID), Version: 1},
			}

			refusals := map[string]store.AuthoritySnapshotBundle{
				"missing-H":       missingH,
				"cross-tenant-E":  crossTenantE,
				"over-budget-H":   overBudget,
				"duplicate-fact":  duplicate,
				"unsupported":     unsupported,
				"malformed":       malformed,
				"H-inside-facts":  hInFacts,
				"lease-mismatch":  leaseInvalid,
				"empty-facts":     {},
				"conflicting-H":   conflictingH,
				"system-tenant-H": systemH,
			}
			for name, bundle := range refusals {
				t.Run(name, func(t *testing.T) {
					label := "dab-refuse-" + name
					err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
						if err := dabBarrier(t, sc).
							LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
							return err
						}
						_, err := dabMarker(ctx, sc, label)
						return err
					})
					if err == nil {
						t.Fatal("refusal case succeeded")
					}
					if got := dabCountMarkers(t, s, tenant, label); got != 0 {
						t.Errorf("markers=%d, want 0", got)
					}
				})
			}
		})
	}
}

// TestDirectoryAuthorityOrderAndEntryRefusals is oracle 4: every state the
// tracker can actually observe produces the documented refusal, and a
// pre-admission refusal on a valid presentation leaves a USABLE transaction.
func TestDirectoryAuthorityOrderAndEntryRefusals(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, users, tenants := dabSpoolTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			live := ataFacts(t, s, tenant, admin.ID)

			t.Run("view", func(t *testing.T) {
				err := s.View(ctx, tenant, func(sc store.Scope) error {
					// The assertion must still SUCCEED on a View: a consumer that
					// could not assert the port would fail closed on a capability
					// that is present, and never reach the real refusal.
					return dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
				})
				if !errors.Is(err, store.ErrReadOnly) {
					t.Fatalf("view err=%v, want ErrReadOnly", err)
				}
			})

			t.Run("system-scope", func(t *testing.T) {
				// The SAME concrete type backs the SYSTEM-tenant Mutate the auth
				// partition is built over, so the refusal is a runtime one and it
				// must poison: a business barrier has no meaning under SYSTEM.
				var refusal error
				err := s.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
					ts := sc.(*tenantScope)
					refusal = dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
					if ts.directoryWriter.locked {
						t.Error("a SYSTEM scope reached directory admission")
					}
					return nil
				})
				if refusal == nil {
					t.Fatal("a SYSTEM scope was admitted")
				}
				if !errors.Is(refusal, store.ErrDirectoryUnavailable) {
					t.Errorf("SYSTEM refusal=%v, want an ErrDirectoryUnavailable entry refusal", refusal)
				}
				if err == nil {
					t.Fatal("a swallowed SYSTEM refusal committed")
				}
			})

			t.Run("duplicate", func(t *testing.T) {
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					b := dabBarrier(t, sc)
					if err := b.LockDirectoryAuthoritySnapshot(ctx, live); err != nil {
						return err
					}
					second := b.LockDirectoryAuthoritySnapshot(ctx, live)
					if !errors.Is(second, errDirectoryAuthorityRepeated) {
						t.Errorf("second acquisition err=%v", second)
					}
					return second
				})
				if err == nil {
					t.Fatal("duplicate acquisition committed")
				}
			})

			t.Run("prior-tenant-authority", func(t *testing.T) {
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					// A real earlier acquisition through the existing legacy locker.
					if err := sc.(store.AuthoritySnapshotLocker).
						LockAuthoritySnapshot(ctx, live.Facts); err != nil {
						return err
					}
					got := dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
					if !errors.Is(got, errDirectoryAuthorityRepeated) {
						t.Errorf("prior authority err=%v", got)
					}
					return got
				})
				if err == nil {
					t.Fatal("prior authority committed")
				}
			})

			t.Run("prior-directory-use", func(t *testing.T) {
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					// A real earlier wrapper: the identity repository admits itself
					// through the global writer before it writes its source row.
					if _, err := sc.Identities().Create(ctx, model.Identity{
						Name: "dab prior directory", Kind: "service",
						ExternalID: "dab-prior-directory",
					}); err != nil {
						return err
					}
					got := dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
					if !errors.Is(got, errDirectoryAuthorityOrder) {
						t.Errorf("prior directory use err=%v", got)
					}
					return got
				})
				if err == nil {
					t.Fatal("prior directory use committed")
				}
			})

			t.Run("prior-audit-order", func(t *testing.T) {
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					// A REAL tenant audit append before any directory admission.
					// The ordinary audit log records the inverse order itself.
					if _, err := sc.Audit().Append(ctx, model.AuditDraft{
						Actor: "user:" + admin.ID.String(), ActorKind: model.ActorUser,
						Action: "test.prior.audit",
					}); err != nil {
						return err
					}
					if !sc.(*tenantScope).directoryWriter.auditBeforeDirectory {
						t.Fatal("the ordinary audit append did not record its own order")
					}
					got := dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
					if !errors.Is(got, errDirectoryAuthorityOrder) {
						t.Errorf("audit-before-directory err=%v", got)
					}
					return got
				})
				if err == nil {
					t.Fatal("audit-before-directory committed")
				}
			})

			t.Run("missing-tracker", func(t *testing.T) {
				var refusal error
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					ts := sc.(*tenantScope)
					keep := ts.directoryWriter
					ts.directoryWriter = nil
					refusal = dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
					ts.directoryWriter = keep
					_, _ = dabMarker(ctx, sc, "dab-missing-tracker")
					return nil
				})
				if !errors.Is(refusal, store.ErrDirectoryUnavailable) {
					t.Fatalf("missing tracker refusal=%v", refusal)
				}
				if err == nil {
					t.Fatal("a swallowed missing-tracker refusal committed")
				}
				if got := dabCountMarkers(t, s, tenant, "dab-missing-tracker"); got != 0 {
					t.Errorf("markers=%d, want 0", got)
				}
			})

			t.Run("existing-poison", func(t *testing.T) {
				injected := errors.New("injected earlier failure")
				var refusal error
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					ts := sc.(*tenantScope)
					ts.directoryWriter.poison(injected)
					refusal = dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
					if ts.directoryWriter.locked {
						t.Error("a poisoned tracker reached directory admission")
					}
					return nil
				})
				if !errors.Is(refusal, injected) {
					t.Fatalf("poison refusal=%v, want the earlier cause preserved", refusal)
				}
				if err == nil {
					t.Fatal("a poisoned transaction committed")
				}
			})

			// A pre-admission refusal must leave a USABLE transaction: it performs
			// no source write and poisons nothing.
			t.Run("pre-admission-does-not-poison", func(t *testing.T) {
				bad := ataFacts(t, s, tenant, admin.ID)
				bad.Facts[0].Version = 0
				if err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					ts := sc.(*tenantScope)
					if err := dabBarrier(t, sc).
						LockDirectoryAuthoritySnapshot(ctx, bad); err == nil {
						t.Fatal("malformed input was admitted")
					}
					if ts.authorityLocked {
						t.Error("pre-admission refusal set authorityLocked")
					}
					if ts.directoryWriter.poisoned != nil {
						t.Error("pre-admission refusal poisoned the tracker")
					}
					if ts.bindingPoison != nil {
						t.Error("pre-admission refusal poisoned a valid presentation")
					}
					if ts.directoryWriter.locked {
						t.Error("pre-admission refusal took directory admission")
					}
					_, err := dabMarker(ctx, sc, "dab-after-refusal")
					return err
				}); err != nil {
					t.Fatalf("transaction unusable after a pre-admission refusal: %v", err)
				}
				if got := dabCountMarkers(t, s, tenant, "dab-after-refusal"); got != 1 {
					t.Errorf("markers=%d, want 1", got)
				}
			})
		})
	}
}

// dabEntryPresentationCase is the DAB1 entry-state regression: an ordinary
// Mutate whose LOGICAL scope is the business tenant but whose ACTUAL SQL
// presentation is another partition, or whose entry presentation cannot be read
// at all. The bundle is complete and valid, so nothing but the entry state can
// explain the refusal.
//
// The check must happen BEFORE directoryWriteTracker.prepare, which rebinds the
// transaction to the writer's permanent presentation and would silently repair
// the mismatch. The assertions therefore prove ZERO admission — no global
// directory lock, no reserved H, no bumped tenant — rather than merely proving a
// non-nil error.
func dabEntryPresentationCase(t *testing.T, engine store.Engine, unreadable bool) {
	t.Helper()
	ctx := context.Background()
	s, users, tenants := dabSpoolTarget(t, engine)
	tenant, admin, wrong := tenants[0], users[0], tenants[1]
	live := ataFacts(t, s, tenant, admin.ID)
	label := "dab-entry-presentation"
	if unreadable {
		label = "dab-entry-unreadable"
	}

	var (
		refusal       error
		logicalTenant model.TenantID
		admitted      bool
		heldUsers     int
		bumpedTenants int
		authorityFlag bool
	)
	err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
		ts := sc.(*tenantScope)
		if unreadable {
			directoryAuthorityEntryReadTestHook = func() error {
				return errors.New("injected entry presentation read failure")
			}
			t.Cleanup(func() { directoryAuthorityEntryReadTestHook = nil })
		} else {
			// Move ONLY the actual SQL presentation. The logical field and the
			// tracker's permanent presentation both stay on the business tenant,
			// which is exactly the state a logical-only check cannot see.
			if err := s.dia.BindTenant(ctx, ts.tx, wrong); err != nil {
				return err
			}
		}
		// The callback deliberately swallows the refusal and writes anyway.
		refusal = dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
		logicalTenant = ts.tenant
		admitted = ts.directoryWriter.locked
		heldUsers = len(ts.directoryWriter.heldUsers)
		bumpedTenants = len(ts.directoryWriter.bumped)
		authorityFlag = ts.authorityLocked
		_, _ = dabMarker(ctx, sc, label)
		return nil
	})

	if refusal == nil {
		t.Fatal("a wrong entry presentation was admitted")
	}
	if !errors.Is(refusal, store.ErrDirectoryUnavailable) {
		t.Errorf("refusal=%v, want an ErrDirectoryUnavailable entry refusal", refusal)
	}
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
	if logicalTenant != tenant {
		t.Errorf("logical scope=%s, want %s", logicalTenant, tenant)
	}
	if err == nil {
		t.Fatal("a swallowed entry refusal committed")
	}
	if got := dabCountMarkers(t, s, tenant, label); got != 0 {
		t.Errorf("markers=%d after a swallowed entry refusal, want 0", got)
	}
}

func TestDirectoryAuthorityRefusesWrongEntryPresentation(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) { dabEntryPresentationCase(t, engine, false) })
	}
}

func TestDirectoryAuthorityRefusesUnreadableEntryPresentation(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) { dabEntryPresentationCase(t, engine, true) })
	}
}

// TestDirectoryAuthorityRefusesMismatchedTrackerPresentation covers the other
// half of step 1: a tracker whose PERMANENT presentation is not the bound
// tenant. prepare would rebind the transaction to that value and leave the
// admitted facts pinned under a partition the caller never asked for.
func TestDirectoryAuthorityRefusesMismatchedTrackerPresentation(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, users, tenants := dabSpoolTarget(t, engine)
			tenant, admin, wrong := tenants[0], users[0], tenants[1]
			live := ataFacts(t, s, tenant, admin.ID)

			var refusal error
			err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
				ts := sc.(*tenantScope)
				ts.directoryWriter.presentationTenant = wrong
				refusal = dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live)
				if ts.directoryWriter.locked {
					t.Error("a mismatched tracker presentation reached admission")
				}
				ts.directoryWriter.presentationTenant = tenant
				_, _ = dabMarker(ctx, sc, "dab-tracker-presentation")
				return nil
			})
			if !errors.Is(refusal, store.ErrDirectoryUnavailable) {
				t.Fatalf("refusal=%v", refusal)
			}
			if err == nil {
				t.Fatal("a swallowed tracker-presentation refusal committed")
			}
			if got := dabCountMarkers(t, s, tenant, "dab-tracker-presentation"); got != 0 {
				t.Errorf("markers=%d, want 0", got)
			}
		})
	}
}

// TestDirectoryAuthoritySQLiteWriterAdmission is the SQLite half of the
// schedule. SQLite admits ONE writer, so this deliberately does not claim to
// reproduce the PostgreSQL row-lock schedule: it asserts the real property this
// engine has, namely that a second writer cannot interleave with an admitted one
// and runs only after it finishes.
func TestDirectoryAuthoritySQLiteWriterAdmission(t *testing.T) {
	ctx := context.Background()
	s, users, tenants := dabSpoolTarget(t, store.EngineSQLite)
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
		// callback. A flag set after that callback returns cannot prove ordering.
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
			if _, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: "test.sqlite.auth.writer",
			}); err != nil {
				return err
			}
			fresh, err := as.Users().Get(ctx, target.ID)
			if err != nil {
				return err
			}
			fresh.DisplayName = "sqlite-second"
			_, err = as.Users().Update(ctx, fresh)
			return err
		})
	}()

	if err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := dabBarrier(t, sc).LockDirectoryAuthoritySnapshot(ctx, live); err != nil {
			return err
		}
		releasePeers()
		<-secondStarted
		// Give the contender a real opportunity to interleave, then sample INSIDE
		// this callback, while this writer still holds the transaction.
		for i := 0; i < 50 && !secondEntered.Load(); i++ {
			time.Sleep(2 * time.Millisecond)
		}
		secondEnteredHeld = secondEntered.Load()
		_, err := dabMarker(ctx, sc, "dab-sqlite-admission")
		return err
	}); err != nil {
		t.Fatalf("admitted transaction: %v", err)
	}
	wg.Wait()

	if secondErr != nil {
		t.Fatalf("second writer never proceeded: %v", secondErr)
	}
	if secondEnteredHeld {
		t.Error("a second writer entered while the admitted transaction held the writer")
	}
	if !secondEntered.Load() {
		t.Error("the second writer never ran, so serialization was not exercised")
	}
	if got := dabCountMarkers(t, s, tenant, "dab-sqlite-admission"); got != 1 {
		t.Errorf("markers=%d, want 1", got)
	}
}
