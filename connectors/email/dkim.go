// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package email

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

// dkimSigner signs a message with DKIM (RFC 6376) using RSA-SHA256 and relaxed/relaxed
// canonicalization — the resilient choice for a high-volume sender, since relaxed
// header/body canonicalization survives the whitespace fix-ups an intermediate MTA may
// apply, which simple canonicalization would not. A valid DKIM signature whose d=
// domain aligns with the From: domain is what satisfies the DMARC alignment that
// Gmail/Yahoo/Microsoft now require of bulk senders (a misaligned or absent signature
// is what triggers Microsoft's 550 5.7.515 rejection).
type dkimSigner struct {
	domain   string // d=
	selector string // s=
	key      *rsa.PrivateKey
}

// newDKIMSigner parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8) and builds a
// signer for the given domain/selector. The key is held in memory only and never
// logged. An empty key disables signing (newDKIMSigner returns nil, nil).
func newDKIMSigner(domain, selector, pemKey string) (*dkimSigner, error) {
	pemKey = strings.TrimSpace(pemKey)
	if pemKey == "" {
		return nil, nil
	}
	if domain == "" || selector == "" {
		return nil, fmt.Errorf("dkim: signing requires both a domain (d=) and a selector (s=)")
	}
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, fmt.Errorf("dkim: private key is not valid PEM")
	}
	key, err := parseRSAPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("dkim: parse private key: %w", err)
	}
	return &dkimSigner{domain: domain, selector: selector, key: key}, nil
}

// parseRSAPrivateKey accepts a PKCS#1 ("RSA PRIVATE KEY") or PKCS#8 ("PRIVATE KEY")
// DER-encoded RSA key.
func parseRSAPrivateKey(der []byte) (*rsa.PrivateKey, error) {
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("dkim: PKCS#8 key is not RSA")
	}
	return rk, nil
}

// header is one message header field (name and value, unfolded) in presentation order.
type header struct {
	name  string
	value string
}

// signedHeaderNames is the ordered set of header field names included in the DKIM
// signature (h=). It deliberately covers the identity-bearing and unsubscribe headers
// so a downstream cannot strip List-Unsubscribe without breaking the signature.
var signedHeaderNames = []string{
	"From", "To", "Subject", "Date", "Message-ID", "MIME-Version",
	"Content-Type", "List-Unsubscribe", "List-Unsubscribe-Post",
}

// sign computes the DKIM-Signature header VALUE (the text that follows
// "DKIM-Signature:") for the given headers and body at time now. The returned value is
// already folded-free and ready to prepend to the message. Only headers present in the
// message and listed in signedHeaderNames are signed, in that order.
func (s *dkimSigner) sign(headers []header, body []byte, now time.Time) (string, error) {
	// 1. Body hash over the relaxed-canonicalized body.
	bodyCanon := canonicalizeBodyRelaxed(body)
	bh := sha256.Sum256(bodyCanon)
	bhB64 := base64.StdEncoding.EncodeToString(bh[:])

	// 2. Determine which signed headers are actually present, preserving order.
	present := presentSignedHeaders(headers)
	hTag := strings.Join(headerNames(present), ":")

	// 3. Build the DKIM-Signature value with an empty b= (signed last, see RFC 6376 §3.7).
	dkimNoB := fmt.Sprintf(
		"v=1; a=rsa-sha256; c=relaxed/relaxed; d=%s; s=%s; t=%d; h=%s; bh=%s; b=",
		s.domain, s.selector, now.UTC().Unix(), hTag, bhB64,
	)

	// 4. Assemble the data to sign: each signed header relaxed-canonicalized with CRLF,
	//    then the DKIM-Signature header itself (relaxed, empty b=) WITHOUT a trailing CRLF.
	var sb strings.Builder
	for _, h := range present {
		sb.WriteString(canonicalizeHeaderRelaxed(h.name, h.value))
		sb.WriteString("\r\n")
	}
	sb.WriteString(canonicalizeHeaderRelaxed("DKIM-Signature", dkimNoB))

	// 5. RSA-SHA256 sign and fill in b=.
	digest := sha256.Sum256([]byte(sb.String()))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("dkim: sign: %w", err)
	}
	return dkimNoB + base64.StdEncoding.EncodeToString(sig), nil
}

// presentSignedHeaders returns the signable headers in signedHeaderNames order, but
// only those actually present in the message.
func presentSignedHeaders(headers []header) []header {
	byName := map[string]header{}
	for _, h := range headers {
		byName[strings.ToLower(h.name)] = h
	}
	var out []header
	for _, name := range signedHeaderNames {
		if h, ok := byName[strings.ToLower(name)]; ok {
			out = append(out, h)
		}
	}
	return out
}

func headerNames(hs []header) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.name
	}
	return out
}

// canonicalizeHeaderRelaxed applies RFC 6376 §3.4.2 relaxed header canonicalization to
// one header field, returning "name:value" with NO trailing CRLF: the field name is
// lowercased; the value is unfolded, internal WSP runs collapse to a single SP, leading
// WSP after the colon and trailing WSP are removed.
func canonicalizeHeaderRelaxed(name, value string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	// Unfold: replace CRLF (and stray CR/LF) with nothing — folding WSP becomes the
	// surrounding WSP which is then collapsed.
	v := strings.ReplaceAll(value, "\r\n", "")
	v = strings.ReplaceAll(v, "\n", "")
	v = strings.ReplaceAll(v, "\r", "")
	v = collapseWSP(v)
	v = strings.TrimSpace(v)
	return n + ":" + v
}

// canonicalizeBodyRelaxed applies RFC 6376 §3.4.4 relaxed body canonicalization:
// trailing WSP on each line is removed, intra-line WSP runs collapse to a single SP,
// and trailing empty lines are removed. The result always ends in a single CRLF (an
// empty body canonicalizes to a single CRLF).
func canonicalizeBodyRelaxed(body []byte) []byte {
	// Normalize line endings to \n for processing.
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		ln = collapseWSP(ln)
		lines[i] = strings.TrimRight(ln, " \t")
	}
	// Remove trailing empty lines.
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	lines = lines[:end]
	var sb strings.Builder
	for _, ln := range lines {
		sb.WriteString(ln)
		sb.WriteString("\r\n")
	}
	if sb.Len() == 0 {
		return []byte("\r\n")
	}
	return []byte(sb.String())
}

// collapseWSP collapses every run of spaces and tabs to a single space.
func collapseWSP(s string) string {
	var sb strings.Builder
	inWSP := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !inWSP {
				sb.WriteByte(' ')
				inWSP = true
			}
			continue
		}
		inWSP = false
		sb.WriteRune(r)
	}
	return sb.String()
}
