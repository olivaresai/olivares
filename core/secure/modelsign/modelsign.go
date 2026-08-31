// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package modelsign

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"regexp"
	"sort"
	"strings"
)

// PredicateTypeOMSv1 is the OpenSSF Model Signing v1.0 in-toto predicate type. A
// statement whose predicateType is not this is rejected (we only admit OMS-shaped
// model signatures, not arbitrary in-toto attestations).
const PredicateTypeOMSv1 = "https://model_signing/signature/v1.0"

// statementTypeInToto is the in-toto Statement v1 _type.
const statementTypeInToto = "https://in-toto.io/Statement/v1"

// dssePayloadTypeInToto is the DSSE payloadType for an in-toto statement.
const dssePayloadTypeInToto = "application/vnd.in-toto+json"

// Signing methods recorded in Verdict.Method.
const (
	MethodSigstoreKeyless = "sigstore-keyless" // Fulcio cert + OIDC identity, chained to a trusted root
	MethodCertificatePKI  = "certificate-pki"  // an x509 cert chained to a trusted root (no OIDC identity)
	MethodBareKey         = "bare-key"         // a long-lived public key on the operator's trust list
)

// Fulcio OID extensions carrying the OIDC issuer (the value that pairs with the
// SAN identity). Both the deprecated v1 (raw string) and the current v2
// (DER-encoded UTF8String) are read, newest first.
var (
	oidFulcioIssuerV2 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidFulcioIssuerV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
)

// TrustPolicy is the operator-provisioned trust anchor a verification is evaluated
// against. It is deny-closed by construction: an empty policy admits NOTHING — a
// keyless/PKI signature needs at least one trusted Root (and, for keyless, an
// identity + issuer allow-list); a bare-key signature needs at least one trusted
// public key. This mirrors the catalog's pinned-key posture (modules/catalog/sign.go):
// trust must be explicitly anchored, never assumed.
type TrustPolicy struct {
	// Roots are PEM-encoded CA certificates a leaf certificate must chain to
	// (Sigstore Fulcio root + intermediates, or a private PKI root). Without a
	// trusted root, a certificate-based signature cannot be anchored and is rejected
	// (a self-issued certificate with the right SAN would otherwise forge identity).
	Roots []string
	// AllowedIdentities are regular expressions matched (unanchored, cosign-style)
	// against the leaf certificate's SAN values (URIs, emails, DNS names).
	//
	// Setting AllowedIdentities OR AllowedIssuers selects KEYLESS mode: the leaf must
	// then carry a SAN identity matching one of these AND an OIDC issuer in
	// AllowedIssuers (a SAN-less / issuer-less cert is rejected — no downgrade). Leaving
	// BOTH empty selects an explicit PKI-only posture (trust rests on the chain to a
	// trusted Root, with no identity pin). Configure both together (cosign-style).
	AllowedIdentities []string
	// AllowedIssuers are exact OIDC issuer URLs matched against the Fulcio issuer
	// extension (see AllowedIdentities for the keyless-vs-PKI mode selection).
	AllowedIssuers []string
	// Keys are PEM-encoded (PKIX) public keys accepted for a bare-key signature.
	// Empty ⇒ bare-key signatures are rejected.
	Keys []string
}

// trusted reports whether the policy can anchor any kind of signature at all. A
// fully empty policy is honest about admitting nothing.
func (p TrustPolicy) trusted() bool {
	return len(p.Roots) > 0 || len(p.Keys) > 0
}

// Verdict is the outcome of verifying one OMS signature. It is intentionally rich
// so the admission record (modules/models) can store an honest, auditable account
// of WHAT was verified and what was left to a documented seam.
type Verdict struct {
	// Verified is the deny-closed core result: the DSSE signature is cryptographically
	// valid, the signer is anchored to the trust policy (cert chained to a trusted
	// root with a matching identity+issuer, OR a bare key on the trust list), and the
	// statement is an OMS v1.0 predicate. It does NOT require artifact re-hashing
	// (the manifest's authenticity is the binding); ArtifactVerified is a separate,
	// additive coverage flag.
	Verified bool `json:"verified"`
	// Method is the signing method that was verified (see Method* constants).
	Method string `json:"method,omitempty"`
	// PredicateType is the in-toto predicateType found in the statement.
	PredicateType string `json:"predicate_type,omitempty"`
	// SignerIdentity is the matched SAN identity (keyless/PKI) or the key fingerprint
	// (bare-key) of the signer.
	SignerIdentity string `json:"signer_identity,omitempty"`
	// SignerIssuer is the OIDC issuer (keyless) the certificate was issued under.
	SignerIssuer string `json:"signer_issuer,omitempty"`
	// SignerRoots are the "root:<fp>" markers (fp = full sha256 hex of the CA cert DER)
	// of the trusted ROOT certificate(s) that anchored the leaf — the terminal cert of
	// EVERY verified chain, deduplicated and sorted. Recorded on the verdict so the exact
	// admission-time root can be re-checked later (AnchorStillTrusted): a certificate-mode
	// verdict must stop certifying once its anchoring root is rotated/replaced out of the
	// policy, even if another root remains. Empty for bare-key (which pins its exact key
	// via SignerIdentity) and empty for a certificate signature that did not verify.
	SignerRoots []string `json:"signer_roots,omitempty"`
	// SubjectName/SubjectDigest identify the signed model (the in-toto subject).
	SubjectName   string `json:"subject_name,omitempty"`
	SubjectDigest string `json:"subject_digest,omitempty"`
	// ResourceCount is the number of per-file entries in the signed manifest.
	ResourceCount int `json:"resource_count"`
	// HashType is the manifest's serialization hash type (sha256|blake2b|blake3).
	HashType string `json:"hash_type,omitempty"`
	// ArtifactVerified is true only when the caller supplied resolved per-file
	// digests AND every one matched the signed manifest (the model on disk IS the
	// model that was signed). False means "not checked" (the control plane usually
	// cannot read the artifact) — see ArtifactCoverage.
	ArtifactVerified bool   `json:"artifact_verified"`
	ArtifactCoverage string `json:"artifact_coverage,omitempty"`
	// TransparencyLogPresent reports that the bundle carried Rekor tlog material.
	TransparencyLogPresent bool `json:"transparency_log_present"`
	// TransparencyLogVerified is ALWAYS false in this native verifier (the documented
	// honest seam, see TransparencyLogNote): inclusion-proof verification against the
	// Sigstore log needs an external cosign/sigstore-go step.
	TransparencyLogVerified bool   `json:"transparency_log_verified"`
	TransparencyLogNote     string `json:"transparency_log_note,omitempty"`
	// Reason explains why Verified is false (deny-closed honesty).
	Reason string `json:"reason,omitempty"`
}

// --- OMS / Sigstore-bundle wire types (the subset we verify) -----------------

// bundle is the detached Sigstore bundle (application/vnd.dev.sigstore.bundle.*).
type bundle struct {
	MediaType            string               `json:"mediaType"`
	VerificationMaterial verificationMaterial `json:"verificationMaterial"`
	DSSEEnvelope         *dsseEnvelope        `json:"dsseEnvelope"`
}

type verificationMaterial struct {
	Certificate          *certBytes            `json:"certificate"`
	X509CertificateChain *x509CertificateChain `json:"x509CertificateChain"`
	PublicKey            *publicKeyHint        `json:"publicKey"`
	TLogEntries          []json.RawMessage     `json:"tlogEntries"`
}

type certBytes struct {
	RawBytes string `json:"rawBytes"` // base64(DER)
}

type x509CertificateChain struct {
	Certificates []certBytes `json:"certificates"` // leaf first
}

type publicKeyHint struct {
	Hint string `json:"hint"`
}

type dsseEnvelope struct {
	Payload     string          `json:"payload"`     // base64(statement JSON)
	PayloadType string          `json:"payloadType"` // application/vnd.in-toto+json
	Signatures  []dsseSignature `json:"signatures"`
}

type dsseSignature struct {
	Sig   string `json:"sig"` // base64(signature bytes)
	KeyID string `json:"keyid,omitempty"`
}

// statement is the in-toto Statement v1 the DSSE payload decodes to.
type statement struct {
	Type          string          `json:"_type"`
	Subject       []subject       `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

type subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// omsPredicate is the OMS v1.0 predicate (resources[] manifest + serialization).
type omsPredicate struct {
	Resources     []resourceDescriptor `json:"resources"`
	Serialization serialization        `json:"serialization"`
}

type resourceDescriptor struct {
	Name      string `json:"name"`
	Digest    string `json:"digest"`    // hex
	Algorithm string `json:"algorithm"` // sha256 | blake2b | blake3
}

type serialization struct {
	Method        string   `json:"method"`    // files | shards
	HashType      string   `json:"hash_type"` // sha256 | blake2b | blake3
	AllowSymlinks bool     `json:"allow_symlinks"`
	ShardSize     *int     `json:"shard_size,omitempty"`
	IgnorePaths   []string `json:"ignore_paths,omitempty"`
}

// Errors callers can match on. A verification that cannot anchor trust returns a
// Verdict{Verified:false, Reason:...} and a nil error — a non-nil error is reserved
// for a malformed bundle the verifier could not even parse.
var (
	// ErrMalformedBundle is returned when the input is not a parseable OMS/Sigstore
	// bundle (distinct from a well-formed bundle that fails to verify).
	ErrMalformedBundle = errors.New("modelsign: malformed signature bundle")
)

// Verify verifies an OMS v1.0 signature bundle against the trust policy. resolved
// is an OPTIONAL map of manifest-relative file name → lowercase-hex digest the
// caller computed locally; when non-empty every entry it contains is checked against
// the signed manifest (and every manifest entry must be covered) to set
// ArtifactVerified. Pass nil when the control plane cannot read the artifact —
// the signature/identity are still fully verified, ArtifactVerified is just false.
//
// It is deny-closed: a parse error returns ErrMalformedBundle; any failure to
// cryptographically verify and anchor the signer returns Verdict{Verified:false}
// with a Reason and a nil error.
func Verify(bundleJSON []byte, policy TrustPolicy, resolved map[string]string) (Verdict, error) {
	v, st, done, err := verifyAnchoredStatement(bundleJSON, policy)
	if err != nil || done {
		return v, err
	}
	v.PredicateType = st.PredicateType
	if st.PredicateType != PredicateTypeOMSv1 {
		v.Reason = fmt.Sprintf("predicateType %q is not the OpenSSF Model Signing v1.0 type %q", st.PredicateType, PredicateTypeOMSv1)
		return v, nil
	}
	if len(st.Subject) > 0 {
		v.SubjectName = st.Subject[0].Name
		v.SubjectDigest = st.Subject[0].Digest["sha256"]
	}
	var pred omsPredicate
	if err := json.Unmarshal(st.Predicate, &pred); err != nil {
		return Verdict{}, fmt.Errorf("%w: OMS predicate JSON: %v", ErrMalformedBundle, err)
	}
	if len(pred.Resources) == 0 {
		v.Reason = "OMS predicate has no resources (an empty manifest signs nothing)"
		return v, nil
	}
	v.ResourceCount = len(pred.Resources)
	v.HashType = pred.Serialization.HashType

	// The signature is cryptographically valid and anchored, and the statement is an
	// OMS v1.0 model-signing predicate. That is the deny-closed verification result.
	v.Verified = true

	// Additive coverage: if the caller supplied locally-computed digests, confirm the
	// on-disk artifact IS the signed model (re-hash match against the manifest).
	if len(resolved) > 0 {
		if reason := checkManifest(pred.Resources, resolved); reason == "" {
			v.ArtifactVerified = true
			v.ArtifactCoverage = fmt.Sprintf("re-hashed %d/%d manifest files matched the signed digests", len(pred.Resources), len(pred.Resources))
		} else {
			v.ArtifactVerified = false
			v.ArtifactCoverage = reason
		}
	} else {
		v.ArtifactCoverage = "artifact files not re-hashed (no resolved digests supplied); signature and signer identity are verified, on-disk artifact integrity is not"
	}
	return v, nil
}

// verifyAnchoredStatement is the shared core of Verify and VerifyAttestation
//: it parses the Sigstore bundle, verifies the DSSE signature over the PAE
// pre-image, anchors the signer to the trust policy, and decodes the in-toto
// Statement v1. done=true means v is FINAL (a deny-closed Reason is set); a
// non-nil error is reserved for a malformed bundle. The caller owns everything
// predicate-specific (the OMS pin, the attestation allow-list, subjects).
func verifyAnchoredStatement(bundleJSON []byte, policy TrustPolicy) (v Verdict, st statement, done bool, err error) {
	var b bundle
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		return Verdict{}, statement{}, false, fmt.Errorf("%w: %v", ErrMalformedBundle, err)
	}
	if b.DSSEEnvelope == nil || len(b.DSSEEnvelope.Signatures) == 0 {
		return Verdict{}, statement{}, false, fmt.Errorf("%w: no DSSE envelope/signatures", ErrMalformedBundle)
	}
	env := b.DSSEEnvelope
	if env.PayloadType != dssePayloadTypeInToto {
		return Verdict{}, statement{}, false, fmt.Errorf("%w: unexpected payloadType %q", ErrMalformedBundle, env.PayloadType)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return Verdict{}, statement{}, false, fmt.Errorf("%w: payload base64: %v", ErrMalformedBundle, err)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)
	if err != nil {
		return Verdict{}, statement{}, false, fmt.Errorf("%w: signature base64: %v", ErrMalformedBundle, err)
	}

	v = Verdict{TransparencyLogPresent: len(b.VerificationMaterial.TLogEntries) > 0}
	if v.TransparencyLogPresent {
		v.TransparencyLogNote = "bundle carries Rekor transparency-log material; native verification does NOT check inclusion (run an external cosign/sigstore-go step for tlog-inclusion proof)"
	}

	if !policy.trusted() {
		v.Reason = "trust policy is empty: no trusted roots or keys configured to anchor the signer (deny-closed)"
		return v, statement{}, true, nil
	}

	preimage := pae(env.PayloadType, payload)

	// Anchor the signer and verify the DSSE signature over the PAE pre-image.
	if cert, chain := leafCertificate(b.VerificationMaterial); cert != nil {
		if reason := anchorCertificate(&v, cert, chain, policy, preimage, sig); reason != "" {
			v.Reason = reason
			return v, statement{}, true, nil
		}
	} else {
		if reason := anchorBareKey(&v, policy, preimage, sig); reason != "" {
			v.Reason = reason
			return v, statement{}, true, nil
		}
	}

	// Signature + signer anchored. The payload must be an in-toto Statement v1.
	if err := json.Unmarshal(payload, &st); err != nil {
		return Verdict{}, statement{}, false, fmt.Errorf("%w: statement JSON: %v", ErrMalformedBundle, err)
	}
	if st.Type != statementTypeInToto {
		v.Reason = fmt.Sprintf("signed payload is not an in-toto Statement v1 (_type=%q)", st.Type)
		return v, statement{}, true, nil
	}
	return v, st, false, nil
}

// pae builds the DSSE Pre-Authentication Encoding the signature covers:
//
//	"DSSEv1" SP len(payloadType) SP payloadType SP len(payload) SP payload
func pae(payloadType string, payload []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	b.Write(payload)
	return b.Bytes()
}

// leafCertificate extracts the leaf signing certificate (and any intermediates)
// from the bundle's verification material, or nil when the signature is bare-key.
func leafCertificate(vm verificationMaterial) (leaf *x509.Certificate, chain []*x509.Certificate) {
	var raw [][]byte
	switch {
	case vm.Certificate != nil && vm.Certificate.RawBytes != "":
		if der, err := base64.StdEncoding.DecodeString(vm.Certificate.RawBytes); err == nil {
			raw = append(raw, der)
		}
	case vm.X509CertificateChain != nil:
		for _, c := range vm.X509CertificateChain.Certificates {
			if der, err := base64.StdEncoding.DecodeString(c.RawBytes); err == nil {
				raw = append(raw, der)
			}
		}
	}
	for _, der := range raw {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, nil
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 {
		return nil, nil
	}
	return chain[0], chain
}

// anchorCertificate verifies a certificate-based (keyless/PKI) signature: the DSSE
// signature must verify under the leaf's public key, the leaf must chain to a
// trusted root, and — when SAN identity material is present (Fulcio keyless) — the
// identity and OIDC issuer must match the allow-lists. Returns "" on success or a
// deny-closed reason. Populates v.Method/SignerIdentity/SignerIssuer.
func anchorCertificate(v *Verdict, leaf *x509.Certificate, chain []*x509.Certificate, policy TrustPolicy, preimage, sig []byte) string {
	if err := verifySignature(leaf.PublicKey, preimage, sig); err != nil {
		return "DSSE signature does not verify under the certificate's public key: " + err.Error()
	}
	if len(policy.Roots) == 0 {
		return "a certificate-based signature was presented but the trust policy configures no trusted roots to anchor it (deny-closed)"
	}
	roots := x509.NewCertPool()
	for _, p := range policy.Roots {
		if !roots.AppendCertsFromPEM([]byte(p)) {
			return "a configured trusted root is not valid PEM"
		}
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	// Signing certificates (Fulcio) are intentionally short-lived; verify the chain
	// as of the leaf's issuance instant (NotBefore) rather than "now", so an expired
	// short-lived cert still proves it was validly issued by the trusted root. The
	// freshness/non-revocation guarantee that the cert was valid AT SIGNING time is
	// what Rekor/timestamp would add — the documented seam (Verdict.TransparencyLog*).
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   leaf.NotBefore,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning, x509.ExtKeyUsageAny},
	})
	if err != nil {
		return "certificate does not chain to a trusted root: " + err.Error()
	}
	// Record the anchoring ROOT(s): the terminal certificate of EVERY verified chain (Go
	// guarantees each returned chain ends at a cert from the Roots pool). Pinning the exact
	// admission-time root(s) on the verdict lets AnchorStillTrusted deny a stale verdict when
	// THAT root is rotated/replaced out of the policy even though another root remains — the
	// certificate-mode counterpart of the exact-key pin bare-key already has. Recording ALL
	// verified chains' roots (a leaf may legitimately cross-sign to multiple trusted roots),
	// deduplicated and sorted, so re-check passes iff at least one recorded anchor is retained.
	seen := map[string]struct{}{}
	var rootMarkers []string
	for _, ch := range chains {
		if len(ch) == 0 {
			continue
		}
		m := rootMarker(ch[len(ch)-1])
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		rootMarkers = append(rootMarkers, m)
	}
	sort.Strings(rootMarkers)
	v.SignerRoots = rootMarkers

	sans := certSANs(leaf)
	issuer := certIssuer(leaf)
	// The trust MODE is decided by the OPERATOR'S POLICY, never inferred from the
	// (attacker-controlled) certificate contents. If the operator pinned an identity or
	// issuer, the keyless identity path is MANDATORY: a certificate that omits its SAN
	// identity or OIDC issuer cannot satisfy the pin and is rejected — there is no silent
	// downgrade to unchecked certificate-PKI. Only when NO identity/issuer pins are
	// configured does trust rest on the chain alone (an explicit PKI-only posture).
	if len(policy.AllowedIdentities) == 0 && len(policy.AllowedIssuers) == 0 {
		v.Method = MethodCertificatePKI
		v.SignerIdentity = leaf.Subject.String()
		return ""
	}
	v.Method = MethodSigstoreKeyless
	if len(sans) == 0 {
		return "certificate carries no SAN identity but the trust policy pins an allowed identity (deny-closed: no keyless→PKI downgrade)"
	}
	if issuer == "" {
		return "certificate carries no OIDC issuer extension but the trust policy pins an allowed issuer (deny-closed: no keyless→PKI downgrade)"
	}
	v.SignerIssuer = issuer
	id, ok := matchIdentity(policy.AllowedIdentities, sans)
	if !ok {
		return "signer identity (certificate SAN) does not match any allowed identity (deny-closed)"
	}
	v.SignerIdentity = id
	if !contains(policy.AllowedIssuers, issuer) {
		return fmt.Sprintf("signer OIDC issuer %q is not in the allowed-issuers list (deny-closed)", issuer)
	}
	return ""
}

// anchorBareKey verifies a bare-key signature against the operator's trusted-key
// list. Returns "" on success or a deny-closed reason. Populates v.Method/SignerIdentity.
func anchorBareKey(v *Verdict, policy TrustPolicy, preimage, sig []byte) string {
	if len(policy.Keys) == 0 {
		return "a bare-key signature was presented but the trust policy configures no trusted keys (deny-closed)"
	}
	for _, p := range policy.Keys {
		pub, err := parsePublicKey(p)
		if err != nil {
			continue
		}
		if verifySignature(pub, preimage, sig) == nil {
			v.Method = MethodBareKey
			v.SignerIdentity = "key:" + keyFingerprint(p)
			return ""
		}
	}
	return "DSSE signature does not verify under any trusted public key (deny-closed)"
}

// verifySignature verifies a detached signature over msg under an Ed25519, ECDSA or
// RSA public key — the three schemes Sigstore/OMS use. Ed25519 signs the message
// directly; ECDSA/RSA sign a hash chosen by the key (the Sigstore convention:
// SHA-256 for P-256/RSA, SHA-384 for P-384, SHA-512 for P-521). ECDSA signatures
// are ASN.1 DER.
func verifySignature(pub crypto.PublicKey, msg, sig []byte) error {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		if !ed25519.Verify(k, msg, sig) {
			return errors.New("ed25519 verification failed")
		}
		return nil
	case *ecdsa.PublicKey:
		h := hashFor(k.Curve.Params().BitSize)
		h.Write(msg)
		if !ecdsa.VerifyASN1(k, h.Sum(nil), sig) {
			return errors.New("ecdsa verification failed")
		}
		return nil
	case *rsa.PublicKey:
		sum := sha256.Sum256(msg)
		if err := rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig); err == nil {
			return nil
		}
		if err := rsa.VerifyPSS(k, crypto.SHA256, sum[:], sig, nil); err == nil {
			return nil
		}
		return errors.New("rsa verification failed (PKCS1v15 and PSS)")
	default:
		return fmt.Errorf("unsupported public key type %T", pub)
	}
}

// hashFor returns the SHA-2 hash paired with an ECDSA curve size.
func hashFor(bits int) hash.Hash {
	switch {
	case bits > 384:
		return sha512.New()
	case bits > 256:
		return sha512.New384()
	default:
		return sha256.New()
	}
}

// checkManifest re-checks caller-supplied per-file digests against the signed
// manifest: every manifest resource must be present in resolved AND match. Returns
// "" on full match or a reason describing the first mismatch/gap.
func checkManifest(resources []resourceDescriptor, resolved map[string]string) string {
	for _, res := range resources {
		got, ok := resolved[res.Name]
		if !ok {
			return fmt.Sprintf("no resolved digest supplied for manifest file %q", res.Name)
		}
		if !strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(res.Digest)) {
			return fmt.Sprintf("digest mismatch for %q (the on-disk artifact differs from the signed manifest)", res.Name)
		}
	}
	return ""
}

// certSANs collects the subject-alternative-name values an identity match runs
// against (Fulcio puts the OIDC identity in a URI SAN; email/DNS are also matched).
func certSANs(c *x509.Certificate) []string {
	var out []string
	for _, u := range c.URIs {
		out = append(out, u.String())
	}
	out = append(out, c.EmailAddresses...)
	out = append(out, c.DNSNames...)
	return out
}

// certIssuer reads the Fulcio OIDC-issuer extension (v2 DER UTF8String preferred,
// then deprecated v1 raw string). Returns "" when absent (a non-Fulcio cert).
func certIssuer(c *x509.Certificate) string {
	for _, ext := range c.Extensions {
		if ext.Id.Equal(oidFulcioIssuerV2) {
			var s string
			if _, err := asn1.Unmarshal(ext.Value, &s); err == nil {
				return s
			}
		}
	}
	for _, ext := range c.Extensions {
		if ext.Id.Equal(oidFulcioIssuerV1) {
			return string(ext.Value)
		}
	}
	return ""
}

// matchIdentity returns the first SAN value matched by any allowed-identity regexp
// (unanchored, cosign-style). With no allowed identities configured nothing matches
// (deny-closed: keyless identity must be pinned).
func matchIdentity(allowed, sans []string) (string, bool) {
	for _, pat := range allowed {
		re, err := regexp.Compile(pat)
		if err != nil {
			continue
		}
		for _, s := range sans {
			if re.MatchString(s) {
				return s, true
			}
		}
	}
	return "", false
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// parsePublicKey decodes a PEM-encoded PKIX public key (the bare-key trust list
// format) into a crypto.PublicKey.
func parsePublicKey(pemStr string) (crypto.PublicKey, error) {
	blk, _ := pem.Decode([]byte(pemStr))
	if blk == nil {
		return nil, errors.New("not PEM")
	}
	return x509.ParsePKIXPublicKey(blk.Bytes)
}

// keyFingerprint is the FULL sha256 hex of a bare key's PKIX DER — the exact identifier
// recorded as a verdict's bare-key SignerIdentity ("key:<fp>") and re-checked against the
// current trusted-key set by AnchorStillTrusted. It is a security PIN in the revocation
// path, so it is NOT truncated: a 16-hex (64-bit) fingerprint is only 2^64 second-preimage
// work, letting an attacker grind a trust-listed key to collide with a revoked key's pin and
// keep its stale verdict alive. The full digest matches the width of the root markers.
func keyFingerprint(pemStr string) string {
	blk, _ := pem.Decode([]byte(pemStr))
	if blk == nil {
		sum := sha256.Sum256([]byte(pemStr))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(blk.Bytes)
	return hex.EncodeToString(sum[:])
}

// rootMarker is the "root:<fp>" identifier recorded for a trusted ROOT certificate,
// fp = FULL sha256 hex of the cert DER. Like keyFingerprint (the bare-key pin), it is a
// security PIN re-checked at deploy/approve, so it uses the full digest — no truncation,
// no 64-bit shortcut. The "root:" prefix keeps it distinct from a bare-key "key:<fp>".
func rootMarker(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "root:" + hex.EncodeToString(sum[:])
}

// policyTrustedRootMarkers returns the set of "root:<fp>" markers for EVERY CA certificate
// the policy's Roots would load into the verification pool. It mirrors
// x509.CertPool.AppendCertsFromPEM exactly: a single Roots[] entry may be a PEM BUNDLE
// (multiple concatenated CERTIFICATE blocks), each a candidate anchor, so every block is
// decoded — not just the first — and non-CERTIFICATE, header-bearing, or unparseable blocks
// are skipped just as the pool skips them. Decoding only the first block would miss the
// actual anchoring root whenever it is not first in its bundle entry, false-denying every
// bundle-configured tenant on the very first re-check.
func policyTrustedRootMarkers(pems []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range pems {
		rest := []byte(p)
		for len(rest) > 0 {
			var blk *pem.Block
			blk, rest = pem.Decode(rest)
			if blk == nil {
				break
			}
			if blk.Type != "CERTIFICATE" || len(blk.Headers) != 0 {
				continue
			}
			cert, err := x509.ParseCertificate(blk.Bytes)
			if err != nil {
				continue
			}
			out[rootMarker(cert)] = struct{}{}
		}
	}
	return out
}

// anyRootRetained reports whether at least one recorded anchoring-root marker is still in
// the current policy's trusted-root set. An empty recorded set never matches (a certificate
// verdict that pinned no root is deny-closed by the caller).
func anyRootRetained(recorded []string, current map[string]struct{}) bool {
	for _, m := range recorded {
		if _, ok := current[m]; ok {
			return true
		}
	}
	return false
}

// RecordedAnchor is the signer anchor a stored admission verdict recorded at admit time,
// re-evaluated by AnchorStillTrusted against the CURRENT trust policy. It is a struct, not
// four positional strings, so the call sites — each reading its own store's columns — cannot
// silently transpose identity/issuer/root/method.
type RecordedAnchor struct {
	// Identity is the recorded signer identity: the matched SAN (keyless/PKI) or the
	// "key:<fp>" bare-key fingerprint (Verdict.SignerIdentity).
	Identity string
	// Issuer is the recorded OIDC issuer, keyless only (Verdict.SignerIssuer).
	Issuer string
	// Roots are the recorded "root:<fp>" markers of the CA root(s) that anchored the leaf
	// at admit (Verdict.SignerRoots). Empty for bare-key, and empty for a certificate
	// verdict admitted BEFORE root pinning (a legacy row) — the latter is deny-closed.
	Roots []string
	// Method is the recorded signing method (a Method* constant).
	Method string
}

// AnchorStillTrusted reports whether a signer previously verified under some policy —
// recorded on a stored verdict as a RecordedAnchor — would STILL be anchored by the CURRENT
// policy p. It lets a caller re-validate an old admission verdict after the trust policy
// changed (a compromised bare key rotated out, an identity dropped, a CA root removed OR
// replaced) WITHOUT re-presenting the original bundle:
//   - bare-key: the recorded key fingerprint (the "key:<fp>" form written at admit) must
//     still be among the current trusted keys p.Keys;
//   - keyless / certificate-PKI: a trusted Root must still be configured, at least one of the
//     recorded anchoring ROOTS must still be present in p.Roots (the exact-root pin, matched
//     bundle-aware against every CA the pool would load), and the recorded identity/issuer
//     must still match p.AllowedIdentities/p.AllowedIssuers when those are pinned.
//
// It is deny-closed: an unknown method, a bare key without the "key:" prefix, a certificate
// signer with no trusted root, or a certificate verdict whose recorded anchoring root(s) are
// all gone returns false. A certificate verdict that recorded NO root (a legacy row from
// before root pinning) is likewise deny-closed — never grandfathered — so re-admission under
// the current policy is required to pin it. This exact-root pin closes the certificate-mode
// residual that a bare-witness "a root is still present" check left open (dropping the
// anchoring root while another remains, or replacing a single root in one edit); bare-key
// never had that residual — it always pinned the exact key.
func AnchorStillTrusted(p TrustPolicy, a RecordedAnchor) bool {
	switch a.Method {
	case MethodBareKey:
		fp := strings.TrimPrefix(a.Identity, "key:")
		if fp == "" || fp == a.Identity { // must carry the "key:" prefix written at admit
			return false
		}
		for _, pem := range p.Keys {
			if keyFingerprint(pem) == fp {
				return true
			}
		}
		return false
	case MethodSigstoreKeyless, MethodCertificatePKI:
		if len(p.Roots) == 0 {
			return false // a certificate signature cannot anchor without a trusted root
		}
		// The EXACT admission-time root must be retained: at least one recorded anchoring
		// root must still be in the current policy. A certificate verdict that recorded no
		// root predates root pinning — deny-closed (re-admit to pin it), never grandfathered.
		if !anyRootRetained(a.Roots, policyTrustedRootMarkers(p.Roots)) {
			return false
		}
		// An identity pin, when configured, must still match. An empty allow-list is the
		// PKI-only posture (no identity pin) — trust rests on the still-present exact root.
		if len(p.AllowedIdentities) > 0 {
			if _, ok := matchIdentity(p.AllowedIdentities, []string{a.Identity}); !ok {
				return false
			}
		}
		if len(p.AllowedIssuers) > 0 && !contains(p.AllowedIssuers, a.Issuer) {
			return false
		}
		return true
	default:
		return false
	}
}
