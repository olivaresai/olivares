// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Login enforcement component state (R5, ROOT-CONSTRUCTION-R5-1 §5). The store keeps a
// durable observation that an artifact declared the login enforcement component
// available at writer promotion (store.LoginCapabilityPort). This file is the auth side
// of that protocol: the declared component state of THIS process, the one configured
// posture predicate, and the transaction guards the participating callers place first.
//
// Lock order inside one auth transaction is L (capability), then D (directory and user
// authority: Users.Update, Sessions.Create), then tenant audit. The store refuses a
// capability lock requested after D or audit and poisons the transaction, so every
// guard below runs before the first directory, user-authority or audit operation.
//
// Scope. Only NEW sessions are guarded. Refresh, elevation and revocation of existing
// sessions keep their behavior. SSO JIT provisioning (findOrProvision) runs in its own
// earlier transaction, so a refused SSO completion can leave a provisioned local user
// without a session.

// LoginComponentState is the declared login enforcement component state of this
// process. The zero value is LoginComponentUnset.
type LoginComponentState uint32

const (
	// LoginComponentUnset is the compatibility state for library and test constructors
	// that never declare a state. It adds no session guard. The ordinary boot path must
	// replace it (InstallLoginComponentState refuses it).
	LoginComponentUnset LoginComponentState = iota
	// LoginComponentWired means the artifact links the component and the actual login
	// policy is wired. Logins keep that policy's evaluation.
	LoginComponentWired
	// LoginComponentAbsent means this artifact does not link the component and the host
	// operator did not select break-glass. New sessions and global/default posture writes
	// refuse when a capability observation is recorded and the global/default posture is
	// configured.
	LoginComponentAbsent
	// LoginComponentDisabledByOperator means the host operator selected an explicit falsey
	// OLIVARES_LOGIN_ENFORCEMENT value (host break-glass), whether or not the artifact
	// links the component. No policy is wired and no session guard applies.
	LoginComponentDisabledByOperator
)

// String returns the stable lower-case name of the state.
func (s LoginComponentState) String() string {
	switch s {
	case LoginComponentUnset:
		return "unset"
	case LoginComponentWired:
		return "wired"
	case LoginComponentAbsent:
		return "absent"
	case LoginComponentDisabledByOperator:
		return "disabled_by_operator"
	}
	return fmt.Sprintf("unknown(%d)", uint32(s))
}

// refusesRecordedDemand reports whether the state applies the absent-component guard.
// Every value other than the three known non-guarding states guards, so an out-of-range
// value fails closed.
func (s LoginComponentState) refusesRecordedDemand() bool {
	switch s {
	case LoginComponentUnset, LoginComponentWired, LoginComponentDisabledByOperator:
		return false
	}
	return true
}

// ErrLoginEnforcementComponentAbsent is returned when this artifact does not link the
// login enforcement component, the deployment database records that a previous artifact
// declared it, and the global/default posture is configured. It refuses a new session or
// a posture write that would configure enforcement this build cannot apply. It is not a
// product or seat limit: an operator clears it by running an artifact with the component
// or by reducing the global/default posture.
var ErrLoginEnforcementComponentAbsent = errors.New("auth: login_enforcement_unavailable: this deployment has configured login enforcement recorded for an enforcing build, and this build cannot enforce it")

// ErrLoginComponentState is returned when a declared login component state is unset,
// unknown, or disagrees with the wired login policy.
var ErrLoginComponentState = errors.New("auth: login component state does not match the wired login policy")

// errLoginCapabilityPortMissing reports a store whose auth scope returned no capability
// port. It fails the transaction instead of skipping the lock.
var errLoginCapabilityPortMissing = errors.New("auth: the store returned no login capability port")

// LoginPostureConfigured is the single configured-posture predicate for the
// global/default login policy: require-SSO or a non-empty network allow-list. Boot, auth
// and Enterprise wiring share it instead of restating the rule.
func LoginPostureConfigured(requireSSO bool, networkAllowCIDRs []string) bool {
	return requireSSO || len(networkAllowCIDRs) > 0
}

// ClassifyLoginComponent derives the declared state from the same two facts the boot
// path uses for policy wiring: whether this artifact links the component, and whether
// the host operator selected an explicit falsey OLIVARES_LOGIN_ENFORCEMENT value. The
// value parsing stays with the shared command parser; this function never re-parses it.
//
// The host selection has precedence (ROOT-R5-BOOT-CLARIFICATION-1): an explicit falsey
// value is DisabledByOperator whether the component is linked or not, so the documented
// recovery path survives an artifact change that drops the component. Otherwise a linked
// component is Wired, and an artifact without it is Absent.
func ClassifyLoginComponent(linked, disabledByOperator bool) LoginComponentState {
	switch {
	case disabledByOperator:
		return LoginComponentDisabledByOperator
	case linked:
		return LoginComponentWired
	default:
		return LoginComponentAbsent
	}
}

// CheckLoginComponentState reports whether state agrees with enforcesLogin
// (Authenticator.EnforcesLogin): Wired requires a wired policy, Absent and
// DisabledByOperator require none, and Unset or an unknown value is refused.
func CheckLoginComponentState(state LoginComponentState, enforcesLogin bool) error {
	switch state {
	case LoginComponentWired:
		if enforcesLogin {
			return nil
		}
	case LoginComponentAbsent, LoginComponentDisabledByOperator:
		if !enforcesLogin {
			return nil
		}
	case LoginComponentUnset:
		return fmt.Errorf("%w: the state is %s", ErrLoginComponentState, state)
	default:
		return fmt.Errorf("%w: the state is %s", ErrLoginComponentState, state)
	}
	return fmt.Errorf("%w: the state is %s and enforces_login is %t", ErrLoginComponentState, state, enforcesLogin)
}

// InstallLoginComponentState asserts that state agrees with authn's wired login policy
// and then installs it on both auth services. It installs nothing when the assertion
// fails. The boot path calls it after the actual policy wiring and before exposing
// authentication endpoints.
func InstallLoginComponentState(state LoginComponentState, authn *Authenticator, fed *FederationService) error {
	if authn == nil || fed == nil {
		return fmt.Errorf("%w: both the authenticator and the federation service are required", ErrLoginComponentState)
	}
	if err := CheckLoginComponentState(state, authn.EnforcesLogin()); err != nil {
		return err
	}
	fed.WithLoginComponentState(state)
	authn.WithLoginComponentState(state)
	return nil
}

// loginComponentCell holds a declared state. It is atomic because the boot path installs
// the state after leader election has started goroutines that may already use a service.
type loginComponentCell struct{ v atomic.Uint32 }

func (c *loginComponentCell) load() LoginComponentState   { return LoginComponentState(c.v.Load()) }
func (c *loginComponentCell) store(s LoginComponentState) { c.v.Store(uint32(s)) }

// WithLoginComponentState sets the declared login component state and returns the
// Authenticator for chaining. It does not change the wired LoginPolicy. Use
// InstallLoginComponentState on the boot path so the state is asserted first.
func (a *Authenticator) WithLoginComponentState(s LoginComponentState) *Authenticator {
	a.loginComponent.store(s)
	return a
}

// LoginComponentState returns the declared login component state.
func (a *Authenticator) LoginComponentState() LoginComponentState { return a.loginComponent.load() }

// AssertLoginComponentState reports whether the declared state agrees with the wired
// login policy (CheckLoginComponentState).
func (a *Authenticator) AssertLoginComponentState() error {
	return CheckLoginComponentState(a.LoginComponentState(), a.EnforcesLogin())
}

// WithLoginComponentState sets the declared login component state and returns the
// service for chaining.
func (s *FederationService) WithLoginComponentState(st LoginComponentState) *FederationService {
	s.loginComponent.store(st)
	return s
}

// LoginComponentState returns the declared login component state.
func (s *FederationService) LoginComponentState() LoginComponentState {
	return s.loginComponent.load()
}

// lockLoginCapability takes the store-owned capability lock L and returns the observation
// read under it. Callers place it before any directory, user-authority or audit operation
// in the transaction. It is idempotent within one transaction.
func lockLoginCapability(ctx context.Context, as store.AuthScope) (store.LoginCapabilityObservation, error) {
	port := as.LoginCapability()
	if port == nil {
		return store.LoginCapabilityObservation{}, errLoginCapabilityPortMissing
	}
	return port.Lock(ctx)
}

// GlobalDefaultLoginDemand reports, inside as, whether the stored global/default
// federation config has a configured posture (LoginPostureConfigured). It reads the same
// row and fields as FederationService.Posture, so boot promotion and the follower
// snapshot evaluate demand in their own transaction without restating the rule. A
// missing row is no demand.
func GlobalDefaultLoginDemand(ctx context.Context, as store.AuthScope) (bool, error) {
	rows, err := drainList(ctx, as.FederationConfigs().List, byEq("target_tenant_id", GlobalFederationScope.String(), 0))
	if err != nil {
		return false, err
	}
	cfg, ok := configByAlias(rows, model.DefaultFederationAlias)
	if !ok {
		return false, nil
	}
	return postureFromConfig(cfg).Configured(), nil
}

// guardNewLoginSession is the absent-component guard for a new session. For any state
// other than Absent it does nothing and takes no lock. For Absent it takes L, reads the
// observation and the global/default posture in the same transaction, and refuses when
// both are present. It is idempotent, so the session create seam repeats it as defense;
// a late call after a directory or audit lock fails with the store's lock-order error.
func (a *Authenticator) guardNewLoginSession(ctx context.Context, as store.AuthScope) error {
	if !a.LoginComponentState().refusesRecordedDemand() {
		return nil
	}
	obs, err := lockLoginCapability(ctx, as)
	if err != nil {
		return err
	}
	if !obs.Present {
		return nil
	}
	demand, err := GlobalDefaultLoginDemand(ctx, as)
	if err != nil {
		return err
	}
	if demand {
		return ErrLoginEnforcementComponentAbsent
	}
	return nil
}

// guardConfigWrite takes L first for a federation config write, whatever the state. For
// Absent it refuses when a capability observation is recorded and next is the
// global/default config with a configured posture. Callers that only reduce demand pass
// nil for next.
func (s *FederationService) guardConfigWrite(ctx context.Context, as store.AuthScope, next *model.FederationConfig) error {
	obs, err := lockLoginCapability(ctx, as)
	if err != nil {
		return err
	}
	if next == nil || !s.LoginComponentState().refusesRecordedDemand() || !obs.Present {
		return nil
	}
	if next.TargetTenantID != GlobalFederationScope || next.Alias != model.NormalizeFederationAlias(model.DefaultFederationAlias) {
		return nil
	}
	if postureFromConfig(*next).Configured() {
		return ErrLoginEnforcementComponentAbsent
	}
	return nil
}
