// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"

	"github.com/olivaresai/olivares/core/webaddr"
)

// WHAT THE PANEL PRINTS, AND THE ONE PARAGRAPH THAT GOES WITH IT.
//
// The banner's job is to hand a first-time operator an address they can open and
// a token they can paste. Two things stopped it doing the first half:
//
//   - A BIND IS NOT AN ADDRESS. The flagship Compose file binds ":8443", which is
//     how you ask Go for the dual-stack wildcard, and the banner printed
//     "https://:8443". Seven live tutorial pages tell the reader to fetch exactly
//     that line out of `docker compose logs`.
//   - AN ADDRESS THAT WORKS IS NOT AN ADDRESS PASSKEYS WORK AT. The default
//     loopback bind prints "https://127.0.0.1:8443", which opens perfectly and at
//     which no browser will ever run a passkey ceremony, because a relying party
//     cannot be an IP. The operator discovers this later, at a step-up prompt,
//     with nothing on screen connecting the two.
//
// So: print the declared address when the operator declared one; otherwise print
// the bind, made openable, and SAY what it is. The paragraph is short, it is only
// printed when there is something to say, and it never claims that anything is
// reachable — reachability is not something this process can observe.

// consoleAddress is the resolved answer for one run: the address the panels
// print, and the advice that goes into the banner's transport slot.
type consoleAddress struct {
	// Browse is what a panel prints.
	Browse webaddr.Address
	// Declared is true when the operator named the address (flag or environment)
	// rather than it being derived from the bind.
	Declared bool
	// Advice is the paragraph block for the banner, or empty when the address
	// needs no explanation. Paragraphs are separated by a blank line. It is filled
	// by withPlan, once the resolved authentication plan is known.
	Advice string
	// AdviceAfterAddresses is Advice without its leading address list, for a
	// reader that has already printed the addresses itself. Identical to Advice
	// when there is no list.
	AdviceAfterAddresses string

	// Reachable is every address this host answers at for a WILDCARD bind, in the
	// order the banner prints them: routable addresses of this host first, then
	// loopback. It is EMPTY for a bind that is already an address — there is
	// nothing to enumerate — and it is a list of addresses this PROCESS holds,
	// never a claim about reachability (hostaddrs.go).
	Reachable []webaddr.Address
	// Container is true when this process can SEE that it runs in a container, in
	// which case the addresses above are the container's and the useful one is the
	// published port on a host this process cannot observe. False means "cannot
	// tell", never "is not".
	Container bool

	// wildcard and insecure are what the address was derived under, kept so the
	// paragraphs can be built later without re-deriving anything.
	wildcard bool
	insecure bool
}

// URL is the address a panel prints.
func (c consoleAddress) URL() string { return c.Browse.Origin }

// resolveConsoleAddress decides the printed address and builds its advice.
//
// insecure is the BACKEND transport, not a judgement about the declared address:
// a declared https address in front of a plaintext engine is what a
// TLS-terminating proxy looks like and is legitimate. It is named, never refused,
// and never assumed to exist.
func resolveConsoleAddress(declared webaddr.Address, listen string, insecure bool) consoleAddress {
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	out := consoleAddress{Browse: declared, Declared: !declared.IsZero()}
	var wildcard bool
	if !out.Declared {
		out.Browse, wildcard = webaddr.FromListen(listen, scheme)
	}
	out.wildcard, out.insecure = wildcard, insecure
	if wildcard {
		out.Reachable = hostConsoleAddresses(listen, scheme)
		out.Container = runningInContainer()
	}
	return out
}

// withPlan returns the address with the advice the RESOLVED authentication plan
// justifies. It is called at announce time, after boot, because that is the
// first moment the plan exists.
//
// THE PANEL USED TO GUESS, and the guess was wrong in both directions an
// independent review measured. It offered "https://localhost:8443, which is a
// name a browser will complete a ceremony against" to a deployment whose every
// ceremony leg answers 503, and to a deployment with a pinned relying party whose
// configured origins do not include localhost. Both sentences were confident and
// neither was true. An operator who follows a printed instruction and gets a
// generic failure has been sent somewhere by the product.
func (c consoleAddress) withPlan(plan webAuthnPlan) consoleAddress {
	// afterAddresses is every paragraph EXCEPT the address list. The banner prints
	// the whole thing; `olivares first-boot` prints the addresses in its own layout
	// and then these, so the list is never rendered twice on one screen.
	var afterAddresses []string
	if p := wildcardBindGuidance(c); p != "" {
		afterAddresses = append(afterAddresses, p)
	}
	if p := passkeyAddressAdvice(c.Browse, plan); p != "" {
		afterAddresses = append(afterAddresses, p)
	}
	if p := schemeTransportAdvice(c.Browse, c.Declared, c.insecure); p != "" {
		afterAddresses = append(afterAddresses, p)
	}
	paragraphs := afterAddresses
	if list := wildcardAddressList(c); list != "" {
		paragraphs = append([]string{list}, afterAddresses...)
	}
	c.Advice = strings.Join(paragraphs, "\n\n")
	c.AdviceAfterAddresses = strings.Join(afterAddresses, "\n\n")
	return c
}

// THE WILDCARD PARAGRAPHS, AND WHY THEY ARE TWO FUNCTIONS AND NOT ONE.
//
// A bind that accepts connections on every interface is explained in two parts:
// the DATA (which addresses it answers at) and the PROSE (what to do about them).
// They are separate because two readers compose them differently — the banner
// prints both, one after the other, while `olivares first-boot` prints the
// addresses in its own layout and then only the prose, so the list is never
// rendered twice on one screen (consolestate.go).
//
// The single paragraph they replaced said only "the URL above is the one that
// works on this machine", which is true and useless: the reader is usually in a
// terminal on a server, over SSH, and the one address the panel offered was the
// one address their browser cannot reach.
//
// Both halves are bounded the same way. The addresses come from the kernel's own
// view of this process's interfaces, so the text says "answers at" and never
// "reachable from": a firewall, a route or a NAT is not something this process
// can observe.

// wildcardAddressList is the DATA half: the addresses, and the sentence that says
// what they are.
func wildcardAddressList(c consoleAddress) string {
	if !c.wildcard {
		return ""
	}
	var b strings.Builder
	b.WriteString("This engine accepts connections on EVERY interface of this host (0.0.0.0, and\n" +
		"where the kernel has IPv6, ::). A bind is not an address, so the console answers\n" +
		"at each of these:")
	if len(c.Reachable) == 0 {
		// Enumeration failed or the host holds nothing but loopback. Say which
		// question went unanswered rather than printing an empty list.
		b.WriteString("\n  " + c.Browse.Origin + "\n" +
			"This process could not enumerate any other address of its own; open the console\n" +
			"at this host's name or address from wherever you are.")
		return b.String()
	}
	for _, addr := range c.Reachable {
		b.WriteString("\n  " + addr.Origin)
	}
	return b.String()
}

// wildcardBindGuidance is the PROSE half: what to do about those addresses. In a
// container the remedy is different in kind, so it is a different paragraph and
// not a hedge on the list.
func wildcardBindGuidance(c consoleAddress) string {
	if !c.wildcard {
		return ""
	}
	if c.Container {
		// The published port is the container runtime's choice and this process
		// cannot read it, so the sentence names the port the container listens on
		// and says the host's is whatever was mapped to it.
		return "Those are addresses INSIDE this container. From outside it, open the host port\n" +
			"mapped to " + c.portLabel() + " — https://<that host>:<published port> — and declare that\n" +
			"address with OLIVARES_PUBLIC_URL so this panel, and the passkey relying party,\n" +
			"use it. The one-time setup token is printed to this container's log and nowhere\n" +
			"else: read it back with `docker logs`, or mint a replacement with\n" +
			"`olivares first-boot --new-token` while no administrator exists."
	}
	return "A name is better than an address: set --public-url (or OLIVARES_PUBLIC_URL) to\n" +
		"the https:// URL your operators type and this panel prints that instead. Passkeys\n" +
		"need it — a relying party cannot be an IP."
}

// portLabel is the port this process listens on, as an operator reads it. An
// address whose port is the scheme default carries no port at all in its origin,
// which is correct for a URL and unhelpful in a sentence about port mapping.
func (c consoleAddress) portLabel() string {
	if c.Browse.Port != "" {
		return c.Browse.Port
	}
	if c.Browse.Scheme == "http" {
		return "80"
	}
	return "443"
}

// passkeyAddressAdvice names, before the operator ever clicks, the two reasons a
// browser will refuse a passkey ceremony at the address on screen. The refusal
// happens in the browser and never reaches the engine, so nothing in a log will
// explain it afterwards.
//
// The two reasons are separate paragraphs' worth of remedy and are kept apart:
// "this origin is not a secure context" and "this host cannot be a relying party"
// have different cures, and an operator told the wrong one changes the wrong
// thing.
func passkeyAddressAdvice(browse webaddr.Address, plan webAuthnPlan) string {
	if browse.IsZero() {
		return ""
	}
	// A secure context is the browser's own precondition and it applies whatever
	// the plan is, so it is answered first and on its own.
	if !browse.PotentiallyTrustworthy() {
		return "Passkeys will not work at that address: browsers run the passkey API only in a\n" +
			"secure context, and plain http away from the local host is not one. Serve the\n" +
			"console over https, or put something in front of it that terminates TLS and\n" +
			"declare that https address with --public-url (or OLIVARES_PUBLIC_URL)."
	}
	switch {
	case plan.Unusable:
		// A DECLARED address that cannot be a relying party. Offering localhost here
		// would be offering an address the engine refuses too: every leg answers
		// 503 until the declared address changes. The reason is branched, because
		// telling an operator who already declared a dotted name to "use a dotted
		// host name" is advice they have already taken.
		return "Passkeys are refused at this deployment: the declared address cannot be a\n" +
			"passkey relying party, so the engine answers every ceremony with a 503 rather\n" +
			"than deriving a relying party nobody chose.\n" +
			relyingPartyRemedy(browse) + "\n" +
			"Change the declared address and restart."

	case plan.RP.ID != "":
		// A PINNED relying party. Its origins are accepted configuration and may be
		// named; localhost is not among them unless the operator put it there, and
		// the verifier checks the origin exactly.
		if browseIsPinnedOrigin(browse, plan.RP.Origins) {
			return ""
		}
		return "Passkeys run at the origins pinned in OLIVARES_WEBAUTHN_ORIGINS, and the\n" +
			"address above is not one of them. A ceremony is verified against those exact\n" +
			"origins, so open the console at one of them — or add this address to that\n" +
			"list and restart."

	default:
		// PER-REQUEST derivation: the relying party follows the address the browser
		// used, so changing the address really does change the outcome, and the
		// localhost alternative is a real one.
		if browse.CanBeRelyingParty() {
			return ""
		}
		body := "Passkeys will not work at that address:\n" + relyingPartyRemedy(browse) + "\n"
		if browse.IsLoopback() {
			return body + "On this machine the same console also answers at\n" +
				"  " + browse.WithHost("localhost").Origin + "\n" +
				"and at that address the relying party is derived from the name, which the\n" +
				"verifier accepts."
		}
		return body + "Declare a usable address with --public-url (or OLIVARES_PUBLIC_URL)."
	}
}

// relyingPartyRemedy names WHY this host cannot be a relying party, and the three
// reasons are not interchangeable.
//
// The panel used to give one sentence — "a single-label host name is not one this
// build's verifier accepts" — and then tell the operator to use a dotted host
// name. For "my_host.example.com" or "-lead.example" both halves are wrong: the
// name already has a dot, this build's verifier accepts the string, and what
// refuses it is the strict-domain rule that a browser applies. An operator told
// their only defect is a missing dot will add a dot and fail again.
func relyingPartyRemedy(browse webaddr.Address) string {
	switch {
	case browse.IsIP():
		// The browser refuses this one, before any request leaves it.
		return "a browser will not run a passkey ceremony at an IP address. Reach the\n" +
			"console by a host name."
	case !strings.Contains(strings.TrimSuffix(browse.Host, "."), "."):
		// This build's verifier, not the specification: its rule for a non-IP is
		// "localhost, or something containing a dot".
		return "a single-label host name is one this build's verifier refuses. Use a\n" +
			"dotted host name, or localhost."
	default:
		// Dotted, and still not a valid domain: an underscore, a leading or
		// trailing hyphen, an empty label. Saying "use a dotted name" here would
		// be advice the operator has already taken.
		return "this host name is not a valid domain, so it cannot be a relying party\n" +
			"even though it has a dot: a label may carry only letters, digits and\n" +
			"inner hyphens. Use a name that does."
	}
}

// browseIsPinnedOrigin reports whether the address the panel prints is one of the
// origins the operator pinned. Compared on the canonical origin, which is what a
// browser sends and what the verifier compares.
func browseIsPinnedOrigin(browse webaddr.Address, origins []string) bool {
	for _, o := range origins {
		if o == browse.Origin {
			return true
		}
	}
	return false
}

// schemeTransportAdvice names a declared scheme that differs from what this
// process is actually serving. It is only ever a description: a proxy in front is
// the normal reason for the difference, this process cannot see whether one is
// there, and refusing the combination would break the deployment shape the whole
// change exists to serve.
func schemeTransportAdvice(browse webaddr.Address, declared, insecure bool) string {
	if !declared || browse.IsZero() {
		return ""
	}
	backend := "HTTPS"
	if insecure {
		backend = "plain HTTP"
	}
	if (browse.Scheme == "https") == !insecure {
		return ""
	}
	return "The declared address is " + browse.Scheme + " and this engine is serving " + backend + ".\n" +
		"That is a legitimate combination when something in front of the engine changes\n" +
		"the transport; this process only knows its own, and checks nothing about what is\n" +
		"in front of it."
}
