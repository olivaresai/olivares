// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3cloudtrail

import (
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/bedrockid"
	"github.com/olivaresai/olivares/sdk/model"
)

// This file maps Claude-on-Bedrock invocations (CLA-11) to model-access edges. The
// Anthropic Admin/Usage/Compliance APIs do NOT apply to Bedrock-served Claude, so an
// S3-delivered CloudTrail DATA-events trail is the observability path: Bedrock
// InvokeModel/Converse are data events (the live aws management-events connector
// cannot see them), and they arrive in the same CloudTrail files this connector
// already reads. We turn each Claude invocation into an edge "identity -> claude.model"
// so the access map shows which principals reach which Claude models on which surface.
//
// EdgeObservation carries no Gateway field (that dimension lives on CostSample), so
// the surface is encoded in the resource identity: ResourceRef = "<gateway>/<modelId>".
// The same model on Mantle vs legacy CRIS is therefore a DISTINCT access resource —
// the correct graph semantics, since they are different id spaces / billing / audit
// surfaces. (Bedrock COST is not on this CloudTrail path; it comes from AWS billing —
// not fabricated here.)

// bedrockModelResource is the ResourceKind for a Claude model reached via Bedrock.
const bedrockModelResource = "claude.model"

// bedrockEventSources are the CloudTrail eventSources for Bedrock model invocation.
// "bedrock.amazonaws.com" is the legacy InvokeModel/Converse (bedrock-runtime) source;
// the bare-prefix check also tolerates a future Mantle runtime source without
// guessing its exact value (the surface is then resolved from the model id).
var bedrockEventSources = map[string]struct{}{
	"bedrock.amazonaws.com":         {},
	"bedrock-runtime.amazonaws.com": {},
}

// bedrockInvokeEvents are the eventNames that represent a model invocation (a
// model-ACCESS, distinct from Bedrock control-plane management events).
var bedrockInvokeEvents = map[string]struct{}{
	"InvokeModel":                   {},
	"InvokeModelWithResponseStream": {},
	"Converse":                      {},
	"ConverseStream":                {},
}

// isBedrockModelInvocation reports whether rec is a Bedrock model-invocation event
// targeting a Claude/Anthropic model (we scope to Claude — this is the Anthropic-first
// observability path, not a generic Bedrock connector).
func isBedrockModelInvocation(rec record) bool {
	if _, ok := bedrockEventSources[rec.EventSource]; !ok {
		if !strings.HasPrefix(rec.EventSource, "bedrock") {
			return false
		}
	}
	if _, ok := bedrockInvokeEvents[rec.EventName]; !ok {
		return false
	}
	return isClaudeModelID(rec.RequestParameters.ModelID)
}

// isClaudeModelID reports whether a Bedrock model id refers to an Anthropic/Claude
// model (the only models this Anthropic-first CloudTrail path governs). It delegates
// to the shared bedrockid classifier so the Claude-access semantics stay
// byte-identical while the generic Bedrock usage/cost connector reuses the same id
// parsing — no duplicated identity logic. It anchors on the Anthropic vendor namespace
// ("anthropic.claude"), so a non-Anthropic model whose name merely contains "claude"
// is not mistagged as a Claude edge.
func isClaudeModelID(modelID string) bool {
	return bedrockid.IsClaude(modelID)
}

// bedrockGateway resolves the Bedrock surface (bedrock-mantle vs bedrock-legacy) from
// the model id. It delegates to the shared bedrockid classifier; the Claude
// classification is unchanged — a geographic CRIS prefix (us./eu./apac./global.) is the
// legacy InvokeModel/Converse surface, a bare "anthropic.*" id is the current Mantle
// surface, an unrecognized id falls back to bedrock-legacy — so the existing access
// edges and their tests hold. ARN-form ids are reduced to their trailing segment first.
func bedrockGateway(modelID string) model.Gateway {
	return bedrockid.Gateway(modelID)
}

// buildBedrockEdge maps a Claude-on-Bedrock invocation to a model-access edge. The
// identity is the IAM principal (reused resolveIdentity); the resource is the
// surface-qualified Claude model. Mode is unknown — a model invocation is neither a
// data read nor a data write (it is "use of" the model), and inventing R/W would be a
// fabrication (ARCHITECTURE.md). ok=false if the timestamp or identity cannot be resolved.
func (s *Source) buildBedrockEdge(rec record) (model.EdgeObservation, bool) {
	ts, ok := parseTime(rec.EventTime)
	if !ok {
		return model.EdgeObservation{}, false
	}
	origin, conf, ok := s.resolveIdentity(rec.UserIdentity)
	if !ok {
		return model.EdgeObservation{}, false
	}
	modelID := rec.RequestParameters.ModelID
	gw := bedrockGateway(modelID)
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    origin,
		ResourceKind: bedrockModelResource,
		ResourceRef:  string(gw) + "/" + modelID,
		Mode:         model.ModeUnknown,
		Source:       model.SignalCloudTrail,
		Confidence:   conf,
		ToolRef:      rec.EventName,
		ObservedAt:   ts,
	}, true
}
