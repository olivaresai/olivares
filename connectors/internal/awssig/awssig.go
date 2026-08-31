// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package awssig is the AWS Signature Version 4 request signer shared by the
// Olivares AI connectors that read or publish to AWS APIs with the standard library
// only (the `aws` audit connector and the `cloudqueue` messaging connector —). It is stdlib-only and signs in place: it mutates only the Authorization,
// X-Amz-Date and X-Amz-Security-Token headers, never the URL or the body, so it
// cannot turn a read into a write. The credentials live only in memory and are never
// logged or emitted (docs/SECURITY-HARDENING.md). It imports no engine package.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Creds is the minimal credential triple a SigV4 signer needs. Token is the optional
// STS session token (present for temporary/assumed-role credentials). These values
// live only in memory and are NEVER logged or emitted.
type Creds struct {
	AKID   string
	Secret string
	Token  string
}

const (
	algorithm       = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	shortDateFormat = "20060102"
)

// Sign signs req in place with AWS Signature Version 4. It computes the canonical
// request from the method, path, sorted query, the signed header set
// (host;x-amz-date and, when a session token is present, x-amz-security-token),
// and the hex SHA-256 of body; derives the string-to-sign and the date→region→
// service signing key; and sets the Authorization, X-Amz-Date and (when present)
// X-Amz-Security-Token headers.
func Sign(req *http.Request, body []byte, service, region string, creds Creds, t time.Time) {
	t = t.UTC()
	amzDate := t.Format(amzDateFormat)
	shortDate := t.Format(shortDateFormat)

	req.Header.Set("X-Amz-Date", amzDate)
	if creds.Token != "" {
		req.Header.Set("X-Amz-Security-Token", creds.Token)
	}

	payloadHash := HexSHA256(body)
	signedHeaders, canonicalHeaders := canonicalHeaders(req, creds.Token != "")

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
		HexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := signingKey(creds.Secret, shortDate, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	req.Header.Set("Authorization", algorithm+
		" Credential="+creds.AKID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+signature)
}

func canonicalHeaders(req *http.Request, withToken bool) (signed, canonical string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	pairs := map[string]string{
		"host":       host,
		"x-amz-date": req.Header.Get("X-Amz-Date"),
	}
	if withToken {
		pairs["x-amz-security-token"] = req.Header.Get("X-Amz-Security-Token")
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
		b.WriteString(strings.Join(strings.Fields(pairs[name]), " "))
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

// canonicalQuery canonicalizes the WIRE-FORM (already percent-encoded) query:
// each key/value is DECODED first and then re-encoded per the SigV4 rules, so a
// caller building its query with url.Values.Encode() signs the same canonical
// form the server recomputes from the wire bytes. Re-encoding without decoding
// (the original behavior, fixed in) double-encoded every '%' (%3D signed
// as %253D) and signed '+' (a url.Values space) as %2B instead of %20 — an
// opaque server-issued pagination token containing base64 '+/=' (AgentCore
// policy nextToken, IAM Marker) then failed with SignatureDoesNotMatch on page
// 2+. url.QueryUnescape maps '+' to space, matching url.Values.Encode() wire
// semantics; query components encode '/' (encodeSlash=true) per the SigV4 spec.
// An undecodable component falls back to its raw bytes (defensive — no caller
// produces one). Mirrors the proven connectors/s3archive/sign.go behavior.
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
			kd, vd = k, v
		}
		pairs = append(pairs, kv{URIEncode(kd, true), URIEncode(vd, true)})
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

// URIEncode percent-encodes s per the AWS SigV4 rules: unreserved characters
// (A-Z a-z 0-9 - _ . ~) pass through, everything else is %XX with uppercase hex.
// When encodeSlash is false a '/' is left as-is (path segments); for query
// components it is true.
func URIEncode(s string, encodeSlash bool) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
	return b.String()
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

// HexSHA256 returns the lowercase hex SHA-256 of data.
func HexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
