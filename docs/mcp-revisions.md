<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# MCP revisions supported

Olivares speaks two Model Context Protocol revisions. Both are declared once, in
`connectors/mcp/revision.go`, and every MCP path reads them from there.

| Constant | Revision | Era |
| --- | --- | --- |
| `LatestRevision` | 2026-07-28 | Modern: no `initialize`, per-request `_meta`, `server/discover` |
| `LegacyRevision` | 2025-11-25 | Handshake era: `initialize`, `Mcp-Session-Id` |

## Where each revision is used

| Path | Behavior |
| --- | --- |
| Managed MCP server over HTTP: session tools and the gateway (`/mcp/gateway/<tenant>/<id>`) | Sends `server/discover` at 2026-07-28 first. A server that answers it is used at 2026-07-28; any other server gets `initialize` at 2025-11-25. Gateway clients keep the 2025-11-25 handshake; the engine translates. |
| Managed local MCP server (stdio) | Sends `initialize` at 2025-11-25 first; a server that refuses it gets `server/discover` at 2026-07-28. An older stdio server can end on any request sent before `initialize`, so the handshake goes first there. |
| Connection test of a managed server | The order of its transport. |
| Governed retrieval surface | Answers `initialize` with the handshake-era revision the client asks for (2025-11-25 when it names none it knows) and `server/discover` with both revisions. With the gateway's `revision_mode` set to `dual`, as the governed-RAG quickstart writes it, both eras work: a request without `MCP-Protocol-Version`, such as an `initialize`, is served as handshake era. The default `revision_mode` stays `legacy`. |
| Session MCP endpoint (tools offered to the AI tool in a session) | 2025-11-25. |

## Negotiation rule

Over HTTP, any answer to the `server/discover` probe other than a discover result that lists
2026-07-28 falls back to `initialize` at 2025-11-25. The rule is keyed to no error code, as the
published "Backward Compatibility" sections require, and it is the rule of the official Go SDK
client (`github.com/modelcontextprotocol/go-sdk@v1.8.0`). Over stdio, a JSON-RPC refusal of `initialize` leads to `server/discover`, and a
discover result that lists 2026-07-28 opens the server at that revision.

Limits:

- A modern HTTP server that answers with a modern error (for example `-32022` without 2026-07-28
  in `supported`) also gets an `initialize`. A modern-only server refuses it, and that refusal is
  what the user sees.
- A stdio server that speaks both eras is used at 2025-11-25.
- A credential found in an answer ends the connection; it never falls back.

## Wire check against the published specification

Checked on 2026-11-04 against `modelcontextprotocol/modelcontextprotocol`:

- Schema `schema/2026-07-28/` at commit `271ecc9a` (2026-07-28T16:42:34Z). `main` on 2026-11-04 is
  byte-identical: `schema.json` sha256 `ef70b61f…0203`, `schema.ts` sha256 `742750af…c2cd`.
  `schema.json` is also the copy in `cmd/olivares/testdata/mcp-final-2026-07-28/`.
  `LATEST_PROTOCOL_VERSION` is `2026-07-28`.
- Pages from `main`: `basic/versioning`, `basic/index`, `basic/transports/streamable-http`,
  `basic/transports/stdio`, `server/discover`, `basic/authorization/index` and
  `extensions/tasks/overview`. The six specification pages differ from the schema commit only in
  links.

Each string the connector emits for 2026-07-28 was found in that text:

| Kind | Value | Found in |
| --- | --- | --- |
| `_meta` key | `io.modelcontextprotocol/protocolVersion`, `…/clientInfo`, `…/clientCapabilities` | schema.ts, server/discover, streamable-http |
| `_meta` key | `io.modelcontextprotocol/serverInfo` | schema.ts, server/discover |
| `_meta` key | `io.modelcontextprotocol/subscriptionId` | schema.ts, basic/index |
| Method | `server/discover`, `subscriptions/listen` | schema.ts, stdio, streamable-http |
| Notification | `notifications/subscriptions/acknowledged` | schema.ts, streamable-http |
| Method (Tasks extension) | `tasks/get`, `tasks/update`, `tasks/cancel` | extensions/tasks/overview |
| HTTP header | `MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name` | streamable-http |
| Result field | `resultType` (`complete`, `input_required`), `supportedVersions` | schema.ts |
| Cache field | `ttlMs`, `cacheScope` (`public`, `private`) | schema.ts |
| Extension id | `io.modelcontextprotocol/tasks`, `io.modelcontextprotocol/ui` | schema.ts, basic/versioning |
| Error code | `-32020` header mismatch, `-32021` missing client capability, `-32022` unsupported version (`data.supported`, `data.requested`), `-32602` | schema.ts constants, basic/index, basic/versioning |
| Listen filter | `toolsListChanged`, `promptsListChanged`, `resourcesListChanged`, `resourceSubscriptions` | schema.ts |
| Authorization | protected resource metadata at `/.well-known/oauth-protected-resource` (RFC 9728, servers MUST); `resource` in token requests (RFC 8707, clients MUST) | basic/authorization/index |

Required result members, read from `schema.json`: `DiscoverResult` needs `resultType`,
`supportedVersions`, `capabilities`, `ttlMs` and `cacheScope`; `ListToolsResult` needs
`resultType`, `tools`, `ttlMs` and `cacheScope`; `CallToolResult` needs `resultType` and
`content`. The governed retrieval surface sends each of them.
