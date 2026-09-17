// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Exact independent-review probes (VD-IR1/VD-IR2). They failed on 3c23a0c16a;
// they must stay green on the correction.

func TestIndependentDocumentProofRoleBijection(t *testing.T) {
	p := validCodexPlanV2()
	p.Selection.RequiredSubjects[1].ProofRole = p.Selection.RequiredSubjects[0].ProofRole
	raw, err := MarshalPlanV2(p)
	if err != nil {
		return
	}
	_, directErr := ReadPlanV2(bytes.NewReader(raw))
	_, unionErr := ReadApprovedPlan(bytes.NewReader(raw))
	if directErr == nil && unionErr == nil {
		t.Errorf("contract violation accepted: two distinct required subject paths reference proof role %q, leaving one of three source proof roles unused; marshal, direct read and union read all accepted", p.Selection.RequiredSubjects[0].ProofRole)
	}
}

func TestIndependentDocumentMissingProofRoleControl(t *testing.T) {
	p := validCodexPlanV2()
	p.Selection.RequiredSubjects[1].ProofRole = "missing-proof-role"
	if _, err := MarshalPlanV2(p); KindOf(err) != KindInvalidRequest {
		t.Fatalf("missing role must remain invalid_request: %v", err)
	}
}

func TestIndependentDocumentNullScalars(t *testing.T) {
	raw := mustMarshalV2(t, validGrokPlanV2())
	for _, field := range []string{"sha256", "size"} {
		t.Run(field, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			doc["selection"].(map[string]any)["fetched_object"].(map[string]any)[field] = nil
			edited, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			p, directErr := ReadPlanV2(bytes.NewReader(edited))
			_, unionErr := ReadApprovedPlan(bytes.NewReader(edited))
			if directErr == nil && unionErr == nil {
				t.Errorf("null %s accepted with unchanged digest=%s; typed unknown/bounded field was silently converted to zero value", field, p.Digest)
			}
		})
	}
}

func TestIndependentDocumentOriginOnlyNullVerifierControl(t *testing.T) {
	raw := mustMarshalV2(t, validGrokPlanV2())
	p, err := ReadPlanV2(bytes.NewReader(raw))
	if err != nil || p.Selection.Verification.Cosign != nil || p.Selection.FetchedObject.SHA256 != "" || p.Selection.FetchedObject.Size != 0 {
		t.Fatalf("explicit origin-only null cosign and empty/zero unknown/bounded values must remain accepted: %v", err)
	}
}
