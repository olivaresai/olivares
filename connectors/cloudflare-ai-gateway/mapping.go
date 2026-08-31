// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfaigateway

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const signalCFAIGateway model.SignalSource = "cloudflare_ai_gateway"

// GatewayCFAIGateway is the deployment-surface tag stamped on every CostSample:
// the call was served THROUGH the Cloudflare AI Gateway.
const GatewayCFAIGateway model.Gateway = "cloudflare-ai-gateway"

const (
	originAccount = "cf.account"
	resGateway    = "cf.ai_gateway"
)

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
