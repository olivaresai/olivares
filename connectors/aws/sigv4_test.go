// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSignGetVanilla asserts the signer against the canonical AWS SigV4 test
// suite "get-vanilla" vector: a bare GET to example.amazonaws.com with the
// documented example credentials, region "us-east-1" and service "service" at
// 20150830T123600Z. The expected Authorization signature is the value AWS
// publishes for this case; matching it byte-for-byte proves the canonical
// request, string-to-sign and signing-key chain are all correct.
func TestSignGetVanilla(t *testing.T) {
	const wantSig = "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"

	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// The vector pins the Host header exactly.
	req.Host = "example.amazonaws.com"

	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	creds := awsCreds{
		akid:   "AKIDEXAMPLE",
		secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}

	sign(req, nil, "service", "us-east-1", creds, when)

	auth := req.Header.Get("Authorization")
	if auth == "" {
		t.Fatal("Authorization header not set")
	}
	// X-Amz-Date must be the ISO8601 basic form of the vector instant.
	if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
		t.Fatalf("X-Amz-Date = %q, want 20150830T123600Z", got)
	}
	// No session token in this vector ⇒ no security-token header, and it must not
	// appear in SignedHeaders.
	if got := req.Header.Get("X-Amz-Security-Token"); got != "" {
		t.Fatalf("unexpected X-Amz-Security-Token %q", got)
	}
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-date,") {
		t.Fatalf("SignedHeaders mismatch in %q", auth)
	}
	if !strings.Contains(auth, "Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request,") {
		t.Fatalf("Credential scope mismatch in %q", auth)
	}
	const sigPrefix = "Signature="
	idx := strings.Index(auth, sigPrefix)
	if idx < 0 {
		t.Fatalf("no Signature in %q", auth)
	}
	gotSig := auth[idx+len(sigPrefix):]
	if gotSig != wantSig {
		t.Fatalf("signature = %q, want %q", gotSig, wantSig)
	}
}

// TestSignGetVanillaQuery asserts the signer against the AWS SigV4 test-suite
// "get-vanilla-query" vector: a GET with a query string (?Param1=value1). It
// exercises the canonical-query path (per-parameter encoding + sort) that the IAM
// Query protocol relies on. The published signature for this vector is asserted
// exactly.
func TestSignGetVanillaQuery(t *testing.T) {
	const wantSig = "a67d582fa61cc504c4bae71f336f98b97f1ea3c7a6bfe1b6e45aec72011b9aeb"

	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/?Param1=value1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "example.amazonaws.com"

	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	creds := awsCreds{akid: "AKIDEXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	sign(req, nil, "service", "us-east-1", creds, when)

	auth := req.Header.Get("Authorization")
	idx := strings.Index(auth, "Signature=")
	if idx < 0 {
		t.Fatalf("no Signature in %q", auth)
	}
	if gotSig := auth[idx+len("Signature="):]; gotSig != wantSig {
		t.Fatalf("query-vector signature = %q, want %q", gotSig, wantSig)
	}
}

// TestSignWithSessionToken proves that a session token is signed: it is added to
// the request, included in SignedHeaders, and (being part of the canonical
// request) changes the resulting signature relative to the token-free case.
func TestSignWithSessionToken(t *testing.T) {
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

	mk := func(token string) string {
		req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Host = "example.amazonaws.com"
		sign(req, nil, "service", "us-east-1",
			awsCreds{akid: "AKIDEXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", token: token}, when)
		return req.Header.Get("Authorization")
	}

	withTok := mk("FQoGZXIvYXdzEExampleToken==")
	if !strings.Contains(withTok, "SignedHeaders=host;x-amz-date;x-amz-security-token,") {
		t.Fatalf("token not in SignedHeaders: %q", withTok)
	}
	// The signed token must materially change the signature vs. the no-token case.
	noTok := mk("")
	if withTok == noTok {
		t.Fatal("session token did not affect the signature")
	}
}
