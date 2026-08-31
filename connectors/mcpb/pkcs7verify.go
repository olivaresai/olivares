// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcpb

// PKCS#7 / CMS SignedData verification of a `.mcpb` signature block, implemented
// with the Go standard library ONLY (encoding/asn1 + crypto/x509 + crypto/rsa +
// crypto/ecdsa). The connector is Apache-2.0 and must never import the AGPL
// /core tree (scripts/check-boundary.sh), and Go ships no crypto/pkcs7, so the
// CMS SignedData structure is parsed here directly from DER.
//
// THE INTEROP CONTRACT (verified empirically against node-forge 1.4.0 — the exact
// library modelcontextprotocol/mcpb src/node/sign.ts uses — on 2026-06-21):
//
//   - The block is `[zip] "MCPB_SIG_V1" [uint32-LE len] [DER PKCS#7] "MCPB_SIG_END"`.
//     The DER is bounded by the LE length prefix, NOT by the footer position
//     (a signer MAY pad before the footer; mcpb's own extractor reads by length).
//   - The PKCS#7 is a DETACHED CMS SignedData (RFC 5652): no eContent; the signer
//     carries authenticated (signed) attributes — contentType=data, messageDigest,
//     signingTime — and the signature (encryptedDigest) is over the DER re-encoding
//     of those attributes as a SET (tag 0x31), per RFC 5652 §5.4.
//   - messageDigest = SHA-256 of the ORIGINAL zip. mcpb signs the zip and THEN
//     bumps the ZIP EOCD comment_length by the whole signature-block length (so a
//     strict zip parser accepts the signed file). So the bytes physically before
//     the marker differ from what was signed by exactly the 2 comment_length bytes.
//     We therefore bind the content by matching messageDigest against EITHER the
//     bytes-before-marker as-is (a stage-then-sign pipeline, e.g. microsoft/mcp)
//     OR those bytes with the comment_length un-bumped (the canonical mcpb signer).
//
// DENY-CLOSED: cryptographic verification runs only when the operator has
// configured trusted roots (cfgTrustedRoots). With no roots a present signature is
// reported as PRESENT-BUT-UNVERIFIED (never silently "valid") — a self-signed cert
// with any CN would otherwise forge publisher identity. This mirrors the catalog /
// modelsign trust posture: trust must be explicitly anchored, never assumed.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	_ "crypto/sha256" // register SHA-256 for crypto.Hash.New
	_ "crypto/sha512" // register SHA-384/512 for crypto.Hash.New
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// CMS / PKCS#9 object identifiers (RFC 5652 / RFC 2315).
var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}

	// Digest algorithm OIDs we accept (the SHA-2 family code-signing uses).
	oidSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}
)

// --- CMS SignedData wire types (the subset a detached mcpb signature uses) -----

type asnContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type asnSignedData struct {
	Version          int
	DigestAlgorithms asn1.RawValue
	EncapContentInfo asn1.RawValue
	Certificates     asn1.RawValue   `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue   `asn1:"optional,tag:1"`
	SignerInfos      []asnSignerInfo `asn1:"set"`
}

type asnIssuerAndSerial struct {
	Issuer       asn1.RawValue
	SerialNumber *big.Int
}

type asnSignerInfo struct {
	Version            int
	IssuerAndSerial    asnIssuerAndSerial
	DigestAlgorithm    pkix.AlgorithmIdentifier
	AuthenticatedAttrs asn1.RawValue `asn1:"optional,tag:0"`
	DigestEncryption   pkix.AlgorithmIdentifier
	EncryptedDigest    []byte
	UnauthAttrs        asn1.RawValue `asn1:"optional,tag:1"`
}

type asnAttribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

// sigVerdict is the deny-closed outcome of verifying one .mcpb signature block.
type sigVerdict struct {
	state  signatureState // sigValid | sigInvalid | sigUnverified
	signer string         // leaf certificate Common Name (raw; sanitized at finding time)
	reason string         // why invalid/unverified — no secret material, safe to hash into a finding
}

// verifyMCPBSignature verifies the DER PKCS#7 block of a signed .mcpb against the
// operator's trusted roots. beforeMarker is every byte before the "MCPB_SIG_V1"
// marker; blockLen is the length of the whole signature block (marker..EOF) — both
// are needed to reconstruct exactly which bytes the messageDigest covers.
//
// roots == nil (no trusted_roots configured) ⇒ sigUnverified (deny-closed: a
// present signature is NEVER reported valid without an anchor). A parse/verify
// failure ⇒ sigInvalid with a reason. Full success ⇒ sigValid + signer CN.
func verifyMCPBSignature(der, beforeMarker []byte, blockLen int, roots *x509.CertPool) sigVerdict {
	if roots == nil {
		return sigVerdict{state: sigUnverified, reason: "no trusted_roots configured to anchor the signer"}
	}

	leaf, chain, signer, attrSetDER, msgDigest, digestHash, sig, err := parseDetachedSignedData(der)
	if err != nil {
		return sigVerdict{state: sigInvalid, reason: "malformed PKCS#7 SignedData: " + err.Error()}
	}

	// 1) The signature must verify over the DER of the authenticated attributes.
	if err := verifyRawSignature(leaf.PublicKey, digestHash, attrSetDER, sig); err != nil {
		return sigVerdict{state: sigInvalid, signer: signer, reason: "signature does not verify under the signer certificate: " + err.Error()}
	}

	// 2) The messageDigest signed attribute must bind the actual content. Accept
	// either content reconstruction (canonical mcpb un-bumps the EOCD comment;
	// a stage-then-sign pipeline signs the bytes as stored).
	if !contentDigestMatches(digestHash, msgDigest, beforeMarker, blockLen) {
		return sigVerdict{state: sigInvalid, signer: signer, reason: "messageDigest attribute does not match the bundle content (the bundle was modified after signing)"}
	}

	// 3) The leaf must chain to a configured trusted root (deny-closed identity).
	inter := x509.NewCertPool()
	for _, c := range chain {
		if c.Equal(leaf) {
			continue
		}
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   time.Now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning, x509.ExtKeyUsageAny},
	}); err != nil {
		return sigVerdict{state: sigInvalid, signer: signer, reason: "signer certificate does not chain to a configured trusted root: " + err.Error()}
	}

	return sigVerdict{state: sigValid, signer: signer}
}

// parseDetachedSignedData parses a detached CMS SignedData and returns the signing
// leaf, the full certificate set, the signer Common Name, the DER of the
// authenticated attributes re-encoded as a SET (the signature pre-image), the
// signed messageDigest attribute, the digest hash function, and the signature.
func parseDetachedSignedData(der []byte) (leaf *x509.Certificate, chain []*x509.Certificate, signer string, attrSetDER, msgDigest []byte, digestHash crypto.Hash, sig []byte, err error) {
	var ci asnContentInfo
	if _, e := asn1.Unmarshal(der, &ci); e != nil {
		return nil, nil, "", nil, nil, 0, nil, fmt.Errorf("ContentInfo: %w", e)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return nil, nil, "", nil, nil, 0, nil, fmt.Errorf("ContentInfo type %v is not signedData", ci.ContentType)
	}
	var sd asnSignedData
	if _, e := asn1.Unmarshal(ci.Content.Bytes, &sd); e != nil {
		return nil, nil, "", nil, nil, 0, nil, fmt.Errorf("SignedData: %w", e)
	}
	if len(sd.SignerInfos) == 0 {
		return nil, nil, "", nil, nil, 0, nil, errors.New("no SignerInfos")
	}
	if len(sd.Certificates.Bytes) == 0 {
		return nil, nil, "", nil, nil, 0, nil, errors.New("no embedded certificates")
	}
	chain, e := x509.ParseCertificates(sd.Certificates.Bytes)
	if e != nil || len(chain) == 0 {
		return nil, nil, "", nil, nil, 0, nil, fmt.Errorf("parse certificates: %w", e)
	}

	si := sd.SignerInfos[0]
	leaf = signerCertificate(chain, si.IssuerAndSerial)
	if leaf == nil {
		return nil, nil, "", nil, nil, 0, nil, errors.New("signer certificate (issuer+serial) not present in the bundle")
	}
	signer = strings.TrimSpace(leaf.Subject.CommonName)

	digestHash, e = hashForOID(si.DigestAlgorithm.Algorithm)
	if e != nil {
		return nil, nil, "", nil, nil, 0, nil, e
	}

	// mcpb always signs authenticated attributes (RFC 5652 §5.4): the signature is
	// over the DER of the attributes as a SET. The field is [0] IMPLICIT in the
	// SignerInfo; re-tag the leading byte to UNIVERSAL SET (0x31) for both parsing
	// and the signature pre-image.
	if len(si.AuthenticatedAttrs.FullBytes) == 0 {
		return nil, nil, "", nil, nil, 0, nil, errors.New("no authenticated attributes (an mcpb signature must carry them)")
	}
	attrSetDER = make([]byte, len(si.AuthenticatedAttrs.FullBytes))
	copy(attrSetDER, si.AuthenticatedAttrs.FullBytes)
	attrSetDER[0] = 0x31

	var attrs []asnAttribute
	if _, e := asn1.UnmarshalWithParams(attrSetDER, &attrs, "set"); e != nil {
		return nil, nil, "", nil, nil, 0, nil, fmt.Errorf("authenticated attributes: %w", e)
	}
	var contentTypeOK bool
	for _, a := range attrs {
		switch {
		case a.Type.Equal(oidMessageDigest):
			var md []byte
			if _, e := asn1.Unmarshal(a.Values.Bytes, &md); e != nil {
				return nil, nil, "", nil, nil, 0, nil, fmt.Errorf("messageDigest attribute: %w", e)
			}
			msgDigest = md
		case a.Type.Equal(oidContentType):
			var ct asn1.ObjectIdentifier
			if _, e := asn1.Unmarshal(a.Values.Bytes, &ct); e == nil && ct.Equal(oidData) {
				contentTypeOK = true
			}
		}
	}
	if len(msgDigest) == 0 {
		return nil, nil, "", nil, nil, 0, nil, errors.New("no messageDigest authenticated attribute")
	}
	if !contentTypeOK {
		return nil, nil, "", nil, nil, 0, nil, errors.New("contentType authenticated attribute is missing or not id-data")
	}
	return leaf, chain, signer, attrSetDER, msgDigest, digestHash, si.EncryptedDigest, nil
}

// signerCertificate finds the cert matching a SignerInfo's issuerAndSerialNumber.
func signerCertificate(chain []*x509.Certificate, ias asnIssuerAndSerial) *x509.Certificate {
	for _, c := range chain {
		if c.SerialNumber.Cmp(ias.SerialNumber) == 0 && bytesEqual(c.RawIssuer, ias.Issuer.FullBytes) {
			return c
		}
	}
	return nil
}

// contentDigestMatches reports whether the signed messageDigest binds the bundle
// content under either reconstruction of "what was signed".
func contentDigestMatches(h crypto.Hash, msgDigest, beforeMarker []byte, blockLen int) bool {
	if digestEqual(h, beforeMarker, msgDigest) {
		return true // stage-then-sign: the stored bytes are exactly what was signed
	}
	// canonical mcpb: it signed the zip, then bumped the EOCD comment_length by the
	// whole block length. Un-bump to recover the signed bytes.
	recon := unbumpEOCDComment(beforeMarker, blockLen)
	return recon != nil && digestEqual(h, recon, msgDigest)
}

// unbumpEOCDComment returns a copy of buf with the ZIP End-Of-Central-Directory
// comment_length field decreased by blockLen (uint16 arithmetic, mirroring the
// signer's `writeUInt16LE(cur + blockLen)`), or nil when no EOCD is found.
func unbumpEOCDComment(buf []byte, blockLen int) []byte {
	const eocdSig = 0x06054b50
	for i := len(buf) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) == eocdSig {
			out := make([]byte, len(buf))
			copy(out, buf)
			cur := binary.LittleEndian.Uint16(out[i+20:])
			binary.LittleEndian.PutUint16(out[i+20:], cur-uint16(blockLen))
			return out
		}
	}
	return nil
}

// digestEqual hashes data with h and compares to want.
func digestEqual(h crypto.Hash, data, want []byte) bool {
	hh := h.New()
	hh.Write(data)
	return bytesEqual(hh.Sum(nil), want)
}

// verifyRawSignature verifies a CMS signature (encryptedDigest) over preimage
// under the signer's public key, hashing with h. RSA uses PKCS#1 v1.5 (the mcpb /
// node-forge default) with a PSS fallback; ECDSA uses ASN.1 DER signatures.
func verifyRawSignature(pub crypto.PublicKey, h crypto.Hash, preimage, sig []byte) error {
	hh := h.New()
	hh.Write(preimage)
	sum := hh.Sum(nil)
	switch k := pub.(type) {
	case *rsa.PublicKey:
		if rsa.VerifyPKCS1v15(k, h, sum, sig) == nil {
			return nil
		}
		if rsa.VerifyPSS(k, h, sum, sig, nil) == nil {
			return nil
		}
		return errors.New("RSA verification failed (PKCS1v15 and PSS)")
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(k, sum, sig) {
			return errors.New("ECDSA verification failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported signer public key type %T", pub)
	}
}

// hashForOID maps a digest-algorithm OID to its crypto.Hash.
func hashForOID(oid asn1.ObjectIdentifier) (crypto.Hash, error) {
	switch {
	case oid.Equal(oidSHA256):
		return crypto.SHA256, nil
	case oid.Equal(oidSHA384):
		return crypto.SHA384, nil
	case oid.Equal(oidSHA512):
		return crypto.SHA512, nil
	default:
		return 0, fmt.Errorf("unsupported digest algorithm %v (only SHA-256/384/512)", oid)
	}
}

// bytesEqual is a length-checked byte compare (avoids importing bytes for one use).
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
