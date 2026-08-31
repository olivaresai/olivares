// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrockid

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestBaseModelID(t *testing.T) {
	cases := map[string]string{
		"anthropic.claude-opus-4-8":    "anthropic.claude-opus-4-8",
		"us.anthropic.claude-opus-4-8": "us.anthropic.claude-opus-4-8",
		"amazon.titan-text-v1":         "amazon.titan-text-v1",
		// foundation-model ARN (no account id) → trailing segment is the bare id.
		"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet-4-5-20250929-v1:0": "anthropic.claude-sonnet-4-5-20250929-v1:0",
		// system inference-profile ARN → trailing is {geo}.{modelId}.
		"arn:aws:bedrock:us-east-1:111122223333:inference-profile/us.anthropic.claude-opus-4-8": "us.anthropic.claude-opus-4-8",
		// application-inference-profile ARN → trailing is an OPAQUE name.
		"arn:aws:bedrock:us-west-2:111122223333:application-inference-profile/USClaudeSonnetApp": "USClaudeSonnetApp",
	}
	for in, want := range cases {
		if got := BaseModelID(in); got != want {
			t.Errorf("BaseModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGateway(t *testing.T) {
	cases := map[string]model.Gateway{
		// Claude (unchanged from the s3-cloudtrail semantics).
		"anthropic.claude-opus-4-8":         model.GatewayBedrockMantle,
		"us.anthropic.claude-opus-4-8":      model.GatewayBedrockLegacy,
		"eu.anthropic.claude-sonnet-4-6":    model.GatewayBedrockLegacy,
		"global.anthropic.claude-haiku-4-5": model.GatewayBedrockLegacy,
		"some-unknown-bedrock-id":           model.GatewayBedrockLegacy, // no vendor.model shape → conservative
		// Generalized beyond Claude: a bare vendor.model id is the Mantle surface…
		"amazon.titan-text-v1":     model.GatewayBedrockMantle,
		"meta.llama3-70b-instruct": model.GatewayBedrockMantle,
		"mistral.mistral-large":    model.GatewayBedrockMantle,
		"cohere.command-r-v1:0":    model.GatewayBedrockMantle,
		// …and a geo-prefixed non-Claude id is the legacy CRIS surface.
		"us.amazon.nova-pro-v1:0":     model.GatewayBedrockLegacy,
		"apac.meta.llama3-70b":        model.GatewayBedrockLegacy,
		"us-gov.anthropic.claude-3-5": model.GatewayBedrockLegacy,
		// ARN forms classify off the trailing segment.
		"arn:aws:bedrock:us-east-1::foundation-model/amazon.titan-text-v1":                       model.GatewayBedrockMantle,
		"arn:aws:bedrock:us-east-1:111122223333:inference-profile/us.meta.llama3-70b":            model.GatewayBedrockLegacy,
		"arn:aws:bedrock:us-west-2:111122223333:application-inference-profile/USClaudeSonnetApp": model.GatewayBedrockLegacy, // opaque → conservative
	}
	for id, want := range cases {
		if got := Gateway(id); got != want {
			t.Errorf("Gateway(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestIsClaude(t *testing.T) {
	claude := []string{
		"anthropic.claude-opus-4-8",
		"us.anthropic.claude-sonnet-4-6",
		"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-opus-4-8",
	}
	for _, id := range claude {
		if !IsClaude(id) {
			t.Errorf("IsClaude(%q) = false, want true", id)
		}
	}
	notClaude := []string{
		"amazon.titan-text-v1",
		"meta.llama3-70b",
		"acme.claude-clone-v1", // contains "claude" but not the anthropic namespace
		"",
	}
	for _, id := range notClaude {
		if IsClaude(id) {
			t.Errorf("IsClaude(%q) = true, want false", id)
		}
	}
}
