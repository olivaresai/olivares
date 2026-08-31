// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"strings"
	"testing"
)

func TestExportedRedactorUsesCanonicalCatalog(t *testing.T) {
	const secret = "correct-horse-battery"
	in := "api_key=" + secret

	if !ContainsSecretOrPII(in) {
		t.Fatal("ContainsSecretOrPII did not recognize a canonical key/value secret")
	}
	if ContainsSecretOrPII("ordinary diagnostic text") {
		t.Fatal("ContainsSecretOrPII reported ordinary diagnostic text")
	}

	out, redactions := RedactText(in)
	if strings.Contains(out, secret) {
		t.Fatalf("RedactText leaked the secret: %q", out)
	}
	if !strings.Contains(out, "[redacted") {
		t.Fatalf("RedactText did not emit a redaction marker: %q", out)
	}
	if redactions != 1 {
		t.Fatalf("RedactText redactions = %d, want 1", redactions)
	}
}

func TestExportedRedactorCoversExtendedKeyValueCatalog(t *testing.T) {
	const sentinel = "support-bundle-sensitive-value"
	keys := []string{
		"cookie", "hmac", "session_id", "session-id", "credential", "nonce", "salt",
		"signature", "session_key", "session-key", "csrf", "xsrf",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			in := key + "=" + sentinel
			if !ContainsSecretOrPII(in) {
				t.Fatalf("ContainsSecretOrPII did not recognize %q", key)
			}
			out, redactions := RedactText(in)
			if strings.Contains(out, sentinel) {
				t.Fatalf("RedactText leaked %q: %q", key, out)
			}
			if redactions != 1 {
				t.Fatalf("RedactText redactions for %q = %d, want 1: %q", key, redactions, out)
			}
		})
	}
}

func TestExportedRedactorRemovesCompletePrivateKeyBlock(t *testing.T) {
	const body = "U0VOU0lUSVZFUEVNS0VZQk9EWQ=="
	// Markers split so a path-scanning detector cannot treat this source as a
	// contiguous PEM (A-04). Runtime concatenation is still a complete block.
	in := "secret=-----BEGIN PRI" + "VATE KEY-----\n" + body + "\n-----END PRI" + "VATE KEY-----\n"

	if !ContainsSecretOrPII(in) {
		t.Fatal("ContainsSecretOrPII did not recognize a complete private-key block")
	}
	out, redactions := RedactText(in)
	beginMark := "-----BEGIN PRI" + "VATE KEY-----"
	endMark := "-----END PRI" + "VATE KEY-----"
	for _, leaked := range []string{beginMark, body, endMark} {
		if strings.Contains(out, leaked) {
			t.Fatalf("RedactText leaked PEM content %q: %q", leaked, out)
		}
	}
	if redactions != 1 {
		t.Fatalf("RedactText redactions = %d, want 1: %q", redactions, out)
	}
	if ContainsSecretOrPII(out) {
		t.Fatalf("ContainsSecretOrPII reported canonical redacted output: %q", out)
	}
}

func TestContainsSecretOrPIIIsRedactorFixedPoint(t *testing.T) {
	inputs := map[string]string{
		"credit-card": "card 4111111111111111",
		"private-key": "-----BEGIN PRI" + "VATE KEY-----\nU0VOU0lUSVZF\n-----END PRI" + "VATE KEY-----",
		"key-value":   "token=opaque-test-value",
		"email":       "operator@example.com",
		"ip-address":  "source 192.0.2.10",
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			redacted, _ := RedactText(input)
			if redacted == input {
				t.Fatalf("RedactText did not redact catalog input %q", input)
			}
			if !ContainsSecretOrPII(input) {
				t.Fatalf("RedactText changed input but ContainsSecretOrPII returned false: %q", input)
			}
		})
	}
}

// TestRedactCredentialsRemovesCredentialsAndKeepsDiagnosis pins the log
// redactor. Its two halves are equally load-bearing and the second one is the one
// that would rot quietly: a redactor that also removed the network identifiers
// would leave an operator a log line with nothing actionable in it, and a log
// nobody can act on is a log somebody turns off.
func TestRedactCredentialsRemovesCredentialsAndKeepsDiagnosis(t *testing.T) {
	removed := map[string]string{
		"url-userinfo":     "connect postgres://alma:S3cr3t-P4ss@db.internal.corp:5432/alma failed",
		"key-value-secret": "GET /v1/x?access_token=AbCdEf0123456789xyz rejected",
		"aws-access-key":   "upstream refused AKIAQQQWWWEEERRRTTTY for role sync",
		"github-token":     "clone failed with ghp_0123456789012345678901234567890123456",
		"bearer-token":     "proxy rejected header Authorization: Bearer abcdef0123456789.token",
		"private-key":      "loaded -----BEGIN PRI" + "VATE KEY-----\nQUJDREVGR0hJSktMTU5PUFFS\n-----END PRI" + "VATE KEY-----\n",
		"jwt":              "assertion eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3OCJ9.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk",
	}
	secrets := map[string]string{
		"url-userinfo":     "S3cr3t-P4ss",
		"key-value-secret": "AbCdEf0123456789xyz",
		"aws-access-key":   "AKIAQQQWWWEEERRRTTTY",
		"github-token":     "ghp_0123456789012345678901234567890123456",
		"bearer-token":     "abcdef0123456789.token",
		"private-key":      "QUJDREVGR0hJSktMTU5PUFFS",
		"jwt":              "dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk",
	}
	for rule, in := range removed {
		t.Run("removes/"+rule, func(t *testing.T) {
			out, n := RedactCredentials(in)
			if strings.Contains(out, secrets[rule]) {
				t.Fatalf("%s survived: %q", rule, out)
			}
			if n == 0 {
				t.Fatalf("%s: no redaction reported for %q", rule, out)
			}
			if !strings.Contains(out, "[REDACTED:") {
				t.Fatalf("%s: no marker emitted: %q", rule, out)
			}
		})
	}

	// The DELIBERATE exclusions. These are the operator's diagnosis, and this
	// redactor serves a surface read by an authenticated system:admin inside the
	// trust boundary. The support bundle, which leaves the machine, keeps
	// RedactText and its full PII catalog — that asymmetry is the decision.
	kept := map[string]string{
		"ipv4":        "dial tcp 10.9.8.7:5432: connect: connection refused",
		"email":       "LDAP: user alice@corp.local not found in the directory",
		"mac-address": "interface 3c:22:fb:11:22:33 dropped the link",
		"hostname":    "TLS handshake to idp.corp.internal timed out",
		// 12345678A: the es-nif SHAPE with a DELIBERATELY WRONG control letter (the
		// valid one is Z). The shape is what both catalogs match on, so the test
		// measures exactly the same thing — and lint:fiscal-id keeps a checksum-valid
		// Spanish identifier off a surface that exports to public repositories.
		"es-nif": "payroll job for 12345678A failed to schedule",
	}
	for name, in := range kept {
		t.Run("keeps/"+name, func(t *testing.T) {
			out, _ := RedactCredentials(in)
			if out != in {
				t.Fatalf("%s: the log redactor removed the diagnosis.\n  in:  %q\n  out: %q", name, in, out)
			}
		})
	}

	// And the control that keeps the two halves honest: the FULL catalog DOES
	// remove those, so "keeps" above is a property of this function and not of the
	// inputs. Without this, weakening the whole catalog would leave "keeps" green.
	for name, in := range kept {
		if name == "hostname" {
			continue // no rule claims a bare hostname in either catalog
		}
		if out, _ := RedactText(in); out == in {
			t.Errorf("control/%s: RedactText left %q unchanged, so the exclusion above proves nothing", name, in)
		}
	}
}

// TestRedactCredentialsIsIdempotent: running it twice must not relabel a marker
// as a generic secret nor count it again — the same fixed-point property the
// catalog's other redactors carry.
func TestRedactCredentialsIsIdempotent(t *testing.T) {
	in := "postgres://alma:S3cr3t@db.corp:5432/x and api_key=0123456789abcdef and AKIAQQQWWWEEERRRTTTY"
	once, n1 := RedactCredentials(in)
	twice, n2 := RedactCredentials(once)
	if once != twice {
		t.Fatalf("not idempotent:\n  once:  %q\n  twice: %q", once, twice)
	}
	if n1 == 0 {
		t.Fatalf("nothing was redacted from %q", in)
	}
	if n2 != 0 {
		t.Errorf("second pass reported %d redactions over already-clean text: %q", n2, twice)
	}
}

// TestURLUserinfoRuleRequiresAPasswordSeparator pins the boundary decision,
// on BOTH sides, across every surface the shared catalog drives.
//
// A hit in this catalog is not cosmetic: modules/sessions/workspace_dlp.go
// classifyContent DENIES a governed file read on one hit in deny mode, and the
// knowledge/inference paths label and gate on the same call. This repository
// documents "postgres://olivares_app@db/olivares?sslmode=verify-full" as a VALID
// configuration (cmd/olivares/envfile_test.go), so a rule that flagged it would
// deny reads over a string the product tells operators to use.
func TestURLUserinfoRuleRequiresAPasswordSeparator(t *testing.T) {
	withPassword := []string{
		"postgres://alma:S3cr3t-P4ss@db.internal.corp:5432/alma?sslmode=require",
		"amqps://svc:hunter2@broker.corp:5671/vhost",
		"https://user:tok3nvalue@api.corp/v1",
	}
	withoutPassword := []string{
		"postgres://olivares_app@db/olivares?sslmode=verify-full",
		"git+ssh://git@code.example.com/acme/widgets",
		"ssh://deploy@bastion.corp:22",
	}

	for _, in := range withPassword {
		t.Run("flags/"+in, func(t *testing.T) {
			if !ContainsSecretOrPII(in) {
				t.Errorf("ContainsSecretOrPII missed a password-bearing DSN: %q", in)
			}
			if out, _ := RedactText(in); out == in {
				t.Errorf("RedactText left a password-bearing DSN unchanged: %q", in)
			}
			if !hasSensitivityRule(ClassifySensitivity(in), "url-userinfo") {
				t.Errorf("ClassifySensitivity did not report url-userinfo for %q", in)
			}
		})
	}

	for _, in := range withoutPassword {
		t.Run("allows/"+in, func(t *testing.T) {
			if hasSensitivityRule(ClassifySensitivity(in), "url-userinfo") {
				t.Errorf("url-userinfo fired on a passwordless authority — a governed read "+
					"in deny mode would be refused over valid configuration: %q", in)
			}
			if hasDetectionRule(newPIIDetector().Inspect(GuardrailInput{Text: in}), "url-userinfo") {
				t.Errorf("the guardrail detector fired url-userinfo on a passwordless authority: %q", in)
			}
		})
	}

	// The control that keeps "allows" from being vacuous: the same strings DO get
	// flagged the moment a password appears, so the exclusion is about the
	// separator and not about these hosts being invisible to the catalog.
	for _, in := range withoutPassword {
		withPw := strings.Replace(in, "@", ":Pa55word@", 1)
		if !hasSensitivityRule(ClassifySensitivity(withPw), "url-userinfo") {
			t.Errorf("control: adding a password to %q did not make it a hit (%q)", in, withPw)
		}
	}
}

func hasSensitivityRule(hits []SensitivityHit, rule string) bool {
	for _, h := range hits {
		if h.Rule == rule {
			return true
		}
	}
	return false
}

func hasDetectionRule(ds []Detection, rule string) bool {
	for _, d := range ds {
		if d.Rule == rule {
			return true
		}
	}
	return false
}

// TestScrubAndContainsAgreeOnTheV2Rule is the cross-cutting conformance the new
// rule needs. The support bundle's fail-closed final guard rests on a documented
// invariant — "for every input, a change made by RedactText implies
// ContainsSecretOrPII returns true" — and url-userinfo was added to five call
// sites by hand. A rule wired into the scrubber and not into the predicate would
// break that guard silently, on the one path whose whole job is to fail closed.
func TestScrubAndContainsAgreeOnTheV2Rule(t *testing.T) {
	for _, in := range []string{
		// THE LOAD-BEARING CASES, and the ones this test did not have until a
		// mutant said so. A host WITH a dot lets the email shape
		// ("S3cr3t@db.corp") answer for the predicate, so deleting url-userinfo
		// from containsSecretOrPII stayed invisible — the exact accident this rule
		// was added to end, hiding the test for the rule. A dotless host and a bare
		// IP leave url-userinfo as the only rule that can fire.
		"postgres://alma:S3cr3t@localhost:5432/x",
		"postgres://alma:S3cr3t@10.9.8.7:5432/x",
		"amqps://svc:hunter2@broker:5671/vhost",
		// And the same shapes with a dotted host, which must not regress either.
		"postgres://alma:S3cr3t@db.corp:5432/x",
		"connect mongodb://root:toor@mongo.internal:27017 failed",
		"plain diagnostic text with no secret at all",
		"postgres://olivares_app@db/olivares",
		"api_key=0123456789abcdef",
	} {
		if out, _ := RedactText(in); out != in && !ContainsSecretOrPII(in) {
			t.Errorf("invariant broken: RedactText changed %q but ContainsSecretOrPII said no", in)
		}
	}
}
