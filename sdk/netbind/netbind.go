// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package netbind is the single admission point for every listening socket this
// product opens.
//
// # Why this package exists
//
// The policy it enforces is not new. Before it had been written SEVEN
// separate times — once in the engine's serve path and once inside each of
// connectors/{aaa,claude,claude-managed-agents,cowork,envoy,ssf} — each with its
// own copy of the loopback classifier and its own wording. Three more listeners
// (connectors/{github,gitlab,tak}) had no copy at all, so they bound WILDCARD
// addresses in plaintext by default: the GitHub and GitLab webhook receivers on
// ":9800"/":9801", and TAK's CoT bearers with "0.0.0.0" as the documented
// example. That gap was found by the external the model contrast of PR #565
// (finding H-03) and is what this package closes.
//
// SIX of those seven are now gone. The seventh survives in the engine's serve path
// (cmd/olivares/cmd_serve.go, with nine callers) because that file is the subject of
// an open pull request and this work was scoped not to collide with it; it is
// declared residue with a follow-up, not a second policy anyone chose to keep. An
// earlier version of this paragraph said the count was down to one, which was
// wrong, and an external contrast caught it.
//
// Seven copies of a security decision are not a policy; they are seven chances to
// drift, and they drifted: three connector copies compared the host byte-for-byte
// against "localhost" and so refused a "LOCALHOST:9000" that does resolve to
// 127.0.0.1, while three others folded with strings.EqualFold and so ADMITTED
// "localhoſt" (U+017F), a host that is not the name they were checking for. (An
// earlier version of this sentence credited the engine's classifier with folding
// ASCII. It does not — cmd/olivares/cmd_serve.go:599 compares exactly. The ASCII
// fold exists only on the unmerged branch of the engine's own guard, and an
// external contrast caught the claim.) So the fix is
// not a guard in each of the three: it is ONE admission point that every socket
// passes through, and a repository-wide invariant test that fails when a new
// component opens a socket without it (see the socket-admission invariant in
// modules/security).
//
// # What converging the seven copies CHANGED
//
// Two behaviors moved, deliberately, and an earlier draft of this work claimed
// the migration "preserved semantics exactly". That was wrong, and an external
// contrast said so:
//
//   - Three connectors compared the host byte-for-byte against "localhost", so
//     they REFUSED a config saying "LOCALHOST:9000". They now admit it. A DNS
//     label is case-insensitive and that address does resolve to 127.0.0.1.
//   - Three folded with strings.EqualFold, so they ADMITTED "localhoſt" (U+017F).
//     They now refuse it. That host is not the name they were checking for.
//
// Both moves are toward the same single answer, and each is the correct one. But
// they ARE changes, and an operator whose config uses either spelling will see a
// different result than before.
//
// # The policy
//
// A listener may bind an address reachable from OFF-HOST only if its traffic is
// protected in transit, OR the operator has explicitly declared that it may not
// be. Loopback is classified apart and always admitted: that is what keeps
// development modes and local agent ingest working without ceremony.
//
// The escape hatch is deliberate and deliberately loud. Some real deployments do
// terminate TLS in front of the process (an ingress, a service mesh, a sidecar),
// and TAK's multicast ingest is off-host by its very nature. Those are supported
// — they just have to say so a second time and by name, which is the difference
// between an operator accepting a risk and an operator never being told about it.
//
// # Licensing
//
// This package lives in the Apache-2.0 SDK, and it has to: scripts/check-boundary.sh
// forbids connectors/ from importing the AGPL engine under /core, so the SDK is
// the only module both the engine and every connector may depend on. It uses the
// standard library only, per the SDK's zero-dependency contract.
package netbind

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// ErrPublicPlaintextBind is the sentinel every refusal from this package wraps,
// so a caller can distinguish "the operator configured an exposure we refused"
// from "the port was busy" without matching on message text.
var ErrPublicPlaintextBind = errors.New("refused: unprotected bind reachable off-host")

// Policy describes ONE listener: who is opening it, what it is for, whether its
// traffic crosses the network unprotected, and whether the operator has declared
// that they accept that.
type Policy struct {
	// Component names the thing opening the socket, as an operator knows it —
	// the connector name ("github") or "engine". It appears in the refusal.
	Component string

	// Purpose names this particular listener, for components that open more than
	// one ("webhook receiver", "CoT TCP bearer", "OTLP/gRPC receiver").
	Purpose string

	// Protected is true when this listener's traffic IS wrapped in transit — TLS
	// terminates on this socket. A protected listener may bind publicly with no
	// declaration; that is what lets the engine serve HTTPS on 0.0.0.0 by default.
	//
	// It is phrased as the POSITIVE so the zero value is the STRICT reading. The
	// field was `Plaintext bool` first, and an external contrast pointed out that
	// its zero value meant "protected": Policy{} admitted a wildcard, so a caller
	// who simply forgot the field got the permissive answer. A security default
	// that depends on remembering to fill in a struct field is not a default.
	Protected bool

	// AllowPublic is the operator's explicit declaration that this listener may
	// be reachable off-host anyway. It must come from a named, documented setting
	// they had to write, never from a default.
	AllowPublic bool

	// OptIn is the name of that setting, quoted back to the operator in the
	// refusal so the way forward is in the message that blocked them: the
	// connector config key ("allow_public_bind") or the engine flag
	// ("--insecure-allow-public-bind").
	OptIn string
}

// Check reports whether addr may be bound under p. It is the whole decision;
// Listen, ListenPacket and ListenMulticastUDP are Check plus the syscall.
//
// Callers that cannot use this package's constructors — because they hand an
// address to something else that binds it — call this first, and must do so
// BEFORE that bind: refusing after binding is not refusing.
func Check(addr string, p Policy) error {
	if p.Protected || IsLoopback(addr) {
		return nil
	}
	if p.AllowPublic {
		return nil
	}
	return refusal(addr, p)
}

// describe names the listener the way its operator knows it.
func describe(p Policy) string {
	what := p.Component
	if p.Purpose != "" {
		what += " " + p.Purpose
	}
	return what
}

// optInName is the setting quoted back to the operator as the way forward.
func optInName(p Policy) string {
	if p.OptIn == "" {
		return "allow_public_bind"
	}
	return p.OptIn
}

func refusal(addr string, p Policy) error {
	what := describe(p)
	optIn := optInName(p)
	// An empty address is the wildcard, but it prints as nothing at all — so name
	// it, or the operator reads a refusal about "" and learns less than before.
	shown := fmt.Sprintf("%q", addr)
	if addr == "" {
		shown = `"" (empty, which means the WILDCARD: every interface)`
	}
	return fmt.Errorf("%w: %s asked to bind %s, a non-loopback address reachable off-host: its traffic is not protected in transit, so request bodies, credentials and the signatures that authenticate them would cross the network in the clear. Bind a loopback address (127.0.0.1), put TLS in front of it, or — only if something already terminates TLS ahead of this listener — declare it with %s",
		ErrPublicPlaintextBind, what, shown, optIn)
}

// IsLoopback reports whether addr binds only the local host. It accepts
// "host:port", a bare host, or ":port".
//
// Everything it cannot positively identify as loopback is NOT loopback,
// including a resolvable hostname: a classifier that would have to ask the
// network what an address means can be told a different answer later, and this
// decision has to hold at bind time. Deny-closed costs an operator one explicit
// setting; fail-open costs them a public port they never knew they had.
//
// A wildcard bind — empty host, 0.0.0.0 or :: — is never loopback. It is the
// single most common way a listener becomes public by accident, and it is
// spelled like an absence rather than like an address, which is why it was the
// default in three connectors for as long as it was.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // no port present
	}
	if host == "" {
		return false // the wildcard
	}
	// ASCII fold, not strings.EqualFold. The name is a DNS label and therefore
	// case-insensitive, so "LOCALHOST:9000" in an operator config resolves to
	// 127.0.0.1 and must not be refused as public. But EqualFold applies UNICODE
	// simple folding, and U+017F (ſ, LATIN SMALL LETTER LONG S) is in the fold
	// orbit of "s": strings.EqualFold("localhoſt", "localhost") is TRUE. Since
	// this classifier decides whether a listener may serve at all, a spelling that
	// is not the name it checks for must not pass. Go's own net package folds
	// ASCII-only for exactly this reason ($GOROOT/src/net/parse.go).
	if asciiEqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// asciiEqualFold reports whether s and t are equal under ASCII-ONLY case
// folding. Comparing byte-wise also makes a multi-byte lookalike fail on length
// before anything else.
func asciiEqualFold(s, t string) bool {
	if len(s) != len(t) {
		return false
	}
	for i := 0; i < len(s); i++ {
		a, b := s[i], t[i]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

// Listen admits addr under p and only then opens a TCP listener. On refusal it
// returns a nil listener and never touches the network.
func Listen(ctx context.Context, network, addr string, p Policy) (net.Listener, error) {
	if err := Check(addr, p); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := confirmBound(addr, ln.Addr(), p); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// confirmBound re-checks the address the kernel ACTUALLY bound, and closes the
// listener if it is not one the policy would have admitted.
//
// Check classifies a string. For a literal IP those are the same thing, but
// "localhost" is a NAME, and the net package resolves it again when it binds —
// so a hosts file or resolver that maps localhost to 0.0.0.0 (or to a routable
// address) would pass a text classification and then bind off-host anyway.
// Nothing in the classifier can see that; only the bound socket can.
//
// Found by the external the model contrast of this branch, which traced
// Check -> IsLoopback -> ListenConfig.Listen -> internetAddrList -> lookupIPAddr
// and showed the two ends never compare notes. This is where they do. The
// listener is closed before it is returned, so a refused bind never serves.
func confirmBound(requested string, bound net.Addr, p Policy) error {
	if bound == nil || p.Protected || p.AllowPublic {
		return nil
	}
	if IsLoopback(bound.String()) {
		return nil
	}
	return fmt.Errorf("%w: %s asked for %q, which classified as loopback, but the kernel bound %q — a non-loopback address. The name resolved to something reachable off-host, so the listener was closed without serving. Bind a loopback IP literal (127.0.0.1) rather than a name, or declare the exposure with %s",
		ErrPublicPlaintextBind, describe(p), requested, bound.String(), optInName(p))
}

// ListenPacket admits addr under p and only then opens a packet socket.
func ListenPacket(ctx context.Context, network, addr string, p Policy) (net.PacketConn, error) {
	if err := Check(addr, p); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	pc, err := lc.ListenPacket(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := confirmBound(addr, pc.LocalAddr(), p); err != nil {
		_ = pc.Close()
		return nil, err
	}
	return pc, nil
}

// ListenMulticastUDP admits a multicast group join and only then performs it.
//
// A group join is off-host BY CONSTRUCTION: the group is a LAN address and the
// entire point is to receive datagrams other hosts send. So it is never
// classified by the listen address — "127.0.0.1:6969 joined to 239.2.3.1" is not
// a loopback listener, and treating it as one would slip an off-host receiver
// past the classification. Joining a group always requires the declaration.
func ListenMulticastUDP(network string, ifi *net.Interface, gaddr *net.UDPAddr, p Policy) (net.PacketConn, error) {
	if gaddr == nil {
		return nil, fmt.Errorf("netbind: %s: nil multicast group address", p.Component)
	}
	forced := p
	forced.Protected = false // a group join carries no transport protection
	if err := Check(gaddr.String(), forced); err != nil {
		return nil, err
	}
	return net.ListenMulticastUDP(network, ifi, gaddr)
}
