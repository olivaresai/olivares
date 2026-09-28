// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// sizedPages answers page i with sizes[i] rows and a fresh cursor, and announces
// more rows until the last page. A call past the last page fails the test.
func sizedPages(t *testing.T, sizes []int, calls *int) func(context.Context, model.Query) ([]int, model.Page, error) {
	t.Helper()
	return func(_ context.Context, _ model.Query) ([]int, model.Page, error) {
		i := *calls
		*calls++
		if i >= len(sizes) {
			t.Errorf("list called %d times for %d pages", i+1, len(sizes))
			return nil, model.Page{}, errors.New("pages exhausted")
		}
		page := model.Page{HasMore: i < len(sizes)-1}
		if page.HasMore {
			page.Cursor = fmt.Sprintf("page-%d", i+2)
		}
		return make([]int, sizes[i]), page, nil
	}
}

// repeat returns n copies of v.
func repeat(v, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestDrainListKeepsItsGuardOfOneHundredPages(t *testing.T) {
	t.Parallel()

	t.Run("one hundred full pages complete", func(t *testing.T) {
		calls := 0
		got, err := drainList(context.Background(), sizedPages(t, repeat(1000, 100), &calls), model.Query{})
		if err != nil {
			t.Fatalf("drainList: %v", err)
		}
		if len(got) != 100_000 || calls != 100 {
			t.Fatalf("rows = %d, calls = %d; want 100000 rows in 100 calls", len(got), calls)
		}
	})

	t.Run("the row budget refuses before the page that crosses it", func(t *testing.T) {
		// A store that answers more rows than asked: 60 pages of 2,000 rows.
		calls := 0
		got, err := drainList(context.Background(), sizedPages(t, repeat(2000, 60), &calls), model.Query{})
		if !errors.Is(err, store.ErrPageCapacity) {
			t.Fatalf("error = %v, want %v: the wrapper keeps a budget of 100,000 rows", err, store.ErrPageCapacity)
		}
		if !errors.Is(err, errDrainListIncomplete) {
			t.Fatalf("error = %v, want it to wrap %v", err, errDrainListIncomplete)
		}
		if got != nil {
			t.Fatalf("rows = %d, want no partial result", len(got))
		}
		if calls != 51 {
			t.Fatalf("list calls = %d, want 51: page 51 would cross 100,000 rows", calls)
		}
	})

	t.Run("the hundred-and-first call is refused", func(t *testing.T) {
		calls := 0
		got, err := drainList(context.Background(), sizedPages(t, repeat(1, 150), &calls), model.Query{})
		if !errors.Is(err, store.ErrPageCapacity) || !errors.Is(err, errDrainListIncomplete) {
			t.Fatalf("error = %v, want %v wrapping %v", err, errDrainListIncomplete, store.ErrPageCapacity)
		}
		if got != nil || calls != 100 {
			t.Fatalf("rows = %d, calls = %d; want no partial result after 100 calls", len(got), calls)
		}
	})
}

// countingStanding is a standing reader under which every id is an account,
// none of them being removed. It counts its reads.
type countingStanding struct{ reads int }

// Standing implements StandingReader.
func (c *countingStanding) Standing(_ context.Context, _ model.TenantID, users []model.ID) (map[model.ID]Standing, error) {
	c.reads++
	out := make(map[model.ID]Standing, len(users))
	for _, u := range users {
		out[u] = Standing{User: u, Exists: true, AuthorityVersion: 1}
	}
	return out, nil
}

func TestTooManyMatchedAccountsRefusedBeforeStanding(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		accounts, reads, mutates int
		want                     error
	}{
		{MaxFencedSubjects + 1, 0, 0, ErrTooManySubjects},
		{MaxFencedSubjects, 1, 1, nil},
	} {
		accounts := make([]model.ID, c.accounts)
		for i := range accounts {
			accounts[i] = model.NewID()
		}
		standing := &countingStanding{}
		mutates := 0
		err := CorrelatedWrite(context.Background(), standing, CorrelatedWriteSpec{
			Tenant:    model.NewTenantID(),
			Correlate: func(context.Context) (Correlation, error) { return Correlation{Accounts: accounts}, nil },
			Mutate: func(context.Context, func(store.Scope) error) error {
				mutates++
				return nil
			},
			Write: func(context.Context, store.Scope, Pinned) error { return nil },
		})
		if !errors.Is(err, c.want) || standing.reads != c.reads || mutates != c.mutates {
			t.Errorf("%d matched accounts: error %v, %d standing reads, %d mutates; want %v, %d, %d",
				c.accounts, err, standing.reads, mutates, c.want, c.reads, c.mutates)
		}
	}
}

// directoryFact reads the tenant's directory fact D_T.
func directoryFact(t *testing.T, ctx context.Context, st store.Store, tenant model.TenantID) store.AuthorizationFactRef {
	t.Helper()
	var fact store.AuthorizationFactRef
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok {
			t.Fatal("the auth scope exposes no directory fact reader")
		}
		var err error
		fact, err = evidence.ReadDirectoryEpochFact(ctx, tenant)
		return err
	}); err != nil {
		t.Fatalf("read the directory fact: %v", err)
	}
	return fact
}

func TestCorrelatedWriteRetriesOnceWithAFreshCorrelation(t *testing.T) {
	t.Run("a refused correlation is not retried", func(t *testing.T) {
		correlations := 0
		err := CorrelatedWrite(context.Background(), &countingStanding{}, CorrelatedWriteSpec{
			Tenant: model.NewTenantID(),
			Correlate: func(context.Context) (Correlation, error) {
				correlations++
				return Correlation{}, CensusIncompleteError{Dimension: "directory_rows"}
			},
			Mutate: func(context.Context, func(store.Scope) error) error {
				t.Error("a refused correlation reached Mutate")
				return nil
			},
			Write: func(context.Context, store.Scope, Pinned) error { return nil },
		})
		if !errors.Is(err, ErrAliasCensusIncomplete) || correlations != 1 {
			t.Fatalf("error %v after %d correlations; want the refusal after 1", err, correlations)
		}
	})

	t.Run("core", func(t *testing.T) {
		forEachConsentEngine(t, func(t *testing.T, st store.Store) {
			ctx := context.Background()
			a := NewAuthenticator(st, nil)
			tenant := consentTenant(t, st, "correlated")
			named := consentMember(t, ctx, st, "named@correlated.example", tenant)
			doc := Document{Raw: `{"owner":"Named@Correlated.example"}`}
			movers := 0

			// run correlates doc before each attempt, and moves D_T with a new
			// membership in the tenant after each of the first moves correlations.
			run := func(doc Document, moves int) (correlations int, written []Pinned, err error) {
				err = CorrelatedWrite(ctx, a, CorrelatedWriteSpec{
					Tenant: tenant,
					Correlate: func(ctx context.Context) (Correlation, error) {
						correlations++
						corr, err := a.CorrelateContent(ctx, tenant, doc, TenantCensusLimits())
						if correlations <= moves {
							movers++
							consentMember(t, ctx, st, fmt.Sprintf("mover-%d@correlated.example", movers), tenant)
						}
						return corr, err
					},
					Mutate: func(ctx context.Context, fn func(store.Scope) error) error { return st.Mutate(ctx, tenant, fn) },
					Write: func(_ context.Context, _ store.Scope, pinned Pinned) error {
						written = append(written, pinned)
						return nil
					},
				})
				return correlations, written, err
			}

			correlations, written, err := run(doc, 1)
			if err != nil || correlations != 2 || len(written) != 1 {
				t.Fatalf("one move: error %v, %d correlations, %d writes; want nil, 2, 1", err, correlations, len(written))
			}
			current := directoryFact(t, ctx, st, tenant)
			if got := written[0].Directory; got.ID != current.ID || got.Version != current.Version {
				t.Errorf("the write pinned D_T %s@%d, want the fresh %s@%d", got.ID, got.Version, current.ID, current.Version)
			}
			if refs := written[0].Refs; len(refs) != 1 || refs[0].UserID != named {
				t.Errorf("the write pinned %+v, want the named account alone", refs)
			}

			correlations, written, err = run(doc, 2)
			if !errors.Is(err, store.ErrConflict) || correlations != 2 || len(written) != 0 {
				t.Fatalf("a move before each attempt: error %v, %d correlations, %d writes; want %v, 2, 0",
					err, correlations, len(written), store.ErrConflict)
			}

			// A document that names nobody still pins D_T, so a move between its
			// correlation and its barrier makes the write correlate again.
			correlations, written, err = run(Document{Raw: `{"owner":"nobody@correlated.example"}`}, 1)
			if err != nil || correlations != 2 || len(written) != 1 {
				t.Fatalf("zero accounts, one move: error %v, %d correlations, %d writes; want nil, 2, 1", err, correlations, len(written))
			}
			current = directoryFact(t, ctx, st, tenant)
			if got := written[0]; len(got.Refs) != 0 || got.Directory.ID != current.ID || got.Directory.Version != current.Version {
				t.Errorf("zero accounts: the write pinned %+v at D_T %s@%d, want no account at the fresh %s@%d",
					got.Refs, got.Directory.ID, got.Directory.Version, current.ID, current.Version)
			}

			// An account that gains an alias the document names, while it is being
			// removed from the tenant, joins the fresh correlation, and its standing
			// refuses the write.
			gained := Document{Raw: `{"owner":"gained@correlated.example"}`}
			attempts, writes := 0, 0
			err = CorrelatedWrite(ctx, a, CorrelatedWriteSpec{
				Tenant: tenant,
				Correlate: func(ctx context.Context) (Correlation, error) {
					attempts++
					corr, err := a.CorrelateContent(ctx, tenant, gained, TenantCensusLimits())
					if attempts == 1 {
						v := consentMember(t, ctx, st, "gained@correlated.example")
						offboardIn(t, ctx, st, v, tenant, model.RetirementRetiring)
					}
					return corr, err
				},
				Mutate: func(ctx context.Context, fn func(store.Scope) error) error { return st.Mutate(ctx, tenant, fn) },
				Write: func(context.Context, store.Scope, Pinned) error {
					writes++
					return nil
				},
			})
			if !errors.Is(err, ErrSubjectRetirementActive) || attempts != 2 || writes != 0 {
				t.Fatalf("an alias gained between the attempts: error %v, %d correlations, %d writes; want %v, 2, 0",
					err, attempts, writes, ErrSubjectRetirementActive)
			}
		})
	})
}

// censusEmail and censusExternal are the aliases of census account i.
func censusEmail(slug string, i int) string { return fmt.Sprintf("%s-%06d@census.example", slug, i) }

func censusExternal(slug string, i int) string { return fmt.Sprintf("%s-ext-%06d", slug, i) }

// censusFixture provisions a tenant with members accounts that are its members
// and then excluded accounts that are not, each with an offboard exclusion in
// retiring. It returns the tenant and the accounts in that order.
func censusFixture(t *testing.T, ctx context.Context, st store.Store, slug string, members, excluded int) (model.TenantID, []model.ID) {
	t.Helper()
	tenant := consentTenant(t, st, slug)
	const batch = 500
	ids := make([]model.ID, 0, members+excluded)
	for start := 0; start < members+excluded; start += batch {
		end := min(start+batch, members+excluded)
		var created []model.ID
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			created = created[:0]
			for i := start; i < end; i++ {
				u, err := as.Users().Create(ctx, model.User{
					Email: censusEmail(slug, i), ExternalID: censusExternal(slug, i), Status: model.StatusActive,
				})
				if err != nil {
					return err
				}
				created = append(created, u.ID)
			}
			for k, id := range created {
				var err error
				if start+k < members {
					_, err = as.Memberships().Create(ctx, model.Membership{UserID: id, TargetTenantID: tenant, Role: RoleViewer})
				} else {
					_, err = as.TenantExclusions().Create(ctx, model.TenantExclusion{
						UserID: id, TargetTenantID: tenant, Kind: model.ExclusionOffboard, CreatedBy: "test",
						RetirementGeneration: 1, RetirementState: model.RetirementRetiring,
					})
				}
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("create census accounts %d to %d: %v", start, end, err)
		}
		ids = append(ids, created...)
	}
	return tenant, ids
}

// TestCensusRowBudgetAtAHundredthOfTheTenantLimits runs the directory census at
// a hundredth of the tenant limits: pages of 5 rows, 500 directory rows and 500
// accounts, so the budget is still 100 pages. It admits 480 + 20 rows and
// refuses 500 + 20, as the tenant-size census does 48,000 + 2,000 and
// 50,000 + 2,000, and it refuses the one row over the budget on either walk.
func TestCensusRowBudgetAtAHundredthOfTheTenantLimits(t *testing.T) {
	tenantLim := TenantCensusLimits()
	lim := tenantLim
	lim.PageRows, lim.MaxDirectoryRows, lim.MaxAccounts = 5, 500, 500
	if tenantLim.PageRows != 100*lim.PageRows || tenantLim.MaxDirectoryRows != 100*lim.MaxDirectoryRows ||
		tenantLim.MaxAccounts != 100*lim.MaxAccounts {
		t.Fatalf("the tenant limits (%d-row pages, %d rows, %d accounts) are no longer a hundred times (%d, %d, %d)",
			tenantLim.PageRows, tenantLim.MaxDirectoryRows, tenantLim.MaxAccounts, lim.PageRows, lim.MaxDirectoryRows, lim.MaxAccounts)
	}
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		for _, c := range []struct {
			name, slug        string
			members, excluded int
			want              string
		}{
			{"480 memberships and 20 exclusions, the budget exactly", "census-hundredth-boundary", 480, 20, ""},
			{"500 memberships and 20 exclusions", "census-hundredth-refusal", 500, 20, "directory_rows"},
			{"480 memberships and 21 exclusions, one row over", "census-hundredth-exclusion-over", 480, 21, "directory_rows"},
			{"501 memberships, one row over", "census-hundredth-membership-over", 501, 0, "directory_rows"},
		} {
			t.Run(c.name, func(t *testing.T) {
				tenant, ids := censusFixture(t, ctx, st, c.slug, c.members, c.excluded)
				doc := Document{Raw: fmt.Sprintf(`{"a":%q,"b":%q}`,
					strings.ToUpper(censusEmail(c.slug, 123)), censusExternal(c.slug, c.members+c.excluded-1))}
				corr, err := NewAuthenticator(st, nil).CorrelateContent(ctx, tenant, doc, lim)
				if c.want != "" {
					wantCensusIncomplete(t, err, c.want)
					if len(corr.Accounts) != 0 || corr.Directory.Version != 0 {
						t.Errorf("a refused census returned %+v, want no correlation", corr)
					}
					return
				}
				if err != nil {
					t.Fatalf("a census of %d rows under a budget of %d: %v, want it complete", c.members+c.excluded, lim.MaxDirectoryRows, err)
				}
				member, excluded := ids[123], ids[c.members+c.excluded-1]
				want := []model.ID{member, excluded}
				if excluded < member {
					want = []model.ID{excluded, member}
				}
				if !reflect.DeepEqual(corr.Accounts, want) {
					t.Errorf("Accounts = %v, want the member and the excluded account %v", corr.Accounts, want)
				}
				if d := directoryFact(t, ctx, st, tenant); corr.Directory.ID != d.ID || corr.Directory.Version != d.Version {
					t.Errorf("Directory = %s@%d, want D_T %s@%d", corr.Directory.ID, corr.Directory.Version, d.ID, d.Version)
				}
			})
		}
	})
}

// offboardIn writes an offboard exclusion of user from tenant in state.
func offboardIn(t *testing.T, ctx context.Context, st store.Store, user model.ID, tenant model.TenantID, state model.RetirementState) {
	t.Helper()
	epoch := int64(1)
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.TenantExclusions().Create(ctx, model.TenantExclusion{
			UserID: user, TargetTenantID: tenant, Kind: model.ExclusionOffboard, CreatedBy: "test",
			RetirementGeneration: 1, RetirementState: state, RetiredEpoch: &epoch,
		})
		return err
	}); err != nil {
		t.Fatalf("write an offboard exclusion in state %q: %v", state, err)
	}
}

// The interface-fault controls below run on the real store and inject, through
// a decorator over its auth views, rows the store itself never holds: a
// directory row or a credential without an account. They are labeled as such
// and replace no backend authority.

// faultStore is the real store with a decorator over the auth views it opens.
type faultStore struct {
	store.Store
	wrap func(store.AuthScope) store.AuthScope
}

// AuthView implements store.Store over the decorated scope.
func (s faultStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthView(ctx, func(as store.AuthScope) error { return fn(s.wrap(as)) })
}

// faultScope is the real auth scope, except for the repositories and the
// directory fact whose hooks are set.
type faultScope struct {
	store.AuthScope
	evidence    store.AuthPrincipalEvidenceScope
	directory   func(ctx context.Context, tenant model.TenantID) (store.AuthorizationFactRef, error)
	users       func(store.MutableRepository[model.User]) store.MutableRepository[model.User]
	memberships func(store.Repository[model.Membership]) store.Repository[model.Membership]
	exclusions  func(store.Repository[model.TenantExclusion]) store.Repository[model.TenantExclusion]
	sessions    func(store.Repository[model.AuthSession]) store.Repository[model.AuthSession]
	tokens      func(store.Repository[model.APIToken]) store.Repository[model.APIToken]
}

// ReadDirectoryEpochFact implements store.AuthPrincipalEvidenceScope.
func (f faultScope) ReadDirectoryEpochFact(ctx context.Context, tenant model.TenantID) (store.AuthorizationFactRef, error) {
	if f.directory != nil {
		return f.directory(ctx, tenant)
	}
	return f.evidence.ReadDirectoryEpochFact(ctx, tenant)
}

// TransactionNow implements store.TransactionClock.
func (f faultScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return f.evidence.TransactionNow(ctx)
}

// Users implements store.AuthScope.
func (f faultScope) Users() store.MutableRepository[model.User] {
	if f.users != nil {
		return f.users(f.AuthScope.Users())
	}
	return f.AuthScope.Users()
}

// Memberships implements store.AuthScope.
func (f faultScope) Memberships() store.Repository[model.Membership] {
	if f.memberships != nil {
		return f.memberships(f.AuthScope.Memberships())
	}
	return f.AuthScope.Memberships()
}

// TenantExclusions implements store.AuthScope.
func (f faultScope) TenantExclusions() store.Repository[model.TenantExclusion] {
	if f.exclusions != nil {
		return f.exclusions(f.AuthScope.TenantExclusions())
	}
	return f.AuthScope.TenantExclusions()
}

// Sessions implements store.AuthScope.
func (f faultScope) Sessions() store.Repository[model.AuthSession] {
	if f.sessions != nil {
		return f.sessions(f.AuthScope.Sessions())
	}
	return f.AuthScope.Sessions()
}

// Tokens implements store.AuthScope.
func (f faultScope) Tokens() store.Repository[model.APIToken] {
	if f.tokens != nil {
		return f.tokens(f.AuthScope.Tokens())
	}
	return f.AuthScope.Tokens()
}

// withFaults returns st with every auth view decorated by configure.
func withFaults(t *testing.T, st store.Store, configure func(f *faultScope)) store.Store {
	return faultStore{Store: st, wrap: func(as store.AuthScope) store.AuthScope {
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok {
			t.Fatal("the auth scope exposes no directory fact reader")
		}
		f := faultScope{AuthScope: as, evidence: evidence}
		configure(&f)
		return f
	}}
}

// faultRepo answers List and Get through its hooks when they are set, and
// otherwise through the real repository.
type faultRepo[T any] struct {
	store.Repository[T]
	list func(ctx context.Context, q model.Query) ([]T, model.Page, error)
	get  func(ctx context.Context, id model.ID) (T, error)
}

// List implements store.Repository.
func (r faultRepo[T]) List(ctx context.Context, q model.Query) ([]T, model.Page, error) {
	if r.list != nil {
		return r.list(ctx, q)
	}
	return r.Repository.List(ctx, q)
}

// Get implements store.Repository.
func (r faultRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	if r.get != nil {
		return r.get(ctx, id)
	}
	return r.Repository.Get(ctx, id)
}

// injectRow returns a decorator that adds row to the first page of every
// listing, as a store holding that row would answer.
func injectRow[T any](row T) func(store.Repository[T]) store.Repository[T] {
	return func(real store.Repository[T]) store.Repository[T] {
		return faultRepo[T]{Repository: real, list: func(ctx context.Context, q model.Query) ([]T, model.Page, error) {
			rows, page, err := real.List(ctx, q)
			if err == nil && q.Cursor == "" {
				rows = append(rows, row)
			}
			return rows, page, err
		}}
	}
}

// injectGet returns a decorator that answers Get for id with row.
func injectGet[T any](id model.ID, row T) func(store.Repository[T]) store.Repository[T] {
	return func(real store.Repository[T]) store.Repository[T] {
		return faultRepo[T]{Repository: real, get: func(ctx context.Context, got model.ID) (T, error) {
			if got == id {
				return row, nil
			}
			return real.Get(ctx, got)
		}}
	}
}

// wantCensusIncomplete fails t unless err is the non-retryable census refusal
// with dimension.
func wantCensusIncomplete(t *testing.T, err error, dimension string) {
	t.Helper()
	var incomplete CensusIncompleteError
	if !errors.As(err, &incomplete) || incomplete.Dimension != dimension ||
		!errors.Is(err, ErrAliasCensusIncomplete) || errors.Is(err, ErrAliasCensusUnavailable) {
		t.Fatalf("error = %v, want alias_census_incomplete:%s (not retryable)", err, dimension)
	}
}

func TestCorrelatedWriteRefusesAnAccountThatDoesNotExist(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		a := NewAuthenticator(st, nil)
		tenant := consentTenant(t, st, "existence")
		member := consentMember(t, ctx, st, "member@existence.example", tenant)
		retiring := consentMember(t, ctx, st, "retiring@existence.example")
		offboardIn(t, ctx, st, retiring, tenant, model.RetirementRetiring)
		absent := model.NewID()

		write := func(restrict bool, accounts ...model.ID) (int, Pinned, error) {
			mutates := 0
			var pinned Pinned
			err := CorrelatedWrite(ctx, a, CorrelatedWriteSpec{
				Tenant: tenant,
				Correlate: func(ctx context.Context) (Correlation, error) {
					return Correlation{Directory: directoryFact(t, ctx, st, tenant), Accounts: accounts}, nil
				},
				Restrict: restrict,
				Mutate: func(ctx context.Context, fn func(store.Scope) error) error {
					mutates++
					return st.Mutate(ctx, tenant, fn)
				},
				Write: func(_ context.Context, _ store.Scope, p Pinned) error {
					pinned = p
					return nil
				},
			})
			return mutates, pinned, err
		}

		for _, restrict := range []bool{false, true} {
			mutates, _, err := write(restrict, member, absent)
			if mutates != 0 {
				t.Errorf("restrict=%t: an account that does not exist reached %d mutates, want none", restrict, mutates)
			}
			wantCensusIncomplete(t, err, "missing_account")
		}

		mutates, _, err := write(false, member, retiring)
		if !errors.Is(err, ErrSubjectRetirementActive) || mutates != 0 {
			t.Errorf("an account being removed: error %v after %d mutates; want %v before any mutate", err, mutates, ErrSubjectRetirementActive)
		}

		mutates, pinned, err := write(true, member, retiring)
		if err != nil || mutates != 1 || len(pinned.Refs) != 2 {
			t.Errorf("a restriction naming an account being removed: error %v, %d mutates, refs %+v; want nil, 1, both accounts pinned", err, mutates, pinned.Refs)
		}
	})
}

// failingStanding answers every standing read with err, or omits the accounts
// in omit; it counts its reads.
type failingStanding struct {
	err   error
	omit  map[model.ID]bool
	reads int
}

// Standing implements StandingReader.
func (f *failingStanding) Standing(_ context.Context, _ model.TenantID, users []model.ID) (map[model.ID]Standing, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[model.ID]Standing, len(users))
	for _, u := range users {
		if !f.omit[u] {
			out[u] = Standing{User: u, Exists: true, AuthorityVersion: 1}
		}
	}
	return out, nil
}

func TestCorrelatedWriteTellsAMissingAccountFromAFailedRead(t *testing.T) {
	t.Parallel()

	named := model.NewID()
	run := func(r StandingReader, accounts ...model.ID) (int, error) {
		mutates := 0
		err := CorrelatedWrite(context.Background(), r, CorrelatedWriteSpec{
			Tenant:    model.NewTenantID(),
			Correlate: func(context.Context) (Correlation, error) { return Correlation{Accounts: accounts}, nil },
			Mutate: func(context.Context, func(store.Scope) error) error {
				mutates++
				return nil
			},
			Write: func(context.Context, store.Scope, Pinned) error { return nil },
		})
		return mutates, err
	}

	t.Run("a zero account is refused before any standing read", func(t *testing.T) {
		reader := &failingStanding{}
		mutates, err := run(reader, named, model.ID(""))
		if reader.reads != 0 || mutates != 0 {
			t.Errorf("%d standing reads and %d mutates, want none", reader.reads, mutates)
		}
		wantCensusIncomplete(t, err, "missing_account")
	})

	t.Run("a failed standing read answers its own error", func(t *testing.T) {
		failure := errors.New("standing unavailable")
		reader := &failingStanding{err: failure}
		mutates, err := run(reader, named)
		if !errors.Is(err, failure) || errors.Is(err, ErrAliasCensusIncomplete) || reader.reads != 1 || mutates != 0 {
			t.Errorf("error %v after %d reads and %d mutates; want the read's own error after 1 read and no mutate", err, reader.reads, mutates)
		}
	})

	t.Run("an account the read omits is a failed read, not a missing account", func(t *testing.T) {
		reader := &failingStanding{omit: map[model.ID]bool{named: true}}
		mutates, err := run(reader, named)
		if err == nil || errors.Is(err, ErrAliasCensusIncomplete) || reader.reads != 1 || mutates != 0 {
			t.Errorf("error %v after %d reads and %d mutates; want a read failure after 1 read and no mutate", err, reader.reads, mutates)
		}
	})
}

func TestCensusRefusesADirectoryRowWithoutAnAccount(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "census-rows")
		member := consentMember(t, ctx, st, "member@census-rows.example", tenant)
		doc := Document{Raw: "member@census-rows.example"}

		for _, c := range []struct {
			name       string
			membership *model.Membership
			exclusion  *model.TenantExclusion
			want       string
		}{
			{"interface fault: a membership without an account", &model.Membership{TargetTenantID: tenant, Role: RoleViewer}, nil, "missing_account"},
			{"interface fault: a membership whose account row is missing", &model.Membership{UserID: model.NewID(), TargetTenantID: tenant, Role: RoleViewer}, nil, "missing_account"},
			{"interface fault: an admitted exclusion without an account", nil, &model.TenantExclusion{TargetTenantID: tenant, Kind: model.ExclusionOffboard, RetirementState: model.RetirementRetiring}, "missing_account"},
			{"interface fault: a retired exclusion without an account", nil, &model.TenantExclusion{TargetTenantID: tenant, Kind: model.ExclusionOffboard, RetirementState: model.RetirementRetired}, ""},
		} {
			t.Run(c.name, func(t *testing.T) {
				faulty := withFaults(t, st, func(f *faultScope) {
					if c.membership != nil {
						f.memberships = injectRow(*c.membership)
					}
					if c.exclusion != nil {
						f.exclusions = injectRow(*c.exclusion)
					}
				})
				corr, err := NewAuthenticator(faulty, nil).CorrelateContent(ctx, tenant, doc, TenantCensusLimits())
				if c.want != "" {
					wantCensusIncomplete(t, err, c.want)
					return
				}
				if err != nil || !reflect.DeepEqual(corr.Accounts, []model.ID{member}) {
					t.Fatalf("census = %v, %v; want the member alone: a row outside the census is not read", corr.Accounts, err)
				}
			})
		}
	})
}

func TestCanonicalLegRefusesALiveSessionWithoutAnOwner(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "canonical-owner")
		credential := model.NewID()
		live := model.NewTimestamp(time.Now().Add(time.Hour))
		expired := model.NewTimestamp(time.Now().Add(-time.Hour))
		session := func(expires model.Timestamp) model.AuthSession {
			s := model.AuthSession{ExpiresAt: expires}
			s.ID = credential
			return s
		}
		token := model.APIToken{ExpiresAt: &live}
		token.ID = credential

		for _, c := range []struct {
			name      string
			configure func(f *faultScope)
			want      string
		}{
			{"interface fault: a live session without an owner", func(f *faultScope) { f.sessions = injectGet(credential, session(live)) }, "missing_account"},
			{"interface fault: an expired session without an owner", func(f *faultScope) { f.sessions = injectGet(credential, session(expired)) }, ""},
			{"a live token without an owner or act-as account", func(f *faultScope) { f.tokens = injectGet(credential, token) }, ""},
		} {
			t.Run(c.name, func(t *testing.T) {
				faulty := withFaults(t, st, c.configure)
				corr, err := NewAuthenticator(faulty, nil).CorrelateContent(ctx, tenant, Document{Raw: credential.String()}, TenantCensusLimits())
				if c.want != "" {
					wantCensusIncomplete(t, err, c.want)
					return
				}
				if err != nil || len(corr.Accounts) != 0 {
					t.Fatalf("census = %v, %v; want no account and no refusal", corr.Accounts, err)
				}
			})
		}
	})
}

// faultUsers answers List through its hook, and everything else through the
// real users repository.
type faultUsers struct {
	store.MutableRepository[model.User]
	list func(ctx context.Context, q model.Query) ([]model.User, model.Page, error)
}

// List implements store.ReadRepository.
func (u faultUsers) List(ctx context.Context, q model.Query) ([]model.User, model.Page, error) {
	return u.list(ctx, q)
}

func TestCorrelationsReadTheDirectoryFactFirst(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "owner-keys")
		key, err := QualifiedSubjectKey("https://idp.example", "sub-owner")
		if err != nil {
			t.Fatalf("QualifiedSubjectKey: %v", err)
		}
		var bound model.ID
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			u, err := as.Users().Create(ctx, model.User{Email: "bound@owner-keys.example", Status: model.StatusActive, SsoSubject: string(key)})
			bound = u.ID
			return err
		}); err != nil {
			t.Fatalf("create the bound account: %v", err)
		}

		var reads []string
		var limits []int
		recorded := withFaults(t, st, func(f *faultScope) {
			f.directory = func(ctx context.Context, tenant model.TenantID) (store.AuthorizationFactRef, error) {
				reads = append(reads, "directory")
				return f.evidence.ReadDirectoryEpochFact(ctx, tenant)
			}
			f.users = func(real store.MutableRepository[model.User]) store.MutableRepository[model.User] {
				return faultUsers{MutableRepository: real, list: func(ctx context.Context, q model.Query) ([]model.User, model.Page, error) {
					reads = append(reads, "users")
					limits = append(limits, q.Limit)
					return real.List(ctx, q)
				}}
			}
			f.memberships = func(real store.Repository[model.Membership]) store.Repository[model.Membership] {
				return faultRepo[model.Membership]{Repository: real, list: func(ctx context.Context, q model.Query) ([]model.Membership, model.Page, error) {
					reads = append(reads, "memberships")
					return real.List(ctx, q)
				}}
			}
		})
		a := NewAuthenticator(recorded, nil)

		if _, err := a.CorrelateContent(ctx, tenant, Document{Raw: "nobody"}, TenantCensusLimits()); err != nil || len(reads) == 0 || reads[0] != "directory" {
			t.Fatalf("content: reads %v, error %v; want the directory fact first", reads, err)
		}

		reads = nil
		corr, err := a.CorrelateOwnerKeys(ctx, tenant, []QualifiedKey{key})
		if err != nil || !reflect.DeepEqual(corr.Accounts, []model.ID{bound}) {
			t.Fatalf("owner keys: %v, %v; want the bound account", corr.Accounts, err)
		}
		if !reflect.DeepEqual(reads, []string{"directory", "users"}) || !reflect.DeepEqual(limits, []int{2}) {
			t.Fatalf("owner keys: reads %v with limits %v; want the directory fact, then one users listing of at most 2", reads, limits)
		}
		if d := directoryFact(t, ctx, st, tenant); corr.Directory.ID != d.ID || corr.Directory.Version != d.Version {
			t.Errorf("owner keys: Directory = %s@%d, want D_T %s@%d", corr.Directory.ID, corr.Directory.Version, d.ID, d.Version)
		}

		reads = nil
		var unkeyable OwnerUnkeyableError
		if _, err := a.CorrelateOwnerKeys(ctx, tenant, []QualifiedKey{""}); !errors.As(err, &unkeyable) || unkeyable.Reason != "empty" || !reflect.DeepEqual(reads, []string{"directory"}) {
			t.Fatalf("an empty key: error %v after reads %v; want owner_unkeyable:empty and no users listing", err, reads)
		}

		// Interface fault: a second account bound to the same key, which the
		// unique index never holds. The decorator honors the listing's limit.
		twin := model.User{Email: "twin@owner-keys.example", SsoSubject: string(key)}
		twin.ID = model.NewID()
		duplicated := withFaults(t, st, func(f *faultScope) {
			f.users = func(real store.MutableRepository[model.User]) store.MutableRepository[model.User] {
				return faultUsers{MutableRepository: real, list: func(ctx context.Context, q model.Query) ([]model.User, model.Page, error) {
					rows, page, err := real.List(ctx, q)
					rows = append(rows, twin)
					if q.Limit > 0 && len(rows) > q.Limit {
						rows = rows[:q.Limit]
					}
					return rows, page, err
				}}
			}
		})
		if _, err := NewAuthenticator(duplicated, nil).CorrelateOwnerKeys(ctx, tenant, []QualifiedKey{key}); !errors.Is(err, ErrDuplicateBinding) {
			t.Fatalf("interface fault, two accounts on one key: error %v, want %v", err, ErrDuplicateBinding)
		}
	})
}

func TestCredentialOwnersAreNamedOnlyWhileLive(t *testing.T) {
	t.Parallel()

	now := model.NewTimestamp(time.Now())
	later := model.NewTimestamp(time.Now().Add(time.Hour))
	earlier := model.NewTimestamp(time.Now().Add(-time.Hour))
	owner, actor := model.NewID(), model.NewID()

	for _, c := range []struct {
		name    string
		session model.AuthSession
		want    []model.ID
		refused bool
	}{
		{"a live session", model.AuthSession{UserID: owner, ExpiresAt: later}, []model.ID{owner}, false},
		{"an expired session", model.AuthSession{UserID: owner, ExpiresAt: earlier}, nil, false},
		{"a session that expires now", model.AuthSession{UserID: owner, ExpiresAt: now}, nil, false},
		{"a revoked session", model.AuthSession{UserID: owner, ExpiresAt: later, Revoked: true}, nil, false},
		{"a live session without an owner", model.AuthSession{ExpiresAt: later}, nil, true},
		{"an expired session without an owner", model.AuthSession{ExpiresAt: earlier}, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := sessionOwners(now, c.session)
			if c.refused {
				wantCensusIncomplete(t, err, "missing_account")
				return
			}
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("sessionOwners = %v, %v; want %v", got, err, c.want)
			}
		})
	}

	for _, c := range []struct {
		name  string
		token model.APIToken
		want  []model.ID
	}{
		{"a live token that acts for another account", model.APIToken{UserID: owner, ActAsUserID: actor, ExpiresAt: &later}, []model.ID{owner, actor}},
		{"a token without an expiry", model.APIToken{UserID: owner}, []model.ID{owner}},
		{"an expired token", model.APIToken{UserID: owner, ActAsUserID: actor, ExpiresAt: &earlier}, nil},
		{"a token that expires now", model.APIToken{UserID: owner, ExpiresAt: &now}, nil},
		{"a revoked token", model.APIToken{UserID: owner, ActAsUserID: actor, Revoked: true}, nil},
		{"a live token without an owner", model.APIToken{ActAsUserID: actor}, []model.ID{actor}},
		{"a live token without an owner or act-as account", model.APIToken{}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := tokenOwners(now, c.token); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("tokenOwners = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCanonicalLegNamesTheOwnersOfLiveCredentials(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "credentials")
		holders := make([]model.ID, 7)
		for i := range holders {
			holders[i] = consentMember(t, ctx, st, fmt.Sprintf("holder-%d@credentials.example", i))
		}
		now := time.Now()
		later := model.NewTimestamp(now.Add(time.Hour))
		earlier := model.NewTimestamp(now.Add(-time.Hour))
		var credentials []string
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			credentials = credentials[:0]
			for _, s := range []model.AuthSession{
				{UserID: holders[0], ExpiresAt: later},
				{UserID: holders[1], ExpiresAt: earlier},
				{UserID: holders[2], ExpiresAt: later, Revoked: true},
			} {
				cred, err := NewCredential(PrefixSession)
				if err != nil {
					return err
				}
				s.Selector, s.SecretHash, s.AAL, s.AMR = cred.Selector, cred.SecretHash, AAL1, []string{"pwd"}
				created, err := as.Sessions().Create(ctx, s)
				if err != nil {
					return err
				}
				credentials = append(credentials, created.ID.String())
			}
			for _, tok := range []model.APIToken{
				{UserID: holders[3], ActAsUserID: holders[4], ExpiresAt: &later},
				{UserID: holders[5], ExpiresAt: &earlier},
				{UserID: holders[6], Revoked: true},
			} {
				cred, err := NewCredential(PrefixToken)
				if err != nil {
					return err
				}
				tok.Name, tok.Selector, tok.SecretHash, tok.Role = "credential", cred.Selector, cred.SecretHash, RoleViewer
				created, err := as.Tokens().Create(ctx, tok)
				if err != nil {
					return err
				}
				credentials = append(credentials, created.ID.String())
			}
			return nil
		}); err != nil {
			t.Fatalf("write the credentials: %v", err)
		}

		corr, err := NewAuthenticator(st, nil).CorrelateContent(ctx, tenant, Document{Raw: strings.Join(credentials, " ")}, TenantCensusLimits())
		want := []model.ID{holders[0], holders[3], holders[4]}
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		if err != nil || !reflect.DeepEqual(corr.Accounts, want) {
			t.Fatalf("census = %v, %v; want the live session's owner and the live token's owner and act-as account %v", corr.Accounts, err, want)
		}
	})
}

func TestCensusAdmitsOnlyOffboardRowsNotRetiredOrLifted(t *testing.T) {
	t.Parallel()

	offboard := func(state model.RetirementState) model.TenantExclusion {
		return model.TenantExclusion{Kind: model.ExclusionOffboard, RetirementState: state}
	}
	for _, c := range []struct {
		name string
		row  model.TenantExclusion
		want bool
	}{
		{"retiring", offboard(model.RetirementRetiring), true},
		{"blocked", offboard(model.RetirementBlocked), true},
		{"an unknown state", offboard("paused"), true},
		{"no state", offboard(""), true},
		{"retired", offboard(model.RetirementRetired), false},
		{"lifted", offboard(model.RetirementLifted), false},
		{"a session exclusion", model.TenantExclusion{Kind: model.ExclusionSession, RetirementState: model.RetirementRetiring}, false},
	} {
		if got := censusAdmits(c.row); got != c.want {
			t.Errorf("%s: censusAdmits = %t, want %t", c.name, got, c.want)
		}
	}
}

func TestCensusAdmitsOffboardAccountsByState(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "states")
		var emails []string
		var want []model.ID
		for i, state := range []model.RetirementState{
			model.RetirementRetiring, model.RetirementBlocked, model.RetirementRetired, model.RetirementLifted, "paused", "",
		} {
			email := fmt.Sprintf("state-%d@states.example", i)
			v := consentMember(t, ctx, st, email)
			offboardIn(t, ctx, st, v, tenant, state)
			emails = append(emails, email)
			if state != model.RetirementRetired && state != model.RetirementLifted {
				want = append(want, v)
			}
		}
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		corr, err := NewAuthenticator(st, nil).CorrelateContent(ctx, tenant, Document{Raw: strings.Join(emails, " ")}, TenantCensusLimits())
		if err != nil || !reflect.DeepEqual(corr.Accounts, want) {
			t.Fatalf("census = %v, %v; want the retiring, blocked and unknown-state accounts %v", corr.Accounts, err, want)
		}
	})
}

func TestCensusAliasByteLimitsAreInclusive(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "alias-bytes")
		const wideEmail = "wide@alias-bytes.example"
		external := strings.Repeat("x", 64)
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			u, err := as.Users().Create(ctx, model.User{Email: wideEmail, ExternalID: external, Status: model.StatusActive})
			if err != nil {
				return err
			}
			_, err = as.Memberships().Create(ctx, model.Membership{UserID: u.ID, TargetTenantID: tenant, Role: RoleViewer})
			return err
		}); err != nil {
			t.Fatalf("create the wide account: %v", err)
		}
		other := consentMember(t, ctx, st, "o@alias-bytes.example", tenant)
		widest := len(external)
		total := len(wideEmail) + len(external) + len("o@alias-bytes.example")
		doc := Document{Raw: "o@alias-bytes.example"}

		for _, c := range []struct {
			name            string
			perAlias, total int
			want            string
		}{
			{"the widest alias at its limit", widest, total, ""},
			{"the widest alias one byte over", widest - 1, total, "alias_bytes"},
			{"the aliases at their total", 4096, total, ""},
			{"the aliases one byte over their total", 4096, total - 1, "total_alias_bytes"},
		} {
			t.Run(c.name, func(t *testing.T) {
				lim := TenantCensusLimits()
				lim.MaxAliasBytes, lim.MaxTotalAliasBytes = c.perAlias, c.total
				corr, err := NewAuthenticator(st, nil).CorrelateContent(ctx, tenant, doc, lim)
				if c.want != "" {
					wantCensusIncomplete(t, err, c.want)
					return
				}
				if err != nil || !reflect.DeepEqual(corr.Accounts, []model.ID{other}) {
					t.Fatalf("census = %v, %v; want the other account", corr.Accounts, err)
				}
			})
		}
	})
}

func TestCensusUnavailableNamesTheReadThatFailed(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "unavailable")
		consentMember(t, ctx, st, "m@unavailable.example", tenant)
		failure := errors.New("injected read failure")

		for _, c := range []struct {
			name      string
			configure func(f *faultScope)
			want      string
		}{
			{"interface fault: the directory fact", func(f *faultScope) {
				f.directory = func(context.Context, model.TenantID) (store.AuthorizationFactRef, error) {
					return store.AuthorizationFactRef{}, failure
				}
			}, "directory_fact"},
			{"interface fault: a listing that does not continue", func(f *faultScope) {
				f.memberships = func(real store.Repository[model.Membership]) store.Repository[model.Membership] {
					return faultRepo[model.Membership]{Repository: real, list: func(ctx context.Context, q model.Query) ([]model.Membership, model.Page, error) {
						rows, _, err := real.List(ctx, q)
						return rows, model.Page{HasMore: true}, err
					}}
				}
			}, "continuation"},
			{"interface fault: a failed page", func(f *faultScope) {
				f.exclusions = func(real store.Repository[model.TenantExclusion]) store.Repository[model.TenantExclusion] {
					return faultRepo[model.TenantExclusion]{Repository: real, list: func(context.Context, model.Query) ([]model.TenantExclusion, model.Page, error) {
						return nil, model.Page{}, failure
					}}
				}
			}, "page_failed"},
		} {
			t.Run(c.name, func(t *testing.T) {
				_, err := NewAuthenticator(withFaults(t, st, c.configure), nil).CorrelateContent(ctx, tenant, Document{Raw: "m@unavailable.example"}, TenantCensusLimits())
				var unavailable CensusUnavailableError
				if !errors.As(err, &unavailable) || unavailable.Dimension != c.want ||
					!errors.Is(err, ErrAliasCensusUnavailable) || errors.Is(err, ErrAliasCensusIncomplete) {
					t.Fatalf("error = %v, want alias_census_unavailable:%s (retryable)", err, c.want)
				}
				if c.want != "continuation" && !errors.Is(err, failure) {
					t.Errorf("error = %v, want it to keep its cause", err)
				}
			})
		}
	})
}
