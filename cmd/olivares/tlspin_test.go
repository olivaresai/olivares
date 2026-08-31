// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/secure"
)

// THE DEFECT THIS FILE PINS.
//
// On first boot the engine generated a self-signed certificate and told the
// operator, in as many words, that clients "must trust it or pin its fingerprint" —
// printing fingerprint_sha256=<value>. That value came from secure.EnsureTLSCert:
// hex(sha256(certificate DER)).
//
// The ONLY pin flag the product has, --pin-sha256, decodes base64 and compares
// against sha256(leaf.RawSubjectPublicKeyInfo) — the SPKI. Two different digests, of
// two different objects, in two different encodings. So the operator who did exactly
// what the startup line said got "invalid --pin-sha256: expected a base64-encoded
// 32-byte SHA-256 digest", and nothing anywhere in the product told them how to
// produce the value that would have worked.
//
// The test that matters is the round trip: take what the engine PRINTS, give it to
// the flag, and connect.

// generatedCert mints the same self-signed pair `serve` mints on a first boot.
func generatedCert(t *testing.T) (certPath, keyPath, printedPin, printedFingerprint string) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	created, fp, err := secure.EnsureTLSCert(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureTLSCert: %v", err)
	}
	if !created {
		t.Fatalf("EnsureTLSCert reported no new certificate in a fresh directory")
	}
	// tlsTrustAttrs is what runEngine logs. Reading the value FROM IT — rather than
	// recomputing a pin here — is the point: a test that derives its own expected
	// value can agree with itself while the product prints something else.
	attrs := tlsTrustAttrs(certPath, fp)
	printedPin = attrValue(t, attrs, "pin_sha256")
	printedFingerprint = attrValue(t, attrs, "cert_fingerprint_sha256")
	return certPath, keyPath, printedPin, printedFingerprint
}

// attrValue pulls a key's value out of the alternating key/value slice slog takes.
func attrValue(t *testing.T, attrs []any, key string) string {
	t.Helper()
	for i := 0; i+1 < len(attrs); i += 2 {
		if k, ok := attrs[i].(string); ok && k == key {
			v, ok := attrs[i+1].(string)
			if !ok {
				t.Fatalf("attribute %q is not a string: %#v", key, attrs[i+1])
			}
			return v
		}
	}
	t.Fatalf("the startup line carries no %q attribute; attrs=%#v", key, attrs)
	return ""
}

// TestThePrintedPinIsAcceptedByTheFlagItRecommends is the whole defect, end to end:
// what the engine prints goes into --pin-sha256 and a real TLS request succeeds.
func TestThePrintedPinIsAcceptedByTheFlagItRecommends(t *testing.T) {
	certPath, keyPath, printedPin, _ := generatedCert(t)

	if printedPin == "" {
		t.Fatal("the first-boot line prints no value the --pin-sha256 flag can take")
	}
	// Step 1: the flag's own decoder must accept it at all.
	if _, err := decodeSPKIPin(printedPin); err != nil {
		t.Fatalf("--pin-sha256 rejects the value the engine told the operator to pin (%q): %v",
			printedPin, err)
	}

	// Step 2: and it must actually authenticate the server that printed it.
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load generated pair: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	client, _, err := cliTransport(cliTransportOptions{
		Resolved: cliResolvedConfig{Server: srv.URL, PinSHA256: []string{printedPin}},
	})
	if err != nil {
		t.Fatalf("build a pinned client from the printed value: %v", err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("a client pinned with the value the engine printed could not connect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

// TestTheRenderedStartupLineYieldsACopyablePin is the test the unit-level one above
// could not be.
//
// THE RESIDUAL DEFECT IT CAUGHT, on the real binary, after the first fix looked
// finished: SPKIPin returned PADDED base64, padded base64 ends in '=', and slog's
// text handler quotes any value containing '='. The line rendered
//
//	pin_sha256="B3PLXfenHGLy4qXGC94M3PlU3VEyNB3EVM3ES50QtVY="
//
// so an operator following "pin it with --pin-sha256=<pin_sha256> (that value,
// verbatim)" copied the quotation marks and was refused — the very failure the fix
// existed to remove, one layer further out. Reading the attribute SLICE cannot see
// this; only rendering the record can. So this test renders it and parses the value
// exactly as a human copying from a terminal would.
func TestTheRenderedStartupLineYieldsACopyablePin(t *testing.T) {
	certPath, _, _, fp := generatedCert(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	logger.Warn("generated a self-signed TLS certificate", tlsTrustAttrs(certPath, fp)...)
	line := buf.String()

	const key = "pin_sha256="
	i := strings.Index(line, key)
	if i < 0 {
		t.Fatalf("the rendered line carries no pin_sha256: %s", line)
	}
	// What a copy-paste actually grabs: the whitespace-delimited token.
	copied := strings.Fields(line[i+len(key):])[0]
	copied = strings.TrimSuffix(copied, "\n")

	if strings.ContainsAny(copied, `"'`) {
		t.Errorf("the rendered pin carries quoting punctuation (%s): an operator told to paste it "+
			"verbatim pastes the quotes too. Full line: %s", copied, line)
	}
	if _, err := decodeSPKIPin(copied); err != nil {
		t.Fatalf("the value as it appears in the log (%s) is rejected by --pin-sha256: %v", copied, err)
	}
}

// TestTheCertificateFingerprintIsNotTheSPKIPin states the fact that made the two
// halves disagree, so nobody ever "simplifies" them back into one value. They are
// different digests of different bytes, and pinning the SPKI is the one that
// survives a certificate renewal on the same key.
func TestTheCertificateFingerprintIsNotTheSPKIPin(t *testing.T) {
	certPath, _, printedPin, printedFingerprint := generatedCert(t)

	if printedFingerprint == "" {
		t.Fatal("the startup line no longer reports the certificate fingerprint at all")
	}
	if printedPin == printedFingerprint {
		t.Fatal("the SPKI pin and the certificate fingerprint are being reported as the same value")
	}

	raw, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("no PEM block in the generated certificate")
	}
	certDigest := sha256.Sum256(block.Bytes)
	if got := hex.EncodeToString(certDigest[:]); got != printedFingerprint {
		t.Errorf("cert_fingerprint_sha256 = %q, want hex(sha256(cert DER)) = %q", printedFingerprint, got)
	}
	// And the pin must decode to a DIFFERENT 32 bytes: the SPKI digest.
	pin, err := decodeSPKIPin(printedPin)
	if err != nil {
		t.Fatalf("decode the printed pin: %v", err)
	}
	if string(pin) == string(certDigest[:]) {
		t.Error("the printed pin is the certificate digest, not the SPKI digest")
	}
}

// TestPinSHA256AcceptsTheEncodingsOperatorsActuallyHave. The digest is 32 bytes; how
// they arrived is not a security property. openssl prints hex (with colons), our own
// startup line printed hex, and the flag took only base64 — so a correct 32-byte
// SPKI digest was refused for its punctuation.
func TestPinSHA256AcceptsTheEncodingsOperatorsActuallyHave(t *testing.T) {
	_, _, printedPin, _ := generatedCert(t)
	want, err := decodeSPKIPin(printedPin)
	if err != nil {
		t.Fatalf("baseline decode: %v", err)
	}
	hexPin := hex.EncodeToString(want)
	forms := map[string]string{
		"base64 (std)":      base64.StdEncoding.EncodeToString(want),
		"base64 (raw)":      base64.RawStdEncoding.EncodeToString(want),
		"SHA256: prefixed":  "SHA256:" + base64.RawStdEncoding.EncodeToString(want),
		"sha256/ prefixed":  "sha256/" + base64.StdEncoding.EncodeToString(want),
		"hex":               hexPin,
		"hex upper":         strings.ToUpper(hexPin),
		"hex colon-grouped": colonize(hexPin),
	}
	for name, spec := range forms {
		t.Run(name, func(t *testing.T) {
			got, err := decodeSPKIPin(spec)
			if err != nil {
				t.Fatalf("decodeSPKIPin(%q) = %v, want the same 32 bytes", spec, err)
			}
			if string(got) != string(want) {
				t.Errorf("decodeSPKIPin(%q) decoded to different bytes", spec)
			}
		})
	}
}

// colonize renders hex the way `openssl x509 -fingerprint` does: AA:BB:CC…
func colonize(h string) string {
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// TestBadPinIsAUsageError. The root command's help documents exit 2 for "the
// invocation itself is wrong (unknown flag, bad arguments)". A malformed
// --pin-sha256 is precisely that, and it exited 1 — indistinguishable, to a script,
// from the engine being broken.
func TestBadPinIsAUsageError(t *testing.T) {
	for _, spec := range []string{
		"not-base64-at-all!!",
		"c2hvcnQ=",                     // valid base64, wrong length
		"0011223344556677889900aabbcc", // hex, wrong length
		"",
	} {
		t.Run(spec, func(t *testing.T) {
			_, err := decodeSPKIPin(spec)
			if err == nil {
				t.Fatalf("decodeSPKIPin(%q) accepted a value that is not a 32-byte digest", spec)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
			}
		})
	}
}

// TestTransportArgumentErrorsAreUsageErrors covers the rest of the same function:
// every refusal cliTransport makes about the CALLER'S ARGUMENTS is exit 2, not the
// generic 1. Found while fixing the pin, and the same defect.
func TestTransportArgumentErrorsAreUsageErrors(t *testing.T) {
	cases := map[string]cliTransportOptions{
		"no server": {Resolved: cliResolvedConfig{}},
		"unparseable server URL": {
			Resolved: cliResolvedConfig{Server: "://nonsense"},
		},
		"pin without https": {
			Resolved: cliResolvedConfig{Server: "http://example.test", PinSHA256: []string{"x"}},
		},
		"malformed pin": {
			Resolved: cliResolvedConfig{Server: "https://example.test", PinSHA256: []string{"nope"}},
		},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := cliTransport(opts)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
			}
		})
	}
}

// TestTheRefusalTellsTheOperatorWhereToGetTheValue. "expected a base64-encoded
// 32-byte SHA-256 digest" never said a digest OF WHAT, and the several openssl steps
// that produce it appeared nowhere in the product. A refusal that does not lead
// anywhere leaves the operator exactly where the wrong startup line left them.
func TestTheRefusalTellsTheOperatorWhereToGetTheValue(t *testing.T) {
	_, err := decodeSPKIPin("nope")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	for _, want := range []string{"SPKI", "pin_sha256"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q, so it does not lead anywhere: %s", want, msg)
		}
	}
}

// TestQuoteTrimmingTakesOnlyAMatchingPair. `strings.Trim(s, "\"'")` strips ANY number
// and ANY mix of quote characters from both ends, so `'digest"` and `""digest` were
// accepted as if they were quoted log fields. That is not an operator pasting a
// quoted value, it is a typo being silently normalised. Found by the sol-max contrast.
func TestQuoteTrimmingTakesOnlyAMatchingPair(t *testing.T) {
	_, _, pin, _ := generatedCert(t)

	for _, ok := range []string{pin, `"` + pin + `"`, `'` + pin + `'`} {
		if _, err := decodeSPKIPin(ok); err != nil {
			t.Errorf("decodeSPKIPin(%s) rejected a correctly-quoted pin: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`"` + pin,         // opening quote only
		pin + `"`,         // closing quote only
		`'` + pin + `"`,   // mismatched pair
		`""` + pin + `""`, // doubled
	} {
		if _, err := decodeSPKIPin(bad); err == nil {
			t.Errorf("decodeSPKIPin(%s) accepted malformed quoting; only ONE matching pair "+
				"is a quoted value, the rest are typos", bad)
		}
	}
}

// TestEveryBootLineThatPrintsTheTwoDigestsAlsoSaysWhichOneToPin closes the half of
// fix that did not reach.
//
// THE DEFECT (verified at cmd_serve.go:187-192 before the fix). The instruction
// lived in the `created` branch — the branch that runs ONCE in the life of a
// deployment. Every boot afterwards took the `else` and logged a bare "serving HTTPS"
// carrying cert_fingerprint_sha256 and pin_sha256 side by side, with no sentence
// saying which of the two a flag will take. Two digests and no verb: the same defect
// Fixed, printed on the path an operator actually reads, because the first boot
// scrolled past months ago and the restart is where somebody goes looking.
//
// It asserts over the SOURCE rather than over a rendered line, deliberately. The
// invariant is "no call site prints these attributes without the advice", and that is
// a property of the call sites — a test that rendered one line would prove nothing
// about the other, which is exactly how the gap opened. Reverting either branch to a
// bare message still COMPILES, and reddens here.
func TestEveryBootLineThatPrintsTheTwoDigestsAlsoSaysWhichOneToPin(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cmd_serve.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd_serve.go: %v", err)
	}

	mentionsAdvice := func(n ast.Node) bool {
		found := false
		ast.Inspect(n, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok && id.Name == "pinAdvice" {
				found = true
			}
			return !found
		})
		return found
	}

	sites := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		printsDigests := false
		for _, arg := range call.Args {
			inner, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}
			if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == "tlsTrustAttrs" {
				printsDigests = true
			}
		}
		if !printsDigests {
			return true
		}
		sites++
		if !mentionsAdvice(call.Args[0]) {
			t.Errorf("%s: this line prints cert_fingerprint_sha256 and pin_sha256 but its message "+
				"does not carry pinAdvice — it shows an operator two digests and never says which "+
				"one --pin-sha256 takes", fset.Position(call.Pos()))
		}
		return true
	})

	// A test that examined nothing would pass. Both boot paths must be present: the
	// first-boot warning and the every-other-boot info line.
	if sites != 2 {
		t.Fatalf("expected 2 call sites logging tlsTrustAttrs (first boot and every boot after), "+
			"found %d — if a path was added or removed, this test must be re-aimed, not deleted", sites)
	}
}
