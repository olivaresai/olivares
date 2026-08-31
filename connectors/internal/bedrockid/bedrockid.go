// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package bedrockid is the shared Amazon Bedrock model-identifier classifier used by
// the connectors that observe Bedrock traffic: the s3-cloudtrail Claude-access path
// (CLA-11) and the generic Bedrock usage/cost connector. It turns a Bedrock
// modelId — a bare id, a cross-region inference-profile (CRIS) id, or a full ARN —
// into the dimensions the cost/access contract needs: the bare model id (BaseModelID),
// the deployment surface (Gateway), and whether it is an Anthropic/Claude model.
//
// It is provider-agnostic by design (extends Bedrock observability beyond Claude
// to Titan/Nova/Llama/Mistral/Cohere/etc.): Gateway classifies every vendor's id
// uniformly, and the bare model id (which carries the vendor token) is the natural
// ModelRef. It never fabricates a dimension it cannot read (ARCHITECTURE.md). It is stdlib +
// sdk/model only and imports no engine package, so it stays on the Apache-2.0 side of
// the boundary.
package bedrockid

import (
	"strings"

	"github.com/olivaresai/olivares/sdk/model"
)

// geoPrefixes are the Bedrock cross-region inference (CRIS) geographic prefixes a
// modelId can carry, e.g. "us.anthropic.claude-…" / "eu.amazon.nova-…". A CRIS id
// is the legacy InvokeModel/Converse surface (see Gateway). The set is matched as a
// whole leading dot-segment so a vendor token that merely starts with these letters
// is never mistaken for a geo prefix. Verified vs the AWS cross-region-inference
// docs (jun-2026): us, eu, apac, global, us-gov.
var geoPrefixes = map[string]struct{}{
	"us":     {},
	"eu":     {},
	"apac":   {},
	"global": {},
	"us-gov": {},
}

// BaseModelID reduces a CloudTrail/invocation-log modelId to its bare id segment.
// The id may be a bare id ("anthropic.claude-opus-4-8" / "us.anthropic.claude-…"),
// or a full ARN
// ("arn:aws:bedrock:<region>:<acct>:foundation-model/anthropic.claude-…",
// ".../inference-profile/us.anthropic.claude-…",
// ".../application-inference-profile/<opaque-id>"). For an ARN we classify off the
// trailing segment after the last "/", so the leading "arn:" never misclassifies it.
func BaseModelID(modelID string) string {
	if strings.HasPrefix(modelID, "arn:") {
		if i := strings.LastIndex(modelID, "/"); i >= 0 {
			return modelID[i+1:]
		}
	}
	return modelID
}

// Gateway resolves the Bedrock deployment surface from the model id (verified id
// formats, jun-2026): a geographic CRIS prefix (us./eu./apac./global./us-gov.) is
// the legacy InvokeModel/Converse surface; a bare "<vendor>.<model>" id is the
// current Mantle surface. An id with no recognizable vendor.model shape (e.g. an
// opaque application-inference-profile id) falls back to bedrock-legacy — the
// observe-only default that never claims the build-target Mantle without evidence
// (ANT2-01). ARN-form ids are reduced to their trailing segment first.
//
// This generalizes the original Claude-only resolver (anthropic.* only) to every
// vendor while keeping the Claude classification byte-identical, so the existing
// s3-cloudtrail access edges are unchanged.
func Gateway(modelID string) model.Gateway {
	id := BaseModelID(modelID)
	if hasGeoPrefix(id) {
		return model.GatewayBedrockLegacy
	}
	if strings.Contains(id, ".") {
		return model.GatewayBedrockMantle
	}
	return model.GatewayBedrockLegacy
}

// IsClaude reports whether a Bedrock model id refers to an Anthropic/Claude model.
// It anchors on the Anthropic vendor namespace ("anthropic.claude") rather than a
// bare "claude" substring, so a non-Anthropic model whose name merely contains
// "claude" is not mistagged as a Claude edge.
func IsClaude(modelID string) bool {
	return strings.Contains(BaseModelID(modelID), "anthropic.claude")
}

// hasGeoPrefix reports whether id's leading dot-segment is a CRIS geo prefix.
func hasGeoPrefix(id string) bool {
	i := strings.IndexByte(id, '.')
	if i < 0 {
		return false
	}
	_, ok := geoPrefixes[id[:i]]
	return ok
}
