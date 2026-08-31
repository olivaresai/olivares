// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	// signalEntraAgentSignIn marks an observed Microsoft Graph sign-in event
	// for an Entra Agent ID principal. It is distinct from Entra inventory and
	// posture signals: the row says an agent principal successfully touched a
	// target resource, not that a grant exists.
	signalEntraAgentSignIn = model.SignalSource("entra_agent_signin")

	// resEntraApp is the honest target kind for signIn.resourceId: Graph names
	// the signed-into target "resource", but in Entra sign-in rows that resource
	// is the target application/service principal, so the connector uses the
	// stable application namespace rather than guessing a data-plane object.
	resEntraApp = "entra.app"
)

// gatherSignIns reads the beta agent attribution surface on auditLogs/signIns
// and emits successful sign-ins as observed access edges. The default filter is
// deliberately the documented servicePrincipal-event slice; broader delegated
// or managed-identity capture is an operator override via signin_filter.
func (s *Source) gatherSignIns(ctx context.Context, sink sdk.Sink, client *httpx.Client, now time.Time) error {
	windowStart := now.UTC().Add(-s.signInLookback).Format(time.RFC3339)
	query := url.Values{
		"$filter": {"(" + s.signInFilter + ") and createdDateTime ge " + windowStart},
	}
	rows, err := collectPages[signIn](ctx, client, "/beta/auditLogs/signIns", query, s.maxPages)
	if err != nil {
		return err
	}

	for _, row := range rows {
		if row.Status.ErrorCode != 0 || row.ServicePrincipalID == "" || row.ResourceID == "" {
			continue
		}
		observedAt, err := time.Parse(time.RFC3339, row.CreatedDateTime)
		if err != nil {
			continue
		}
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   "identity",
			OriginRef:    redact.Clean(row.ServicePrincipalID),
			ResourceKind: resEntraApp,
			ResourceRef:  redact.Clean(row.ResourceID),
			// A sign-in proves reachability, but it does not say whether the
			// target application was read from, written to or only token-bound.
			Mode:       model.ModeUnknown,
			Source:     signalEntraAgentSignIn,
			Confidence: signInConfidence(row.Agent.AgentType),
			ObservedAt: observedAt.UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func signInConfidence(agentType string) model.Confidence {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "agenticappinstance", "agentiduser", "agentidentity":
		return model.ConfidenceAttributed
	default:
		return model.ConfidenceApproximate
	}
}
