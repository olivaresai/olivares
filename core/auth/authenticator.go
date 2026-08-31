// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
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
	st         store.Store
	clock      model.Clock
	throttle   *throttle
	sessionTTL time.Duration
	log        *slog.Logger
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
}

// NewAuthenticator builds an Authenticator over st. clock may be nil (system
// clock). Login is throttled to 5 failures per 15 minutes per account and per IP.
func NewAuthenticator(st store.Store, clock model.Clock) *Authenticator {
	if clock == nil {
		clock = model.SystemClock{}
	}
	return &Authenticator{
		st:         st,
		clock:      clock,
		throttle:   newThrottle(5, 15*time.Minute, func() time.Time { return clock.Now().Time() }),
		sessionTTL: DefaultSessionTTL,
		log:        slog.Default(),
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
		return a.authSession(ctx, selector, secret)
	case PrefixToken:
		return a.authToken(ctx, selector, secret)
	default:
		return Principal{}, ErrUnauthenticated
	}
}

func (a *Authenticator) authSession(ctx context.Context, selector, secret string) (Principal, error) {
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
		u, err := as.Users().Get(ctx, s.UserID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrUnauthenticated
			}
			return err
		}
		if u.Status != model.StatusActive {
			return ErrUnauthenticated
		}
		if !validPrincipalRef(PrincipalRef{kind: KindUser, credentialID: s.ID, version: s.Version}) {
			return ErrUnauthenticated
		}
		grants, groups, confined, err := loadGrants(ctx, as, u.ID)
		if err != nil {
			return err
		}
		p = newPrincipal(KindUser, u.ID, s.ID, u.IsSuperadmin, u.DisplayName, grants, groups).withConfinements(confined)
		p.AAL = effectiveAAL(s, a.clock.Now())
		p.AMR = s.AMR
		p = p.withCredentialRef(s.Version)
		return nil
	})
	if err != nil {
		return Principal{}, err
	}
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
			case CommunicationSessionCredentialPurpose:
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
		// A token-exchange/delegation row stays authenticated on the historical
		// bearer path, but it must not expose the reusable evidence credential
		// handle. Principal evidence intentionally does not compose delegated,
		// audience-bound or agent-OBO authority in this cut.
		if t.Purpose == "" && tokenCarriesDelegationBinding(t) {
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
// grants base membership — a user a group names but who holds no membership in
// the group's target tenant gains NOTHING there, NEITHER an elevated role NOR a
// group identity. This is the deny-closed lynchpin of group mapping: an IdP
// roster push can widen a role (or make a member a grant subject) only where a
// tenant operator already admitted the user, and it can never widen the role
// DOWN (a higher direct role wins). The gate guards BOTH the MappedRole
// elevation and the new group-subject propagation identically. Token principals
// never pass here: authToken builds its single bound grant itself — least
// privilege, ceiling-checked at issue time, carrying no group memberships.
func loadGrants(ctx context.Context, as store.AuthScope, userID model.ID) (map[model.TenantID]string, map[model.TenantID][]string, map[model.TenantID]model.ID, error) {
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
		// THE PER-TENANT GATE (unchanged): a group confers nothing where the user
		// holds no direct membership — group membership alone never admits. It
		// guards the group-subject propagation AND the MappedRole elevation below.
		cur, member := g[grp.TargetTenantID]
		if !member {
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
		if IsRole(mapped) && RoleRank(mapped) > RoleRank(cur) {
			g[grp.TargetTenantID] = mapped
		}
	}
	return g, subjectGroups, confined, nil
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
// and returns its token (shown once). It is throttled per account and per IP, and
// runs an argon2id verification even for an unknown email so timing cannot reveal
// which accounts exist. Failed attempts and lockouts are recorded to the audit
// ledger.
func (a *Authenticator) Login(ctx context.Context, emailRaw, password, ip string) (string, model.AuthSession, error) {
	email := normalizeEmail(emailRaw)
	emKey, ipKey := "email:"+email, "ip:"+ip
	if a.locked(emKey) || a.locked(ipKey) {
		return "", model.AuthSession{}, ErrLockedOut
	}

	// network allow-list. Refuse a peer outside the configured CIDR list
	// BEFORE any credential work (cheap, deny-closed, no account oracle: an IP block
	// reveals nothing about which accounts exist). A nil login policy (the open build /
	// a test embedder) is a no-op, so this is byte-identical to today there.
	if err := a.enforceNetwork(ctx, ip); err != nil {
		a.auditLoginBlocked(ctx, "anonymous", ip, "network_not_allowed")
		return "", model.AuthSession{}, err
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
		return "", model.AuthSession{}, err
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
		a.throttle.fail(emKey)
		a.throttle.fail(ipKey)
		return "", model.AuthSession{}, ErrInvalidCredentials
	}

	// require-SSO. The password is CORRECT here; refuse the PASSWORD login when
	// the scope requires SSO (deny-closed). Checking AFTER the credential verify keeps
	// this from being an oracle — a wrong password already returned ErrInvalidCredentials
	// above, so only the legitimate account holder ever sees "use SSO". The SSO
	// completion path (CompleteSSO) is never routed here. nil policy ⇒ no-op (open build).
	if err := a.enforceRequireSSO(ctx, user); err != nil {
		a.auditLoginBlocked(ctx, "user:"+user.ID.String(), ip, "sso_required")
		return "", model.AuthSession{}, err
	}

	token, sess, err := a.mintSession(ctx, user, ip, "auth.login", []string{"pwd"})
	if err != nil {
		return "", model.AuthSession{}, err
	}
	a.throttle.reset(emKey)
	a.throttle.reset(ipKey)
	return token, sess, nil
}

// mintSession creates an opaque server-side session for an already-authenticated
// user and records action (e.g. "auth.login" for password login, "sso.login" for
// federation) on the ledger in the same transaction. It performs NO credential
// check — the caller has already established the identity (password, or a
// validated SSO assertion). amr names the method(s) that established it; every
// fresh session starts at AAL1 — assurance is only ever raised by a verified
// step-up ceremony (ElevateSession), never at mint time (fail-closed).
func (a *Authenticator) mintSession(ctx context.Context, user model.User, ip, action string, amr []string) (string, model.AuthSession, error) {
	var (
		tok  string
		sess model.AuthSession
	)
	if err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		t, s, err := a.mintSessionTx(ctx, as, user, ip, action, amr)
		tok, sess = t, s
		return err
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	return tok, sess, nil
}

// mintSessionTx mints a fresh opaque session for user and audits it INSIDE the
// caller's auth transaction. It is the shared core of mintSession and any flow
// that must activate an account and mint its session atomically (e.g. accepting
// an onboarding invite — onboarding.go). A fresh session is always AAL1; assurance
// is only ever raised by a verified step-up ceremony (ElevateSession).
func (a *Authenticator) mintSessionTx(ctx context.Context, as store.AuthScope, user model.User, ip, action string, amr []string) (string, model.AuthSession, error) {
	cred, err := NewCredential(PrefixSession)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	now := a.clock.Now()
	created, err := as.Sessions().Create(ctx, model.AuthSession{
		UserID:     user.ID,
		Selector:   cred.Selector,
		SecretHash: cred.SecretHash,
		ExpiresAt:  model.NewTimestamp(now.Time().Add(a.sessionTTL)),
		CreatedIP:  ip,
		AAL:        1,
		AMR:        amr,
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
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return "", model.AuthSession{}, ErrUnauthenticated
	}
	cred, err := NewCredential(PrefixSession)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	now := a.clock.Now()
	var sess model.AuthSession
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
		s.Selector = cred.Selector
		s.SecretHash = cred.SecretHash
		s.ExpiresAt = model.NewTimestamp(now.Time().Add(a.sessionTTL))
		updated, err := as.Sessions().Update(ctx, s)
		if err != nil {
			return err
		}
		sess = updated
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.refresh", TargetKind: "core.auth_session", TargetID: s.ID,
		})
		return err
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	return cred.Token, sess, nil
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

// locked reports whether key is currently locked out.
func (a *Authenticator) locked(key string) bool {
	ok, _ := a.throttle.allowed(key)
	return !ok
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
// page bound is a runaway guard far above any real tenant, not a working limit.
// A continuation that cannot make progress, or one that exceeds the bound,
// fails closed and discards the partial result. The caller's q.Limit/q.Cursor
// are overridden (completeness is the point).
func drainList[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error), q model.Query) ([]T, error) {
	const pageSize, maxPages = 1000, 100
	q.Limit = pageSize
	q.Cursor = ""
	var out []T
	seenCursors := make(map[string]struct{}, maxPages)
	for i := 0; i < maxPages; i++ {
		items, page, err := list(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if !page.HasMore {
			return out, nil
		}
		if page.Cursor == "" {
			return nil, errDrainListIncomplete
		}
		if _, seen := seenCursors[page.Cursor]; seen {
			return nil, errDrainListIncomplete
		}
		seenCursors[page.Cursor] = struct{}{}
		q.Cursor = page.Cursor
	}
	return nil, errDrainListIncomplete
}

// normalizeEmail lowercases and trims an email for consistent storage and lookup.
func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
