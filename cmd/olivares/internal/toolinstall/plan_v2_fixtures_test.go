// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"fmt"
	"strings"
	"testing"
)

func fixtureHex(b byte) string {
	return strings.Repeat(fmt.Sprintf("%02x", b), 32)
}

func v1ClaudePlan() *Plan {
	return &Plan{
		Schema:           PlanSchema,
		Driver:           claudeDriver,
		Channel:          ChannelExact,
		RequestedVersion: "2.1.261",
		Version:          "2.1.261",
		Platform:         Platform{OS: "linux", Arch: "amd64", Libc: "glibc"},
		VendorPlatform:   "linux-x64",
		Source: Source{
			Kind:         SourceOfficial,
			BaseURL:      "https://example.invalid",
			ManifestURL:  "https://example.invalid/manifest.json",
			SignatureURL: "https://example.invalid/manifest.json.sig",
			ArtifactURL:  "https://example.invalid/claude",
		},
		Artifact:    Artifact{Name: "claude", SHA256: "aa", Size: 1},
		Provenance:  Provenance{Class: ProvenancePublisherSigned, KeyFingerprint: "F", ManifestSHA256: "m", KeySHA256: "k"},
		Destination: Destination{Root: "/tmp/root", ReleaseDir: "/tmp/root/claude/v", Executable: "/tmp/root/claude/v/bin/claude"},
		Verified:    []string{"manifest_signature", "manifest_version", "artifact_selection"},
		Action:      ActionInstall,
		Observed:    []string{"existing_release_dir"},
		Existing:    &Observed{Note: "must not appear in the approval file"},
	}
}

func validCodexPlanV2() *PlanV2 {
	return &PlanV2{
		Schema: PlanSchemaV2,
		Selection: SelectionV2{
			Driver:           DriverCodex,
			Channel:          ChannelExact,
			RequestedVersion: "0.153.4",
			Version:          "0.153.4",
			Platform:         PlatformV2{OS: "linux", Arch: "amd64", Libc: "musl"},
			VendorPlatform:   "x86_64-unknown-linux-musl",
			Source: SourcePolicyV2{
				Kind: SourceOfficial,
				Pointer: URLRef{
					State: URLStatePresent,
					URL:   "https://example.invalid/channels/0.153.4",
				},
				Checksums: URLRef{
					State: URLStatePresent,
					URL:   "https://example.invalid/codex-package_SHA256SUMS",
				},
				Package: URLRef{
					State: URLStatePresent,
					URL:   "https://example.invalid/codex-package-x86_64-unknown-linux-musl.tar.gz",
				},
				Proofs: []ProofLocator{
					{Role: "bwrap", URL: "https://example.invalid/bwrap.sigstore"},
					{Role: "codex", URL: "https://example.invalid/codex.sigstore"},
					{Role: "codex-code-mode-host", URL: "https://example.invalid/codex-code-mode-host.sigstore"},
				},
				AllowedOrigins: []string{"https://example.invalid"},
				Redirects:      RedirectPolicyV2{MaxHops: 0, AllowedOrigins: []string{}},
			},
			FetchedObject: FetchedObjectExpectation{
				DigestState: DigestStateExact,
				SHA256:      fixtureHex(0x11),
				SizeState:   SizeStateExact,
				Size:        1024,
				MaxSize:     4096,
			},
			Layout: ExpectedLayout{
				ID:           LayoutIDCodexPackageV1,
				Variant:      LayoutVariantCodex,
				EntryPoint:   "bin/codex",
				ResourcesDir: "codex-resources",
				PathDir:      "codex-path",
				Members:      expectedMembersFromSpec(codexLayoutMembers()),
				Limits: ExtractionLimits{
					MaxCompressedBytes: 4096,
					MaxExpandedBytes:   8192,
					MaxMembers:         11,
					MaxMemberBytes:     4096,
				},
			},
			RequiredSubjects: []SubjectExpectation{
				{
					Path:           "bin/codex",
					ExpectedSHA256: fixtureHex(0x21),
					Identity:       "https://example.invalid/identity/codex",
					Issuer:         "https://example.invalid/issuer",
					ProofRole:      "codex",
				},
				{
					Path:           "bin/codex-code-mode-host",
					ExpectedSHA256: fixtureHex(0x22),
					Identity:       "https://example.invalid/identity/codex",
					Issuer:         "https://example.invalid/issuer",
					ProofRole:      "codex-code-mode-host",
				},
				{
					Path:           "codex-resources/bwrap",
					ExpectedSHA256: fixtureHex(0x23),
					Identity:       "https://example.invalid/identity/codex",
					Issuer:         "https://example.invalid/issuer",
					ProofRole:      "bwrap",
				},
			},
			PackagePolicyID: PackagePolicyCodexMixedV1,
			Verification: VerificationProfileV2{
				Kind: VerificationSigstoreCosign,
				Cosign: &CosignProfileV2{
					ID:                   "cosign",
					Version:              "v3.1.3",
					ExecutableSHA256:     fixtureHex(0x31),
					TrustedRootIteration: 10,
					TrustedRootSHA256:    fixtureHex(0x32),
					Mode:                 CosignModeOffline,
					RefreshPolicy:        RefreshPolicyNone,
					EgressPolicy:         EgressPolicyNone,
					BundleFormat:         BundleFormatCosignLegacy,
					RequireSignature:     true,
					RequireRekor:         true,
					RequireSCT:           true,
				},
			},
			Destination: Destination{
				Root:       "/tmp/root",
				ReleaseDir: "/tmp/root/codex/0.153.4-x86_64-unknown-linux-musl",
				Executable: "/tmp/root/codex/0.153.4-x86_64-unknown-linux-musl/bin/codex",
			},
		},
	}
}

func validGrokPlanV2() *PlanV2 {
	return &PlanV2{
		Schema: PlanSchemaV2,
		Selection: SelectionV2{
			Driver:           DriverGrok,
			Channel:          ChannelExact,
			RequestedVersion: "1.0.13",
			Version:          "1.0.13",
			Platform:         PlatformV2{OS: "linux", Arch: "amd64", Libc: ""},
			VendorPlatform:   "linux-x86_64",
			Source: SourcePolicyV2{
				Kind: SourceOfficial,
				Pointer: URLRef{
					State: URLStatePresent,
					URL:   "https://example.invalid/cli/1.0.13",
				},
				Checksums: URLRef{State: URLStateAbsent, URL: ""},
				Package: URLRef{
					State: URLStatePresent,
					URL:   "https://example.invalid/cli/grok-1.0.13-linux-x86_64",
				},
				Proofs:         []ProofLocator{},
				AllowedOrigins: []string{"https://example.invalid"},
				Redirects:      RedirectPolicyV2{MaxHops: 0, AllowedOrigins: []string{}},
			},
			FetchedObject: FetchedObjectExpectation{
				DigestState: DigestStateUnknown,
				SHA256:      "",
				SizeState:   SizeStateBounded,
				Size:        0,
				MaxSize:     4096,
			},
			Layout: ExpectedLayout{
				ID:           LayoutIDGrokBinV1,
				Variant:      LayoutVariantGrok,
				EntryPoint:   "bin/grok",
				ResourcesDir: "",
				PathDir:      "",
				Members:      expectedMembersFromSpec(grokLayoutMembers()),
				Limits: ExtractionLimits{
					MaxCompressedBytes: 4096,
					MaxExpandedBytes:   8192,
					MaxMembers:         2,
					MaxMemberBytes:     4096,
				},
			},
			RequiredSubjects: []SubjectExpectation{},
			PackagePolicyID:  PackagePolicyOriginOnlyV1,
			Verification: VerificationProfileV2{
				Kind:   VerificationNoneOriginOnly,
				Cosign: nil,
			},
			Destination: Destination{
				Root:       "/tmp/root",
				ReleaseDir: "/tmp/root/grok/1.0.13-linux-x86_64",
				Executable: "/tmp/root/grok/1.0.13-linux-x86_64/bin/grok",
			},
		},
	}
}

func mustMarshalV2(t *testing.T, p *PlanV2) []byte {
	t.Helper()
	raw, err := MarshalPlanV2(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustMarshalV1(t *testing.T, p *Plan) []byte {
	t.Helper()
	raw, err := MarshalPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
