// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net"
	"os"
	"strings"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/webaddr"
)

// WHAT A WILDCARD BIND CAN HONESTLY BE TURNED INTO, AND WHAT IT CANNOT.
//
// The banner's job is to give an operator an address to open. With a wildcard
// bind there is no single answer, and the panel used to print the only one it was
// sure of — https://localhost:8443 — to a person sitting in front of a different
// machine. That is the anti-pattern this file exists to end: a first-run panel
// that prints an address which is correct for the process and useless to its
// reader.
//
// THE BOUND OF WHAT THIS CAN CLAIM, and it is the whole reason the wording below
// is careful: enumerating interfaces tells us which addresses this PROCESS holds.
// It does not tell us
//
//   - whether a firewall drops the port,
//   - whether the operator's machine can route to any of them,
//   - or, in a container, whether the address means anything outside the container.
//
// So nothing here says "reachable". The panel says "this host answers at", lists
// what the kernel reports, and names the container case separately, because in a
// container the true answer is the PUBLISHED port on a host this process cannot
// see at all.

// hostConsoleAddresses returns the addresses a browser could be pointed at for a
// WILDCARD bind, in the order an operator wants to read them: routable addresses
// of this host first (IPv4 before IPv6, each sorted), then loopback last.
//
// Addresses no browser can use are dropped rather than printed with a caveat:
// link-local (IPv4 169.254/16 and IPv6 fe80::/10, which a browser cannot open
// without a zone identifier the panel has no way to spell), the unspecified
// address, and multicast. An interface that is administratively down contributes
// nothing.
//
// A failure to enumerate returns nil and no error: this is a decoration on a
// banner, and a boot must not fail because a netlink query did.
func hostConsoleAddresses(listen, scheme string) []webaddr.Address {
	_, port, ok := splitListenPort(listen)
	if !ok {
		return nil
	}
	addrs := secure.LocalLANAddresses()

	out := make([]webaddr.Address, 0, len(addrs)+1)
	for _, addr := range addrs {
		if a, _ := webaddr.FromListen(net.JoinHostPort(addr.String(), port), scheme); !a.IsZero() {
			out = append(out, a)
		}
	}
	// Loopback last, and always: it is the address that works for somebody in a
	// terminal on this machine, which is exactly who reads a first-boot banner
	// over SSH.
	if a, _ := webaddr.FromListen(net.JoinHostPort("127.0.0.1", port), scheme); !a.IsZero() {
		out = append(out, a)
	}
	return out
}

// splitListenPort extracts the port from a bind spelling. It is deliberately
// separate from webaddr.FromListen: that function answers "what URL do I print",
// this one answers "which port did we bind", and a bind with no port at all has
// no answer to the second.
func splitListenPort(listen string) (host, port string, ok bool) {
	listen = strings.TrimSpace(listen)
	i := strings.LastIndex(listen, ":")
	if i < 0 {
		return "", "", false
	}
	host, port = listen[:i], listen[i+1:]
	if port == "" {
		return "", "", false
	}
	return host, port, true
}

// containerMarkers are the files a container runtime leaves in the filesystem it
// gives the process. They are HINTS, and the panel treats them as such: their
// presence means "you are in a container and the addresses above are the
// container's", their absence means nothing at all — a containerd/CRI pod has
// neither file, and this must never be read as proof of running on the host.
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// runningInContainer reports whether this process can SEE that it is in a
// container. False means "cannot tell", never "is not".
func runningInContainer() bool {
	for _, marker := range containerMarkers {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	// Kubernetes injects this into every pod that has the default service
	// account's environment, which is the shape our Helm chart ships.
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}
