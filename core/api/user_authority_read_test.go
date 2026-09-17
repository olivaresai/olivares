// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type completeReadPolicy struct {
	facts           map[string]store.AuthorizationFactRef
	deny            string
	observed, until time.Time
}

func (p *completeReadPolicy) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{}, errors.New("legacy policy evaluation")
}
func (p *completeReadPolicy) EvaluateEvidence(_ context.Context, req auth.Request) (auth.PolicyEvidenceDecision, error) {
	check := auth.CheckEvidence{Verdict: auth.CheckClean, Code: "read_fixture_clean"}
	if req.Resource.ID == p.deny {
		check = auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "read_fixture_deny"}
	}
	var facts []store.AuthorizationFactRef
	if fact, ok := p.facts[req.Resource.ID]; ok {
		facts = []store.AuthorizationFactRef{fact}
	}
	return auth.PolicyEvidenceDecision{ForbidAbsence: check, Facts: facts, ObservedAt: p.observed, FreshUntil: p.until}, nil
}

type completeReadScopeSpy struct {
	store.Scope
	reader  store.AuthoritySnapshotBundleReader
	bundles []store.AuthoritySnapshotBundle
}

func (s *completeReadScopeSpy) ValidateAuthoritySnapshotBundle(ctx context.Context, bundle store.AuthoritySnapshotBundle) error {
	s.bundles = append(s.bundles, store.AuthoritySnapshotBundle{
		Facts:           append([]store.AuthorizationFactRef(nil), bundle.Facts...),
		UserAuthorities: append([]store.UserAuthorityFactRef(nil), bundle.UserAuthorities...),
	})
	return s.reader.ValidateAuthoritySnapshotBundle(ctx, bundle)
}

func TestCompleteReadAuthoritySQLite(t *testing.T) { testCompleteReadAuthority(t, store.EngineSQLite) }
func TestCompleteReadAuthorityPostgres(t *testing.T) {
	testCompleteReadAuthority(t, store.EnginePostgres)
}

func testCompleteReadAuthority(t *testing.T, engine store.Engine) {
	cfg := store.Config{Engine: engine, DSN: ":memory:", Debug: true, MaxConns: 4}
	if engine == store.EnginePostgres {
		dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
		cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = dsns.App, dsns.Owner, dsns.Admin
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := sqlstore.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if engine == store.EnginePostgres {
		db, err := sql.Open("pgx", cfg.DSN)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var super, bypass, owner bool
		if err := db.QueryRowContext(ctx, `SELECT r.rolsuper,r.rolbypassrls,c.relowner=r.oid FROM pg_roles r JOIN pg_class c ON c.relname='core_user_authority' JOIN pg_namespace n ON n.oid=c.relnamespace WHERE r.rolname=current_user AND n.nspname='public'`).Scan(&super, &bypass, &owner); err != nil {
			t.Fatal(err)
		}
		if super || bypass || owner {
			t.Fatal("application role is privileged")
		}
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "complete-read", Slug: "complete-read", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sessionCredential, err := auth.NewCredential(auth.PrefixSession)
	if err != nil {
		t.Fatal(err)
	}
	tokenCredential, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	var user model.User
	var session model.AuthSession
	expires := model.NewTimestamp(time.Now().Add(time.Hour))
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(ctx, model.User{Email: "complete-read@example.test", Status: model.StatusActive})
		if err != nil {
			return err
		}
		if _, err := as.Memberships().Create(ctx, model.Membership{UserID: user.ID, TargetTenantID: tenant, Role: auth.RoleViewer}); err != nil {
			return err
		}
		session, err = as.Sessions().Create(ctx, model.AuthSession{UserID: user.ID, Selector: sessionCredential.Selector, SecretHash: sessionCredential.SecretHash, ExpiresAt: expires, AAL: auth.AAL1, AMR: []string{"pwd"}})
		if err != nil {
			return err
		}
		_, err = as.Tokens().Create(ctx, model.APIToken{Name: "directory-only", UserID: user.ID, Selector: tokenCredential.Selector, SecretHash: tokenCredential.SecretHash, BoundTenantID: tenant, Role: auth.RoleViewer, ExpiresAt: &expires})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	policy := &completeReadPolicy{facts: make(map[string]store.AuthorizationFactRef)}
	var resources []auth.ResourceAttrs
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < 130; i++ {
			row, err := sc.Agents().Create(ctx, model.Agent{Name: fmt.Sprintf("read-%03d", i)})
			if err != nil {
				return err
			}
			policy.facts[row.ID.String()] = store.AuthorizationFactRef{Kind: "core.agent", ID: row.ID, Version: row.Version}
			resources = append(resources, auth.ResourceAttrs{Kind: "agent", ID: row.ID.String()})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	policy.observed, policy.until, policy.deny = time.Now(), time.Now().Add(time.Minute), resources[len(resources)-1].ID
	authr := auth.NewAuthenticator(st, nil)
	az := auth.NewAuthorizer(policy)
	legacy := api.NewReadRowAuthorizationPort(az, authr)
	complete, ok := legacy.(api.CompleteReadRowAuthorizationPort)
	if !ok {
		t.Fatal("complete interface lost behind generic return")
	}
	raw, err := authr.Authenticate(ctx, sessionCredential.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, principal, err := complete.RefreshReadPrincipal(ctx, raw, tenant)
	if err != nil {
		t.Fatal(err)
	}
	req := auth.Request{Principal: principal, Tenant: tenant, Permission: "agent:read"}
	set, err := complete.DecideReadRows(ctx, principal, tenant, req.Permission, api.RouteMetadata{}, resources)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Decisions) != 130 || set.Allowed[129] {
		t.Fatal("denied candidate was omitted from complete result")
	}
	baseline := []api.CompleteReadRowBatch{{Set: set, Resources: resources}}
	validate := func(batches []api.CompleteReadRowBatch, question auth.Request) (int, error) {
		calls := 0
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			now, err := sc.(store.TransactionClock).TransactionNow(ctx)
			if err != nil {
				return err
			}
			spy := &completeReadScopeSpy{Scope: sc, reader: sc.(store.AuthoritySnapshotBundleReader)}
			err = api.ValidateCompleteReadRowBatches(ctx, spy, now.Time(), question, batches)
			calls = len(spy.bundles)
			for _, bundle := range spy.bundles {
				if len(bundle.Facts) < 1 || len(bundle.Facts) > 64 {
					t.Fatal("invalid fact chunk")
				}
				if question.Principal.Kind == auth.KindUser && (len(bundle.UserAuthorities) != 1 || bundle.UserAuthorities[0].UserID != user.ID) {
					t.Fatal("chunk lost expected User authority")
				}
			}
			return err
		})
		return calls, err
	}
	t.Run("human-batches-and-legacy-refusal", func(t *testing.T) {
		if calls, err := validate(baseline, req); err != nil || calls != 3 {
			t.Fatalf("complete chunks = %d / %v", calls, err)
		}
		if calls, err := validate(append(baseline, baseline...), req); err != nil || calls != 3 {
			t.Fatalf("duplicate authority = %d / %v", calls, err)
		}
		if _, err := legacy.DecideRows(ctx, principal, tenant, req.Permission, api.RouteMetadata{}, resources[:1]); !errors.Is(err, auth.ErrRouteUndecided) {
			t.Fatal("human legacy read succeeded")
		}
		ordinary, err := api.NewRowAuthorizationPort(az).DecideRows(ctx, principal, tenant, req.Permission, api.RouteMetadata{}, resources[:1])
		if err != nil {
			t.Fatal(err)
		}
		if err := api.CheckRowSet(ordinary, time.Now(), req, resources[:1]); err != nil {
			t.Fatal("ordinary row contract changed", err)
		}
		for _, d := range set.Decisions {
			if d.AllowWitness().Allows(time.Now()) {
				t.Fatal("complete H receipt promoted to ordinary proof")
			}
		}
	})
	t.Run("denied-and-missing-batch-integrity-before-SQL", func(t *testing.T) {
		for name, alter := range map[string]func(*api.CompleteReadRowBatch){
			"missing": func(b *api.CompleteReadRowBatch) { b.Set.Decisions = nil },
			"denied-zero": func(b *api.CompleteReadRowBatch) {
				b.Set.Decisions = append([]auth.RouteReadDecision(nil), set.Decisions...)
				b.Set.Decisions[129] = auth.RouteReadDecision{}
			},
			"denied-flipped": func(b *api.CompleteReadRowBatch) {
				b.Set.Allowed = append([]bool(nil), set.Allowed...)
				b.Set.Allowed[129] = true
			},
			"resource": func(b *api.CompleteReadRowBatch) {
				b.Resources = append([]auth.ResourceAttrs(nil), resources...)
				b.Resources[129].ID = model.NewID().String()
			},
		} {
			t.Run(name, func(t *testing.T) {
				b := baseline[0]
				alter(&b)
				if calls, err := validate([]api.CompleteReadRowBatch{b}, req); err == nil || calls != 0 {
					t.Fatalf("malformed batch reached SQL: %d / %v", calls, err)
				}
			})
		}
		if calls, err := validate(nil, req); err == nil || calls != 0 {
			t.Fatal("receipt-free empty page accepted")
		}
		outerResources := []auth.ResourceAttrs{{Kind: "agent"}}
		outer, err := complete.DecideReadRows(ctx, principal, tenant, req.Permission, api.RouteMetadata{}, outerResources)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := validate([]api.CompleteReadRowBatch{{Set: outer, Resources: outerResources}}, req); err != nil {
			t.Fatal("empty collection receipt refused", err)
		}
		old := policy.facts[resources[0].ID]
		changed := old
		changed.Version++
		policy.facts[resources[0].ID] = changed
		conflict, err := complete.DecideReadRows(ctx, principal, tenant, req.Permission, api.RouteMetadata{}, resources[:1])
		policy.facts[resources[0].ID] = old
		if err != nil {
			t.Fatal(err)
		}
		batches := append(append([]api.CompleteReadRowBatch(nil), baseline...), api.CompleteReadRowBatch{Set: conflict, Resources: resources[:1]})
		if calls, err := validate(batches, req); !errors.Is(err, store.ErrConflict) || calls != 0 {
			t.Fatalf("cross-batch contradiction reached SQL: %d / %v", calls, err)
		}
	})
	t.Run("optional-capability-confinement-and-cancellation", func(t *testing.T) {
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			now, err := sc.(store.TransactionClock).TransactionNow(ctx)
			if err != nil {
				return err
			}
			if err := api.ValidateCompleteReadRowBatches(ctx, struct{ store.Scope }{sc}, now.Time(), req, baseline); !errors.Is(err, store.ErrLineageUnavailable) {
				t.Fatal("missing bundle reader fell back", err)
			}
			ws, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return err
			}
			confined, err := store.ConfineWorkspace(ctx, sc, ws.ID)
			if err != nil {
				return err
			}
			if err := api.ValidateCompleteReadRowBatches(ctx, confined, now.Time(), req, baseline); err != nil {
				return err
			}
			cancelled, stop := context.WithCancel(ctx)
			prior := sc.Agents()
			stop()
			if err := api.ValidateCompleteReadRowBatches(cancelled, sc, now.Time(), req, baseline); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled barrier accepted", err)
			}
			if _, err := prior.Get(ctx, model.ID(resources[0].ID)); err == nil {
				t.Fatal("repository obtained before poison remained usable")
			}
			return nil
		})
		if !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatal("outer View ignored failed binding capture", err)
		}
	})
	t.Run("token-complete-and-legacy", func(t *testing.T) {
		raw, err := authr.Authenticate(ctx, tokenCredential.Token)
		if err != nil {
			t.Fatal(err)
		}
		tokenCtx, token, err := complete.RefreshReadPrincipal(ctx, raw, tenant)
		if err != nil {
			t.Fatal(err)
		}
		tokenReq := req
		tokenReq.Principal = token
		set, err := complete.DecideReadRows(tokenCtx, token, tenant, req.Permission, api.RouteMetadata{}, resources[:1])
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := set.Decisions[0].AuthorityFor(time.Now(), auth.Request{Principal: token, Tenant: tenant, Permission: req.Permission, Resource: resources[0]})
		if err != nil || len(bundle.UserAuthorities) != 0 {
			t.Fatal("token owner acquired human authority", err)
		}
		if _, err := validate([]api.CompleteReadRowBatch{{Set: set, Resources: resources[:1]}}, tokenReq); err != nil {
			t.Fatal(err)
		}
		old, err := legacy.DecideRows(tokenCtx, token, tenant, req.Permission, api.RouteMetadata{}, resources[:1])
		if err != nil {
			t.Fatal(err)
		}
		if err := api.CheckRowSet(old, time.Now(), tokenReq, resources[:1]); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("real-User-write-invalidates-unexpired-receipts", func(t *testing.T) {
		question := req
		question.Resource = resources[0]
		old, err := set.Decisions[0].AuthorityFor(time.Now(), question)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			current, err := as.Users().Get(ctx, user.ID)
			if err != nil {
				return err
			}
			current.DisplayName = "updated authority"
			_, err = as.Users().Update(ctx, current)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := set.Decisions[0].AuthorityFor(time.Now(), question); err != nil {
			t.Fatal("read window ended before causal validation", err)
		}
		if _, err := validate(baseline, req); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("old page survived User write: %v", err)
		}
		freshCtx, fresh, err := complete.RefreshReadPrincipal(ctx, principal, tenant)
		if err != nil {
			t.Fatal(err)
		}
		oldRef, _ := principal.Ref()
		newRef, _ := fresh.Ref()
		if oldRef != newRef {
			t.Fatal("refresh replaced credential")
		}
		freshSet, err := complete.DecideReadRows(freshCtx, fresh, tenant, req.Permission, api.RouteMetadata{}, resources[:1])
		if err != nil {
			t.Fatal(err)
		}
		freshReq := req
		freshReq.Principal = fresh
		freshQuestion := freshReq
		freshQuestion.Resource = resources[0]
		latest, err := freshSet.Decisions[0].AuthorityFor(time.Now(), freshQuestion)
		if err != nil || latest.UserAuthorities[0].Version <= old.UserAuthorities[0].Version {
			t.Fatal("real User writer did not advance observed H", err)
		}
		if _, err := validate([]api.CompleteReadRowBatch{{Set: freshSet, Resources: resources[:1]}}, freshReq); err != nil {
			t.Fatal(err)
		}
		// Current tenant facts with only the old H isolate the stale-H store check.
		latest.UserAuthorities = old.UserAuthorities
		if err := st.View(ctx, tenant, func(sc store.Scope) error { return store.ValidateReadAuthorityBundle(ctx, sc, latest) }); !errors.Is(err, store.ErrConflict) {
			t.Fatal("old H matched fresh tenant facts", err)
		}
		if calls, err := validate([]api.CompleteReadRowBatch{{Set: freshSet, Resources: resources[:1]}}, req); err == nil || calls != 0 {
			t.Fatal("refreshed principal receipt transplanted", err)
		}
		mixed := append(append([]api.CompleteReadRowBatch(nil), baseline...), api.CompleteReadRowBatch{Set: freshSet, Resources: resources[:1]})
		for _, question := range []auth.Request{req, freshReq} {
			if calls, err := validate(mixed, question); err == nil || calls != 0 {
				t.Fatal("different H reconstructions reached SQL together", err)
			}
		}
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			current, err := as.Sessions().Get(ctx, session.ID)
			if err != nil {
				return err
			}
			current.Revoked = true
			_, err = as.Sessions().Update(ctx, current)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := validate([]api.CompleteReadRowBatch{{Set: freshSet, Resources: resources[:1]}}, freshReq); !errors.Is(err, store.ErrConflict) {
			t.Fatal("old read survived session revocation", err)
		}
		if _, _, err := complete.RefreshReadPrincipal(ctx, fresh, tenant); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatal("revoked credential refreshed", err)
		}
	})
}
