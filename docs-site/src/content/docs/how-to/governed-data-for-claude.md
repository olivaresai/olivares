---
title: "Governed data for Claude"
description: "Expose your Drive or S3 content to Claude Code through a semantic KB and an MCP retrieval endpoint, governed by identity, clearance, ACL and source scope."
sidebar:
  order: 7
---

This path lets Claude Code ask questions over **your** Google Drive or S3 content
without turning Olivares into an AI gateway. The control plane pulls the content
into a governed knowledge base, records provenance per document, and exposes only
the retrieval tools over MCP:

| Default | What it means |
|---|---|
| Semantic KB | `embed_policy=model_backed`; `/status` must show `retrieval_semantic=true` before ingest. |
| Visible fallback | If no semantic embedder is configured, the KB create/ingest refuses rather than pretending local-hash vectors are semantic. |
| ACL-aware guard | The requesting agent must resolve to a bound identity with enough `attr_clearance` and matching group ACLs. |
| Source scope | Bind the KB to the Claude Code agent; out-of-scope subjects fail closed. |
| Honest live mode | A live connector response carries `source_mode=live`; static exports stay `source_mode=export` and are never presented as live. |

## 1. Store the source credential

Keep the live source credential in the runtime secret store. The source config
will reference it as `store:<name>`, never inline.

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name s3/prod-runbooks-read \
  --value-file /run/secrets/s3-prod-runbooks-read
```

For Google Drive, store the OAuth bearer/refresh material your deployment uses
for read-only Drive access and use a different secret name.

## 2. Generate the governed RAG config

For S3:

```sh
olivares quickstart governed-rag \
  --data-dir /var/lib/olivares \
  --tenant-id ten_... \
  --source s3 \
  --source-name prod-runbooks-live \
  --bucket prod-runbooks \
  --prefix claude/ \
  --credential-ref store:s3/prod-runbooks-read \
  --mcp-issuer https://idp.example.com/ \
  --mcp-jwks-url https://idp.example.com/.well-known/jwks.json
```

For Google Drive, use `--source gdrive --drive-id <shared-drive-id>` and a Drive
credential reference.

The command writes:

| File | Purpose |
|---|---|
| `sources.json` | Registers the content source under `documents[]` with `mode=live`. |
| `agent-gateway.json` | Enables the MCP resource server with `retrieval.enabled=true`. |
| `bootstrap-after-login.sh` | Creates the semantic KB, ingests the live source, binds the agent and adds the source-scope binding. |

If the command warns that `retrieval_semantic=false`, configure
`OLIVARES_EMBEDDINGS_*` first. A model-backed KB intentionally refuses to ingest
with only the local-hash fallback.

## 3. Start with the generated config

```sh
OLIVARES_SOURCES_CONFIG=/var/lib/olivares/quickstart/governed-rag/sources.json \
OLIVARES_AGENT_GATEWAY_CONFIG=/var/lib/olivares/quickstart/governed-rag/agent-gateway.json \
olivares quickstart --data-dir /var/lib/olivares
```

Finish the first-run console setup if this is a fresh install. Then run the
bootstrap script with an admin token:

```sh
OLIVARES_TOKEN=<admin-token> \
OLIVARES_TENANT=ten_... \
/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh
```

## 4. Identity prerequisite

The retrieval guard reads identity facts from the roster/SCIM graph. The bound
identity must exist before Claude Code can retrieve restricted content:

| Identity fact | Example |
|---|---|
| Agent token subject / `agent_ref` | `claude-code-governed` |
| Bound NHI identity | `agent:claude-code-governed` |
| Clearance metadata | `attr_clearance=confidential` or higher |
| Group membership | `group:engineering` matching the document ACL |

If the agent has no identity, no clearance, or no matching group, restricted
chunks are not returned. If the agent is not bound to the KB by source scope, the
MCP retrieval call fails closed.

## 5. Point Claude Code at MCP

Configure Claude Code with the protected resource URL printed by the quickstart,
usually:

```text
http://127.0.0.1:8446/mcp
```

The access token presented to that MCP resource server must have:

| Claim/control | Required value |
|---|---|
| `iss` | The issuer configured by `--mcp-issuer`. |
| `sub` | The agent external id, for example `claude-code-governed`. |
| Scope | `knowledge:retrieval:read`. |
| Audience/resource | The MCP resource URL configured in `agent-gateway.json`. |

## 6. Verify

Run the reference demo E2E:

```sh
task demo:governed-rag
```

It checks semantic status, live-source provenance, an allowed scoped retrieval,
a low-clearance non-retrieval, an out-of-scope denial and `source_mode=live` in
the MCP result.

For existing deployments, also verify a real document:

```sh
curl -sk "$OLIVARES_BASE_URL/v1/m/knowledge/kbs/$KB_ID/documents" \
  -H "Authorization: Bearer $OLIVARES_TOKEN" \
  -H "X-Olivares-Tenant: $OLIVARES_TENANT"
```

Every live-ingested document should show `source_mode: "live"`. If it says
`export`, the KB was ingested from an export file and should be described that
way to operators.
