// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package webaddr

import "fmt"

// A REFUSAL NAMES THE FIELD AND THE FAILURE CLASS, AND NOTHING ELSE.
//
// This is a hard rule of the package and not a style preference. The value being
// refused is an operator-supplied URL, and an operator can put a credential, a
// token or a session id inside one. The refusal is returned to a composition
// root that prints it on stderr, which in every documented deployment of this
// product is the journal, `docker compose logs` or pod logs — a pipeline whose
// readers are not the person who typed the value.
//
// So: no byte of the supplied value ever reaches Error(). Not the host, not the
// port, not the scheme, and above all not a nested *url.Error — (*url.Error).Error()
// is fmt.Sprintf("%s %q: %s", e.Op, e.URL, e.Err) and embeds the whole URL, so
// dropping the value from the format string is not enough on its own. Parse maps
// the library failure to a Code and discards the library's text.
//
// There is DELIBERATELY NO DEBUG ESCAPE HATCH. A flag that re-enables echoing is a
// flag that gets set on the deployment that is already in trouble.
//
// What the operator gets instead is a class plus a concrete remedy, which is what
// they can act on: every Code below names one thing to change. An address that is
// ACCEPTED is a different matter — it is printed in the startup panel, because
// that is what it is for.

// Code is the class of an address refusal: stable, machine-readable, and safe to
// log on its own.
type Code uint8

// The refusal classes. Ordered as Parse evaluates them.
const (
	// CodeControlCharacter: the value carries a space or a control byte.
	CodeControlCharacter Code = iota + 1
	// CodeNotAURL: the value is not a URL at all.
	CodeNotAURL
	// CodeNoScheme: no scheme, or no "//" after it (bare "host:port").
	CodeNoScheme
	// CodeUnsupportedScheme: a scheme other than https or http.
	CodeUnsupportedScheme
	// CodeUserInfoPresent: the value carries userinfo (user or user:password).
	CodeUserInfoPresent
	// CodePathPresent: the value carries a path other than "/".
	CodePathPresent
	// CodeQueryPresent: the value carries a query string.
	CodeQueryPresent
	// CodeFragmentPresent: the value carries a fragment.
	CodeFragmentPresent
	// CodeNoHost: the value has no host component.
	CodeNoHost
	// CodePortOutOfRange: the port is not one a browser can open (1-65535).
	CodePortOutOfRange
	// CodeHostZoneIdentifier: an IPv6 literal carrying a zone identifier.
	CodeHostZoneIdentifier
	// CodeHostNotAnAddress: a host that ends in a number, which the URL Standard
	// hands to the IPv4 parser, and that parser returns failure for.
	CodeHostNotAnAddress
	// CodeHostNotAName: a host a browser's domain parser refuses.
	CodeHostNotAName
)

// unshownNotice explains the deliberate omission. Silence about an omission reads
// as a bug in the message, so the message says the omission is on purpose.
const unshownNotice = " The value is not shown: a refused address can carry a credential or a token, and this message is written to this process's log."

// example is the shape every remedy points at.
const example = "scheme://host[:port], for example https://olivares.example.com:8443"

// reason is the class sentence for a Code. It contains no byte of any value.
func (c Code) reason() string {
	switch c {
	case CodeControlCharacter:
		return "contains a space or a control character, which no host may carry"
	case CodeNotAURL:
		return "is not a URL. Give " + example
	case CodeNoScheme:
		return "has no scheme (a bare host:port is read as a scheme). Give " + example
	case CodeUnsupportedScheme:
		return "uses a scheme this console is not served over. Use https, or http when something in front of the engine terminates TLS"
	case CodeUserInfoPresent:
		return "carries credentials. A browser address is " + example
	case CodePathPresent:
		return "carries a path. The console is served at the root of its origin, so a path here would print an address that does not answer. Give " + example
	case CodeQueryPresent:
		return "carries a query. A browser address is " + example
	case CodeFragmentPresent:
		return "carries a fragment. A browser address is " + example
	case CodeNoHost:
		return "has no host. Give " + example
	case CodePortOutOfRange:
		return "names a port a browser cannot open. A port is 1-65535; port 0 means \"let the kernel choose\" to a listener and nothing at all to a browser"
	case CodeHostZoneIdentifier:
		return "carries an IPv6 zone identifier. A zone identifier is local to one machine and is not part of an address a browser can be given"
	case CodeHostNotAnAddress:
		return "names a numeric host a browser cannot open. In a dotted numeric address no part but the last may exceed 255, and the last must be below 256 raised to (5 minus the number of parts)"
	case CodeHostNotAName:
		return "does not name a host a browser would accept: a host may not contain a space, a control byte, an empty label, or a character forbidden in a domain"
	default:
		return "is not an address this console can publish"
	}
}

// Refusal is why an operator-supplied address was refused. It is safe by
// construction: Field is a name this product chose and Code is a class, so
// Error() can carry no byte of the refused value.
type Refusal struct {
	// Field is the configuration field that supplied the value, e.g.
	// "--public-url" or "OLIVARES_PUBLIC_URL".
	Field string
	// Code is the failure class.
	Code Code
}

func (r *Refusal) Error() string {
	field := r.Field
	if field == "" {
		field = "the console address"
	}
	return fmt.Sprintf("%s %s.%s", field, r.Code.reason(), unshownNotice)
}

// refuse builds a refusal for one field.
func refuse(field string, code Code) *Refusal { return &Refusal{Field: field, Code: code} }
