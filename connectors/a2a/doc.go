// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package a2a is a READ-ONLY observation connector for the Agent2Agent (A2A)
// protocol v1.0 (Linux Foundation; v1.0.0 released 2026-03-12, conformance pinned
// to v1.0.1, 2026-05-28 — Verified the full surface against the canonical
// a2a.proto + spec at both tags). It closes the vendor-neutral half of the
// agent↔agent interop graph: where the claude/mcp connectors only see Anthropic's
// Task delegation and MCP topology, this connector observes non-Claude A2A agents
// and feeds their edges into module IV's orchestration graph (AIP-05).
//
// It OBSERVES, it never ACTS (docs/SECURITY-HARDENING.md): it does not create, drive, or cancel
// Tasks, never sits in an agent↔agent data path, and reads only the public trust
// surface. Concretely, staged by leverage and lowest actuation risk:
//
//  1. Agent Card discovery + signature verification — the highest-leverage, lowest
//     risk surface. It fetches each configured agent's Agent Card (the recommended
//     well-known path /.well-known/agent-card.json, RFC 8615) and VERIFIES its JWS
//     signatures (RFC 7515) over the JCS-canonical card (RFC 8785), which is the
//     agent-to-agent trust signal a control plane must check and map. A card is
//     trusted only when its signature verifies against an operator-configured trust
//     anchor; an unsigned or unverifiable card is surfaced as UNTRUSTED, never
//     silently trusted (docs/SECURITY-HARDENING.md anti-evasion).
//  2. Task lifecycle observation — observed A2A task/message records (TASK_STATE_*,
//     the ProtoJSON SCREAMING_SNAKE_CASE enum of v1.0) become agent↔agent edges
//     into module IV, with confidence reflecting the participants' verified trust.
//  3. Transport bindings — A2A v1.0 defines three equivalent bindings over one
//     canonical Protocol-Buffers model, declared per AgentInterface in the card's
//     supportedInterfaces (JSONRPC / GRPC / HTTP+JSON). The observation Source
//     speaks plain HTTPS for card fetch; the delegation client (delegate.go) speaks
//     the JSON-RPC 2.0 binding; gRPC / HTTP+JSON/REST stay reserved behind the
//     transport seam (transport.go) — not claimed as built. The live
//     push-notification receiver is pushrecv.go, with the v1.0 config
//     surface client-side in pushconfig.go.
//
// GOVERNED EMISSION (emit_task.go). The one place this package leaves the
// read-only posture is the Client.SendMessage primitive: it discovers + verifies a
// remote Agent Card and, ONLY for a card whose identity is established against the
// operator trust anchor (trustVerified — stricter than observation, because emitting
// is an action), emits exactly ONE A2A v1.0 SendMessage (JSON-RPC 2.0), with
// credentials out-of-band in HTTP headers over TLS and the 9-state Task FSM modeled
// (input/auth-required surfaced as actionable interrupts). It is the minimal,
// governed emission of a SINGLE Task that the orchestration Dispatcher actuates. The
// FULL delegation Policy-Enforcement-Point — remote agent/skill/scope allowlist,
// ListTasks, push-notification receiver (SSRF + JWT/JWKS), SubscribeToTask/resubscribe
// (SSE), OTel spans — is NOT this primitive.
//
// Boundary (LICENSING.md): Apache-2.0; imports only /sdk (+ go-jose for JWS and the
// stdlib). It NEVER imports /core. Minimal data (docs/SECURITY-HARDENING.md): it carries agent
// references, security-scheme types and task states — never message payloads,
// prompts, or secrets; sensitive finding detail is hashed.
package a2a
