// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package awssig

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSignKnownAnswer checks the canonical AWS SigV4 test-suite vanilla GET: a
// fixed request, credential and time produce the documented signature. This pins the
// algorithm (canonical request, string-to-sign, signing key) to AWS's own vectors.
func TestSignKnownAnswer(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	req.Host = "example.amazonaws.com"
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	Sign(req, nil, "service", "us-east-1",
		Creds{AKID: "AKIDEXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, when)

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request") {
		t.Fatalf("authorization scope wrong: %s", auth)
	}
	const wantSig = "Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if !strings.Contains(auth, wantSig) {
		t.Fatalf("signature mismatch:\n got %s\n want ...%s", auth, wantSig)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Fatalf("x-amz-date wrong: %s", req.Header.Get("X-Amz-Date"))
	}
}

func TestSignWithSessionTokenSignsTokenHeader(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	req.Host = "example.amazonaws.com"
	Sign(req, nil, "service", "us-east-1",
		Creds{AKID: "AKIDEXAMPLE", Secret: "secret", Token: "FQoG-session"}, time.Unix(0, 0))
	if req.Header.Get("X-Amz-Security-Token") != "FQoG-session" {
		t.Fatal("session token header not set")
	}
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-security-token") {
		t.Fatal("session token must be a signed header")
	}
}

func TestURIEncode(t *testing.T) {
	if got := URIEncode("a/b c", false); got != "a/b%20c" {
		t.Fatalf("path encode: %q", got)
	}
	if got := URIEncode("a/b c", true); got != "a%2Fb%20c" {
		t.Fatalf("query encode: %q", got)
	}
}

// TestCanonicalQueryDecodesWireForm: the canonical query must be the
// SINGLE encoding of each decoded component — a wire query built with
// url.Values.Encode() (the form every caller uses) signs identically to what
// the server recomputes. Before the fix, '%' double-encoded (%3D → %253D) and
// '+' (a url.Values space) signed as %2B, so any opaque pagination token with
// base64 '+/=' (AgentCore policy nextToken, IAM Marker) hit
// SignatureDoesNotMatch on page 2+.
func TestCanonicalQueryDecodesWireForm(t *testing.T) {
	cases := []struct{ raw, want string }{
		// base64-ish opaque token: already single-encoded on the wire; the
		// canonical form must be byte-identical, not double-encoded.
		{"nextToken=abc%2Bd%2Ff%3D", "nextToken=abc%2Bd%2Ff%3D"},
		// url.Values.Encode() writes a space as '+'; SigV4 canonicalizes it %20.
		{"a=b+c", "a=b%20c"},
		// unreserved-only stays untouched (the pre-fix callers' shape).
		{"Action=ListUsers&Version=2010-05-08", "Action=ListUsers&Version=2010-05-08"},
		// sorting still applies after decoding.
		{"b=2&a=1", "a=1&b=2"},
		// a '/' in a query VALUE is encoded (encodeSlash=true for queries).
		{"path=a%2Fb", "path=a%2Fb"},
	}
	for _, tc := range cases {
		if got := canonicalQuery(tc.raw); got != tc.want {
			t.Errorf("canonicalQuery(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
