// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Login-enforcement capability: classified once before election, reconciled by an absent
// follower, recorded once at promotion, and asserted against the wired policy before the
// API is built.
//
// The host selection outranks the linked component. An operator whose replacement
// artifact still carries the enforcer keeps the documented break-glass; with the
// precedence reversed, an explicit falsey value on a linked build would classify Wired and
// the recovery path after a downgrade would disappear.
//
// Capability is never probed by calling newLoginPolicy: the private identity-scale factory
// returns nil for a nil federation service by design, so a nil result cannot separate "no
// component" from "no posture to read". loginEnforcementComponentLinked() answers it, and
// every build resolves exactly one implementation beside its own factory.
//
// Licence validity is not capability. enterprise/ssoenforce reads no licence claim, so a
// lapsed licence leaves a linked component enforcing.

// envLoginEnforcement is the single host variable this capability reads.
const envLoginEnforcement = "OLIVARES_LOGIN_ENFORCEMENT"

// The durable operator-recovery record's stable values. The capability names the
// global/default login-enforcement control the host selection applied to; it is metadata,
// not a target entity, because this event records the engine applying a host selection
// and attests no authenticated user identity.
const (
	auditLoginRecovery           = "auth.login_enforcement.operator_recovery"
	auditLoginRecoveryActor      = "host_operator"
	auditLoginRecoveryCapability = "global/default/login-enforcement"
)

// loginEnforcementDisabled reports whether OLIVARES_LOGIN_ENFORCEMENT names an explicit
// OFF value (the operator break-glass). strconv.ParseBool does not accept off/no, so the
// falsey set is matched explicitly; unset, on, 1, true and any other value enforce.
//
// This is the only copy. The private policy factory and the classifier below both read it,
// and a second copy could drift so that one wires a nil policy while the other records
// Wired.
func loginEnforcementDisabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no", "disabled":
		return true
	}
	return false
}

// loginCapabilityBoot carries the declared state from pre-election classification to
// promotion and to the post-wiring assertion. It owns no resource and opens nothing, so it
// adds no startup cleanup path.
type loginCapabilityBoot struct {
	state auth.LoginComponentState
	// linked stays a distinct diagnostic fact: DisabledByOperator hides component
	// presence, and presence must remain separately legible.
	linked bool
	// artifactVersion is recorded with a Wired observation. It is never an entitlement.
	artifactVersion string

	// Follower snapshot, taken only when Absent.
	observedPresent bool
	demand          bool
	snapshotTaken   bool
}

// newLoginCapabilityBoot classifies once, before election, from the host selection and
// this artifact's compiled capability. auth.ClassifyLoginComponent is the single
// classifier; this function supplies its two inputs and holds no second algorithm.
func newLoginCapabilityBoot(getenv func(string) string, artifactVersion string) loginCapabilityBoot {
	linked := loginEnforcementComponentLinked()
	return loginCapabilityBoot{
		state:           auth.ClassifyLoginComponent(linked, loginEnforcementDisabled(getenv(envLoginEnforcement))),
		linked:          linked,
		artifactVersion: artifactVersion,
	}
}

// observeFollower is the absent artifact's pre-election reconciliation. It reads the
// stored capability observation and the global/default demand inside ONE AuthView, so the
// two facts cannot straddle a concurrent write, and it never writes.
//
// Only Absent needs it: Wired has nothing to reconcile and DisabledByOperator is a host
// decision no stored row can contradict. Read failures propagate; a failed read is not an
// observation.
func (b *loginCapabilityBoot) observeFollower(ctx context.Context, st store.Store) error {
	if b.state != auth.LoginComponentAbsent {
		return nil
	}
	return st.AuthView(ctx, func(as store.AuthScope) error {
		obs, err := as.LoginCapability().Read(ctx)
		if err != nil {
			return err
		}
		demand, err := auth.GlobalDefaultLoginDemand(ctx, as)
		if err != nil {
			return err
		}
		b.observedPresent = obs.Present
		b.demand = demand
		b.snapshotTaken = true
		return nil
	})
}

// refusesStartup reports whether an absent artifact must refuse: this deployment is known
// to have carried the component AND a global/default demand is configured.
//
// Both halves are required. Pristine Community staging configures a posture with no
// capability history, and refusing it would break a supported first-run path: the posture
// was never enforced there, so nothing was lost. An unobserved snapshot never refuses,
// because "I did not look" is not "there is nothing there".
func (b loginCapabilityBoot) refusesStartup() bool {
	return b.state == auth.LoginComponentAbsent && b.snapshotTaken && b.observedPresent && b.demand
}

// recordAtPromotion runs in the promotion callback, in its own AuthMutate, after the
// EnsureSystemTenant system transaction has completed.
//
// The capability lock is taken first in this transaction, as the store requires: it must
// precede any directory, user-authority or audit lock, so the recovery audit below can
// only be appended after it.
//
//   - Wired records an observation for the actual artifact version.
//   - Absent re-checks known history plus demand under the lock and refuses if both hold.
//   - DisabledByOperator appends the durable recovery record and takes no observation: a
//     disabled artifact must not create a capability observation.
//
// Every failure propagates. A refusal here is a promotion refusal, not a store close.
func (b loginCapabilityBoot) recordAtPromotion(ctx context.Context, st store.Store) error {
	return st.AuthMutate(ctx, func(as store.AuthScope) error {
		obs, err := as.LoginCapability().Lock(ctx)
		if err != nil {
			return err
		}
		switch b.state {
		case auth.LoginComponentWired:
			if _, err := as.LoginCapability().Observe(ctx, b.artifactVersion); err != nil {
				return err
			}
			return nil
		case auth.LoginComponentAbsent:
			demand, err := auth.GlobalDefaultLoginDemand(ctx, as)
			if err != nil {
				return err
			}
			if obs.Present && demand {
				return fmt.Errorf("%w: this deployment recorded the login-enforcement component and a global/default posture is configured, but this artifact does not link it; install an artifact that carries it or clear the posture", errLoginCapabilityRefused)
			}
			return nil
		case auth.LoginComponentDisabledByOperator:
			ev, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor:     auditLoginRecoveryActor,
				ActorKind: model.ActorSystem,
				Action:    auditLoginRecovery,
				Meta: map[string]any{
					"capability":       auditLoginRecoveryCapability,
					"component_state":  b.state.String(),
					"component_linked": b.linked,
					"artifact_version": b.artifactVersion,
				},
			})
			if err != nil {
				return err
			}
			// A zero sequence with a nil error is the store's explicit degrade answer:
			// the evidence was dropped. Returning it refuses the promotion and rolls
			// the transaction back, so a disabled artifact is never promoted on a
			// recovery record that is not in the ledger.
			if ev.Seq <= 0 {
				return fmt.Errorf("%w: the recovery record returned sequence %d", errLoginCapabilityAudit, ev.Seq)
			}
			return nil
		default:
			return fmt.Errorf("%w: %s", errLoginCapabilityUndeclared, b.state)
		}
	})
}

// installAndAssert installs the declared state on both auth services and checks it against
// the policy actually wired, after newLoginPolicy and before the API is constructed.
func (b loginCapabilityBoot) installAndAssert(authn *auth.Authenticator, fed *auth.FederationService) error {
	return auth.InstallLoginComponentState(b.state, authn, fed)
}

var (
	errLoginCapabilityRefused    = errors.New("login enforcement: refusing to start")
	errLoginCapabilityAudit      = errors.New("login enforcement: the operator recovery record was not durable")
	errLoginCapabilityUndeclared = errors.New("login enforcement: the component state was never declared")
)
