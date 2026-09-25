// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// principalViewWorkspace is the workspace a confined membership names. loadGrants
// reads the stored id verbatim, so it needs no row of its own.
const principalViewWorkspace = "ws-standing"

// authViewCounter counts how many times a caller opens an auth view, and serves a
// NESTED opening from the view that is already open.
//
// The count is what these measures read. The re-entry is what makes the wrong shape
// observable at all: the engine's SQLite pool holds a single connection, so a second
// read transaction opened while the first is still running waits for a connection
// only the first can release, and the nested case would hang instead of reporting.
// Serving it from the open scope keeps the reconstruction honest — it reads the same
// rows through the same transaction — while the counter still records that a second
// view was asked for.
//
// The store double the principal-evidence tests already use counts openings too, but
// it forwards every one of them to the real store, so it cannot stand in here. Both
// tests below drive this one from a single goroutine, so the counter needs no lock.
type authViewCounter struct {
	store.Store
	views int
	open  store.AuthScope
}

func (c *authViewCounter) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	c.views++
	if c.open != nil {
		return fn(c.open)
	}
	return c.Store.AuthView(ctx, func(as store.AuthScope) error {
		c.open = as
		defer func() { c.open = nil }()
		return fn(as)
	})
}

// principalViewFixture is a real store holding one standing user whose principal is
// worth comparing — a direct membership, a mapped directory group that elevates it,
// and a workspace-confined membership in a second tenant — next to the two accounts
// that must authorize nothing: one an administrator switched off, one the identity
// provider deprovisioned.
type principalViewFixture struct {
	t        *testing.T
	ctx      context.Context
	raw      store.Store
	st       *authViewCounter
	a        *Authenticator
	tenant   model.TenantID
	scoped   model.TenantID
	standing model.User
	disabled model.User
	departed model.User
	group    model.UserGroup
}

func newPrincipalViewFixture(t *testing.T) *principalViewFixture {
	t.Helper()
	ctx := context.Background()
	raw, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	f := &principalViewFixture{t: t, ctx: ctx, raw: raw}
	f.st = &authViewCounter{Store: raw}
	f.a = NewAuthenticator(f.st, nil)
	f.tenant = f.provision("standing", true)
	f.scoped = f.provision("scoped", false)
	f.seed()
	return f
}

// provision creates one business tenant the way the active writer provisions one at
// promotion: EnsureSystemTenant, then CreateOrg, each in its own System transaction.
func (f *principalViewFixture) provision(slug string, systemFirst bool) model.TenantID {
	f.t.Helper()
	var id model.TenantID
	if err := f.raw.System(f.ctx, func(sys store.SystemScope) error {
		if systemFirst {
			if _, err := sys.EnsureSystemTenant(f.ctx); err != nil {
				return err
			}
		}
		org, err := sys.CreateOrg(f.ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		id = org.TenantID
		return err
	}); err != nil {
		f.t.Fatalf("provision %q: %v", slug, err)
	}
	return id
}

// seed writes the three accounts and the standing user's authority rows. The two
// inactive accounts are created active and then switched off, which is the order the
// real paths use — an administrator disabling an account and a directory
// deprovisioning one both flip an existing row rather than insert an inactive one.
func (f *principalViewFixture) seed() {
	f.t.Helper()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		var err error
		if f.standing, err = as.Users().Create(f.ctx, model.User{
			Email: "standing@example.test", DisplayName: "Standing", Status: model.StatusActive,
		}); err != nil {
			return err
		}
		disabled, err := as.Users().Create(f.ctx, model.User{
			Email: "disabled@example.test", DisplayName: "Disabled", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		disabled.Status = model.StatusInactive
		if f.disabled, err = as.Users().Update(f.ctx, disabled); err != nil {
			return err
		}
		departed, err := as.Users().Create(f.ctx, model.User{
			Email: "departed@example.test", DisplayName: "Departed", Status: model.StatusActive,
			ExternalID: "idp-departed", SsoSubject: "https://idp.example.test\x1fdeparted",
		})
		if err != nil {
			return err
		}
		departed.Status = model.StatusInactive
		if f.departed, err = as.Users().Update(f.ctx, departed); err != nil {
			return err
		}
		if _, err = as.Memberships().Create(f.ctx, model.Membership{
			UserID: f.standing.ID, TargetTenantID: f.tenant, Role: RoleEditor,
		}); err != nil {
			return err
		}
		if _, err = as.Memberships().Create(f.ctx, model.Membership{
			UserID: f.standing.ID, TargetTenantID: f.scoped, Role: RoleViewer,
			WorkspaceID: model.ID(principalViewWorkspace),
		}); err != nil {
			return err
		}
		if f.group, err = as.Groups().Create(f.ctx, model.UserGroup{
			TargetTenantID: f.tenant, DisplayName: "Standing Admins",
		}); err != nil {
			return err
		}
		f.group.MappedRole = RoleAdmin
		if f.group, err = as.Groups().Update(f.ctx, f.group); err != nil {
			return err
		}
		_, err = as.GroupMembers().Create(f.ctx, model.UserGroupMember{
			GroupID: f.group.ID, UserID: f.standing.ID,
		})
		return err
	}); err != nil {
		f.t.Fatalf("seed accounts: %v", err)
	}
}

// A caller that already holds an auth view must be able to rebuild a standing user's
// principal INSIDE it. The store's principal-evidence contract is explicit that a
// caller bracketing a reconstruction between two directory-generation reads "must not
// open a second Store transaction" (core/store/auth_evidence.go), so a reconstruction
// that insists on opening its own view cannot be bracketed at all — and on the SQLite
// engine, whose pool is a single connection, it cannot even be nested.
//
// The assertion is on the number of views the caller's own bracket costs, not on what
// the reconstruction returns: the principal must come out identical either way, which
// the second assertion pins.
func TestStandingPrincipalReconstructionHappensInsideTheEpochBracketedView(t *testing.T) {
	f := newPrincipalViewFixture(t)
	ref := f.standing.ID.String()

	want, found, err := f.a.PrincipalForUser(f.ctx, ref, AAL3)
	if err != nil || !found {
		t.Fatalf("PrincipalForUser = (found=%v, err=%v), want the standing principal", found, err)
	}

	var got Principal
	var gotFound bool
	var before, after store.AuthorizationFactRef
	f.st.views = 0
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok {
			t.Fatal("the auth scope under test lacks the principal-evidence capability")
		}
		var e error
		if before, e = evidence.ReadDirectoryEpochFact(f.ctx, f.tenant); e != nil {
			return e
		}
		if got, gotFound, e = principalForUserInScope(f.ctx, as, ref, AAL3); e != nil {
			return e
		}
		after, e = evidence.ReadDirectoryEpochFact(f.ctx, f.tenant)
		return e
	}); err != nil {
		t.Fatalf("epoch-bracketed reconstruction: %v", err)
	}

	if f.st.views != 1 {
		t.Fatalf("reconstruction opened a second AuthView; want the same view as the before/after epoch reads (views = %d, want 1)", f.st.views)
	}
	if !gotFound {
		t.Fatal("reconstruction inside the caller's view did not find the standing user")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reconstruction inside the caller's view = %+v, want the principal PrincipalForUser builds = %+v", got, want)
	}
	if before.Version < 1 || before != after {
		t.Errorf("directory generation moved across the reconstruction: before %+v, after %+v", before, after)
	}
}

// The two callers that already ask for a standing principal — the AuthZEN decision
// point resolving a subject by id or email, and the federated grant resolving a
// verified email hint at AAL1 — keep the method that opens its own view, and keep
// every answer it gives: exactly one view per call, the same principal, the assurance
// clamped into the user range, and (false, nil) for an account that authorizes
// nothing, which is never an error and never a fabricated principal.
func TestPrincipalForUserKeepsItsOwnViewForAuthZENAndEMA(t *testing.T) {
	f := newPrincipalViewFixture(t)

	cases := []struct {
		name      string
		ref       string
		assurance int
		wantFound bool
		wantAAL   int
	}{
		{"subject by id at the step-up ceiling", f.standing.ID.String(), AAL3, true, AAL3},
		{"subject by id without a step-up", f.standing.ID.String(), AAL1, true, AAL1},
		{"subject by id below the user floor", f.standing.ID.String(), 0, true, AAL1},
		{"subject by id above the engine ceiling", f.standing.ID.String(), 9, true, AAL3},
		{"verified email hint", f.standing.Email, AAL1, true, AAL1},
		{"email hint in the case the caller received it", "STANDING@Example.test", AAL3, true, AAL3},
		{"absent subject", "nobody@example.test", AAL3, false, 0},
		{"absent subject that parses as an id", model.NewID().String(), AAL3, false, 0},
		{"account an administrator switched off", f.disabled.Email, AAL3, false, 0},
		{"account the directory deprovisioned", f.departed.Email, AAL1, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.st.views = 0
			p, found, err := f.a.PrincipalForUser(f.ctx, tc.ref, tc.assurance)
			if err != nil {
				t.Fatalf("PrincipalForUser(%q) returned %v, want no error", tc.ref, err)
			}
			if f.st.views != 1 {
				t.Errorf("PrincipalForUser opened %d auth views, want exactly 1", f.st.views)
			}
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if !found {
				if !reflect.DeepEqual(p, Principal{}) {
					t.Errorf("an account that authorizes nothing resolved %+v, want the zero principal", p)
				}
				return
			}
			if p.AAL != tc.wantAAL {
				t.Errorf("AAL = %d, want %d", p.AAL, tc.wantAAL)
			}
			if p.UserID != f.standing.ID || p.CredID != f.standing.ID {
				t.Errorf("identity = (user %s, credential %s), want the user id %s in both", p.UserID, p.CredID, f.standing.ID)
			}
		})
	}

	// The authority the standing principal carries: the direct membership elevated by
	// the mapped directory group, that group as a subject, and the confinement the
	// workspace-scoped membership in the second tenant imposes.
	p, found, err := f.a.PrincipalForUser(f.ctx, f.standing.ID.String(), AAL3)
	if err != nil || !found {
		t.Fatalf("PrincipalForUser = (found=%v, err=%v)", found, err)
	}
	if p.Kind != KindUser || p.Superadmin {
		t.Errorf("principal = {Kind %s, Superadmin %v}, want {user, false}", p.Kind, p.Superadmin)
	}
	if role, ok := p.RoleIn(f.tenant); !ok || role != RoleAdmin {
		t.Errorf("role = (%q, %v), want the mapped group's admin", role, ok)
	}
	if groups := p.GroupsIn(f.tenant); len(groups) != 1 || groups[0] != f.group.ID.String() {
		t.Errorf("groups = %v, want the one gated group %s", groups, f.group.ID)
	}
	if ws, ok := p.ConfinedWorkspaceIn(f.scoped); !ok || ws != model.ID(principalViewWorkspace) {
		t.Errorf("confinement = (%q, %v), want %q", ws, ok, principalViewWorkspace)
	}

	byEmail, found, err := f.a.PrincipalForUser(f.ctx, f.standing.Email, AAL3)
	if err != nil || !found {
		t.Fatalf("PrincipalForUser by email = (found=%v, err=%v)", found, err)
	}
	if !reflect.DeepEqual(byEmail, p) {
		t.Errorf("the email hint resolved %+v, want the principal the id resolves = %+v", byEmail, p)
	}
}
