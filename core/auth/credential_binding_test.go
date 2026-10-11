// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// credentialBindingFixture is a real store with the real Authenticator:
// two accounts that are members of one tenant, and a second tenant only the
// administrator belongs to. Sessions are seeded as rows and then authenticated,
// so every principal carries the private reference Authenticate stamps.
type credentialBindingFixture struct {
	t      *testing.T
	ctx    context.Context
	dsn    string
	st     store.Store
	a      *Authenticator
	tenant model.TenantID
	other  model.TenantID
	userA  model.User
	admin  model.User
}

func newCredentialBindingFixture(t *testing.T) *credentialBindingFixture {
	t.Helper()
	return newCredentialBindingFixtureConfig(t, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "credential-binding.db"), Debug: true})
}

func newCredentialBindingFixtureConfig(t *testing.T, cfg store.Config) *credentialBindingFixture {
	t.Helper()
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &credentialBindingFixture{t: t, ctx: ctx, dsn: cfg.DSN, st: st, a: NewAuthenticator(st, nil)}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "binding", Slug: "binding", Status: model.StatusActive})
		if err != nil {
			return err
		}
		f.tenant = org.TenantID
		other, err := sys.CreateOrg(ctx, model.Org{Name: "binding-other", Slug: "binding-other", Status: model.StatusActive})
		f.other = other.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision tenants: %v", err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		f.userA, err = as.Users().Create(ctx, model.User{
			Email: "initiator@binding.test", DisplayName: "Initiator", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		f.admin, err = as.Users().Create(ctx, model.User{
			Email: "admin@binding.test", DisplayName: "Administrator", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		for _, m := range []model.Membership{
			{UserID: f.userA.ID, TargetTenantID: f.tenant, Role: RoleAdmin},
			{UserID: f.admin.ID, TargetTenantID: f.tenant, Role: RoleOwner},
			{UserID: f.admin.ID, TargetTenantID: f.other, Role: RoleOwner},
		} {
			if _, err := as.Memberships().Create(ctx, m); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	return f
}

// session seeds one session of user, lets edit adjust the row, and returns the
// authenticated principal together with its bearer and row.
func (f *credentialBindingFixture) session(
	user model.User,
	edit func(*model.AuthSession),
) (Principal, string, model.AuthSession) {
	f.t.Helper()
	cred, err := NewCredential(PrefixSession)
	if err != nil {
		f.t.Fatalf("mint session: %v", err)
	}
	row := model.AuthSession{
		UserID: user.ID, Selector: cred.Selector, SecretHash: cred.SecretHash,
		ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)), AAL: AAL1, AMR: []string{"pwd"},
	}
	if edit != nil {
		edit(&row)
	}
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		row, err = as.Sessions().Create(f.ctx, row)
		return err
	}); err != nil {
		f.t.Fatalf("seed session: %v", err)
	}
	p, err := f.a.Authenticate(f.ctx, cred.Token)
	if err != nil {
		f.t.Fatalf("authenticate session: %v", err)
	}
	return p, cred.Token, row
}

func (f *credentialBindingFixture) deadline() context.Context {
	ctx, cancel := context.WithTimeout(f.ctx, time.Minute)
	f.t.Cleanup(cancel)
	return ctx
}

func (f *credentialBindingFixture) subject(run model.ID, user model.ID) CredentialBindingSubject {
	return CredentialBindingSubject{Tenant: f.tenant, Kind: CredentialBindingWorkflowRun, Ref: run, User: user}
}

func (f *credentialBindingFixture) mustBind(p Principal, s CredentialBindingSubject) CredentialBinding {
	f.t.Helper()
	b, err := f.a.BindCredential(f.ctx, p, s)
	if err != nil || b.IsZero() {
		f.t.Fatalf("bind credential: %v", err)
	}
	return b
}

func (f *credentialBindingFixture) mustResolve(b CredentialBinding, s CredentialBindingSubject) PrincipalRef {
	f.t.Helper()
	ref, err := f.a.ResolveCredentialBinding(f.deadline(), b, s)
	if err != nil {
		f.t.Fatalf("resolve credential binding: %v", err)
	}
	// The reference carries the binding it was read from, so the exact pair
	// proves that binding current at the epoch it reconstructs (R1).
	if ref.binding != (credentialBindingProof{id: b.id, subject: s}) {
		f.t.Fatalf("resolved reference carries binding %+v, want %s for %+v", ref.binding, b.id, s)
	}
	return ref
}

// pinnedRevision is the credential revision a resolved reference pins,
// without the binding it was read from.
func pinnedRevision(ref PrincipalRef) PrincipalRef {
	ref.binding = credentialBindingProof{}
	return ref
}

func (f *credentialBindingFixture) refuses(b CredentialBinding, s CredentialBindingSubject, what string) {
	f.t.Helper()
	if ref, err := f.a.ResolveCredentialBinding(f.deadline(), b, s); !errors.Is(err, ErrCredentialBindingInvalid) ||
		ref != (PrincipalRef{}) {
		f.t.Fatalf("%s: resolve = %+v, %v; want ErrCredentialBindingInvalid", what, ref, err)
	}
}

func (f *credentialBindingFixture) rows(run model.ID) []model.CredentialBinding {
	f.t.Helper()
	var rows []model.CredentialBinding
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		scope, ok := as.(store.AuthCredentialBindingScope)
		if !ok {
			return errors.New("auth scope lacks credential bindings")
		}
		// The store reads by generation only; these tests use small generations.
		for generation := int64(0); generation < 16; generation++ {
			row, err := scope.CredentialBindings().AtGeneration(f.ctx, f.tenant, CredentialBindingWorkflowRun, run, generation)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return nil
	}); err != nil {
		f.t.Fatalf("list bindings: %v", err)
	}
	return rows
}

func TestBindingPrincipalOriginAndScope(t *testing.T) {
	f := newCredentialBindingFixture(t)
	p, _, row := f.session(f.userA, nil)
	run := model.NewID()
	s := f.subject(run, f.userA.ID)

	// A principal without the private reference Authenticate stamps is refused,
	// whatever its exported fields say.
	synthetic := Principal{Kind: KindUser, UserID: f.userA.ID, CredID: row.ID}
	local, err := NewLocalOperator(LocalOperator{Subject: "operator@binding.test", Via: "cli:test", Reason: "binding test"})
	if err != nil {
		t.Fatalf("local operator: %v", err)
	}
	for name, candidate := range map[string]Principal{"synthetic": synthetic, "local": local} {
		if _, err := f.a.BindCredential(f.ctx, candidate, s); !errors.Is(err, ErrCredentialBindingInvalid) {
			t.Fatalf("%s principal bind = %v, want ErrCredentialBindingInvalid", name, err)
		}
	}
	// A copied principal whose exported identity contradicts its credential:
	// the subject account is compared with the account the credential's rows
	// name, never with p.UserID.
	copied := p
	copied.UserID, copied.Superadmin, copied.AAL = f.admin.ID, true, AAL3
	if _, err := f.a.BindCredential(f.ctx, copied, f.subject(model.NewID(), f.admin.ID)); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("contradictory copied principal bind = %v, want ErrCredentialBindingInvalid", err)
	}
	// Subjects the server would never select: another tenant the credential is
	// not admitted to, an unknown kind, a zero run, a zero account.
	for name, bad := range map[string]CredentialBindingSubject{
		"unadmitted tenant": {Tenant: f.other, Kind: CredentialBindingWorkflowRun, Ref: run, User: f.userA.ID},
		"unknown kind":      {Tenant: f.tenant, Kind: "orchestration.schedule", Ref: run, User: f.userA.ID},
		"zero run":          {Tenant: f.tenant, Kind: CredentialBindingWorkflowRun, User: f.userA.ID},
		"zero account":      {Tenant: f.tenant, Kind: CredentialBindingWorkflowRun, Ref: run},
	} {
		if _, err := f.a.BindCredential(f.ctx, p, bad); !errors.Is(err, ErrCredentialBindingInvalid) {
			t.Fatalf("%s bind = %v, want ErrCredentialBindingInvalid", name, err)
		}
	}
	if rows := f.rows(run); len(rows) != 0 {
		t.Fatalf("refused binds wrote %d rows", len(rows))
	}

	// The copied principal still binds its own credential: exported fields are
	// ignored, not trusted.
	b := f.mustBind(copied, s)
	want, _ := p.Ref()
	if got := f.mustResolve(b, s); pinnedRevision(got) != want {
		t.Fatalf("resolved ref = %+v, want the exact authenticated ref %+v", got, want)
	}
	if _, err := f.a.BindCredential(f.ctx, p, s); !errors.Is(err, ErrCredentialBindingConflict) {
		t.Fatalf("second bind of one subject = %v, want ErrCredentialBindingConflict", err)
	}

	// Resolution checks the whole server-selected subject.
	f.refuses(b, f.subject(model.NewID(), f.userA.ID), "cross-run")
	f.refuses(b, CredentialBindingSubject{Tenant: f.other, Kind: CredentialBindingWorkflowRun, Ref: run, User: f.userA.ID}, "cross-tenant")
	f.refuses(b, f.subject(run, f.admin.ID), "other account")
	f.refuses(b, CredentialBindingSubject{Tenant: f.tenant, Kind: "orchestration.schedule", Ref: run, User: f.userA.ID}, "other kind")
	f.refuses(CredentialBinding{}, s, "missing handle")
	f.refuses(CredentialBinding{id: model.NewID()}, s, "unknown handle")
	// A missing deadline is the caller's operational fault, not provenance.
	if _, err := f.a.ResolveCredentialBinding(f.ctx, b, s); !errors.Is(err, ErrCredentialBindingUnavailable) ||
		errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("resolve without a deadline = %v, want ErrCredentialBindingUnavailable", err)
	}
	// An empty handle is invalid provenance whatever the context: nothing is
	// read to decide it, so no deadline is required.
	if _, err := f.a.ResolveCredentialBinding(f.ctx, CredentialBinding{}, s); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("resolve an empty handle without a deadline = %v, want ErrCredentialBindingInvalid", err)
	}
	if parsed, err := ParseCredentialBindingStorage(b.StorageValue()); err != nil || parsed != b {
		t.Fatalf("storage round trip = %v, %v", parsed, err)
	}
	if _, err := ParseCredentialBindingStorage("not-a-binding"); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("malformed storage parse = %v", err)
	}

	// A tampered row fails its seal and resolves nothing.
	db, err := sql.Open("sqlite", f.dsn)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(f.ctx,
		"UPDATE core_credential_bindings SET credential_version = credential_version + 1 WHERE id = ?",
		b.StorageValue()); err != nil {
		t.Fatalf("tamper binding row: %v", err)
	}
	f.refuses(b, s, "tampered row")

	// The handle never prints or serializes.
	text := fmt.Sprintf("%v %+v %#v %s", b, struct{ B CredentialBinding }{b}, b, b)
	if strings.Contains(text, b.StorageValue()) {
		t.Fatalf("formatted handle leaked its value: %s", text)
	}
	if raw, err := json.Marshal(struct{ B CredentialBinding }{b}); err == nil {
		t.Fatalf("handle serialized to %s", raw)
	}
}

func TestBindingRevisionLifecycle(t *testing.T) {
	f := newCredentialBindingFixture(t)
	check := func(name string, b CredentialBinding, s CredentialBindingSubject, pinned PrincipalRef, current bool) {
		t.Helper()
		ref := f.mustResolve(b, s)
		if pinnedRevision(ref) != pinned {
			t.Fatalf("%s: binding resolved %+v, want the pinned revision %+v (never a newer credential)", name, ref, pinned)
		}
		_, err := f.a.ResolvePrincipalScope(f.deadline(), ref, f.tenant)
		if current && err != nil {
			t.Fatalf("%s: pinned revision is not current: %v", name, err)
		}
		if !current && !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("%s: pinned revision resolution = %v, want ErrUnauthenticated", name, err)
		}
	}

	// Routine authentication keeps the revision; refresh rotates it.
	p, token, _ := f.session(f.userA, nil)
	s := f.subject(model.NewID(), f.userA.ID)
	b := f.mustBind(p, s)
	pinned, _ := p.Ref()
	for i := 0; i < 3; i++ {
		if _, err := f.a.Authenticate(f.ctx, token); err != nil {
			t.Fatalf("repeat authentication: %v", err)
		}
	}
	check("repeated authentication", b, s, pinned, true)
	if _, _, err := f.a.RefreshSession(f.ctx, p); err != nil {
		t.Fatalf("refresh session: %v", err)
	}
	check("after refresh", b, s, pinned, false)

	// Step-up keeps the bearer and changes the row's assurance: the pinned
	// revision is conservatively invalidated, not rotated.
	p2, _, row2 := f.session(f.userA, nil)
	s2 := f.subject(model.NewID(), f.userA.ID)
	b2 := f.mustBind(p2, s2)
	pinned2, _ := p2.Ref()
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		row, err := as.Sessions().Get(f.ctx, row2.ID)
		if err != nil {
			return err
		}
		until := model.NewTimestamp(time.Now().Add(time.Minute))
		row.AAL, row.AMR, row.AALExpiresAt = AAL3, []string{"pwd", "webauthn"}, &until
		stamp := model.NewTimestamp(time.Now())
		row.AALAuthenticatedAt = &stamp
		_, err = as.Sessions().Update(f.ctx, row)
		return err
	}); err != nil {
		t.Fatalf("step up session: %v", err)
	}
	check("after step-up revision", b2, s2, pinned2, false)

	// Revocation and expiry refuse the pinned revision.
	p3, _, row3 := f.session(f.userA, nil)
	s3 := f.subject(model.NewID(), f.userA.ID)
	b3 := f.mustBind(p3, s3)
	pinned3, _ := p3.Ref()
	if err := f.a.RevokeSession(f.ctx, p3, row3.ID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	check("after revocation", b3, s3, pinned3, false)

	p4, _, _ := f.session(f.userA, func(row *model.AuthSession) {
		row.ExpiresAt = model.NewTimestamp(time.Now().Add(1500 * time.Millisecond))
	})
	s4 := f.subject(model.NewID(), f.userA.ID)
	b4 := f.mustBind(p4, s4)
	pinned4, _ := p4.Ref()
	check("before expiry", b4, s4, pinned4, true)

	// An elevated window closing needs no row update: the pinned revision stays
	// current and its assurance falls to AAL1 at database time.
	p5, _, _ := f.session(f.userA, func(row *model.AuthSession) {
		until := model.NewTimestamp(time.Now().Add(1500 * time.Millisecond))
		row.AAL, row.AMR, row.AALExpiresAt = AAL3, []string{"pwd", "webauthn"}, &until
		stamp := model.NewTimestamp(time.Now())
		row.AALAuthenticatedAt = &stamp
	})
	s5 := f.subject(model.NewID(), f.userA.ID)
	b5 := f.mustBind(p5, s5)
	elevated, err := f.a.ResolvePrincipalScope(f.deadline(), f.mustResolve(b5, s5), f.tenant)
	if err != nil || elevated.AAL != AAL3 {
		t.Fatalf("elevated resolution = AAL %d, %v; want AAL3", elevated.AAL, err)
	}
	time.Sleep(2 * time.Second)
	check("after expiry", b4, s4, pinned4, false)
	degraded, err := f.a.ResolvePrincipalScope(f.deadline(), f.mustResolve(b5, s5), f.tenant)
	if err != nil || degraded.AAL != AAL1 {
		t.Fatalf("expired step-up resolution = AAL %d, %v; want AAL1", degraded.AAL, err)
	}

	// Reauthorization binds the same account's fresh revision as a successor;
	// the old handle stops resolving.
	fresh, _, _ := f.session(f.userA, nil)
	successor, err := f.a.RebindCredential(f.ctx, b, 1, fresh, s, fresh)
	if err != nil {
		t.Fatalf("rebind fresh revision: %v", err)
	}
	freshRef, _ := fresh.Ref()
	check("successor", successor, s, freshRef, true)
	f.refuses(b, s, "superseded predecessor")
}

func TestCredentialBindingRebindGeneration(t *testing.T) {
	f := newCredentialBindingFixture(t)
	p, _, _ := f.session(f.userA, nil)
	run := model.NewID()
	s := f.subject(run, f.userA.ID)
	original := f.mustBind(p, s)

	freshA, _, _ := f.session(f.userA, nil)
	freshB, _, _ := f.session(f.userA, nil)
	if _, err := f.a.RebindCredential(f.ctx, original, 0, freshA, s, freshA); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("generation 0 succession = %v, want ErrCredentialBindingInvalid", err)
	}
	first, err := f.a.RebindCredential(f.ctx, original, 1, freshA, s, freshA)
	if err != nil {
		t.Fatalf("succession at generation 1: %v", err)
	}
	// A retried call is the same successor; another credential at the same
	// generation lost the race.
	if again, err := f.a.RebindCredential(f.ctx, original, 1, freshA, s, freshA); err != nil || again != first {
		t.Fatalf("retried succession = %v, %v; want the same successor", again, err)
	}
	if _, err := f.a.RebindCredential(f.ctx, original, 1, freshB, s, freshB); !errors.Is(err, ErrCredentialBindingConflict) {
		t.Fatalf("second successor at generation 1 = %v, want ErrCredentialBindingConflict", err)
	}
	f.refuses(original, s, "predecessor after succession")

	// The owner never published generation 1 (crash or lost CAS). A later
	// reservation supersedes that orphan; a stale generation cannot.
	second, err := f.a.RebindCredential(f.ctx, original, 2, freshB, s, freshB)
	if err != nil {
		t.Fatalf("succession over an unpublished orphan: %v", err)
	}
	f.refuses(first, s, "superseded orphan")
	freshBRef, _ := freshB.Ref()
	if got := f.mustResolve(second, s); pinnedRevision(got) != freshBRef {
		t.Fatalf("current successor resolves %+v, want %+v", got, freshBRef)
	}
	if _, err := f.a.RebindCredential(f.ctx, original, 1, freshA, s, freshA); err != nil {
		// generation 1 already has its (superseded) row: the retry returns it,
		// and it resolves nothing because it is superseded.
		t.Fatalf("retry at an old generation = %v", err)
	}
	f.refuses(first, s, "old generation retry")
	freshC, _, _ := f.session(f.userA, nil)
	if _, err := f.a.RebindCredential(f.ctx, original, 1, freshC, s, freshC); !errors.Is(err, ErrCredentialBindingConflict) {
		t.Fatalf("new credential at a stale generation = %v, want ErrCredentialBindingConflict", err)
	}

	// Concurrent successions at one generation: exactly one winner.
	var wg sync.WaitGroup
	results := make([]error, 4)
	winners := make([]CredentialBinding, 4)
	for i := range results {
		fresh, _, _ := f.session(f.userA, nil)
		wg.Add(1)
		go func(i int, fresh Principal) {
			defer wg.Done()
			winners[i], results[i] = f.a.RebindCredential(f.ctx, second, 3, fresh, s, fresh)
		}(i, fresh)
	}
	wg.Wait()
	won := 0
	for i, err := range results {
		switch {
		case err == nil:
			won++
			f.mustResolve(winners[i], s)
		case !errors.Is(err, ErrCredentialBindingConflict):
			t.Fatalf("concurrent succession %d = %v, want success or conflict", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("concurrent successions won = %d, want exactly one", won)
	}
	live := 0
	for _, row := range f.rows(run) {
		if row.SupersededBy.IsZero() {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("live bindings after successions = %d, want one", live)
	}
}

func TestCredentialBindingSameSubjectSeparateAdministrator(t *testing.T) {
	f := newCredentialBindingFixture(t)
	p, _, _ := f.session(f.userA, nil)
	run := model.NewID()
	s := f.subject(run, f.userA.ID)
	original := f.mustBind(p, s)
	admin, _, _ := f.session(f.admin, nil)

	// The administrator's own credential never replaces the run's account.
	if _, err := f.a.RebindCredential(f.ctx, original, 1, admin, s, admin); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("administrator's credential succession = %v, want ErrCredentialBindingInvalid", err)
	}
	if _, err := f.a.RebindCredential(f.ctx, original, 1, admin, f.subject(run, f.admin.ID), admin); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("succession claiming the administrator as subject = %v, want ErrCredentialBindingInvalid", err)
	}
	// An unauthenticated administrator is refused.
	fresh, _, _ := f.session(f.userA, nil)
	if _, err := f.a.RebindCredential(f.ctx, original, 1, fresh, s, Principal{Kind: KindUser, UserID: f.admin.ID}); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("synthetic administrator = %v, want ErrCredentialBindingInvalid", err)
	}
	// The account's fresh credential, approved by the administrator.
	successor, err := f.a.RebindCredential(f.ctx, original, 1, fresh, s, admin)
	if err != nil {
		t.Fatalf("administrator-approved succession: %v", err)
	}
	freshRef, _ := fresh.Ref()
	if got := f.mustResolve(successor, s); pinnedRevision(got) != freshRef {
		t.Fatalf("successor resolves %+v, want the account's fresh credential %+v", got, freshRef)
	}
	var found bool
	for _, row := range f.rows(run) {
		if row.ID != successor.id {
			continue
		}
		found = true
		if row.AuthorizedByActor != "user:"+f.admin.ID.String() || row.SubjectUserID != f.userA.ID ||
			row.CredentialID != freshRef.credentialID {
			t.Fatalf("successor row = %+v", row)
		}
	}
	if !found {
		t.Fatal("successor row is absent")
	}
	// One audit event, carrying digests and never a handle.
	var events []model.AuditEvent
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(f.ctx, 1, func(e model.AuditEvent) error {
			if e.Action == "auth.credential_binding.rebind" {
				events = append(events, e)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	if len(events) != 1 || events[0].Actor != "user:"+f.admin.ID.String() || events[0].TargetID != run {
		t.Fatalf("succession audit = %+v", events)
	}
	meta, _ := json.Marshal(events[0].Meta)
	for _, handle := range []CredentialBinding{original, successor} {
		if strings.Contains(string(meta), handle.StorageValue()) {
			t.Fatalf("audit meta carries a handle: %s", meta)
		}
	}
}
