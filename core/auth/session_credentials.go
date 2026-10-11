// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SessionScope is the server-resolved boundary of one running agent. WorkspaceID
// is the core authorization workspace; FolderRef/FolderPath name its filesystem
// folder. The caller supplies these from resolved launch state, never hook input.
type SessionScope struct {
	TenantID    model.TenantID
	WorkspaceID model.ID
	FolderRef   string
	FolderPath  string
	SessionRef  string
	RunRef      string
	AgentRef    string
	Holder      string
	Fence       int64
	// StopEpoch is resolved by the issuer from persisted kill-switch history.
	// It prevents a stopped generation from recovering when the stop is cleared.
	StopEpoch string
	// Preset and AllowedTools are resolved from the launch intent and retained
	// only by the engine. The issuer copies the tool surface at mint and resolve.
	Preset       string
	AllowedTools []string
	// SecretEnv names the vault secrets this session was given as environment
	// variables, comma-joined "VARIABLE<-secret", never their values. Every mint
	// reads them from the launch the run row records, so a resume cannot widen them.
	SecretEnv string
}

// SessionValidator checks current run authority and the kill switch. Any error
// refuses the request and revokes the generation, including after re-enabling.
type SessionValidator func(context.Context, SessionScope) error

// ErrSessionAccessEnded identifies an owner whose account or tenant standing
// no longer permits a live run. It conveys no authority to resume.
var ErrSessionAccessEnded = fmt.Errorf("%w: session owner access ended", ErrUnauthenticated)

// ErrSessionOwnerUnbound means this issuer has no owner scope for the run. It
// grants no credential authority and is not evidence of an owner withdrawal.
var ErrSessionOwnerUnbound = fmt.Errorf("%w: session owner is not bound to this issuer", ErrUnauthenticated)

// SessionCredentialExpiryReason is shared by credential denial and its terminal
// runtime record. Expiry requires a new session, without extending this bearer.
const SessionCredentialExpiryReason = "Session credential expired after 24 hours. Start a successor session."

var ErrSessionCredentialExpired = fmt.Errorf("%w: %s", ErrUnauthenticated, SessionCredentialExpiryReason)

// A revision mismatch grants nothing. For an already expired child, it must not
// turn a later parent renewal into withdrawal and hide the deadline guidance.
var errSessionOwnerRevisionChanged = fmt.Errorf("%w: pinned session owner credential revision changed", ErrUnauthenticated)

// ErrSessionAccessChanged refuses a generation whose current directory subjects
// differ from its launch. Resuming must mint from the current launcher authority.
var ErrSessionAccessChanged = fmt.Errorf("%w: session group closure changed", ErrUnauthenticated)

// SessionAccessChangeHandler retires the exact revoked scope through its owning
// runtime. The issuer calls it at most once, outside its credential-map lock.
type SessionAccessChangeHandler func(context.Context, SessionScope, string) error

type sessionCredential struct {
	launcher PrincipalRef
	seal     string
	ceiling  Principal
	scope    SessionScope
	expires  time.Time
	revoked  bool
}

// Retain the issuer's lookup handle, never a bearer or a reusable native ref.
type sessionPrincipalOrigin struct {
	issuer  *SessionCredentials
	digest  [32]byte
	confine bool
}

type sessionRunKey struct {
	tenant model.TenantID
	run    string
}

// SessionCredentials is the one in-process issuer for hooks, session MCP and
// protocol approvals. It stores digests, refreshes launcher authority on use, and
// never admits these credentials to the general admin API. Restart revokes all.
type SessionCredentials struct {
	mu            sync.Mutex
	entries       map[[32]byte]sessionCredential
	runs          map[sessionRunKey][32]byte
	authr         *Authenticator
	validate      SessionValidator
	accessChanged SessionAccessChangeHandler
}

func NewSessionCredentials(a *Authenticator, validate SessionValidator, changed ...SessionAccessChangeHandler) *SessionCredentials {
	c := &SessionCredentials{entries: make(map[[32]byte]sessionCredential), runs: make(map[sessionRunKey][32]byte), authr: a, validate: validate}
	if len(changed) > 0 {
		c.accessChanged = changed[0]
	}
	if a != nil {
		a.ownerRenewal.Lock()
		a.ownerIssuers = append(a.ownerIssuers, c)
		a.ownerRenewal.Unlock()
	}
	return c
}

// Mint creates a new generation and revokes the previous bearer for this run.
// Only principals returned by Authenticate can launch a session credential.
func (c *SessionCredentials) Mint(ctx context.Context, launcher Principal, scope SessionScope) (string, error) {
	scope.AllowedTools = slices.Clone(scope.AllowedTools)
	ref, ok := launcher.Ref()
	if !ok || launcher.IsPurposeRestricted() || scope.TenantID.IsZero() || scope.TenantID.IsSystem() || scope.WorkspaceID.IsZero() || scope.SessionRef == "" || scope.RunRef == "" || scope.FolderRef == "" || c.authr == nil || c.validate == nil {
		return "", ErrUnauthenticated
	}
	c.authr.ownerRenewal.RLock()
	p, _, err := c.refresh(ctx, ref)
	c.authr.ownerRenewal.RUnlock()
	if err != nil {
		if errors.Is(err, errSessionOwnerRevisionChanged) {
			err = ErrUnauthenticated
		}
		return "", err
	}
	if _, err = narrowSessionPrincipal(p, launcher, scope); err != nil {
		return "", err
	}
	validationScope := scope
	validationScope.AllowedTools = slices.Clone(scope.AllowedTools)
	if err = c.validate(ctx, validationScope); err != nil {
		return "", err
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return "", err
	}
	token := "olvsess_" + hex.EncodeToString(entropy[:])
	digest := sha256.Sum256([]byte(token))
	// The runtime validator ran without auth locks. Recheck the exact credential
	// before publishing; a concurrent renewal cannot admit a stale new launch.
	c.authr.ownerRenewal.RLock()
	defer c.authr.ownerRenewal.RUnlock()
	p, seal, err := c.refresh(ctx, ref)
	if err != nil {
		if errors.Is(err, errSessionOwnerRevisionChanged) {
			err = ErrUnauthenticated
		}
		return "", err
	}
	if _, err := narrowSessionPrincipal(p, launcher, scope); err != nil {
		return "", err
	}
	now := c.authr.clock.Now().Time()
	key := sessionRunKey{scope.TenantID, scope.RunRef}
	c.mu.Lock()
	defer c.mu.Unlock()
	for hash, item := range c.entries {
		rk := sessionRunKey{item.scope.TenantID, item.scope.RunRef}
		// The current generation also captures the live run's owner. Bearer
		// expiry refuses credential use in resolve; it must not erase the owner
		// that CheckOwnerAccess still monitors. Prune only superseded entries.
		if !now.Before(item.expires) && c.runs[rk] != hash {
			delete(c.entries, hash)
		}
	}
	if previous, ok := c.runs[key]; ok {
		item := c.entries[previous]
		item.revoked = true
		c.entries[previous] = item
	}
	c.entries[digest] = sessionCredential{launcher: ref, seal: seal, ceiling: launcher, scope: scope, expires: now.Add(24 * time.Hour)}
	c.runs[key] = digest
	return token, nil
}

// Resolve returns the current confined principal and its immutable scope. For a
// known revoked bearer it returns scope with an error, for denial attribution;
// an error never conveys authority. No credential string is retained.
func (c *SessionCredentials) Resolve(ctx context.Context, token string) (Principal, SessionScope, error) {
	return c.resolve(ctx, sha256.Sum256([]byte(token)), true)
}

func (c *SessionCredentials) Authenticate(ctx context.Context, token string) (Principal, error) {
	p, _, err := c.Resolve(ctx, token)
	return p, err
}

// AuthenticateLauncher resolves a session bearer as Authenticate does, with the
// same lifecycle checks, but to its launcher's own rights in the session's
// tenant: the launcher's workspace confinement, if it had one, not the session's.
// Every other bound stays: the role and assurance at launch, the same group
// closure, no superadmin, the session's tenant, identity and lifetime. The session
// configuration tools act with it, so a session configures what its user could
// configure in the console and is refused what the console would refuse.
func (c *SessionCredentials) AuthenticateLauncher(ctx context.Context, token string) (Principal, error) {
	p, _, err := c.resolve(ctx, sha256.Sum256([]byte(token)), false)
	return p, err
}

// LauncherForRun returns the launcher of the caller's live session run as
// the launcher's own exact credential, after every check the run's bearer
// passes, for that exact generation (session, run and fence). It carries no
// session identity and no session narrowing, so a module that refuses session
// credentials admits it as that person. The run's checks refuse a launcher
// whose authority changed since the launch, a promotion included: a new
// session carries the new authority. A module rebuilds its caller from the
// returned credential, so capping the principal would not hold: any change of
// tenant role, superadmin flag or workspace confinement, or a higher assurance,
// is ErrSessionAccessEnded. It is an in-process API for one caller, the session
// publish proposal, after a person approved that exact effect; a session never
// holds it.
func (c *SessionCredentials) LauncherForRun(ctx context.Context, session Principal) (Principal, error) {
	tenant := session.SessionScope()
	c.mu.Lock()
	hash, ok := c.runs[sessionRunKey{tenant, session.SessionRunRef}]
	item, exists := c.entries[hash]
	c.mu.Unlock()
	if !ok || !exists || session.SessionIdentity == "" || item.scope.SessionRef != session.SessionIdentity || item.scope.Fence != session.SessionFence {
		return Principal{}, ErrUnauthenticated
	}
	if _, _, err := c.resolve(ctx, hash, true); err != nil {
		return Principal{}, err
	}
	c.authr.ownerRenewal.RLock()
	p, seal, err := c.refresh(ctx, item.launcher)
	c.authr.ownerRenewal.RUnlock()
	if err == nil && seal != item.seal {
		err = ErrUnauthenticated
	}
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, store.ErrNotFound) {
		return Principal{}, ErrSessionAccessEnded
	}
	if err != nil {
		return Principal{}, err
	}
	// The launch bounds hold on the principal returned, not only on the read
	// the run's checks made before it.
	if _, err := narrowSessionPrincipal(p, item.ceiling, item.scope); err != nil {
		return Principal{}, err
	}
	role, _ := p.RoleIn(tenant)
	launchRole, _ := item.ceiling.RoleIn(tenant)
	ws, confined := p.ConfinedWorkspaceIn(tenant)
	launchWS, launchConfined := item.ceiling.ConfinedWorkspaceIn(tenant)
	if role != launchRole || p.Superadmin != item.ceiling.Superadmin || confined != launchConfined || ws != launchWS || p.AAL > item.ceiling.AAL {
		return Principal{}, ErrSessionAccessEnded
	}
	return p, nil
}

// ResolveRun gives the approval service the same live principal without an HTTP
// self-call or a second bearer. It is an in-process API, never a public endpoint.
func (c *SessionCredentials) ResolveRun(ctx context.Context, tenant model.TenantID, runRef string) (Principal, SessionScope, error) {
	c.mu.Lock()
	hash, ok := c.runs[sessionRunKey{tenant, runRef}]
	c.mu.Unlock()
	if !ok {
		return Principal{}, SessionScope{}, ErrUnauthenticated
	}
	return c.resolve(ctx, hash, true)
}

// CheckOwnerAccess is the runtime's active lifecycle check, independent of tool
// activity. It refreshes the original launch credential through its existing
// native reconstruction and returns only the captured scope
// for teardown attribution, never a reconstructed principal or launch authority.
// The caller must stop through that exact scope on ErrSessionAccessEnded or
// ErrSessionCredentialExpired. Expiry revokes bearer authority, not the retained
// owner binding: later proven withdrawal still returns ErrSessionAccessEnded
// for that generation, even after mint cleanup. Proven withdrawal takes
// precedence over expiry.
func (c *SessionCredentials) CheckOwnerAccess(ctx context.Context, tenant model.TenantID, runRef string) (SessionScope, string, error) {
	if c.authr != nil {
		c.authr.ownerRenewal.RLock()
		defer c.authr.ownerRenewal.RUnlock()
	}
	key := sessionRunKey{tenant, runRef}
	c.mu.Lock()
	hash, ok := c.runs[key]
	item, exists := c.entries[hash]
	c.mu.Unlock()
	if !ok || !exists {
		return SessionScope{}, "", ErrSessionOwnerUnbound
	}
	if c.authr == nil {
		return SessionScope{}, "", ErrUnauthenticated
	}
	scope := item.scope
	scope.AllowedTools = slices.Clone(scope.AllowedTools)
	user := item.ceiling.DisplayName
	if user == "" {
		user = item.ceiling.UserID.String()
	}
	// A refused native credential can already have revoked this generation; that
	// must not hide an idle owned process. The same reconstruction supports user
	// logins and standalone tokens, and preserves standing retirement floors.
	p, seal, err := c.refresh(ctx, item.launcher)
	if err == nil && seal != item.seal {
		err = ErrUnauthenticated
	}
	ended := errors.Is(err, ErrUnauthenticated) || errors.Is(err, store.ErrNotFound)
	expired := !c.authr.clock.Now().Time().Before(item.expires)
	if expired && errors.Is(err, errSessionOwnerRevisionChanged) {
		ended = false
	}
	if err != nil && !ended && !expired {
		return scope, user, err
	}
	if err == nil {
		_, admitted := p.RoleIn(tenant)
		currentFloor, currentHasFloor := p.RetirementFloor(tenant)
		launchFloor, launchHasFloor := item.ceiling.RetirementFloor(tenant)
		ended = p.ExcludedFrom(tenant) || (!p.Superadmin && !admitted) || currentHasFloor != launchHasFloor || currentFloor != launchFloor
	}
	c.mu.Lock()
	current, exists := c.entries[hash]
	same := exists && c.runs[key] == hash
	if same && (ended || expired) {
		current.revoked = true
		c.entries[hash] = current
	}
	c.mu.Unlock()
	if !same {
		return scope, user, ErrUnauthenticated
	}
	if ended {
		return scope, user, ErrSessionAccessEnded
	}
	if expired {
		return scope, user, ErrSessionCredentialExpired
	}
	return scope, user, nil
}

func (c *SessionCredentials) Revoke(tenant model.TenantID, runRef string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hash, ok := c.runs[sessionRunKey{tenant, runRef}]; ok {
		item := c.entries[hash]
		item.revoked = true
		c.entries[hash] = item
	}
}

// resolve narrows the launcher's current authority to the session. confine=false
// keeps the launcher's own workspace confinement instead of the session's.
func (c *SessionCredentials) resolve(ctx context.Context, hash [32]byte, confine bool) (Principal, SessionScope, error) {
	if c.authr == nil {
		return Principal{}, SessionScope{}, ErrUnauthenticated
	}
	c.authr.ownerRenewal.RLock()
	c.mu.Lock()
	item, ok := c.entries[hash]
	c.mu.Unlock()
	if !ok {
		c.authr.ownerRenewal.RUnlock()
		return Principal{}, SessionScope{}, ErrUnauthenticated
	}
	item.scope.AllowedTools = slices.Clone(item.scope.AllowedTools)
	if item.revoked {
		c.authr.ownerRenewal.RUnlock()
		return Principal{}, item.scope, ErrUnauthenticated
	}
	var p Principal
	var seal string
	var err error
	if !c.authr.clock.Now().Time().Before(item.expires) {
		err = ErrSessionCredentialExpired
	} else {
		p, seal, err = c.refresh(ctx, item.launcher)
	}
	c.authr.ownerRenewal.RUnlock()
	if err == nil && seal != item.seal {
		err = ErrUnauthenticated
	}
	user := p.DisplayName
	if user == "" {
		user = item.ceiling.DisplayName
	}
	if user == "" {
		user = item.ceiling.UserID.String()
	}
	if !errors.Is(err, ErrSessionCredentialExpired) && (errors.Is(err, ErrUnauthenticated) || errors.Is(err, store.ErrNotFound)) {
		err = ErrSessionAccessEnded
	}
	if err == nil {
		p, err = narrowSessionLauncherPrincipal(p, item.ceiling, item.scope, confine)
	}
	if err == nil {
		validationScope := item.scope
		validationScope.AllowedTools = slices.Clone(item.scope.AllowedTools)
		err = c.validate(ctx, validationScope)
	}
	return c.finishResolve(ctx, hash, item, p, user, confine, err)
}

// finishResolve applies an observed refusal to the captured generation before
// notifying its runtime. Callers release renewal and issuer locks first.
func (c *SessionCredentials) finishResolve(ctx context.Context, hash [32]byte, item sessionCredential, p Principal, user string, confine bool, err error) (Principal, SessionScope, error) {
	c.mu.Lock()
	current, exists := c.entries[hash]
	accessLost := errors.Is(err, ErrSessionAccessChanged) || errors.Is(err, ErrSessionAccessEnded) || errors.Is(err, ErrSessionCredentialExpired)
	notify := exists && !current.revoked && accessLost && c.runs[sessionRunKey{item.scope.TenantID, item.scope.RunRef}] == hash
	if err != nil && exists {
		current.revoked = true
		c.entries[hash] = current
	}
	revoked := !exists || current.revoked
	c.mu.Unlock()
	// Finalization can revoke the same credential; never hold mu over the runtime
	// callback. The captured scope fences a concurrent resume to a new generation.
	if notify && c.accessChanged != nil {
		err = errors.Join(err, c.accessChanged(ctx, item.scope, user))
	}
	if err != nil {
		return Principal{}, item.scope, err
	}
	if revoked {
		return Principal{}, item.scope, ErrUnauthenticated
	}
	p.sessionOrigin = &sessionPrincipalOrigin{issuer: c, digest: hash, confine: confine}
	return p, item.scope, nil
}

// ResolveSessionLauncherScope reconstructs the issuer's exact launcher and
// returns its complete directory evidence separately from the narrowed session
// principal. That evidence fences the source; it is never mutation authority
// attesting that the narrowed principal is an ordinary native credential.
func (a *Authenticator) ResolveSessionLauncherScope(ctx context.Context, p Principal, tenant model.TenantID) (Principal, store.AuthoritySnapshotBundle, time.Time, error) {
	origin := p.sessionOrigin
	if origin == nil || origin.issuer.authr != a {
		return Principal{}, store.AuthoritySnapshotBundle{}, time.Time{}, ErrUnauthenticated
	}
	c := origin.issuer
	if _, _, err := c.resolve(ctx, origin.digest, origin.confine); err != nil {
		return Principal{}, store.AuthoritySnapshotBundle{}, time.Time{}, err
	}
	a.ownerRenewal.RLock()
	c.mu.Lock()
	item, exists := c.entries[origin.digest]
	c.mu.Unlock()
	if !exists || item.revoked || item.scope.TenantID != tenant {
		a.ownerRenewal.RUnlock()
		return Principal{}, store.AuthoritySnapshotBundle{}, time.Time{}, ErrUnauthenticated
	}
	item.scope.AllowedTools = slices.Clone(item.scope.AllowedTools)
	source, err := a.ResolvePrincipalScope(ctx, item.launcher, tenant)
	a.ownerRenewal.RUnlock()
	user := source.DisplayName
	if user == "" {
		user = item.ceiling.DisplayName
	}
	if user == "" {
		user = item.ceiling.UserID.String()
	}
	// Unknown evidence is not an established owner withdrawal. A known native
	// credential loss retains the issuer's ordinary access-ended disposition.
	if !errors.Is(err, ErrPrincipalEvidenceUnavailable) && !errors.Is(err, ErrSessionCredentialExpired) && (errors.Is(err, ErrUnauthenticated) || errors.Is(err, store.ErrNotFound)) {
		err = ErrSessionAccessEnded
	}
	var bundle store.AuthoritySnapshotBundle
	var expiry time.Time
	if err == nil {
		var window evidenceWindow
		var ok bool
		bundle, window, ok = principalCompleteAuthorizationEvidence(source, tenant)
		if !ok {
			err = ErrPrincipalEvidenceUnavailable
		} else {
			expiry = window.freshUntil
			if item.expires.Before(expiry) {
				expiry = item.expires
			}
		}
	}
	var narrowed Principal
	if err == nil {
		narrowed, err = narrowSessionLauncherPrincipal(source, item.ceiling, item.scope, origin.confine)
	}
	if err == nil {
		narrowed.sessionOrigin = origin
		err = narrowed.ValidateSessionLauncher(a.clock.Now().Time(), tenant)
	}
	// Unavailable reconstruction evidence proves no access loss. Deny this call
	// without retiring the child; a retry must reconstruct fresh authority again.
	if errors.Is(err, ErrPrincipalEvidenceUnavailable) {
		return Principal{}, store.AuthoritySnapshotBundle{}, time.Time{}, err
	}
	narrowed, _, err = c.finishResolve(ctx, origin.digest, item, narrowed, user, origin.confine, err)
	if err != nil {
		return Principal{}, store.AuthoritySnapshotBundle{}, time.Time{}, err
	}
	return narrowed, bundle, expiry, nil
}

// AuthorizeSessionLauncher evaluates the issuer-reconstructed, launch-bounded
// principal with the ordinary typed policy algebra. Source authority attests
// this evaluation only; it gives the narrowed principal neither a native
// credential reference nor a native mutation witness. Trusted composition must
// retain the returned window and lock the complete bundle on its mutation.
func (a *Authenticator) AuthorizeSessionLauncher(ctx context.Context, az *Authorizer, req Request) (AuthorizationEvidence, store.AuthoritySnapshotBundle, error) {
	if az == nil {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, ErrAuthorizerUnavailable
	}
	p, source, expiry, err := a.ResolveSessionLauncherScope(ctx, req.Principal, req.Tenant)
	if err != nil {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, err
	}
	req = cloneEvidenceRequest(req)
	req.Principal = p
	if req.Route.RequiresStepUp(ctx, p) {
		az.RecordStepUpRefusal(ctx, req)
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, ErrStepUpRequired
	}
	window := evidenceWindow{}
	if !window.add(p.evidence.observedAt, expiry) {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	// ResolveSessionLauncherScope extracts the complete native source before
	// narrowing; its directory fact is the principal term in the shared fold.
	launcher := sessionLauncherEvidence{fact: source.Facts[0], window: window}
	decision := az.authorizeEvidenceWithPrincipal(ctx, req, nil, &launcher)
	if decision.Outcome != EvidenceAllow {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, DenialFor(decision)
	}
	now := az.clock()
	if now.Before(decision.ObservedAt) || !now.Before(decision.FreshUntil) {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	if err := p.ValidateSessionLauncher(now, req.Tenant); err != nil {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, err
	}
	bundle, err := MergeAuthoritySnapshotBundles(source, store.AuthoritySnapshotBundle{Facts: decision.Facts})
	if err != nil {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, err
	}
	if err := ctx.Err(); err != nil {
		return AuthorizationEvidence{}, store.AuthoritySnapshotBundle{}, err
	}
	return decision, bundle, nil
}

// ValidateSessionLauncher rechecks the issuer's generation and expiry without
// reading the directory. A write calls it after acquiring the source barrier;
// the barrier itself protects the durable launcher and directory facts.
func (p Principal) ValidateSessionLauncher(now time.Time, tenant model.TenantID) error {
	origin := p.sessionOrigin
	if origin == nil {
		return ErrUnauthenticated
	}
	c := origin.issuer
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.entries[origin.digest]
	if !ok || item.revoked || item.scope.TenantID != tenant || c.runs[sessionRunKey{tenant, item.scope.RunRef}] != origin.digest {
		return ErrUnauthenticated
	}
	if !now.Before(item.expires) {
		return ErrSessionCredentialExpired
	}
	return nil
}

func narrowSessionLauncherPrincipal(live, ceiling Principal, scope SessionScope, confine bool) (Principal, error) {
	p, err := narrowSessionPrincipal(live, ceiling, scope)
	if err == nil && !confine {
		_, liveConfined := live.ConfinedWorkspaceIn(scope.TenantID)
		_, launchConfined := ceiling.ConfinedWorkspaceIn(scope.TenantID)
		if !liveConfined && !launchConfined {
			p.confined = nil
		}
	}
	return p, err
}

func (c *SessionCredentials) refresh(ctx context.Context, ref PrincipalRef) (Principal, string, error) {
	var p Principal
	var seal string
	err := c.authr.st.AuthView(ctx, func(as store.AuthScope) error {
		if ref.kind == KindUser {
			row, err := as.Sessions().Get(ctx, ref.credentialID)
			if err != nil {
				return err
			}
			if row.Revoked {
				return ErrUnauthenticated
			}
			if row.Version != ref.version {
				return errSessionOwnerRevisionChanged
			}
			seal = queuedCredentialSeal(row.SecretHash)
			p, err = c.authr.principalFromSession(ctx, as, row)
			return err
		}
		row, err := as.Tokens().Get(ctx, ref.credentialID)
		if err != nil {
			return err
		}
		if row.Revoked {
			return ErrUnauthenticated
		}
		if row.Version != ref.version {
			return errSessionOwnerRevisionChanged
		}
		seal = queuedCredentialSeal(row.SecretHash)
		var found bool
		p, found, err = c.authr.principalFromToken(ctx, as, row)
		if err == nil && !found {
			return ErrUnauthenticated
		}
		return err
	})
	return p, seal, err
}

func narrowSessionPrincipal(p, ceiling Principal, scope SessionScope) (Principal, error) {
	tenant := scope.TenantID
	if p.IsPurposeRestricted() || ceiling.ExcludedFrom(tenant) || (!p.SessionScope().IsZero() && p.SessionScope() != tenant) {
		return Principal{}, ErrUnauthenticated
	}
	role, ok := p.RoleIn(tenant)
	if p.Superadmin {
		role, ok = RoleOwner, true
	}
	currentFloor, currentHasFloor := p.RetirementFloor(tenant)
	launchFloor, launchHasFloor := ceiling.RetirementFloor(tenant)
	if !ok || p.ExcludedFrom(tenant) || currentHasFloor != launchHasFloor || currentFloor != launchFloor {
		return Principal{}, ErrSessionAccessEnded
	}
	currentGroups, launchGroups := p.GroupsIn(tenant), ceiling.GroupsIn(tenant)
	slices.Sort(currentGroups)
	slices.Sort(launchGroups)
	if !slices.Equal(slices.Compact(currentGroups), slices.Compact(launchGroups)) {
		return Principal{}, ErrSessionAccessChanged
	}
	limit, admitted := ceiling.RoleIn(tenant)
	if ceiling.Superadmin {
		limit, admitted = RoleOwner, true
	}
	if !ok || !admitted {
		return Principal{}, ErrUnauthenticated
	}
	if RoleRank(role) > RoleRank(limit) {
		role = limit
	}
	for _, parent := range []Principal{p, ceiling} {
		if ws, confined := parent.ConfinedWorkspaceIn(tenant); confined && ws != scope.WorkspaceID {
			return Principal{}, ErrUnauthenticated
		}
	}
	p = p.withSessionScope(tenant)
	p.grants = map[model.TenantID]string{tenant: role}
	// The subject closure must stay identical to launch. Intersecting subjects
	// can hide a newly joined group's live forbid; carrying additions can widen
	// grants. Refuse the generation so resume can mint the current closure.
	p.groups = map[model.TenantID][]string{tenant: p.GroupsIn(tenant)}
	p.confined = map[model.TenantID]model.ID{tenant: scope.WorkspaceID}
	if p.AAL > ceiling.AAL {
		p.AAL = ceiling.AAL
	}
	p.credentialRef = PrincipalRef{}
	p.AgentIdentity = scope.AgentRef
	p.SessionIdentity = scope.SessionRef
	p.SessionWorkspaceID = scope.WorkspaceID
	p.SessionRunRef = scope.RunRef
	p.SessionFence = scope.Fence
	return p, nil
}
