// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// principalEvidenceAppClock is deliberately far from the DB witness used by
// the tests. ResolvePrincipalScope must never consult it for expiry, AAL, or
// provenance; Authenticate continues to use it on its historical hot path.
type principalEvidenceAppClock struct{ now model.Timestamp }

func (c principalEvidenceAppClock) Now() model.Timestamp { return c.now }

type principalEvidenceHooks struct {
	now               model.Timestamp
	clockErr          error
	epochs            []store.AuthorizationFactRef
	epochFact         store.AuthorizationFactRef
	epochErr          error
	epochErrAt        int
	epochCall         int
	trace             []string
	membershipQueries []model.Query
	// rewriteMemberships is a repository-decorator seam for causal malformed
	// row tests. It never affects the durable fixture itself.
	rewriteMemberships  func([]model.Membership) []model.Membership
	rewriteGroupMembers func([]model.UserGroupMember) []model.UserGroupMember
	rewriteGroup        func(model.UserGroup) model.UserGroup
	lookupGroup         func(context.Context, store.Repository[model.UserGroup], model.ID) (model.UserGroup, error)
	rewriteSession      func(model.AuthSession) model.AuthSession
	rewriteToken        func(model.APIToken) model.APIToken
}

func (h *principalEvidenceHooks) reset() {
	h.epochCall = 0
	h.trace = nil
	h.membershipQueries = nil
}

type principalEvidenceStore struct {
	store.Store
	views          int
	skipCallback   bool
	repeatCallback bool
	wrap           func(store.AuthScope) store.AuthScope
}

func (s *principalEvidenceStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	s.views++
	if s.skipCallback {
		return nil
	}
	return s.Store.AuthView(ctx, func(as store.AuthScope) error {
		if s.wrap != nil {
			as = s.wrap(as)
		}
		if err := fn(as); err != nil || !s.repeatCallback {
			return err
		}
		return fn(as)
	})
}

// principalEvidenceScope exposes the optional evidence capability while
// tracing only the repositories ResolvePrincipalScope is allowed to read.
type principalEvidenceScope struct {
	store.AuthScope
	hooks *principalEvidenceHooks
}

var _ store.AuthPrincipalEvidenceScope = (*principalEvidenceScope)(nil)

func (s *principalEvidenceScope) ReadDirectoryEpochFact(
	ctx context.Context,
	tenant model.TenantID,
) (store.AuthorizationFactRef, error) {
	s.hooks.epochCall++
	s.hooks.trace = append(s.hooks.trace, fmt.Sprintf("directory-%d", s.hooks.epochCall))
	if s.hooks.epochErr != nil && (s.hooks.epochErrAt == 0 || s.hooks.epochCall == s.hooks.epochErrAt) {
		return s.hooks.epochFact, s.hooks.epochErr
	}
	if len(s.hooks.epochs) >= s.hooks.epochCall {
		return s.hooks.epochs[s.hooks.epochCall-1], nil
	}
	evidence, ok := s.AuthScope.(store.AuthPrincipalEvidenceScope)
	if !ok {
		return store.AuthorizationFactRef{}, errors.New("underlying auth scope lacks evidence capability")
	}
	return evidence.ReadDirectoryEpochFact(ctx, tenant)
}

func (s *principalEvidenceScope) TransactionNow(context.Context) (model.Timestamp, error) {
	s.hooks.trace = append(s.hooks.trace, "db-clock")
	return s.hooks.now, s.hooks.clockErr
}

func (s *principalEvidenceScope) Users() store.MutableRepository[model.User] {
	return principalEvidenceTraceMutableRepo[model.User]{MutableRepository: s.AuthScope.Users(), hooks: s.hooks, name: "user"}
}

func (s *principalEvidenceScope) Memberships() store.Repository[model.Membership] {
	return principalEvidenceMembershipRepo{Repository: s.AuthScope.Memberships(), hooks: s.hooks}
}

func (s *principalEvidenceScope) Groups() store.Repository[model.UserGroup] {
	return principalEvidenceGroupRepo{Repository: s.AuthScope.Groups(), hooks: s.hooks}
}

func (s *principalEvidenceScope) GroupMembers() store.Repository[model.UserGroupMember] {
	return principalEvidenceGroupMemberRepo{Repository: s.AuthScope.GroupMembers(), hooks: s.hooks}
}

func (s *principalEvidenceScope) Sessions() store.Repository[model.AuthSession] {
	return principalEvidenceSessionRepo{Repository: s.AuthScope.Sessions(), hooks: s.hooks}
}

func (s *principalEvidenceScope) Tokens() store.Repository[model.APIToken] {
	return principalEvidenceTokenRepo{Repository: s.AuthScope.Tokens(), hooks: s.hooks}
}

type principalEvidenceTraceRepo[T any] struct {
	store.Repository[T]
	hooks *principalEvidenceHooks
	name  string
}

type principalEvidenceTraceMutableRepo[T any] struct {
	store.MutableRepository[T]
	hooks *principalEvidenceHooks
	name  string
}

type principalEvidenceMembershipRepo struct {
	store.Repository[model.Membership]
	hooks *principalEvidenceHooks
}

type principalEvidenceGroupRepo struct {
	store.Repository[model.UserGroup]
	hooks *principalEvidenceHooks
}

type principalEvidenceGroupMemberRepo struct {
	store.Repository[model.UserGroupMember]
	hooks *principalEvidenceHooks
}

func (r principalEvidenceGroupMemberRepo) Get(ctx context.Context, id model.ID) (model.UserGroupMember, error) {
	r.hooks.trace = append(r.hooks.trace, "group-member-get")
	return r.Repository.Get(ctx, id)
}

func (r principalEvidenceGroupMemberRepo) List(ctx context.Context, q model.Query) ([]model.UserGroupMember, model.Page, error) {
	r.hooks.trace = append(r.hooks.trace, "group-member-list")
	items, page, err := r.Repository.List(ctx, q)
	if err == nil && r.hooks.rewriteGroupMembers != nil {
		items = r.hooks.rewriteGroupMembers(items)
	}
	return items, page, err
}

func (r principalEvidenceGroupRepo) Get(ctx context.Context, id model.ID) (model.UserGroup, error) {
	r.hooks.trace = append(r.hooks.trace, "group-get")
	if r.hooks.lookupGroup != nil {
		return r.hooks.lookupGroup(ctx, r.Repository, id)
	}
	group, err := r.Repository.Get(ctx, id)
	if err == nil && r.hooks.rewriteGroup != nil {
		group = r.hooks.rewriteGroup(group)
	}
	return group, err
}

func (r principalEvidenceGroupRepo) List(ctx context.Context, q model.Query) ([]model.UserGroup, model.Page, error) {
	r.hooks.trace = append(r.hooks.trace, "group-list")
	return r.Repository.List(ctx, q)
}

type principalEvidenceTokenRepo struct {
	store.Repository[model.APIToken]
	hooks *principalEvidenceHooks
}

type principalEvidenceSessionRepo struct {
	store.Repository[model.AuthSession]
	hooks *principalEvidenceHooks
}

func (r principalEvidenceSessionRepo) Get(ctx context.Context, id model.ID) (model.AuthSession, error) {
	r.hooks.trace = append(r.hooks.trace, "session-get")
	session, err := r.Repository.Get(ctx, id)
	if err == nil && r.hooks.rewriteSession != nil {
		session = r.hooks.rewriteSession(session)
	}
	return session, err
}

func (r principalEvidenceSessionRepo) List(ctx context.Context, q model.Query) ([]model.AuthSession, model.Page, error) {
	r.hooks.trace = append(r.hooks.trace, "session-list")
	return r.Repository.List(ctx, q)
}

func (r principalEvidenceTokenRepo) Get(ctx context.Context, id model.ID) (model.APIToken, error) {
	r.hooks.trace = append(r.hooks.trace, "token-get")
	token, err := r.Repository.Get(ctx, id)
	if err == nil && r.hooks.rewriteToken != nil {
		token = r.hooks.rewriteToken(token)
	}
	return token, err
}

func (r principalEvidenceTokenRepo) List(ctx context.Context, q model.Query) ([]model.APIToken, model.Page, error) {
	r.hooks.trace = append(r.hooks.trace, "token-list")
	return r.Repository.List(ctx, q)
}

func (r principalEvidenceMembershipRepo) Get(ctx context.Context, id model.ID) (model.Membership, error) {
	r.hooks.trace = append(r.hooks.trace, "membership-get")
	return r.Repository.Get(ctx, id)
}

func (r principalEvidenceMembershipRepo) List(ctx context.Context, q model.Query) ([]model.Membership, model.Page, error) {
	r.hooks.trace = append(r.hooks.trace, "membership-list")
	r.hooks.membershipQueries = append(r.hooks.membershipQueries, q)
	items, page, err := r.Repository.List(ctx, q)
	if err == nil && r.hooks.rewriteMemberships != nil {
		items = r.hooks.rewriteMemberships(items)
	}
	return items, page, err
}

func (r principalEvidenceTraceMutableRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	r.hooks.trace = append(r.hooks.trace, r.name+"-get")
	return r.MutableRepository.Get(ctx, id)
}

func (r principalEvidenceTraceMutableRepo[T]) List(ctx context.Context, q model.Query) ([]T, model.Page, error) {
	r.hooks.trace = append(r.hooks.trace, r.name+"-list")
	return r.MutableRepository.List(ctx, q)
}

func (r principalEvidenceTraceRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	r.hooks.trace = append(r.hooks.trace, r.name+"-get")
	return r.Repository.Get(ctx, id)
}

func (r principalEvidenceTraceRepo[T]) List(ctx context.Context, q model.Query) ([]T, model.Page, error) {
	r.hooks.trace = append(r.hooks.trace, r.name+"-list")
	return r.Repository.List(ctx, q)
}

// principalEvidenceNoCapabilityScope intentionally embeds only AuthScope. The
// embedded interface's method set does not expose the optional capability even
// when the concrete scope behind it implements one.
type principalEvidenceNoCapabilityScope struct{ store.AuthScope }

// principalEvidenceFixedDeadlineContext lets a test present an expired/equal
// deadline without context cancellation preventing the callback from reaching
// the DB-clock branch it is meant to exercise.
type principalEvidenceFixedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c principalEvidenceFixedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

// principalEvidenceUUIDv4ID deliberately constructs a canonical UUID which is
// nevertheless invalid for every durable identity in this evidence path. The
// distinction makes the Version(7) check mutation-sensitive rather than merely
// exercising ParseID's syntax validation.
func principalEvidenceUUIDv4ID(t *testing.T) model.ID {
	t.Helper()
	id := model.ID(uuid.NewString())
	parsed, err := uuid.Parse(id.String())
	if err != nil || parsed.Version() != uuid.Version(4) || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("fixture UUID = %q / %v, want canonical UUIDv4", id, err)
	}
	return id
}

func principalEvidenceNoncanonicalUpperUUIDv7(t *testing.T) string {
	t.Helper()
	for range 32 {
		canonical := model.NewID().String()
		upper := strings.ToUpper(canonical)
		if upper != canonical {
			return upper
		}
	}
	t.Fatal("could not construct a noncanonical uppercase UUIDv7 fixture")
	return ""
}

type principalEvidenceFixture struct {
	t       *testing.T
	ctx     context.Context
	raw     store.Store
	st      *principalEvidenceStore
	hooks   *principalEvidenceHooks
	a       *Authenticator
	tenant  model.TenantID
	now     time.Time
	user    model.User
	member  model.Membership
	session model.AuthSession
	token   model.APIToken
	sessRaw string
	tokRaw  string
}

func newPrincipalEvidenceFixture(t *testing.T) *principalEvidenceFixture {
	t.Helper()
	ctx := context.Background()
	raw, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now().UTC().Truncate(time.Nanosecond)
	hooks := &principalEvidenceHooks{now: model.NewTimestamp(now)}
	wrapped := &principalEvidenceStore{Store: raw}
	wrapped.wrap = func(as store.AuthScope) store.AuthScope {
		return &principalEvidenceScope{AuthScope: as, hooks: hooks}
	}
	f := &principalEvidenceFixture{
		t: t, ctx: ctx, raw: raw, st: wrapped, hooks: hooks,
		a: NewAuthenticator(wrapped, principalEvidenceAppClock{
			now: model.NewTimestamp(time.Date(2001, time.January, 2, 3, 4, 5, 0, time.UTC)),
		}),
		now: now,
	}
	if err := raw.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "principal-evidence", Slug: "principal-evidence", Status: model.StatusActive})
		f.tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}

	sessionCred, err := NewCredential(PrefixSession)
	if err != nil {
		t.Fatalf("mint session credential: %v", err)
	}
	tokenCred, err := NewCredential(PrefixToken)
	if err != nil {
		t.Fatalf("mint API credential: %v", err)
	}
	expires := model.NewTimestamp(now.Add(time.Hour))
	if err := raw.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		f.user, err = as.Users().Create(ctx, model.User{
			Email: "principal-evidence@example.test", DisplayName: "Principal Evidence", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		f.member, err = as.Memberships().Create(ctx, model.Membership{
			UserID: f.user.ID, TargetTenantID: f.tenant, Role: RoleViewer,
		})
		if err != nil {
			return err
		}
		f.session, err = as.Sessions().Create(ctx, model.AuthSession{
			UserID: f.user.ID, Selector: sessionCred.Selector, SecretHash: sessionCred.SecretHash,
			ExpiresAt: expires, AAL: AAL1, AMR: []string{"pwd"},
		})
		if err != nil {
			return err
		}
		f.token, err = as.Tokens().Create(ctx, model.APIToken{
			Name: "principal-evidence-token", UserID: f.user.ID,
			Selector: tokenCred.Selector, SecretHash: tokenCred.SecretHash,
			BoundTenantID: f.tenant, Role: RoleViewer, ExpiresAt: &expires,
		})
		return err
	}); err != nil {
		t.Fatalf("seed authenticated credentials: %v", err)
	}
	f.sessRaw, f.tokRaw = sessionCred.Token, tokenCred.Token
	return f
}

func (f *principalEvidenceFixture) deadline(after time.Duration) context.Context {
	ctx, cancel := context.WithDeadline(f.ctx, f.now.Add(after))
	f.t.Cleanup(cancel)
	return ctx
}

func (f *principalEvidenceFixture) resetTrace() {
	f.st.views = 0
	f.hooks.reset()
}

func (f *principalEvidenceFixture) sessionRef() PrincipalRef {
	f.t.Helper()
	p, err := f.a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		f.t.Fatalf("authenticate session: %v", err)
	}
	ref, ok := p.Ref()
	if !ok {
		f.t.Fatal("authenticated session has no principal ref")
	}
	return ref
}

func (f *principalEvidenceFixture) tokenRef() PrincipalRef {
	f.t.Helper()
	p, err := f.a.Authenticate(f.ctx, f.tokRaw)
	if err != nil {
		f.t.Fatalf("authenticate token: %v", err)
	}
	ref, ok := p.Ref()
	if !ok {
		f.t.Fatal("authenticated token has no principal ref")
	}
	return ref
}

func (f *principalEvidenceFixture) provisionTenant(name, slug string) model.TenantID {
	f.t.Helper()
	var tenant model.TenantID
	if err := f.raw.System(f.ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(f.ctx, model.Org{Name: name, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		f.t.Fatalf("provision tenant %q: %v", slug, err)
	}
	return tenant
}

func (f *principalEvidenceFixture) addGroupWithMember(
	tenant model.TenantID,
	name string,
	mappedRole string,
) model.UserGroup {
	return f.addGroup(tenant, name, mappedRole, true)
}

func (f *principalEvidenceFixture) addGroup(
	tenant model.TenantID,
	name string,
	mappedRole string,
	withMember bool,
) model.UserGroup {
	f.t.Helper()
	var group model.UserGroup
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		var err error
		group, err = as.Groups().Create(f.ctx, model.UserGroup{
			TargetTenantID: tenant,
			DisplayName:    name,
		})
		if err != nil {
			return err
		}
		if mappedRole != "" {
			group.MappedRole = mappedRole
			group, err = as.Groups().Update(f.ctx, group)
			if err != nil {
				return err
			}
		}
		if !withMember {
			return nil
		}
		_, err = as.GroupMembers().Create(f.ctx, model.UserGroupMember{GroupID: group.ID, UserID: f.user.ID})
		return err
	}); err != nil {
		f.t.Fatalf("seed group %q: %v", name, err)
	}
	return group
}

func (f *principalEvidenceFixture) directoryEpoch(tenant model.TenantID) store.AuthorizationFactRef {
	f.t.Helper()
	var fact store.AuthorizationFactRef
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		var err error
		fact, err = as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(f.ctx, tenant)
		return err
	}); err != nil {
		f.t.Fatalf("read directory epoch for %s: %v", tenant, err)
	}
	return fact
}

func (f *principalEvidenceFixture) addUnadmittedSessionRef() (PrincipalRef, model.AuthSession) {
	f.t.Helper()
	credential, err := NewCredential(PrefixSession)
	if err != nil {
		f.t.Fatalf("mint unadmitted session credential: %v", err)
	}
	var session model.AuthSession
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		user, err := as.Users().Create(f.ctx, model.User{
			Email:       "unadmitted-principal-evidence@example.test",
			DisplayName: "Unadmitted Principal Evidence",
			Status:      model.StatusActive,
		})
		if err != nil {
			return err
		}
		session, err = as.Sessions().Create(f.ctx, model.AuthSession{
			UserID: user.ID, Selector: credential.Selector, SecretHash: credential.SecretHash,
			ExpiresAt: model.NewTimestamp(f.now.Add(time.Hour)), AAL: AAL1, AMR: []string{"pwd"},
		})
		return err
	}); err != nil {
		f.t.Fatalf("seed unadmitted session: %v", err)
	}
	principal, err := f.a.Authenticate(f.ctx, credential.Token)
	if err != nil {
		f.t.Fatalf("authenticate unadmitted session: %v", err)
	}
	ref, ok := principal.Ref()
	if !ok {
		f.t.Fatal("unadmitted authenticated session has no ref")
	}
	return ref, session
}

func TestPrincipalRefIsOpaqueVersionBoundAndAuthenticateStampsCredentialShapes(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)

	assertRef := func(name, raw string, wantKind PrincipalKind) PrincipalRef {
		t.Helper()
		p, err := f.a.Authenticate(f.ctx, raw)
		if err != nil {
			t.Fatalf("%s authenticate: %v", name, err)
		}
		ref, ok := p.Ref()
		if !ok || ref.kind != wantKind || ref.credentialID != p.CredID || ref.version < 1 {
			t.Fatalf("%s ref = %+v / %t, principal=%+v", name, ref, ok, p)
		}
		if ref != ref { // PrincipalRef must remain comparable for callers' stable maps/sets.
			t.Fatalf("%s ref is not comparable", name)
		}
		return ref
	}

	sessionRef := assertRef("session", f.sessRaw, KindUser)
	ordinaryRef := assertRef("ordinary", f.tokRaw, KindToken)
	if sessionRef == ordinaryRef {
		t.Fatal("distinct credentials produced the same principal ref")
	}
	authenticated, err := f.a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		t.Fatalf("authenticate principal binding fixture: %v", err)
	}
	wrongCredential := authenticated
	wrongCredential.CredID = model.NewID()
	if _, ok := wrongCredential.Ref(); ok {
		t.Fatal("principal with changed public credential id unexpectedly exposes ref")
	}
	wrongKind := authenticated
	wrongKind.Kind = KindToken
	if _, ok := wrongKind.Ref(); ok {
		t.Fatal("principal with changed public kind unexpectedly exposes ref")
	}
	wrongPrivateKind := authenticated
	wrongPrivateKind.credentialRef.kind = KindToken
	if _, ok := wrongPrivateKind.Ref(); ok {
		t.Fatal("principal with changed private ref kind unexpectedly exposes ref")
	}
	for _, test := range []struct {
		name         string
		credentialID model.ID
	}{
		{name: "UUIDv4 credential id", credentialID: principalEvidenceUUIDv4ID(t)},
		{name: "uppercase noncanonical credential id", credentialID: model.ID(principalEvidenceNoncanonicalUpperUUIDv7(t))},
	} {
		t.Run(test.name, func(t *testing.T) {
			malformed := authenticated
			// Keep public and private ids equal: Ref must reject the durable
			// identity itself, not merely the public/private binding check.
			malformed.CredID = test.credentialID
			malformed.credentialRef.credentialID = test.credentialID
			if _, ok := malformed.Ref(); ok {
				t.Fatalf("principal with %s unexpectedly exposes ref", test.name)
			}
		})
	}

	system, err := NewSystemOperator("test:principal-evidence", "credential ref coverage")
	if err != nil {
		t.Fatalf("system operator: %v", err)
	}
	live := NewAuthenticator(f.st, model.SystemClock{})
	work, err := live.IssueWorkSessionCredential(f.ctx, system, WorkSessionCredentialSpec{
		Tenant: f.tenant, SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
		AgentRef: "agent:" + model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatalf("issue work credential: %v", err)
	}
	workRef := assertRef("work", work.Token, KindToken)
	if workRef.credentialID != work.ID {
		t.Fatalf("work ref id = %s, want %s", workRef.credentialID, work.ID)
	}

	communication, err := live.IssueCommunicationSessionCredential(f.ctx, system, CommunicationSessionCredentialSpec{
		Tenant: f.tenant, WorkspaceID: model.NewID(), SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), AgentRef: "agent:" + model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatalf("issue communication credential: %v", err)
	}
	communicationRef := assertRef("communication", communication.Token, KindToken)
	if communicationRef.credentialID != communication.ID {
		t.Fatalf("communication ref id = %s, want %s", communicationRef.credentialID, communication.ID)
	}

	for name, principal := range map[string]Principal{
		"zero":       {},
		"synthetic":  newPrincipal(KindUser, f.user.ID, f.session.ID, false, "", nil, nil),
		"local":      mustPrincipalEvidenceLocal(t),
		"malformed":  newPrincipal(KindToken, "", f.token.ID, false, "", nil, nil).withCredentialRef(0),
		"delegation": PrincipalForDelegation(VerifiedDelegation{}),
	} {
		if _, ok := principal.Ref(); ok {
			t.Fatalf("%s principal unexpectedly exposes a credential ref", name)
		}
	}
	lookup, found, err := f.a.PrincipalForToken(f.ctx, f.token.ID)
	if err != nil || !found {
		t.Fatalf("lookup principal = found %t, err %v", found, err)
	}
	if _, ok := lookup.Ref(); ok {
		t.Fatal("lookup principal unexpectedly exposes a credential ref")
	}
	userLookup, found, err := f.a.PrincipalForUser(f.ctx, f.user.ID.String(), AAL1)
	if err != nil || !found {
		t.Fatalf("user lookup principal = found %t, err %v", found, err)
	}
	if _, ok := userLookup.Ref(); ok {
		t.Fatal("user lookup principal unexpectedly exposes a credential ref")
	}
}

func TestAuthenticateStampsExactRenewedCredentialVersion(t *testing.T) {
	for _, test := range []struct {
		name     string
		renew    func(*principalEvidenceFixture) int64
		bearer   func(*principalEvidenceFixture) string
		wantKind PrincipalKind
	}{
		{
			name: "session",
			renew: func(f *principalEvidenceFixture) int64 {
				if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
					session, err := as.Sessions().Get(f.ctx, f.session.ID)
					if err != nil {
						return err
					}
					session.AMR = []string{"pwd", "renewed"}
					f.session, err = as.Sessions().Update(f.ctx, session)
					return err
				}); err != nil {
					f.t.Fatalf("renew session: %v", err)
				}
				return f.session.Version
			},
			bearer:   func(f *principalEvidenceFixture) string { return f.sessRaw },
			wantKind: KindUser,
		},
		{
			name: "token",
			renew: func(f *principalEvidenceFixture) int64 {
				if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
					token, err := as.Tokens().Get(f.ctx, f.token.ID)
					if err != nil {
						return err
					}
					token.Name = "principal-evidence-token-renewed"
					f.token, err = as.Tokens().Update(f.ctx, token)
					return err
				}); err != nil {
					f.t.Fatalf("renew token: %v", err)
				}
				return f.token.Version
			},
			bearer:   func(f *principalEvidenceFixture) string { return f.tokRaw },
			wantKind: KindToken,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			wantVersion := test.renew(f)
			if wantVersion < 2 {
				t.Fatalf("renewed %s version = %d, want at least 2", test.name, wantVersion)
			}
			principal, err := f.a.Authenticate(f.ctx, test.bearer(f))
			if err != nil {
				t.Fatalf("authenticate renewed %s: %v", test.name, err)
			}
			ref, ok := principal.Ref()
			if !ok || ref.kind != test.wantKind || ref.version != wantVersion {
				t.Fatalf("renewed %s ref = %+v / %t, want kind %q and exact version %d",
					test.name, ref, ok, test.wantKind, wantVersion)
			}
		})
	}
}

func mustPrincipalEvidenceLocal(t *testing.T) Principal {
	t.Helper()
	p, err := NewLocalOperator(LocalOperator{Subject: "operator", Via: "test:principal-evidence", Reason: "test"})
	if err != nil {
		t.Fatalf("new local operator: %v", err)
	}
	return p
}

func TestResolvePrincipalScopeRejectsNonV7OrNoncanonicalTenantBeforeAuthView(t *testing.T) {
	for _, test := range []struct {
		name   string
		tenant func(*principalEvidenceFixture) model.TenantID
	}{
		{
			name: "UUIDv4 tenant",
			tenant: func(t *principalEvidenceFixture) model.TenantID {
				return model.TenantID(principalEvidenceUUIDv4ID(t.t))
			},
		},
		{
			name: "uppercase noncanonical UUIDv7 tenant",
			tenant: func(t *principalEvidenceFixture) model.TenantID {
				return model.TenantID(principalEvidenceNoncanonicalUpperUUIDv7(t.t))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			ref := f.tokenRef()
			f.resetTrace()
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, test.tenant(f)); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("ResolvePrincipalScope error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
			if f.st.views != 0 || len(f.hooks.trace) != 0 {
				t.Fatalf("invalid tenant entered AuthView: views=%d trace=%v", f.st.views, f.hooks.trace)
			}
		})
	}
}

func TestResolvePrincipalScopeRehydratesCurrentAuthorityInOneAuthView(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	ref := f.sessionRef()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		member, err := as.Memberships().Get(f.ctx, f.member.ID)
		if err != nil {
			return err
		}
		member.Role = RoleAdmin
		updated, err := as.Memberships().Update(f.ctx, member)
		f.member = updated
		return err
	}); err != nil {
		t.Fatalf("change membership after authentication: %v", err)
	}

	f.resetTrace()
	resolved, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("resolve principal evidence: %v", err)
	}
	if f.st.views != 1 {
		t.Fatalf("AuthView calls = %d, want exactly 1", f.st.views)
	}
	if role, ok := resolved.RoleIn(f.tenant); !ok || role != RoleAdmin {
		t.Fatalf("rehydrated role = %q / %t, want current %q", role, ok, RoleAdmin)
	}
	if gotRef, ok := resolved.Ref(); !ok || gotRef != ref {
		t.Fatalf("resolved ref = %+v / %t, want exact %+v", gotRef, ok, ref)
	}
	if resolved.evidence.tenant != f.tenant || resolved.evidence.ref != ref ||
		resolved.evidence.directoryEpoch.Kind != model.DirectoryEpochKind ||
		resolved.evidence.directoryEpoch.ID != model.ID(f.tenant) ||
		!resolved.evidence.observedAt.Equal(f.now) ||
		!resolved.evidence.freshUntil.Equal(f.now.Add(30*time.Minute)) {
		t.Fatalf("private provenance = %+v, want exact tenant/ref/epoch/DB window", resolved.evidence)
	}
	assertTraceExact(t, f.hooks.trace,
		"directory-1", "session-get", "user-get", "membership-list", "group-member-list", "directory-2", "db-clock")
	if len(f.hooks.membershipQueries) == 0 || len(f.hooks.membershipQueries[0].Filters) != 0 {
		t.Fatalf("evidence membership query = %+v, want unfiltered self-validation", f.hooks.membershipQueries)
	}
}

func TestResolvePrincipalScopeRejectsMalformedCurrentMembershipRow(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	ref := f.sessionRef()
	f.hooks.rewriteMemberships = func(rows []model.Membership) []model.Membership {
		out := append([]model.Membership(nil), rows...)
		for i := range out {
			if out[i].UserID == f.user.ID {
				out[i].Version = 0
				return out
			}
		}
		return out
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("malformed membership resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
	}
}

func TestResolvePrincipalScopeRejectsUUIDv4RelevantIdentityRows(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*principalEvidenceFixture)
	}{
		{
			name: "membership durable id",
			configure: func(f *principalEvidenceFixture) {
				badID := principalEvidenceUUIDv4ID(f.t)
				f.hooks.rewriteMemberships = func(rows []model.Membership) []model.Membership {
					out := append([]model.Membership(nil), rows...)
					for i := range out {
						if out[i].ID == f.member.ID {
							out[i].ID = badID
							return out
						}
					}
					f.t.Fatal("fixture lacks selected membership row")
					return nil
				}
			},
		},
		{
			name: "group-member durable id",
			configure: func(f *principalEvidenceFixture) {
				group := f.addGroupWithMember(f.tenant, "principal-evidence-v4-edge", RoleEditor)
				badID := principalEvidenceUUIDv4ID(f.t)
				f.hooks.rewriteGroupMembers = func(rows []model.UserGroupMember) []model.UserGroupMember {
					out := append([]model.UserGroupMember(nil), rows...)
					for i := range out {
						if out[i].UserID == f.user.ID && out[i].GroupID == group.ID {
							out[i].ID = badID
							return out
						}
					}
					f.t.Fatal("fixture lacks selected group-member row")
					return nil
				}
			},
		},
		{
			name: "group target id",
			configure: func(f *principalEvidenceFixture) {
				group := f.addGroupWithMember(f.tenant, "principal-evidence-v4-group", RoleEditor)
				badID := principalEvidenceUUIDv4ID(f.t)
				f.hooks.rewriteGroupMembers = func(rows []model.UserGroupMember) []model.UserGroupMember {
					out := append([]model.UserGroupMember(nil), rows...)
					for i := range out {
						if out[i].UserID == f.user.ID && out[i].GroupID == group.ID {
							out[i].GroupID = badID
							return out
						}
					}
					f.t.Fatal("fixture lacks selected group-member row")
					return nil
				}
				// When the UUIDv7 guard is removed, keep the decorated group lookup
				// internally coherent so the mutated GroupID would otherwise grant
				// its mapped role. The current code must reject the edge before it
				// reaches this seam.
				f.hooks.lookupGroup = func(ctx context.Context, repo store.Repository[model.UserGroup], id model.ID) (model.UserGroup, error) {
					if id != badID {
						return model.UserGroup{}, fmt.Errorf("unexpected group lookup %s", id)
					}
					row, err := repo.Get(ctx, group.ID)
					if err != nil {
						return model.UserGroup{}, err
					}
					row.ID = badID
					return row, nil
				}
			},
		},
		{
			name: "session user id",
			configure: func(f *principalEvidenceFixture) {
				badID := principalEvidenceUUIDv4ID(f.t)
				f.hooks.rewriteSession = func(row model.AuthSession) model.AuthSession {
					if row.ID == f.session.ID {
						row.UserID = badID
					}
					return row
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			ref := f.sessionRef()
			test.configure(f)
			f.resetTrace()
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("ResolvePrincipalScope error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
			if f.st.views != 1 {
				t.Fatalf("AuthView calls = %d, want one for relevant row validation", f.st.views)
			}
		})
	}
}

func TestResolvePrincipalScopeRejectsCrossSubjectMembershipIdentityInEitherOrder(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]model.Membership, model.Membership) []model.Membership
	}{
		{
			name: "foreign row before subject row",
			mutate: func(rows []model.Membership, foreign model.Membership) []model.Membership {
				return append([]model.Membership{foreign}, rows...)
			},
		},
		{
			name: "foreign row after subject row",
			mutate: func(rows []model.Membership, foreign model.Membership) []model.Membership {
				out := append([]model.Membership(nil), rows...)
				return append(out, foreign)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			ref := f.sessionRef()
			f.hooks.rewriteMemberships = func(rows []model.Membership) []model.Membership {
				for _, membership := range rows {
					if membership.ID == f.member.ID {
						foreign := membership
						foreign.UserID = model.NewID()
						return test.mutate(rows, foreign)
					}
				}
				f.t.Fatal("fixture lacks the selected membership row")
				return nil
			}
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("cross-subject ambiguous membership resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
		})
	}
}

func TestResolvePrincipalScopeEvidenceUnavailableFaults(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*principalEvidenceFixture)
		ctx       func(*principalEvidenceFixture) context.Context
		ref       func(*principalEvidenceFixture) PrincipalRef
	}{
		{
			name: "missing capability",
			configure: func(f *principalEvidenceFixture) {
				f.st.wrap = func(as store.AuthScope) store.AuthScope { return principalEvidenceNoCapabilityScope{AuthScope: as} }
			},
		},
		{
			name: "directory generation error despite valid fact",
			configure: func(f *principalEvidenceFixture) {
				if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
					var err error
					f.hooks.epochFact, err = as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(f.ctx, f.tenant)
					return err
				}); err != nil {
					f.t.Fatalf("read fixture epoch: %v", err)
				}
				f.hooks.epochErr = errors.New("directory epoch fault")
			},
		},
		{
			name: "directory generation after error despite valid fact",
			configure: func(f *principalEvidenceFixture) {
				if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
					var err error
					f.hooks.epochFact, err = as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(f.ctx, f.tenant)
					return err
				}); err != nil {
					f.t.Fatalf("read fixture epoch: %v", err)
				}
				f.hooks.epochErr = errors.New("directory epoch after fault")
				f.hooks.epochErrAt = 2
			},
		},
		{
			name: "directory generation changed",
			configure: func(f *principalEvidenceFixture) {
				var first store.AuthorizationFactRef
				if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
					var err error
					first, err = as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(f.ctx, f.tenant)
					return err
				}); err != nil {
					f.t.Fatalf("read fixture epoch: %v", err)
				}
				second := first
				second.Version++
				f.hooks.epochs = []store.AuthorizationFactRef{first, second}
			},
		},
		{
			name:      "database clock error despite timestamp",
			configure: func(f *principalEvidenceFixture) { f.hooks.clockErr = errors.New("clock fault") },
		},
		{
			name:      "database clock zero",
			configure: func(f *principalEvidenceFixture) { f.hooks.now = model.Timestamp{} },
		},
		{
			name: "deadline equal DB clock",
			ctx: func(f *principalEvidenceFixture) context.Context {
				return principalEvidenceFixedDeadlineContext{Context: f.ctx, deadline: f.now}
			},
		},
		{
			name: "deadline before DB clock",
			ctx: func(f *principalEvidenceFixture) context.Context {
				return principalEvidenceFixedDeadlineContext{Context: f.ctx, deadline: f.now.Add(-time.Nanosecond)}
			},
		},
		{
			name: "deadline absent",
			ctx:  func(f *principalEvidenceFixture) context.Context { return f.ctx },
		},
		{
			name: "nil context",
			ctx:  func(*principalEvidenceFixture) context.Context { return nil },
		},
		{
			name: "malformed version-bound ref",
			ref: func(f *principalEvidenceFixture) PrincipalRef {
				return PrincipalRef{kind: KindToken, credentialID: f.token.ID, version: 0}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			if test.configure != nil {
				test.configure(f)
			}
			ref := f.tokenRef()
			if test.ref != nil {
				ref = test.ref(f)
			}
			ctx := f.deadline(30 * time.Minute)
			if test.ctx != nil {
				ctx = test.ctx(f)
			}
			f.resetTrace()
			if _, err := f.a.ResolvePrincipalScope(ctx, ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("ResolvePrincipalScope error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
			if test.name == "deadline equal DB clock" || test.name == "deadline before DB clock" {
				assertTraceOrder(t, f.hooks.trace, "db-clock")
			}
		})
	}
}

func TestResolvePrincipalScopeRequiresExactlyOneAuthViewCallback(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*principalEvidenceFixture)
	}{
		{
			name: "callback omitted",
			configure: func(f *principalEvidenceFixture) {
				f.st.skipCallback = true
			},
		},
		{
			name: "callback repeated",
			configure: func(f *principalEvidenceFixture) {
				f.st.repeatCallback = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			ref := f.tokenRef()
			test.configure(f)
			f.resetTrace()
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("ResolvePrincipalScope error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
			if f.st.views != 1 {
				t.Fatalf("AuthView calls = %d, want exactly 1", f.st.views)
			}
			if test.name == "callback omitted" && len(f.hooks.trace) != 0 {
				t.Fatalf("omitted callback trace = %v, want no reconstruction", f.hooks.trace)
			}
		})
	}
}

func TestResolvePrincipalScopeRejectsMalformedDirectoryEpochFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*store.AuthorizationFactRef, *principalEvidenceFixture)
	}{
		{
			name: "wrong fact kind",
			mutate: func(fact *store.AuthorizationFactRef, _ *principalEvidenceFixture) {
				fact.Kind = model.AuthorizationEpochKind
			},
		},
		{
			name: "wrong fact id",
			mutate: func(fact *store.AuthorizationFactRef, _ *principalEvidenceFixture) {
				fact.ID = model.NewID()
			},
		},
		{
			name: "UUIDv4 fact id",
			mutate: func(fact *store.AuthorizationFactRef, f *principalEvidenceFixture) {
				fact.ID = principalEvidenceUUIDv4ID(f.t)
			},
		},
		{
			name: "uppercase noncanonical UUIDv7 fact id",
			mutate: func(fact *store.AuthorizationFactRef, f *principalEvidenceFixture) {
				fact.ID = model.ID(principalEvidenceNoncanonicalUpperUUIDv7(f.t))
			},
		},
		{
			name: "zero fact version",
			mutate: func(fact *store.AuthorizationFactRef, _ *principalEvidenceFixture) {
				fact.Version = 0
			},
		},
		{
			name: "leased fact",
			mutate: func(fact *store.AuthorizationFactRef, f *principalEvidenceFixture) {
				leased, err := store.NewLeaseFenceAuthorizationFactRef(
					model.DirectoryEpochKind,
					model.ID(f.tenant),
					fact.Version,
					"principal-evidence-test",
					1,
					model.NewTimestamp(f.now.Add(time.Minute)),
				)
				if err != nil {
					f.t.Fatalf("construct leased directory fact: %v", err)
				}
				*fact = leased
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			var fact store.AuthorizationFactRef
			if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
				var err error
				fact, err = as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(f.ctx, f.tenant)
				return err
			}); err != nil {
				t.Fatalf("read fixture directory fact: %v", err)
			}
			test.mutate(&fact, f)
			// Both epoch reads must see the same malformed fact. Otherwise a
			// regression that accepts this fact could still be rejected only by
			// the unrelated G-before/G-after equality fence, leaving the fact
			// validator itself untested.
			f.hooks.epochs = []store.AuthorizationFactRef{fact, fact}
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), f.tokenRef(), f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("malformed directory fact resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
		})
	}
}

func TestResolvePrincipalScopeUsesDBExpiryAndRejectsChangedCredential(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)

	// The app clock is in 2001, so the historical Authenticate path accepts the
	// exact-boundary token. The evidence resolver must instead use its DB witness
	// and reject equality as expired.
	boundaryCred, err := NewCredential(PrefixToken)
	if err != nil {
		t.Fatalf("mint boundary token: %v", err)
	}
	boundary := model.NewTimestamp(f.now)
	var boundaryRow model.APIToken
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		var err error
		boundaryRow, err = as.Tokens().Create(f.ctx, model.APIToken{
			Name: "db-boundary", Selector: boundaryCred.Selector, SecretHash: boundaryCred.SecretHash,
			BoundTenantID: f.tenant, Role: RoleViewer, ExpiresAt: &boundary,
		})
		return err
	}); err != nil {
		t.Fatalf("seed boundary token: %v", err)
	}
	p, err := f.a.Authenticate(f.ctx, boundaryCred.Token)
	if err != nil {
		t.Fatalf("historical auth should see stale app clock and accept: %v", err)
	}
	boundaryRef, ok := p.Ref()
	if !ok || boundaryRef.credentialID != boundaryRow.ID {
		t.Fatalf("boundary token ref = %+v / %t", boundaryRef, ok)
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), boundaryRef, f.tenant); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("DB-expiry resolver error = %v, want ErrUnauthenticated", err)
	}

	ref := f.tokenRef()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(f.ctx, f.token.ID)
		if err != nil {
			return err
		}
		token.Name = "rotated-version"
		_, err = as.Tokens().Update(f.ctx, token)
		return err
	}); err != nil {
		t.Fatalf("rotate token version: %v", err)
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("rotated credential resolver error = %v, want ErrUnauthenticated", err)
	}

	ref = f.tokenRef()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(f.ctx, f.token.ID)
		if err != nil {
			return err
		}
		token.Revoked = true
		_, err = as.Tokens().Update(f.ctx, token)
		return err
	}); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked credential resolver error = %v, want ErrUnauthenticated", err)
	}
}

func TestResolvePrincipalScopeRejectsSameVersionRevokedCredentialRow(t *testing.T) {
	for _, test := range []struct {
		name      string
		ref       func(*principalEvidenceFixture) PrincipalRef
		configure func(*principalEvidenceFixture)
	}{
		{
			name: "token",
			ref:  func(f *principalEvidenceFixture) PrincipalRef { return f.tokenRef() },
			configure: func(f *principalEvidenceFixture) {
				f.hooks.rewriteToken = func(token model.APIToken) model.APIToken {
					if token.ID == f.token.ID {
						token.Revoked = true
					}
					return token
				}
			},
		},
		{
			name: "session",
			ref:  func(f *principalEvidenceFixture) PrincipalRef { return f.sessionRef() },
			configure: func(f *principalEvidenceFixture) {
				f.hooks.rewriteSession = func(session model.AuthSession) model.AuthSession {
					if session.ID == f.session.ID {
						session.Revoked = true
					}
					return session
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			ref := test.ref(f)
			test.configure(f)
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("same-version revoked credential resolver error = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestResolvePrincipalScopeRejectsMalformedZeroCredentialExpiry(t *testing.T) {
	for _, test := range []struct {
		name      string
		ref       func(*principalEvidenceFixture) PrincipalRef
		configure func(*principalEvidenceFixture)
	}{
		{
			name: "token",
			ref:  func(f *principalEvidenceFixture) PrincipalRef { return f.tokenRef() },
			configure: func(f *principalEvidenceFixture) {
				f.hooks.rewriteToken = func(token model.APIToken) model.APIToken {
					if token.ID == f.token.ID {
						zero := model.Timestamp{}
						token.ExpiresAt = &zero
					}
					return token
				}
			},
		},
		{
			name: "session",
			ref:  func(f *principalEvidenceFixture) PrincipalRef { return f.sessionRef() },
			configure: func(f *principalEvidenceFixture) {
				f.hooks.rewriteSession = func(session model.AuthSession) model.AuthSession {
					if session.ID == f.session.ID {
						session.ExpiresAt = model.Timestamp{}
					}
					return session
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			ref := test.ref(f)
			test.configure(f)
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("zero credential expiry resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
		})
	}
}

func TestResolvePrincipalScopeRefusesUnadmittedSessionUntilCredentialFactIsComposed(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	ref, session := f.addUnadmittedSessionRef()
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("unadmitted session resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
	}

	epochBeforeRevoke := f.directoryEpoch(f.tenant)
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		current, err := as.Sessions().Get(f.ctx, session.ID)
		if err != nil {
			return err
		}
		current.Revoked = true
		_, err = as.Sessions().Update(f.ctx, current)
		return err
	}); err != nil {
		t.Fatalf("revoke unadmitted session: %v", err)
	}
	if epochAfterRevoke := f.directoryEpoch(f.tenant); epochAfterRevoke != epochBeforeRevoke {
		t.Fatalf("unadmitted session revoke changed unrelated tenant epoch: before=%+v after=%+v",
			epochBeforeRevoke, epochAfterRevoke)
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked unadmitted session resolver error = %v, want ErrUnauthenticated", err)
	}
}

func TestResolvePrincipalScopeRehydratesOnlyRequestedTenantAuthority(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	tenantB := f.provisionTenant("principal-evidence-b", "principal-evidence-b")
	workspaceA := model.NewID()
	var memberB model.Membership
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		memberA, err := as.Memberships().Get(f.ctx, f.member.ID)
		if err != nil {
			return err
		}
		memberA.WorkspaceID = workspaceA
		memberA, err = as.Memberships().Update(f.ctx, memberA)
		if err != nil {
			return err
		}
		f.member = memberA
		memberB, err = as.Memberships().Create(f.ctx, model.Membership{
			UserID: f.user.ID, TargetTenantID: tenantB, Role: RoleOwner,
		})
		return err
	}); err != nil {
		t.Fatalf("seed tenant-local memberships: %v", err)
	}
	groupA := f.addGroupWithMember(f.tenant, "principal-evidence-a-group", RoleAdmin)
	ref := f.sessionRef()

	resolved, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("resolve tenant A: %v", err)
	}
	if role, ok := resolved.RoleIn(f.tenant); !ok || role != RoleAdmin {
		t.Fatalf("tenant A role = %q / %t, want group-elevated %q", role, ok, RoleAdmin)
	}
	if got, ok := resolved.ConfinedWorkspaceIn(f.tenant); !ok || got != workspaceA {
		t.Fatalf("tenant A confinement = %q / %t, want %q / true", got, ok, workspaceA)
	}
	if groups := resolved.GroupsIn(f.tenant); len(groups) != 1 || groups[0] != groupA.ID.String() {
		t.Fatalf("tenant A groups = %v, want [%s]", groups, groupA.ID)
	}
	if _, ok := resolved.RoleIn(tenantB); ok || len(resolved.GroupsIn(tenantB)) != 0 {
		t.Fatalf("tenant B authority leaked into tenant A reconstruction: role/groups = %q / %v",
			resolved.grants[tenantB], resolved.GroupsIn(tenantB))
	}
	if _, ok := resolved.ConfinedWorkspaceIn(tenantB); ok {
		t.Fatal("tenant B confinement leaked into tenant A reconstruction")
	}
	epochABeforeBMutation := f.directoryEpoch(f.tenant)

	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		current, err := as.Memberships().Get(f.ctx, memberB.ID)
		if err != nil {
			return err
		}
		current.Role = RoleViewer
		_, err = as.Memberships().Update(f.ctx, current)
		return err
	}); err != nil {
		t.Fatalf("mutate unrelated tenant B membership: %v", err)
	}
	if epochAAfterBMutation := f.directoryEpoch(f.tenant); epochAAfterBMutation != epochABeforeBMutation {
		t.Fatalf("tenant B mutation changed tenant A directory witness: before=%+v after=%+v",
			epochABeforeBMutation, epochAAfterBMutation)
	}
	resolved, err = f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("resolve tenant A after tenant B mutation: %v", err)
	}
	if role, ok := resolved.RoleIn(f.tenant); !ok || role != RoleAdmin {
		t.Fatalf("tenant A role after tenant B mutation = %q / %t, want %q", role, ok, RoleAdmin)
	}
	if _, ok := resolved.RoleIn(tenantB); ok {
		t.Fatal("tenant B authority leaked after unrelated tenant B mutation")
	}
}

func TestResolvePrincipalScopeRejectsMalformedDanglingAndCrossTenantGroupClosure(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*principalEvidenceFixture, model.UserGroup)
	}{
		{
			name: "malformed group row",
			configure: func(f *principalEvidenceFixture, group model.UserGroup) {
				f.hooks.rewriteGroup = func(g model.UserGroup) model.UserGroup {
					if g.ID == group.ID {
						g.Version = 0
					}
					return g
				}
			},
		},
		{
			name: "dangling parent group",
			configure: func(f *principalEvidenceFixture, group model.UserGroup) {
				f.hooks.rewriteGroup = func(g model.UserGroup) model.UserGroup {
					if g.ID == group.ID {
						g.ParentGroupID = model.NewID()
					}
					return g
				}
			},
		},
		{
			name: "cross tenant parent group",
			configure: func(f *principalEvidenceFixture, group model.UserGroup) {
				foreignTenant := f.provisionTenant("principal-evidence-foreign", "principal-evidence-foreign")
				foreignGroup := f.addGroupWithMember(foreignTenant, "principal-evidence-foreign-group", "")
				f.hooks.rewriteGroup = func(g model.UserGroup) model.UserGroup {
					if g.ID == group.ID {
						g.ParentGroupID = foreignGroup.ID
					}
					return g
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			group := f.addGroupWithMember(f.tenant, "principal-evidence-closure", RoleEditor)
			ref := f.sessionRef()
			test.configure(f, group)
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("group closure resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
		})
	}
}

func TestResolvePrincipalScopeRejectsAmbiguousGroupMemberRowIdentity(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	groupA := f.addGroupWithMember(f.tenant, "principal-evidence-edge-a", RoleEditor)
	groupB := f.addGroup(f.tenant, "principal-evidence-edge-b", RoleOwner, false)
	ref := f.sessionRef()
	f.hooks.rewriteGroupMembers = func(rows []model.UserGroupMember) []model.UserGroupMember {
		out := append([]model.UserGroupMember(nil), rows...)
		for _, edge := range rows {
			if edge.UserID == f.user.ID && edge.GroupID == groupA.ID {
				ambiguous := edge
				ambiguous.GroupID = groupB.ID
				return append(out, ambiguous)
			}
		}
		f.t.Fatal("fixture lacks the selected group-member row")
		return nil
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("ambiguous group-member identity resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
	}
}

func TestResolvePrincipalScopeRejectsCrossSubjectGroupMemberIdentityInEitherOrder(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]model.UserGroupMember, model.UserGroupMember) []model.UserGroupMember
	}{
		{
			name: "foreign row before subject row",
			mutate: func(rows []model.UserGroupMember, foreign model.UserGroupMember) []model.UserGroupMember {
				return append([]model.UserGroupMember{foreign}, rows...)
			},
		},
		{
			name: "foreign row after subject row",
			mutate: func(rows []model.UserGroupMember, foreign model.UserGroupMember) []model.UserGroupMember {
				out := append([]model.UserGroupMember(nil), rows...)
				return append(out, foreign)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			group := f.addGroupWithMember(f.tenant, "principal-evidence-cross-subject-edge", RoleEditor)
			ref := f.sessionRef()
			f.hooks.rewriteGroupMembers = func(rows []model.UserGroupMember) []model.UserGroupMember {
				for _, edge := range rows {
					if edge.UserID == f.user.ID && edge.GroupID == group.ID {
						foreign := edge
						foreign.UserID = model.NewID()
						return test.mutate(rows, foreign)
					}
				}
				f.t.Fatal("fixture lacks the selected group-member row")
				return nil
			}
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("cross-subject ambiguous group-member resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
		})
	}
}

func TestResolvePrincipalScopeClipsContextCredentialAndElevatedAALWindows(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	credentialUntil := model.NewTimestamp(f.now.Add(3 * time.Minute))
	credentialCred, err := NewCredential(PrefixToken)
	if err != nil {
		t.Fatalf("mint credential-clipped token: %v", err)
	}
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		_, err := as.Tokens().Create(f.ctx, model.APIToken{
			Name: "credential-clipped", Selector: credentialCred.Selector, SecretHash: credentialCred.SecretHash,
			BoundTenantID: f.tenant, Role: RoleViewer, ExpiresAt: &credentialUntil,
		})
		return err
	}); err != nil {
		t.Fatalf("seed credential-clipped token: %v", err)
	}
	credentialPrincipal, err := f.a.Authenticate(f.ctx, credentialCred.Token)
	if err != nil {
		t.Fatalf("authenticate credential-clipped token: %v", err)
	}
	credentialRef, ok := credentialPrincipal.Ref()
	if !ok {
		t.Fatal("credential-clipped token has no ref")
	}
	credentialResolved, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), credentialRef, f.tenant)
	if err != nil {
		t.Fatalf("resolve credential-clipped token: %v", err)
	}
	if !credentialResolved.evidence.freshUntil.Equal(credentialUntil.Time()) {
		t.Fatalf("credential-clipped window = %s, want %s", credentialResolved.evidence.freshUntil, credentialUntil.Time())
	}

	elevatedUntil := model.NewTimestamp(f.now.Add(5 * time.Minute))
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		session, err := as.Sessions().Get(f.ctx, f.session.ID)
		if err != nil {
			return err
		}
		session.AAL = AAL3
		session.AMR = []string{"pwd", "webauthn"}
		session.AALExpiresAt = &elevatedUntil
		updated, err := as.Sessions().Update(f.ctx, session)
		f.session = updated
		return err
	}); err != nil {
		t.Fatalf("elevate fixture session: %v", err)
	}
	ref := f.sessionRef()

	resolved, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("resolve elevated principal: %v", err)
	}
	if resolved.AAL != AAL3 || !resolved.evidence.freshUntil.Equal(elevatedUntil.Time()) {
		t.Fatalf("elevated result AAL/window = %d/%s, want %d/%s", resolved.AAL,
			resolved.evidence.freshUntil, AAL3, elevatedUntil.Time())
	}

	resolved, err = f.a.ResolvePrincipalScope(f.deadline(2*time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("resolve caller-clipped principal: %v", err)
	}
	if !resolved.evidence.freshUntil.Equal(f.now.Add(2 * time.Minute)) {
		t.Fatalf("caller-clipped window = %s, want %s", resolved.evidence.freshUntil, f.now.Add(2*time.Minute))
	}
}

func TestResolvePrincipalScopeDegradesExpiredElevatedAAL(t *testing.T) {
	for _, test := range []struct {
		name   string
		offset time.Duration
	}{
		{name: "at DB boundary", offset: 0},
		{name: "before DB boundary", offset: -time.Nanosecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			expiredElevatedUntil := model.NewTimestamp(f.now.Add(test.offset))
			if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
				session, err := as.Sessions().Get(f.ctx, f.session.ID)
				if err != nil {
					return err
				}
				session.AAL = AAL3
				session.AMR = []string{"pwd", "webauthn"}
				session.AALExpiresAt = &expiredElevatedUntil
				f.session, err = as.Sessions().Update(f.ctx, session)
				return err
			}); err != nil {
				t.Fatalf("seed expired elevated session: %v", err)
			}
			ref := f.sessionRef()
			resolved, err := f.a.ResolvePrincipalScope(f.deadline(15*time.Minute), ref, f.tenant)
			if err != nil {
				t.Fatalf("resolve expired elevated session: %v", err)
			}
			if resolved.AAL != AAL1 {
				t.Fatalf("expired elevated AAL = %d, want downgrade to %d", resolved.AAL, AAL1)
			}
			if !resolved.evidence.freshUntil.Equal(f.now.Add(15 * time.Minute)) {
				t.Fatalf("expired elevated window = %s, want caller deadline %s",
					resolved.evidence.freshUntil, f.now.Add(15*time.Minute))
			}
		})
	}
}

func TestResolvePrincipalScopeRefusesGlobalAndRecoversPurposeTokenBindings(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	sessionRef := f.sessionRef()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		user, err := as.Users().Get(f.ctx, f.user.ID)
		if err != nil {
			return err
		}
		user.IsSuperadmin = true
		_, err = as.Users().Update(f.ctx, user)
		return err
	}); err != nil {
		t.Fatalf("mark session user superadmin: %v", err)
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), sessionRef, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("global session scope error = %v, want ErrPrincipalEvidenceUnavailable", err)
	}

	globalCred, err := NewCredential(PrefixToken)
	if err != nil {
		t.Fatalf("mint global token: %v", err)
	}
	var global model.APIToken
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		var err error
		global, err = as.Tokens().Create(f.ctx, model.APIToken{
			Name: "global", Selector: globalCred.Selector, SecretHash: globalCred.SecretHash, IsSuperadmin: true,
		})
		return err
	}); err != nil {
		t.Fatalf("seed global token: %v", err)
	}
	p, err := f.a.Authenticate(f.ctx, globalCred.Token)
	if err != nil {
		t.Fatalf("authenticate global token: %v", err)
	}
	globalRef, ok := p.Ref()
	if !ok || globalRef.credentialID != global.ID {
		t.Fatalf("global token ref = %+v / %t", globalRef, ok)
	}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), globalRef, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("global token scope error = %v, want ErrPrincipalEvidenceUnavailable", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*model.APIToken)
	}{
		{name: "parent token", mutate: func(token *model.APIToken) { token.ParentTokenID = f.token.ID }},
		{name: "audience", mutate: func(token *model.APIToken) { token.Audience = "https://pep.example.test" }},
		{name: "act-as", mutate: func(token *model.APIToken) { token.ActAsUserID = f.user.ID }},
		{name: "agent OBO", mutate: func(token *model.APIToken) { token.AgentRef = "agent:" + model.NewID().String() }},
	} {
		t.Run("delegated ordinary "+test.name, func(t *testing.T) {
			exchangedCred, err := NewCredential(PrefixToken)
			if err != nil {
				t.Fatalf("mint exchanged token: %v", err)
			}
			candidate := model.APIToken{
				Name: "delegated-looking-" + test.name, Selector: exchangedCred.Selector, SecretHash: exchangedCred.SecretHash,
				BoundTenantID: f.tenant, Role: RoleViewer,
			}
			test.mutate(&candidate)
			var exchanged model.APIToken
			if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
				var err error
				exchanged, err = as.Tokens().Create(f.ctx, candidate)
				return err
			}); err != nil {
				t.Fatalf("seed delegated-looking ordinary token: %v", err)
			}
			p, err = f.a.Authenticate(f.ctx, exchangedCred.Token)
			if err != nil {
				t.Fatalf("historical ordinary authentication: %v", err)
			}
			if _, ok := p.Ref(); ok {
				t.Fatal("delegated-looking ordinary token unexpectedly exposes a principal ref")
			}
			// A caller outside auth cannot fabricate this opaque ref. Constructing it
			// here exercises the resolver's second defensive boundary against a row
			// changed from ordinary to delegated after a prior successful authentication.
			exchangedRef := PrincipalRef{kind: KindToken, credentialID: p.CredID, version: exchanged.Version}
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), exchangedRef, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("delegated-looking token scope error = %v, want ErrPrincipalEvidenceUnavailable", err)
			}
		})
	}

	system, err := NewSystemOperator("test:principal-evidence", "purpose resolver coverage")
	if err != nil {
		t.Fatalf("system operator: %v", err)
	}
	live := NewAuthenticator(f.st, model.SystemClock{})
	work, err := live.IssueWorkSessionCredential(f.ctx, system, WorkSessionCredentialSpec{
		Tenant: f.tenant, SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatalf("issue work credential: %v", err)
	}
	communication, err := live.IssueCommunicationSessionCredential(f.ctx, system, CommunicationSessionCredentialSpec{
		Tenant: f.tenant, WorkspaceID: model.NewID(), SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatalf("issue communication credential: %v", err)
	}
	for name, raw := range map[string]string{"work": work.Token, "communication": communication.Token} {
		t.Run(name, func(t *testing.T) {
			principal, err := f.a.Authenticate(f.ctx, raw)
			if err != nil {
				t.Fatalf("authenticate %s: %v", name, err)
			}
			ref, ok := principal.Ref()
			if !ok {
				t.Fatalf("%s lacks authenticated principal ref", name)
			}
			resolved, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant)
			if err != nil {
				t.Fatalf("resolve %s: %v", name, err)
			}
			switch name {
			case "work":
				if !resolved.IsWorkSessionCredential() {
					t.Fatal("work binding was not reconstructed")
				}
			case "communication":
				if !resolved.IsCommunicationSessionCredential() {
					t.Fatal("communication binding was not reconstructed")
				}
			}
		})
	}
}

func TestResolvePrincipalScopeRejectsWrongTenantCredentialBindings(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	otherTenant := f.provisionTenant("principal-evidence-other", "principal-evidence-other")
	system, err := NewSystemOperator("test:principal-evidence", "wrong tenant binding coverage")
	if err != nil {
		t.Fatalf("system operator: %v", err)
	}
	live := NewAuthenticator(f.st, model.SystemClock{})
	work, err := live.IssueWorkSessionCredential(f.ctx, system, WorkSessionCredentialSpec{
		Tenant: f.tenant, SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatalf("issue work credential: %v", err)
	}
	communication, err := live.IssueCommunicationSessionCredential(f.ctx, system, CommunicationSessionCredentialSpec{
		Tenant: f.tenant, WorkspaceID: model.NewID(), SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatalf("issue communication credential: %v", err)
	}
	for name, raw := range map[string]string{
		"ordinary":      f.tokRaw,
		"work":          work.Token,
		"communication": communication.Token,
	} {
		t.Run(name, func(t *testing.T) {
			principal, err := f.a.Authenticate(f.ctx, raw)
			if err != nil {
				t.Fatalf("authenticate %s token: %v", name, err)
			}
			ref, ok := principal.Ref()
			if !ok {
				t.Fatalf("%s token lacks principal ref", name)
			}
			if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, otherTenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("wrong-tenant %s resolver error = %v, want ErrPrincipalEvidenceUnavailable", name, err)
			}
		})
	}
}

func TestPrincipalEvidenceDoesNotExposeTokenExchangeCredentialRef(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	caller, err := f.a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		t.Fatalf("authenticate exchange caller: %v", err)
	}
	result, err := f.a.ExchangeToken(f.ctx, caller, ExchangeRequest{
		SubjectToken:     f.sessRaw,
		SubjectTokenType: TokenTypeAccessToken,
	})
	if err != nil {
		t.Fatalf("exchange session subject without target/actor/agent: %v", err)
	}
	if result.Stored.Scope == "" || !result.Stored.ParentTokenID.IsZero() ||
		result.Stored.Audience != "" || !result.Stored.ActAsUserID.IsZero() || result.Stored.AgentRef != "" {
		t.Fatalf("exchange fixture did not isolate Scope-only delegation marker: %+v", result.Stored)
	}
	child, err := f.a.Authenticate(f.ctx, result.AccessToken)
	if err != nil {
		t.Fatalf("historical exchanged-token authentication: %v", err)
	}
	if _, ok := child.Ref(); ok {
		t.Fatal("Scope-marked exchanged token unexpectedly exposes a credential ref")
	}
	ref := PrincipalRef{kind: KindToken, credentialID: result.Stored.ID, version: result.Stored.Version}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("Scope-marked exchanged token resolver error = %v, want ErrPrincipalEvidenceUnavailable", err)
	}
}

func assertTraceOrder(t *testing.T, trace []string, want ...string) {
	t.Helper()
	from := 0
	for _, label := range want {
		found := -1
		for i := from; i < len(trace); i++ {
			if trace[i] == label {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("trace %v does not contain ordered %q (wanted %v)", trace, label, want)
		}
		from = found + 1
	}
}

func assertTraceExact(t *testing.T, trace []string, want ...string) {
	t.Helper()
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want exactly %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace = %v, want exactly %v", trace, want)
		}
	}
}
