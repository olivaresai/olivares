// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sigv4 is the AWS Signature Version 4 request signer shared by the
// core's AWS REST clients: the ledger checkpoint signer (core/audit/kmssign) and
// the key-custody KEK wrapper (core/secure/kmswrap). It is a clean-room
// reimplementation of the standard algorithm; the connectors side has its own
// copy under Apache, but /core cannot import a connectors-internal package, so
// the integrity-critical clients carry their own. It lives in core/internal so
// it can never become public API surface.
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Credentials is the minimal credential triple a SigV4 signer needs.
// SessionToken is the optional STS session token (assumed-role / IRSA). These
// live only in memory and are NEVER logged or emitted (docs/SECURITY-HARDENING.md).
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// sigv4 facts.
const (
	algorithm     = "AWS4-HMAC-SHA256"
	amzDateFormat = "20060102T150405Z"
	shortDateFmt  = "20060102"
)

// Sign signs req in place with AWS Signature Version 4 for the given service
// and region. It mutates only the Authorization, X-Amz-Date and
// X-Amz-Security-Token headers — never the URL or body — so it cannot turn a
// read into a write.
func Sign(req *http.Request, body []byte, service, region string, creds Credentials, t time.Time) {
	t = t.UTC()
	amzDate := t.Format(amzDateFormat)
	shortDate := t.Format(shortDateFmt)
	req.Header.Set("X-Amz-Date", amzDate)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	payloadHash := hexSHA256(body)

	signed, canonHeaders := canonicalHeaders(req, creds.SessionToken != "")
	canonReq := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.EscapedPath()),
		req.URL.RawQuery, // KMS requests carry no query string
		canonHeaders,
		signed,
		payloadHash,
	}, "\n")
	scope := strings.Join([]string{shortDate, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hexSHA256Str(canonReq),
	}, "\n")
	key := signingKey(creds.SecretAccessKey, shortDate, region, service)
	sig := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))
	req.Header.Set("Authorization", algorithm+
		" Credential="+creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+
		", Signature="+sig)
}

func canonicalHeaders(req *http.Request, withToken bool) (signed, canonical string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	pairs := map[string]string{
		"content-type": req.Header.Get("Content-Type"),
		"host":         host,
		"x-amz-date":   req.Header.Get("X-Amz-Date"),
		"x-amz-target": req.Header.Get("X-Amz-Target"),
	}
	if withToken {
		pairs["x-amz-security-token"] = req.Header.Get("X-Amz-Security-Token")
	}
	names := make([]string, 0, len(pairs))
	for n := range pairs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(strings.Join(strings.Fields(pairs[n]), " "))
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

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hexSHA256Str(s string) string { return hexSHA256([]byte(s)) }
