// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package egressproxy is the Olivares AI connector for an agent EGRESS PROXY's
// verdict log. It OBSERVES the allow/deny decisions an
// already-deployed egress proxy WROTE — it never builds a proxy, never opens a
// network listener, never makes an outbound connection, and never decrypts
// traffic. It is a PURE FILE PARSER (session constraint
// "no construir proxy").
//
// # What it observes
//
// A modern agent sandbox confines an autonomous agent's network reach behind a
// man-in-the-middle egress proxy that allow/denies every outbound connection by an
// FQDN allowlist. Anthropic's containment writeup describes exactly this surface:
// "We fixed it using a defensive man-in-the-middle proxy inside the VM that
// intercepts traffic to our API"; "Previously, we'd conceptualized the allowlist as
// a destination filter, something that told Claude these domains are okay to talk
// to"; and a verdict is rendered per connection — "The egress proxy checked the
// destination, saw api.anthropic.com, and let it through" (anthropic.com/engineering/
// how-we-contain-claude, verified 2026-06-06). The proxy's allowlist IS the declared
// permitted-path set; its verdict log is the OBSERVED egress, so the two sides of
// the permitted-vs-observed diff (ARCHITECTURE.md, module III) come from the same control.
//
// # Read-first, minimal-data posture (docs/SECURITY-HARDENING.md, §3)
//
// The operator ships the verdict log the proxy already emits; this connector tails
// or batch-reads that file. It records only STRUCTURAL metadata — the source
// workload/agent identity, the destination FQDN (+ optional port), the allow/deny
// verdict and a short reason — NEVER a request/response body, a header value, an
// API key or any payload. A free-text reason is scrubbed for secrets
// (connectors/internal/redact) before it ever travels, and a finding's detail is
// reduced to a SHA-256; there is a negative test that a secret embedded in a reason
// field never survives into an emitted observation. The connector opens NO listener
// and dials NOTHING (a test asserts the package never references net.Listen): the
// only syscall surface is reading the configured file/dir.
//
// # The verdict-log shape is a TOLERANT, DOCUMENTED ingest — not an invented standard
//
// There is NO standardized egress-proxy verdict-log format. The Anthropic authority
// describes the MECHANISM (allow/deny by FQDN) but specifies no wire schema, and
// real deployments emit ad-hoc JSON (Squid, Envoy RBAC, a bespoke MITM proxy, a
// gVisor/Firecracker sandbox sidecar) with divergent field names. So this connector
// defines a TOLERANT JSON-lines record (one decision per line) over the fields
// commonly present, accepting a few field-name ALIASES rather than pinning one
// vendor's spelling:
//
//	timestamp:   ts | time | timestamp | @timestamp | eventTime
//	identity:    identity | source | principal | workload | agent | client
//	destination: fqdn | host | destination | dest | sni | authority
//	port:        port | dest_port | destination_port
//	decision:    decision | verdict | action | result
//	reason:      reason | message | detail | policy
//
// decision values are matched case-insensitively: allow/allowed/permit/permitted/
// pass → an ALLOWED egress; deny/denied/block/blocked/drop/reject → a DENIED egress.
// Any other token is left UNKNOWN and the record is skipped (never guessed — ARCHITECTURE.md
// §6). This is a DOCUMENTED EXPECTED SHAPE (a tolerant ingest), explicitly NOT an
// invented vendor standard. A line that is blank or unparseable is
// tolerated and skipped without failing the run.
//
// # What it emits (via connectors/internal/meshobs, the shared L7 builder)
//
// Each verdict line maps onto the sealed SDK sum type (sdk/model) exactly as every
// other network connector does, so an egress edge and a kernel eBPF edge
// classify and de-dup the same way:
//
//   - ALLOW → meshobs.Record{Verdict: VerdictAllowed} → ONE model.EdgeObservation:
//     OriginKind "identity", OriginRef <identity>, ResourceKind "net.endpoint"
//     (ResourceRef "tcp://host[:port]") or "http.api" keyed by FQDN, Mode from the
//     method (absent ⇒ ModeReadWrite), Source SignalEgressProxy, ToolRef
//     "egress_proxy.verdict", Confidence Approximate (a log line is not a
//     cryptographic peer identity, so OriginVerified=false), ObservedAt from the
//     record timestamp (fallback: the connector clock). It is the OBSERVED side of
//     the permitted-path diff (module III).
//
//   - DENY → meshobs.Record{Verdict: VerdictDenied} → ONE model.FindingReport
//     (Kind "egress_denied", SubjectKind "net.egress", SubjectRef <FQDN>) — a
//     permitted-path violation / anti-evasion signal (module IX): an agent that
//     tried to reach a destination its egress allowlist forbids. DenyReason carries
//     the (scrubbed) record reason; the detail is hashed, the raw value never leaves.
//     A denied access did NOT happen, so it is NEVER emitted as an edge.
//
// The Mode is taken from what the log states (the HTTP method when present, else a
// bidirectional socket); it is never fabricated. SignalEgressProxy ("egress_proxy")
// is a PACKAGE-LOCAL open-string model.SignalSource so the operator never silently
// collapses a proxy verdict with an Envoy ALS edge or a kernel eBPF edge.
//
// # Operation
//
// Config key "path" names the verdict-log file OR a directory of *.log/*.json/*.jsonl
// /*.ndjson files (mirrors awskms). Gather is a BATCH POLLER: it lists the files,
// scans each line with bufio + encoding/json, emits, and returns nil at EOF so the
// engine re-runs it (set follow=true to tail a single growing log instead). It
// honors ctx cancellation in the loop and holds no handles between runs (Close is a
// no-op).
//
// It imports only the SDK, the Apache connectors/internal helpers (meshobs, redact,
// tracecontext) and the standard library — never the engine (/core), per LICENSING.md.
//
// Authority verified against: https://www.anthropic.com/engineering/how-we-contain-claude
// (egress proxy / FQDN allowlist / per-destination allow-or-deny verdict), 2026-06-06.
package egressproxy
