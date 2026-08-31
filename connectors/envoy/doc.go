// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package envoy is the L7 service-mesh observation connector for the Olivares AI
// R/RW access map (ARCHITECTURE.md; README.md modules III and IX). It is the L7 half of
// the network plane the kernel eBPF backstop cannot see: where Tetragon
// reports a 5-tuple + SNI it can only attribute to a process (ConfidenceApproximate,
// no identity), this connector reports the SAME egress with the mesh's service
// identity, the explicit FQDN, the HTTP method (→ R/RW) and — for ext_authz — the
// per-request authorization decision. It closes.
//
// It hosts the three Envoy observation gRPC services the data plane already speaks,
// configurable via the `services` setting:
//
//   - ALS — the Envoy gRPC AccessLogService (als.proto). Envoy STREAMS
//     its per-request access logs to this server; the connector turns each HTTP
//     entry into an observed L7 edge (TCP entries degrade to net.endpoint edges).
//   - ext_authz — the Envoy external authorization filter
//     (ext_authz_filter). Envoy asks this server to authorize each request; the
//     connector OBSERVES the request and ALWAYS RETURNS OK (it is read-first, never
//     an inline enforcer — docs/SECURITY-HARDENING.md). The verdict it records is the observation,
//     not a block.
//   - ext_proc — the Envoy external processor (external_processor.proto).
//     Envoy streams request/response phases (headers AND body) to this server,
//     giving body-level visibility (prompt / tool-args / response). The connector
//     ALWAYS RETURNS CONTINUE with NO mutation, and NEVER emits a raw body: a body
//     is scrubbed for secrets in memory (connectors/internal/redact) and only a
//     SHA-256 of the redacted detail travels in a finding (docs/SECURITY-HARDENING.md).
//
// # Read-first / do-not-interpose (docs/SECURITY-HARDENING.md)
//
// ext_authz and ext_proc are, by protocol, INLINE filters Envoy calls into. This
// connector reconciles that with the product's read-first posture by being an
// OBSERVER, never an enforcer: the ext_authz Check ALWAYS returns an OK response and
// the ext_proc Process ALWAYS returns CONTINUE with no header/body mutation. The
// operator deploys the filters with `failure_mode_allow: true`, so if this collector
// is down, production is unaffected — a collector failure must never break the agent
// data path. Inline enforcement, if ever wanted, is a separate governed (HITL)
// decision, never this connector's default. A test asserts the never-deny /
// never-mutate invariant.
//
// # Topology & privilege (ARCHITECTURE.md, docs/SECURITY-HARDENING.md)
//
// This is a collector that runs in the CLIENT's infra and PUSHES its observations to
// the core (CB-1 transport B/C); it exposes NO listener to the core. The ALS /
// ext_authz / ext_proc gRPC server it runs is a LOCAL data-plane endpoint the mesh's
// own Envoy sidecars connect to — the same receiver pattern as the cooperative OTLP
// receiver and the SSF receiver. It binds 127.0.0.1 BY DEFAULT and
// REFUSES a non-loopback bind unless `allow_public_bind=true` (the secure
// default), so it never silently opens a public port.
//
// Because it carries the Envoy gRPC dependency tree (go-control-plane), it ships
// OUT-OF-PROCESS as the `envoy-source` go-plugin binary (CB-1 transport B): its deps
// never link into the pure-Go core (ARCHITECTURE.md, §4). It imports only the SDK and the
// Apache internal helpers (meshobs, tracecontext, redact), never the engine
// (LICENSING.md).
//
// # What it emits
//
// Via connectors/internal/meshobs, an ALLOWED/forwarded request becomes an
// EdgeObservation (the OBSERVED side of the permitted-vs-observed diff, module III):
// OriginKind "identity" (a workload/service identity from the mTLS peer SPIFFE SVID —
// never a resolved "agent"), ResourceKind "http.api" keyed by the FQDN, Mode from the
// HTTP method, Source one of envoy_als / envoy_ext_authz / envoy_ext_proc,
// Confidence Attributed when the peer identity is mTLS-verified else Approximate. A
// DENIED ext_authz decision (which the connector observes but does not produce) and a
// secret detected in an ext_proc body become a FindingReport (anti-evasion / module
// IX). W3C Trace Context (traceparent) the request carries is extracted and handed to
// the deny-closed correlator seam, never embedded in an observation.
//
// # Minimal data (docs/SECURITY-HARDENING.md)
//
// Only edges and findings cross the boundary. The FQDN, method, port and service
// identity are structural metadata; an ext_proc body is scrubbed and hashed in
// memory and NEVER emitted raw; request headers are read only for the method/
// authority/path pseudo-headers and the traceparent, never persisted.
package envoy
