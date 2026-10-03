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

// ErrSessionAccessChanged refuses a generation whose current directory subjects
// differ from its launch. Resuming must mint from the current launcher authority.
var ErrSessionAccessChanged = fmt.Errorf("%w: session group closure changed", ErrUnauthenticated)

// SessionAccessChangeHandler retires the exact revoked scope through its owning
// runtime. The issuer calls it at most once, outside its credential-map lock.
type SessionAccessChangeHandler func(context.Context, SessionScope, string) error

type sessionCredential struct {
	launcher PrincipalRef
	ceiling  Principal
	scope    SessionScope
	expires  time.Time
	revoked  bool
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
	p, err := c.refresh(ctx, ref)
	if err != nil {
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
	c.entries[digest] = sessionCredential{launcher: ref, ceiling: launcher, scope: scope, expires: now.Add(24 * time.Hour)}
	c.runs[key] = digest
	return token, nil
}

// Resolve returns the current confined principal and its immutable scope. For a
// known revoked bearer it returns scope with an error, for denial attribution;
// an error never conveys authority. No credential string is retained.
func (c *SessionCredentials) Resolve(ctx context.Context, token string) (Principal, SessionScope, error) {
	return c.resolve(ctx, sha256.Sum256([]byte(token)))
}

func (c *SessionCredentials) Authenticate(ctx context.Context, token string) (Principal, error) {
	p, _, err := c.Resolve(ctx, token)
	return p, err
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
	return c.resolve(ctx, hash)
}

// CheckOwnerAccess is the runtime's active lifecycle check, independent of tool
// activity. It refreshes the original launch credential through its existing
// native reconstruction and returns only the captured scope
// for teardown attribution, never a reconstructed principal or launch authority.
// The caller must stop through that exact scope on ErrSessionAccessEnded.
func (c *SessionCredentials) CheckOwnerAccess(ctx context.Context, tenant model.TenantID, runRef string) (SessionScope, string, error) {
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
	p, err := c.refresh(ctx, item.launcher)
	ended := errors.Is(err, ErrUnauthenticated) || errors.Is(err, store.ErrNotFound)
	if err != nil && !ended {
		return scope, user, err
	}
	_, admitted := p.RoleIn(tenant)
	currentFloor, currentHasFloor := p.RetirementFloor(tenant)
	launchFloor, launchHasFloor := item.ceiling.RetirementFloor(tenant)
	ended = ended || p.ExcludedFrom(tenant) || (!p.Superadmin && !admitted) || currentHasFloor != launchHasFloor || currentFloor != launchFloor
	c.mu.Lock()
	current, exists := c.entries[hash]
	same := exists && c.runs[key] == hash
	if same && ended {
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

func (c *SessionCredentials) resolve(ctx context.Context, hash [32]byte) (Principal, SessionScope, error) {
	c.mu.Lock()
	item, ok := c.entries[hash]
	c.mu.Unlock()
	if !ok {
		return Principal{}, SessionScope{}, ErrUnauthenticated
	}
	item.scope.AllowedTools = slices.Clone(item.scope.AllowedTools)
	if item.revoked || !c.authr.clock.Now().Time().Before(item.expires) {
		return Principal{}, item.scope, ErrUnauthenticated
	}
	p, err := c.refresh(ctx, item.launcher)
	user := p.DisplayName
	if user == "" {
		user = item.ceiling.DisplayName
	}
	if user == "" {
		user = item.ceiling.UserID.String()
	}
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, store.ErrNotFound) {
		err = ErrSessionAccessEnded
	}
	if err == nil {
		p, err = narrowSessionPrincipal(p, item.ceiling, item.scope)
	}
	if err == nil {
		validationScope := item.scope
		validationScope.AllowedTools = slices.Clone(item.scope.AllowedTools)
		err = c.validate(ctx, validationScope)
	}
	c.mu.Lock()
	current, exists := c.entries[hash]
	accessLost := errors.Is(err, ErrSessionAccessChanged) || errors.Is(err, ErrSessionAccessEnded)
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
	return p, item.scope, nil
}

func (c *SessionCredentials) refresh(ctx context.Context, ref PrincipalRef) (Principal, error) {
	var p Principal
	err := c.authr.st.AuthView(ctx, func(as store.AuthScope) error {
		if ref.kind == KindUser {
			row, err := as.Sessions().Get(ctx, ref.credentialID)
			if err != nil {
				return err
			}
			if row.Version != ref.version {
				return ErrUnauthenticated
			}
			p, err = c.authr.principalFromSession(ctx, as, row)
			return err
		}
		row, err := as.Tokens().Get(ctx, ref.credentialID)
		if err != nil {
			return err
		}
		if row.Version != ref.version {
			return ErrUnauthenticated
		}
		var found bool
		p, found, err = c.authr.principalFromToken(ctx, as, row)
		if err == nil && !found {
			return ErrUnauthenticated
		}
		return err
	})
	return p, err
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
