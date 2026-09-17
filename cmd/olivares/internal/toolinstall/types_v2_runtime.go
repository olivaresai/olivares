// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

// RequestV2 is the operator request handed to PackageProviderV2.ResolveV2.
// It is a runtime value, not a persisted plan or receipt schema.
//
// Field sources (R3 CONTRACT request + accepted PlanV2 platform):
//   - Driver, Version, DestRoot, Source: same meaning as v1 Request.
//     Version is an exact version or a provider pointer name (latest, stable).
//     Source is empty for the official origin or an https mirror base; it never
//     selects the pinned verification policy.
//   - Platform: PlanV2 PlatformV2, so libc is always represented (empty for Grok).
//
// PackagePolicyID is not a request field: the provider binds the closed policy
// for the driver (SelectionV2.PackagePolicyID). CLI consent remains a later
// wiring concern and is not invented here as a trust status.
type RequestV2 struct {
	Driver   string
	Version  string
	Platform PlatformV2
	DestRoot string
	Source   string
}

// ResolvedMaterialV2 is metadata ResolveV2 retained for later FetchV2 / PlaceV2 /
// VerifyPayload. It is resolved from the declared metadata GETs, not a verified
// payload observation and not receipt/v2.
//
// Field sources (R3 CONTRACT ResolveV2 metadata GETs + SourcePolicyV2):
//   - Pointer: SourcePolicyV2.Pointer (URLRef). Bytes are the pointer document.
//   - Checksums: SourcePolicyV2.Checksums. Absent for origin-only Grok.
//   - Proofs: SourcePolicyV2.Proofs, canonical role order. Bytes are the fetched
//     proof objects (bundles). They are pending material, not VerifyPayload results.
//
// SHA256/Size on nested objects are digests of those metadata bytes, analogous
// to v1 Provenance.ManifestSHA256 over Material.Manifest. They are not
// FetchedObjectObserved and not subject verification. Trusted-root bytes are
// verifier custody (later seam), not resolve material. Package bytes belong to
// FetchV2, not this type. Retained proof paths are receipt retain names and are
// not invented here.
type ResolvedMaterialV2 struct {
	Pointer   ResolvedObjectV2
	Checksums ResolvedObjectV2
	Proofs    []ResolvedProofV2
}

// ResolvedObjectV2 is one metadata object fetched during ResolveV2, or an
// explicit absence matching URLRef.State.
type ResolvedObjectV2 struct {
	State  string
	URL    string
	Bytes  []byte
	SHA256 string
	Size   int64
}

// ResolvedProofV2 is one proof object fetched during ResolveV2, keyed by
// ProofLocator.Role. Path is omitted: receipt retain names are not a resolve
// representation.
type ResolvedProofV2 struct {
	Role   string
	URL    string
	Bytes  []byte
	SHA256 string
	Size   int64
}

// FetchedObjectObserved is the object FetchV2 wrote. It is an observation of
// the downloaded bytes, not FetchedObjectExpectation and not publisher proof.
//
// Field sources (R3 CONTRACT install machine + receipt fetched-object hash/size):
//   - SHA256, Size: digest and length of the bytes written to the fetch destination.
//     Always computed. When the plan's FetchedObjectExpectation.DigestState is
//     unknown (Grok origin-only), this observation still carries the measured
//     digest; that does not elevate provenance.
type FetchedObjectObserved struct {
	SHA256 string
	Size   int64
}

// ObservedPayloadInventory is the closed payload PlaceV2 produced under the
// engine's *os.Root. It is observed after extract/place, not the plan's
// ExpectedLayout and not a verification outcome.
//
// Field source: R3 CONTRACT ObservedPayloadInventory. Members are unique
// relative paths in canonical order. The fetched tar and staging marker are
// not members.
type ObservedPayloadInventory struct {
	Members []ObservedMember
}

// ObservedMember is one path PlaceV2 observed.
//
// Field sources (R3 CONTRACT ObservedMember):
//   - Path, Kind, Mode: relative path, directory|regular, and observed bits.
//   - SHA256, Size: digest and length of each regular file. Directories use
//     empty SHA256 and Size 0; the contract hashes regulars, not directories.
//     Role is not an observation: it lives on ExpectedLayout / PackagePolicyV2.
type ObservedMember struct {
	Path   string
	Kind   string
	SHA256 string
	Size   int64
	Mode   uint32
}

// PackagePolicyV2 is the closed package verification policy VerifyPayload
// applies to a placed inventory. It is a requirement set, not a verified status.
//
// Field sources (R3 CONTRACT VerifyPayload + SelectionV2):
//   - ID: SelectionV2.PackagePolicyID (codex-mixed-v1 | origin-only-v1).
//   - RequiredSubjects: SelectionV2.RequiredSubjects (pending publisher-signed
//     claims). Empty for origin-only-v1.
//   - Layout: SelectionV2.Layout (closed members and roles: signed-subject vs
//     archive-only vs metadata).
//   - Verification: SelectionV2.Verification (sigstore-cosign profile or
//     none-origin-only with null Cosign).
//
// all-executables-publisher-signed is not a supported ID; that policy refuses
// the mixed Codex layout and is not a fallback. No outcome field is recorded.
type PackagePolicyV2 struct {
	ID               string
	RequiredSubjects []SubjectExpectation
	Layout           ExpectedLayout
	Verification     VerificationProfileV2
}
