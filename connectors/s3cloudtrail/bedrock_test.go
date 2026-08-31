// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3cloudtrail

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestBuildBedrockEdge(t *testing.T) {
	s := &Source{}
	rec := record{
		EventTime:   "2026-06-05T10:00:00Z",
		EventSource: "bedrock.amazonaws.com",
		EventName:   "InvokeModel",
		UserIdentity: userIdentity{
			Type: "IAMUser", ARN: "arn:aws:iam::123456789012:user/dev", PrincipalID: "AIDA",
		},
		RequestParameters: requestParameters{ModelID: "us.anthropic.claude-opus-4-8"},
	}
	e, ok := s.buildEdge(rec)
	if !ok {
		t.Fatal("Bedrock InvokeModel must build a model-access edge")
	}
	if e.ResourceKind != bedrockModelResource {
		t.Errorf("kind = %q, want claude.model", e.ResourceKind)
	}
	// Surface encoded in the resource identity (legacy CRIS id).
	if e.ResourceRef != "bedrock-legacy/us.anthropic.claude-opus-4-8" {
		t.Errorf("ref = %q", e.ResourceRef)
	}
	if e.Source != model.SignalCloudTrail || e.Mode != model.ModeUnknown || e.ToolRef != "InvokeModel" {
		t.Errorf("edge = %+v", e)
	}
	if e.OriginKind != originKind || e.OriginRef != "arn:aws:iam::123456789012:user/dev" {
		t.Errorf("origin = %s/%s", e.OriginKind, e.OriginRef)
	}
}

func TestBedrockGatewayFromModelID(t *testing.T) {
	cases := map[string]model.Gateway{
		"anthropic.claude-opus-4-8":         model.GatewayBedrockMantle,
		"us.anthropic.claude-opus-4-8":      model.GatewayBedrockLegacy,
		"eu.anthropic.claude-sonnet-4-6":    model.GatewayBedrockLegacy,
		"global.anthropic.claude-haiku-4-5": model.GatewayBedrockLegacy,
		"some-unknown-bedrock-id":           model.GatewayBedrockLegacy, // conservative default
	}
	for id, want := range cases {
		if got := bedrockGateway(id); got != want {
			t.Errorf("bedrockGateway(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestBedrockInvocationFilters(t *testing.T) {
	// A non-invocation Bedrock management event is not a model-access edge.
	if isBedrockModelInvocation(record{EventSource: "bedrock.amazonaws.com", EventName: "ListFoundationModels", RequestParameters: requestParameters{ModelID: "anthropic.claude-opus-4-8"}}) {
		t.Error("management event must not be a model-access edge")
	}
	// A non-Claude model invocation is out of scope for this Anthropic-first path.
	if isBedrockModelInvocation(record{EventSource: "bedrock.amazonaws.com", EventName: "InvokeModel", RequestParameters: requestParameters{ModelID: "amazon.titan-text-v1"}}) {
		t.Error("non-Claude model must not be governed here")
	}
	// A real Claude InvokeModel IS in scope.
	if !isBedrockModelInvocation(record{EventSource: "bedrock.amazonaws.com", EventName: "Converse", RequestParameters: requestParameters{ModelID: "anthropic.claude-opus-4-8"}}) {
		t.Error("Claude Converse must be in scope")
	}
	// An S3 event still goes through the S3 path, not the Bedrock branch.
	if isBedrockModelInvocation(record{EventSource: "s3.amazonaws.com", EventName: "GetObject"}) {
		t.Error("S3 event must not be a Bedrock invocation")
	}
	// A non-Anthropic model whose name merely contains "claude" must NOT be tagged.
	if isClaudeModelID("acme.claude-clone-v1") {
		t.Error("non-anthropic model with 'claude' substring must not be a Claude edge")
	}
}

func TestBedrockARNModelID(t *testing.T) {
	// ARN-form inference-profile id must classify off the trailing segment, not be
	// dumped to the bedrock-legacy fallback by the leading "arn:".
	mantleARN := "arn:aws:bedrock:us-east-1:123456789012:foundation-model/anthropic.claude-opus-4-8"
	if got := bedrockGateway(mantleARN); got != model.GatewayBedrockMantle {
		t.Errorf("mantle ARN gateway = %q, want bedrock-mantle", got)
	}
	crisARN := "arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-opus-4-8"
	if got := bedrockGateway(crisARN); got != model.GatewayBedrockLegacy {
		t.Errorf("CRIS ARN gateway = %q, want bedrock-legacy", got)
	}
	if !isClaudeModelID(mantleARN) {
		t.Error("ARN-form Claude id must be recognized")
	}
	// The edge ResourceRef uses the resolved surface, and keeps the raw modelId.
	s := &Source{}
	e, ok := s.buildEdge(record{EventTime: "2026-06-05T10:00:00Z", EventSource: "bedrock.amazonaws.com", EventName: "InvokeModel",
		UserIdentity: userIdentity{Type: "IAMUser", ARN: "arn:aws:iam::1:user/d"}, RequestParameters: requestParameters{ModelID: mantleARN}})
	if !ok || e.ResourceRef != "bedrock-mantle/"+mantleARN {
		t.Errorf("ARN edge ref = %q ok=%v", e.ResourceRef, ok)
	}
}
