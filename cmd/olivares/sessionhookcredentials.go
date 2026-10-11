// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

const sessionHookTokenPrefix = hookpep.SessionTokenPrefix

// The hook PEP asks its authenticator for the session plane by type assertion; this keeps a
// signature drift a compile error instead of a PEP that quietly stops masking session secrets.
var _ hookpep.SessionCredentials = (*sessionHookCredentials)(nil)

// freshStopEpoch rides the context of one Mint. The issuer read the stop history
// for this mint and put the answer in the scope, so the validator does not list the
// history a second time for the same launch. A stop engaged between the read and the
// publish leaves a bearer whose epoch is stale: its first use reads the history,
// finds the stop and refuses. A later use of a bearer carries no such value, so it
// always reads the history.
type freshStopEpoch struct {
	tenant   model.TenantID
	agentRef string
	epoch    string
}

type freshStopEpochKey struct{}

// sessionHookCredentials only adapts the shared credential service to the launch
// gate and legacy hook authenticator. It owns no separate identity or token map.
type sessionHookCredentials struct {
	*auth.SessionCredentials
	authr     *auth.Authenticator
	store     store.Store
	stopEpoch func(context.Context, model.TenantID, string) (string, error)
	boundURL  atomic.Value // string, set only after the engine owns the listener
}

func newSessionHookCredentials(a *auth.Authenticator, st store.Store, m *sessions.Module, stops ...killSwitchGuard) *sessionHookCredentials {
	var stopEpoch func(context.Context, model.TenantID, string) (string, error)
	if len(stops) > 0 {
		if history, ok := stops[0].(interface {
			SessionStopEpoch(context.Context, model.TenantID, string) (string, error)
		}); ok {
			stopEpoch = history.SessionStopEpoch
		}
	}
	validate := func(ctx context.Context, scope auth.SessionScope) error {
		fresh, justRead := ctx.Value(freshStopEpochKey{}).(freshStopEpoch)
		if stopEpoch != nil {
			if !justRead || fresh != (freshStopEpoch{scope.TenantID, scope.AgentRef, scope.StopEpoch}) {
				epoch, err := stopEpoch(ctx, scope.TenantID, scope.AgentRef)
				if err != nil || epoch != scope.StopEpoch {
					return auth.ErrUnauthenticated
				}
			}
		} else if len(stops) > 0 && stops[0] != nil {
			state, err := stops[0].KillSwitchState(ctx, scope.TenantID)
			if err != nil {
				return err
			}
			if _, stopped := state.Stopped(scope.AgentRef); stopped {
				return auth.ErrUnauthenticated
			}
		}
		if m == nil {
			return auth.ErrUnauthenticated
		}
		lease, active, err := m.ActiveClaim(ctx, scope.TenantID, scope.SessionRef)
		if err != nil || !active || lease.Fence != scope.Fence || lease.Holder != scope.Holder {
			return auth.ErrUnauthenticated
		}
		return nil
	}
	var credentials *sessionHookCredentials
	credentials = &sessionHookCredentials{SessionCredentials: auth.NewSessionCredentials(a, validate, func(ctx context.Context, scope auth.SessionScope, user string) error {
		if m == nil {
			return auth.ErrUnauthenticated
		}
		// Retiring the owned process must outlive a canceled hook/approval request.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _, err := credentials.CheckOwnerAccess(stopCtx, scope.TenantID, scope.RunRef)
		if errors.Is(err, auth.ErrSessionAccessEnded) {
			return m.StopForAccessEnded(stopCtx, scope, user)
		}
		if errors.Is(err, auth.ErrSessionCredentialExpired) {
			return m.StopForCredentialExpiry(stopCtx, scope)
		}
		if err != nil {
			return err
		}
		return m.StopForAccessChange(stopCtx, scope, user)
	}), authr: a, store: st, stopEpoch: stopEpoch}
	if m != nil {
		m.SessionAccessCheck = credentials.CheckOwnerAccess
	}
	return credentials
}

func (e *engine) hookCredentials() *sessionHookCredentials {
	if e.sessionHooks == nil {
		e.sessionHooks = newSessionHookCredentials(e.authr, e.store, e.sessionsMod, e.killSwitch)
	}
	return e.sessionHooks
}

func (c *sessionHookCredentials) provisioner() *sessionPEPProvisioner {
	return &sessionPEPProvisioner{endpoint: c.endpoint, mintLaunch: c.mint}
}

func (c *sessionHookCredentials) endpoint() string {
	endpoint, _ := c.boundURL.Load().(string)
	return endpoint
}

// bindEndpoint uses the socket the engine actually acquired, never a configured
// port that another process may own. A launch before binding refuses explicitly.
func (c *sessionHookCredentials) bindEndpoint(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" || port == "0" {
		return errors.New("session hook listener has no bound address")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	scheme := "https"
	if hostIsLoopback(addr) {
		scheme = "http"
	}
	c.boundURL.Store(scheme + "://" + addr + "/")
	return nil
}

func (c *sessionHookCredentials) mint(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (string, error) {
	return c.mintForPrincipalContext(ctx, intent.LauncherPrincipal, tenant, intent)
}

func (c *sessionHookCredentials) mintForPrincipal(p auth.Principal, tenant model.TenantID, intent sessions.LaunchIntent) (string, error) {
	return c.mintForPrincipalContext(context.Background(), p, tenant, intent)
}

func (c *sessionHookCredentials) mintForPrincipalContext(ctx context.Context, p auth.Principal, tenant model.TenantID, intent sessions.LaunchIntent) (string, error) {
	if p.Actor() != intent.Actor {
		return "", auth.ErrUnauthenticated
	}
	// The canonical session, not the caller's hint, supplies the authz workspace.
	var workspace model.ID
	err := c.store.View(ctx, tenant, func(sc store.Scope) error {
		sid, err := sessions.ReadSessionIdentityInScope(ctx, sc, intent.ClaimSID)
		if err != nil {
			return err
		}
		workspace = sid.WorkspaceID
		if workspace.IsZero() {
			defaultWorkspace, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return err
			}
			workspace = defaultWorkspace.ID
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	var epoch string
	if c.stopEpoch != nil {
		epoch, err = c.stopEpoch(ctx, tenant, intent.AgentRef)
		if err != nil {
			return "", err
		}
		ctx = context.WithValue(ctx, freshStopEpochKey{}, freshStopEpoch{tenant, intent.AgentRef, epoch})
	}
	return c.Mint(ctx, p, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: firstNonEmptyStr(intent.WorkspaceRef, intent.RunRef), FolderPath: intent.FolderPath, SessionRef: intent.ClaimSID, RunRef: intent.RunRef, AgentRef: intent.AgentRef, Holder: intent.Holder, Fence: intent.Fence, StopEpoch: epoch, Preset: intent.Preset(), AllowedTools: intent.AllowedTools,
		SecretEnv: strings.Join(secretEnvNames(intent.SecretEnv), ",")})
}

func (c *sessionHookCredentials) Authenticate(ctx context.Context, token string) (auth.Principal, error) {
	if strings.HasPrefix(token, sessionHookTokenPrefix) {
		return c.SessionCredentials.Authenticate(ctx, token)
	}
	return c.authr.Authenticate(ctx, token)
}

// ResolvesStopHistory reports whether resolving this credential already checks the live stop history.
func (c *sessionHookCredentials) ResolvesStopHistory() bool { return c.stopEpoch != nil }
