// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package mcp is the MCP (Model Context Protocol) introspection connector
// (ARCHITECTURE.md, §6; README.md modules I, V). It is a batch SourceConnector: each
// Gather connects to every configured MCP server (stdio or Streamable HTTP),
// runs the read-only introspection methods (initialize, tools/list,
// resources/list, resources/templates/list, prompts/list) and emits one
// capability edge per discovered tool, resource and prompt, from which the
// inventory and catalog modules materialize the MCPServer / Tool / Resource /
// Skill entities.
//
// The crux is trust: the MCP spec states a client MUST consider tool annotations
// untrusted unless the server is trusted, and the annotation defaults are
// asymmetric (readOnlyHint defaults false; destructiveHint defaults true). So the
// R/RW mode this connector derives from readOnlyHint/destructiveHint is a DECLARED
// capability hint, never an observed access: every edge carries
// SignalMCPAnnotation and ConfidenceApproximate, and is marked neither observed
// nor permitted by consumers (see the contract). The trusted R/RW signal is
// the Claude OTEL observed-access edge (the claude connector) corroborated by the
// eBPF backstop; this connector supplies the capability surface to diff
// against (the permitted-vs-observed killer feature, ARCHITECTURE.md).
//
// It imports only the SDK (and stdlib): the small JSON-RPC client is hand-rolled
// rather than pulling a full MCP SDK, keeping the connector dependency-light and
// the Apache-2.0/AGPL boundary clean (LICENSING.md). It is read-only — it lists,
// it never calls a tool or reads a resource's contents — and minimal-data: a
// resource URI is scrubbed for embedded secrets before it becomes an edge.
//
// The inline Resource Server in this package keeps its discovery metadata as an
// honesty currency: it advertises DPoP proof algorithms because it verifies them,
// advertises required DPoP-bound tokens only when the operator requires them, and
// advertises mTLS-bound token support only when this process is configured to see
// TLS client certificates directly.
package mcp
