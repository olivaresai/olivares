// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type mutationAuthorityFixture struct {
	*principalEvidenceFixture
	p      Principal
	az     *Authorizer
	now    time.Time
	scoped *principalAuthorityScopedProducer
	lease  store.AuthorizationFactRef
}

// newMutationAuthorityFixture reconstructs a real human principal from the
// existing SQLite evidence fixture and pins the authorizer clock inside its
// window. The scoped contribution carries a leased fact so every lease
// coordinate is exercised by the digest and the bundle.
func newMutationAuthorityFixture(t *testing.T) *mutationAuthorityFixture {
	t.Helper()
	f, p := resolvedPrincipalAuthorityEvidence(t)
	now := p.evidence.observedAt.Add(time.Second)
	lease, err := store.NewLeaseFenceAuthorizationFactRef(
		"test.lease", model.NewID(), 7, "subject", 9, model.NewTimestamp(now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	scoped := &principalAuthorityScopedProducer{
		decision: principalAuthorityCleanScoped(p.evidence.observedAt, p.evidence.freshUntil, lease),
	}
	return &mutationAuthorityFixture{
		principalEvidenceFixture: f, p: p, now: now, scoped: scoped, lease: lease,
		az: NewAuthorizer(nil, WithScopedGrants(scoped), WithClock(func() time.Time { return now })),
	}
}

// callerCtx is the tightest legal caller lifetime: a finite deadline exactly at
// the evidence window's end, which the new entry point must accept.
func (m *mutationAuthorityFixture) callerCtx() context.Context {
	m.t.Helper()
	ctx, cancel := context.WithDeadline(m.ctx, m.p.evidence.freshUntil)
	m.t.Cleanup(cancel)
	return ctx
}

func (m *mutationAuthorityFixture) request() Request {
	return principalAuthorityEvidenceRequest(m.p, m.tenant)
}

func TestRouteMutationAuthorizationProducesTheCompleteBundle(t *testing.T) {
	m := newMutationAuthorityFixture(t)
	req := m.request()
	a, err := m.az.AuthorizeRouteMutation(m.callerCtx(), req)
	if err != nil {
		t.Fatalf("human mutation authorization: %v", err)
	}
	bundle, err := a.AuthorityFor(m.now, req)
	if err != nil {
		t.Fatalf("AuthorityFor: %v", err)
	}
	want := store.UserAuthorityFactRef{UserID: m.user.ID, Version: m.p.evidence.userAuthority.Version}
	if want.Version < 1 {
		t.Fatal("fixture produced no positive User fence")
	}
	if !slices.Equal(bundle.UserAuthorities, []store.UserAuthorityFactRef{want}) {
		t.Fatalf("UserAuthorities = %+v, want the real User fence %+v", bundle.UserAuthorities, want)
	}
	if !slices.Contains(bundle.Facts, m.p.evidence.directoryEpoch) {
		t.Fatalf("Facts = %+v, want the tenant directory fence", bundle.Facts)
	}
	// The complete lease coordinates survive extraction, not merely the identity.
	i := slices.IndexFunc(bundle.Facts, func(f store.AuthorizationFactRef) bool { return f.ID == m.lease.ID })
	if i < 0 || bundle.Facts[i] != m.lease {
		t.Fatalf("lease coordinates lost: %+v", bundle.Facts)
	}
	if !slices.Equal(bundle.Facts, a.witness.Decision.Facts) {
		t.Fatal("returned facts are not the authorized canonical fact set")
	}

	// A token reaches the same Interface and gets no User fence at all.
	token, err := m.a.ResolvePrincipalScope(m.deadline(30*time.Minute), m.tokenRef(), m.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if token.evidence.authorityMode != principalTokenDirectoryOnly {
		t.Fatal("fixture token is not directory-only")
	}
	tokenNow := token.evidence.observedAt.Add(time.Second)
	tokenAz := NewAuthorizer(nil,
		WithScopedGrants(&principalAuthorityScopedProducer{
			decision: principalAuthorityCleanScoped(token.evidence.observedAt, token.evidence.freshUntil),
		}),
		WithClock(func() time.Time { return tokenNow }))
	tokenCtx, cancel := context.WithDeadline(m.ctx, token.evidence.freshUntil)
	t.Cleanup(cancel)
	tokenReq := principalAuthorityEvidenceRequest(token, m.tenant)
	tokenAuth, err := tokenAz.AuthorizeRouteMutation(tokenCtx, tokenReq)
	if err != nil {
		t.Fatalf("token mutation authorization: %v", err)
	}
	tokenBundle, err := tokenAuth.AuthorityFor(tokenNow, tokenReq)
	if err != nil {
		t.Fatalf("token AuthorityFor: %v", err)
	}
	if len(tokenBundle.UserAuthorities) != 0 {
		t.Fatalf("token acquired a User fence: %+v", tokenBundle.UserAuthorities)
	}
	if len(tokenBundle.Facts) == 0 {
		t.Fatal("token lost its tenant facts")
	}
}

func TestRouteMutationAuthorizationEvaluatesOnceAndKeepsTheAlgebra(t *testing.T) {
	m := newMutationAuthorityFixture(t)
	req := m.request()
	if _, err := m.az.AuthorizeRouteMutation(m.callerCtx(), req); err != nil {
		t.Fatal(err)
	}
	if m.scoped.typedCalls != 1 || m.scoped.legacyCalls != 0 {
		t.Fatalf("evaluations = %d typed / %d legacy, want exactly one typed",
			m.scoped.typedCalls, m.scoped.legacyCalls)
	}

	// Step-up is a precondition of authentication: it answers BEFORE the engine is
	// consulted, so a principal who must step up learns nothing about the verdict.
	stepUp := req
	stepUp.Route.MinimumAAL = m.p.AAL + 1
	if _, err := m.az.AuthorizeRouteMutation(m.callerCtx(), stepUp); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("step-up error = %v, want ErrStepUpRequired", err)
	}
	if m.scoped.typedCalls != 1 {
		t.Fatalf("step-up performed %d evaluations, want zero", m.scoped.typedCalls-1)
	}

	// Denied and undecided keep their distinct remedies, and neither issues.
	broken := newMutationAuthorityFixture(t)
	broken.scoped.decision = principalAuthorityBrokenScoped(
		broken.p.evidence.observedAt, broken.p.evidence.freshUntil)
	denied, err := broken.az.AuthorizeRouteMutation(broken.callerCtx(), broken.request())
	if !errors.Is(err, ErrRouteDenied) || errors.Is(err, ErrRouteUndecided) || denied.issued {
		t.Fatalf("denial = %v (issued=%v), want ErrRouteDenied", err, denied.issued)
	}
	unknown := newMutationAuthorityFixture(t)
	unknown.scoped.err = errors.New("engine unavailable")
	undecided, err := unknown.az.AuthorizeRouteMutation(unknown.callerCtx(), unknown.request())
	if !errors.Is(err, ErrRouteUndecided) || errors.Is(err, ErrRouteDenied) || undecided.issued {
		t.Fatalf("unavailable engine = %v (issued=%v), want ErrRouteUndecided", err, undecided.issued)
	}

	// No authorizer, no principal, and no reachable synthetic shape.
	var absent *Authorizer
	if _, err := absent.AuthorizeRouteMutation(m.callerCtx(), req); !errors.Is(err, ErrAuthorizerUnavailable) {
		t.Fatalf("nil authorizer = %v", err)
	}
	for name, mutate := range map[string]func(*Request){
		"zero-principal":   func(r *Request) { r.Principal = Principal{} },
		"fabricated-seal":  func(r *Request) { r.Principal.evidence.seal[0] ^= 1 },
		"fabricated-fence": func(r *Request) { r.Principal.evidence.userAuthority.Version++ },
		"system-actor":     func(r *Request) { r.Principal.localSystem = true },
		"superadmin":       func(r *Request) { r.Principal.Superadmin = true },
		"local-subject":    func(r *Request) { r.Principal.localSubject = "operator" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := m.request()
			bad.Principal = cloneEvidencePrincipal(m.p)
			mutate(&bad)
			// The ordinary algebra owns WHICH refusal (a zero principal is an
			// ordinary denial, an unsealed one is undecided); MA1 owes that no
			// value is issued and that the empty value stays inert.
			got, err := m.az.AuthorizeRouteMutation(m.callerCtx(), bad)
			if got.issued || !(errors.Is(err, ErrRouteUndecided) || errors.Is(err, ErrRouteDenied) ||
				errors.Is(err, ErrScopedGrantRequired)) {
				t.Fatalf("err = %v (issued=%v), want a route refusal and no issuance", err, got.issued)
			}
			if _, err := got.AuthorityFor(m.now, bad); !errors.Is(err, ErrRouteUndecided) {
				t.Fatalf("empty value answered: %v", err)
			}
		})
	}
	// The empty value refuses even the question it would otherwise have answered.
	if _, err := (RouteMutationAuthorization{}).AuthorityFor(m.now, req); !errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("empty authorization answered: %v", err)
	}
}

func TestRouteMutationAuthorizationBindsTheWholeQuestion(t *testing.T) {
	m := newMutationAuthorityFixture(t)
	req := m.request()
	a, err := m.az.AuthorizeRouteMutation(m.callerCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AuthorityFor(m.now, req); err != nil {
		t.Fatalf("the authorized question was refused: %v", err)
	}
	for name, transplant := range map[string]func(*Request){
		"permission":      func(r *Request) { r.Permission = "agent:write" },
		"cedar-action":    func(r *Request) { r.Route.CedarAction = "agent:update" },
		"tenant":          func(r *Request) { r.Tenant = model.TenantID(model.NewID()) },
		"resource-kind":   func(r *Request) { r.Resource.Kind = "other" },
		"resource-id":     func(r *Request) { r.Resource.ID = model.NewID().String() },
		"workspace":       func(r *Request) { r.Resource.WorkspaceID = model.NewID() },
		"sensitivity":     func(r *Request) { r.Resource.Sensitivity = "restricted" },
		"scoped-grant":    func(r *Request) { r.Route.RequireScopedGrant = !r.Route.RequireScopedGrant },
		"role-floor":      func(r *Request) { r.Route.RBACMinimumRole = RoleAdmin },
		"agent-groups":    func(r *Request) { r.Route.SessionInheritsAgentGroups = true },
		"route-aal-floor": func(r *Request) { r.Route.MinimumAAL = m.p.AAL },
	} {
		t.Run(name, func(t *testing.T) {
			other := m.request()
			transplant(&other)
			if other.Tenant == req.Tenant && questionDigest(other) == questionDigest(req) {
				t.Fatal("the transplant did not change the question")
			}
			if _, err := a.AuthorityFor(m.now, other); !errors.Is(err, ErrRouteUndecided) {
				t.Fatalf("witness answered another question: %v", err)
			}
		})
	}

	// Same human, different provenance: an equal identity is not equal authority.
	for name, reseal := range map[string]func(*Principal){
		"User-version": func(p *Principal) { p.evidence.userAuthority.Version++ },
		"directory-epoch": func(p *Principal) {
			p.evidence.directoryEpoch.Version++
		},
	} {
		t.Run("reconstruction-"+name, func(t *testing.T) {
			fresh := m.request()
			fresh.Principal = cloneEvidencePrincipal(m.p)
			reseal(&fresh.Principal)
			seal, err := computePrincipalAuthoritySeal(fresh.Principal)
			if err != nil {
				t.Fatal(err)
			}
			fresh.Principal.evidence.seal = seal
			if fresh.Principal.UserID != m.p.UserID {
				t.Fatal("the fixture changed the human identity, not its provenance")
			}
			if _, err := a.AuthorityFor(m.now, fresh); !errors.Is(err, ErrRouteUndecided) {
				t.Fatalf("a different sealed reconstruction reused the old authorization: %v", err)
			}
		})
	}
	// A principal whose seal no longer verifies cannot present the authorization.
	broken := m.request()
	broken.Principal = cloneEvidencePrincipal(m.p)
	broken.Principal.evidence.seal[0] ^= 1
	if _, err := a.AuthorityFor(m.now, broken); !errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("unsealed principal presented the authorization: %v", err)
	}
}

func TestRouteMutationAuthorizationLifetimeIsFinite(t *testing.T) {
	m := newMutationAuthorityFixture(t)
	req := m.request()

	canceled, cancel := context.WithDeadline(m.ctx, m.p.evidence.freshUntil)
	cancel()
	if _, err := m.az.AuthorizeRouteMutation(canceled, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context = %v, want context.Canceled", err)
	}
	if m.scoped.typedCalls != 0 {
		t.Fatal("a canceled caller reached the engine")
	}
	if _, err := m.az.AuthorizeRouteMutation(context.Background(), req); !errors.Is(err, ErrRouteUndecided) {
		t.Fatal("a caller with no deadline obtained an unbounded authorization")
	}
	expired, cancelExpired := context.WithDeadline(m.ctx, m.now.Add(-time.Second))
	t.Cleanup(cancelExpired)
	if _, err := m.az.AuthorizeRouteMutation(expired, req); !errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("an expired caller obtained an authorization: %v", err)
	}
	// A deadline the evidence window outlives cannot be widened by issuing anyway.
	short, cancelShort := context.WithDeadline(m.ctx, m.p.evidence.freshUntil.Add(-time.Nanosecond))
	t.Cleanup(cancelShort)
	if _, err := m.az.AuthorizeRouteMutation(short, req); !errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("the evidence window outlived the caller and still issued: %v", err)
	}
	if m.scoped.typedCalls == 0 {
		t.Fatal("the short-deadline case never reached the evaluation it is meant to discard")
	}

	a, err := m.az.AuthorizeRouteMutation(m.callerCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	observedAt, freshUntil := a.witness.Decision.ObservedAt, a.witness.Decision.FreshUntil
	if !freshUntil.After(observedAt) {
		t.Fatal("the fixture window is empty")
	}
	for name, at := range map[string]time.Time{
		"before-observed": observedAt.Add(-time.Nanosecond),
		"at-fresh-until":  freshUntil,
		"after-window":    freshUntil.Add(time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := a.AuthorityFor(at, req); !errors.Is(err, ErrRouteUndecided) {
				t.Fatalf("half-open window accepted %s: %v", name, err)
			}
		})
	}
	for name, at := range map[string]time.Time{
		"at-observed":        observedAt,
		"last-valid-instant": freshUntil.Add(-time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := a.AuthorityFor(at, req); err != nil {
				t.Fatalf("half-open window refused %s: %v", name, err)
			}
		})
	}
	// The credential's own lifetime still bounds the window this value captured.
	if freshUntil.After(m.p.evidence.freshUntil) {
		t.Fatal("the authorization extended the reconstruction's lifetime")
	}
}

func TestRouteMutationAuthorizationIsImmutableEvidence(t *testing.T) {
	m := newMutationAuthorityFixture(t)
	req := m.request()
	a, err := m.az.AuthorizeRouteMutation(m.callerCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.AuthorityFor(m.now, req)
	if err != nil {
		t.Fatal(err)
	}
	first.Facts[0].Version++
	first.UserAuthorities[0].Version++
	first.Facts = append(first.Facts, first.Facts[0])
	second, err := a.AuthorityFor(m.now, req)
	if err != nil {
		t.Fatalf("edited return changed the value: %v", err)
	}
	if second.Facts[0].Version == first.Facts[0].Version ||
		second.UserAuthorities[0] != m.p.evidence.userAuthority {
		t.Fatal("returned slices alias the retained authorization")
	}
	// A caller-owned map cannot reach the retained value. Issuance routes the
	// request through the existing cloneEvidenceRequest helper, which the read path
	// already covers; this case supplies the map that helper must copy and then
	// edits it, so the assertion is about isolation and not merely about the
	// fixture carrying no map.
	aliased := m.request()
	aliased.Resource.Extra = map[string]string{"seat": "one"}
	withMap, err := m.az.AuthorizeRouteMutation(m.callerCtx(), aliased)
	if err != nil {
		t.Fatalf("authorize with a caller-owned map: %v", err)
	}
	if _, err := withMap.AuthorityFor(m.now, aliased); err != nil {
		t.Fatalf("the map-bearing question was refused: %v", err)
	}
	aliased.Resource.Extra["seat"] = "two"
	if _, err := withMap.AuthorityFor(m.now, aliased); !errors.Is(err, ErrRouteUndecided) {
		t.Fatal("editing the caller's map did not change the question")
	}
	aliased.Resource.Extra["seat"] = "one"
	if _, err := withMap.AuthorityFor(m.now, aliased); err != nil {
		t.Fatalf("the retained value tracked the caller's map instead of its copy: %v", err)
	}

	for name, change := range map[string]func(*RouteMutationAuthorization){
		"issuance":       func(v *RouteMutationAuthorization) { v.issued = false },
		"outcome":        func(v *RouteMutationAuthorization) { v.witness.Decision.Outcome = EvidenceDeny },
		"minting":        func(v *RouteMutationAuthorization) { v.witness.minted = false },
		"mode":           func(v *RouteMutationAuthorization) { v.authorityMode = principalTokenDirectoryOnly },
		"unknown-mode":   func(v *RouteMutationAuthorization) { v.authorityMode = 255 },
		"User-identity":  func(v *RouteMutationAuthorization) { v.userAuthority.UserID = model.NewID() },
		"User-version":   func(v *RouteMutationAuthorization) { v.userAuthority.Version++ },
		"zero-version":   func(v *RouteMutationAuthorization) { v.userAuthority.Version = 0 },
		"principal-seal": func(v *RouteMutationAuthorization) { v.principalSeal[0] ^= 1 },
		"tenant":         func(v *RouteMutationAuthorization) { v.tenant = model.TenantID(model.NewID()) },
		"digest":         func(v *RouteMutationAuthorization) { v.authorityDigest[0] ^= 1 },
		"evidence-digest": func(v *RouteMutationAuthorization) {
			v.witness.EvidenceDigest[0] ^= 1
		},
		"question-digest": func(v *RouteMutationAuthorization) { v.witness.QuestionDigest[0] ^= 1 },
		"fact-version": func(v *RouteMutationAuthorization) {
			v.witness.Decision.Facts = slices.Clone(v.witness.Decision.Facts)
			v.witness.Decision.Facts[0].Version++
		},
		"fact-order": func(v *RouteMutationAuthorization) {
			v.witness.Decision.Facts = slices.Clone(v.witness.Decision.Facts)
			slices.Reverse(v.witness.Decision.Facts)
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := a
			change(&v)
			if _, err := v.AuthorityFor(m.now, req); !errors.Is(err, ErrRouteUndecided) {
				t.Fatalf("a changed authorization verified: %v", err)
			}
		})
	}

	// Every lease coordinate is retained and every change to one is detected.
	for _, name := range []string{"presence", "subject", "fence", "deadline"} {
		t.Run("lease-"+name, func(t *testing.T) {
			subject, fence, until := "subject", int64(9), m.lease
			changed := store.AuthorizationFactRef{Kind: m.lease.Kind, ID: m.lease.ID, Version: m.lease.Version}
			if name != "presence" {
				gotSubject, gotFence, deadline, ok := until.LeaseFenceWitness()
				if !ok || gotSubject != subject || gotFence != fence {
					t.Fatal("the fixture lease lost its coordinates")
				}
				switch name {
				case "subject":
					subject = "changed"
				case "fence":
					fence++
				case "deadline":
					deadline = model.NewTimestamp(deadline.Time().Add(time.Second))
				}
				var err error
				changed, err = store.NewLeaseFenceAuthorizationFactRef(
					m.lease.Kind, m.lease.ID, m.lease.Version, subject, fence, deadline)
				if err != nil {
					t.Fatal(err)
				}
			}
			v := a
			v.witness.Decision.Facts = slices.Clone(a.witness.Decision.Facts)
			i := slices.IndexFunc(v.witness.Decision.Facts,
				func(f store.AuthorizationFactRef) bool { return f.ID == m.lease.ID })
			if i < 0 {
				t.Fatal("the leased fact is not in the authorized set")
			}
			v.witness.Decision.Facts[i] = changed
			if evidenceDigest(v.witness) == a.witness.EvidenceDigest {
				t.Fatal("the ordinary digest omitted a lease coordinate")
			}
			if _, err := v.AuthorityFor(m.now, req); !errors.Is(err, ErrRouteUndecided) {
				t.Fatalf("a changed lease coordinate verified: %v", err)
			}
		})
	}
}

// TestRouteMutationAuthorityDigestVector pins the integrity codec to bytes derived
// OUTSIDE this package's writer, so a reordered, retyped or silently widened field
// fails here instead of agreeing with itself.
func TestRouteMutationAuthorityDigestVector(t *testing.T) {
	var evidence, seal [32]byte
	for i := range evidence {
		evidence[i], seal[i] = byte(i), byte(255-i)
	}
	const userID = "01994f2a-1234-7abc-8def-0123456789ab"
	if !validPrincipalEvidenceID(model.ID(userID)) {
		t.Fatal("the vector's User ID is not a canonical durable identity")
	}
	human := RouteMutationAuthorization{
		witness:       RouteAuthorizationWitness{EvidenceDigest: evidence},
		principalSeal: seal,
		authorityMode: principalHumanAuthority,
		userAuthority: store.UserAuthorityFactRef{UserID: model.ID(userID), Version: 7},
	}
	token := RouteMutationAuthorization{
		witness:       RouteAuthorizationWitness{EvidenceDigest: evidence},
		principalSeal: seal,
		authorityMode: principalTokenDirectoryOnly,
	}
	for name, tc := range map[string]struct {
		value RouteMutationAuthorization
		want  string
	}{
		"human": {human, "13f17e159e6182f21805601412fa008f619dc4ad9696b5c250b61ff7e2cf69d4"},
		"token": {token, "34f641f77e6253bf2e1fc213488b10bc69d18b2bb81f2dee417f02674ef28893"},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.value.completeDigest()
			if hex.EncodeToString(got[:]) != tc.want {
				t.Fatalf("completeDigest = %s, want %s", hex.EncodeToString(got[:]), tc.want)
			}
		})
	}
	// The mutation domain is separated from the read receipt's: identical inputs
	// under the two codecs must not agree.
	read := RouteReadDecision{
		witness:       human.witness,
		principalSeal: human.principalSeal,
		authorityMode: human.authorityMode,
		userAuthority: human.userAuthority,
	}
	if read.completeDigest() == human.completeDigest() {
		t.Fatal("the read and mutation domains collide")
	}
	if human.completeDigest() == token.completeDigest() {
		t.Fatal("the authority mode does not enter the digest")
	}
}

// mutationJourney records the three outcomes of one protected transaction
// SEPARATELY, because collapsing them into one error is exactly what let an
// unrelated write failure pass for a barrier refusal in the first attempt.
type mutationJourney struct {
	lockErr      error
	recheckErr   error
	writeErr     error
	commitErr    error
	written      model.ID
	before       model.Timestamp
	after        model.Timestamp
	clockPresent bool
	rechecked    bool
}

// providerWriteRequest is the ACTUAL mutation question: the core provider:write
// permission, an explicit Cedar action, and the concrete tenant and target row the
// write lands on. The shared read fixture's agent:read question would authorize
// nothing a mutation consumer needs.
//
// ⛔ IT CARRIES NO WorkspaceID, AND THAT IS A FACT ABOUT THE ROW, NOT AN OMISSION.
// model.Provider is BaseFields plus Name, Kind, BaseURL, Status and Config; neither
// it nor BaseFields has a workspace column, and the providers table declares none.
// A question naming the tenant's default workspace here would digest a binding the
// stored row cannot have. The scope this question really has is tenant + entity kind
// + that exact row id, which is what it now states.
//
// ⛔ AND target IS A ROW THAT EXISTS. store.Repository[T] exposes Create but not
// CreateWithID — only the untyped module GenericRepo takes a caller-supplied id — so
// a preselected UUID handed to Providers().Create is discarded and the row lands
// under a different one. Authorizing a create against an id the repository will not
// honor names a row that never exists. The protected write is therefore an Update of
// a row seeded BEFORE issuance, whose real id is the authorized target.
func providerWriteRequest(p Principal, tenant model.TenantID, target model.ID) Request {
	const permission = Permission("provider:write")
	return Request{
		Principal:  p,
		Tenant:     tenant,
		Permission: permission,
		Route:      RouteMetadata{CedarAction: "provider:update"},
		Resource:   ResourceAttrs{Kind: permission.Resource(), ID: target.String()},
	}
}

// engineInstant reads a legitimate engine-observed instant through a View, so the
// authorizer clock is a database time rather than an application offset.
func engineInstant(t *testing.T, f *principalEvidenceFixture) time.Time {
	t.Helper()
	var now model.Timestamp
	if err := f.raw.View(f.ctx, f.tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return errors.New("the engine scope exposes no TransactionClock")
		}
		var err error
		now, err = clock.TransactionNow(f.ctx)
		return err
	}); err != nil {
		t.Fatalf("observe the engine clock: %v", err)
	}
	return now.Time()
}

// TestRouteMutationAuthorizationProtectsARealMutation is the bounded store
// integration case: the bundle obtained through the public Interface is locked on
// the SAME transaction as a real product write, that transaction commits, its
// marker is read back, and a fence that moved between issuance and the
// transaction refuses the barrier with store.ErrConflict and leaves no marker.
//
// ⛔ THE PROTECTED WRITE IS Providers(), NOT Agents(), AND THAT WAS THE FIRST
// ATTEMPT'S DEFECT. Agents() is wrapped in newDirectoryTrackedRepo, whose Create
// calls the directory writer's prepare, which deliberately refuses a NEW directory
// write attempted after authority row locks. That refusal came from the product
// write, not from LockAuthoritySnapshotBundle: the raw bundle lock neither arms nor
// mutates the directory. Providers() is a plain typed tenant repository, so the
// journey measures the barrier instead of an unrelated ordering rule.
func TestRouteMutationAuthorizationProtectsARealMutation(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			cfg := store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
			if engine == store.EnginePostgres {
				if !pgtest.Available(t) {
					t.Skip("no Postgres configured: this engine's coverage is UNAVAILABLE, not passing")
				}
				dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
				cfg = store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
			}
			f := newPrincipalEvidenceFixtureConfig(t, cfg)
			// The authorized target is a REAL row, seeded before issuance through an
			// ordinary unprotected write, so the question names an id the repository
			// already honors and the protected write can be asserted to hit it exactly.
			const originalURL = "https://original.example.test"
			var target model.ID
			if err := f.raw.Mutate(f.ctx, f.tenant, func(sc store.Scope) error {
				seeded, err := sc.Providers().Create(f.ctx, model.Provider{
					Name: "ma1-target-" + string(engine), Kind: "anthropic",
					BaseURL: originalURL, Status: model.StatusActive,
				})
				target = seeded.ID
				return err
			}); err != nil {
				t.Fatalf("seed the authorization target: %v", err)
			}
			if target.IsZero() {
				t.Fatal("the seeded target has no identity")
			}

			// The shared fixture seeds a VIEWER, and a viewer is not a write grant.
			// Prove that before granting anything, so the authority this journey rests
			// on is established rather than assumed.
			viewer, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.sessionRef(), f.tenant)
			if err != nil {
				t.Fatalf("reconstruct the viewer: %v", err)
			}
			viewerNow := engineInstant(t, f)
			viewerAz := NewAuthorizer(nil, WithScopedGrants(&principalAuthorityScopedProducer{
				decision: principalAuthorityCleanScoped(viewer.evidence.observedAt, viewer.evidence.freshUntil),
			}), WithClock(func() time.Time { return viewerNow }))
			viewerCtx, cancelViewer := context.WithDeadline(f.ctx, viewer.evidence.freshUntil)
			t.Cleanup(cancelViewer)
			if _, err := viewerAz.AuthorizeRouteMutation(viewerCtx,
				providerWriteRequest(viewer, f.tenant, target)); err == nil {
				t.Fatal("the seeded viewer was authorized for provider:write")
			}

			// Grant the write authority for THIS test only, before reconstruction, by
			// moving this fixture's own membership to editor. The shared fixture
			// builder is unchanged and every other test still gets its viewer.
			if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
				member, err := as.Memberships().Get(f.ctx, f.member.ID)
				if err != nil {
					return err
				}
				member.Role = RoleEditor
				_, err = as.Memberships().Update(f.ctx, member)
				return err
			}); err != nil {
				t.Fatalf("grant the editor membership: %v", err)
			}

			p, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.sessionRef(), f.tenant)
			if err != nil {
				t.Fatalf("reconstruct the writer: %v", err)
			}
			// The issuance instant is engine-observed, not observedAt plus an
			// application offset, so it cannot sit ahead of the database time the
			// journey below verifies against.
			now := engineInstant(t, f)
			if now.Before(p.evidence.observedAt) || !now.Before(p.evidence.freshUntil) {
				t.Fatalf("the engine instant %s is outside the evidence window [%s, %s)",
					now, p.evidence.observedAt, p.evidence.freshUntil)
			}
			az := NewAuthorizer(nil, WithScopedGrants(&principalAuthorityScopedProducer{
				decision: principalAuthorityCleanScoped(p.evidence.observedAt, p.evidence.freshUntil),
			}), WithClock(func() time.Time { return now }))
			ctx, cancel := context.WithDeadline(f.ctx, p.evidence.freshUntil)
			t.Cleanup(cancel)
			req := providerWriteRequest(p, f.tenant, target)
			a, err := az.AuthorizeRouteMutation(ctx, req)
			if err != nil {
				t.Fatalf("AuthorizeRouteMutation for provider:write: %v", err)
			}

			// journey is the shape MA1 §5 prescribes, on ONE finite transaction:
			// transaction time, AuthorityFor, LockAuthoritySnapshotBundle, transaction
			// time again, then the product write. Each outcome is recorded on its own.
			journey := func(auth RouteMutationAuthorization, question Request, marker string) mutationJourney {
				var j mutationJourney
				txCtx, cancelTx := context.WithTimeout(f.ctx, 60*time.Second)
				defer cancelTx()
				err := f.raw.Mutate(txCtx, f.tenant, func(sc store.Scope) error {
					clock, ok := sc.(store.TransactionClock)
					if !ok {
						j.lockErr = errors.New("no engine TransactionClock; refusing")
						return j.lockErr
					}
					j.clockPresent = true
					var e error
					if j.before, e = clock.TransactionNow(txCtx); e != nil {
						j.lockErr = e
						return e
					}
					// ⛔ THE VERIFIER IS ASKED AT THE DATABASE'S TIME, NOT THE
					// AUTHORIZER'S. MA1's window is checked by the consumer, and a
					// consumer that verified against its own clock would be trusting
					// exactly the application time this barrier exists to replace.
					before, e := auth.AuthorityFor(j.before.Time(), question)
					if e != nil {
						j.lockErr = e
						return e
					}
					if len(before.UserAuthorities) != 1 || len(before.Facts) == 0 {
						j.lockErr = errors.New("the bundle lost its H or its tenant facts")
						return j.lockErr
					}
					locker, ok := sc.(store.AuthoritySnapshotBundleLocker)
					if !ok {
						j.lockErr = errors.New("the complete bundle locker is unavailable; refusing")
						return j.lockErr
					}
					if e := locker.LockAuthoritySnapshotBundle(txCtx, before); e != nil {
						j.lockErr = e
						return e
					}
					if j.after, e = clock.TransactionNow(txCtx); e != nil {
						j.lockErr = e
						return e
					}
					// The lifetime recheck MA1 §5 requires, at the post-lock database
					// time. It must answer with the SAME complete bundle: this verifier
					// refreshes nothing, so a second answer that differed would mean the
					// value had consulted something.
					after, e := auth.AuthorityFor(j.after.Time(), question)
					if e != nil {
						j.recheckErr = e
						return e
					}
					if !slices.Equal(before.Facts, after.Facts) ||
						!slices.Equal(before.UserAuthorities, after.UserAuthorities) {
						j.recheckErr = errors.New("the post-lock recheck returned a different bundle")
						return j.recheckErr
					}
					j.rechecked = true
					current, e := sc.Providers().Get(txCtx, model.ID(question.Resource.ID))
					if e != nil {
						j.writeErr = e
						return e
					}
					current.BaseURL = marker
					updated, e := sc.Providers().Update(txCtx, current)
					if e != nil {
						j.writeErr = e
						return e
					}
					j.written = updated.ID
					return nil
				})
				if j.lockErr == nil && j.recheckErr == nil && j.writeErr == nil {
					j.commitErr = err
				}
				return j
			}

			// ── Positive qualification: the lock ADMITS, the write lands, the
			// transaction commits, and the marker is readable afterwards.
			const positiveURL = "https://positive.example.test"
			positive := journey(a, req, positiveURL)
			if !positive.clockPresent {
				t.Fatal("the engine scope exposed no TransactionClock")
			}
			if positive.lockErr != nil {
				t.Fatalf("LockAuthoritySnapshotBundle refused a current bundle: %v", positive.lockErr)
			}
			if !positive.rechecked || positive.recheckErr != nil {
				t.Fatalf("the post-lock recheck at database time failed: %v", positive.recheckErr)
			}
			if positive.writeErr != nil {
				t.Fatalf("the protected provider write failed: %v", positive.writeErr)
			}
			if positive.commitErr != nil {
				t.Fatalf("the protected transaction did not commit: %v", positive.commitErr)
			}
			if positive.written != target {
				t.Fatalf("the protected write hit %s, want the authorized target %s",
					positive.written, target)
			}
			if positive.before.IsZero() || positive.after.IsZero() ||
				positive.after.Time().Before(positive.before.Time()) {
				t.Fatalf("transaction time went backwards across the lock: %s → %s",
					positive.before, positive.after)
			}
			assertProviderURL(t, f, target, positiveURL)

			// ── Moved human User fence (H).
			if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
				user, err := as.Users().Get(f.ctx, f.user.ID)
				if err != nil {
					return err
				}
				user.DisplayName = "Moved Fence"
				_, err = as.Users().Update(f.ctx, user)
				return err
			}); err != nil {
				t.Fatalf("advance the User fence: %v", err)
			}
			moved, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.sessionRef(), f.tenant)
			if err != nil {
				t.Fatalf("re-reconstruct after the User fence moved: %v", err)
			}
			if moved.evidence.userAuthority.Version == p.evidence.userAuthority.Version {
				t.Fatalf("the fixture did not advance the User fence: %+v", moved.evidence.userAuthority)
			}
			staleH := journey(a, req, "https://stale-h.example.test")
			if !errors.Is(staleH.lockErr, store.ErrConflict) {
				t.Fatalf("a stale H did not conflict AT THE BARRIER: lock=%v write=%v commit=%v",
					staleH.lockErr, staleH.writeErr, staleH.commitErr)
			}
			if staleH.writeErr != nil || staleH.rechecked || !staleH.written.IsZero() {
				t.Fatalf("the refused journey still passed the barrier: %v", staleH.writeErr)
			}
			assertProviderURL(t, f, target, positiveURL)

			// ── Moved tenant directory fence (E), with H held still so the refusal is
			// attributable to E alone.
			movedNow := engineInstant(t, f)
			movedAz := NewAuthorizer(nil, WithScopedGrants(&principalAuthorityScopedProducer{
				decision: principalAuthorityCleanScoped(moved.evidence.observedAt, moved.evidence.freshUntil),
			}), WithClock(func() time.Time { return movedNow }))
			movedCtx, cancelMoved := context.WithDeadline(f.ctx, moved.evidence.freshUntil)
			t.Cleanup(cancelMoved)
			movedReq := providerWriteRequest(moved, f.tenant, target)
			fresh, err := movedAz.AuthorizeRouteMutation(movedCtx, movedReq)
			if err != nil {
				t.Fatalf("re-authorize after the User fence moved: %v", err)
			}
			// A tenant-local directory write advances E without touching this human's H.
			if err := f.raw.Mutate(f.ctx, f.tenant, func(sc store.Scope) error {
				_, err := sc.Identities().Create(f.ctx, model.Identity{
					Name: "epoch-mover", Kind: "db_role",
					ExternalID: "epoch-mover", Provider: "test",
				})
				return err
			}); err != nil {
				t.Fatalf("advance the tenant directory fence: %v", err)
			}
			after, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.sessionRef(), f.tenant)
			if err != nil {
				t.Fatalf("re-reconstruct after the directory write: %v", err)
			}
			if after.evidence.directoryEpoch.Version == moved.evidence.directoryEpoch.Version {
				t.Fatalf("the fixture did not advance the tenant directory fence: %d",
					moved.evidence.directoryEpoch.Version)
			}
			if after.evidence.userAuthority != moved.evidence.userAuthority {
				t.Fatalf("the directory write also moved H (%+v → %+v); this journey no longer "+
					"isolates E", moved.evidence.userAuthority, after.evidence.userAuthority)
			}
			staleE := journey(fresh, movedReq, "https://stale-e.example.test")
			if !errors.Is(staleE.lockErr, store.ErrConflict) {
				t.Fatalf("a stale E did not conflict AT THE BARRIER: lock=%v write=%v commit=%v",
					staleE.lockErr, staleE.writeErr, staleE.commitErr)
			}
			if staleE.writeErr != nil || staleE.rechecked || !staleE.written.IsZero() {
				t.Fatalf("the refused journey still passed the barrier: %v", staleE.writeErr)
			}
			assertProviderURL(t, f, target, positiveURL)
		})
	}
}

func assertProviderURL(t *testing.T, f *principalEvidenceFixture, id model.ID, want string) {
	t.Helper()
	if err := f.raw.View(f.ctx, f.tenant, func(sc store.Scope) error {
		row, err := sc.Providers().Get(f.ctx, id)
		if err != nil {
			return err
		}
		if row.ID != id {
			t.Errorf("read back %s, want the authorized target %s", row.ID, id)
		}
		if row.BaseURL != want {
			t.Errorf("provider %s BaseURL = %q, want %q", id, row.BaseURL, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back the protected row: %v", err)
	}
}
