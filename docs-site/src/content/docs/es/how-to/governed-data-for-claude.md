---
title: "Datos gobernados para Claude"
description: "Expón tu contenido de Drive o S3 a Claude Code mediante una KB semántica y un endpoint MCP de retrieval, gobernado por identidad, clearance, ACL y scope de fuente."
sidebar:
  order: 7
---

Esta ruta permite que Claude Code consulte **tus** contenidos de Google Drive o
S3 sin convertir Olivares en un gateway de IA. El control plane trae el
contenido a una knowledge base gobernada, registra procedencia por documento y
expone por MCP solo las herramientas de retrieval:

| Default | Qué significa |
|---|---|
| KB semántica | `embed_policy=model_backed`; `/status` debe mostrar `retrieval_semantic=true` antes de ingestar. |
| Fallback visible | Si no hay embedder semántico, el create/ingest de la KB se niega en vez de fingir que los vectores local-hash son semánticos. |
| Guard ACL-aware | El agente solicitante debe resolver a una identidad vinculada con `attr_clearance` suficiente y grupos que coincidan con las ACL. |
| Source scope | Vincula la KB al agente de Claude Code; los subjects fuera de scope fallan cerrado. |
| Modo live honesto | Una respuesta de conector live lleva `source_mode=live`; los exports estáticos quedan como `source_mode=export` y nunca se presentan como live. |

## 1. Guarda la credencial de la fuente

Mantén la credencial de la fuente live en el runtime secret store. La config de
la fuente la referenciará como `store:<name>`, nunca inline.

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name s3/prod-runbooks-read \
  --value-file /run/secrets/s3-prod-runbooks-read
```

Para Google Drive, guarda el material OAuth que tu despliegue usa para acceso
read-only a Drive y usa otro nombre de secreto.

## 2. Genera la config de governed RAG

Para S3:

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

Para Google Drive, usa `--source gdrive --drive-id <shared-drive-id>` y una
referencia de credencial de Drive.

El comando escribe:

| Fichero | Propósito |
|---|---|
| `sources.json` | Registra la content source bajo `documents[]` con `mode=live`. |
| `agent-gateway.json` | Habilita el MCP resource server con `retrieval.enabled=true`. |
| `bootstrap-after-login.sh` | Crea la KB semántica, ingesta la fuente live, vincula el agente y añade el binding de source scope. |

Si el comando avisa que `retrieval_semantic=false`, configura primero
`OLIVARES_EMBEDDINGS_*`. Una KB model-backed se niega intencionadamente a
ingestar con solo el fallback local-hash.

## 3. Arranca con la config generada

```sh
OLIVARES_SOURCES_CONFIG=/var/lib/olivares/quickstart/governed-rag/sources.json \
OLIVARES_AGENT_GATEWAY_CONFIG=/var/lib/olivares/quickstart/governed-rag/agent-gateway.json \
olivares quickstart --data-dir /var/lib/olivares
```

Completa el setup inicial en consola si es una instalación nueva. Después ejecuta
el script de bootstrap con un token admin:

```sh
OLIVARES_TOKEN=<admin-token> \
OLIVARES_TENANT=ten_... \
/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh
```

## 4. Prerrequisito de identidad

El retrieval guard lee hechos de identidad desde el grafo de roster/SCIM. La
identidad vinculada debe existir antes de que Claude Code pueda recuperar
contenido restringido:

| Hecho de identidad | Ejemplo |
|---|---|
| Subject del token del agente / `agent_ref` | `claude-code-governed` |
| Identidad NHI vinculada | `agent:claude-code-governed` |
| Metadata de clearance | `attr_clearance=confidential` o superior |
| Membership de grupo | `group:engineering` que coincida con la ACL del documento |

Si el agente no tiene identidad, clearance o grupo coincidente, los chunks
restringidos no se devuelven. Si el agente no está vinculado a la KB por source
scope, la llamada MCP de retrieval falla cerrado.

## 5. Apunta Claude Code al MCP

Configura Claude Code con la URL de protected resource que imprime el
quickstart, normalmente:

```text
http://127.0.0.1:8446/mcp
```

El access token presentado a ese MCP resource server debe tener:

| Claim/control | Valor requerido |
|---|---|
| `iss` | El issuer configurado con `--mcp-issuer`. |
| `sub` | El external id del agente, por ejemplo `claude-code-governed`. |
| Scope | `knowledge:retrieval:read`. |
| Audience/resource | La URL MCP configurada en `agent-gateway.json`. |

## 6. Verifica

Ejecuta el E2E demo de referencia:

```sh
task demo:governed-rag
```

Comprueba status semántico, procedencia live, retrieval permitido con scope,
no-retrieval por clearance bajo, denegación fuera de scope y `source_mode=live`
en la respuesta MCP.

En despliegues existentes, verifica también un documento real:

```sh
curl -sk "$OLIVARES_BASE_URL/v1/m/knowledge/kbs/$KB_ID/documents" \
  -H "Authorization: Bearer $OLIVARES_TOKEN" \
  -H "X-Olivares-Tenant: $OLIVARES_TENANT"
```

Cada documento ingestado live debe mostrar `source_mode: "live"`. Si dice
`export`, la KB se ingestó desde un fichero export y debe describirse así a los
operadores.
