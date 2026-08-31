// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

// CommunicationStoreReadinessWitness is the narrow boundary for K3's store
// phase. True means the composition root has proved the complete schema,
// directory epoch, writer-control, tombstone and invariant posture. The module
// cannot derive that proof from an individual repository.
type CommunicationStoreReadinessWitness interface {
	CommunicationStoreReady(context.Context) (bool, error)
}

// CommunicationContentSealerReadinessWitness proves that the bound sealer's
// immutable key snapshot passed its constructor self-test. The witness must be
// implemented by the same object as CommunicationContentSealer; a port without
// this companion proof keeps K3 OFF.
type CommunicationContentSealerReadinessWitness interface {
	CommunicationContentSealerReady(context.Context) (bool, error)
}

// CommunicationPumpReadinessWitness is WP-3's narrow proof that the pump is
// composed and may participate in K3. A nil or false witness keeps handlers and
// credential activation OFF, and the pump itself must not claim work.
type CommunicationPumpReadinessWitness interface {
	CommunicationPumpReady(context.Context) (bool, error)
}

// CommunicationReadinessDependency is a closed diagnostic vocabulary. It is
// safe to expose because it contains no provider, key or directory details.
type CommunicationReadinessDependency string

const (
	CommunicationReadinessStore       CommunicationReadinessDependency = "store"
	CommunicationReadinessIssuer      CommunicationReadinessDependency = "issuer"
	CommunicationReadinessSealer      CommunicationReadinessDependency = "sealer"
	CommunicationReadinessResolver    CommunicationReadinessDependency = "resolver"
	CommunicationReadinessPermissions CommunicationReadinessDependency = "permissions"
	CommunicationReadinessPump        CommunicationReadinessDependency = "pump"
)

var communicationReadinessDependencyOrder = [...]CommunicationReadinessDependency{
	CommunicationReadinessStore,
	CommunicationReadinessIssuer,
	CommunicationReadinessSealer,
	CommunicationReadinessResolver,
	CommunicationReadinessPermissions,
	CommunicationReadinessPump,
}

// CommunicationReadinessComponents records each term of the effective K3
// conjunction. Keeping every term explicit prevents a new port from being
// silently treated as implied by schema or by the credential issuer.
type CommunicationReadinessComponents struct {
	StoreReady       bool `json:"store_ready"`
	IssuerReady      bool `json:"issuer_ready"`
	SealerReady      bool `json:"sealer_ready"`
	ResolverReady    bool `json:"resolver_ready"`
	PermissionsReady bool `json:"permissions_ready"`
	PumpReady        bool `json:"pump_ready"`
}

func (c CommunicationReadinessComponents) ready(dependency CommunicationReadinessDependency) bool {
	switch dependency {
	case CommunicationReadinessStore:
		return c.StoreReady
	case CommunicationReadinessIssuer:
		return c.IssuerReady
	case CommunicationReadinessSealer:
		return c.SealerReady
	case CommunicationReadinessResolver:
		return c.ResolverReady
	case CommunicationReadinessPermissions:
		return c.PermissionsReady
	case CommunicationReadinessPump:
		return c.PumpReady
	default:
		return false
	}
}

// CommunicationReadiness is the typed two-phase result consumed by future WP-2
// handlers and WP-3's pump. Any non-ready result is UNKNOWN/503, never a policy
// denial and never permission to issue a communication-session credential.
type CommunicationReadiness struct {
	Verdict          AssessmentVerdict                  `json:"verdict"`
	Code             string                             `json:"code"`
	Effective        bool                               `json:"effective"`
	StoreReady       bool                               `json:"store_ready"`
	CompositionReady bool                               `json:"composition_ready"`
	Components       CommunicationReadinessComponents   `json:"components"`
	Missing          []CommunicationReadinessDependency `json:"missing,omitempty"`
	Unavailable      []CommunicationReadinessDependency `json:"unavailable,omitempty"`
}

// EvaluateCommunicationReadiness samples every dynamic witness, then evaluates
// the complete conjunction. It never enables runtime credentials as a side
// effect; activation remains an explicit later rollout action.
func (m *Module) EvaluateCommunicationReadiness(ctx context.Context) (CommunicationReadiness, error) {
	components := CommunicationReadinessComponents{
		IssuerReady: communicationPortBound(m.rt.communicationSessionCreds),
		ResolverReady: communicationPortBound(m.communicationDirectoryResolver) &&
			communicationPortBound(m.communicationAudienceAttestor) &&
			communicationPortBound(m.communicationGrantClosure),
		// The term watches the DIRECT BINDER, not the two CoreEntity* ports.
		// Adjudicated 2026-08-26 on a measurement from this lane, and the reason is
		// that those ports CANNOT authorize faithfully: AuthorizeEntityRead and
		// AuthorizeEntityOperation receive a CommunicationPrincipal, which carries
		// identity and nothing else -- no role, no membership, no AAL, no CredID --
		// while Authorizer.Authorize decides through rbacAllows, which needs the
		// roles. An adapter would have to rebuild a principal with no roles (denies
		// everything) or invent the missing attributes (grants too much), and it
		// would also have to INVENT the AAL: evaluated at AAL1 it under-permits a
		// session that did step up, at AAL3 it over-permits one that did not.
		//
		// The bundle is faithful: it resolves the REAL principal from its
		// PrincipalRef (ResolvePrincipalScope) and asks the same authorizer that
		// gates the rest of the product. It is indivisible by construction --
		// useCommunicationRequestAuthoritySources stores nil when either half is
		// missing -- so one pointer check proves both halves.
		//
		// The two ports are NOT deleted: they remain an optional seam for an
		// external PDP that authorizes by identity attributes, which is the only
		// thing their signature supports. They are simply out of this term.
		PermissionsReady: communicationPermissionsReady(m.Permissions()) &&
			communicationPortBound(m.communicationAuthoritySources),
	}

	var unavailable []CommunicationReadinessDependency
	var witnessErrors []error
	if communicationPortBound(m.communicationStoreReadiness) {
		ready, err := m.communicationStoreReadiness.CommunicationStoreReady(ctx)
		components.StoreReady = ready && err == nil
		if err != nil {
			unavailable = append(unavailable, CommunicationReadinessStore)
			witnessErrors = append(witnessErrors, fmt.Errorf("communication store readiness: %w", err))
		}
	}
	if communicationPortBound(m.communicationSealer) {
		sealerReadiness, ok := m.communicationSealer.(CommunicationContentSealerReadinessWitness)
		if ok && communicationPortBound(sealerReadiness) {
			ready, err := sealerReadiness.CommunicationContentSealerReady(ctx)
			components.SealerReady = ready && err == nil
			if err != nil {
				unavailable = append(unavailable, CommunicationReadinessSealer)
				witnessErrors = append(witnessErrors,
					fmt.Errorf("communication content sealer readiness: %w", err))
			}
		}
	}
	if communicationPortBound(m.communicationPumpReadiness) {
		ready, err := m.communicationPumpReadiness.CommunicationPumpReady(ctx)
		components.PumpReady = ready && err == nil
		if err != nil {
			unavailable = append(unavailable, CommunicationReadinessPump)
			witnessErrors = append(witnessErrors, fmt.Errorf("communication pump readiness: %w", err))
		}
	}

	result := evaluateCommunicationReadiness(components)
	result.Unavailable = unavailable
	if len(unavailable) != 0 {
		result.Code = "communication_readiness_unavailable"
	}
	return result, errors.Join(witnessErrors...)
}

func evaluateCommunicationReadiness(components CommunicationReadinessComponents) CommunicationReadiness {
	result := CommunicationReadiness{
		Verdict:    VerdictUnknown,
		Code:       "communication_not_ready",
		StoreReady: components.StoreReady,
		Components: components,
		Missing:    make([]CommunicationReadinessDependency, 0),
	}
	for _, dependency := range communicationReadinessDependencyOrder {
		if !components.ready(dependency) {
			result.Missing = append(result.Missing, dependency)
		}
	}
	result.CompositionReady = components.IssuerReady &&
		components.SealerReady &&
		components.ResolverReady &&
		components.PermissionsReady &&
		components.PumpReady
	result.Effective = result.StoreReady && result.CompositionReady
	if result.Effective {
		result.Verdict = VerdictClean
		result.Code = "communication_ready"
	}
	return result
}

// communicationPortBound also rejects an interface containing a typed nil.
// Late binding a typed nil must never turn an absent dependency into readiness.
func communicationPortBound(port any) bool {
	if port == nil {
		return false
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}
