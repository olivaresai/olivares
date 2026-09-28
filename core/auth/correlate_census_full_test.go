// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The tenant-size census provisions 52,000 accounts, 50,000 directory rows in one
// tenant and 52,000 in another, and that is most of the package's time. Both
// cases read one fixture, built once per engine: the accounts are shared, and
// each case has its own tenant, which neither case writes. Each engine runs the
// census at the full tenant limits in one build and at a hundredth of them in
// the other, asserting the same invariants scaled. Under the race detector
// PostgreSQL carries the full size, where the directory scale matters, and SQLite
// runs a hundredth: the two former tests' separate fixtures took 2341.9 and
// 1072.4 of the 4102 seconds those tests took under the race detector in one
// measured run. Without the race
// detector SQLite carries the full size and PostgreSQL runs a hundredth. The size
// follows the engine and the build, never the environment, and no case is
// skipped (TestCensusSizeIsAnEngineSwitchNeverASkip).

package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// censusScaleFor returns the divisor of the tenant limits a tenant-size census
// case runs at on engine: 1 for the full limits, 100 for a hundredth.
func censusScaleFor(engine store.Engine) int {
	full := store.EngineSQLite
	if raceDetectorEnabled {
		full = store.EnginePostgres
	}
	if engine == full {
		return 1
	}
	return 100
}

// censusLimitsAt returns the tenant limits divided by scale. It fails when a
// limit no longer divides exactly, so a scaled case keeps the tenant budget of
// 100 pages.
func censusLimitsAt(t *testing.T, scale int) CensusLimits {
	t.Helper()
	lim := TenantCensusLimits()
	if lim.PageRows%scale != 0 || lim.MaxDirectoryRows%scale != 0 || lim.MaxAccounts%scale != 0 {
		t.Fatalf("the tenant limits (%d-row pages, %d rows, %d accounts) do not divide by %d",
			lim.PageRows, lim.MaxDirectoryRows, lim.MaxAccounts, scale)
	}
	lim.PageRows, lim.MaxDirectoryRows, lim.MaxAccounts = lim.PageRows/scale, lim.MaxDirectoryRows/scale, lim.MaxAccounts/scale
	return lim
}

// forEachCensusEngine runs body on SQLite and on PostgreSQL, as
// forEachConsentEngine does, except that the engine carrying the full tenant
// limits in this build never skips: without a configured PostgreSQL server it
// fails, so the build's one full-size run cannot vanish from a leg.
func forEachCensusEngine(t *testing.T, body func(t *testing.T, st store.Store)) {
	t.Helper()
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			if engine == store.EnginePostgres && censusScaleFor(engine) == 1 && !pgtest.Available(t) {
				t.Fatalf("this build runs the tenant-size census at the full limits on PostgreSQL, and %s names no reachable server",
					pgtest.EnvSuperuserDSN)
			}
			body(t, openConsentStore(t, engine))
		})
	}
}

// tenantSizeCensus is the one fixture both tenant-size cases read. The accounts
// are shared: the boundary tenant holds the first ones as 48,000 memberships and
// the next as 2,000 exclusions, and the refusal tenant the first ones as 50,000
// memberships and the next as 2,000 exclusions, at the full limits. The cases
// only read it, and each proves it: the fixture's rows and directory facts are
// what the fixture declares before and after its run.
type tenantSizeCensus struct {
	accounts          []model.ID
	boundary, refusal censusTenant
}

// censusTenant is one tenant of the fixture and the rows it declares.
type censusTenant struct {
	id                model.TenantID
	members, excluded int
}

// tenantSizeCensusSlug spells the shared accounts' emails and external ids.
const tenantSizeCensusSlug = "census-tenant-size"

// buildTenantSizeCensus provisions the fixture with the rows the case declares
// for each tenant, in batches that each create a run of accounts and every row
// of either tenant that names them. The sizes come from the case, which derives
// them from censusScaleFor, so the builder never chooses a size itself.
func buildTenantSizeCensus(t *testing.T, ctx context.Context, st store.Store, boundary, refusal censusTenant) tenantSizeCensus {
	t.Helper()
	boundary.id = consentTenant(t, st, "census-boundary")
	refusal.id = consentTenant(t, st, "census-refusal")
	f := tenantSizeCensus{boundary: boundary, refusal: refusal}
	total := max(f.boundary.members+f.boundary.excluded, f.refusal.members+f.refusal.excluded)
	const batch = 500
	f.accounts = make([]model.ID, 0, total)
	for start := 0; start < total; start += batch {
		end := min(start+batch, total)
		var created []model.ID
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			created = created[:0]
			for i := start; i < end; i++ {
				u, err := as.Users().Create(ctx, model.User{
					Email:      censusEmail(tenantSizeCensusSlug, i),
					ExternalID: censusExternal(tenantSizeCensusSlug, i),
					Status:     model.StatusActive,
				})
				if err != nil {
					return err
				}
				created = append(created, u.ID)
			}
			for k, id := range created {
				for _, tenant := range []censusTenant{f.boundary, f.refusal} {
					if err := tenant.place(ctx, as, start+k, id); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("create census accounts %d to %d: %v", start, end, err)
		}
		f.accounts = append(f.accounts, created...)
	}
	return f
}

// place writes the row the i-th account holds in the tenant: a membership, an
// exclusion, or none past the tenant's rows.
func (c censusTenant) place(ctx context.Context, as store.AuthScope, i int, id model.ID) error {
	var err error
	switch {
	case i < c.members:
		_, err = as.Memberships().Create(ctx, model.Membership{UserID: id, TargetTenantID: c.id, Role: RoleViewer})
	case i < c.members+c.excluded:
		_, err = as.TenantExclusions().Create(ctx, model.TenantExclusion{
			UserID: id, TargetTenantID: c.id, Kind: model.ExclusionOffboard, CreatedBy: "test",
			RetirementGeneration: 1, RetirementState: model.RetirementRetiring,
		})
	}
	return err
}

// wantUnchanged fails unless every tenant of the fixture still holds exactly the
// memberships and exclusions it declares, at the directory fact read once the
// fixture was built. when names the moment for the failure.
func (f tenantSizeCensus) wantUnchanged(t *testing.T, ctx context.Context, st store.Store, facts map[model.TenantID]store.AuthorizationFactRef, when string) {
	t.Helper()
	for _, c := range []censusTenant{f.boundary, f.refusal} {
		var members, excluded int
		if err := st.AuthView(ctx, func(as store.AuthScope) error {
			m, err := drainList(ctx, as.Memberships().List, byEq("target_tenant_id", c.id.String(), 0))
			if err != nil {
				return err
			}
			x, err := drainList(ctx, as.TenantExclusions().List, byEq("target_tenant_id", c.id.String(), 0))
			members, excluded = len(m), len(x)
			return err
		}); err != nil {
			t.Fatalf("count the rows of census tenant %s %s the case: %v", c.id, when, err)
		}
		if members != c.members || excluded != c.excluded {
			t.Errorf("census tenant %s holds %d memberships and %d exclusions %s the case, want %d and %d",
				c.id, members, excluded, when, c.members, c.excluded)
		}
		if d, want := directoryFact(t, ctx, st, c.id), facts[c.id]; d.ID != want.ID || d.Version != want.Version {
			t.Errorf("census tenant %s is at D_T %s@%d %s the case, want %s@%d",
				c.id, d.ID, d.Version, when, want.ID, want.Version)
		}
	}
}

// directoryFacts reads the directory fact of every tenant of the fixture.
func (f tenantSizeCensus) directoryFacts(t *testing.T, ctx context.Context, st store.Store) map[model.TenantID]store.AuthorizationFactRef {
	t.Helper()
	return map[model.TenantID]store.AuthorizationFactRef{
		f.boundary.id: directoryFact(t, ctx, st, f.boundary.id),
		f.refusal.id:  directoryFact(t, ctx, st, f.refusal.id),
	}
}

// TestCensusAtTheTenantLimits runs both tenant-size cases on one fixture per
// engine. Neither case writes the fixture, and each shows it: the rows and
// directory facts of both tenants are unchanged over its run.
func TestCensusAtTheTenantLimits(t *testing.T) {
	forEachCensusEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		scale := censusScaleFor(st.Engine())
		lim := censusLimitsAt(t, scale)
		begin := time.Now()
		f := buildTenantSizeCensus(t, ctx, st,
			censusTenant{members: 48_000 / scale, excluded: 2_000 / scale},
			censusTenant{members: 50_000 / scale, excluded: 2_000 / scale})
		t.Logf("scale=1/%d fixture_seconds=%.1f", scale, time.Since(begin).Seconds())
		facts := f.directoryFacts(t, ctx, st)

		t.Run("FortyEightThousandPlusTwoThousandIsComplete", func(t *testing.T) {
			f.wantUnchanged(t, ctx, st, facts, "before")
			defer f.wantUnchanged(t, ctx, st, facts, "after")
			b := f.boundary
			memberAt, excludedAt := 12_345/scale, b.members+1_234/scale
			member, excludedAccount := f.accounts[memberAt], f.accounts[excludedAt]
			doc := Document{Raw: fmt.Sprintf(`{"a":%q,"b":%q}`,
				strings.ToUpper(censusEmail(tenantSizeCensusSlug, memberAt)), censusExternal(tenantSizeCensusSlug, excludedAt))}
			begin := time.Now()
			corr, err := NewAuthenticator(st, nil).CorrelateContent(ctx, b.id, doc, lim)
			t.Logf("census_seconds=%.1f", time.Since(begin).Seconds())
			if err != nil {
				t.Fatalf("a census of %d memberships and %d exclusions (%d rows) at 1/%d of the tenant limits: %v, want it complete",
					b.members, b.excluded, b.members+b.excluded, scale, err)
			}
			want := []model.ID{member, excludedAccount}
			if excludedAccount < member {
				want = []model.ID{excludedAccount, member}
			}
			if !reflect.DeepEqual(corr.Accounts, want) {
				t.Errorf("Accounts = %v, want the member and the excluded account %v", corr.Accounts, want)
			}
			if d := facts[b.id]; corr.Directory.ID != d.ID || corr.Directory.Version != d.Version {
				t.Errorf("Directory = %s@%d, want D_T %s@%d", corr.Directory.ID, corr.Directory.Version, d.ID, d.Version)
			}
		})

		t.Run("FiftyThousandPlusTwoThousandIsRefused", func(t *testing.T) {
			f.wantUnchanged(t, ctx, st, facts, "before")
			defer f.wantUnchanged(t, ctx, st, facts, "after")
			r := f.refusal
			begin := time.Now()
			corr, err := NewAuthenticator(st, nil).CorrelateContent(ctx, r.id, Document{Raw: censusEmail(tenantSizeCensusSlug, 0)}, lim)
			t.Logf("census_seconds=%.1f", time.Since(begin).Seconds())
			var incomplete CensusIncompleteError
			if !errors.As(err, &incomplete) || incomplete.Dimension != "directory_rows" || !errors.Is(err, ErrAliasCensusIncomplete) {
				t.Fatalf("a census of %d memberships and %d exclusions (%d rows) at 1/%d of the tenant limits: %v, want alias_census_incomplete:directory_rows",
					r.members, r.excluded, r.members+r.excluded, scale, err)
			}
			if len(corr.Accounts) != 0 || corr.Directory.Version != 0 {
				t.Errorf("a refused census returned %+v, want no correlation", corr)
			}
		})
	})
}
