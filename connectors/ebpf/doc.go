// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package ebpf is the kernel-level, non-cooperative backstop connector for the
// R/RW access map (ARCHITECTURE.md; README.md modules III and IX). It is the
// anti-evasion half of the moat: where the cooperative connector (Claude
// Code OTEL+hooks) sees what an agent reports, this connector sees what the
// kernel actually did — file reads/writes, process execution and network
// connections — even when an agent disables its own telemetry. It runs OUTSIDE
// the agent's control (docs/SECURITY-HARDENING.md).
//
// It imports only the SDK (github.com/olivaresai/olivares/sdk and .../sdk/model)
// and the Go standard library, never the engine, so it ships under Apache-2.0
// (LICENSING.md). It is read-first and minimal-data: it emits access relationships,
// never payloads. It is BLIND TO THE TLS BODY — it never decrypts, never inspects
// payloads, and is not a content sniffer (docs/SECURITY-HARDENING.md, §3).
//
// # Integration: Tetragon, post-kernel
//
// The connector does NOT load eBPF programs itself. The kernel capture is done by
// Tetragon (github.com/cilium/tetragon, Apache-2.0), deployed as a separate,
// hardened service (ARCHITECTURE.md, docs/SECURITY-HARDENING.md). This connector is a post-kernel
// CONSUMER of Tetragon's JSON event stream (the newline-delimited
// GetEventsResponse encoding produced by `tetra getevents -o json` or Tetragon's
// file/FIFO export). Consuming the export — rather than the gRPC API — keeps the
// connector dependency-light (standard-library JSON only), golden-testable
// without a kernel, and lets it run with NO kernel capabilities of its own.
//
// The events it understands are produced by the TracingPolicies bundled under
// deploy/ (file access and TCP connect). Because this connector ships those
// policies, it controls the argument layout it parses; the decoder is still
// tolerant of unknown/extra fields and missing optional fields so a Tetragon
// version bump does not break ingestion. Minimum Tetragon: v1.0.
//
// # Privilege posture (docs/SECURITY-HARDENING.md, §6)
//
// Capability split — this is a security claim, stated precisely:
//   - Tetragon (the kernel sensor, a SEPARATE hardened DaemonSet/service) holds
//     CAP_BPF + CAP_PERFMON (or CAP_SYS_ADMIN on older kernels). The Kubernetes
//     example permits root and uses seccomp RuntimeDefault; the Compose example
//     does not set a user and uses seccomp=unconfined. Both expose no inbound
//     listener and set CPU/memory limits. See deploy/tetragon-daemonset.yaml and
//     deploy/docker-compose.yaml.
//   - THIS connector process requires NO kernel capabilities. It only reads a
//     file/FIFO/socket that Tetragon owns. Its attack surface is read-only. The
//     example manifests do not enforce the export's file or directory mode, so
//     the deployment must set and verify ownership and permissions.
//
// Caveat (honest framing): although the connector needs no privilege, the DATA it
// consumes — unfiltered kernel events across the host — is sensitive. A
// compromised connector leaks process/network/file metadata. So the connector is
// isolated (own user/container, no listener), the export volume is private to the
// sensor and connector, and Tetragon hardening is a PREREQUISITE, not a
// replacement, for connector isolation.
//
// # What it emits
//
// The SDK Observation set is sealed (sdk/model: only edge/cost/finding). Process
// and network "events" are therefore expressed as EdgeObservations, not as a new
// observation kind; a bare process exec/exit is tracked INTERNALLY (for
// attribution, classification and liveness) and is never emitted on its own (host
// inventory is scope). All observations are emitted by VALUE.
//
// File access (Tetragon security_file_permission kprobe):
//
//	EdgeObservation{
//	  OriginKind: "identity",            // see "Origin & confidence" below
//	  OriginRef:  "<workload>:<exe>",    // the kernel-observed non-human identity
//	  ResourceKind: "file.path",
//	  ResourceRef:  "<absolute path>",
//	  Mode:  read | write | readwrite | unknown,   // from the MAY_* mask, see filemask.go
//	  Source: model.SignalEBPF,
//	  Confidence: model.ConfidenceApproximate,
//	  ToolRef: "security_file_permission",
//	  ObservedAt: <kernel event time>,
//	}
//
// Network connect (Tetragon tcp_connect kprobe; 5-tuple + optional SNI):
//
//	EdgeObservation{
//	  OriginKind: "identity",
//	  OriginRef:  "<workload>:<exe>",
//	  ResourceKind: "net.endpoint",
//	  ResourceRef:  "tcp://<sni-or-dst-ip>:<dport>",  // SNI when present, else dst IP
//	  Mode:  model.ModeReadWrite,        // a socket is bidirectional (see network.go)
//	  Source: model.SignalEBPF,
//	  Confidence: model.ConfidenceApproximate,
//	  ToolRef: "tcp_connect",
//	  ObservedAt: <kernel event time>,
//	}
//
// SNI note: Tetragon has NO first-class SNI field. This connector parses an SNI
// label only if the operator's TracingPolicy provides one (an optional, clearly
// labeled enrichment); otherwise the endpoint is the destination IP:port. SNI,
// when captured, is the cleartext ClientHello hostname — never the TLS body. It is
// a hostname (recon-relevant); it is treated as part of the sensitive access
// graph and gated by the same RBAC as every other edge.
//
// # Origin & confidence (read this before consuming an edge)
//
// OriginKind is "identity" — NOT "agent". The kernel attributes an access to a
// process / cgroup / container, i.e. a non-human runtime identity, never to a
// resolved agent. Classifying a process as an agent (see evasion.go) is a
// heuristic and is NEVER used to claim OriginKind "agent"; that would overstate
// certainty. OriginRef is a stable workload key (container id when present, else
// the host/node) joined with the executable base name, so repeated accesses by
// the same workload+binary de-duplicate to one edge per (origin, resource, mode).
//
// Confidence is ALWAYS ConfidenceApproximate, and the reason is specific:
//   - The ACCESS itself is kernel GROUND-TRUTH (the syscall happened).
//   - Confidence qualifies the ATTRIBUTION, not the access. The kernel gives a
//     process/cgroup, not an agent. The process↔agent correlation is resolved by
//     the access-map module, which UPGRADES confidence once it attributes
//     the identity to an agent.
//
// Do not read ConfidenceApproximate as "the access might not have happened." It
// means "the access certainly happened; which agent did it is not yet resolved."
//
// # Anti-evasion gap marker (docs/SECURITY-HARDENING.md) — heuristic, OFF by default
//
// When enabled (detect_evasion, default false), the connector emits a
// FindingReport{Kind: "anti_evasion", Severity: Low} for an agent-classified
// workload that performs kernel-observed resource access but is NOT seen opening a
// connection to any configured cooperative-telemetry endpoint (otlp_endpoints)
// within evasion_window of its first observed activity. The intuition (docs/SECURITY-HARDENING.md
// §6): a known agent acting at the kernel while its cooperative telemetry path is
// dark is itself a signal. This is the kernel-side complement of watchdog
// (which flags OTEL-silent-while-hooks-active from the cooperative side)
// joins the two for forensics.
//
// It is a HEURISTIC, not proof, and is honest about it: Severity is Low and it is
// off by default because it false-positives when the agent uses a remote/BYO OTLP
// collector not listed in otlp_endpoints, when a batching exporter connects after
// the window, during early startup, or in air-gapped deployments. It emits at most
// ONE finding per process instance; a single observed connection to a cooperative
// endpoint latches the instance as cooperative for the rest of its observed life
// (a healthy agent connects once and streams over that connection), so it is never
// flagged. Detecting an agent that tears down an ALREADY-established cooperative
// connection mid-session is out of scope here — no new connect event fires — and is
// covered by the cooperative watchdog. The final correlation and
// any enforcement live in never here.
//
// # Minimal data
//
// Only edges and findings cross the boundary. A process command line is captured
// IN-MEMORY for attribution/classification and is NEVER emitted raw; the connector
// reads no environment variables (no /proc/<pid>/environ). FindingReport.DetailHash
// is a SHA-256 of a stable process key, not of any secret. See redact.go.
package ebpf
