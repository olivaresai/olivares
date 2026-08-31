// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrOffPin reports a dial to an address the authorization did not cover.
var ErrOffPin = errors.New("egress: dial target is not the authorized address")

type pinKey struct{}

type liftedKey struct{}

// WithReservedAuthorization records the addresses an operator authorized BY ADDRESS,
// so a dialer may reach those past its reserved-address floor and no others.
//
// It carries a SET rather than a flag, and that is the correctness of the whole
// air-gapped case. A flag said "an operator rule permitted this destination", and a
// HOST rule permits a NAME: the addresses come from DNS. So a rule for
// `siem.internal` lifted the floor for whatever that name resolved to — including the
// metadata service, which the operator never wrote.
//
// It ALWAYS writes, including an empty set. An earlier version returned the parent
// context unchanged for the negative case, so a value inherited from an outer context
// could not be cleared — a shape that is safe only for as long as nobody nests.
func WithReservedAuthorization(ctx context.Context, ips []net.IP) context.Context {
	cp := make([]net.IP, len(ips))
	copy(cp, ips)
	return context.WithValue(ctx, liftedKey{}, cp)
}

// ReservedAuthorizedFor reports whether addr's IP is one the operator authorized by
// address on this request.
func ReservedAuthorizedFor(ctx context.Context, addr string) bool {
	lifted, _ := ctx.Value(liftedKey{}).([]net.IP)
	if len(lifted) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, l := range lifted {
		if l.Equal(ip) {
			return true
		}
	}
	return false
}

// WithPin attaches the addresses an authorization permitted to a context, so the
// dialer that eventually opens the connection can refuse anything else.
//
// This closes the gap between deciding and connecting. A policy authorizes a NAME;
// resolving that name again at dial time asks the same question of DNS a second
// time, and the second answer is whoever controls the zone to choose. Between the
// two lookups the record can change — that is not an exotic attack, it is the
// documented behavior of a short TTL. Pinning makes the decision and the connection
// refer to the same machine.
//
// An empty or nil pin attaches nothing, and that guard is LOAD-BEARING rather than
// defensive: Evaluate returns Permitted with a nil pin whenever no policy is in
// force, which is the default estate. Installing a pin there would forbid every
// address on exactly the deployments that asked for no policy at all.
func WithPin(ctx context.Context, ips []net.IP) context.Context {
	if len(ips) == 0 {
		return ctx
	}
	cp := make([]net.IP, len(ips))
	copy(cp, ips)
	return context.WithValue(ctx, pinKey{}, cp)
}

// PinOf returns the addresses pinned on a context, or nil.
func PinOf(ctx context.Context) []net.IP {
	pinned, _ := ctx.Value(pinKey{}).([]net.IP)
	return pinned
}

// Dialer is the net.Dialer method DialPinned needs.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// DialPinned opens a connection to addr, SUBSTITUTING a pinned address for the host
// when the context carries a pin. Without a pin it dials addr unchanged, because the
// pin is an additional constraint: a caller that authorized nothing is not made
// safer by a dialer that refuses everything, only broken.
//
// Substituting is the point, and it is what a bare check cannot do. The address an
// http.Transport hands its dialer is "hostname:port" — the transport does not
// resolve the name, net.Dialer does, inside the dial. So a dialer that only VERIFIED
// its argument would be looking at a name and would have to either refuse it (which
// breaks every destination addressed by name — that is, all of them) or let the
// resolution it cannot see decide the machine. Dialing the pinned address directly
// means the name is resolved exactly once, at authorization, and the connection goes
// to the machine that was authorized.
//
// The original hostname is not lost: an http.Transport takes the TLS server name and
// the Host header from the request URL, not from the dial address, so SNI and
// virtual hosting keep working while the connection lands on a pinned address.
//
// An addr that is ALREADY an IP literal is verified rather than substituted — that
// is a destination the operator addressed by address, and the pin is then a
// consistency check on it.
func DialPinned(ctx context.Context, d Dialer, network, addr string) (net.Conn, error) {
	pinned := PinOf(ctx)
	if len(pinned) == 0 {
		return d.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: unparseable dial address", ErrOffPin)
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, p := range pinned {
			if p.Equal(ip) {
				return d.DialContext(ctx, network, addr)
			}
		}
		return nil, fmt.Errorf("%w: %s was not among the authorized addresses", ErrOffPin, ip)
	}
	// A name: dial the authorized addresses in order rather than resolving it again.
	// Trying each is what keeps a multi-homed destination working — refusing after
	// the first failure would make a policy that authorized four addresses behave
	// like one that authorized the first.
	//
	// Each attempt gets a SHARE of the remaining time, not all of it. Giving the
	// first address the whole budget is how a serial fallback silently becomes a
	// single-address policy: with a 5s dial timeout inside a 10s attempt deadline,
	// two dead addresses consume everything and the third — the live one — fails
	// instantly, deterministically, on every retry, until the ladder dead-letters a
	// destination that is up. The standard library divides the deadline for exactly
	// this reason (net.Dialer's partialDeadline); this reimplements the dial, so it
	// has to reimplement the sharing too.
	var lastErr error
	for i, p := range pinned {
		attemptCtx, cancel := partialDeadline(ctx, len(pinned)-i)
		conn, derr := d.DialContext(attemptCtx, network, net.JoinHostPort(p.String(), port))
		cancel()
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: the pin was empty", ErrOffPin)
	}
	return nil, lastErr
}

// partialDeadline gives one of `remaining` attempts an equal share of whatever time
// is left. A context with no deadline is returned unchanged with a no-op cancel:
// slicing an unbounded budget would invent a limit the caller never asked for.
func partialDeadline(ctx context.Context, remaining int) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok || remaining <= 1 {
		return ctx, func() {}
	}
	left := time.Until(deadline)
	if left <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, left/time.Duration(remaining))
}
