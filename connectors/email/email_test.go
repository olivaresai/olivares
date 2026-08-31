// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package email

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// captured holds what the injected sender received.
type captured struct {
	msg  []byte
	from string
	to   []string
}

func openEmail(t *testing.T, extra map[string]string) (*Output, *captured) {
	t.Helper()
	cap := &captured{}
	o := New()
	o.send = func(_ context.Context, msg []byte, from string, to []string) error {
		cap.msg, cap.from, cap.to = msg, from, to
		return nil
	}
	o.now = func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }
	o.rid = func() (string, error) { return "abc123def456abcd", nil }
	cfg := map[string]string{
		cfgSMTPHost: "smtp.acme.io", cfgFrom: "alerts@acme.io", cfgTo: "oncall@acme.io",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o, cap
}

func TestMessageHeadersAndUnsubscribe(t *testing.T) {
	o, cap := openEmail(t, map[string]string{
		cfgUnsubURL:    "https://acme.io/u/opaque",
		cfgUnsubMailto: "unsub@acme.io",
		cfgFromName:    "Olivares Alerts",
	})
	n := sdk.Notification{Title: "Drift", Body: "1 unexpected access", Severity: model.SeverityHigh, Tenant: "acme"}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	msg := string(cap.msg)
	if cap.from != "alerts@acme.io" || len(cap.to) != 1 || cap.to[0] != "oncall@acme.io" {
		t.Fatalf("envelope from/to = %q %v", cap.from, cap.to)
	}
	mustContain(t, msg, "From: Olivares Alerts <alerts@acme.io>")
	mustContain(t, msg, "Subject: Drift")
	mustContain(t, msg, "To: oncall@acme.io")
	mustContain(t, msg, "List-Unsubscribe: <https://acme.io/u/opaque>, <mailto:unsub@acme.io>")
	mustContain(t, msg, "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	// Body carries the detail and the severity/tenant block.
	mustContain(t, msg, "1 unexpected access")
	mustContain(t, msg, "severity: high")
}

func TestPerNotificationRecipient(t *testing.T) {
	o, cap := openEmail(t, nil)
	n := sdk.Notification{Title: "x", Fields: map[string]string{fieldTo: "sec@acme.io, lead@acme.io"}}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(cap.to) != 2 || cap.to[0] != "sec@acme.io" || cap.to[1] != "lead@acme.io" {
		t.Fatalf("recipients = %v", cap.to)
	}
}

func TestNoRecipientErrors(t *testing.T) {
	o := New()
	o.send = func(context.Context, []byte, string, []string) error { return nil }
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{cfgSMTPHost: "h", cfgFrom: "f@x"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err == nil {
		t.Fatal("expected error with no recipient")
	}
}

// TestDKIMSignatureVerifies generates a key, signs a message, and verifies the DKIM
// signature end-to-end: it recomputes the body hash and the signed-header digest and
// checks the RSA signature against the public key — proving the connector emits a
// cryptographically valid DKIM-Signature, not a plausible-looking but unverifiable one.
func TestDKIMSignatureVerifies(t *testing.T) {
	key := genKey(t)
	priv := pemPKCS1(key)

	o, cap := openEmail(t, map[string]string{
		cfgDKIMDomain: "acme.io", cfgDKIMSelector: "s1", cfgDKIMKey: priv,
		cfgUnsubURL: "https://acme.io/u/x",
	})
	if err := o.Notify(context.Background(), sdk.Notification{Title: "Hello", Body: "world"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	msg := string(cap.msg)
	if !strings.HasPrefix(msg, "DKIM-Signature: ") {
		t.Fatalf("message does not start with DKIM-Signature header:\n%s", msg)
	}
	tags := parseDKIM(t, msg)
	if tags["v"] != "1" || tags["a"] != "rsa-sha256" || tags["c"] != "relaxed/relaxed" {
		t.Fatalf("dkim tags = %v", tags)
	}
	if tags["d"] != "acme.io" || tags["s"] != "s1" {
		t.Fatalf("dkim d/s = %v", tags)
	}

	headers, body := splitMessage(t, msg)

	// Verify body hash (bh=).
	bodyCanon := canonicalizeBodyRelaxed(body)
	bh := sha256.Sum256(bodyCanon)
	if base64.StdEncoding.EncodeToString(bh[:]) != tags["bh"] {
		t.Fatalf("body hash mismatch")
	}

	// Reconstruct the signed data: each signed header (relaxed) + the DKIM-Signature
	// header with b= emptied, no trailing CRLF.
	var signed strings.Builder
	for _, name := range strings.Split(tags["h"], ":") {
		signed.WriteString(canonicalizeHeaderRelaxed(name, headers[strings.ToLower(name)]))
		signed.WriteString("\r\n")
	}
	dkimVal := headerValue(msg, "DKIM-Signature")
	dkimNoB := dkimVal[:strings.Index(dkimVal, "b=")+2] // up to and including "b="
	signed.WriteString(canonicalizeHeaderRelaxed("DKIM-Signature", dkimNoB))

	digest := sha256.Sum256([]byte(signed.String()))
	sig, err := base64.StdEncoding.DecodeString(tags["b"])
	if err != nil {
		t.Fatalf("decode b=: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("DKIM signature does not verify: %v", err)
	}
}

func TestNoDKIMWhenUnconfigured(t *testing.T) {
	o, cap := openEmail(t, nil)
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if strings.Contains(string(cap.msg), "DKIM-Signature") {
		t.Fatal("must not emit a DKIM-Signature when no key is configured")
	}
}

func TestNoSecretLeak(t *testing.T) {
	const pw = "smtp-password-secret"
	o := New()
	o.send = func(context.Context, []byte, string, []string) error {
		return errCustom("550 5.7.515 access denied from smtp.acme.io")
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgSMTPHost: "smtp.acme.io", cfgFrom: "f@acme.io", cfgTo: "t@acme.io",
		cfgSMTPUsername: "u", cfgSMTPPassword: pw,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	err := o.Notify(context.Background(), sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatal("expected delivery error")
	}
	if strings.Contains(err.Error(), pw) {
		t.Fatalf("SECURITY: SMTP password leaked into error: %v", err)
	}
	for _, f := range New().Descriptor().ConfigFields {
		if f.Secret && f.Default != "" {
			t.Errorf("secret field %q has a non-empty default", f.Key)
		}
	}
}

// --- helpers -----------------------------------------------------------------------

type errCustom string

func (e errCustom) Error() string { return string(e) }

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("message missing %q in:\n%s", sub, s)
	}
}

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return k
}

func pemPKCS1(k *rsa.PrivateKey) string {
	der := x509.MarshalPKCS1PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

// parseDKIM extracts the DKIM-Signature tags into a map.
func parseDKIM(t *testing.T, msg string) map[string]string {
	t.Helper()
	val := headerValue(msg, "DKIM-Signature")
	tags := map[string]string{}
	for _, part := range strings.Split(val, ";") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "="); i > 0 {
			tags[part[:i]] = strings.TrimSpace(part[i+1:])
		}
	}
	return tags
}

// headerValue returns the unfolded value of the named header (first occurrence).
func headerValue(msg, name string) string {
	for _, line := range strings.Split(headerBlock(msg), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			return strings.TrimSpace(line[len(name)+1:])
		}
	}
	return ""
}

func headerBlock(msg string) string {
	if i := strings.Index(msg, "\r\n\r\n"); i >= 0 {
		return msg[:i]
	}
	return msg
}

// splitMessage returns a lowercased-name header map and the raw body bytes.
func splitMessage(t *testing.T, msg string) (map[string]string, []byte) {
	t.Helper()
	i := strings.Index(msg, "\r\n\r\n")
	if i < 0 {
		t.Fatal("no header/body separator")
	}
	headers := map[string]string{}
	for _, line := range strings.Split(msg[:i], "\r\n") {
		if c := strings.Index(line, ":"); c > 0 {
			headers[strings.ToLower(strings.TrimSpace(line[:c]))] = strings.TrimSpace(line[c+1:])
		}
	}
	return headers, []byte(msg[i+4:])
}
