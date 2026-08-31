// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// ErrProtocolBindingSpecUnsupported lets a composition-owned validator decline
// a route so another validator for the same protocol can prove it. It never
// means that remote evidence was observed and failed.
var ErrProtocolBindingSpecUnsupported = errors.New("sessions: protocol binding spec route unsupported")

// ProtocolBindingSpecValidator is the composition-owned K5 capability reader.
// It may perform authenticated, non-actuating peer reads. It must not create a
// remote task or write local state.
type ProtocolBindingSpecValidator interface {
	ValidateProtocolBindingSpec(
		context.Context,
		model.TenantID,
		ProtocolBindingSpecInput,
	) (ProtocolBindingValidation, error)
}

// UseProtocolBindingSpecValidator late-binds the one validator for a protocol.
// Passing nil removes the validator and returns that protocol to UNKNOWN/OFF.
func (m *Module) UseProtocolBindingSpecValidator(
	protocol BindingProtocol,
	validator ProtocolBindingSpecValidator,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.protocolBindingSpecValidators == nil {
		m.protocolBindingSpecValidators = make(map[BindingProtocol][]ProtocolBindingSpecValidator)
	}
	if validator == nil {
		delete(m.protocolBindingSpecValidators, protocol)
		return
	}
	m.protocolBindingSpecValidators[protocol] = []ProtocolBindingSpecValidator{validator}
}

// AddProtocolBindingSpecValidator adds another independently provisioned route
// for a protocol. This is used when one binary hosts several MCP Resource
// Servers or both outbound and inbound A2A authorities.
func (m *Module) AddProtocolBindingSpecValidator(
	protocol BindingProtocol,
	validator ProtocolBindingSpecValidator,
) {
	if validator == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.protocolBindingSpecValidators == nil {
		m.protocolBindingSpecValidators = make(map[BindingProtocol][]ProtocolBindingSpecValidator)
	}
	m.protocolBindingSpecValidators[protocol] = append(
		m.protocolBindingSpecValidators[protocol], validator,
	)
}

func (m *Module) validateProtocolBindingSpecCapability(
	ctx context.Context,
	tenant model.TenantID,
	input ProtocolBindingSpecInput,
) ProtocolBindingValidation {
	if local := m.validateProtocolLocalResourcePreview(ctx, tenant, input); local != nil {
		return *local
	}
	m.mu.Lock()
	validators := append(
		[]ProtocolBindingSpecValidator(nil),
		m.protocolBindingSpecValidators[input.Protocol]...,
	)
	m.mu.Unlock()
	if len(validators) == 0 {
		return ProtocolBindingValidation{
			Verdict: ProtocolObservationUnknown, Code: "capability_validator_unwired",
		}
	}
	for _, validator := range validators {
		validation, err := validator.ValidateProtocolBindingSpec(ctx, tenant, input)
		if errors.Is(err, ErrProtocolBindingSpecUnsupported) {
			continue
		}
		if err != nil {
			return ProtocolBindingValidation{
				Verdict: ProtocolObservationUnknown, Code: "capability_observation_unavailable",
			}
		}
		normalized, err := normalizeServerProtocolBindingValidation(validation)
		if err != nil {
			return ProtocolBindingValidation{
				Verdict: ProtocolObservationUnknown, Code: "capability_evidence_invalid",
			}
		}
		return normalized
	}
	return ProtocolBindingValidation{
		Verdict: ProtocolObservationUnknown, Code: "capability_route_unconfigured",
	}
}

func normalizeServerProtocolBindingValidation(
	value ProtocolBindingValidation,
) (ProtocolBindingValidation, error) {
	value.Code = strings.TrimSpace(value.Code)
	if !value.Verdict.valid() || !boundedToken(value.Code, 128) ||
		(value.Verdict == ProtocolObservationClean && value.ObservedAt.IsZero()) {
		return ProtocolBindingValidation{}, protocolBindingInvalid("invalid_capability_evidence")
	}
	if !value.ObservedAt.IsZero() {
		value.ObservedAt = value.ObservedAt.UTC()
	}
	return value, nil
}
