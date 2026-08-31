// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcpb

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata/ are GENUINE PKCS#7-signed .mcpb bundles produced by
// node-forge 1.4.0 — the exact library modelcontextprotocol/mcpb src/node/sign.ts
// uses — so these tests verify true interop, not a self-consistent re-encoding:
//
//	signed-valid.mcpb     leaf "Acme Extensions Publisher" -> "Olivares MCPB Test Root CA" (== root.pem)
//	signed-untrusted.mcpb leaf "Impersonator Publisher"    -> "Rogue Self-Signed CA" (NOT in root.pem)
//	root.pem              the trusted CA for signed-valid.mcpb
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func rootPool(t *testing.T) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(fixture(t, "root.pem")) {
		t.Fatal("root.pem is not valid PEM")
	}
	return pool
}

// splitBundle locates the signature block and returns (der, beforeMarker, blockLen).
func splitBundle(t *testing.T, data []byte) (der, before []byte, blockLen int) {
	t.Helper()
	idx := bytes.LastIndex(data, []byte(sigMarkerStart))
	if idx < 0 {
		t.Fatal("no signature marker in fixture")
	}
	d, ok := extractSignatureDER(data, idx)
	if !ok {
		t.Fatal("could not extract DER")
	}
	return d, data[:idx], len(data) - idx
}

// TestVerifyRealValid: a genuine node-forge signature verifies and anchors to the
// configured trusted root, surfacing the signer Common Name.
func TestVerifyRealValid(t *testing.T) {
	der, before, blockLen := splitBundle(t, fixture(t, "signed-valid.mcpb"))
	v := verifyMCPBSignature(der, before, blockLen, rootPool(t))
	if v.state != sigValid {
		t.Fatalf("want sigValid, got state=%d reason=%q", v.state, v.reason)
	}
	if v.signer != "Acme Extensions Publisher" {
		t.Errorf("want signer Common Name, got %q", v.signer)
	}
}

// TestVerifyUntrustedRoot: a genuine signature whose leaf does NOT chain to the
// configured root is INVALID (deny-closed), distinct from absent.
func TestVerifyUntrustedRoot(t *testing.T) {
	der, before, blockLen := splitBundle(t, fixture(t, "signed-untrusted.mcpb"))
	v := verifyMCPBSignature(der, before, blockLen, rootPool(t))
	if v.state != sigInvalid {
		t.Fatalf("want sigInvalid, got state=%d", v.state)
	}
	if !strings.Contains(v.reason, "chain") {
		t.Errorf("want a chain-anchoring reason, got %q", v.reason)
	}
}

// TestVerifyNoRoots: a valid signature with NO trusted_roots configured is
// PRESENT-BUT-UNVERIFIED — never silently valid (the deny-closed default).
func TestVerifyNoRoots(t *testing.T) {
	der, before, blockLen := splitBundle(t, fixture(t, "signed-valid.mcpb"))
	v := verifyMCPBSignature(der, before, blockLen, nil)
	if v.state != sigUnverified {
		t.Fatalf("want sigUnverified, got state=%d", v.state)
	}
	if !strings.Contains(v.reason, "trusted_roots") {
		t.Errorf("reason should name the missing trusted_roots, got %q", v.reason)
	}
}

// TestVerifyTamperedContent: the signature still verifies over the authenticated
// attributes, but the messageDigest no longer binds the (modified) content.
func TestVerifyTamperedContent(t *testing.T) {
	der, before, blockLen := splitBundle(t, fixture(t, "signed-valid.mcpb"))
	tampered := append([]byte(nil), before...)
	tampered[0] ^= 0xff // corrupt the zip local-header signature byte
	v := verifyMCPBSignature(der, tampered, blockLen, rootPool(t))
	if v.state != sigInvalid {
		t.Fatalf("want sigInvalid for tampered content, got state=%d reason=%q", v.state, v.reason)
	}
	if !strings.Contains(v.reason, "messageDigest") {
		t.Errorf("want a content-binding reason, got %q", v.reason)
	}
}

// TestVerifyBadSignature: flipping a byte inside the RSA signature value breaks
// the cryptographic check (structure still parses).
func TestVerifyBadSignature(t *testing.T) {
	der, before, blockLen := splitBundle(t, fixture(t, "signed-valid.mcpb"))
	bad := append([]byte(nil), der...)
	bad[len(bad)-100] ^= 0xff // deep inside the trailing 256-byte signature OCTET STRING
	v := verifyMCPBSignature(bad, before, blockLen, rootPool(t))
	if v.state != sigInvalid {
		t.Fatalf("want sigInvalid for a broken signature, got state=%d", v.state)
	}
	if !strings.Contains(v.reason, "signature does not verify") {
		t.Errorf("want a signature-verify reason, got %q", v.reason)
	}
}

// TestVerifyCorruptDER: a truncated DER blob cannot be parsed as SignedData.
func TestVerifyCorruptDER(t *testing.T) {
	der, before, blockLen := splitBundle(t, fixture(t, "signed-valid.mcpb"))
	corrupt := der[:len(der)/2]
	v := verifyMCPBSignature(corrupt, before, blockLen, rootPool(t))
	if v.state != sigInvalid {
		t.Fatalf("want sigInvalid for corrupt DER, got state=%d", v.state)
	}
	if !strings.Contains(v.reason, "malformed PKCS#7") {
		t.Errorf("want a malformed-structure reason, got %q", v.reason)
	}
}

// TestExtractSignatureDERFraming: the length prefix bounds the DER; an overrunning
// or zero length is rejected as malformed framing.
func TestExtractSignatureDERFraming(t *testing.T) {
	valid := fixture(t, "signed-valid.mcpb")
	idx := bytes.LastIndex(valid, []byte(sigMarkerStart))
	if _, ok := extractSignatureDER(valid, idx); !ok {
		t.Fatal("genuine framing should extract")
	}
	// A length prefix that overruns the file is rejected.
	overrun := append([]byte(nil), valid...)
	off := idx + len(sigMarkerStart)
	overrun[off] = 0xff
	overrun[off+1] = 0xff
	overrun[off+2] = 0xff
	overrun[off+3] = 0x7f
	if _, ok := extractSignatureDER(overrun, idx); ok {
		t.Error("an overrunning length prefix must be rejected")
	}
}

// TestClassifyBundleReadsBumpedManifest: a canonical mcpb signer bumps the EOCD
// comment_length, so the manifest must be read from the FULL file. classifyBundle
// must still recover the manifest name AND verify the signature.
func TestClassifyBundleReadsBumpedManifest(t *testing.T) {
	state, signer, _, m, parseErr := classifyBundle(fixture(t, "signed-valid.mcpb"), rootPool(t))
	if parseErr {
		t.Fatal("manifest must parse from the EOCD-bumped real bundle")
	}
	if m.name() != "signed-valid" {
		t.Errorf("want manifest name signed-valid, got %q", m.name())
	}
	if state != sigValid || signer != "Acme Extensions Publisher" {
		t.Errorf("want sigValid + signer, got state=%d signer=%q", state, signer)
	}
}
