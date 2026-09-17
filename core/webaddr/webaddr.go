// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package webaddr parses and classifies ONE fact: the address a browser reaches
// this console at.
//
// It exists because that fact had four disagreeing answers in this tree — the
// startup panel concatenated the bind spelling, the WebAuthn relying party was
// derived from a request header, a console-side notice asked a regular
// expression, and a walk script asked a fourth. A host is either an address or a
// name, and which one it is decides what a panel prints, whether a passkey
// ceremony can run at all, and what the console warns about before the operator
// clicks. One parser, one classification, and the console mirrors the table this
// package emits rather than reimplementing it.
//
// The parser follows the URL Standard's host parser: an IPv6 literal in brackets,
// a host that "ends in a number" handed to the IPv4 parser (which reads 0x and
// leading-zero forms, and REFUSES what a browser refuses), and otherwise
// domain-to-ASCII with the strictness a browser uses. After Parse the host is
// either a canonical IP or an A-label domain, so every later question is a
// netip question plus one name family.
//
// It is deliberately MORE PERMISSIVE than core/egress's CanonicalHost and must
// not be merged with it: egress answers an authorization question and is right to
// deny-close; this one answers "what will the browser do".
//
// Nothing in this package echoes a supplied value into an error. See refusal.go.
package webaddr

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Address is a parsed, canonical browser address for this console.
//
// The zero Address means "no address was declared", which is not an error: an
// operator who does not name an address gets today's behavior.
type Address struct {
	// Origin is the exact web origin a browser reports — scheme://host[:port] —
	// and is also the URL a panel prints, because the console is served at the
	// root of its origin. An IPv6 host is bracketed here.
	Origin string
	// Scheme is "https" or "http", lowercase.
	Scheme string
	// Host is the canonical host. An IPv6 literal is stored WITHOUT brackets, so
	// it can be handed to netip directly.
	Host string
	// Port is the canonical decimal port, empty when it is the scheme's default.
	// A browser never sends the default port in an origin, and the installed
	// verifier drops only the literal ":80"/":443", so carrying "0443" here would
	// fail every ceremony.
	Port string
	// Kind is what the URL Standard's host parser decided this host is.
	Kind Kind
}

// IsZero reports whether no address was declared.
func (a Address) IsZero() bool { return a.Kind == KindNone && a.Origin == "" }

// IsIP reports whether the host is a numeric address rather than a name.
func (a Address) IsIP() bool { return a.Kind == KindIPv4 || a.Kind == KindIPv6 }

// addr returns the parsed numeric host, or the zero netip.Addr for a name.
func (a Address) addr() netip.Addr {
	if !a.IsIP() {
		return netip.Addr{}
	}
	ip, err := netip.ParseAddr(a.Host)
	if err != nil {
		return netip.Addr{}
	}
	return ip
}

// isUnspecified reports whether the host is the wildcard 0.0.0.0 or ::, which is
// a BIND and not an address anything can connect to.
//
// Unexported on purpose: the question a CALLER asks is "is this bind a wildcard",
// and FromListen answers that directly. Nothing outside this package has ever had
// a reason to ask it of an Address — a browser cannot be at 0.0.0.0 — and an
// exported predicate with no caller is a contract nobody agreed to.
func (a Address) isUnspecified() bool {
	ip := a.addr()
	return ip.IsValid() && ip.IsUnspecified()
}

// IsLoopback reports whether the host is a loopback address or a member of the
// localhost NAME family (localhost, localhost., *.localhost, *.localhost.), which
// Secure Contexts treats as potentially trustworthy.
func (a Address) IsLoopback() bool {
	if ip := a.addr(); ip.IsValid() {
		return ip.IsLoopback()
	}
	if a.Kind != KindDomain {
		return false
	}
	h := strings.TrimSuffix(a.Host, ".")
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// PotentiallyTrustworthy reports whether a browser is likely to treat this origin
// as a secure context, which the WebAuthn API requires before it will run at all.
//
// STATED LIMIT, and it is why this predicate only ever SUPPRESSES a warning and
// never promises anything: the loopback IP ranges and exact "localhost" are
// settled (Secure Contexts sections 3.1 and 5.2), but the "*.localhost" subtree
// is user-agent conditional in practice. The console-side notice does not reuse
// this judgement at all — it reads the browser's own isSecureContext, which is
// the only authority on the machine in front of the operator.
func (a Address) PotentiallyTrustworthy() bool {
	return a.Scheme == "https" || a.IsLoopback()
}

// CanBeRelyingParty reports whether this address's host can serve as the WebAuthn
// relying-party ID for the verifier compiled into this build.
//
// It is NOT "is this a domain". An IP is a fine browser address and is never a
// relying party (browsers refuse it, even though the installed library would
// accept the string). A name a browser opens happily — my_host.example.com — is
// not a valid domain in the URL Standard's strict sense and so is not a valid RP
// ID. And a single-label name is refused by THIS BUILD's verifier rather than by
// the specification: see isValidRPDomain.
func (a Address) CanBeRelyingParty() bool {
	return a.Kind == KindDomain && IsRelyingPartyDomain(a.Host)
}

// WithHost returns the same address served at another host, keeping scheme and
// port. It is how a panel offers "the same console, on localhost, on the port it
// is actually served on". A host this package would refuse leaves the address
// unchanged.
func (a Address) WithHost(host string) Address {
	canonical, kind, code := ParseHost(host)
	if code != 0 {
		return a
	}
	out := a
	out.Host, out.Kind = canonical, kind
	out.Origin = originOf(a.Scheme, canonical, kind, a.Port)
	return out
}

// originOf serializes an origin the way a browser does.
func originOf(scheme, host string, kind Kind, port string) string {
	if kind == KindIPv6 {
		host = "[" + host + "]"
	}
	origin := scheme + "://" + host
	if port != "" {
		origin += ":" + port
	}
	return origin
}

// defaultPort is the URL Standard's default-port table for the two schemes this
// console can be served over.
func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// Parse canonicalizes an operator-supplied absolute URL naming the address a
// browser reaches this console at. An empty value is the zero Address and a nil
// error: not naming an address is not a misconfiguration.
//
// field names the configuration source for the refusal message ("--public-url",
// "OLIVARES_PUBLIC_URL"). No byte of raw ever reaches that message.
func Parse(field, raw string) (Address, error) {
	raw = strings.Trim(raw, " \t\r\n\v\f")
	if raw == "" {
		return Address{}, nil
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7F || r == ' ' {
			return Address{}, refuse(field, CodeControlCharacter)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		// The library error is DROPPED, not wrapped: (*url.Error).Error() embeds
		// the URL it failed on, so wrapping would echo the value this package
		// promises never to echo.
		return Address{}, refuse(field, CodeNotAURL)
	}
	if u.Scheme == "" || u.Opaque != "" {
		// "panel.example.com:8443" parses as scheme "panel.example.com" with an
		// opaque body. The missing "//" is the honest diagnosis.
		return Address{}, refuse(field, CodeNoScheme)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return Address{}, refuse(field, CodeUnsupportedScheme)
	}
	if u.User != nil {
		return Address{}, refuse(field, CodeUserInfoPresent)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return Address{}, refuse(field, CodeQueryPresent)
	}
	if u.Fragment != "" || u.RawFragment != "" || strings.ContainsRune(raw, '#') {
		return Address{}, refuse(field, CodeFragmentPresent)
	}
	if p := u.EscapedPath(); p != "" && p != "/" {
		return Address{}, refuse(field, CodePathPresent)
	}
	hostPart, portPart, ok := splitHostPort(u.Host)
	if !ok {
		return Address{}, refuse(field, CodeHostNotAName)
	}
	if hostPart == "" {
		return Address{}, refuse(field, CodeNoHost)
	}
	port, code := canonicalPort(scheme, portPart)
	if code != 0 {
		return Address{}, refuse(field, code)
	}
	host, kind, code := ParseHost(hostPart)
	if code != 0 {
		return Address{}, refuse(field, code)
	}
	return Address{
		Origin: originOf(scheme, host, kind, port),
		Scheme: scheme, Host: host, Port: port, Kind: kind,
	}, nil
}

// canonicalPort normalizes a port the way a browser does: leading zeros are not
// part of the integer, and the scheme's default port is not part of an origin.
func canonicalPort(scheme, port string) (string, Code) {
	if port == "" {
		return "", 0
	}
	if !onlyASCIIDigits(port) {
		return "", CodePortOutOfRange
	}
	trimmed := strings.TrimLeft(port, "0")
	if trimmed == "" || len(trimmed) > 5 {
		return "", CodePortOutOfRange
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n < 1 || n > 65535 {
		return "", CodePortOutOfRange
	}
	if trimmed == defaultPort(scheme) {
		return "", 0
	}
	return trimmed, 0
}

// splitHostPort splits an authority into host and port, keeping the brackets on
// an IPv6 literal so ParseHost can tell a literal from a name.
func splitHostPort(hostport string) (host, port string, ok bool) {
	if strings.HasPrefix(hostport, "[") {
		end := strings.LastIndex(hostport, "]")
		if end < 0 {
			return "", "", false
		}
		host, rest := hostport[:end+1], hostport[end+1:]
		switch {
		case rest == "":
			return host, "", true
		case rest[0] == ':':
			return host, rest[1:], true
		default:
			return "", "", false
		}
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[:i], hostport[i+1:], true
	}
	return hostport, "", true
}

// FromListen builds the address a panel can print for a BIND spelling, and says
// whether that bind is a wildcard.
//
// A bare port (":8443") is how you ask Go for the dual-stack wildcard. It is NOT
// an empty host, and treating it as one is how the most-documented install path
// came to print "https://:8443" — an address no browser can open — as the console
// URL. For a wildcard the printed address becomes localhost, which is true on the
// machine running the engine and is never a claim about reachability from
// anywhere else; the caller is expected to say so.
//
// It never fails: a bind this cannot classify is printed as given, exactly as
// before, because a panel must not be able to stop a server from starting.
func FromListen(listen, scheme string) (Address, bool) {
	hostPart, portPart, ok := splitHostPort(strings.TrimSpace(listen))
	if !ok {
		return Address{Origin: scheme + "://" + listen, Scheme: scheme}, false
	}
	wildcard := hostPart == ""
	if !wildcard {
		if probe, kind, code := ParseHost(hostPart); code == 0 {
			if (Address{Host: probe, Kind: kind}).isUnspecified() {
				wildcard = true
			}
		}
	}
	if wildcard {
		hostPart = "localhost"
	}
	port, code := canonicalPort(scheme, portPart)
	if code != 0 {
		port = portPart
	}
	host, kind, code := ParseHost(hostPart)
	if code != 0 {
		return Address{Origin: scheme + "://" + listen, Scheme: scheme}, wildcard
	}
	return Address{
		Origin: originOf(scheme, host, kind, port),
		Scheme: scheme, Host: host, Port: port, Kind: kind,
	}, wildcard
}
