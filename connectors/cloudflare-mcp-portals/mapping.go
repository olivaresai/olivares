// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const signalCFMCPPortals model.SignalSource = "cloudflare_mcp_portals"

const (
	originAccount = "cf.account"

	resMCPServer = "cf.mcp_server"
	resMCPPortal = "cf.mcp_portal"
)

func inventoryEdge(originKind, originRef, resKind, resRef, toolRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: resKind,
		ResourceRef:  resRef,
		Mode:         model.ModeUnknown,
		Source:       signalCFMCPPortals,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      toolRef,
		ObservedAt:   at,
	}
}

func healthFinding(subjectKind, subjectRef, title string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}
