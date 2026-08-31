// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/olivaresai/olivares/core/secure/modelsign"
)

// externalplugins.go — S142: the deny-closed host gate for EXTERNAL (third-party)
// connector plugin binaries. The trust model:
//
//   - The OPERATOR pins both the artifact digest and the trust anchors in the SAME
//     operator-owned config file that already carries source secrets
//     (OLIVARES_SOURCES_CONFIG — CB-1, the decided-once wiring input). The engine
//     never fetches trust from the network and never infers it from the binary.
//   - An external binary runs ONLY if (a) its sha256 matches the operator-pinned
//     digest, (b) a Sigstore/DSSE attestation bundle over that digest verifies
//     against the operator trust policy (the modelsign verifier), and
//     (c) go-plugin SecureConfig re-pins the checksum at exec time
//     (runtime.LoadSourcePluginVerified). There is NO observe mode and NO
//     allow-unsigned escape hatch: the dev loop is "sign with your own key, trust
//     your own pubkey" (bare-key mode), never "skip the signature".
//   - Division of labor: the catalog module's connector admission (S142,
//     modules/catalog) is the tenant-facing CERTIFICATION record — what a tenant
//     claims/verifies about a connector artifact; THIS gate is the host-side EXEC
//     enforcement on the machine that actually launches the process. Defense in
//     depth, the same pairing as admit-route + deployment-gate.
//
// admitExternalPlugin is PURE (config in, verdict out, no engine state) so it
// unit-tests without a boot; wireSources (sources.go) owns the logging and the
// load call.

// externalPluginSpec provisions one EXTERNAL connector plugin binary inside a
// sourceSpec or documentSpec (sources.go). All trust inputs are
// operator-supplied paths/digests; none of them is secret material.
type externalPluginSpec struct {
	// Path is the absolute path to the plugin executable the operator placed on
	// the host. The engine never downloads a binary; placement is an operator act.
	Path string `json:"path"`
	// SHA256 is the REQUIRED artifact pin: the lowercase-hex sha256 of the binary
	// (a "sha256:" prefix and uppercase hex are tolerated and normalized). The
	// on-disk file must hash to exactly this, and the attestation's subject must
	// cover it. Missing or malformed ⇒ the source is refused (deny-closed).
	SHA256 string `json:"sha256"`
	// Bundle is the path to the detached Sigstore attestation bundle JSON over the
	// pinned digest (the `cosign attest-blob --bundle` / `gh attestation download`
	// output). Public signature material, not sealed operator config. Empty ⇒
	// refused: unsigned external plugins never run.
	Bundle string `json:"bundle"`
	// PredicateTypes optionally NARROWS the trust policy's predicate allow-list
	// for THIS source (e.g. require SLSA provenance specifically, not any SBOM).
	// It can never WIDEN it — a type outside the policy's set is dropped, and an
	// empty intersection refuses (the catalog effectivePredicates posture).
	PredicateTypes []string `json:"predicate_types,omitempty"`
}

// connectorTrustSpec is the operator trust root for external connector binaries
// (sourcesConfig.ConnectorTrust): the same field semantics as the
// admission policies. Public material only (PEM certs/keys, regexps, URLs —
// never a private key). Keyless pins are set together-or-neither: the modelsign
// anchoring itself enforces that an identity/issuer pin with no matching
// certificate material refuses rather than downgrades.
type connectorTrustSpec struct {
	// AllowedIdentities are regexps matched against the signing certificate's SAN
	// (Sigstore keyless: the CI workflow/builder identity).
	AllowedIdentities []string `json:"allowed_identities,omitempty"`
	// AllowedIssuers are exact OIDC issuer URLs paired with AllowedIdentities.
	AllowedIssuers []string `json:"allowed_issuers,omitempty"`
	// TrustedKeys are PEM (PKIX) public keys accepted for bare-key signatures —
	// the third-party-developer loop: sign with your own key, the operator trusts
	// your published pubkey.
	TrustedKeys []string `json:"trusted_keys,omitempty"`
	// TrustedRoots are PEM CA certificates (Fulcio root or a private PKI root) a
	// certificate-based signature must chain to.
	TrustedRoots []string `json:"trusted_roots,omitempty"`
	// AllowedPredicates overrides the default predicate allow-list
	// (defaultExternalPluginPredicates) for ALL external plugin sources; a
	// per-source PredicateTypes may then only narrow it further.
	AllowedPredicates []string `json:"allowed_predicates,omitempty"`
}

// toTrustPolicy maps the operator spec onto the modelsign verifier's policy.
func (c connectorTrustSpec) toTrustPolicy() modelsign.TrustPolicy {
	return modelsign.TrustPolicy{
		Roots:             c.TrustedRoots,
		AllowedIdentities: c.AllowedIdentities,
		AllowedIssuers:    c.AllowedIssuers,
		Keys:              c.TrustedKeys,
	}
}

// defaultExternalPluginPredicates is the predicate allow-list when neither the
// trust policy nor the source narrows it: the supply-chain attestation types a
// released Go binary actually carries — SLSA provenance (v1 and the v0.2 the
// slsa-github-generator still emits) and SBOM attestations (SPDX/CycloneDX).
// Explicitly NOT OMS (PredicateTypeOMSv1): that predicate is model-weights-shaped
// (a per-file serialization manifest); accepting it here would let a model
// signature stand in for binary provenance. The catalog's MCP default
// (modules/catalog defaultAllowedPredicates) includes OMS for its own reasons —
// an exec gate must not.
func defaultExternalPluginPredicates() []string {
	return []string{
		modelsign.PredicateTypeSLSAProvenanceV1,
		modelsign.PredicateTypeSLSAProvenanceV02,
		modelsign.PredicateTypeSPDX,
		modelsign.PredicateTypeCycloneDX,
	}
}

// effectiveExternalPredicates resolves the predicate allow-list for one source:
// the source's PredicateTypes narrows the trust policy's set (or the defaults)
// and may never WIDEN it — a requested predicate outside the policy is dropped,
// and an empty intersection stays empty so VerifyAttestation refuses deny-closed
// (mirrors modules/catalog effectivePredicates).
//
// OMS (model-signing) is filtered out UNCONDITIONALLY, even when an operator
// lists it in AllowedPredicates: an exec gate must never let a model-weights
// signature stand in for binary provenance (defaultExternalPluginPredicates
// documents why). This is an invariant of the gate, not just its default — the
// catalog certification side may include OMS, the host that launches a process
// may not.
func effectiveExternalPredicates(trust connectorTrustSpec, requested []string) []string {
	allowed := trust.AllowedPredicates
	if len(allowed) == 0 {
		allowed = defaultExternalPluginPredicates()
	}
	allowed = withoutOMS(allowed)
	if len(requested) == 0 {
		return allowed
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = struct{}{}
	}
	var out []string
	for _, p := range requested {
		if _, ok := allowedSet[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// withoutOMS drops the OpenSSF Model Signing predicate from a list: the host
// exec gate refuses to accept weights-shaped attestations for a binary, no
// matter how the operator configured the allow-list.
func withoutOMS(predicates []string) []string {
	out := make([]string, 0, len(predicates))
	for _, p := range predicates {
		if p == modelsign.PredicateTypeOMSv1 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// normalizeExternalPluginDigest lowercases, strips an optional "sha256:" prefix
// and returns "" for anything that is not a 64-char hex string — a malformed pin
// must never accidentally compare equal (the modelsign normalizeSHA256 posture).
func normalizeExternalPluginDigest(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) != 64 {
		return ""
	}
	for _, r := range d {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return d
}

// admitExternalPlugin is the deny-closed admission decision for one external
// plugin binary. Every failure path returns a non-empty refusal — the single
// source of truth for wireSources' WARN line — and success returns the
// NORMALIZED digest the runtime must pin at exec
// (runtime.LoadSourcePluginVerified). It performs only local file reads (hash
// the binary, read the bundle); no network, no engine state.
func admitExternalPlugin(spec externalPluginSpec, trust *connectorTrustSpec) (sha256Hex string, refusal string) {
	// 1) Trust anchors first: with no operator trust root there is nothing any
	// signature could ever anchor to. Refuse before touching the filesystem.
	if trust == nil || (len(trust.TrustedRoots) == 0 && len(trust.TrustedKeys) == 0) {
		return "", "external connector plugins are deny-closed: configure connector_trust with at least one trust anchor (trusted_roots or trusted_keys)"
	}
	// 1b) Keyless pins are cosign-style: an identity pin without an issuer pin (or
	// vice versa) can never admit anything (modelsign refuses a half-configured
	// keyless policy), so a one-sided pin is an operator misconfiguration, not a
	// valid-but-strict policy. Refuse it with a clear reason rather than letting
	// every source fail opaquely at verification time (mirrors the catalog
	// admission policy's together-or-neither guard).
	if (len(trust.AllowedIdentities) > 0) != (len(trust.AllowedIssuers) > 0) {
		return "", "connector_trust keyless verification requires BOTH allowed_identities and allowed_issuers (cosign-style); set both or neither"
	}
	// 2) The artifact pin. A missing or unusable pin refuses — it never degrades
	// to "hash whatever is on disk and call that the pin" (TOFU is not a pin).
	want := normalizeExternalPluginDigest(spec.SHA256)
	if want == "" {
		return "", "sha256 pin is missing or not a 64-char hex sha256 digest (a supplied-but-unusable pin is refused, never silently ignored)"
	}
	if spec.Path == "" {
		return "", "plugin path is empty"
	}
	// 3) Hash the on-disk binary (streaming — plugin binaries are tens of MB).
	// The error itself is not logged: the path is operator-supplied and safe to
	// cite, but an os error string adds nothing the operator cannot see locally.
	f, err := os.Open(spec.Path)
	if err != nil {
		return "", fmt.Sprintf("cannot read plugin binary %q (the file must exist and be readable by the engine)", spec.Path)
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	_ = f.Close()
	if copyErr != nil {
		return "", fmt.Sprintf("failed reading plugin binary %q while hashing", spec.Path)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		// Digests are public — stating both lets the operator diff the artifact.
		return "", fmt.Sprintf("plugin binary digest mismatch: expected sha256 %s, got %s (the on-disk binary is not the pinned artifact)", want, got)
	}
	// 4) The attestation. No bundle ⇒ no run: there is no observe mode and no
	// allow-unsigned escape hatch for external binaries.
	if spec.Bundle == "" {
		return "", "no attestation bundle configured: unsigned external plugins never run (sign the binary and pin the signer in connector_trust)"
	}
	// Plain read: the bundle is public signature material, not sealed operator
	// config (readOperatorConfig is for secret-bearing files).
	bundleJSON, err := os.ReadFile(spec.Bundle)
	if err != nil {
		return "", fmt.Sprintf("cannot read attestation bundle %q (the file must exist and be readable by the engine)", spec.Bundle)
	}
	// 5) Verify: signature anchored to the operator trust policy, predicate in
	// the (possibly narrowed) allow-list, and the statement subject covering the
	// pinned digest. VerifyAttestation is itself deny-closed (empty predicate
	// set admits nothing; an unusable digest pin refuses).
	preds := effectiveExternalPredicates(*trust, spec.PredicateTypes)
	verdict, err := modelsign.VerifyAttestation(bundleJSON, trust.toTrustPolicy(), preds, want)
	if err != nil {
		if errors.Is(err, modelsign.ErrMalformedBundle) {
			return "", fmt.Sprintf("attestation bundle %q is not a parseable Sigstore attestation", spec.Bundle)
		}
		return "", fmt.Sprintf("attestation verification errored: %v", err)
	}
	if !verdict.Verified || !verdict.ArtifactVerified {
		// A well-formed bundle that fails to verify is a RECORDED negative
		// verdict, not an error: the refusal carries the verifier's reason.
		// ArtifactVerified is checked belt-and-braces: the digest pin is always
		// supplied above, so on success VerifyAttestation sets both flags
		// together by construction.
		return "", fmt.Sprintf("attestation did not verify: %s", verdict.Reason)
	}
	return want, ""
}
