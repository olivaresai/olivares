// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func TestStampDecisionReconstructionFieldsNamesArtifactAndDigest(t *testing.T) {
	t.Parallel()
	in := sdk.AuthorizationDecisionContent{
		Inputs: []sdk.AccessDependency{{
			Kind: sdk.DependencyPolicyArtifact, Ref: "art-1", Required: true,
		}},
	}
	got, err := sdk.StampDecisionReconstructionFields(in)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if got.PolicyVersionID != "art-1" {
		t.Fatalf("policy_version_id = %q, want the required artifact ref", got.PolicyVersionID)
	}
	if !got.InputsDigestKnown() {
		t.Fatal("inputs_digest must be stamped")
	}
	again, err := sdk.AccessInputsDigest(in.Inputs)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if got.InputsDigest != again {
		t.Fatalf("stamped digest %q != recomputed %q", got.InputsDigest, again)
	}
}

func TestStampDecisionReconstructionFieldsLeavesUnknownWithoutArtifact(t *testing.T) {
	t.Parallel()
	got, err := sdk.StampDecisionReconstructionFields(sdk.AuthorizationDecisionContent{})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if got.PolicyVersionKnown() {
		t.Fatalf("policy_version_id = %q, want unknown", got.PolicyVersionID)
	}
	if !got.InputsDigestKnown() {
		t.Fatal("empty inputs still have a defined digest; unknown is reserved for absent rows")
	}
}

func TestStampDecisionReconstructionFieldsRefusesWrongDigest(t *testing.T) {
	t.Parallel()
	_, err := sdk.StampDecisionReconstructionFields(sdk.AuthorizationDecisionContent{
		InputsDigest: "not-the-digest",
		Inputs:       []sdk.AccessDependency{{Kind: sdk.DependencyFact, Ref: "f1", Required: true}},
	})
	if err == nil {
		t.Fatal("wrong inputs_digest must be refused")
	}
}

func TestLegacyDecisionJSONUnmarshalsAsUnknownPolicyVersion(t *testing.T) {
	t.Parallel()
	// A row written before the stamp: no policy_version_id, no inputs_digest.
	raw := []byte(`{"schema_version":1,"question":{"schema_version":1,"actor_ref":"a","action":"read"},"purpose":"live_authorization","evaluator":"cedar","evaluator_version":"3","outcome":"allow","reason_code":"policy.allow","replay_completeness":"unknown","authorization_point":"test"}`)
	var d sdk.AuthorizationDecisionContent
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if d.PolicyVersionKnown() || d.InputsDigestKnown() {
		t.Fatalf("legacy row must stay unknown, got policy=%q inputs=%q", d.PolicyVersionID, d.InputsDigest)
	}
}

func TestAccessInputsDigestIsStableAcrossTwoCalls(t *testing.T) {
	t.Parallel()
	inputs := []sdk.AccessDependency{
		{Kind: sdk.DependencyPolicyArtifact, Ref: "a", Digest: "d1", Required: true},
		{Kind: sdk.DependencyFact, Ref: "f", Version: "2", Required: false},
	}
	first, err := sdk.AccessInputsDigest(inputs)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := sdk.AccessInputsDigest(inputs)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second || first == "" {
		t.Fatalf("digest not stable: %q vs %q", first, second)
	}
}
