---
title: "Admin plane de Anthropic (uso, coste, cumplimiento)"
description: >-
  Gobierna la propia organización de Claude: coste y uso facturados autoritativos
  vía la Admin API, allow-sets de MCP y server-tool del lado de la API como
  aristas permitidas, el feed de actividad de cumplimiento y el directorio de la
  organización — cada credencial acotada, cada punto ciego nombrado.
sidebar:
  order: 6
---

La telemetría de Claude Code te dice qué se ejecuta en las máquinas de los
desarrolladores. El **admin plane de Anthropic** te dice qué hace la
*organización*: coste facturado, uso por workspace, miembros y claves de la
organización, el feed de actividad de cumplimiento. Cuatro fuentes de solo
lectura lo cubren; esta página cablea las dos centrales y resume sus compañeras
del lado del roster.

| Fuente (`kind`) | Qué lee | Credencial |
|---|---|---|
| `claude-api` | uso y coste facturado, inventario de modelos/workspaces, analítica de Claude Code, gobernanza de MCP/server-tool del lado de la API | clave de Admin API (`admin_key`) |
| `claude-compliance` | el feed de actividad de cumplimiento (eventos con valor de evidencia) + el directorio de la organización | clave del feed de actividad + una Compliance Access Key **distinta** |
| `claude-console` | roster de IAM de la organización (miembros, roles) → findings de postura SSO/SCIM | credenciales de la consola |
| `claude-wif` | identidades no humanas (cuentas de servicio `svac_…`, identidades federadas) + sus aristas de scope **permitido** | credenciales del endpoint WIF |

Todas son **de solo lectura y deny-closed**: una credencial vacía significa que
ese feed está apagado y el producto lo dice — nunca un inventario vacío
fabricado.

## `claude-api`: coste, uso y gobernanza del lado de la API

```json
{
  "sources": [{
    "name": "anthropic-org",
    "kind": "claude-api",
    "tenant": "<tenant-id>",
    "config": {
      "admin_key": "<admin-api-key-reference>",
      "cost_report": "true",
      "claude_code": "true"
    }
  }]
}
```

Las claves que importan (del descriptor publicado; valores por defecto entre
paréntesis):

- **`admin_key`** (secreto) — la clave de Admin API de Anthropic. Vacía = solo
  catálogo offline.
- **`cost_report`** (`true`) — extrae el informe de coste **facturado** (diario,
  autoritativo) junto a la estimación de uso derivada. El producto mantiene
  ambos separados: las estimaciones se reconcilian contra las cifras
  facturadas, una sola fuente de coste por sesión, nunca ambas.
- **`lookback`** (`24h`) / **`cost_lookback`** (`48h`) /
  **`bucket_width`** (`1d`; también `1h`, `1m`) / **`max_pages`** — ventanas de
  extracción y límites de paginación.
- **`claude_code`** (`false`) — extrae también el feed de Claude Code Analytics
  (coste estimado por desarrollador y por modelo) para chargeback.
- **`claude_code_shadow_auth`** (`true`) — con el feed de analítica activo,
  marca a cada desarrollador cuyo uso de Claude Code se factura como
  `customer_type=api` — una clave personal/de API **fuera de la suscripción de
  la organización**, es decir, identidad y gasto circulando sobre una clave no
  gobernada. Pon `false` solo si tu organización ejecuta Claude Code sobre
  facturación de API intencionadamente.
- **`gateway`** (`direct`) — la superficie de despliegue sobre la que corre esta
  organización (`direct | claude-platform-aws | bedrock-mantle | bedrock-legacy
  | vertex | foundry`). En una superficie sin la Admin API (Bedrock/Vertex/
  Foundry) la ingesta de gobernanza **degrada honestamente con un finding de
  postura** en lugar de fingir un inventario vacío.
- **`mcp_toolsets`** / **`server_tool_grants`** — allow-sets declarados por el
  operador para agentes de Claude dirigidos por API (qué herramientas MCP, qué
  tipos de server-tool de Anthropic *puede* usar un agente). Cada entrada
  permitida se convierte en una **arista permitida** en el módulo III,
  contrastada contra el acceso observado — el mismo diff permitido-vs-observado
  que en todas partes. El `agent_ref` debe ser el id externo del agente tal como
  se descubre en runtime, o el grant es un no-op honesto en lugar de una
  coincidencia falsa.

:::caution[El feed de analítica tiene una frontera nombrada]
El feed de Claude Code Analytics solo rastrea el uso en la **Claude API**. Las
flotas en Claude Platform on AWS, Bedrock, Gemini Enterprise Agent Platform (anteriormente Vertex AI) o Microsoft Foundry **no
están en él** — la ausencia de findings ahí no es evidencia de ausencia. Para
esas superficies el [plano OTel](/es/how-to/claude-code-enterprise-otel/) es la
observación que tienes.
:::

## `claude-compliance`: el feed de evidencia y el directorio

```json
{
  "sources": [{
    "name": "anthropic-compliance",
    "kind": "claude-compliance",
    "tenant": "<tenant-id>",
    "config": {
      "api_key": "<activity-feed-key-reference>",
      "compliance_access_key": "<compliance-access-key-reference>"
    }
  }]
}
```

Dos credenciales **distintas**, deliberadamente:

- **`api_key`** — una clave de Admin API con `read:compliance_activities`;
  extrae el feed de actividad (eventos con valor de evidencia).
- **`compliance_access_key`** — una clave separada con
  `read:compliance_org_data` / `read:compliance_user_data`; habilita la ingesta
  del **directorio** de la organización (orgs, usuarios, roles, grupos —
  incluyendo la señal de aprovisionamiento SCIM que la Admin API no puede ver).
  Vacía = directorio apagado, deny-closed.

El scope de borrado (`delete:compliance_user_data`, usado por la ruta de derecho
al olvido) se aprovisiona por separado y está sujeto a dual-control — este
connector de lectura nunca lo posee.

## Qué verás en la consola

Gasto facturado y estimado, segmentado por las dimensiones que transporta la
telemetría (las etiquetas de team y project pasan a ser de primer nivel), en
**Cost & FinOps**; miembros de la organización, identidades no humanas y sus
scopes en **Identity & NHI**; findings de postura (shadow auth, degradación de
superficie, footguns de WIF) en **Security**:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="La vista Cost & FinOps: gasto por modelo y dimensión, con presupuestos y alertas." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="La vista Cost & FinOps: gasto por modelo y dimensión, con presupuestos y alertas." />

## Límites honestos

- **La autoridad de coste es el informe facturado.** Las cifras derivadas del
  uso son estimaciones y se reconcilian, nunca se contabilizan por duplicado.
- **El admin plane ve las superficies operadas por Anthropic.** Claude alojado
  por terceros (Bedrock/Vertex/Foundry) es invisible para él — nombrado
  explícitamente vía `gateway`, cubierto por el plano OTel.
- **Los findings de postura de `claude-console` incluyen un punto ciego:** la
  consola no puede observar si SSO/SCIM se aplica aguas arriba — el finding lo
  dice en lugar de adivinarlo.

## Relacionado

- [OTel empresarial para Claude Code](/es/how-to/claude-code-enterprise-otel/) — el
  plano por sesión que estos feeds a nivel de organización complementan.
- [Presupuestos y guardrails de FinOps](/es/how-to/cookbook/budgets-and-finops-guardrails/)
  — convierte el flujo de coste en límites aplicados.
- [Connectors y niveles de cobertura](/es/reference/connectors/) — el catálogo
  completo.
