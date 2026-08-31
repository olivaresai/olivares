// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"net"
	"strconv"
)

// netTuple is the normalized destination of a network kprobe: the connection's
// remote address and port plus the optional cleartext SNI. The connector is BLIND
// TO THE TLS BODY — it carries only the 5-tuple destination and, when a TLS-SNI
// tracing policy provides it, the ClientHello hostname; never any payload.
type netTuple struct {
	dstIP string
	dport uint32
	sni   string
}

// tupleFromArgs extracts the destination tuple from a kprobe's args, preferring a
// sock argument and falling back to an skb argument, with the SNI taken from a
// string argument if present. It returns false when no network argument is found.
func tupleFromArgs(args []tetragonArg) (netTuple, bool) {
	for i := range args {
		if s := args[i].SockArg; s != nil {
			return netTuple{dstIP: s.Daddr, dport: s.Dport, sni: firstStringArg(args)}, true
		}
	}
	for i := range args {
		if s := args[i].SkbArg; s != nil {
			return netTuple{dstIP: s.Daddr, dport: s.Dport, sni: firstStringArg(args)}, true
		}
	}
	return netTuple{}, false
}

// endpointRef renders the resource reference for a network edge: tcp://<host>:<port>,
// where host is the SNI hostname when captured (more useful and stable than a
// rotating IP) and the destination IP otherwise. It returns "" when neither a
// host nor a port is known, so an empty connection event yields no edge.
func (t netTuple) endpointRef() string {
	host := t.sni
	if host == "" {
		host = t.dstIP
	}
	if host == "" || t.dport == 0 {
		return ""
	}
	return "tcp://" + net.JoinHostPort(host, strconv.FormatUint(uint64(t.dport), 10))
}

// matchesEndpoint reports whether the tuple's destination IP:port is in the set of
// cooperative-telemetry endpoints (used by the anti-evasion detector to recognize
// an agent contacting its OTLP collector). It matches on IP:port, the form a
// kernel connect exposes; an SNI is not matched here.
func (t netTuple) matchesEndpoint(endpoints []string) bool {
	if t.dstIP == "" || t.dport == 0 {
		return false
	}
	target := net.JoinHostPort(t.dstIP, strconv.FormatUint(uint64(t.dport), 10))
	for _, e := range endpoints {
		if e == target {
			return true
		}
	}
	return false
}
