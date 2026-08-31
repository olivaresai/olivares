// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3archive

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
)

// This file is the SigV4 signer for the object-lock writes. It exists
// alongside connectors/internal/awssig because awssig.Sign pins the
// signed-header set to host;x-amz-date[;x-amz-security-token], while the S3
// SigV4 contract requires EVERY x-amz-* header present on a request to be in
// CanonicalHeaders/SignedHeaders ("Any x-amz-* headers that you plan to
// include in your request must also be added" — Authenticating Requests:
// Using the Authorization Header, AWS SigV4). For this connector that rule is
// not a formality: the x-amz-object-lock-* headers ARE the WORM protection,
// and leaving them out of the signature would leave the immutability request
// unauthenticated (and rejected by S3). signV4 therefore signs every header
// set on the request, plus host. The algorithm is awssig's, byte for byte
// (same canonical request, string-to-sign and key derivation, pinned by the
// same AWS known-answer vector in the tests); the shared primitives
// (HexSHA256, URIEncode) are awssig's exports. Folding this back into awssig
// as an additive "extra signed headers" variant is the obvious follow-up.

const (
	algorithm       = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	shortDateFormat = "20060102"
)

// signV4 signs req in place with AWS Signature Version 4, including every
// header already set on req (plus host) in the signed set. The payload hash is
// taken from the X-Amz-Content-Sha256 header when present (the S3 convention),
// else it is the empty-body hash. It mutates only the Authorization, X-Amz-Date
// and X-Amz-Security-Token headers; the credentials live only in memory and
// never appear in an error or a log.
func signV4(req *http.Request, service, region string, creds awssig.Creds, t time.Time) {
	t = t.UTC()
	amzDate := t.Format(amzDateFormat)
	shortDate := t.Format(shortDateFormat)

	req.Header.Set("X-Amz-Date", amzDate)
	if creds.Token != "" {
		req.Header.Set("X-Amz-Security-Token", creds.Token)
	}

	payloadHash := req.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = awssig.HexSHA256(nil)
	}
	signedHeaders, canonicalHeaders := canonicalHeadersAll(req)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.EscapedPath()),
		canonicalQuery(req.URL.RawQuery),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{shortDate, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		awssig.HexSHA256([]byte(canonicalRequest)),
	}, "\n")

	key := signingKey(creds.Secret, shortDate, region, service)
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", algorithm+
		" Credential="+creds.AKID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+signature)
}

// canonicalHeadersAll builds the SigV4 canonical/signed header lists from
// EVERY header on req plus host (Authorization itself excluded). Values are
// whitespace-collapsed; multiple values of one header join with a comma, per
// the canonicalization rules.
func canonicalHeadersAll(req *http.Request) (signed, canonical string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	pairs := map[string]string{"host": host}
	for name, vals := range req.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" {
			continue
		}
		collapsed := make([]string, 0, len(vals))
		for _, v := range vals {
			collapsed = append(collapsed, strings.Join(strings.Fields(v), " "))
		}
		pairs[lower] = strings.Join(collapsed, ",")
	}
	names := make([]string, 0, len(pairs))
	for name := range pairs {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(pairs[name])
		b.WriteByte('\n')
	}
	return strings.Join(names, ";"), b.String()
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

// canonicalQuery canonicalizes the wire-form (already percent-encoded) query:
// each key/value is decoded and re-encoded per the SigV4 rules, then the pairs
// are sorted. Because this connector builds its query values with
// awssig.URIEncode, the wire form and the canonical form agree byte for byte.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	type kv struct{ k, v string }
	pairs := make([]kv, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		kd, kerr := url.QueryUnescape(k)
		vd, verr := url.QueryUnescape(v)
		if kerr != nil || verr != nil {
			// Not decodable: canonicalize the raw bytes as-is (defensive; this
			// connector never produces such a query).
			kd, vd = k, v
		}
		pairs = append(pairs, kv{awssig.URIEncode(kd, true), awssig.URIEncode(vd, true)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

func signingKey(secret, shortDate, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(shortDate))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
