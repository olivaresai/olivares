// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/webaddr"
)

// declaredConsoleAddress builds the panel input for a console reached at rawURL.
// The announce tests used to pass a bare string; the value is typed now so the
// panel cannot re-parse a URL it was already handed.
func declaredConsoleAddress(t *testing.T, rawURL string, insecure bool) consoleAddress {
	t.Helper()
	a, err := webaddr.Parse("--public-url", rawURL)
	if err != nil {
		t.Fatalf("webaddr.Parse(%q): %v", rawURL, err)
	}
	// The banner tests describe the DEFAULT deployment: nothing pinned, so the
	// relying party is derived per request and the advice is the advice that plan
	// justifies. A test that wants another plan builds it explicitly.
	return resolveConsoleAddress(a, "", insecure).withPlan(webAuthnPlan{Source: "per-request"})
}

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// envOf is env, named for the rows that build their map inline.
func envOf(pairs map[string]string) func(string) string { return env(pairs) }

// PRECEDENCE, including the half that is easy to get wrong. A flag that is
// PRESENT wins over the environment even when its value is empty — that is how an
// operator clears an OLIVARES_PUBLIC_URL baked into a systemd environment file
// without editing the file. A mutant that tests the value's emptiness instead of
// the flag's presence passes every other row here and fails this one.
func TestPublicAddressPrecedence(t *testing.T) {
	t.Parallel()
	const fromEnv = "https://env.example.com"
	const fromFlag = "https://flag.example.com"
	cases := []struct {
		name       string
		flag       string
		flagSet    bool
		env        string
		wantOrigin string
		wantSource publicAddrSource
	}{
		{"nothing declared", "", false, "", "", publicAddrUnset},
		{"environment only", "", false, fromEnv, fromEnv, publicAddrEnv},
		{"flag only", fromFlag, true, "", fromFlag, publicAddrFlag},
		{"flag beats environment", fromFlag, true, fromEnv, fromFlag, publicAddrFlag},
		{"explicit empty flag CLEARS the environment", "", true, fromEnv, "", publicAddrFlag},
		{"a blank environment value declares nothing", "", false, "   ", "", publicAddrUnset},
		{"an unset flag with an empty default does not clear", "", false, fromEnv, fromEnv, publicAddrEnv},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, source, err := resolvePublicAddr(c.flag, c.flagSet, env(map[string]string{publicURLEnv: c.env}))
			if err != nil {
				t.Fatalf("resolvePublicAddr: %v", err)
			}
			if addr.Origin != c.wantOrigin {
				t.Errorf("origin = %q, want %q", addr.Origin, c.wantOrigin)
			}
			if source != c.wantSource {
				t.Errorf("source = %v, want %v", source, c.wantSource)
			}
		})
	}
}

// A refused value never reaches the process log, from either source. The field
// name does, because that is what the operator has to go and edit.
func TestARefusedPublicAddressNamesTheSourceAndNotTheValue(t *testing.T) {
	t.Parallel()
	const bad = "https://ops:S3cretPass@panel.invalid/admin?tk=t0kenvalue"
	if _, _, err := resolvePublicAddr(bad, true, env(nil)); err == nil {
		t.Fatal("the flag accepted a value carrying credentials")
	} else {
		for _, tok := range []string{"S3cretPass", "panel.invalid", "t0kenvalue", "admin"} {
			if strings.Contains(err.Error(), tok) {
				t.Errorf("the flag refusal echoes %q: %s", tok, err)
			}
		}
		if !strings.Contains(err.Error(), "--public-url") {
			t.Errorf("the flag refusal does not name --public-url: %s", err)
		}
	}
	if _, _, err := resolvePublicAddr("", false, env(map[string]string{publicURLEnv: bad})); err == nil {
		t.Fatal("the environment accepted a value carrying credentials")
	} else if !strings.Contains(err.Error(), publicURLEnv) {
		t.Errorf("the environment refusal does not name %s: %s", publicURLEnv, err)
	} else if strings.Contains(err.Error(), "S3cretPass") {
		t.Errorf("the environment refusal echoes the password: %s", err)
	}
}

// AN EXPLICIT PAIR IS USED OR THE ENGINE REFUSES TO START. It is never quietly
// replaced by a derived relying party, and "half of it" is not a configuration
// this engine completes on the operator's behalf.
func TestAnExplicitRelyingPartyNeverFallsBackSilently(t *testing.T) {
	t.Parallel()
	declared, err := webaddr.Parse("--public-url", "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	refuse := []struct {
		name string
		env  map[string]string
	}{
		{"an ID with no origins", map[string]string{"OLIVARES_WEBAUTHN_RPID": "panel.example.com"}},
		{"origins with no ID", map[string]string{"OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com"}},
		{"an IP as the relying party", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "10.1.2.3", "OLIVARES_WEBAUTHN_ORIGINS": "https://10.1.2.3:8443"}},
		{"an IPv4 shorthand as the relying party", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "127.1", "OLIVARES_WEBAUTHN_ORIGINS": "https://127.1:8443"}},
		{"a single-label name this build's verifier refuses", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "olivares", "OLIVARES_WEBAUTHN_ORIGINS": "https://olivares:8443"}},
		{"an ID that is not a valid domain", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "my_host.example.com", "OLIVARES_WEBAUTHN_ORIGINS": "https://my_host.example.com"}},
		{"an origin carrying a path", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "panel.example.com", "OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com/console"}},
		{"an ID with a scheme", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "https://panel.example.com", "OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com"}},
		// PRESENCE IS THE RAW VALUE, not what survives parsing. A key set to
		// separators names no origin, and reading it as "unset" would fall back to a
		// derived relying party from a TYPO — the exact silent substitution this
		// whole rule exists to stop.
		{"origins that are separators only", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "panel.example.com", "OLIVARES_WEBAUTHN_ORIGINS": ",, ,"}},
		{"separators-only origins with no ID", map[string]string{
			"OLIVARES_WEBAUTHN_ORIGINS": ",,"}},
	}
	for _, c := range refuse {
		t.Run(c.name, func(t *testing.T) {
			plan, err := resolveWebAuthnRP(declared, publicAddrFlag, env(c.env))
			if err == nil {
				t.Fatalf("accepted an unusable explicit pair: %+v", plan)
			}
			if !strings.Contains(err.Error(), "OLIVARES_WEBAUTHN_") {
				t.Errorf("the refusal does not name the keys to edit: %s", err)
			}
			for _, secret := range []string{"/console", "10.1.2.3"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the refusal echoes a configured value %q: %s", secret, err)
				}
			}
		})
	}

	// The non-firing direction for PRESENCE: both keys truly empty is not a
	// declaration, and a display name on its own pins nothing — so neither refuses
	// a boot, and the relying party comes from the declared address as usual.
	for _, name := range []string{"both empty", "blank strings", "only a display name"} {
		env := map[string]string{}
		switch name {
		case "blank strings":
			env = map[string]string{"OLIVARES_WEBAUTHN_RPID": "  ", "OLIVARES_WEBAUTHN_ORIGINS": "   "}
		case "only a display name":
			env = map[string]string{"OLIVARES_WEBAUTHN_RP_NAME": "Acme"}
		}
		plan, err := resolveWebAuthnRP(declared, publicAddrFlag, envOf(env))
		if err != nil {
			t.Errorf("%s refused a boot: %v", name, err)
			continue
		}
		if plan.RP.ID != "panel.example.com" {
			t.Errorf("%s: rp id = %q, want the address-derived panel.example.com", name, plan.RP.ID)
		}
	}

	// The non-firing direction, and it is the one that keeps the rule honest: a
	// valid pin still wins, a registrable PARENT domain is still supported, and
	// several origins are still supported.
	accept := []struct {
		name        string
		env         map[string]string
		wantID      string
		wantOrigins []string
	}{
		{"exact host", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "panel.example.com", "OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com"},
			"panel.example.com", []string{"https://panel.example.com"}},
		{"a registrable parent domain over several origins", map[string]string{
			"OLIVARES_WEBAUTHN_RPID":    "example.com",
			"OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com, https://ops.eu.example.com:8443 ,https://example.com:443"},
			"example.com", []string{"https://panel.example.com", "https://ops.eu.example.com:8443", "https://example.com"}},
		{"a RELATED origin, which WebAuthn Level 3 allows and this engine no longer refuses", map[string]string{
			"OLIVARES_WEBAUTHN_RPID":    "panel.example.com",
			"OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com,https://elsewhere.test"},
			"panel.example.com", []string{"https://panel.example.com", "https://elsewhere.test"}},
		{"localhost, the verifier's named exception", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "localhost", "OLIVARES_WEBAUTHN_ORIGINS": "https://localhost:8443"},
			"localhost", []string{"https://localhost:8443"}},
		{"case and default ports are canonicalized to what a browser sends", map[string]string{
			"OLIVARES_WEBAUTHN_RPID": "Panel.Example.COM", "OLIVARES_WEBAUTHN_ORIGINS": "HTTPS://Panel.Example.COM:443"},
			"panel.example.com", []string{"https://panel.example.com"}},
	}
	for _, c := range accept {
		t.Run(c.name, func(t *testing.T) {
			plan, err := resolveWebAuthnRP(declared, publicAddrFlag, env(c.env))
			if err != nil {
				t.Fatalf("refused a usable explicit pair: %v", err)
			}
			if plan.Unusable {
				t.Fatal("a valid explicit pair was marked unusable")
			}
			if plan.RP.ID != c.wantID {
				t.Errorf("rp id = %q, want %q", plan.RP.ID, c.wantID)
			}
			if strings.Join(plan.RP.Origins, "|") != strings.Join(c.wantOrigins, "|") {
				t.Errorf("origins = %v, want %v", plan.RP.Origins, c.wantOrigins)
			}
			if plan.RP.DisplayName == "" {
				t.Error("an authenticator would be shown an empty relying-party name")
			}
		})
	}
}

// The four precedence outcomes when no pin is set. The last one is the
// compatibility guard for every deployment that configures nothing at all, and
// the third is the outcome root's adjudication turns on: a declared address that
// cannot be a relying party is an EXPLICIT unavailable, not permission to pick an
// authority out of a request header.
func TestRelyingPartyFromTheDeclaredAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		declared     string
		wantID       string
		wantOrigin   string
		wantUnusable bool
	}{
		{"https://panel.example.com", "panel.example.com", "https://panel.example.com", false},
		{"https://panel.example.com:8443", "panel.example.com", "https://panel.example.com:8443", false},
		{"https://localhost:8443", "localhost", "https://localhost:8443", false},
		{"https://192.168.1.10:8443", "", "", true},
		{"https://127.0.0.1:8443", "", "", true},
		{"https://127.1:8443", "", "", true},
		{"https://[2001:db8::1]:8443", "", "", true},
		{"https://0.0.0.0:8443", "", "", true},
		{"https://olivares:8443", "", "", true},
		{"https://my_host.example.com", "", "", true},
		{"", "", "", false}, // nothing declared: per-request derivation, unchanged
	}
	for _, c := range cases {
		addr, err := webaddr.Parse("--public-url", c.declared)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.declared, err)
		}
		plan, err := resolveWebAuthnRP(addr, publicAddrFlag, env(nil))
		if err != nil {
			t.Fatalf("resolveWebAuthnRP(%q): %v", c.declared, err)
		}
		if plan.Unusable != c.wantUnusable {
			t.Errorf("%q: unusable = %v, want %v", c.declared, plan.Unusable, c.wantUnusable)
		}
		if plan.RP.ID != c.wantID {
			t.Errorf("%q: rp id = %q, want %q", c.declared, plan.RP.ID, c.wantID)
		}
		if c.wantOrigin != "" && (len(plan.RP.Origins) != 1 || plan.RP.Origins[0] != c.wantOrigin) {
			t.Errorf("%q: origins = %v, want [%s]", c.declared, plan.RP.Origins, c.wantOrigin)
		}
	}
	// The compatibility guard, stated on its own so nobody has to read a table to
	// find it: nothing configured must stay the zero value, which is what makes
	// core/api derive the relying party per request exactly as it did before.
	plan, err := resolveWebAuthnRP(webaddr.Address{}, publicAddrFlag, env(nil))
	if err != nil || plan.Unusable || plan.RP.ID != "" || len(plan.RP.Origins) != 0 {
		t.Fatalf("an unconfigured deployment no longer derives per request: %+v err=%v", plan, err)
	}
}

// A valid pin beats a declared address, and the two disagreeing is LEGITIMATE:
// pinning a registrable parent domain while serving a subdomain is the documented
// way to keep credentials working across panel host names.
func TestAValidPinBeatsTheDeclaredAddress(t *testing.T) {
	t.Parallel()
	addr, err := webaddr.Parse("--public-url", "https://panel.example.com:8443")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolveWebAuthnRP(addr, publicAddrFlag, env(map[string]string{
		"OLIVARES_WEBAUTHN_RPID":    "example.com",
		"OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com:8443",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.RP.ID != "example.com" {
		t.Fatalf("rp id = %q, want the pinned example.com", plan.RP.ID)
	}
}

// GOVERNED-RAG RESOLVES ONCE, AND BEFORE IT WRITES ANYTHING.
//
// It used to read the setting twice — once through runEngine and once inside the
// bootstrap-script generator, which threw the refusal away. Without --start that
// was silent: exit 0, a script pointing at the bind, and no mention of the
// address the operator had declared. An independent review measured both halves.
func TestGovernedRAGRefusesADeclaredAddressBeforeWritingAnything(t *testing.T) {
	out := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "install")
	opts := quickstartGovernedRAGOptions{
		listen: "127.0.0.1:8443", grpcListen: "127.0.0.1:8444",
		dataDir: dataDir, outDir: out,
		source: "s3", sourceName: "n", credentialRef: "store:s3/read", bucket: "b",
		kbName: "kb", agentRef: "a", agentName: "A", identityRef: "agent:a",
		clearance: "confidential", groupRef: "group:engineering",
		publicURL: "https://panel.example.com/admin", publicURLSet: true,
	}
	err := runQuickstartGovernedRAG(context.Background(), io.Discard, opts)
	if err == nil {
		t.Fatal("a declared address carrying a path was accepted")
	}
	if !strings.Contains(err.Error(), "--public-url") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
	if strings.Contains(err.Error(), "/admin") || strings.Contains(err.Error(), "panel.example.com") {
		t.Errorf("the refusal echoes the value: %v", err)
	}
	// The observable that makes this about ORDER and not just about refusing:
	// nothing may be on disk yet.
	if entries, rerr := os.ReadDir(out); rerr == nil && len(entries) > 0 {
		t.Errorf("wrote %d file(s) before refusing: %v", len(entries), entries)
	}
	if _, serr := os.Stat(dataDir); !os.IsNotExist(serr) {
		t.Errorf("created the data directory before refusing (stat err = %v)", serr)
	}
}

// The non-firing directions: a VALID declared address reaches the generated
// script, an explicit empty flag clears a set environment, and a valid flag
// overrides an invalid environment value instead of inheriting its refusal.
func TestGovernedRAGBootstrapURLFollowsTheResolvedAddress(t *testing.T) {
	base := quickstartGovernedRAGOptions{listen: "127.0.0.1:8443", tenantID: "t"}

	declared := base
	declared.publicAddr = mustAddr(t, "https://panel.example.com:8443")
	if got := governedRAGBaseURL(declared); got != "https://panel.example.com:8443" {
		t.Errorf("declared address = %q, want the declared one", got)
	}

	// Explicit empty: nothing declared, so the script names an openable form of
	// the bind rather than a URL curl cannot resolve.
	if got := governedRAGBaseURL(base); got != "https://127.0.0.1:8443" {
		t.Errorf("undeclared = %q, want the bind", got)
	}

	t.Run("a valid flag overrides an invalid environment value", func(t *testing.T) {
		t.Setenv(publicURLEnv, "https://ops:S3cret@bad.invalid/admin")
		addr, source, err := resolvePublicAddr("https://good.example.com", true, osGetenv)
		if err != nil {
			t.Fatalf("a valid flag inherited the environment's refusal: %v", err)
		}
		if addr.Origin != "https://good.example.com" || source != publicAddrFlag {
			t.Fatalf("addr = %q source = %v, want the flag's value", addr.Origin, source)
		}
	})

	t.Run("an explicit empty flag clears a set environment", func(t *testing.T) {
		t.Setenv(publicURLEnv, "https://from-the-env.example.com")
		addr, source, err := resolvePublicAddr("", true, osGetenv)
		if err != nil {
			t.Fatal(err)
		}
		if !addr.IsZero() || source != publicAddrFlag {
			t.Fatalf("addr = %q source = %v, want cleared by the flag", addr.Origin, source)
		}
	})
}

func mustAddr(t *testing.T, raw string) webaddr.Address {
	t.Helper()
	a, err := webaddr.Parse("--public-url", raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return a
}

// THE BOOT LINE NAMES THE INPUT THAT ACTUALLY SUPPLIED THE ADDRESS. It used to
// say OLIVARES_PUBLIC_URL whichever way the value arrived, so an operator
// looking for where a value came from could be sent to an environment file when
// the answer was a flag on the command line.
func TestThePlanNamesTheInputThatSuppliedTheAddress(t *testing.T) {
	t.Parallel()
	addr := mustAddr(t, "https://panel.example.com")
	for _, c := range []struct {
		source publicAddrSource
		want   string
	}{
		{publicAddrFlag, "--public-url"},
		{publicAddrEnv, publicURLEnv},
	} {
		plan, err := resolveWebAuthnRP(addr, c.source, env(nil))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Source != c.want {
			t.Errorf("source = %q, want %q", plan.Source, c.want)
		}
	}
	// An UNUSABLE declared address reports its source too: that is the line the
	// operator needs most, because it is the one that leads to a 503.
	unusable, err := resolveWebAuthnRP(mustAddr(t, "https://10.0.0.7:8443"), publicAddrFlag, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !unusable.Unusable || unusable.Source != "--public-url" {
		t.Errorf("unusable plan = %+v, want the flag named", unusable)
	}
	// A PIN still names the pin, not the address that lost to it.
	pinned, err := resolveWebAuthnRP(addr, publicAddrFlag, env(map[string]string{
		"OLIVARES_WEBAUTHN_RPID": "example.com", "OLIVARES_WEBAUTHN_ORIGINS": "https://panel.example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Source != "OLIVARES_WEBAUTHN_RPID" {
		t.Errorf("pinned source = %q, want the pin", pinned.Source)
	}
}

// EFFECTIVE CONFIGURATION DOES NOT ECHO A REFUSED DECLARED ADDRESS.
//
// `config effective` reports the REQUESTED environment of this host. It is a
// separate invocation from any running engine and cannot know its flags, so the
// value it shows may have been overridden — but whatever it shows must be safe,
// and a refused address is precisely the class that can carry a credential in
// its userinfo or a token in its query.
func TestEffectiveConfigNeverEchoesARefusedDeclaredAddress(t *testing.T) {
	t.Parallel()
	for _, refused := range []string{
		"https://ops:S3cretPass@panel.invalid/admin?tk=t0kenvalue",
		"https://panel.example.com:0",
		"not-a-url-at-all",
		"https://256.1.1.1",
	} {
		got := redactEffectiveConfigValue(publicURLEnv, refused)
		if got != redactedConfigValue {
			t.Errorf("a refused value was rendered as %q, want %q", got, redactedConfigValue)
		}
		for _, leak := range []string{"S3cretPass", "t0kenvalue", "panel.invalid", "256.1.1.1"} {
			if strings.Contains(got, leak) {
				t.Errorf("rendered value leaks %q: %s", leak, got)
			}
		}
	}
	// The non-firing direction: an ACCEPTED value is still shown, canonicalized —
	// otherwise this would be a redaction that hides working configuration.
	for raw, want := range map[string]string{
		"https://panel.example.com":       "https://panel.example.com",
		"HTTPS://Panel.Example.COM:443":   "https://panel.example.com",
		"https://panel.example.com:8443/": "https://panel.example.com:8443",
		"":                                "",
	} {
		if got := redactEffectiveConfigValue(publicURLEnv, raw); got != want {
			t.Errorf("redactEffectiveConfigValue(%q) = %q, want %q", raw, got, want)
		}
	}
	// And the neighbouring keys are untouched by this rule.
	if got := redactEffectiveConfigValue("OLIVARES_LISTEN", "0.0.0.0:8443"); got != "0.0.0.0:8443" {
		t.Errorf("an unrelated key was rewritten: %q", got)
	}
}

// C5. THE SOURCE TRAVELS WITH THE ADDRESS THROUGH governed-rag --start.
//
// The serveOptions literal set publicAddrResolved and not the source, so
// runEngine inferred it from the environment AFTER a flag had already decided —
// and the boot line said OLIVARES_PUBLIC_URL for a value that came from
// --public-url. This calls the production constructor the --start path uses,
// not a second copy of its fields. A helper-only test never showed the defect.
func TestGovernedRAGCarriesTheResolvedSourceIntoServeOptions(t *testing.T) {
	const (
		listen = "127.0.0.1:8443"
		grpc   = "127.0.0.1:8444"
		data   = "governed-rag-data"
	)
	for _, c := range []struct {
		name       string
		flag       string
		flagSet    bool
		env        string
		wantAddr   string
		wantSource publicAddrSource
	}{
		{"a flag names the flag", "https://panel.example.com", true, "", "https://panel.example.com", publicAddrFlag},
		{"a flag beating an environment still names the flag", "https://panel.example.com", true, "https://from-env.example.com", "https://panel.example.com", publicAddrFlag},
		{"an explicit EMPTY flag is still the flag", "", true, "https://from-env.example.com", "", publicAddrFlag},
		{"the environment alone names the environment", "", false, "https://from-env.example.com", "https://from-env.example.com", publicAddrEnv},
		{"nothing declared names nothing", "", false, "", "", publicAddrUnset},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(publicURLEnv, c.env)
			opts := quickstartGovernedRAGOptions{
				listen: listen, grpcListen: grpc, dataDir: data,
				publicURL: c.flag, publicURLSet: c.flagSet,
			}
			addr, source, err := resolvePublicAddr(opts.publicURL, opts.publicURLSet, osGetenv)
			if err != nil {
				t.Fatal(err)
			}
			opts.publicAddr, opts.publicAddrSource = addr, source
			serve := governedRAGServeOptions(opts)
			if serve.publicAddr.Origin != c.wantAddr {
				t.Errorf("address = %q, want %q", serve.publicAddr.Origin, c.wantAddr)
			}
			if serve.publicAddrSource != c.wantSource {
				t.Errorf("source = %v, want %v — runEngine would report the wrong input", serve.publicAddrSource, c.wantSource)
			}
			if !serve.publicAddrResolved {
				t.Error("publicAddrResolved is false — runEngine would re-read the environment")
			}
			if serve.listen != listen || serve.grpcListen != grpc || serve.dataDir != data {
				t.Errorf("listen/grpc/data = %q/%q/%q, want %q/%q/%q",
					serve.listen, serve.grpcListen, serve.dataDir, listen, grpc, data)
			}
			if serve.engine != "sqlite" || serve.checkpointInterval != time.Hour {
				t.Errorf("engine/checkpoint = %q/%s, want sqlite/%s",
					serve.engine, serve.checkpointInterval, time.Hour)
			}
			// And the plan built from it names the same input.
			plan, err := resolveWebAuthnRP(serve.publicAddr, serve.publicAddrSource, env(nil))
			if err != nil {
				t.Fatal(err)
			}
			want := c.wantSource.String()
			if serve.publicAddr.IsZero() {
				want = "per-request"
			}
			if plan.Source != want {
				t.Errorf("plan source = %q, want %q", plan.Source, want)
			}
		})
	}
}

// N4. THE ENGINE'S PROJECTION REPORTS THE RUNNING INVOCATION, NOT THE ENVIRONMENT.
//
// `config effective` is a different process and cannot observe a running one, so
// its view stays the redacted REQUESTED environment. The in-engine callback is
// not in that position: it holds the address this process resolved at startup. It
// used to show the environment anyway, so an operator reading the console saw a
// value a flag had overridden — or one an explicit empty flag had cleared.
//
// The address is never re-read after startup, which is what the last case proves:
// mutating the environment afterwards does not move the row.
func TestTheEngineProjectionShowsTheRunningInvocation(t *testing.T) {
	find := func(entries []api.EffectiveConfigEntry) (string, string, bool) {
		for _, e := range entries {
			if e.Key == publicURLEnv {
				return e.Value, e.Source, true
			}
		}
		return "", "", false
	}
	for _, c := range []struct {
		name       string
		env        string
		flag       string
		flagSet    bool
		wantValue  string
		wantSource string
	}{
		{"a flag overriding an INVALID environment value", "https://ops:S3cretPass@bad.invalid/admin", "https://panel.example.com", true, "https://panel.example.com", "--public-url"},
		{"a flag overriding a VALID environment value", "https://from-env.example.com", "https://panel.example.com", true, "https://panel.example.com", "--public-url"},
		{"an explicit EMPTY flag clearing a set environment", "https://from-env.example.com", "", true, "", "--public-url"},
		{"environment only", "https://from-env.example.com", "", false, "https://from-env.example.com", publicURLEnv},
		{"nothing declared", "", "", false, "", "unset"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(publicURLEnv, c.env)
			addr, source, err := resolvePublicAddr(c.flag, c.flagSet, osGetenv)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			running := resolvedPublicAddr{addr: addr, source: source, known: true}
			value, src, ok := find(effectiveConfigEntriesFor(os.Environ(), osGetenv, running))
			if !ok {
				t.Fatalf("the row is absent entirely")
			}
			if value != c.wantValue || src != c.wantSource {
				t.Fatalf("row = (%q, %q), want (%q, %q)", value, src, c.wantValue, c.wantSource)
			}
			// No raw secret from the environment, ever.
			for _, leak := range []string{"S3cretPass", "bad.invalid"} {
				if strings.Contains(value, leak) {
					t.Errorf("the row leaks %q", leak)
				}
			}
			// A LATER environment mutation does not move it: the address is not
			// re-read after startup.
			t.Setenv(publicURLEnv, "https://mutated-after-boot.example.com")
			value2, src2, _ := find(effectiveConfigEntriesFor(os.Environ(), osGetenv, running))
			if value2 != c.wantValue || src2 != c.wantSource {
				t.Errorf("a later environment change moved the row to (%q, %q)", value2, src2)
			}
		})
	}

	// The standalone command keeps its requested-environment meaning, redacted.
	t.Setenv(publicURLEnv, "https://ops:S3cretPass@bad.invalid/admin")
	value, source, ok := find(effectiveConfigEntries(os.Environ(), osGetenv))
	if !ok {
		t.Fatal("the standalone projection dropped the row")
	}
	if value != redactedConfigValue || source != "env" {
		t.Fatalf("standalone row = (%q, %q), want the redacted environment view", value, source)
	}
}
