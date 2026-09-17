// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalPlanV2OmitsObservationMaterial(t *testing.T) {
	raw := mustMarshalV2(t, validCodexPlanV2())
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if k := observationKeyIn(keys); k != "" {
		t.Fatalf("MarshalPlanV2 wrote observation key %q", k)
	}
	var sel map[string]json.RawMessage
	if err := json.Unmarshal(keys["selection"], &sel); err != nil {
		t.Fatal(err)
	}
	if k := observationKeyIn(sel); k != "" {
		t.Fatalf("selection carried observation key %q", k)
	}
	if _, ok := sel["proof"]; ok {
		t.Fatal("selection carried a proof report field")
	}
	if grok := mustMarshalV2(t, validGrokPlanV2()); bytes.Contains(grok, []byte(`"action"`)) || bytes.Contains(grok, []byte(`"observed"`)) || bytes.Contains(grok, []byte(`"existing"`)) {
		t.Fatal("Grok approval contained observation names")
	}
}

func TestPlanV2DigestBindsVerifierSourceLayoutSubjects(t *testing.T) {
	base := validCodexPlanV2()
	baseDigest := ComputeDigestV2(base)
	if baseDigest != ComputeDigestV2(validCodexPlanV2()) {
		t.Fatal("identical selections produced different digests")
	}

	mutators := []struct {
		name string
		edit func(*PlanV2)
	}{
		{"cosign version", func(p *PlanV2) { p.Selection.Verification.Cosign.Version = "v3.1.4" }},
		{"cosign executable hash", func(p *PlanV2) { p.Selection.Verification.Cosign.ExecutableSHA256 = fixtureHex(0x99) }},
		{"trusted root hash", func(p *PlanV2) { p.Selection.Verification.Cosign.TrustedRootSHA256 = fixtureHex(0x98) }},
		{"trusted root iteration", func(p *PlanV2) { p.Selection.Verification.Cosign.TrustedRootIteration = 11 }},
		{"cosign mode", func(p *PlanV2) {
			p.Selection.Verification.Cosign.Mode = CosignModeOnline
			p.Selection.Verification.Cosign.RefreshPolicy = RefreshPolicyTUF
			p.Selection.Verification.Cosign.EgressPolicy = EgressPolicyTUF
		}},
		{"package URL", func(p *PlanV2) {
			p.Selection.Source.Package.URL = "https://example.invalid/codex-package-other.tar.gz"
		}},
		{"allowed origin", func(p *PlanV2) {
			p.Selection.Source.AllowedOrigins = []string{"https://mirror.example.invalid"}
			p.Selection.Source.Pointer.URL = "https://mirror.example.invalid/channels/0.153.4"
			p.Selection.Source.Checksums.URL = "https://mirror.example.invalid/codex-package_SHA256SUMS"
			p.Selection.Source.Package.URL = "https://mirror.example.invalid/codex-package-x86_64-unknown-linux-musl.tar.gz"
			p.Selection.Source.Proofs = []ProofLocator{
				{Role: "bwrap", URL: "https://mirror.example.invalid/bwrap.sigstore"},
				{Role: "codex", URL: "https://mirror.example.invalid/codex.sigstore"},
				{Role: "codex-code-mode-host", URL: "https://mirror.example.invalid/codex-code-mode-host.sigstore"},
			}
		}},
		{"subject claim hash", func(p *PlanV2) { p.Selection.RequiredSubjects[0].ExpectedSHA256 = fixtureHex(0x44) }},
		{"subject identity", func(p *PlanV2) {
			p.Selection.RequiredSubjects[0].Identity = "https://example.invalid/identity/other"
		}},
		{"fetched object hash", func(p *PlanV2) { p.Selection.FetchedObject.SHA256 = fixtureHex(0x55) }},
		{"destination", func(p *PlanV2) {
			p.Selection.Destination.Root = "/tmp/other"
			p.Selection.Destination.ReleaseDir = "/tmp/other/codex/0.153.4-x86_64-unknown-linux-musl"
			p.Selection.Destination.Executable = "/tmp/other/codex/0.153.4-x86_64-unknown-linux-musl/bin/codex"
		}},
	}
	for _, tc := range mutators {
		t.Run(tc.name, func(t *testing.T) {
			p := validCodexPlanV2()
			tc.edit(p)
			got := ComputeDigestV2(p)
			if got == baseDigest {
				t.Fatalf("mutating %s kept the digest", tc.name)
			}
			raw := mustMarshalV2(t, p)
			doc, err := ReadApprovedPlan(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("mutated plan must still be a valid document: %v", err)
			}
			v2, _ := doc.V2()
			if v2.Digest == baseDigest {
				t.Fatal("approved digest did not follow the selection")
			}
		})
	}

	t.Run("observation material is not an approval field", func(t *testing.T) {
		raw := mustMarshalV2(t, validCodexPlanV2())
		if ComputeDigestV2(validCodexPlanV2()) != baseDigest {
			t.Fatal("rebuilding the fixture changed the digest")
		}
		if bytes.Contains(raw, []byte(`"action"`)) {
			t.Fatal("approval carried action")
		}
	})
}

func TestReadPlanV2RefusesDigestOnlyMutation(t *testing.T) {
	raw := mustMarshalV2(t, validCodexPlanV2())
	var p PlanV2
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	mutated := bytes.Replace(raw, []byte(p.Digest), []byte(strings.Repeat("0", 64)), 1)
	if bytes.Equal(mutated, raw) {
		t.Fatal("digest replacement failed")
	}
	_, err := ReadPlanV2(bytes.NewReader(mutated))
	if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("digest-only mutation: %v", err)
	}
	_, err = ReadApprovedPlan(bytes.NewReader(mutated))
	if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("ReadApprovedPlan digest-only mutation: %v", err)
	}
}

func TestReadPlanV2RefusesNoncanonicalAndContradictorySelection(t *testing.T) {
	t.Run("noncanonical member order", func(t *testing.T) {
		p := validCodexPlanV2()
		p.Selection.Layout.Members[0], p.Selection.Layout.Members[1] = p.Selection.Layout.Members[1], p.Selection.Layout.Members[0]
		_, err := MarshalPlanV2(p)
		if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "canonical") {
			t.Fatalf("unsorted members: %v", err)
		}
	})
	t.Run("duplicate subject path", func(t *testing.T) {
		p := validCodexPlanV2()
		p.Selection.RequiredSubjects[1] = p.Selection.RequiredSubjects[0]
		_, err := MarshalPlanV2(p)
		if KindOf(err) != KindInvalidRequest {
			t.Fatalf("duplicate subjects: %v", err)
		}
	})
	t.Run("invented archive-only subject hash", func(t *testing.T) {
		p := validCodexPlanV2()
		p.Selection.RequiredSubjects = append(p.Selection.RequiredSubjects, SubjectExpectation{
			Path:           "codex-path/rg",
			ExpectedSHA256: fixtureHex(0x77),
			Identity:       "https://example.invalid/identity/codex",
			Issuer:         "https://example.invalid/issuer",
			ProofRole:      "rg",
		})
		p.Selection.Source.Proofs = append(p.Selection.Source.Proofs, ProofLocator{
			Role: "rg", URL: "https://example.invalid/rg.sigstore",
		})
		_, err := MarshalPlanV2(p)
		if KindOf(err) != KindInvalidRequest {
			t.Fatalf("invented rg subject: %v", err)
		}
	})
	t.Run("grok exact digest", func(t *testing.T) {
		p := validGrokPlanV2()
		p.Selection.FetchedObject.DigestState = DigestStateExact
		p.Selection.FetchedObject.SHA256 = fixtureHex(0x12)
		p.Selection.FetchedObject.SizeState = SizeStateExact
		p.Selection.FetchedObject.Size = 12
		_, err := MarshalPlanV2(p)
		if KindOf(err) != KindInvalidRequest {
			t.Fatalf("grok with invented digest: %v", err)
		}
	})
	t.Run("codex origin-only", func(t *testing.T) {
		p := validCodexPlanV2()
		p.Selection.Verification = VerificationProfileV2{Kind: VerificationNoneOriginOnly, Cosign: nil}
		_, err := MarshalPlanV2(p)
		if KindOf(err) != KindInvalidRequest {
			t.Fatalf("codex origin-only: %v", err)
		}
	})
	t.Run("omitted cosign key", func(t *testing.T) {
		raw := mustMarshalV2(t, validGrokPlanV2())
		edited := bytes.Replace(raw, []byte(`"cosign": null`), []byte(``), 1)
		edited = bytes.Replace(edited, []byte(`"kind": "none-origin-only",`+"\n      "), []byte(`"kind": "none-origin-only"`+"\n      "), 1)
		_, err := ReadPlanV2(bytes.NewReader(edited))
		if KindOf(err) != KindInvalidRequest {
			t.Fatalf("omitted cosign: %v", err)
		}
	})
}

func requireV2DocumentRefused(t *testing.T, raw []byte) {
	t.Helper()
	if _, err := ReadPlanV2(bytes.NewReader(raw)); KindOf(err) != KindInvalidRequest {
		t.Fatalf("ReadPlanV2: %v", err)
	}
	if _, err := ReadApprovedPlan(bytes.NewReader(raw)); KindOf(err) != KindInvalidRequest {
		t.Fatalf("ReadApprovedPlan: %v", err)
	}
}

func TestPlanV2ProofRolesAreABijection(t *testing.T) {
	raw := mustMarshalV2(t, validCodexPlanV2())
	direct, err := ReadPlanV2(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	union, err := ReadApprovedPlan(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	v2, ok := union.V2()
	if !ok {
		t.Fatal("canonical Codex document is not v2")
	}
	consumed := map[string]struct{}{}
	for _, s := range direct.Selection.RequiredSubjects {
		if _, dup := consumed[s.ProofRole]; dup {
			t.Fatalf("canonical fixture consumed proof_role %q twice", s.ProofRole)
		}
		consumed[s.ProofRole] = struct{}{}
	}
	if len(consumed) != len(direct.Selection.Source.Proofs) || len(consumed) != len(v2.Selection.Source.Proofs) {
		t.Fatalf("canonical consumed roles %d, source proofs %d", len(consumed), len(direct.Selection.Source.Proofs))
	}
	for _, p := range direct.Selection.Source.Proofs {
		if _, ok := consumed[p.Role]; !ok {
			t.Fatalf("canonical source proof role %q is not consumed", p.Role)
		}
	}

	t.Run("duplicated consumed role", func(t *testing.T) {
		p := validCodexPlanV2()
		p.Selection.RequiredSubjects[1].ProofRole = p.Selection.RequiredSubjects[0].ProofRole
		if _, err := MarshalPlanV2(p); KindOf(err) != KindInvalidRequest {
			t.Fatalf("MarshalPlanV2 duplicated consumed role: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		subs := doc["selection"].(map[string]any)["required_subjects"].([]any)
		first := subs[0].(map[string]any)["proof_role"]
		subs[1].(map[string]any)["proof_role"] = first
		edited, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		requireV2DocumentRefused(t, edited)
	})

	t.Run("orphan source role", func(t *testing.T) {
		p := validCodexPlanV2()
		p.Selection.Source.Proofs = append(p.Selection.Source.Proofs, ProofLocator{
			Role: "orphan", URL: "https://example.invalid/orphan.sigstore",
		})
		if _, err := MarshalPlanV2(p); KindOf(err) != KindInvalidRequest {
			t.Fatalf("MarshalPlanV2 orphan source role: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		src := doc["selection"].(map[string]any)["source"].(map[string]any)
		proofs := src["proofs"].([]any)
		src["proofs"] = append(proofs, map[string]any{
			"role": "orphan", "url": "https://example.invalid/orphan.sigstore",
		})
		edited, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		requireV2DocumentRefused(t, edited)
	})
}

func TestReadPlanV2RefusesNullScalarsAndKeepsEmptyZeroAndNullableVerifier(t *testing.T) {
	raw := mustMarshalV2(t, validGrokPlanV2())
	direct, err := ReadPlanV2(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	union, err := ReadApprovedPlan(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	v2, ok := union.V2()
	if !ok || v2.Selection.Verification.Cosign != nil || direct.Selection.FetchedObject.SHA256 != "" || direct.Selection.FetchedObject.Size != 0 {
		t.Fatalf("legal empty SHA, bounded size 0 and nullable cosign must remain accepted")
	}

	t.Run("null sha256", func(t *testing.T) {
		edited := bytes.Replace(raw, []byte(`"sha256": ""`), []byte(`"sha256": null`), 1)
		if bytes.Equal(edited, raw) {
			t.Fatal("sha256 wire edit missed")
		}
		requireV2DocumentRefused(t, edited)
	})
	t.Run("null size", func(t *testing.T) {
		edited := bytes.Replace(raw, []byte(`"size": 0`), []byte(`"size": null`), 1)
		if bytes.Equal(edited, raw) {
			t.Fatal("size wire edit missed")
		}
		requireV2DocumentRefused(t, edited)
	})
	t.Run("folded null sha256", func(t *testing.T) {
		edited := bytes.Replace(raw, []byte(`"sha256": ""`), []byte(`"Sha256": null`), 1)
		if bytes.Equal(edited, raw) {
			t.Fatal("folded sha256 wire edit missed")
		}
		requireV2DocumentRefused(t, edited)
	})
}

func TestGrokUnknownDigestIsEmptyAndCodexHasNoRemainderHashes(t *testing.T) {
	grok := mustMarshalV2(t, validGrokPlanV2())
	var gp PlanV2
	if err := json.Unmarshal(grok, &gp); err != nil {
		t.Fatal(err)
	}
	if gp.Selection.FetchedObject.DigestState != DigestStateUnknown || gp.Selection.FetchedObject.SHA256 != "" {
		t.Fatalf("grok digest state %+v", gp.Selection.FetchedObject)
	}
	codex := validCodexPlanV2()
	for _, m := range codex.Selection.Layout.Members {
		if m.Role != MemberRoleSignedSubject {
			for _, s := range codex.Selection.RequiredSubjects {
				if s.Path == m.Path {
					t.Fatalf("archive-only path %q has a subject hash", m.Path)
				}
			}
		}
	}
}
