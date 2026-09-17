// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/netip"
	"strings"
	"testing"
)

// TestSplitListenPort is the table for the one thing the enumeration needs out of
// a bind spelling: the port. It is separate from webaddr.FromListen because the
// two answer different questions, and a bind with no port has no answer to this
// one.
func TestSplitListenPort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		listen string
		port   string
		ok     bool
	}{
		{":8443", "8443", true},
		{"0.0.0.0:8443", "8443", true},
		{"[::]:8443", "8443", true},
		{"[2001:db8::1]:8443", "8443", true},
		{"127.0.0.1:8443", "8443", true},
		{"  :8443  ", "8443", true},
		{"panel.example.com:443", "443", true},
		// No port at all: there is nothing to build an address with, and guessing
		// one would print a URL that does not answer.
		{"8443", "", false},
		{"", "", false},
		{"0.0.0.0:", "", false},
	}
	for _, c := range cases {
		_, port, ok := splitListenPort(c.listen)
		if ok != c.ok || port != c.port {
			t.Errorf("splitListenPort(%q) = %q,%v, want %q,%v", c.listen, port, ok, c.port, c.ok)
		}
	}
}

// TestHostConsoleAddressesShape measures the invariants of the enumeration against
// whatever interfaces this machine really has. It cannot assert WHICH addresses
// come back — that is the host's business, and a test that pinned them would only
// pass on the machine that wrote it — so it asserts the properties the banner
// depends on.
func TestHostConsoleAddressesShape(t *testing.T) {
	t.Parallel()
	got := hostConsoleAddresses(":8443", "https")
	if len(got) == 0 {
		t.Fatal("no address at all: loopback is unconditional, so this cannot be empty")
	}
	last := got[len(got)-1]
	if last.Origin != "https://127.0.0.1:8443" {
		t.Errorf("last address = %q, want loopback last", last.Origin)
	}
	seen := map[string]bool{}
	for i, a := range got {
		if seen[a.Origin] {
			t.Errorf("address %q enumerated twice", a.Origin)
		}
		seen[a.Origin] = true
		if !strings.HasPrefix(a.Origin, "https://") {
			t.Errorf("address %q does not carry the requested scheme", a.Origin)
		}
		if !strings.HasSuffix(a.Origin, ":8443") {
			t.Errorf("address %q does not carry the bound port", a.Origin)
		}
		if i == len(got)-1 {
			continue
		}
		if a.IsLoopback() {
			t.Errorf("loopback address %q before the end of the list", a.Origin)
		}
		// Nothing a browser cannot open may be offered. A link-local address needs
		// a zone identifier this panel has no way to spell.
		host := strings.TrimSuffix(strings.TrimPrefix(a.Origin, "https://"), ":8443")
		addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
		if err != nil {
			t.Errorf("enumerated host %q is not an IP address", host)
			continue
		}
		if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
			t.Errorf("address %q is one no browser can be sent to", a.Origin)
		}
	}
	// IPv4 before IPv6: an operator scanning the list wants the form they are most
	// likely to be able to type.
	sawV6 := false
	for _, a := range got[:len(got)-1] {
		isV6 := strings.HasPrefix(a.Origin, "https://[")
		if sawV6 && !isV6 {
			t.Errorf("IPv4 address %q comes after an IPv6 one; want IPv4 first", a.Origin)
		}
		sawV6 = sawV6 || isV6
	}
	// The scheme is the caller's, not a constant.
	if plain := hostConsoleAddresses(":8080", "http"); len(plain) == 0 || !strings.HasPrefix(plain[len(plain)-1].Origin, "http://") {
		t.Errorf("the requested scheme did not reach the enumerated addresses: %v", plain)
	}
	// A bind with no port enumerates nothing rather than inventing one.
	if bad := hostConsoleAddresses("8443", "https"); bad != nil {
		t.Errorf("a portless bind enumerated %v", bad)
	}
}

// TestRunningInContainerIsAHintNeverAProof locks the asymmetry that the banner's
// wording depends on: a positive is evidence, a negative is ignorance. The rows
// drive the environment half, which is the only half a test can arrange without
// root.
func TestRunningInContainerIsAHintNeverAProof(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	if !runningInContainer() {
		t.Error("a pod's injected service host did not count as a container")
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	// Without the variable the answer depends on the filesystem markers, which is
	// the point: this assertion is about the MARKERS being the only other input,
	// not about the verdict.
	if len(containerMarkers) == 0 {
		t.Error("no filesystem marker is consulted, so a plain Docker container would report false")
	}
	for _, marker := range containerMarkers {
		if !strings.HasPrefix(marker, "/") {
			t.Errorf("container marker %q is not an absolute path", marker)
		}
	}
}
