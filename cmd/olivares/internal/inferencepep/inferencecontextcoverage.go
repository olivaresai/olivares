// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferencepep

import (
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/models"
)

// Classify only the governed request, never caller-supplied audit metadata.
// A requested edit is a possible context change, not proof it was applied.
func inferenceContextCoverage(req claudeapi.MessageRequest) inferenceproxy.ContextCoverage {
	tokens := req.BetaHeaders()
	if req.ContextManagement != nil {
		for _, edit := range req.ContextManagement.Edits {
			switch edit.Type {
			case models.EditCompaction, models.BetaClearToolUses, models.BetaClearThinking:
				tokens = append(tokens, edit.Type)
			default:
				return inferenceproxy.ContextCoverageUnknown
			}
		}
	}
	coverage := inferenceproxy.ContextCoverageMetadataOnly
	if models.ContextMayBeServerCleared(tokens) {
		coverage = inferenceproxy.ContextCoverageMayChange
	}
	return coverage
}

func batchInferenceContextCoverage(requests []claudeapi.BatchRequest) inferenceproxy.ContextCoverage {
	coverage := inferenceproxy.ContextCoverageMetadataOnly
	for _, request := range requests {
		switch inferenceContextCoverage(request.Params) {
		case inferenceproxy.ContextCoverageUnknown:
			return inferenceproxy.ContextCoverageUnknown
		case inferenceproxy.ContextCoverageMayChange:
			coverage = inferenceproxy.ContextCoverageMayChange
		}
	}
	return coverage
}
