// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package egress is the single definition of "may this control plane open a
// connection to this destination".
//
// It exists for the reason sdk/siemwire exists one layer down: the same question
// was being answered in several places with several different answers. The eventing
// sink validated an authoring-time URL and never looked again; the sandbox proxy had
// the only real allow-list, and its host comparison used strings.EqualFold, which
// does not mean what an allow-list needs it to mean (see CanonicalHost). A product
// whose thesis is governing egress cannot hold two notions of an authorized
// destination, so both now resolve their hosts through this package.
//
// SCOPE, stated rather than implied: modules/notify does NOT use this package. Its
// destinations are operator-provisioned rather than tenant-authored, so what it needs
// and what it got is tenant SCOPING of a destination NAME — which tenants may address
// a destination the operator already chose. Applying a destination allow-list to
// operator config would be checking the operator against themselves. Governing where
// a connector plugin may connect is a different problem again and is not solved here:
// an out-of-process plugin owns its own socket.
//
// Two properties are the whole point, and neither is obtainable from a string
// comparison alone:
//
//   - A destination is authorized as a NAME, but a connection is opened to an
//     ADDRESS. Checking the name and then dialing the name resolves it twice, and
//     the second answer is the tenant's DNS to choose. Evaluate therefore takes the
//     resolved addresses and Decision returns the ones the caller MUST dial.
//   - Authorization must be re-checked where the bytes actually leave, not only
//     where the configuration was written. A policy edited after authoring is worth
//     nothing if existing rows are grandfathered, and a write path that skips the
//     validator (there was one) voids an authoring-time check entirely.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// idnaProfile is the STRICT lookup profile: it maps to ASCII, validates the
// resulting labels, and rejects rather than repairs. The permissive profiles exist
// for rendering user input; neither belongs anywhere near an authorization
// decision, because "repair it into something plausible" is how a host nobody
// approved becomes a host that matches.
var idnaProfile = idna.New(
	idna.MapForLookup(),
	idna.BidiRule(),
	idna.StrictDomainName(true),
	idna.VerifyDNSLength(true),
)

// ErrBadHost reports a host that cannot be canonicalized, which is always a denial
// rather than a fallback to the raw text.
var ErrBadHost = errors.New("egress: host is not a valid destination")

// CanonicalHost reduces a host to the one spelling both sides of an authorization
// check must agree on: IDNA2008 A-labels, no trailing dot, lower-case ASCII, and an
// IP literal in its canonical form.
//
// It does NOT use strings.ToLower or strings.EqualFold on the raw host, and the
// reason is more specific than "Unicode is hard". Both fold characters together:
// strings.EqualFold on U+017F LATIN SMALL LETTER LONG S and an ordinary "s" is
// true, and strings.ToLower maps the KELVIN SIGN onto "k". Folding is not itself
// the bug — UTS-46 lookup mapping, which idnaProfile applies, folds those same
// characters, and that IS what a conforming resolver does with them.
//
// The bug is folding for the CHECK and dialing the ORIGINAL. Then the name that was
// authorized and the name that is resolved are different strings: the allow-list
// decided about one while the connection went to the other. This package closes it
// by making the canonical form the only thing that leaves — Destination.Host is the
// canonicalized host, Resolve looks THAT up, and Decision.Pin returns the addresses
// to dial. The invariant is "what matched is what is dialed", pinned by
// TestWhatMatchedIsWhatIsDialed.
//
// After ToASCII every label is ASCII, so the final ASCII-only lower-casing is exact
// and the rule comparison is a byte comparison.
//
// A trailing dot is stripped because "example.com." and "example.com" are the same
// DNS name and different Go strings, so leaving it is a bypass that costs one
// character.
func CanonicalHost(raw string) (string, error) {
	// The control-character check runs on the RAW input, BEFORE any trimming.
	// Trimming first would quietly accept "example.com\r" as "example.com", which is
	// the exact smuggling this refuses — strings.TrimSpace removes CR, LF and TAB,
	// so the check has to come first or it does not run on the interesting bytes.
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x20 || c == 0x7f {
			return "", fmt.Errorf("%w: contains a control character", ErrBadHost)
		}
	}
	h := strings.Trim(raw, " ")
	if h == "" {
		return "", fmt.Errorf("%w: empty", ErrBadHost)
	}
	if strings.IndexByte(h, ' ') >= 0 {
		return "", fmt.Errorf("%w: contains a space", ErrBadHost)
	}
	// An IP literal is its own canonical form once normalized: ::1, 0:0:0:0:0:0:0:1
	// and 0000:...:0001 are one address and three strings, so the comparison must be
	// net.IP.Equal or a canonical text, never the text the caller happened to type.
	if ip := parseIPLiteral(h); ip != nil {
		return ip.String(), nil
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", fmt.Errorf("%w: empty after the trailing dot", ErrBadHost)
	}
	// A host whose last label is entirely digits is refused rather than treated as a
	// name. net.ParseIP rejects the legacy inet_aton spellings — "127.000.000.001",
	// "2130706433", "0x7f.0.0.1" — since Go 1.17, so they arrive here looking like
	// domain names and would be authorized as opaque hosts. glibc's getaddrinfo does
	// still accept several of them, so the string a policy approved and the address a
	// resolver returns can differ. No real TLD is all-numeric (RFC 1123 §2.1 relies
	// on exactly that to keep names and addresses distinguishable), so refusing costs
	// nothing and closes the ambiguity instead of betting on the platform resolver.
	if last := h[strings.LastIndexByte(h, '.')+1:]; last == "" || allDigits(last) {
		return "", fmt.Errorf("%w: %q is not a hostname and is not a valid IP literal", ErrBadHost, h)
	}
	ascii, err := idnaProfile.ToASCII(h)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadHost, err)
	}
	// ToASCII leaves already-ASCII labels' case alone, so fold the ASCII explicitly.
	// This is safe where a general Unicode fold was not: every byte is now ASCII.
	return lowerASCII(ascii), nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// parseIPLiteral accepts an IP literal with or without brackets. net.ParseIP does
// not accept the bracketed IPv6 form that appears in a URL host.
func parseIPLiteral(h string) net.IP {
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	return net.ParseIP(h)
}

func lowerASCII(s string) string {
	need := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			need = true
			break
		}
	}
	if !need {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// Destination is a canonicalized target: what a policy is evaluated against and
// what a caller records in a denial. It carries the port explicitly because a
// policy that cannot express one authorizes every service on an approved host —
// https://approved.host:22 is a different destination from https://approved.host.
type Destination struct {
	// Host is the canonical host (see CanonicalHost).
	Host string
	// Port is the effective port: the URL's, or the scheme's default.
	Port int
	// Scheme is the lower-cased URL scheme, always one this package recognizes.
	Scheme string
	// IP is non-nil when Host was an IP literal.
	IP net.IP
}

// String renders the destination for a log or a denial record. It is host:port and
// never the full URL, because a path or a query can carry tenant data and a denial
// record is read by an operator who is not entitled to it.
func (d Destination) String() string { return net.JoinHostPort(d.Host, strconv.Itoa(d.Port)) }

// ParseDestination canonicalizes a URL into the destination a policy decides about.
// It deliberately ignores everything after the authority: a policy authorizes a
// place to connect to, not a request to make.
func ParseDestination(rawURL string) (Destination, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Destination{}, fmt.Errorf("%w: %v", ErrBadHost, err)
	}
	if u.User != nil {
		// Credentials in a URL are refused HERE and not only at authoring, because the
		// URL a renderer produces is not the URL an author wrote: a userinfo added
		// downstream would otherwise reach the wire, and it would reach it in a field
		// that is logged and stored by whatever sits in front of the destination.
		return Destination{}, fmt.Errorf("%w: the URL carries credentials", ErrBadHost)
	}
	host, err := CanonicalHost(u.Hostname())
	if err != nil {
		return Destination{}, err
	}
	// The SCHEME is decided first and unconditionally. It used to be consulted only
	// when the URL carried no explicit port, so "ftp://host:9999/" and
	// "http://host:8080/" parsed cleanly — the port branch returned before the scheme
	// was ever looked at. A destination whose transport this package does not
	// recognize is not a destination it can decide about.
	scheme := strings.ToLower(u.Scheme)
	defaultPort := 0
	switch scheme {
	case "https":
		defaultPort = 443
	case "http":
		defaultPort = 80
	default:
		return Destination{}, fmt.Errorf("%w: unsupported scheme %q", ErrBadHost, u.Scheme)
	}
	port := defaultPort
	if p := u.Port(); p != "" {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 65535 {
			return Destination{}, fmt.Errorf("%w: port %q", ErrBadHost, p)
		}
		port = n
	}
	return Destination{Host: host, Port: port, Scheme: scheme, IP: parseIPLiteral(host)}, nil
}

// PortRange is an inclusive port range. A rule with no ranges permits any port.
type PortRange struct {
	Low  int `json:"low"`
	High int `json:"high"`
}

func (p PortRange) contains(port int) bool { return port >= p.Low && port <= p.High }

// Rule is one allow-list entry. Exactly one of Host or CIDR is set.
//
// Host matching is EXPLICIT about subdomains, which is the one place this diverges
// from the repository's older allow-list (enterprise/servertoolegress/domains.go
// hostCovers, where every entry implicitly covers its subdomains):
//
//   - "example.com" permits exactly that host.
//   - "*.example.com" permits any subdomain and NOT the apex.
//
// The mechanism is the same one and it is the part that matters — matching on a
// LABEL BOUNDARY, so "example.com" never covers "evilexample.com", the foot-gun a
// bare strings.HasSuffix walks straight into. What changes is that the operator
// states the intent instead of the code assuming it. An operator approving one SOC
// endpoint should not silently approve every name a tenant can create under it.
type Rule struct {
	// Host is a canonical host, or "*." followed by a canonical parent domain.
	Host string `json:"host,omitempty"`
	// CIDR is a network the destination's addresses must lie inside.
	CIDR string `json:"cidr,omitempty"`
	// Ports restricts which ports the rule permits; empty permits any.
	Ports []PortRange `json:"ports,omitempty"`

	// reachesReserved records that this rule can authorize an address the SSRF floor
	// would otherwise refuse. Set by Validate, never by an operator: it is a derived
	// fact about the rule, not a switch.
	reachesReserved bool
}

func (r Rule) portOK(port int) bool {
	if len(r.Ports) == 0 {
		return true
	}
	for _, p := range r.Ports {
		if p.contains(port) {
			return true
		}
	}
	return false
}

// Policy is an allow-list plus the flag that keeps "no policy" distinguishable from
// "a policy that allows nothing".
//
// The tri-state is not a nicety, and collapsing it is the single easiest way to get
// this wrong in either direction. An empty allow-list must mean deny-everything for
// the algebra to be sound; an absent policy must mean unconstrained, or the first
// upgrade that ships this code breaks every subscription already in the field. They
// are different states and InForce is what separates them — the same shape as
// governance's CronAllowlistInForce.
//
// A policy that could not be READ is a third thing again, and it is the caller's job
// to treat that as a denial rather than as an absent policy. See Indeterminate.
type Policy struct {
	// InForce reports that an operator has authored a policy. When false, Allow is
	// ignored and every destination is permitted.
	InForce bool `json:"in_force"`
	// Allow is the set of permitted destinations. Empty with InForce true is an
	// authored deny-all, which is a legitimate thing for an operator to mean.
	Allow []Rule `json:"allow,omitempty"`
	// Unavailable reports that the real policy could not be READ. It denies like an
	// empty allow-list and is reported differently, because an outage is not a
	// refusal — see Indeterminate.
	Unavailable bool `json:"-"`
	// Ref names the policy for a denial record (a file path, a row id). It is an
	// identifier, never the policy content.
	Ref string `json:"-"`
}

// Indeterminate is the policy a caller must use when the real one could not be
// read. It denies everything, and it reports CodePolicyUnavailable rather than
// CodeNotAllowlisted — a distinction that is NOT cosmetic, because the two demand
// opposite handling. A destination the policy refuses will not become permitted by
// waiting, so it is terminal; a policy STORE that is briefly unreadable will, so
// treating it as terminal would dead-letter deliveries because a lookup timed out.
//
// An earlier revision returned a plain empty allow-list here, which is materially
// the same denial and reported the same code — so the comment promised a
// distinction the value could not make. Unavailable is now a field, not a
// convention.
func Indeterminate(ref string) Policy {
	return Policy{InForce: true, Unavailable: true, Ref: ref}
}

// Denial codes. They are STABLE tokens: they are recorded and they drive automation.
//
// A code carries no fragment of the policy's CONTENT — never a rule, never a host,
// never a count. It does distinguish WHY a destination was refused, and that
// distinction is information: telling "the host is allowed but not that port" apart
// from "the host is not allowed" confirms the host is on the allow-list. That is
// acceptable for the LEDGER, which an operator reads, and is not acceptable on a
// tenant-facing error, which is why modules/eventing collapses every refusal to one
// message before it answers a caller (egressAuthoringError). A layer that surfaces
// these codes to a caller who may not read the policy must do the same.
const (
	// CodeAllowed is the permit.
	CodeAllowed = "allowed"
	// CodeNoPolicy is the permit when no policy is in force.
	CodeNoPolicy = "no_policy_in_force"
	// CodeNotAllowlisted means no rule covered the destination.
	CodeNotAllowlisted = "destination_not_allowlisted"
	// CodePortNotAllowed means a rule covered the host but not the port.
	CodePortNotAllowed = "destination_port_not_allowed"
	// CodeAddressNotAllowlisted means the host was covered by name but one of its
	// resolved addresses was not — a partial rebind.
	CodeAddressNotAllowlisted = "resolved_address_not_allowlisted"
	// CodeUnresolvable means the destination produced no address to check.
	CodeUnresolvable = "destination_did_not_resolve"
	// CodePolicyUnavailable means the policy itself could not be read, so nothing was
	// decided ABOUT the caller. It denies, and it is RETRYABLE: an unreadable store
	// is an outage and will resolve, whereas a refused destination will not.
	CodePolicyUnavailable = "policy_unavailable"
	// CodePolicyRequired means the deployment ENFORCES this control and no policy has
	// been authored, so there is nothing that could permit a destination.
	//
	// It is a separate code from CodeNotAllowlisted because the remediation has a
	// different OWNER and that is the only thing the recipient can act on: nothing the
	// tenant does to its destination will help, and a platform operator has to
	// authorize it. It is terminal rather than retryable — an absent policy does not
	// appear on its own — and it is the state a fresh install starts in, so the
	// message must read as configuration pending rather than as a fault.
	CodePolicyRequired = "egress_policy_required"
	// CodeLegacyException means the destination is not covered by the operator's
	// current policy but is one this deployment already had when the control was
	// installed, and the control is still in compatibility mode.
	//
	// It PERMITS, and it is deliberately distinguishable from CodeAllowed: every
	// delivery carrying it is a row on the list of things that stop working at
	// actuation, and an operator who cannot count them cannot consent to the change.
	CodeLegacyException = "legacy_compat_exception"
	// CodeRolloutUnavailable means the deployment's own durable rollout state could
	// not be read, so it is not known whether this control is in force. It denies and
	// is RETRYABLE, for the same reason CodePolicyUnavailable is: "the plane could not
	// decide" must never be delivered as "the plane decided yes".
	CodeRolloutUnavailable = "rollout_state_unavailable"
	// CodeSeedIncomplete means the deployment is in compatibility mode but this
	// tenant's pre-existing entitlements have not been recorded yet, so a denial
	// cannot be distinguished from a destination that was always permitted. It denies
	// and is RETRYABLE: the seeding is a local operation that will complete.
	CodeSeedIncomplete = "legacy_seed_incomplete"
)

// Retryable reports whether a denial may resolve on its own. It exists so a caller
// cannot accidentally dead-letter a delivery because a lookup timed out — the
// failure mode of collapsing "refused" and "could not decide" into one denial.
func (d Decision) Retryable() bool {
	switch d.Code {
	case CodePolicyUnavailable, CodeUnresolvable, CodeRolloutUnavailable, CodeSeedIncomplete:
		return true
	}
	return false
}

// Decision is the outcome of one evaluation.
type Decision struct {
	// Permitted is the verdict.
	Permitted bool
	// Code is a stable token from the list above.
	Code string
	// PolicyRef identifies the policy that decided, for the denial record.
	PolicyRef string
	// Pin is the address set the caller MUST dial when Permitted. Dialing the name
	// again would resolve it a second time, and the second answer is not the one
	// that was authorized.
	Pin []net.IP
	// ReservedAuthorized is the subset of Pin that an operator authorized BY ADDRESS —
	// each one covered by an explicit CIDR rule, with the port — and for which a dialer
	// may lift its reserved-address floor.
	//
	// It is a SET and not a boolean, and that distinction is the whole correctness of
	// the air-gapped case. A boolean said "an operator rule permitted this
	// destination", and a HOST rule permits a NAME: the addresses come from DNS, so a
	// rule for `siem.internal` lifted the floor for whatever that name resolved to —
	// including 169.254.169.254, which the operator never wrote. Naming a host is not
	// naming an address.
	//
	// Loopback, link-local, multicast and unspecified are never in this set whatever
	// the policy says. They are not "internal collectors an operator might legitimately
	// run"; they are the addresses the floor exists for.
	ReservedAuthorized []net.IP
}

// Lifts reports whether ip is one the operator authorized by address.
func (d Decision) Lifts(ip net.IP) bool {
	for _, a := range d.ReservedAuthorized {
		if a.Equal(ip) {
			return true
		}
	}
	return false
}

// Evaluate decides whether dest is permitted, given the addresses it resolved to.
//
// ips must be the COMPLETE resolution. Passing a subset would let a host that splits
// across allowed and disallowed addresses be permitted on the strength of the half
// that happened to be checked.
func Evaluate(p Policy, dest Destination, ips []net.IP) Decision {
	if p.Unavailable {
		return Decision{Code: CodePolicyUnavailable, PolicyRef: p.Ref}
	}
	if !p.InForce {
		return Decision{Permitted: true, Code: CodeNoPolicy, PolicyRef: p.Ref, Pin: ips}
	}
	if len(ips) == 0 {
		return Decision{Code: CodeUnresolvable, PolicyRef: p.Ref}
	}
	// Which of the resolved addresses did the operator authorize BY ADDRESS? Only
	// those may lift a caller's reserved-address floor. Computed before any permit,
	// because a host rule alone can never grant it: a host rule names a NAME, and DNS
	// picks the address.
	lifted := reservedAuthorizedBy(p.Allow, ips, dest.Port)

	// 1) A host rule authorizes the NAME. The addresses are still pinned, so a later
	// re-resolution cannot move the dial target, but they are not required to be
	// individually allow-listed: the operator approved a name whose addresses are
	// the destination's to choose.
	hostMatched := false
	for _, r := range p.Allow {
		if r.Host == "" || !hostCovers(r.Host, dest.Host) {
			continue
		}
		hostMatched = true
		if r.portOK(dest.Port) {
			// A RESERVED address behind an allowed name is refused unless a CIDR rule
			// also names it. Permitting it would either be undialable (the floor
			// refuses) or, worse, would lift the floor for an address the operator
			// never wrote — a name that resolves to the metadata service is the case
			// that matters, and it is not hypothetical.
			if bad := unauthorizedReserved(ips, lifted); bad != nil {
				return Decision{Code: CodeAddressNotAllowlisted, PolicyRef: p.Ref}
			}
			return Decision{Permitted: true, Code: CodeAllowed, PolicyRef: p.Ref, Pin: ips,
				ReservedAuthorized: lifted}
		}
	}
	// 2) CIDR rules authorize ADDRESSES, and then EVERY resolved address must be
	// covered. Requiring all of them is what makes the rule rebind-safe: a name that
	// answers with one allowed and one disallowed address is denied, rather than
	// permitted on the strength of whichever came first.
	if hasCIDR(p.Allow) {
		all := true
		for _, ip := range ips {
			if !addressAllowed(p.Allow, ip, dest.Port) {
				all = false
				break
			}
		}
		if all {
			return Decision{Permitted: true, Code: CodeAllowed, PolicyRef: p.Ref, Pin: ips,
				ReservedAuthorized: lifted}
		}
		if hostMatched {
			return Decision{Code: CodePortNotAllowed, PolicyRef: p.Ref}
		}
		return Decision{Code: CodeAddressNotAllowlisted, PolicyRef: p.Ref}
	}
	if hostMatched {
		return Decision{Code: CodePortNotAllowed, PolicyRef: p.Ref}
	}
	return Decision{Code: CodeNotAllowlisted, PolicyRef: p.Ref}
}

// CoversAuthority reports whether p has a rule that names dest's host and port.
//
// It is a REPORTING predicate and NOT an authorization, and the difference is not
// stylistic. Evaluate takes the resolved addresses and refuses a destination whose
// resolution disagrees with the rule that named it; this asks only the question a
// human is asking when they read "would my policy still cover this?" about a
// destination they are not connecting to right now. Nothing may dial on its answer,
// which is why it returns a bare bool and never a Decision.
//
// It exists because the alternative was worse. The unit-G actuation report has
// to tell an operator which grandfathered destinations their candidate policy still
// covers, over authorities recorded months earlier — resolving every one of them
// would turn reading a report into a burst of DNS traffic, and would make the report
// change depending on what DNS answered at that instant. Both engines' rule matching
// is shared with Evaluate (hostCovers, portOK, addressAllowed), so there is one
// implementation of "does this rule name this place" and not two.
func CoversAuthority(p Policy, dest Destination) bool {
	if !p.InForce || p.Unavailable {
		return false
	}
	for _, r := range p.Allow {
		if r.Host != "" && hostCovers(r.Host, dest.Host) && r.portOK(dest.Port) {
			return true
		}
	}
	// An authority whose host IS an address is decided by the CIDR rules, because that
	// is what would decide it at send time: there is no name for a host rule to cover.
	if dest.IP != nil {
		return addressAllowed(p.Allow, dest.IP, dest.Port)
	}
	return false
}

// hostCovers reports whether a rule host permits a destination host. Both are
// already canonical, so the comparison is byte-exact.
//
// The wildcard form matches on a LABEL BOUNDARY and never the apex: "*.example.com"
// covers "a.example.com" and "a.b.example.com", and does not cover "example.com" nor
// "evilexample.com". The boundary is the entire point — a bare suffix test would
// treat "evilexample.com" as covered by "example.com".
func hostCovers(rule, host string) bool {
	if strings.HasPrefix(rule, "*.") {
		parent := rule[2:]
		return parent != "" && len(host) > len(parent)+1 && strings.HasSuffix(host, "."+parent)
	}
	return rule == host
}

// reservedAuthorizedBy returns the addresses that are BOTH reserved AND covered by an
// explicit CIDR rule permitting this port. Those are the only ones a caller may dial
// past its floor.
//
// Loopback, link-local, multicast and unspecified are excluded unconditionally. An
// operator running an internal collector runs it on a routable private network; the
// metadata service and the loopback interface are not collectors, they are the reason
// the floor exists.
func reservedAuthorizedBy(rules []Rule, ips []net.IP, port int) []net.IP {
	var out []net.IP
	for _, ip := range ips {
		if !ReservedAddress(ip) || neverLiftable(ip) {
			continue
		}
		if addressAllowed(rules, ip, port) {
			out = append(out, ip)
		}
	}
	return out
}

// neverLiftable names the addresses no policy may authorize.
func neverLiftable(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified()
}

// unauthorizedReserved returns the first resolved address that is reserved and NOT
// authorized by address, or nil.
//
// LOOPBACK is excluded, and deliberately: whether a loopback destination is
// acceptable is a property of the CALLER's deployment posture, not of the operator's
// destination policy. Every dialer in this tree already has that switch (a
// single-box install legitimately points at 127.0.0.1; production refuses it), and
// duplicating the decision here would either break development or override the
// production refusal. The policy layer decides about the addresses an operator can
// write a rule for; loopback is not one of them.
func unauthorizedReserved(ips []net.IP, lifted []net.IP) net.IP {
	for _, ip := range ips {
		v4 := ip
		if m := ip.To4(); m != nil {
			v4 = m
		}
		if !ReservedAddress(ip) || v4.IsLoopback() {
			continue
		}
		found := false
		for _, l := range lifted {
			if l.Equal(ip) {
				found = true
				break
			}
		}
		if !found {
			return ip
		}
	}
	return nil
}

func hasCIDR(rules []Rule) bool {
	for _, r := range rules {
		if r.CIDR != "" {
			return true
		}
	}
	return false
}

func addressAllowed(rules []Rule, ip net.IP, port int) bool {
	for _, r := range rules {
		if r.CIDR == "" {
			continue
		}
		_, n, err := net.ParseCIDR(strings.TrimSpace(r.CIDR))
		if err != nil {
			// A rule that does not parse never permits. Skipping it silently is the
			// only safe reading: treating it as a wildcard would turn a typo into an
			// open allow-list.
			continue
		}
		if n.Contains(ip) && r.portOK(port) {
			return true
		}
	}
	return false
}

// Validate checks a policy at authoring time so an operator learns about a typo when
// they write it rather than when a delivery is refused. It canonicalizes every rule
// host IN PLACE, which is what makes the byte-exact comparison in hostCovers legal:
// a rule stored in some other spelling would never match anything.
func (p *Policy) Validate() error {
	for i := range p.Allow {
		r := &p.Allow[i]
		switch {
		case r.Host != "" && r.CIDR != "":
			return fmt.Errorf("egress: rule %d sets both host and cidr", i)
		case r.Host == "" && r.CIDR == "":
			return fmt.Errorf("egress: rule %d sets neither host nor cidr", i)
		case r.Host != "":
			wildcard := strings.HasPrefix(r.Host, "*.")
			bare := strings.TrimPrefix(r.Host, "*.")
			c, err := CanonicalHost(bare)
			if err != nil {
				return fmt.Errorf("egress: rule %d host: %w", i, err)
			}
			if wildcard {
				if parseIPLiteral(c) != nil {
					return fmt.Errorf("egress: rule %d: an IP literal has no subdomains", i)
				}
				c = "*." + c
			}
			r.Host = c
		default:
			_, n, err := net.ParseCIDR(strings.TrimSpace(r.CIDR))
			if err != nil {
				return fmt.Errorf("egress: rule %d cidr: %w", i, err)
			}
			// A rule over a private or reserved range is PERMITTED, and it is the only
			// way an air-gapped deployment can ship its evidence anywhere: its SIEM is
			// on RFC 1918 by definition. Refusing it here — which an earlier revision
			// did — closed the product's own target case in the name of a guard whose
			// whole purpose is to constrain TENANT-authored destinations, not the
			// operator who configures the box.
			//
			// The rule is marked instead of refused, and a caller that dials may lift
			// its SSRF floor for exactly the addresses such a rule authorizes. Nothing
			// is relaxed by default: with no policy there is no rule, and a tenant
			// cannot write one.
			r.CIDR = strings.TrimSpace(r.CIDR)
			r.reachesReserved = NetworkCoversReserved(n)
		}
		for _, pr := range r.Ports {
			if pr.Low < 1 || pr.High > 65535 || pr.Low > pr.High {
				return fmt.Errorf("egress: rule %d has an invalid port range %d-%d", i, pr.Low, pr.High)
			}
		}
	}
	return nil
}

// specialUseCIDRs are the reserved ranges the standard library classifiers miss.
// Each carries the RFC that reserves it, because a range in this list is a range no
// destination reaches without an operator naming it, and that is not a judgement to
// make from memory.
//
// COMPLETENESS against the full IANA special-purpose registry is UNVERIFIED and the
// list is additive: a range that is missing is one the classifier calls ordinary, so
// the failure direction is a permit the dialer's other guards may still refuse rather
// than a silent bypass of them. The IPv6 side was the thin part — it held only the two
// NAT64 prefixes until the documentation and discard ranges were added here.
var specialUseCIDRs = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		// IPv4
		"100.64.0.0/10",      // RFC 6598 CGNAT / shared address space
		"192.0.0.0/24",       // RFC 6890 IETF protocol assignments
		"192.0.2.0/24",       // RFC 5737 TEST-NET-1
		"198.51.100.0/24",    // RFC 5737 TEST-NET-2
		"203.0.113.0/24",     // RFC 5737 TEST-NET-3
		"198.18.0.0/15",      // RFC 2544 benchmarking
		"240.0.0.0/4",        // RFC 1112 class E, reserved
		"255.255.255.255/32", // limited broadcast
		// IPv6
		"64:ff9b::/96",   // RFC 6052 NAT64 well-known prefix — maps IPv4 into "public" IPv6
		"64:ff9b:1::/48", // RFC 8215 NAT64 local-use prefix
		"2001:db8::/32",  // RFC 3849 documentation — Go's own resolver tests treat it as reserved
		"3fff::/20",      // RFC 9637 documentation (2024), the successor block
		"100::/64",       // RFC 6666 discard-only
		"2001::/23",      // RFC 2928 IETF protocol assignments
	} {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("egress: bad reserved CIDR literal: " + c)
		}
		out = append(out, n)
	}
	return out
}()

// ReservedAddress reports whether an address is one no destination should be dialed
// at without an operator explicitly saying so: loopback, private, link-local (the
// cloud metadata service lives there), multicast, unspecified, and the special-use
// ranges above.
//
// It is THE classifier. It was three: the engine's dialer had one table, this
// package's policy validation had a narrower one, and the CLI had a third that was
// narrower still — so an operator could write a rule this package accepted and the
// dialer would refuse forever, with no error saying why. IPv4-mapped IPv6 is unmapped
// first, so ::ffff:10.0.0.1 cannot mask a private IPv4 target.
func ReservedAddress(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range specialUseCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// NetworkCoversReserved reports whether a CIDR contains ANY reserved address. It
// walks the classifier over the block's boundaries rather than testing only the base,
// because a supernet with a public base can still cover a private range — 172.0.0.0/8
// contains 172.16.0.0/12, and a base-only test called it public.
func NetworkCoversReserved(n *net.IPNet) bool {
	if ReservedAddress(n.IP) {
		return true
	}
	for _, r := range append([]*net.IPNet{}, specialUseCIDRs...) {
		if n.Contains(r.IP) {
			return true
		}
	}
	// The private, loopback and link-local blocks are not in specialUseCIDRs (the
	// stdlib classifies them), so they are checked explicitly.
	for _, c := range []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8",
		"169.254.0.0/16", "fc00::/7", "fe80::/10", "::1/128",
	} {
		_, r, err := net.ParseCIDR(c)
		if err != nil || r == nil {
			continue
		}
		if n.Contains(r.IP) {
			return true
		}
	}
	return false
}

// Resolver is the name lookup Evaluate's caller performs. It is an interface so a
// test can pin an answer, and so a caller that already holds a resolution does not
// resolve twice.
type Resolver interface {
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

// NetResolver adapts the standard resolver.
type NetResolver struct{ R *net.Resolver }

func (n NetResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	r := n.R
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// Resolve returns the destination's addresses: an IP literal is its own resolution,
// a name is looked up once. The single lookup is what the caller pins.
func Resolve(ctx context.Context, res Resolver, dest Destination) ([]net.IP, error) {
	if dest.IP != nil {
		return []net.IP{dest.IP}, nil
	}
	if res == nil {
		res = NetResolver{}
	}
	return res.LookupIP(ctx, dest.Host)
}
