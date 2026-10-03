// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Authentication errors. They are deliberately coarse so a caller cannot
// distinguish "no such user" from "wrong password" (user-enumeration guard); the
// HTTP layer maps all of them to 401 with a generic body.
var (
	// ErrUnauthenticated means the presented credential is missing, malformed,
	// unknown, revoked or expired.
	ErrUnauthenticated = errors.New("auth: unauthenticated")
	// ErrInvalidCredentials means a login email/password did not match.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrLockedOut means too many recent failed attempts for this account or IP.
	ErrLockedOut = errors.New("auth: too many attempts, locked out")
)

// DefaultSessionTTL is how long a freshly minted human session is valid.
const DefaultSessionTTL = 12 * time.Hour

// Authenticator resolves credentials to principals and performs login. It reads
// (and on login, writes) only the engine's auth partition. It is safe for
// concurrent use.
type Authenticator struct {
	// stepUp caches the administrative step-up policy (stepup_policy.go) so
	// every request can carry it without reading inside a store transaction.
	stepUp stepUpCache

	st         store.Store
	clock      model.Clock
	throttle   *throttle
	sessionTTL time.Duration
	log        *slog.Logger
	// trustedLoginProxies is installed once before serving and affects only the
	// throttle's address key. Policy, sessions and audit retain the transport peer.
	trustedLoginProxies TrustedLoginProxies
	// exchangeTTLDur and allowedAudiences configure RFC 8693 token exchange
	// (tokenexchange.go); the zero values mean DefaultExchangeTTL and "accept any
	// well-formed target".
	exchangeTTLDur   time.Duration
	allowedAudiences map[string]bool

	// delegation* configure the delegation-handle verifier (delegation.go): the
	// mint TTL, the request-freshness max age, and the future-skew tolerance. Zero
	// values mean the safe defaults (DefaultDelegationTTL / DefaultDelegationMaxAge
	// / DefaultDelegationFutureSkew), set once at boot via SetDelegationPolicy.
	delegationTTLDur         time.Duration
	delegationMaxAgeDur      time.Duration
	delegationFutureSkew     time.Duration
	decisionClaimLifetimeDur time.Duration

	// ceremonyPending holds the in-flight WebAuthn challenges (webauthn.go),
	// built lazily so embedders that never run a ceremony pay nothing.
	ceremonyOnce    sync.Once
	ceremonyPending *ceremonyStore

	// totpSealer seals the RFC 6238 second-factor seeds at rest (totp.go),
	// set once at boot via WithTOTPSeedSealer. nil means enrolment and
	// verification fail closed with ErrNoTOTPSealer — never a silent skip.
	totpSealer TOTPSeedSealer
	// totpThrottle is the factor's own verification budget, separate from the
	// password throttle so neither can exhaust the other's window.
	totpThrottle *throttle
	// totpPending holds the in-flight TOTP login challenges and enrolment
	// ceremonies (totp.go), built lazily like the WebAuthn ceremonies.
	totpOnce    sync.Once
	totpPending *totpPendingStore

	// seatPolicy is the retained seat seam (seatcap.go), set once at boot via
	// WithSeatPolicy. Since B10 it is DISPLAY-ONLY: account creation is unlimited
	// in every self-hosted tier whatever is wired here (nil included), and the
	// policy figure only feeds the SeatLimit accessor.
	seatPolicy SeatPolicy

	// loginPolicy is the reserved enterprise login-enforcement capability
	// (login_policy.go): require-SSO + network/IP allow-list over the login
	// surface, set once at boot via WithLoginPolicy. nil means NO enforcement —
	// login behaves exactly as today (the open binary wires nil; the enterprise
	// build injects the closed engine). The cap is a binary packaging decision, so
	// embedders and the test suite are unenforced.
	loginPolicy LoginPolicy

	// loginComponent is the declared login enforcement component state (R5,
	// login_capability.go). The zero value is LoginComponentUnset, which adds no guard.
	loginComponent loginComponentCell

	// agentChecker validates an agent identity's lifecycle status for token
	// exchange (agent-OBO). Set via SetAgentLifecycleChecker from the
	// composition root (governance module). nil means agent-OBO is unavailable
	// — requested_actor is rejected.
	agentChecker AgentLifecycleChecker

	// groupMapper is the reserved ENTERPRISE capability (U2, groupmap.go):
	// mapping the directory groups an IdP asserts at SSO login to the tenant's own
	// groups, so login-driven membership reconciliation lights up the group→role
	// (MappedRole) and group-subject (S256) machinery. Set once at boot via
	// WithGroupMapper. nil means NO login-time group mapping — the open binary
	// extracts asserted groups (open-core, U1) but never turns them into grants
	// (the honest cap, symmetric with the multi-IdP and login-policy caps). The
	// cap is a binary packaging decision, so embedders and the test suite are
	// unmapped unless they inject one.
	groupMapper GroupMapper

	// retirementWake wakes the retirement pump after an offboard (retirement.go).
	// nil means no pump is running in this process; the record still waits for one.
	retirementWake func()
	// census is the composition census of the store as it opened, for a store
	// wrapped in guards that hide it (retirement.go); nil reads it from st.
	census store.CompositionCensus
}

// NewAuthenticator builds an Authenticator over st. clock may be nil (system
// clock). Login is throttled at 5 failures per 15 minutes on two keys, the
// ACCOUNT and the CLIENT ADDRESS; what each of them then refuses, and what it
// only makes expensive, is the throttle's decision (throttle.go).
func NewAuthenticator(st store.Store, clock model.Clock) *Authenticator {
	if clock == nil {
		clock = model.SystemClock{}
	}
	return &Authenticator{
		st:           st,
		clock:        clock,
		throttle:     newThrottle(5, 15*time.Minute, func() time.Time { return clock.Now().Time() }),
		totpThrottle: newThrottle(totpThrottleFails, totpThrottleWindow, func() time.Time { return clock.Now().Time() }),
		sessionTTL:   DefaultSessionTTL,
		log:          slog.Default(),
	}
}

// Authenticate resolves a bearer credential string to a Principal, or returns
// ErrUnauthenticated. It is read-only (it does not take the write path), so it is
// cheap on the hot path of every API request.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (Principal, error) {
	prefix, selector, secret, ok := ParseToken(token)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	switch prefix {
	case PrefixSession:
		return a.authSession(ctx, selector, secret, false)
	case PrefixScopedSession:
		return a.authSession(ctx, selector, secret, true)
	case PrefixToken:
		return a.authToken(ctx, selector, secret)
	default:
		return Principal{}, ErrUnauthenticated
	}
}

// authSession resolves a session credential. scoped is whether the token
// carries the scoped-session prefix; the stored row's scope must agree with it,
// so a token minted for one tenant can never present as account-wide.
func (a *Authenticator) authSession(ctx context.Context, selector, secret string, scoped bool) (Principal, error) {
	var p Principal
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		sessions, _, err := as.Sessions().List(ctx, byEq("selector", selector, 1))
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			return ErrUnauthenticated
		}
		s := sessions[0]
		if !SecretMatches(secret, s.SecretHash) || s.Revoked || s.ExpiresAt.Before(a.clock.Now()) {
			return ErrUnauthenticated
		}
		if scoped == s.TenantScope.IsZero() {
			return ErrUnauthenticated
		}
		p, err = a.principalFromSession(ctx, as, s)
		return err
	})
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}

// principalFromSession is the shared, current authority reconstruction for both
// login bearers and a running session's retained launcher credential.
func (a *Authenticator) principalFromSession(ctx context.Context, as store.AuthScope, s model.AuthSession) (Principal, error) {
	if s.Revoked || s.DeletedAt != nil || !a.clock.Now().Time().Before(s.ExpiresAt.Time()) {
		return Principal{}, ErrUnauthenticated
	}
	var p Principal
	u, err := as.Users().Get(ctx, s.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	if u.Status != model.StatusActive || u.DeletedAt != nil {
		return Principal{}, ErrUnauthenticated
	}
	if !validPrincipalRef(PrincipalRef{kind: KindUser, credentialID: s.ID, version: s.Version}) {
		return Principal{}, ErrUnauthenticated
	}
	grants, groups, confined, err := loadGrants(ctx, as, u.ID, u.IsSuperadmin)
	if err != nil {
		return Principal{}, err
	}
	standing, err := loadStanding(ctx, as, u.ID, s.ID)
	if err != nil {
		return Principal{}, err
	}
	p = newPrincipal(KindUser, u.ID, s.ID, u.IsSuperadmin, u.DisplayName, grants, groups).
		withConfinements(confined).withStanding(standing)
	if !s.TenantScope.IsZero() {
		p = p.withSessionScope(s.TenantScope)
	}
	p.AAL = effectiveAAL(s, a.clock.Now())
	p.AMR = s.AMR
	p = p.withCredentialRef(s.Version)
	return p, nil
}

func (a *Authenticator) authToken(ctx context.Context, selector, secret string) (Principal, error) {
	var p Principal
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		t, found, err := lookupAPITokenBySelector(ctx, as, selector)
		if err != nil {
			return err
		}
		if !found {
			return ErrUnauthenticated
		}
		if !SecretMatches(secret, t.SecretHash) || t.Revoked {
			return ErrUnauthenticated
		}
		if t.ExpiresAt != nil {
			now := a.clock.Now()
			switch t.Purpose {
			case WorkSessionCredentialPurpose:
				if workSessionCredentialExpired(*t.ExpiresAt, now) {
					return ErrUnauthenticated
				}
			case CommunicationSessionCredentialPurpose, OrchestrationSessionCredentialPurpose:
				if communicationSessionCredentialExpired(*t.ExpiresAt, now) {
					return ErrUnauthenticated
				}
			default:
				if t.ExpiresAt.Before(now) {
					return ErrUnauthenticated
				}
			}
		}
		// The two runtime session purposes are ordinary HTTP bearers with
		// separate private ceilings. Every other purpose stays confined to its
		// dedicated protocol path (for example AuthenticatePEP).
		switch t.Purpose {
		case WorkSessionCredentialPurpose:
			var ok bool
			p, ok = workSessionPrincipal(t)
			if !ok {
				return ErrUnauthenticated
			}
		case OrchestrationSessionCredentialPurpose:
			var ok bool
			p, ok = orchestrationSessionPrincipal(t)
			if !ok {
				return ErrUnauthenticated
			}
		case CommunicationSessionCredentialPurpose:
			var ok bool
			p, ok = communicationSessionPrincipal(t)
			if !ok {
				return ErrUnauthenticated
			}
		case "":
			// Continue through ordinary token validation below.
			// Session binding columns are meaningful only under a dedicated runtime
			// purpose. A legacy/ordinary row carrying any of them is malformed, not
			// a broad role token that also happens to prove a session.
			if t.SessionRef != "" || !t.WorkspaceID.IsZero() || t.SessionRunRef != "" ||
				t.SessionFence != 0 {
				return ErrUnauthenticated
			}
			grants := map[model.TenantID]string{}
			if !t.IsSuperadmin {
				// A bound token must name exactly one valid tenant+role; anything else
				// is a misconfigured token and authenticates to nothing.
				if t.BoundTenantID.IsZero() || !IsRole(t.Role) {
					return ErrUnauthenticated
				}
				grants[t.BoundTenantID] = t.Role
			}
			p = newPrincipal(KindToken, t.UserID, t.ID, t.IsSuperadmin, t.Name, grants, nil)
			if p, err = withTokenStanding(ctx, as, p, t); err != nil {
				return err
			}
			// A token-exchanged (delegated) token carries its audience binding and the
			// principal it acts for, so a resource server can enforce confused-deputy
			// protection (RFC 8707) and the audit trail can attribute the delegation.
			if t.Audience != "" {
				p.audiences = strings.Split(t.Audience, "\n")
			}
			p.actAs = t.ActAsUserID
			// Agent-OBO: the token acts as its agent; mirror the MCP AgentIdentity precedent.
			if t.AgentRef != "" {
				p = p.WithAgentIdentity(t.AgentRef)
			}
		default:
			return ErrUnauthenticated
		}
		if !validPrincipalRef(PrincipalRef{kind: KindToken, credentialID: t.ID, version: t.Version}) {
			return ErrUnauthenticated
		}
		// Generic delegated/act-as/audience tokens stay on the historical bearer
		// path and do not expose the reusable evidence handle. A canonical
		// agent-OBO token is the one exception: its agent identity is the subject
		// K3 authorizes, its verb scope exactly matches its stored role, and it has
		// no act-as or audience ambiguity. The communication kernel separately
		// revalidates the current Agent/lifecycle/channel facts before any effect.
		if t.Purpose == "" && tokenCarriesDelegationBinding(t) &&
			!principalEvidenceAgentOBOToken(t) {
			return nil
		}
		p = p.withCredentialRef(t.Version)
		return nil
	})
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}

func tokenCarriesDelegationBinding(t model.APIToken) bool {
	return t.Scope != "" || !t.ParentTokenID.IsZero() || t.Audience != "" ||
		!t.ActAsUserID.IsZero() || t.AgentRef != ""
}

// principalEvidenceAgentOBOToken recognizes the product token-exchange shape
// that represents one agent as itself. Scope is stored canonically by
// ExchangeToken; requiring exact role/scope parity prevents a hand-written or
// corrupted row from presenting an editor role under a read-only delegation.
// Act-as and audience-bound credentials remain excluded because either adds a
// second possible authority subject/target interpretation.
func principalEvidenceAgentOBOToken(t model.APIToken) bool {
	if t.Purpose != "" || t.AgentRef == "" || !validRuntimeAgentIdentity(t.AgentRef) ||
		!validPrincipalEvidenceTenant(t.BoundTenantID) || !validPrincipalEvidenceID(t.UserID) ||
		!IsRole(t.Role) || t.Role == RoleOwner ||
		!t.ActAsUserID.IsZero() || t.Audience != "" {
		return false
	}
	wantScope := strings.Join(scopeForTier(verbTierForRole(t.Role)), " ")
	return wantScope != "" && t.Scope == wantScope
}

// lookupAPITokenBySelector is the shared selector lookup for ordinary and PEP
// token authentication. Secret verification stays in the caller so both paths
// use SecretMatches for the same constant-time comparison.
func lookupAPITokenBySelector(
	ctx context.Context,
	as store.AuthScope,
	selector string,
) (model.APIToken, bool, error) {
	tokens, _, err := as.Tokens().List(ctx, byEq("selector", selector, 1))
	if err != nil {
		return model.APIToken{}, false, err
	}
	if len(tokens) == 0 {
		return model.APIToken{}, false, nil
	}
	return tokens[0], true, nil
}

// loadGrants reads a user's full membership set (every tenant it may act in),
// then folds in the directory groups: their SCIM group→role mappings AND (S256)
// the group identities themselves, so the principal can be a subject of a
// group-scoped grant.
//
// It returns two maps, both keyed by tenant: the resolved role per tenant, and
// the group ids the user is a GATED member of per tenant (the user's direct
// groups plus every group they are nested under — loadGroupClosure). The second
// is the subject-side hierarchy S256 materializes: each id becomes a Cedar
// `Group::"<id>"` principal parent (buildPrincipalEntity).
//
// THE PER-TENANT GATE: a group ELEVATES an existing direct membership, it never
// grants base membership. An ordinary user without a direct membership gains
// neither an elevated role nor a group identity. A verified superadmin is already
// admitted as an owner and keeps its stored group subjects, so an authored forbid
// also restricts that user on legacy routes. This is the group-mapping gate: an IdP
// roster push can widen a role (or make a member a grant subject) only where a
// tenant operator already admitted the user, and it can never widen the role
// DOWN (a higher direct role wins). MappedRole elevation still requires a direct
// membership, including for a superadmin. Token principals
// never pass here: authToken builds its single bound grant itself — least
// privilege, ceiling-checked at issue time, carrying no group memberships.
func loadGrants(ctx context.Context, as store.AuthScope, userID model.ID, superadmin bool) (map[model.TenantID]string, map[model.TenantID][]string, map[model.TenantID]model.ID, error) {
	ms, err := drainList(ctx, as.Memberships().List, byEq("user_id", userID.String(), 0))
	if err != nil {
		return nil, nil, nil, err
	}
	g := make(map[model.TenantID]string, len(ms))
	// a workspace-scoped membership (WorkspaceID != zero) CONFINES the principal to
	// that workspace in the tenant. There is exactly one membership row per (user, tenant)
	// (the auth store's unique key excludes workspace_id), so a tenant maps to at most one
	// confinement. A group's MappedRole may elevate the ROLE below, but never the
	// confinement (which is a property of the direct membership, not the group).
	var confined map[model.TenantID]model.ID
	for _, m := range ms {
		if IsRole(m.Role) {
			g[m.TargetTenantID] = m.Role
		}
		if !m.WorkspaceID.IsZero() {
			if confined == nil {
				confined = make(map[model.TenantID]model.ID, 1)
			}
			confined[m.TargetTenantID] = m.WorkspaceID
		}
	}
	rows, err := drainList(ctx, as.GroupMembers().List, byEq("user_id", userID.String(), 0))
	if err != nil {
		return nil, nil, nil, err
	}
	cache := make(map[model.ID]*model.UserGroup, len(rows)) // each group resolved once per call
	var subjectGroups map[model.TenantID][]string           // S256: gated group memberships → principal Group:: parents
	seen := map[model.ID]bool{}                             // a group is carried once even via several nesting paths
	for _, r := range rows {
		grp, err := groupByID(ctx, as, cache, r.GroupID)
		if err != nil {
			return nil, nil, nil, err
		}
		if grp == nil {
			continue // dangling member row (its group is gone): grants nothing
		}
		// Group membership alone never admits an ordinary user. Superadmins keep
		// their stored subjects without a direct membership; mapped roles below
		// still require one. The flag comes from the caller's stored user row.
		cur, member := g[grp.TargetTenantID]
		if !member && !superadmin {
			continue
		}
		// S256: the user is a GATED member of this group, so carry the group — and
		// every group it is nested under — as principal subjects, so a scoped grant
		// whose subject is the group (or any ancestor) matches. The closure stays
		// within grp.TargetTenantID (a parent edge crossing tenants is refused at
		// set time and skipped here), so the single gate check above covers it.
		mapped, e := loadGroupClosure(ctx, as, cache, grp, seen, &subjectGroups)
		if e != nil {
			return nil, nil, nil, e
		}
		// MappedRole elevation — semantics UNCHANGED: a mapped group raises an
		// existing direct membership to the max, it never grants base membership.
		// Nesting does NOT inherit a parent's MappedRole (a nested group is a
		// subject relationship, not a role grant); only the directly-named group's
		// own mapping elevates, exactly as before S256.
		if member && IsRole(mapped) && RoleRank(mapped) > RoleRank(cur) {
			g[grp.TargetTenantID] = mapped
		}
	}
	return g, subjectGroups, confined, nil
}

// accountStanding is what an account's tenant exclusions say about its
// principal: the tenants it, or the session being resolved, is excluded from,
// and the authorization epoch at which each tenant last retired it.
type accountStanding struct {
	excluded map[model.TenantID]struct{}
	floors   map[model.TenantID]int64
}

// loadStanding reads the account's tenant exclusions. session is the session
// being resolved, or zero: a session exclusion removes only that session from
// its tenant. An exclusion of a kind this build does not know excludes.
func loadStanding(ctx context.Context, as store.AuthScope, userID, session model.ID) (accountStanding, error) {
	rows, err := drainList(ctx, as.TenantExclusions().List, byEq("user_id", userID.String(), 0))
	if err != nil {
		return accountStanding{}, err
	}
	var st accountStanding
	exclude := func(tenant model.TenantID) {
		if st.excluded == nil {
			st.excluded = map[model.TenantID]struct{}{}
		}
		st.excluded[tenant] = struct{}{}
	}
	for _, x := range rows {
		switch x.Kind {
		case model.ExclusionOffboard:
			if x.RetiredEpoch != nil {
				if st.floors == nil {
					st.floors = map[model.TenantID]int64{}
				}
				if floor, ok := st.floors[x.TargetTenantID]; !ok || *x.RetiredEpoch > floor {
					st.floors[x.TargetTenantID] = *x.RetiredEpoch
				}
			}
			if x.InForce() {
				exclude(x.TargetTenantID)
			}
		case model.ExclusionSession:
			if !session.IsZero() && x.SessionID == session {
				exclude(x.TargetTenantID)
			}
		default:
			exclude(x.TargetTenantID)
		}
	}
	return st, nil
}

// withTokenStanding applies the owner's standing to an ordinary token
// principal: a token bound to a tenant that excludes its owner carries nothing
// there, and the owner's floor in that tenant travels with it.
func withTokenStanding(ctx context.Context, as store.AuthScope, p Principal, t model.APIToken) (Principal, error) {
	if t.UserID.IsZero() || t.IsSuperadmin {
		return p, nil
	}
	standing, err := loadStanding(ctx, as, t.UserID, "")
	if err != nil {
		return Principal{}, err
	}
	return p.withStanding(standing), nil
}

// groupByID resolves a group id through a per-call cache (each group is fetched
// at most once). A missing group caches and returns nil (a dangling edge grants
// nothing — deny-closed), distinguishing "resolved to absent" from "not yet
// looked up".
func groupByID(ctx context.Context, as store.AuthScope, cache map[model.ID]*model.UserGroup, id model.ID) (*model.UserGroup, error) {
	if grp, cached := cache[id]; cached {
		return grp, nil
	}
	got, err := as.Groups().Get(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		cache[id] = nil
		return nil, nil
	case err != nil:
		return nil, err
	default:
		cache[id] = &got
		return &got, nil
	}
}

// loadGroupClosure records grp and every group it is nested under (following
// ParentGroupID, S256) as principal subjects in *out, keyed by tenant, and
// returns grp's OWN MappedRole (only the directly-named group elevates; nesting
// is a subject relationship, not a role grant). The walk is bounded and
// cycle-safe (the shared `seen` set also dedupes a group reached through several
// children), and never leaves grp's tenant: a parent edge that crosses tenants
// (or dangles) ends the chain (deny-closed). The caller has already applied the
// per-tenant gate to grp.TargetTenantID, which — because the closure stays in
// that tenant — covers every group it adds.
func loadGroupClosure(ctx context.Context, as store.AuthScope, cache map[model.ID]*model.UserGroup, grp *model.UserGroup, seen map[model.ID]bool, out *map[model.TenantID][]string) (string, error) {
	tenant := grp.TargetTenantID
	mappedRole := grp.MappedRole
	for cur := grp; cur != nil; {
		if seen[cur.ID] {
			break // already carried (a shared ancestor or a cycle) — stop
		}
		seen[cur.ID] = true
		if *out == nil {
			*out = map[model.TenantID][]string{}
		}
		(*out)[tenant] = append((*out)[tenant], cur.ID.String())
		if cur.ParentGroupID.IsZero() {
			break
		}
		parent, err := groupByID(ctx, as, cache, cur.ParentGroupID)
		if err != nil {
			return "", err
		}
		// A parent that is gone, or that lives in another tenant, ends the chain:
		// nesting never crosses the tenant the gate was checked against.
		if parent == nil || parent.TargetTenantID != tenant {
			break
		}
		cur = parent
	}
	return mappedRole, nil
}

// Login validates an email/password and, on success, mints a server-side session
// and returns its token (shown once). It is throttled per account and per client
// address, and runs an argon2id verification even for an unknown email so timing
// cannot reveal which accounts exist. Failed attempts and lockouts are recorded to
// the audit ledger. ip is the transport peer, which is the only address this
// engine observed for itself.
//
// A caller that abandons the request while the throttle is holding the attempt
// gets the context's error back, so the attempt can be reported rather than lost.
func (a *Authenticator) Login(ctx context.Context, emailRaw, password, ip string) (string, model.AuthSession, error) {
	res, err := a.LoginFrom(ctx, emailRaw, password, ip, nil)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	if res.RequiresMFA() {
		if res.MFAEnrolmentRequired {
			return "", model.AuthSession{}, ErrTOTPEnrolmentRequired
		}
		return "", model.AuthSession{}, ErrTOTPRequired
	}
	return res.Token, res.Session, nil
}

// LoginFrom performs Login with all X-Forwarded-For values in received order.
// A configured trusted peer may supply the throttle address; ip remains the
// transport peer for network policy, session provenance and audit.
func (a *Authenticator) LoginFrom(ctx context.Context, emailRaw, password, ip string, forwarded []string) (LoginResult, error) {
	email := normalizeEmail(emailRaw)
	accountKey, addressKey := "email:"+email, "ip:"+a.trustedLoginProxies.clientAddress(ip, forwarded)

	// One question, asked once: the throttle reads both keys together, says what
	// this attempt is worth, and records the admission in the same locked step
	// (throttle.go). Nothing about the account of record is known yet, and nothing
	// here needs it.
	verdict, wait := a.throttle.decide(accountKey, addressKey)
	if verdict == loginRefuse {
		return LoginResult{}, ErrLockedOut
	}
	// And one outcome, reported exactly once however this attempt ends. Abandoned is
	// the honest default for every return that reaches no VERDICT on the password:
	// the early ones below, where the credential was never read, and equally a
	// require-SSO refusal or a session that could not be minted AFTER a password
	// that matched. None of them accuses the account or absolves it. The defer
	// releases the admission even if the path below panics.
	outcome := loginAbandoned
	defer func() { a.throttle.record(accountKey, addressKey, verdict, outcome) }()

	// network allow-list. Refuse a peer outside the configured CIDR list
	// BEFORE any credential work (cheap, deny-closed, no account oracle: an IP block
	// reveals nothing about which accounts exist). A nil login policy (the open build /
	// a test embedder) is a no-op, so this is byte-identical to today there. It runs
	// before the delay so a peer that may not log in at all never waits for one.
	attempt, err := a.beginLogin(ctx, ip)
	if err != nil {
		return LoginResult{}, err
	}

	// A tripped address makes an unproven attempt expensive instead of impossible.
	// Charged before the store read and the hash, so the attacker pays it and the
	// engine does not, and charged whatever account the attempt names, so it tells
	// a caller nothing about which accounts exist.
	if verdict == loginDelay {
		if err := a.throttle.waitOut(ctx, wait); err != nil {
			return LoginResult{}, err
		}
	}

	// Phase 1 — read the user in a READ-only transaction. The argon2id verify must
	// NOT run inside a write transaction: on the single-connection SQLite engine
	// that would hold the one writer for the whole hash, serializing the engine
	// under login load (a DoS amplifier).
	var user model.User
	var found bool
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		users, _, err := as.Users().List(ctx, byEq("email", email, 1))
		if err != nil {
			return err
		}
		if len(users) > 0 {
			user, found = users[0], true
		}
		return nil
	}); err != nil {
		return LoginResult{}, err
	}

	// Phase 2 — verify with NO transaction held. Always spend argon2 time (a dummy
	// hash on the unknown / SSO-only / inactive path) so timing cannot reveal
	// which accounts exist.
	authed := false
	if found && user.PasswordHash != "" && user.Status == model.StatusActive {
		match, verr := VerifyPassword(password, user.PasswordHash)
		authed = verr == nil && match
	} else {
		DummyVerify(password)
	}

	// Phase 3 — short write transaction only (no argon2 inside).
	if !authed {
		actor := "anonymous"
		if found {
			actor = "user:" + user.ID.String()
		}
		if aerr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
			return appendLoginFail(ctx, as, actor, ip)
		}); aerr != nil {
			a.log.Error("auth: recording failed login", "err", aerr)
		}
		outcome = loginFailed
		return LoginResult{}, ErrInvalidCredentials
	}

	// the second-factor gate. A password that matched owes either a code
	// challenge (a confirmed factor) or a forced enrolment (the policy requires
	// administrators to hold one and this account has none). Neither mints a
	// session: both return a single-use pending credential the caller must
	// satisfy. The require-SSO refusal is checked FIRST (mirroring mintSession's
	// own order) so an SSO-mandated account never reveals it also carries a
	// factor. The throttle outcome stays loginSucceeded — the PASSWORD was
	// correct, and the factor keeps its own verification budget.
	if err := a.enforceRequireSSO(ctx, user); err != nil {
		a.auditLoginBlocked(ctx, "user:"+user.ID.String(), attempt.ip, "sso_required")
		return LoginResult{}, err
	}
	challenge, enrol, err := a.totpGate(ctx, user)
	if err != nil {
		return LoginResult{}, err
	}
	if challenge {
		pending, err := a.mintTOTPPending(user.ID, attempt.ip, user.CustodyScope(), nil)
		if err != nil {
			return LoginResult{}, err
		}
		outcome = loginSucceeded
		return LoginResult{MFAToken: pending, MFAEnrolmentRequired: enrol}, nil
	}

	token, sess, err := a.mintSession(ctx, attempt, user, user.CustodyScope(), "auth.login", passwordLogin, nil, nil, nil)
	if err != nil {
		return LoginResult{}, err
	}
	outcome = loginSucceeded
	return LoginResult{Token: token, Session: sess}, nil
}

// mintSession creates an opaque server-side session for an already-authenticated
// user and records action (e.g. "auth.login" for password login, "sso.login" for
// federation) on the ledger in the same transaction. It performs NO credential
// check — the caller has already established the identity (password, or a
// validated SSO assertion). method names how it was established; every
// fresh session starts at AAL1 — assurance is only ever raised by a verified
// step-up ceremony (ElevateSession), never at mint time (fail-closed).
//
// scope confines the session to one tenant: an account a tenant holds signs in
// scoped to it, and so does every sign-in through a tenant's identity provider.
// The zero scope is an account-scope session.
func (a *Authenticator) mintSession(ctx context.Context, attempt *loginAttempt, user model.User, scope model.TenantID, action string, method sessionLoginMethod, amrExtra []string, activate func(store.AuthScope) error, txVerify func(store.AuthScope) error) (string, model.AuthSession, error) {
	if attempt == nil || (method != passwordLogin && method != federatedLogin && method != externalLogin) {
		return "", model.AuthSession{}, ErrInvalidCredentials
	}
	// Credential verification has finished. The password-policy decision
	// (require-SSO) now lives in the PASSWORD callers themselves — LoginFrom and
	// AcceptInvite — so each consults the wired policy exactly once and the TOTP
	// gate in LoginFrom can order itself after it without a second consult (a
	// factor-gated SSO-mandated account must refuse on the SSO reason alone,
	// never reveal that a factor also exists). The SSO completion path never
	// consulted it and still does not.
	ip, amr := attempt.ip, []string{string(method)}
	amr = append(amr, amrExtra...)
	var (
		tok  string
		sess model.AuthSession
	)
	if err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// R5: for an absent component, the capability lock and the posture read come
		// first, before the session create takes directory/user authority.
		if err := a.guardNewLoginSession(ctx, as); err != nil {
			return err
		}
		// The optional transactional revalidation (a continuation's
		// SessionTxRevalidator): checked BEFORE any factor activation or
		// credential creation writes, and again AFTER the session and audit
		// writes — both inside THIS transaction, so a withdrawal or expiry
		// that lands between them rolls the whole issuance back. Callers
		// that pass nil (every local, OIDC and SAML path) are unchanged.
		if txVerify != nil {
			if err := txVerify(as); err != nil {
				return err
			}
		}
		if activate != nil {
			if err := activate(as); err != nil {
				return err
			}
		}
		t, s, err := a.mintSessionTx(ctx, as, user, scope, ip, action, amr)
		if err != nil {
			return err
		}
		if txVerify != nil {
			if err := txVerify(as); err != nil {
				return err
			}
		}
		tok, sess = t, s
		return nil
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	return tok, sess, nil
}

// mintSessionTx mints a fresh opaque session for user and audits it INSIDE the
// caller's auth transaction. Native login producers call it only after policy
// admission and any atomic account activation. A fresh session is always AAL1; assurance
// is only ever raised by a verified step-up ceremony (ElevateSession).
func (a *Authenticator) mintSessionTx(ctx context.Context, as store.AuthScope, user model.User, scope model.TenantID, ip, action string, amr []string) (string, model.AuthSession, error) {
	// R5 defense at the create seam: the guard is idempotent, so a caller that already
	// took it first pays one more read. A caller that reached here after a directory or
	// audit lock gets the store's lock-order error, which also poisons the transaction.
	if err := a.guardNewLoginSession(ctx, as); err != nil {
		return "", model.AuthSession{}, err
	}
	// A scoped session has its own prefix, so a binary that does not know session
	// scope refuses the token instead of reading it as account-wide. Each prefix is
	// named at its call, so the session issuance census sees both producers.
	var cred Credential
	var err error
	if scope.IsZero() {
		cred, err = NewCredential(PrefixSession)
	} else {
		cred, err = NewCredential(PrefixScopedSession)
	}
	if err != nil {
		return "", model.AuthSession{}, err
	}
	now := a.clock.Now()
	created, err := as.Sessions().Create(ctx, model.AuthSession{
		UserID:      user.ID,
		Selector:    cred.Selector,
		SecretHash:  cred.SecretHash,
		ExpiresAt:   model.NewTimestamp(now.Time().Add(a.sessionTTL)),
		CreatedIP:   ip,
		AAL:         1,
		AMR:         amr,
		TenantScope: scope,
	})
	if err != nil {
		return "", model.AuthSession{}, err
	}
	if _, err := as.Audit().Append(ctx, model.AuditDraft{
		Actor: "user:" + user.ID.String(), ActorKind: model.ActorUser,
		Action: action, TargetKind: "core.user", TargetID: user.ID,
		Meta: map[string]any{"session": created.ID.String()},
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	return cred.Token, created, nil
}

// RefreshSession rotates the calling session's credential in place and extends its
// expiry, returning a NEW opaque token while the OLD one stops working (the secret
// AND selector are rotated, so the prior token no longer resolves). It operates ONLY
// on the caller's own session (actor.CredID) and refuses a revoked or already-expired
// session — the same deny-closed semantics as the rest of auth, and it never widens
// scope (the session's user, grants and superadmin status are untouched). A non-user
// principal (an API token) is not renewable here: tokens are reissued via /v1/tokens.
func (a *Authenticator) RefreshSession(ctx context.Context, actor Principal) (string, model.AuthSession, error) {
	return a.rotateSession(ctx, actor, true)
}

// MigrateBrowserSession retires a legacy browser bearer without extending its
// lifetime or changing its identity, scope or assurance.
func (a *Authenticator) MigrateBrowserSession(ctx context.Context, actor Principal) (string, model.AuthSession, error) {
	return a.rotateSession(ctx, actor, false)
}

func (a *Authenticator) rotateSession(ctx context.Context, actor Principal, renew bool) (string, model.AuthSession, error) {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return "", model.AuthSession{}, ErrUnauthenticated
	}
	now := a.clock.Now()
	var (
		sess  model.AuthSession
		token string
	)
	if err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		s, err := as.Sessions().Get(ctx, actor.CredID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrUnauthenticated
			}
			return err
		}
		// A revoked or expired session is not renewable — re-login is required
		// (deny-closed: refresh extends a live session, it never resurrects a dead one).
		if s.Revoked || s.ExpiresAt.Before(now) {
			return ErrUnauthenticated
		}
		// Migration is single-use even when two requests authenticated the old
		// bearer before either reached this transaction.
		if !renew && actor.credentialRef.version != s.Version {
			return ErrUnauthenticated
		}
		// A scoped session stays scoped: the new token carries the prefix the
		// row's scope requires, named at its call for the session issuance census.
		var cred Credential
		if s.TenantScope.IsZero() {
			cred, err = NewCredential(PrefixSession)
		} else {
			cred, err = NewCredential(PrefixScopedSession)
		}
		if err != nil {
			return err
		}
		token = cred.Token
		s.Selector = cred.Selector
		s.SecretHash = cred.SecretHash
		action := "auth.session.cookie"
		if renew {
			s.ExpiresAt = model.NewTimestamp(now.Time().Add(a.sessionTTL))
			action = "auth.refresh"
		}
		updated, err := as.Sessions().Update(ctx, s)
		if err != nil {
			return err
		}
		sess = updated
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: action, TargetKind: "core.auth_session", TargetID: s.ID,
		})
		return err
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	return token, sess, nil
}

// RevokeSession marks a session revoked (logout), recording it with the acting
// principal as actor.
func (a *Authenticator) RevokeSession(ctx context.Context, actor Principal, sessionID model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		s, err := as.Sessions().Get(ctx, sessionID)
		if err != nil {
			return err
		}
		if s.Revoked {
			return nil
		}
		s.Revoked = true
		if _, err := as.Sessions().Update(ctx, s); err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.logout", TargetKind: "core.auth_session", TargetID: sessionID,
		})
		return err
	})
}

// appendLoginFail records a failed-login event and returns nil so the enclosing
// AuthMutate commits it (a failure audit must persist, unlike a rolled-back read).
func appendLoginFail(ctx context.Context, as store.AuthScope, actor, ip string) error {
	_, err := as.Audit().Append(ctx, model.AuditDraft{
		Actor: actor, ActorKind: model.ActorUser, Action: "auth.login.failed",
		Meta: map[string]any{"ip": ip},
	})
	return err
}

// byEq builds a single-equality List query.
func byEq(col, val string, limit int) model.Query {
	return model.Query{Filters: []model.Filter{{Column: col, Op: model.OpEq, Value: val}}, Limit: limit}
}

var errDrainListIncomplete = errors.New("auth: repository listing did not complete")

// drainList pages a repository listing to completion: the store silently clamps
// any requested Limit to its per-page cap (sqlstore maxLimit = 1000), so a
// single List over a set that can legitimately exceed it (an "all employees"
// directory group's member rows, a tenant's groups, a tenant's member set)
// would silently truncate — and a truncated read here corrupts writes built on
// it (a member diff misses rows; the leaver sweep leaves stale elevations). The
// guards are runaway bounds far above any real tenant, not working limits: at
// most one hundred calls and 100,000 rows. It pages through store.WalkPages, so
// a continuation that cannot make progress, or one that exceeds either bound,
// fails closed with errDrainListIncomplete and discards the partial result. The
// caller's q.Limit/q.Cursor are overridden (completeness is the point).
func drainList[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error), q model.Query) ([]T, error) {
	const pageSize, maxPages = 1000, 100
	q.Limit = pageSize
	q.Cursor = ""
	calls := 0
	guarded := func(c context.Context, pq model.Query) ([]T, model.Page, error) {
		if calls == maxPages {
			return nil, model.Page{}, fmt.Errorf("%w: more than %d pages", store.ErrPageCapacity, maxPages)
		}
		calls++
		return list(c, pq)
	}
	var out []T
	err := store.WalkPages(ctx, guarded, q, pageSize*maxPages, func(rows []T) error {
		out = append(out, rows...)
		return nil
	})
	if errors.Is(err, store.ErrPageContinuation) || errors.Is(err, store.ErrPageCapacity) {
		return nil, fmt.Errorf("%w: %w", errDrainListIncomplete, err)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// normalizeEmail lowercases and trims an email for consistent storage and lookup.
func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
