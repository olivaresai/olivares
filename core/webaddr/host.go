// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package webaddr

import (
	"math"
	"net/netip"
	"strconv"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"golang.org/x/net/idna"
)

// THE HOST PARSER, AND THE TWO QUESTIONS IT KEEPS APART.
//
// 1. "Would a browser open this?" — the URL Standard's host parser. It is
//    permissive on purpose: browsers call domain-to-ASCII with beStrict FALSE, so
//    UseSTD3ASCIIRules and CheckHyphens are OFF and an underscore is not a
//    forbidden host code point. https://my_host.example.com is an ordinary
//    internal name that every browser opens.
//
// 2. "Can this be a WebAuthn relying party?" — a STRICTER question with a
//    different answer, and conflating the two is the defect this package exists to
//    remove. WebAuthn L3 requires the RP ID to be a valid domain, and the URL
//    Standard defines "valid domain" as domain-to-ASCII with beStrict TRUE. So the
//    underscore host above is a fine browser address and is NOT a valid RP domain.
//
// On top of (2) sits a third rule that is neither the URL Standard's nor
// WebAuthn's: what THIS BUILD's verifier accepts. go-webauthn v0.17.4's
// protocol.ValidateRPID accepts any net.ParseIP-able value (browsers do not) and
// refuses a single-label name unless it is exactly "localhost". That is a
// property of the installed library, not a property of the specification, and it
// is quoted as such wherever this package speaks about it.
//
// Two profiles, therefore, and never one. Option ORDER matters: MapForLookup sets
// StrictDomainName(true) and ValidateLabels(true) internally, so the two relaxing
// options must come AFTER it or they are silently overwritten.

var (
	// browserDomains is domain-to-ASCII with beStrict = false: what a browser will
	// accept as a typed host. ValidateLabels stays ON (it comes from MapForLookup
	// and is what rejects an empty label), CheckJoiners stays ON with it, and the
	// two STD3-era restrictions are turned off.
	browserDomains = idna.New(
		idna.MapForLookup(),
		idna.BidiRule(),
		idna.StrictDomainName(false),
		idna.CheckHyphens(false),
	)
	// strictDomains is domain-to-ASCII with beStrict = true: the URL Standard's
	// "valid domain" predicate, which WebAuthn L3 requires of a relying party ID.
	strictDomains = idna.New(
		idna.MapForLookup(),
		idna.BidiRule(),
		idna.VerifyDNSLength(true),
	)
)

// Kind is what the URL Standard's host parser decided a host is.
type Kind uint8

const (
	// KindNone is the zero Address: no host was named.
	KindNone Kind = iota
	// KindDomain is an ASCII A-label domain.
	KindDomain
	// KindIPv4 is a canonical dotted quad.
	KindIPv4
	// KindIPv6 is a canonical IPv6 literal, stored WITHOUT brackets.
	KindIPv6
)

// String renders the kind for the classification table the console mirrors.
func (k Kind) String() string {
	switch k {
	case KindDomain:
		return "domain"
	case KindIPv4:
		return "ipv4"
	case KindIPv6:
		return "ipv6"
	default:
		return "none"
	}
}

// forbiddenInDomain reports whether r is a forbidden domain code point: the URL
// Standard's forbidden host code points, plus every C0 control, U+007F and "%".
func forbiddenInDomain(r rune) bool {
	switch r {
	case 0x7F, ' ', '#', '/', ':', '<', '>', '?', '@', '[', '\\', ']', '^', '|', '%':
		return true
	}
	return r < 0x20
}

// ParseHost runs the URL Standard's host parser over one host component and
// returns its canonical serialization and kind. A bracketed input is an IPv6
// literal; the brackets are not part of the returned host.
//
// The returned Code is zero on success. It is a class, never a value: this
// function is also the one place that decides whether a numeric host is an
// address a browser can open or a URL a browser rejects outright.
func ParseHost(host string) (string, Kind, Code) {
	if host == "" {
		return "", KindNone, CodeNoHost
	}
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return "", KindNone, CodeHostNotAName
		}
		literal := host[1 : len(host)-1]
		if strings.ContainsRune(literal, '%') {
			// A zone identifier is meaningful only on the machine that owns the
			// interface. It is not part of an address anybody can be given.
			return "", KindNone, CodeHostZoneIdentifier
		}
		addr, err := netip.ParseAddr(literal)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", KindNone, CodeHostNotAnAddress
		}
		return serializeIPv6(addr.As16()), KindIPv6, 0
	}
	for _, r := range host {
		if forbiddenInDomain(r) {
			if r == ' ' || r < 0x20 || r == 0x7F {
				return "", KindNone, CodeControlCharacter
			}
			return "", KindNone, CodeHostNotAName
		}
	}
	ascii, err := browserDomains.ToASCII(host)
	if err != nil || ascii == "" {
		return "", KindNone, CodeHostNotAName
	}
	for _, r := range ascii {
		if forbiddenInDomain(r) {
			return "", KindNone, CodeHostNotAName
		}
	}
	if erasesALabel(host) {
		// MEASURED, and it is the one place x/net decodes to something a browser
		// refuses: a label whose punycode payload is empty comes back as "" with a
		// NIL error, so a label the operator wrote is erased rather than rejected.
		// The installed Node implementation refuses those inputs outright.
		return "", KindNone, CodeHostNotAName
	}
	if !endsInANumber(ascii) {
		return ascii, KindDomain, 0
	}
	quad, ok := parseIPv4(ascii)
	if !ok {
		// The URL Standard's IPv4 parser returned failure, and a host-parse failure
		// makes the WHOLE URL invalid: a browser cannot open this at all. The old
		// shape predicate ("is the last label all digits?") accepted 256.1.1.1 and
		// 99999999999999999999.1 and printed them as the console's address.
		return "", KindNone, CodeHostNotAnAddress
	}
	return quad, KindIPv4, 0
}

// endsInANumber is the URL Standard's "ends in a number" checker: it decides
// whether a host goes to the IPv4 parser or stays a domain. A trailing empty
// label (the root dot) is dropped before the last part is examined, which is why
// "panel.example.com." stays a domain.
func endsInANumber(host string) bool {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last == "" {
		return false
	}
	if onlyASCIIDigits(last) {
		return true
	}
	// A number too large to be an address is still a number, so the host still
	// goes to the IPv4 parser — which refuses it, which makes the whole URL
	// invalid. That is what a browser does with it.
	_, isNumber, _ := parseIPv4Number(last)
	return isNumber
}

func onlyASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// parseIPv4Number is the URL Standard's IPv4-number parser: decimal by default,
// hexadecimal after "0x"/"0X", octal after a leading "0".
//
// THREE OUTCOMES, NOT TWO, and the distinction is the whole point. The standard's
// parser is arbitrary-precision — it returns "the mathematical integer value"
// — so a number too large for any address is still a NUMBER. Collapsing "not a
// number" and "a number that does not fit" into one failure is what let
// "https://0xffffffffffffffffffff" through as a DOMAIN: ends-in-a-number said no,
// the host never reached the IPv4 parser, and a URL the installed WHATWG
// implementation refuses outright was accepted — two of them as valid relying
// parties. Found by an independent review's overlay probe, confirmed against Node,
// and it is the only case measured where this parser accepted an address a
// browser rejects. The values themselves are still bounded by uint64: every bound
// the caller checks fits, so an overflowed value is simply reported as overflowed
// instead of being wrapped or truncated.
func parseIPv4Number(input string) (value uint64, isNumber, overflowed bool) {
	if input == "" {
		return 0, false, false
	}
	radix := uint64(10)
	switch {
	case len(input) >= 2 && (strings.HasPrefix(input, "0x") || strings.HasPrefix(input, "0X")):
		radix, input = 16, input[2:]
	case len(input) > 1 && input[0] == '0':
		radix, input = 8, input[1:]
	}
	if input == "" {
		return 0, true, false
	}
	var n uint64
	for i := 0; i < len(input); i++ {
		var d uint64
		c := input[i]
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			return 0, false, false
		}
		if d >= radix {
			return 0, false, false
		}
		if overflowed || n > (math.MaxUint64-d)/radix {
			// Keep scanning: the remaining characters still decide whether this is
			// a number at all, and "0xZZ" must stay NOT A NUMBER rather than
			// becoming an overflowed one.
			overflowed = true
			continue
		}
		n = n*radix + d
	}
	return n, true, overflowed
}

// parseIPv4 is the URL Standard's IPv4 parser. It returns the canonical dotted
// quad a browser would display, or failure — and failure means the browser
// refuses the URL, it does not mean "treat it as a name".
func parseIPv4(host string) (string, bool) {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return "", false
	}
	numbers := make([]uint64, 0, len(parts))
	for _, p := range parts {
		n, isNumber, overflowed := parseIPv4Number(p)
		if !isNumber || overflowed {
			// An overflowed part cannot satisfy either bound below, so it fails
			// here rather than being carried as a value nobody can compare.
			return "", false
		}
		numbers = append(numbers, n)
	}
	for _, n := range numbers[:len(numbers)-1] {
		if n > 255 {
			return "", false
		}
	}
	last := numbers[len(numbers)-1]
	// bound = 256 ** (5 - len(numbers)); len(numbers) is 1..4 so it fits in uint64.
	bound := uint64(1)
	for i := 0; i < 5-len(numbers); i++ {
		bound *= 256
	}
	if last >= bound {
		return "", false
	}
	value := last
	for i, n := range numbers[:len(numbers)-1] {
		value += n << (8 * (3 - uint(i)))
	}
	return netip.AddrFrom4([4]byte{
		byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value),
	}).String(), true
}

// IsRelyingPartyDomain answers question (2) above for an already-canonical host: is
// this a valid domain in the URL Standard's strict sense, AND does the verifier
// compiled into this build accept it as a relying-party ID?
//
// Both halves are load-bearing and neither implies the other:
//   - the strict half refuses "my_host.example.com" (STD3) and "256.1.1.1" is
//     never reached here because it is not a domain at all;
//   - the library half refuses a single-label name other than exactly "localhost",
//     and ACCEPTS an IP, which is why callers ask Kind first.
//
// The trailing root dot is dropped before the strict check: the empty root label
// is not a DNS label, and the verifier and browsers both accept the dotted form.
func IsRelyingPartyDomain(host string) bool {
	if host == "" {
		return false
	}
	// ONE trailing dot is the root label and is not a label; a SECOND one is an
	// empty label that is not the root, and a domain may not have one. x/net's
	// length check does not catch it — measured: "example.." and
	// "panel.example.com.." came through as valid relying parties, which the URL
	// Standard's strict domain rule does not allow. Interior empty labels
	// ("a..b.example") x/net already refuses on its own.
	probe := strings.TrimSuffix(host, ".")
	if probe == "" || strings.HasSuffix(probe, ".") {
		return false
	}
	if _, err := strictDomains.ToASCII(probe); err != nil {
		return false
	}
	return protocol.ValidateRPID(host) == nil
}

// serializeIPv6 is the URL Standard's IPv6 serializer: eight lowercase hex
// pieces, the LONGEST run of more than one zero piece compressed to "::", ties to
// the leftmost run.
//
// It is written out rather than delegated to netip.Addr.String because of exactly
// one input class, and that class is measurable: an IPv4-mapped address. netip
// prints "::ffff:127.0.0.1" and a browser shows "[::ffff:7f00:1]". Everywhere
// else the two agree (netip follows RFC 5952, which the URL Standard's serializer
// matches), and the differential test against the installed Node implementation
// is what keeps that claim honest rather than assumed.
func serializeIPv6(b [16]byte) string {
	var pieces [8]uint16
	for i := range pieces {
		pieces[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	bestStart, bestLen, curStart, curLen := -1, 0, -1, 0
	for i, p := range pieces {
		if p != 0 {
			curStart, curLen = -1, 0
			continue
		}
		if curStart < 0 {
			curStart = i
		}
		curLen++
		if curLen > bestLen {
			bestStart, bestLen = curStart, curLen
		}
	}
	if bestLen < 2 {
		bestStart, bestLen = -1, 0
	}
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		if i == bestStart {
			sb.WriteString("::")
			i += bestLen - 1
			continue
		}
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), ":") {
			sb.WriteByte(':')
		}
		sb.WriteString(strconv.FormatUint(uint64(pieces[i]), 16))
	}
	return sb.String()
}

// erasesALabel reports whether domain-to-ASCII would turn a label the operator
// WROTE into nothing.
//
// THE PREVIOUS VERSION MATCHED ONE SPELLING AND THAT WAS NOT ENOUGH. It looked
// for the literal four bytes "xn--", so every compatibility spelling walked past
// it and UTS-46 then erased the label anyway: the fullwidth form, a fullwidth
// hyphen, a soft hyphen inside or in front. An independent review showed the
// consequence — "https://panel.<fullwidth xn-->" was accepted as "https://panel."
// with canBeRP true, and an explicit pin written that way was SILENTLY REWRITTEN
// to the relying-party ID "panel.". A pin an operator typed must never be
// replaced by something else, and Node refuses every one of those inputs.
//
// So the question is asked of the MAPPED label rather than of its spelling, with
// the same profile the rest of this parser uses:
//
//	a label that was empty to begin with      -> left alone; browsers accept it
//	a label that maps to something            -> fine
//	a NON-EMPTY label that maps to nothing    -> two very different causes
//
// The two causes have to be told apart, because one is legal and the other is
// not. A label made only of UTS-46-IGNORED code points (a soft hyphen, say)
// legitimately maps to nothing and a browser accepts it. A label whose punycode
// payload is empty also maps to nothing, and a browser refuses it.
//
// THE PROBE. Prepend an ASCII sentinel and map again. For ignored-only input the
// result is EXACTLY the sentinel, because everything else was dropped. For an
// emptied ACE payload the sentinel fuses with the label's own characters and the
// result is something longer — "xn--" behind the sentinel is no longer an ACE
// prefix, so the mapping keeps it. Any mapping error at either step refuses.
//
// The sentinel is a VALIDATION PROBE and never leaves this function: the host
// that goes forward is the one the parser already produced, and nothing here
// rewrites an authority.
func erasesALabel(host string) bool {
	for _, label := range splitDomainLabels(normalizeSeparators(host)) {
		if label == "" {
			continue
		}
		mapped, err := browserDomains.ToASCII(label)
		if err != nil {
			return true
		}
		if mapped != "" {
			continue
		}
		probed, err := browserDomains.ToASCII(labelProbeSentinel + label)
		if err != nil || probed != labelProbeSentinel {
			return true
		}
	}
	return false
}

// labelProbeSentinel is an ASCII label that maps to itself under the browser
// profile and carries no ACE prefix, so anything left beside it after mapping
// came from the label under test.
const labelProbeSentinel = "a"

// normalizeSeparators rewrites the code points UTS-46 maps to a label separator
// so the labels can be split on one character. It is used only to ASK the
// question above; the parsed host keeps whatever the operator wrote.
func normalizeSeparators(host string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u3002', '\uff0e', '\uff61':
			return '.'
		}
		return r
	}, host)
}

// splitDomainLabels splits on every code point UTS-46 maps to a label separator,
// not just on U+002E. Splitting on "." alone would miss the separator a host was
// actually written with.
func splitDomainLabels(host string) []string {
	return strings.FieldsFunc(host, func(r rune) bool {
		switch r {
		case '.', '\u3002', '\uff0e', '\uff61':
			return true
		}
		return false
	})
}
