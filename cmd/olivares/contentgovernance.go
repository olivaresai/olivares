// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"

	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/compliance"
)

// contentGovernanceClassifier is the composition-root bridge between the
// claude-compliance connector's minimal-data content enumeration (Apache) and
// the compliance module's retention/hold framework (AGPL). It classifies
// provider-side content REFERENCES by kind and governance metadata — never
// bodies, PII, or secrets — so the retention module can apply policies and the
// hold-gate can cover provider-side content in legal holds.
//
// This is the ONLY place that speaks both the connector's types (ContentRef)
// and the compliance module's types (HoldSubject) — neither side imports the
// other. The connector provides the enumeration; the compliance module provides
// the governance; this bridge connects them at the composition root.
type contentGovernanceClassifier struct {
	comp *compliance.Module
	log  *slog.Logger
}

// classifyContentForHold checks whether a provider-side content reference
// (chat/project) is covered by an active legal hold that should block its
// erasure. It maps the content kind to a hold subject and queries the compliance
// module's hold-gate — the same gate the RTBF workflow uses for engine-side
// content.
//
// This is consumed by the provider eraser adapter (erasurewiring.go) as a
// pre-flight check: before routing a content reference through the connector's
// dual-control erase PEP, verify no hold covers it. The hold-gate is
// fail-closed: an error is treated as "held" (over-preserve, never destroy
// under uncertainty).
func (c *contentGovernanceClassifier) classifyContentForHold(
	ctx context.Context, tenant model.TenantID, ref claudecompliance.ContentRef,
) (held bool, err error) {
	if c == nil || c.comp == nil {
		return false, nil
	}
	sub := compliance.HoldSubject{
		Kind: contentRefToSubjectKind(ref.Kind),
		Ref:  ref.ID,
	}
	dec, err := c.comp.CheckHold(ctx, tenant, sub)
	if err != nil {
		c.log.Warn("content governance: hold-gate check failed (fail-closed, treating as held)",
			"content_kind", ref.Kind, "content_id", ref.ID, "err", err)
		return true, nil
	}
	return dec.Held, nil
}

// contentRefToSubjectKind maps a connector content kind (chat/project) to the
// compliance module's hold subject vocabulary. Unknown kinds map to the generic
// "document" kind (conservative: a subject hold on "document" covers the
// reference rather than ignoring it).
func contentRefToSubjectKind(kind string) string {
	switch kind {
	case "chat":
		return "chat"
	case "project":
		return "project"
	default:
		return "document"
	}
}
