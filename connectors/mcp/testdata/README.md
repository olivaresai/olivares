# MCP Wire Fixtures

Verification date: 2026-07-03.

These fixtures are compact JSON-RPC exchanges used by the connector tests. They
are derived from the MCP specification examples and tables, not SDK code.

| Fixture | Primary source |
| --- | --- |
| `2025-11-25_initialize.json` | https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle |
| `2025-11-25_tools_list.json` | https://modelcontextprotocol.io/specification/2025-11-25/server/tools |
| `2025-11-25_tools_call_gated.json` | https://modelcontextprotocol.io/specification/2025-11-25/server/tools |
| `2026-07-28_server_discover.json` | https://modelcontextprotocol.io/specification/2026-07-28/server/discover |
| `2026-07-28_tools_list_meta.json` | https://modelcontextprotocol.io/specification/2026-07-28/server/tools |
| `2026-07-28_tools_call_gated.json` | https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http |
| `2026-07-28_unsupported_protocol_version.json` | https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning |
| `2026-07-28_header_mismatch.json` | https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http |
| `2026-07-28_tools_call_task.json` | https://modelcontextprotocol.io/specification/2026-07-28/server/tools |
| `2026-07-28_tools_call_input_required.json` | https://modelcontextprotocol.io/specification/2026-07-28/server/tools |

The official SDK beta tags announced on 2026-06-29 corroborate the frozen RC, but
the wire bodies here are sourced from the spec pages above.
