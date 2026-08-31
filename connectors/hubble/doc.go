// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package hubble is the Cilium Hubble flow observation connector for the Olivares AI
// R/RW access map (ARCHITECTURE.md; README.md modules III and IX). It closes
// and is the L7 EXTENSION of the eBPF backstop: where the eBPF
// connector consumes Tetragon's KERNEL events (a 5-tuple + SNI attributed to a
// process), this connector consumes Cilium's HUBBLE flows — the same egress seen at
// L7, with the destination FQDN, the HTTP method (→ R/RW) and, crucially, the
// network-policy VERDICT (allow/deny). It is the piece that turns the egress
// allowlist into a permitted-vs-observed diff: a forwarded flow is an OBSERVED edge,
// a DROPPED flow to a non-permitted FQDN is the anti-evasion / least-privilege signal
// (docs/SECURITY-HARDENING.md, §6).
//
// # Integration: Hubble Relay, read-first
//
// The connector is a gRPC CLIENT of the Hubble Relay Observer API (flow.proto /
// observer.proto). It calls Observer.GetFlows(Follow=true) and streams the flows
// Cilium already produces; it loads NO eBPF programs, requires NO kernel capability,
// and never writes policy — it OBSERVES (docs/SECURITY-HARDENING.md, §2). It runs in the CLIENT's
// infra alongside the relay and PUSHES its observations to the core (CB-1 transport
// B/C); it exposes no listener to the core. Because it carries the Cilium API
// dependency tree, it ships OUT-OF-PROCESS as the `hubble-source` go-plugin binary so
// those deps never link into the pure-Go core (ARCHITECTURE.md, §4).
//
// Secure by default (docs/SECURITY-HARDENING.md): it connects over TLS/mTLS (tlsx, the shared
// secure-default client config) when configured. Plaintext is accepted only to a
// LOCAL relay (a loopback address or a unix socket on the same node); a plaintext
// connection to a NON-local relay is refused unless allow_insecure_remote=true, so
// flow metadata is never sent cleartext across the network by accident.
//
// # What it emits
//
// Via connectors/internal/meshobs, each flow becomes:
//   - a FORWARDED flow → an EdgeObservation (OriginKind "identity" = the Cilium
//     source workload "namespace/pod"; ResourceKind "http.api" keyed by the
//     destination FQDN for an L7 HTTP flow, else "net.endpoint"; Mode from the HTTP
//     method; Source "hubble"; Confidence Approximate — the identity is label-derived
//     by the CNI, not a cryptographic peer cert, exactly as honest as the eBPF
//     backstop about attributing a workload, not an agent);
//   - a DROPPED flow → a FindingReport (egress denial; the drop reason rides the
//     redacted detail), the permitted-path violation the access map diffs against.
//
// To stay signal-dense it skips intra-cluster L3/L4 noise: a forwarded flow with no
// destination name and no L7 layer is dropped; egress (named destinations), L7 HTTP
// flows and ALL denials are kept. W3C Trace Context (traceparent) found in an L7 HTTP
// flow's headers is extracted and handed to the deny-closed correlator.
//
// # Minimal data (docs/SECURITY-HARDENING.md)
//
// Only edges and findings cross the boundary: the FQDN, port, HTTP method and source
// workload are structural metadata; a drop reason is scrubbed and a finding's detail
// is hashed; flow HTTP headers are read only for the traceparent, never persisted.
package hubble
