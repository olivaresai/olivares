// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

var _ sessions.ProtocolBindingSpecValidator = (*orchRemoteExecutor)(nil)

// ValidateProtocolBindingSpec proves an A2A draft against the operator-pinned
// target and the peer's signed Agent Card. It performs no remote task creation.
func (e *orchRemoteExecutor) ValidateProtocolBindingSpec(
	ctx context.Context,
	tenant model.TenantID,
	input sessions.ProtocolBindingSpecInput,
) (sessions.ProtocolBindingValidation, error) {
	if e == nil || tenant.IsZero() || tenant.IsSystem() ||
		input.Protocol != sessions.BindingProtocolA2A ||
		strings.TrimSpace(input.ProtocolVersion) != a2a.ProtocolVersion ||
		input.Direction != sessions.BindingOutbound ||
		strings.TrimSpace(input.RemoteResourceKind) != "agent" {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"%w: orch remote protocol binding spec is not a supported A2A target",
			sessions.ErrProtocolBindingSpecUnsupported,
		)
	}
	target, ok := e.targets[remoteTargetKey(input.PeerAuthority, input.RemoteResourceRef)]
	if !ok || strings.TrimSpace(target.skill) == "" || len(target.scopes) == 0 ||
		!protocolRuntimePolicyMatches(input.RuleRefs, input.PermissionProfileRef, target.policy) {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"%w: orch remote protocol binding spec target is not operator-provisioned",
			sessions.ErrProtocolBindingSpecUnsupported,
		)
	}
	anchor := input
	anchor.Validation = sessions.ProtocolBindingValidation{}
	canonical, err := json.Marshal(anchor)
	if err != nil {
		return sessions.ProtocolBindingValidation{}, err
	}
	paramsHash := remoteDigestHex(
		"a2a.protocol-binding-spec.v1", target.fingerprint, string(canonical),
	)
	check, err := e.client(target, nil).Test(ctx, a2a.DelegateSpec{
		AgentName: target.name, AgentURL: target.url, Skill: target.skill,
		Scope: target.scopes[0], Tenant: tenant.String(),
		RequestedBy: "system:protocol-binding-validator",
		Objective:   "protocol-binding-spec", ParamsHash: paramsHash,
	})
	if err != nil {
		return sessions.ProtocolBindingValidation{}, err
	}
	if check.AgentName != target.name || check.Skill != target.skill ||
		check.Scope != target.scopes[0] || check.Trust != "verified" || check.PlanHash == "" {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"orch remote: A2A capability witness changed target identity",
		)
	}
	return sessions.ProtocolBindingValidation{
		Verdict: sessions.ProtocolObservationClean, Code: "a2a_capability_validated",
		ObservedAt: e.now().UTC(),
	}, nil
}
