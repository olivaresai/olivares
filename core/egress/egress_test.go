// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress_test

import (
	"net"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/egress"
)

// TestCanonicalHostCollapsesTheSpellingsOfOneName. Each group is one destination
// written several ways; an allow-list that compares raw text authorizes one member
// of a group and refuses the others, or worse, authorizes a name it never approved.
func TestCanonicalHostCollapsesTheSpellingsOfOneName(t *testing.T) {
	groups := [][]string{
		{"example.com", "EXAMPLE.COM", "Example.Com", "example.com.", " example.com "},
		{"a.example.com", "A.Example.COM."},
		// One IPv6 address, four spellings, including the bracketed URL form.
		{"::1", "0:0:0:0:0:0:0:1", "0000:0000:0000:0000:0000:0000:0000:0001", "[::1]"},
		{"127.0.0.1", "127.0.0.1"},
	}
	for _, g := range groups {
		want, err := egress.CanonicalHost(g[0])
		if err != nil {
			t.Fatalf("%q: %v", g[0], err)
		}
		for _, spelling := range g[1:] {
			got, err := egress.CanonicalHost(spelling)
			if err != nil {
				t.Errorf("%q: %v", spelling, err)
				continue
			}
			if got != want {
				t.Errorf("%q canonicalized to %q, want %q (same destination)", spelling, got, want)
			}
		}
	}
}

// TestWhatMatchedIsWhatIsDialed is the invariant that makes case folding safe, and
// it is a sharper statement than "never fold".
//
// UTS-46 lookup mapping folds U+017F (LATIN SMALL LETTER LONG S) onto "s" and the
// KELVIN SIGN onto "k" — the same characters Go's strings.EqualFold folds — and it
// is RIGHT to do so, because that is what a conforming resolver does with them. The
// hole is not folding; it is folding for the CHECK and then dialing the ORIGINAL
// string, so the allow-list decides about one name and the connection goes to
// another.
//
// This asserts the closure: whatever spelling arrives, the destination that comes
// out is the canonical one, and it is that canonical host a caller resolves and
// dials. There is no path by which the raw bytes reach a resolver.
func TestWhatMatchedIsWhatIsDialed(t *testing.T) {
	approved, err := egress.CanonicalHost("sink.example.com")
	if err != nil {
		t.Fatal(err)
	}
	p := egress.Policy{InForce: true, Allow: []egress.Rule{{Host: approved}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{
		"sink.example.com", "SINK.EXAMPLE.COM", "sink.example.com.",
		"ſink.example.com", // U+017F, folded onto "s" by UTS-46
		"Keep.example.com", // KELVIN SIGN, folded onto "k" — a DIFFERENT host
	} {
		d, err := egress.ParseDestination("https://" + spelling + "/events")
		if err != nil {
			continue // refusing outright is also a correct answer
		}
		permitted := egress.Evaluate(p, d, []net.IP{net.ParseIP("93.184.216.34")}).Permitted
		// The decisive property: a spelling is permitted only if it CANONICALIZED to
		// the approved host, and in that case the host that will be resolved and
		// dialed is that same canonical string — never the spelling that arrived.
		if permitted != (d.Host == approved) {
			t.Errorf("%q: permitted=%v but canonical host is %q (approved %q)",
				spelling, permitted, d.Host, approved)
		}
		// A permitted destination must carry no byte outside the canonical alphabet:
		// no upper case, no non-ASCII, nothing the resolver would see differently.
		for i := 0; i < len(d.Host); i++ {
			if c := d.Host[i]; c >= 0x80 || (c >= 'A' && c <= 'Z') {
				t.Errorf("%q: permitted destination kept a non-canonical byte in %q", spelling, d.Host)
				break
			}
		}
	}
}

// TestCanonicalHostRefusesSmuggledBytes: a control character or a space inside a
// host is how one string gets past a check while a different one reaches a resolver.
func TestCanonicalHostRefusesSmuggledBytes(t *testing.T) {
	for _, bad := range []string{
		"", " ", "exa mple.com", "example.com\r", "example.com\n", "example.com\x00",
		"exam\tple.com", ".",
		// The legacy inet_aton spellings. net.ParseIP has refused these since Go 1.17,
		// so they arrive looking like ordinary domain names — while glibc's
		// getaddrinfo still resolves several of them to 127.0.0.1. Authorizing one as
		// an opaque hostname is how an allow-list approves a string whose resolution
		// is a loopback address.
		"127.000.000.001", "2130706433", "0177.0.0.1", "127.1",
	} {
		if got, err := egress.CanonicalHost(bad); err == nil {
			t.Errorf("accepted %q as %q, want refusal", bad, got)
		}
	}
}

// TestHostRuleMatchesOnALabelBoundary is the foot-gun a bare suffix test walks into:
// strings.HasSuffix("evilexample.com", "example.com") is true.
func TestHostRuleMatchesOnALabelBoundary(t *testing.T) {
	for _, tc := range []struct {
		rule, host string
		want       bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "a.example.com", false},   // exact means exact
		{"example.com", "evilexample.com", false}, // the foot-gun
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},        // the wildcard is not the apex
		{"*.example.com", "evilexample.com", false},    // the foot-gun again
		{"*.example.com", "xexample.com", false},       //
		{"*.example.com", "notexample.com", false},     //
		{"*.example.com", ".example.com", false},       // an empty label is not a subdomain
		{"sink.example.com", "sink.example.com", true}, //
	} {
		p := egress.Policy{InForce: true, Allow: []egress.Rule{{Host: tc.rule}}}
		if err := p.Validate(); err != nil {
			t.Fatalf("rule %q: %v", tc.rule, err)
		}
		d := egress.Destination{Host: tc.host, Port: 443}
		got := egress.Evaluate(p, d, []net.IP{net.ParseIP("93.184.216.34")}).Permitted
		if got != tc.want {
			t.Errorf("rule %q vs host %q = %v, want %v", tc.rule, tc.host, got, tc.want)
		}
	}
}

// TestAbsentPolicyAndAuthoredEmptyPolicyAreDifferent. Collapsing the tri-state
// breaks in one direction or the other and there is no third option: an absent
// policy that denied would break every subscription already in the field on the
// first upgrade, and an authored-empty policy that allowed would silently ignore an
// operator who meant "nothing".
func TestAbsentPolicyAndAuthoredEmptyPolicyAreDifferent(t *testing.T) {
	dest := egress.Destination{Host: "example.com", Port: 443}
	ips := []net.IP{net.ParseIP("93.184.216.34")}

	absent := egress.Evaluate(egress.Policy{InForce: false}, dest, ips)
	if !absent.Permitted || absent.Code != egress.CodeNoPolicy {
		t.Errorf("no policy in force must permit: %+v", absent)
	}
	authoredEmpty := egress.Evaluate(egress.Policy{InForce: true, Allow: nil}, dest, ips)
	if authoredEmpty.Permitted {
		t.Errorf("an authored empty allow-list must deny everything: %+v", authoredEmpty)
	}
	indeterminate := egress.Evaluate(egress.Indeterminate("policy:unreadable"), dest, ips)
	if indeterminate.Permitted {
		t.Errorf("an unreadable policy must deny: %+v", indeterminate)
	}
}

// TestEveryResolvedAddressMustBeAllowlisted: a name that answers with one permitted
// and one forbidden address is denied. Permitting it on the strength of whichever
// address happened to be checked first is the rebind hole.
func TestEveryResolvedAddressMustBeAllowlisted(t *testing.T) {
	p := egress.Policy{InForce: true, Allow: []egress.Rule{{CIDR: "93.184.216.0/24"}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	dest := egress.Destination{Host: "split.example.com", Port: 443}

	all := egress.Evaluate(p, dest, []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("93.184.216.35")})
	if !all.Permitted {
		t.Errorf("every address inside the CIDR must permit: %+v", all)
	}
	split := egress.Evaluate(p, dest, []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("8.8.4.4")})
	if split.Permitted {
		t.Error("a partial rebind was permitted: one address was outside the allow-list")
	}
	if split.Code != egress.CodeAddressNotAllowlisted {
		t.Errorf("code = %q, want %q", split.Code, egress.CodeAddressNotAllowlisted)
	}
}

// TestPortsAreDecided: without a port check, approving one endpoint on a host
// approves every service on it — https://approved.host:22 included.
func TestPortsAreDecided(t *testing.T) {
	p := egress.Policy{InForce: true, Allow: []egress.Rule{
		{Host: "soc.example.com", Ports: []egress.PortRange{{Low: 443, High: 443}}},
	}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	ips := []net.IP{net.ParseIP("93.184.216.34")}
	ok := egress.Evaluate(p, egress.Destination{Host: "soc.example.com", Port: 443}, ips)
	if !ok.Permitted {
		t.Errorf("the approved port must be permitted: %+v", ok)
	}
	ssh := egress.Evaluate(p, egress.Destination{Host: "soc.example.com", Port: 22}, ips)
	if ssh.Permitted {
		t.Error("port 22 on an approved host was permitted")
	}
	if ssh.Code != egress.CodePortNotAllowed {
		t.Errorf("code = %q, want %q", ssh.Code, egress.CodePortNotAllowed)
	}
}

// TestDecisionCarriesNoFragmentOfThePolicy. A holder of the write permission must
// not be able to enumerate the operator's allow-list by watching which destinations
// produce which message.
func TestDecisionCarriesNoFragmentOfThePolicy(t *testing.T) {
	p := egress.Policy{
		InForce: true,
		Allow:   []egress.Rule{{Host: "secret-soc.internal.example.com"}},
		Ref:     "operator-policy",
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	d := egress.Evaluate(p, egress.Destination{Host: "attacker.example.com", Port: 443},
		[]net.IP{net.ParseIP("93.184.216.34")})
	if d.Permitted {
		t.Fatal("unexpected permit")
	}
	for _, field := range []string{d.Code, d.PolicyRef} {
		if strings.Contains(field, "secret-soc") {
			t.Errorf("a denial leaked the allow-list: %q", field)
		}
	}
}

// TestParseDestinationDerivesTheEffectivePort, because a policy that decides ports
// must decide the same port the dialer will use.
func TestParseDestinationDerivesTheEffectivePort(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		host string
		port int
	}{
		{"https://Example.COM./events", "example.com", 443},
		{"https://example.com:8443/x", "example.com", 8443},
		{"http://example.com/x", "example.com", 80},
		{"https://[::1]:9000/x", "::1", 9000},
	} {
		d, err := egress.ParseDestination(tc.raw)
		if err != nil {
			t.Errorf("%q: %v", tc.raw, err)
			continue
		}
		if d.Host != tc.host || d.Port != tc.port {
			t.Errorf("%q -> %s:%d, want %s:%d", tc.raw, d.Host, d.Port, tc.host, tc.port)
		}
	}
	for _, bad := range []string{"ftp://example.com/x", "https://example.com:0/x", "https:///x"} {
		if d, err := egress.ParseDestination(bad); err == nil {
			t.Errorf("accepted %q as %+v, want refusal", bad, d)
		}
	}
}

// TestValidateCanonicalizesRulesInPlace: a rule stored in some other spelling would
// never match anything, so a typo would present as a silent deny-all rather than as
// an authoring error.
func TestValidateCanonicalizesRulesInPlace(t *testing.T) {
	p := egress.Policy{InForce: true, Allow: []egress.Rule{
		{Host: "SOC.Example.COM."}, {Host: "*.Sub.Example.COM"}, {CIDR: " 93.184.216.0/24 "},
	}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	want := []string{"soc.example.com", "*.sub.example.com", ""}
	for i, w := range want {
		if p.Allow[i].Host != w {
			t.Errorf("rule %d host = %q, want %q", i, p.Allow[i].Host, w)
		}
	}
	if p.Allow[2].CIDR != "93.184.216.0/24" {
		t.Errorf("cidr = %q", p.Allow[2].CIDR)
	}
	for _, bad := range []egress.Policy{
		{Allow: []egress.Rule{{Host: "a.example.com", CIDR: "93.184.216.0/24"}}},
		{Allow: []egress.Rule{{}}},
		{Allow: []egress.Rule{{CIDR: "not-a-cidr"}}},
		{Allow: []egress.Rule{{Host: "*.127.0.0.1"}}},
		{Allow: []egress.Rule{{Host: "a.example.com", Ports: []egress.PortRange{{Low: 0, High: 5}}}}},
		{Allow: []egress.Rule{{Host: "a.example.com", Ports: []egress.PortRange{{Low: 90, High: 10}}}}},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted an invalid policy: %+v", bad.Allow)
		}
	}
}

// TestPermitPinsTheAddresses. A permit that returned only "yes" would leave the
// caller to resolve the name a second time, and the second answer is the tenant's
// DNS to choose — which is the whole rebinding problem.
func TestPermitPinsTheAddresses(t *testing.T) {
	p := egress.Policy{InForce: true, Allow: []egress.Rule{{Host: "soc.example.com"}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	ips := []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("93.184.216.35")}
	d := egress.Evaluate(p, egress.Destination{Host: "soc.example.com", Port: 443}, ips)
	if !d.Permitted {
		t.Fatal("expected a permit")
	}
	if len(d.Pin) != len(ips) {
		t.Fatalf("permit pinned %d addresses, want %d", len(d.Pin), len(ips))
	}
	empty := egress.Evaluate(p, egress.Destination{Host: "soc.example.com", Port: 443}, nil)
	if empty.Permitted || empty.Code != egress.CodeUnresolvable {
		t.Errorf("a destination with no resolution must not be permitted: %+v", empty)
	}
}

// TestParseDestinationDecidesTheSchemeEvenWithAnExplicitPort. The scheme used to be
// consulted only on the branch that had to DERIVE a default port, so a URL carrying
// one — "ftp://host:9999/" — returned before the scheme was ever looked at. A
// destination whose transport this package does not recognize is not one it can
// decide about.
func TestParseDestinationDecidesTheSchemeEvenWithAnExplicitPort(t *testing.T) {
	for _, bad := range []string{
		"ftp://soc.example.com:9999/x",
		"gopher://soc.example.com:70/x",
		"file://soc.example.com:1/x",
		"ftp://soc.example.com/x", // the branch that always checked
	} {
		if d, err := egress.ParseDestination(bad); err == nil {
			t.Errorf("accepted %q as %s", bad, d)
		}
	}
	// http IS recognized here. This parser answers "which host and port", which is an
	// AUTHORITY question; requiring TLS is a transport rule and belongs to the caller
	// that is about to send bytes — modules/eventing applies it to the actual URL at
	// send time.
	for _, ok := range []string{"http://soc.example.com:8080/x", "https://soc.example.com/x"} {
		if _, err := egress.ParseDestination(ok); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
}

// TestAPermitIsCompatibleWithTheDialFloor is the cross-layer invariant the two sides
// were missing, and its absence is why they could disagree in silence:
//
//	Evaluate(...).Permitted ⇒ every Decision.Pin address is dialable
//
// Before, the evaluator's notion of "reserved" was narrower than the dialer's, so a
// policy could permit an address the socket would refuse forever — reported as a
// retryable network error, never as the policy/floor conflict it was. The evaluator
// now uses the SAME classifier, and where the two would still differ the permit
// carries OperatorAuthorized so a caller can lift its floor deliberately.
func TestAPermitIsCompatibleWithTheDialFloor(t *testing.T) {
	for _, addr := range []string{
		"100.64.0.9",      // CGNAT — the evaluator's old table missed it
		"203.0.113.9",     // TEST-NET-3 — the CIDR test used to treat this as production
		"198.18.0.9",      // benchmarking
		"192.0.2.9",       // TEST-NET-1
		"240.0.0.9",       // class E
		"10.2.3.4",        // RFC 1918
		"169.254.169.254", // the metadata service
		// IPv6 — the thin side of the table until these were added. Go's own resolver
		// tests treat the documentation prefix as reserved; ours did not.
		"2001:db8::1",    // RFC 3849 documentation
		"3fff::1",        // RFC 9637 documentation
		"100::1",         // RFC 6666 discard-only
		"2001::1",        // RFC 2928 IETF protocol assignments
		"64:ff9b::a00:1", // NAT64: an RFC 1918 target wearing a public-looking address
		"fc00::1",        // ULA (stdlib IsPrivate)
		"fe80::1",        // link-local
	} {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("bad fixture %q", addr)
		}
		if !egress.ReservedAddress(ip) {
			t.Errorf("%s is not classified reserved, but a dial floor refuses it", addr)
		}
	}
	// Ordinary public addresses are not swept up, in either family.
	for _, ok := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if egress.ReservedAddress(net.ParseIP(ok)) {
			t.Errorf("public address %s was classified reserved", ok)
		}
	}
}

// TestASupernetWithAPublicBaseStillCoversPrivateAddresses. Checking only the network
// base called 172.0.0.0/8 public, although it contains 172.16.0.0/12.
func TestASupernetWithAPublicBaseStillCoversPrivateAddresses(t *testing.T) {
	for _, c := range []string{"172.0.0.0/8", "0.0.0.0/0", "192.0.0.0/8", "100.0.0.0/8"} {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		if !egress.NetworkCoversReserved(n) {
			t.Errorf("%s was called free of reserved addresses", c)
		}
	}
	for _, c := range []string{"93.184.216.0/24", "8.8.8.0/24"} {
		_, n, _ := net.ParseCIDR(c)
		if egress.NetworkCoversReserved(n) {
			t.Errorf("%s was called reserved", c)
		}
	}
}

// TestAnOperatorCanAuthorizeAnAirGappedCollector is the product case this closes, and
// it is the target market's ordinary configuration rather than an edge case: an
// air-gapped SIEM is on RFC 1918 by definition.
//
// An earlier revision refused such a rule at authoring, in the name of a guard whose
// purpose is to constrain TENANT-authored destinations. The rule is permitted now and
// the permit says so, so a dialer can lift its floor for exactly those addresses —
// and for nothing else, because with no policy there is no rule and a tenant cannot
// write one.
func TestAnOperatorCanAuthorizeAnAirGappedCollector(t *testing.T) {
	p := egress.Policy{InForce: true, Allow: []egress.Rule{{CIDR: "10.0.0.0/8"}}, Ref: "operator"}
	if err := p.Validate(); err != nil {
		t.Fatalf("an operator may not authorize their own internal collector: %v", err)
	}
	d := egress.Evaluate(p, egress.Destination{Host: "siem.internal", Port: 8088, Scheme: "https"},
		[]net.IP{net.ParseIP("10.2.3.4")})
	if !d.Permitted {
		t.Fatalf("the internal collector was refused: %+v", d)
	}
	if !d.Lifts(net.ParseIP("10.2.3.4")) {
		t.Error("the address the operator wrote a CIDR rule for is not authorized, so the " +
			"dialer's floor will refuse it and the policy is decorative")
	}
	// And an estate with NO policy authorizes no address: the permit lifts nothing.
	none := egress.Evaluate(egress.Policy{}, egress.Destination{Host: "x.example.com", Port: 443},
		[]net.IP{net.ParseIP("10.2.3.4")})
	if none.Lifts(net.ParseIP("10.2.3.4")) {
		t.Error("an unconfigured estate authorized a reserved address")
	}
}

// TestNamingAHostIsNotNamingAnAddress is the hole the first version of the lift had,
// and it is the reason the permit carries a SET of addresses rather than a flag.
//
// An operator writes a rule for a NAME. DNS picks the address. So a flag meaning "an
// operator rule permitted this destination" lifted the floor for whatever
// `siem.internal` happened to resolve to — including 169.254.169.254, the cloud
// metadata service, which the operator never wrote and would never write.
func TestNamingAHostIsNotNamingAnAddress(t *testing.T) {
	hostOnly := egress.Policy{InForce: true, Ref: "operator",
		Allow: []egress.Rule{{Host: "siem.internal"}}}
	if err := hostOnly.Validate(); err != nil {
		t.Fatal(err)
	}
	dest := egress.Destination{Host: "siem.internal", Port: 8088, Scheme: "https"}

	// The metadata service is NEVER liftable, whatever any rule says, and a name that
	// resolves there is refused rather than permitted-and-undialable.
	meta := egress.Evaluate(hostOnly, dest, []net.IP{net.ParseIP("169.254.169.254")})
	if meta.Permitted || meta.Lifts(net.ParseIP("169.254.169.254")) {
		t.Errorf("a host rule authorized the metadata service: %+v", meta)
	}

	// An ordinary private address behind a host-only rule is refused too: the operator
	// named a name, not an address, so there is nothing to lift with.
	priv := egress.Evaluate(hostOnly, dest, []net.IP{net.ParseIP("10.2.3.4")})
	if priv.Permitted {
		t.Errorf("a host rule alone authorized a private address: %+v", priv)
	}

	// Adding the CIDR the operator actually means makes it work — and STILL not the
	// metadata service, because loopback and link-local are never liftable.
	withCIDR := egress.Policy{InForce: true, Ref: "operator", Allow: []egress.Rule{
		{Host: "siem.internal"}, {CIDR: "10.0.0.0/8"},
	}}
	if err := withCIDR.Validate(); err != nil {
		t.Fatal(err)
	}
	ok := egress.Evaluate(withCIDR, dest, []net.IP{net.ParseIP("10.2.3.4")})
	if !ok.Permitted || !ok.Lifts(net.ParseIP("10.2.3.4")) {
		t.Errorf("naming both the host and its network did not authorize it: %+v", ok)
	}
	// A partially-covered resolution is refused whole: one authorized address does not
	// license the others.
	split := egress.Evaluate(withCIDR, dest,
		[]net.IP{net.ParseIP("10.2.3.4"), net.ParseIP("169.254.169.254")})
	if split.Permitted {
		t.Errorf("a resolution containing an unauthorized reserved address was permitted: %+v", split)
	}
	// And a loopback address is refused even when a CIDR rule names it.
	loop := egress.Policy{InForce: true, Ref: "operator", Allow: []egress.Rule{
		{Host: "siem.internal"}, {CIDR: "127.0.0.0/8"},
	}}
	if err := loop.Validate(); err != nil {
		t.Fatal(err)
	}
	l := egress.Evaluate(loop, dest, []net.IP{net.ParseIP("127.0.0.1")})
	if l.Lifts(net.ParseIP("127.0.0.1")) {
		t.Error("loopback was authorized by policy; it is never liftable")
	}
}
