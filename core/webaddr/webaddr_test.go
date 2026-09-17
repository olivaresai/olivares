// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package webaddr

import (
	"errors"
	"strings"
	"testing"
)

// The published acceptance table. Every row is a claim about what a BROWSER does
// with this value, and the differential test in browsergolden_test.go proves the
// accept rows against the installed Node WHATWG URL implementation rather than
// against this file's opinion.
var acceptTable = []struct {
	in     string
	origin string
	host   string
	port   string
	kind   Kind
}{
	{"https://panel.example.com:8443", "https://panel.example.com:8443", "panel.example.com", "8443", KindDomain},
	{"https://panel.example.com:443", "https://panel.example.com", "panel.example.com", "", KindDomain},
	{"http://localhost:80", "http://localhost", "localhost", "", KindDomain},
	{"https://panel.example.com:0443", "https://panel.example.com", "panel.example.com", "", KindDomain},
	{"HTTPS://Panel.Example.COM", "https://panel.example.com", "panel.example.com", "", KindDomain},
	{"https://panel.example.com:8443/", "https://panel.example.com:8443", "panel.example.com", "8443", KindDomain},
	{"https://panel.example.com.", "https://panel.example.com.", "panel.example.com.", "", KindDomain},
	{"https://bücher.example", "https://xn--bcher-kva.example", "xn--bcher-kva.example", "", KindDomain},
	{"https://xn--bcher-kva.example", "https://xn--bcher-kva.example", "xn--bcher-kva.example", "", KindDomain},
	{"https://my_host.example.com", "https://my_host.example.com", "my_host.example.com", "", KindDomain},
	{"https://ab--cd.example", "https://ab--cd.example", "ab--cd.example", "", KindDomain},
	{"https://olivares", "https://olivares", "olivares", "", KindDomain},
	{"https://LOCALHOST:8443", "https://localhost:8443", "localhost", "8443", KindDomain},
	{"https://localhost.", "https://localhost.", "localhost.", "", KindDomain},
	{"https://foo.localhost", "https://foo.localhost", "foo.localhost", "", KindDomain},
	{"https://127.0.0.1:8443", "https://127.0.0.1:8443", "127.0.0.1", "8443", KindIPv4},
	{"https://192.168.1.10:8443", "https://192.168.1.10:8443", "192.168.1.10", "8443", KindIPv4},
	{"https://127.1:8443", "https://127.0.0.1:8443", "127.0.0.1", "8443", KindIPv4},
	{"https://0x7f000001", "https://127.0.0.1", "127.0.0.1", "", KindIPv4},
	{"https://2130706433", "https://127.0.0.1", "127.0.0.1", "", KindIPv4},
	{"https://0177.0.0.1", "https://127.0.0.1", "127.0.0.1", "", KindIPv4},
	{"https://0", "https://0.0.0.0", "0.0.0.0", "", KindIPv4},
	{"https://0.0.0.0:8443", "https://0.0.0.0:8443", "0.0.0.0", "8443", KindIPv4},
	{"https://[2001:0DB8:0:0:0:0:0:1]:8443", "https://[2001:db8::1]:8443", "2001:db8::1", "8443", KindIPv6},
	{"https://[::1]", "https://[::1]", "::1", "", KindIPv6},
	{"https://[::]:8443", "https://[::]:8443", "::", "8443", KindIPv6},
	{"https://[::ffff:127.0.0.1]", "https://[::ffff:7f00:1]", "::ffff:7f00:1", "", KindIPv6},
	// A leading empty label is NOT refused, and this row records a measurement
	// that contradicts the study this work was built from: x/net's lookup mapping
	// with VerifyDnsLength off accepts it, and so does the installed Node WHATWG
	// implementation. Matching the browser is the whole contract of this parser.
	{"https://.panel.example.com", "https://.panel.example.com", ".panel.example.com", "", KindDomain},
	{"https://-lead.example", "https://-lead.example", "-lead.example", "", KindDomain},
	// UTS-46 MAPS SEPARATORS AND DELETES IGNORABLES, so the label structure of a
	// host legitimately changes on the way to ASCII. A guard that treated that as
	// erasure refused all four of these; the installed WHATWG implementation
	// accepts all four, and now so does this.
	{"https://a\u3002example", "https://a.example", "a.example", "", KindDomain},
	{"https://a\uff61example", "https://a.example", "a.example", "", KindDomain},
	{"https://panel\uff0eexample\uff0ecom", "https://panel.example.com", "panel.example.com", "", KindDomain},
	{"https://a.\u00ad.example", "https://a..example", "a..example", "", KindDomain},
	// A label of ONLY ignored code points legitimately maps to nothing, and a
	// browser accepts it. The sentinel probe is what tells these apart from an
	// emptied punycode payload: prepend an ASCII label and map again, and for
	// ignored-only input exactly the sentinel comes back.
	{"https://\u00ad.example", "https://.example", ".example", "", KindDomain},
	{"https://a.\u200b.example", "https://a..example", "a..example", "", KindDomain},
	{"https://trail-.example", "https://trail-.example", "trail-.example", "", KindDomain},
	// OVERFLOW FOLLOWED BY A NON-NUMERIC SUFFIX, which is the control the first
	// report got wrong: it read "non-numeric neighbour" as a separate preceding
	// LABEL, and the neighbour that matters is inside the same label, AFTER the
	// digits that overflow. A parser that stopped scanning at the first overflow
	// would call these numbers, hand them to the IPv4 parser and refuse them;
	// Node accepts all five as ordinary domains.
	{"https://panel.0xfffffffffffffffffffzz", "https://panel.0xfffffffffffffffffffzz", "panel.0xfffffffffffffffffffzz", "", KindDomain},
	{"https://panel.0xzz", "https://panel.0xzz", "panel.0xzz", "", KindDomain},
	{"https://panel.0x1g", "https://panel.0x1g", "panel.0x1g", "", KindDomain},
	{"https://99999999999999999999z.example", "https://99999999999999999999z.example", "99999999999999999999z.example", "", KindDomain},
	{"https://panel.1a", "https://panel.1a", "panel.1a", "", KindDomain},
}

// The published refusal table.
var refuseTable = []struct {
	in   string
	code Code
}{
	{"https://256.1.1.1", CodeHostNotAnAddress},
	{"https://99999999999999999999.1", CodeHostNotAnAddress},
	{"https://1.2.3.4.5", CodeHostNotAnAddress},
	{"https://4294967296", CodeHostNotAnAddress},
	// A NUMBER TOO LARGE FOR ANY ADDRESS IS STILL A NUMBER. The URL Standard's
	// IPv4-number parser is arbitrary-precision, so these go to the IPv4 parser and
	// are refused there, which makes the whole URL invalid — and that is what the
	// installed WHATWG implementation does with them. They used to be accepted as
	// DOMAINS, and two of them as valid relying parties.
	{"https://0xffffffffffffffffffff", CodeHostNotAnAddress},
	{"https://panel.0x10000000000000000", CodeHostNotAnAddress},
	{"https://panel.example.0x1ffffffffffffffff", CodeHostNotAnAddress},
	{"https://a.18446744073709551616", CodeHostNotAnAddress},
	{"https://panel.99999999999999999999", CodeHostNotAnAddress},
	{"https://panel.example.com:0", CodePortOutOfRange},
	{"https://panel.example.com:00000", CodePortOutOfRange},
	{"https://panel.example.com:70000", CodePortOutOfRange},
	{"panel.example.com:8443", CodeNoScheme},
	{"//panel.example.com", CodeNoScheme},
	{"ftp://panel.example.com", CodeUnsupportedScheme},
	{"file:///etc/olivares", CodeUnsupportedScheme},
	{"https://", CodeNoHost},
	{"https://panel.example.com/olivares", CodePathPresent},
	{"https://panel.example.com?a=1", CodeQueryPresent},
	{"https://panel.example.com#x", CodeFragmentPresent},
	{"https://ops:pw@panel.example.com", CodeUserInfoPresent},
	{"https://ops@panel.example.com", CodeUserInfoPresent},
	{"https://[fe80::1%25eth0]:8443", CodeHostZoneIdentifier},
	{"https://my host.example", CodeControlCharacter},
	{"https://panel.example.com\x00", CodeControlCharacter},
	// The punycode prefix with nothing to decode. x/net returns "" for it with a
	// NIL error — the one place it produces a host the browser refuses — so this
	// is checked explicitly, in every spelling of the separator and either case.
	{"https://xn--.example", CodeHostNotAName},
	{"https://XN--.example", CodeHostNotAName},
	{"https://xn--\u3002example", CodeHostNotAName},
	// EVERY COMPATIBILITY SPELLING OF THE PREFIX, not just the literal one. UTS-46
	// maps the fullwidth letters and the fullwidth hyphen, and deletes the soft
	// hyphen, so all of these become "xn--" with an empty payload and the label is
	// erased. Matching the four literal bytes let them through, and one of them
	// silently rewrote an explicit pin. Node refuses all five.
	{"https://\uff58\uff4e\uff0d\uff0d.example", CodeHostNotAName},
	{"https://xn\uff0d\uff0d.example", CodeHostNotAName},
	{"https://xn-\u00ad-.example", CodeHostNotAName},
	{"https://\u00adxn--.example", CodeHostNotAName},
	{"https://panel.\uff58\uff4e\uff0d\uff0d", CodeHostNotAName},
}

// T1. The whole published table, accept and refuse, asserting every canonical
// field. Mutants: drop the default-port rule; keep the zero-padded port; drop the
// domain-to-ASCII call; drop the IPv6 serializer.
func TestParseCanonicalizes(t *testing.T) {
	for _, row := range acceptTable {
		got, err := Parse("--public-url", row.in)
		if err != nil {
			t.Errorf("Parse(%q) refused: %v", row.in, err)
			continue
		}
		if got.Origin != row.origin || got.Host != row.host || got.Port != row.port || got.Kind != row.kind {
			t.Errorf("Parse(%q) = {origin:%q host:%q port:%q kind:%v}, want {origin:%q host:%q port:%q kind:%v}",
				row.in, got.Origin, got.Host, got.Port, got.Kind, row.origin, row.host, row.port, row.kind)
		}
	}
	for _, row := range refuseTable {
		got, err := Parse("--public-url", row.in)
		if err == nil {
			t.Errorf("Parse(%q) accepted %q, want refusal %d", row.in, got.Origin, row.code)
			continue
		}
		var ref *Refusal
		if !errors.As(err, &ref) {
			t.Errorf("Parse(%q) error is %T, want *Refusal", row.in, err)
			continue
		}
		if ref.Code != row.code {
			t.Errorf("Parse(%q) code = %d, want %d (%s)", row.in, ref.Code, row.code, ref.Error())
		}
	}
}

// The empty value is the zero Address and NOT an error: an operator who declares
// no address keeps today's behavior exactly. This is the compatibility guard for
// every deployment that never sets the field.
func TestAnEmptyValueIsTheZeroAddressAndNotAnError(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		got, err := Parse("--public-url", raw)
		if err != nil {
			t.Fatalf("Parse(%q) = %v, want no error", raw, err)
		}
		if !got.IsZero() {
			t.Fatalf("Parse(%q) = %+v, want the zero Address", raw, got)
		}
	}
}

// T2. Numeric hosts a browser cannot open are refused; the shorthand forms a
// browser CAN open are accepted and canonicalized. The non-firing direction is
// the point: a mutant that refuses all numeric shorthand passes the first half.
func TestNumericHostsABrowserCannotOpenAreRefused(t *testing.T) {
	for _, in := range []string{
		"https://256.1.1.1", "https://99999999999999999999.1", "https://1.2.3.4.5", "https://0x100000000",
		// The overflow class, which reached this table by way of an independent
		// review's probe rather than by anybody's reading: the last part is the
		// oversized one, so nothing earlier fails first.
		"https://0xffffffffffffffffffff", "https://panel.0x10000000000000000",
	} {
		if a, err := Parse("--public-url", in); err == nil {
			t.Errorf("Parse(%q) accepted %q; the URL Standard's IPv4 parser returns failure, which makes the whole URL invalid", in, a.Origin)
		}
	}
	for in, want := range map[string]string{
		"https://127.1":           "127.0.0.1",
		"https://0x7f000001":      "127.0.0.1",
		"https://2130706433":      "127.0.0.1",
		"https://192.168.1.10":    "192.168.1.10",
		"https://255.255.255.255": "255.255.255.255",
		// The non-firing edge of the overflow rule: 0xffffffff is the LARGEST value
		// that still fits, so a mutant that refuses anything hexadecimal goes red.
		"https://0xffffffff": "255.255.255.255",
	} {
		a, err := Parse("--public-url", in)
		if err != nil {
			t.Errorf("Parse(%q) refused: %v", in, err)
			continue
		}
		if a.Host != want || a.Kind != KindIPv4 {
			t.Errorf("Parse(%q) host = %q kind = %v, want %q ipv4", in, a.Host, a.Kind, want)
		}
	}
}

// T5. One predicate answers every address question, over every spelling of the
// same address. A mutant that reintroduces a shape predicate ("is the last label
// all digits?") disagrees on at least one row here.
func TestOnePredicateForAddresses(t *testing.T) {
	cases := []struct {
		in                          string
		isIP, loopback, unspecified bool
	}{
		{"https://0", true, false, true},
		{"https://0.0.0.0", true, false, true},
		{"https://[::]", true, false, true},
		{"https://127.1", true, true, false},
		{"https://0x7f000001", true, true, false},
		{"https://127.0.0.1", true, true, false},
		{"https://[::1]", true, true, false},
		{"https://localhost", false, true, false},
		{"https://localhost.", false, true, false},
		{"https://foo.localhost", false, true, false},
		{"https://foo.localhost.", false, true, false},
		{"https://notlocalhost", false, false, false},
		{"https://panel.example.com", false, false, false},
	}
	for _, c := range cases {
		a, err := Parse("--public-url", c.in)
		if err != nil {
			t.Errorf("Parse(%q) refused: %v", c.in, err)
			continue
		}
		if a.IsIP() != c.isIP || a.IsLoopback() != c.loopback || a.isUnspecified() != c.unspecified {
			t.Errorf("Parse(%q): ip=%v loopback=%v unspecified=%v, want %v %v %v",
				c.in, a.IsIP(), a.IsLoopback(), a.isUnspecified(), c.isIP, c.loopback, c.unspecified)
		}
	}
}

// Browser-host acceptance and RP-domain validity are DIFFERENT predicates, and
// this is the test that keeps them apart. Every row here is a host a browser
// opens; the column is whether it can be a relying party for the verifier this
// build ships. A mutant that defines CanBeRelyingParty as "Kind == KindDomain"
// turns three rows green that must be red.
func TestRelyingPartyValidityIsNotBrowserAcceptance(t *testing.T) {
	cases := map[string]bool{
		"https://panel.example.com":     true,
		"https://panel.example.com.":    true, // the root dot is not a label
		"https://xn--bcher-kva.example": true,
		"https://localhost":             true, // the installed verifier's named exception
		"https://localhost:8443":        true,
		"https://foo.localhost":         true,
		"https://my_host.example.com":   false, // a browser opens it; STD3 says it is not a valid domain
		"https://-lead.example":         false, // CheckHyphens is off for browsers, on for a valid domain
		"https://trail-.example":        false,
		"https://.panel.example.com":    false, // empty label
		"https://olivares":              false, // refused by THIS build's verifier, not by the specification
		// AND THE TRAILING DOT FLIPS IT, which looks like a bug and is a faithful
		// report of the installed verifier: its rule for a non-IP is
		// `value != "localhost" && !strings.Contains(rpid.Path, ".")`, and the root
		// dot satisfies the dot. A browser agrees — "olivares." is a valid domain
		// and is its own effective domain — so this is recorded rather than
		// second-guessed. Raised by an independent review's probe.
		"https://olivares.":    true,
		"https://127.0.0.1":    false, // the verifier accepts the string; browsers refuse it
		"https://127.1":        false,
		"https://0x7f000001":   false,
		"https://[::1]":        false,
		"https://0.0.0.0":      false,
		"https://192.168.1.10": false,
	}
	for in, want := range cases {
		a, err := Parse("--public-url", in)
		if err != nil {
			t.Errorf("Parse(%q) refused: %v", in, err)
			continue
		}
		if got := a.CanBeRelyingParty(); got != want {
			t.Errorf("Parse(%q).CanBeRelyingParty() = %v, want %v", in, got, want)
		}
	}
}

// T4. The IDNA option ORDER. MapForLookup sets StrictDomainName(true) and
// ValidateLabels(true) internally, so the two relaxing options must come after
// it. A mutant that moves them before MapForLookup compiles and silently restores
// the strict profile; these two hosts go red.
//
// The non-firing direction is the second half: relaxing the browser profile must
// NOT relax the RP-domain predicate, which is a separate profile.
func TestBrowserStrictnessProfileOrder(t *testing.T) {
	for _, in := range []string{"https://my_host.example.com", "https://ab--cd.example", "https://-lead.example"} {
		if _, err := Parse("--public-url", in); err != nil {
			t.Errorf("Parse(%q) refused: %v — a browser accepts this host", in, err)
		}
	}
	if !IsRelyingPartyDomain("panel.example.com") {
		t.Error("IsRelyingPartyDomain(panel.example.com) = false, want true")
	}
	if IsRelyingPartyDomain("my_host.example.com") {
		t.Error("IsRelyingPartyDomain(my_host.example.com) = true; STD3 is required of a VALID domain even though browsers do not apply it")
	}
}

// T3, in root's overridden form. A refusal names the field and the failure class
// and NOTHING of the value — not the query, not the fragment, not the userinfo,
// and not the host either. The old work echoed the whole value into stderr, which
// in every documented deployment is a log pipeline.
//
// The "cannot be satisfied by printing nothing" anchor is the field name and the
// remedy, not the host: printing the host was the requirement root rejected.
func TestRefusalNeverEchoesTheValue(t *testing.T) {
	secrets := []struct {
		in     string
		tokens []string
	}{
		{"https://ops:S3cretPass@vault-host.invalid:51997", []string{"ops", "S3cretPass", "vault-host.invalid", "51997"}},
		{"https://vault-host.invalid/adminpath?tk=t0kenvalue", []string{"vault-host.invalid", "adminpath", "t0kenvalue"}},
		{"https://vault-host.invalid#fragvalue", []string{"vault-host.invalid", "fragvalue"}},
		{"vault-host.invalid:8443", []string{"vault-host.invalid"}},
		{"ftp://vault-host.invalid", []string{"vault-host.invalid", "ftp"}},
		{"https://256.1.1.1", []string{"256.1.1.1"}},
		{"https://my_secret host.invalid", []string{"my_secret", "host.invalid"}},
		{"h ttps://%%%", []string{"%%%"}},
	}
	for _, c := range secrets {
		_, err := Parse("--public-url", c.in)
		if err == nil {
			t.Errorf("Parse(%q) was accepted; this row exists to check its refusal", c.in)
			continue
		}
		msg := err.Error()
		for _, tok := range c.tokens {
			if strings.Contains(msg, tok) {
				t.Errorf("Parse(%q) refusal echoes %q: %s", c.in, tok, msg)
			}
		}
		if !strings.Contains(msg, "--public-url") {
			t.Errorf("Parse(%q) refusal does not name the field: %s", c.in, msg)
		}
		if !strings.Contains(msg, "olivares.example.com") && !strings.Contains(msg, "terminates TLS") &&
			!strings.Contains(msg, "1-65535") && !strings.Contains(msg, "255") && !strings.Contains(msg, "forbidden") &&
			!strings.Contains(msg, "control character") {
			t.Errorf("Parse(%q) refusal carries no remedy, so it could be satisfied by saying nothing: %s", c.in, msg)
		}
	}
}

// The *url.Error trap, named on its own because removing the value from the
// format string does NOT close it: (*url.Error).Error() embeds the URL. A mutant
// that wraps the parse failure instead of mapping it to a Code goes red here and
// nowhere else.
func TestAURLErrorIsNeverWrapped(t *testing.T) {
	const raw = "ht\ttps://%zz:S3cretPass@leak-host.invalid"
	_, err := Parse("OLIVARES_PUBLIC_URL", strings.ReplaceAll(raw, "\t", ""))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, tok := range []string{"S3cretPass", "leak-host.invalid", "%zz", "parse"} {
		if strings.Contains(err.Error(), tok) {
			t.Fatalf("refusal carries library or value text %q: %s", tok, err.Error())
		}
	}
	var ref *Refusal
	if !errors.As(err, &ref) || ref.Field != "OLIVARES_PUBLIC_URL" {
		t.Fatalf("refusal = %#v, want a *Refusal naming the field it read", err)
	}
}

// A bind spelling is not an address, and a bare port is not an empty host. The
// most-documented install path binds ":8443", and the panel printed
// "https://:8443" — a URL no browser can open — as the console address.
//
// Non-firing direction: a real host is NOT called a wildcard.
func TestFromListenNamesAWildcardBind(t *testing.T) {
	for _, listen := range []string{":8443", "0.0.0.0:8443", "[::]:8443", "0:8443"} {
		a, wildcard := FromListen(listen, "https")
		if !wildcard {
			t.Errorf("FromListen(%q) wildcard = false, want true", listen)
		}
		if a.Origin != "https://localhost:8443" {
			t.Errorf("FromListen(%q) origin = %q, want https://localhost:8443", listen, a.Origin)
		}
	}
	for listen, want := range map[string]string{
		"127.0.0.1:8443":         "https://127.0.0.1:8443",
		"panel.example.com:8443": "https://panel.example.com:8443",
		"[::1]:8443":             "https://[::1]:8443",
		"127.0.0.1:443":          "https://127.0.0.1",
	} {
		a, wildcard := FromListen(listen, "https")
		if wildcard {
			t.Errorf("FromListen(%q) called a concrete bind a wildcard", listen)
		}
		if a.Origin != want {
			t.Errorf("FromListen(%q) origin = %q, want %q", listen, a.Origin, want)
		}
	}
	if a, _ := FromListen("127.0.0.1:8080", "http"); a.Origin != "http://127.0.0.1:8080" {
		t.Errorf("FromListen under --insecure = %q, want http://127.0.0.1:8080", a.Origin)
	}
	// A panel must never be able to stop a server from starting.
	if a, _ := FromListen("not a bind", "https"); a.Origin == "" {
		t.Error("FromListen returned an empty origin for an unclassifiable bind")
	}
}

// WithHost is the "same console, on localhost, on the port it is actually served
// on" move. A mutant that hard-codes 8443, or that drops the scheme, goes red.
func TestWithHostKeepsSchemeAndPort(t *testing.T) {
	a, err := Parse("--public-url", "https://127.0.0.1:8477")
	if err != nil {
		t.Fatal(err)
	}
	got := a.WithHost("localhost")
	if got.Origin != "https://localhost:8477" || got.Kind != KindDomain {
		t.Fatalf("WithHost = {%q, %v}, want https://localhost:8477 domain", got.Origin, got.Kind)
	}
	if unchanged := a.WithHost("not a host"); unchanged.Origin != a.Origin {
		t.Fatalf("WithHost with a refused host = %q, want the address unchanged", unchanged.Origin)
	}
}

// PotentiallyTrustworthy is only ever used to SUPPRESS a warning, so its shape
// matters: https is always trustworthy, http only on the loopback families.
func TestPotentiallyTrustworthy(t *testing.T) {
	cases := map[string]bool{
		"https://panel.example.com": true,
		"https://127.0.0.1":         true,
		"http://localhost:8443":     true,
		"http://127.0.0.1:8443":     true,
		"http://[::1]:8443":         true,
		"http://foo.localhost":      true,
		"http://panel.example.com":  false,
		"http://192.168.1.10:8443":  false,
		"http://0.0.0.0:8443":       false,
	}
	for in, want := range cases {
		a, err := Parse("--public-url", in)
		if err != nil {
			t.Errorf("Parse(%q) refused: %v", in, err)
			continue
		}
		if got := a.PotentiallyTrustworthy(); got != want {
			t.Errorf("Parse(%q).PotentiallyTrustworthy() = %v, want %v", in, got, want)
		}
	}
}
