// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package netbind

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func isRefusal(err error) bool { return errors.Is(err, ErrPublicPlaintextBind) }

func testPolicy() Policy {
	return Policy{Component: "widget", Purpose: "webhook receiver", OptIn: "allow_public_bind"}
}

func TestLoopbackAddressesAreAdmittedWithoutAnOptIn(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1:8080", "127.0.0.53:8080", "[::1]:8080", "localhost:8080",
		// A DNS label is case-insensitive: an operator config saying LOCALHOST
		// resolves to 127.0.0.1 and must not be refused as public.
		"LOCALHOST:8080", "LocalHost:8080",
		"127.0.0.1", // no port
	} {
		if err := Check(addr, testPolicy()); err != nil {
			t.Errorf("Check(%q) refused a loopback bind: %v", addr, err)
		}
	}
}

func TestPublicAddressesArePlainlyRefused(t *testing.T) {
	for _, addr := range []string{
		"0.0.0.0:8080", ":8080", "[::]:8080", "192.0.2.7:8080", "[2001:db8::1]:8080",
		"", // empty == wildcard
	} {
		err := Check(addr, testPolicy())
		if err == nil {
			t.Errorf("Check(%q) admitted a plaintext public bind", addr)
			continue
		}
		for _, want := range []string{"widget", "webhook receiver", "allow_public_bind"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Check(%q) error must name %q; got: %v", addr, want, err)
			}
		}
	}
}

// A name that is NOT "localhost" must never be classified as loopback, even when
// Unicode case-folding would make it look like one. strings.EqualFold folds
// U+017F (ſ) into "s", so EqualFold("localhoſt","localhost") is TRUE — a
// classifier built on it would admit a host that does not resolve to 127.0.0.1.
func TestLookalikeHostnamesAreNotLoopback(t *testing.T) {
	for _, addr := range []string{
		"localhoſt:8080", // localhoſt
		"localhost.evil.example:8080",
		"notlocalhost:8080",
		"local host:8080",
		"locаlhost:8080", // Cyrillic а
	} {
		if IsLoopback(addr) {
			t.Errorf("IsLoopback(%q) = true; that host is not the name it imitates", addr)
		}
	}
}

// An unresolvable / malformed address must be refused, not admitted. A
// classifier that cannot tell what an address is has not shown it is safe.
func TestUnclassifiableAddressesAreRefused(t *testing.T) {
	for _, addr := range []string{"::::", "not an address:::9", "example.com:8080"} {
		if IsLoopback(addr) {
			t.Errorf("IsLoopback(%q) = true", addr)
		}
		if err := Check(addr, testPolicy()); err == nil {
			t.Errorf("Check(%q) admitted an address it could not classify as loopback", addr)
		}
	}
}

func TestAnExplicitDeclarationAdmitsAPublicBind(t *testing.T) {
	p := testPolicy()
	p.AllowPublic = true
	if err := Check("0.0.0.0:8080", p); err != nil {
		t.Errorf("a declared public bind must be admitted: %v", err)
	}
}

// The policy is about PLAINTEXT reaching the network. A listener that wraps its
// traffic in TLS is not the exposure this guard exists to stop, so it binds
// publicly without the dangerous-mode opt-in — that is what lets the engine
// serve HTTPS on 0.0.0.0 by default.
func TestATLSListenerNeedsNoOptInToBindPublicly(t *testing.T) {
	p := testPolicy()
	p.Protected = true
	if err := Check("0.0.0.0:8443", p); err != nil {
		t.Errorf("a TLS listener must bind publicly without an opt-in: %v", err)
	}
}

func TestListenRefusesBeforeItBinds(t *testing.T) {
	// The refusal must happen BEFORE the socket exists: refusing after binding is
	// not refusing. If a listener were opened first, the port would be taken and
	// this second bind on the same address would fail with EADDRINUSE instead of
	// succeeding.
	ln, err := Listen(context.Background(), "tcp", "0.0.0.0:0", testPolicy())
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen admitted a plaintext public bind")
	}
	if ln != nil {
		t.Fatal("Listen returned a listener alongside its refusal")
	}

	probe, perr := net.Listen("tcp", "127.0.0.1:0")
	if perr != nil {
		t.Fatalf("probe listen: %v", perr)
	}
	addr := probe.Addr().String()
	if cerr := probe.Close(); cerr != nil {
		t.Fatalf("probe close: %v", cerr)
	}
	good, err := Listen(context.Background(), "tcp", addr, testPolicy())
	if err != nil {
		t.Fatalf("Listen refused a loopback bind: %v", err)
	}
	_ = good.Close()
}

func TestListenPacketRefusesAPublicBind(t *testing.T) {
	pc, err := ListenPacket(context.Background(), "udp", "0.0.0.0:0", testPolicy())
	if err == nil {
		_ = pc.Close()
		t.Fatal("ListenPacket admitted a plaintext public bind")
	}
	good, err := ListenPacket(context.Background(), "udp", "127.0.0.1:0", testPolicy())
	if err != nil {
		t.Fatalf("ListenPacket refused a loopback bind: %v", err)
	}
	_ = good.Close()
}

// A multicast join is off-host BY CONSTRUCTION: the group is a LAN address and
// the purpose is to receive from other hosts. It must therefore be declared even
// when the LISTEN address is loopback — otherwise "127.0.0.1:6969 + group
// 239.2.3.1" would slip an off-host receiver past a loopback classification.
func TestMulticastIsNeverLoopbackWhateverTheListenAddressSays(t *testing.T) {
	gaddr := &net.UDPAddr{IP: net.ParseIP("239.2.3.1"), Port: 6969}
	pc, err := ListenMulticastUDP("udp", nil, gaddr, testPolicy())
	if err == nil {
		_ = pc.Close()
		t.Fatal("a multicast group join was admitted without a declaration")
	}
	if !strings.Contains(err.Error(), "allow_public_bind") {
		t.Errorf("refusal must name the opt-in; got: %v", err)
	}
}

// The refusal is what an operator reads at 3am. It must say what was refused,
// why it is dangerous, and every way out — including the one that unblocks them.
func TestTheRefusalTellsTheOperatorHowToProceed(t *testing.T) {
	err := Check("0.0.0.0:9800", Policy{
		Component: "github", Purpose: "webhook receiver",
		OptIn: "allow_public_bind",
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	// "non-loopback" and "loopback" are load-bearing, not decoration: they are the
	// words operators (and the connector tests that predate this package) search
	// for. The wording contract lives HERE, with the one implementation.
	for _, want := range []string{"github", "webhook receiver", "0.0.0.0:9800", "clear", "non-loopback", "127.0.0.1", "allow_public_bind"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q; got: %s", want, msg)
		}
	}
}

func TestRefusalIsIdentifiableByErrorsIs(t *testing.T) {
	err := Check("0.0.0.0:1", testPolicy())
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !isRefusal(err) {
		t.Error("a refusal must be matchable with errors.Is(err, ErrPublicPlaintextBind)")
	}
}

// The zero value must be the SAFE one. This was not true when the field was
// spelled `Plaintext bool`: Policy{} then meant "protected", so a caller who
// forgot the field got a wildcard admitted. An external contrast found it, and a
// security default that depends on remembering to fill in a struct field is not
// a default at all.
func TestTheZeroPolicyRefusesAPublicBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080", ""} {
		if err := Check(addr, Policy{}); err == nil {
			t.Errorf("Check(%q, Policy{}) admitted a public bind; the zero value must be the strict reading", addr)
		}
	}
	if err := Check("127.0.0.1:8080", Policy{}); err != nil {
		t.Errorf("the zero policy must still admit loopback: %v", err)
	}
}

// confirmBound is the second half of the guard: Check classifies a STRING, and
// for a name like "localhost" the net package resolves it again when it binds.
// A hosts file mapping localhost to 0.0.0.0 would pass the text classification
// and bind off-host anyway. Only the bound socket can reveal that, so it is
// tested on the bound address directly — rewriting /etc/hosts in a test would be
// both unportable and a decoy for the wrong thing.
func TestBoundAddressIsConfirmedAgainstThePolicy(t *testing.T) {
	type addrStub struct{ net.Addr }
	stub := func(s string) net.Addr { return fakeAddr(s) }

	if err := confirmBound("localhost:8080", stub("0.0.0.0:8080"), testPolicy()); err == nil {
		t.Error("a name that classified as loopback but bound the WILDCARD must be refused")
	} else if !isRefusal(err) {
		t.Errorf("must be a refusal: %v", err)
	}
	if err := confirmBound("localhost:8080", stub("192.0.2.7:8080"), testPolicy()); err == nil {
		t.Error("a name that bound a routable address must be refused")
	}
	if err := confirmBound("localhost:8080", stub("127.0.0.1:8080"), testPolicy()); err != nil {
		t.Errorf("an honest loopback resolution must be admitted: %v", err)
	}

	// A declared exposure and a protected listener were never relying on the
	// classification, so re-checking the bound address must not refuse them.
	declared := testPolicy()
	declared.AllowPublic = true
	if err := confirmBound("0.0.0.0:8080", stub("0.0.0.0:8080"), declared); err != nil {
		t.Errorf("a declared public bind must survive confirmation: %v", err)
	}
	protected := testPolicy()
	protected.Protected = true
	if err := confirmBound("0.0.0.0:8443", stub("0.0.0.0:8443"), protected); err != nil {
		t.Errorf("a protected listener must survive confirmation: %v", err)
	}
	_ = addrStub{}
}

// fakeAddr is a net.Addr whose String() is whatever the test needs the kernel to
// have reported.
type fakeAddr string

func (f fakeAddr) Network() string { return "tcp" }
func (f fakeAddr) String() string  { return string(f) }
