// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureopenai

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// providerRef is the CostSample/Catalog ProviderRef every observation this connector
// emits carries — modelprovider.ProviderAzureOpenAI. The DEPLOYMENT surface is
// disambiguated by Gateway=foundry on every cost sample.
const providerRef = "azure-openai" // == modelprovider.ProviderAzureOpenAI

// SubjectKind values used in health findings, one per source, so a failed source is a
// visible signal rather than silence (a gap is a signal, not silence).
const (
	subjectSubscriptions = "azure-openai.subscriptions"
	subjectUsage         = "azure-openai.usage"
	subjectCost          = "azure-openai.cost"
)

// healthFinding reports an enabled source that could not be read. The error detail is
// hashed, never embedded, so a message that carries a token is not persisted (minimal-
// data). The connector emits this and continues with the next source.
func healthFinding(subjectKind, subjectRef, title string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKind,
		SubjectRef:  redact.Clean(subjectRef),
		Title:       title,
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}
