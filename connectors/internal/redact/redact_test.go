// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"strings"
	"testing"
)

// secretCorpus pairs a raw value containing a secret with the substring that
// must NOT survive a scrub. The fingerprint is a slice of the secret long enough
// to be unambiguous; if it appears in the cleaned output, redaction failed.
var secretCorpus = []struct {
	name       string
	raw        string
	leak       string // must be absent from the scrubbed result
	wantSecret bool
}{
	{"aws-access-key", "AKIAIOSFODNN7EXAMPLE used here", "AKIAIOSFODNN7EXAMPLE", true},
	{"github-classic", "token ghp_1234567890abcdefghijklmnopqrstuvwxyzAB", "ghp_1234567890abcdefghijklmnopqrstuvwxyzAB", true},
	{"github-pat", "github_pat_11ABCDEFG0abcdefghijkl_xyz1234567890ABCDEFGH", "github_pat_11ABCDEFG0abcdefghijkl", true},
	{"slack", "xoxb" + "-1234567890-abcdefghijklmnop", "xoxb" + "-1234567890-abcdefghijklmnop", true},
	{"google", "AIza" + "SyA1234567890abcdefghijklmnopqrstuv", "AIza" + "SyA1234567890abcdefghijklmnopqrstuv", true},
	{"anthropic", "sk-ant-api03-abcdefghijklmnopqrstuvwxyz", "sk-ant-api03-abcdefghijklmnopqrstuvwxyz", true},
	{"openai", "sk-abcdefghijklmnopqrstuvwxyz0123", "sk-abcdefghijklmnopqrstuvwxyz0123", true},
	{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N", "eyJzdWIiOiIxMjM0NTY3ODkwIn0", true},
	{"private-key", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn...", "BEGIN OPENSSH PRIVATE KEY", true},
	{"bearer", "Authorization: Bearer abcDEF123456_secret.token", "abcDEF123456_secret.token", true},
	{"kv-equals", "API_KEY=supersecretvalue123", "supersecretvalue123", true},
	{"kv-colon", `{"password": "hunter2hunter2"}`, "hunter2hunter2", true},
	{"kv-query", "https://x.test/path?token=abcdef123456&y=1", "abcdef123456", true},
}

func TestScrubRemovesSecrets(t *testing.T) {
	for _, tc := range secretCorpus {
		t.Run(tc.name, func(t *testing.T) {
			got, redacted := Scrub(tc.raw)
			if redacted != tc.wantSecret {
				t.Errorf("Scrub(%q) redacted=%v, want %v (got %q)", tc.raw, redacted, tc.wantSecret, got)
			}
			if strings.Contains(got, tc.leak) {
				t.Errorf("Scrub leaked secret %q in result %q", tc.leak, got)
			}
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("Scrub(%q) produced no redaction marker: %q", tc.raw, got)
			}
		})
	}
}

func TestScrubKeepsKeyStructure(t *testing.T) {
	got, _ := Scrub("api_key=ABCDEF1234567890")
	if !strings.HasPrefix(strings.ToLower(got), "api_key=") {
		t.Errorf("Scrub dropped the key: %q", got)
	}
	if strings.Contains(got, "ABCDEF1234567890") {
		t.Errorf("Scrub leaked value: %q", got)
	}
}

func TestScrubLeavesCleanText(t *testing.T) {
	clean := []string{
		"",
		"public.customers",
		"/home/user/project/main.go",
		"https://api.github.com/repos/olivaresai/olivares",
		"SELECT count(*) FROM orders",
		"Read the file and summarize it",
	}
	for _, s := range clean {
		got, redacted := Scrub(s)
		if redacted {
			t.Errorf("Scrub(%q) flagged clean text; got %q", s, got)
		}
		if got != s {
			t.Errorf("Scrub(%q) altered clean text to %q", s, got)
		}
	}
}

func TestContainsSecret(t *testing.T) {
	if !ContainsSecret("AKIAIOSFODNN7EXAMPLE") {
		t.Error("ContainsSecret missed an AWS key")
	}
	if ContainsSecret("/var/log/app.log") {
		t.Error("ContainsSecret false-positived on a plain path")
	}
}

func TestSanitizeURL(t *testing.T) {
	cases := map[string]string{
		"https://user:p4ss@host.test/path?token=abc#frag": "https://host.test/path",
		"http://host.test/a/b?api_key=secret":             "http://host.test/a/b",
		"https://host.test":                               "https://host.test",
	}
	for raw, want := range cases {
		if got := SanitizeURL(raw); got != want {
			t.Errorf("SanitizeURL(%q) = %q, want %q", raw, got, want)
		}
		if strings.Contains(SanitizeURL(raw), "secret") || strings.Contains(SanitizeURL(raw), "p4ss") || strings.Contains(SanitizeURL(raw), "token=abc") {
			t.Errorf("SanitizeURL(%q) leaked a credential", raw)
		}
	}
}

func TestSanitizeURLMalformed(t *testing.T) {
	// A value that is not a URL must still be scrubbed, never returned raw.
	got := SanitizeURL("not a url token=abcdef123456")
	if strings.Contains(got, "abcdef123456") {
		t.Errorf("SanitizeURL leaked from malformed input: %q", got)
	}
}

func TestSanitizeURLStripsUserinfoAcrossSchemes(t *testing.T) {
	// Userinfo credentials must be stripped regardless of scheme — and even when
	// there is no scheme (scheme-confusion), where url.Parse yields an empty Host.
	cases := []struct{ raw, leak string }{
		{"user:p4ssw0rd@host/data", "p4ssw0rd"},
		{"//user:p4ssw0rd@host/data", "p4ssw0rd"},
		{"redis://default:p4ssw0rd@cache:6379/0", "p4ssw0rd"},
		{"postgres://admin:s3cr3tpw@db.internal/orders", "s3cr3tpw"},
		{"mongodb+srv://u:VerySecret@cluster/db", "VerySecret"},
	}
	for _, tc := range cases {
		if got := SanitizeURL(tc.raw); strings.Contains(got, tc.leak) {
			t.Errorf("SanitizeURL(%q) leaked %q: %q", tc.raw, tc.leak, got)
		}
	}
	// The resource identity (host/path) survives; a clean URI is not mangled.
	if got := SanitizeURL("postgres://admin:s3cr3tpw@db.internal/orders"); !strings.Contains(got, "db.internal") {
		t.Errorf("SanitizeURL dropped the host: %q", got)
	}
	if got := SanitizeURL("file:///etc/hosts"); got != "file:///etc/hosts" {
		t.Errorf("SanitizeURL mangled a clean file URI: %q", got)
	}
}

func TestSanitizeURLStripsHostlessQuery(t *testing.T) {
	// A URL-shaped value with an EMPTY host (triple-slash, or an opaque URI) must
	// still have its query/fragment stripped — a credential there would otherwise
	// survive, because the host-based stripURL path cannot fire. The query key may
	// be one the key=value scrubber does not know (e.g. an opaque signature), so the
	// only safe behavior is to drop the whole query.
	cases := []struct{ raw, leak string }{
		{"s3:///bucket/logs?x-amz-signature=ABCDEF0123456789", "ABCDEF0123456789"},
		{"gs:///bucket/path#frag-sig=DEADBEEFCAFE", "DEADBEEFCAFE"},
		{"//host-less/path?sig=TOPSECRETVALUE", "TOPSECRETVALUE"},
	}
	for _, tc := range cases {
		if got := SanitizeURL(tc.raw); strings.Contains(got, tc.leak) {
			t.Errorf("SanitizeURL(%q) leaked %q: %q", tc.raw, tc.leak, got)
		}
	}
	// A clean hostless URI (no query/fragment) is preserved verbatim.
	if got := SanitizeURL("s3:///bucket/logs"); got != "s3:///bucket/logs" {
		t.Errorf("SanitizeURL mangled a clean hostless URI: %q", got)
	}
}

func TestSanitizeDSN(t *testing.T) {
	got := SanitizeDSN("postgres://app:s3cr3tpw@db.internal:5432/orders?sslmode=require")
	if strings.Contains(got, "s3cr3tpw") {
		t.Errorf("SanitizeDSN leaked password: %q", got)
	}
	if !strings.Contains(got, "db.internal") || !strings.Contains(got, "orders") {
		t.Errorf("SanitizeDSN dropped resource identity: %q", got)
	}
	if !strings.Contains(got, "app") {
		t.Errorf("SanitizeDSN dropped username (useful identity): %q", got)
	}
}

func TestSanitizeDSNKeyValue(t *testing.T) {
	got := SanitizeDSN("host=db.internal user=app password=s3cr3tpw dbname=orders")
	if strings.Contains(got, "s3cr3tpw") {
		t.Errorf("SanitizeDSN leaked kv password: %q", got)
	}
}

func TestHashStableAndOpaque(t *testing.T) {
	a := Hash("the-secret")
	b := Hash("the-secret")
	if a != b {
		t.Error("Hash not stable")
	}
	if len(a) != 64 {
		t.Errorf("Hash length = %d, want 64 hex chars", len(a))
	}
	if strings.Contains(a, "the-secret") {
		t.Error("Hash is not opaque")
	}
	if Hash("a") == Hash("b") {
		t.Error("Hash collided on distinct inputs")
	}
}
