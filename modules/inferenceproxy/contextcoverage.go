// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

// ContextCoverage describes a limit of retained inference metadata. No value
// implies that a captured trail reconstructs the provider's working context.
type ContextCoverage string

const (
	ContextCoverageMetadataOnly ContextCoverage = "inference_metadata_only"
	ContextCoverageMayChange    ContextCoverage = "provider_context_may_change"
	ContextCoverageUnknown      ContextCoverage = "provider_context_unknown"
)
