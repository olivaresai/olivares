---
title: "Introspección de MCP y gobernanza del registry"
description: >-
  Inventaría cada servidor MCP que tus agentes pueden alcanzar, trata sus hints
  autodeclarados como no fiables por especificación, escanea el catálogo en
  busca de tool-poisoning y problemas de postura, y reconcilia contra los
  registries públicos y federados.
sidebar:
  order: 7
---

La fuente `mcp` gobierna la **superficie de capacidades** que ven tus agentes:
introspecciona servidores MCP (tools, recursos, prompts), deriva *hints* de
lectura/escritura de sus anotaciones y — opt-in — reconcilia lo que está en
ejecución contra el MCP Registry público, tus registries federados y el Docker
MCP Catalog, graduando la postura por el camino.

Una regla ancla todo lo que emite esta fuente:

:::caution[Las anotaciones de MCP no son fiables, por especificación]
Los `readOnlyHint` / `destructiveHint` de un servidor son autodeclaraciones, y
la especificación de MCP dice que los clientes DEBEN tratarlos como no fiables.
Cada arista que produce esta fuente es un **hint de capacidad declarada** —
`approximate`, ni observada ni permitida — que aporta la superficie contra la que
hacer el diff. Se corrobora con fuentes observadas, nunca se confía en él solo.
:::

## Declarar la fuente

```json
{
  "sources": [{
    "name": "mcp-estate",
    "kind": "mcp",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/.mcp.json",
      "posture_scan": "true",
      "registry_enabled": "true"
    }
  }]
}
```

Apúntala a los servidores de cualquiera de las dos formas:

| Clave | Significado |
|---|---|
| `servers` | array JSON inline de specs de servidores MCP a introspeccionar |
| `config_path` | ruta a un `.mcp.json` de Claude Code cuyos `mcpServers` se introspeccionan |
| `timeout` | timeout de introspección por servidor |

## Las capas de gobernanza (cada una opt-in, cada una honesta)

- **Escaneo de postura** (`posture_scan`, por defecto `true`) — escanea los
  metadatos del catálogo introspeccionado en busca de tool-poisoning,
  inyección, homoglifos y ámbitos demasiado amplios, graduando la postura contra
  el OWASP MCP Top-10. Solo *metadatos* del catálogo — no sondea ni explota
  servidores.
- **Registry público** (`registry_enabled`, por defecto `false`;
  `registry_url`) — enriquecimiento de procedencia de solo lectura desde el MCP
  Registry (preview upstream; el conector autoverifica lo que lee).
- **Sincronización del registry** (`registry_sync` + `owned_namespaces`) —
  enumera los namespaces de DNS inverso que tu organización posee en el registry
  público para detectar publicaciones retiradas o no gestionadas (el ángulo de
  cadena de suministro), y limpia tus servidores internos del marcado en sombra.
- **Reconciliación interna** (`internal_servers`) — un array JSON de servidores
  internos aprobados (`{name, registry_name, version}`); los servidores en
  ejecución se reconcilian contra él, con detección de drift de versión. Lo que
  se ejecuta pero no está en la lista es un finding de **shadow**.
- **Registries federados** (`federated_registries`) — registries de organización
  GitHub BYO, Azure API Center y subregistries privados que implementan la
  **`/v0.1` registry OpenAPI** fijada.
- **Feed de deprecaciones** (`deprecation_feed`) — obtiene el registry oficial
  de funcionalidades deprecadas de MCP en cada pasada para detectar drift de
  reglas; las reglas de deprecación compiladas nunca dependen de la obtención.
- **Docker MCP Catalog** (`docker_catalog`) — drift del pin de digest de imagen
  más procedencia Docker-built (firmada) vs community (no atestiguada) por
  servidor.
- **Preview de la próxima revisión** (`next_revision_preview`) — introspecciona
  servidores en el modo stateless del RC 2026-07-28 de MCP mientras siguen
  anunciando 2025-11-25; explícitamente un knob de preview.

Los findings aterrizan por capa: grados de postura, huecos de procedencia,
servidores shadow, uso de funcionalidades deprecadas, drift del registry.

## Qué verás en la consola

**MCP & skills** es el catálogo de capacidades vivo — servidores, sus tools y
hints declarados, skills, y cómo cada uno se cablea a los agentes:

<img class="light:sl-hidden" src="/console/capabilities-dark.png" alt="La vista de MCP & skills: el catálogo de capacidades vivo con servidores, tools, cableado y configuraciones gestionadas." />
<img class="dark:sl-hidden" src="/console/capabilities-light.png" alt="La vista de MCP & skills: el catálogo de capacidades vivo con servidores, tools, cableado y configuraciones gestionadas." />

Los hints aportan la superficie *declarada* al **Access map**; el panel de drift
es donde una tool declarada de solo lectura observada escribiendo deja de ser un
problema de hint y se convierte en un finding.

## Límites honestos

- **La introspección es una instantánea de lo que los servidores afirman.** Un
  servidor puede mentir; esa es la propia posición de la especificación y la
  razón de que cada arista esté marcada como lo está. La corroboración viene de
  fuentes observadas.
- **Una instantánea parcial del registry es un error, no un resultado** — el
  conector se niega a graduar contra una lectura de registry que no pudo
  completar.
- **El escaneo de postura lee metadatos.** No ejecuta tools, no hace fuzzing a
  servidores ni detecta una implementación con backdoor detrás de un catálogo
  limpio.

## Relacionado

- [Conectar Claude Code](/es/how-to/connect-claude-code/) — donde los hints de MCP
  se encuentran con la telemetría de sesión.
- [Módulo V — MCP, skills y capacidades](/es/reference/modules/v-capabilities/).
- [Construir y publicar un conector](/es/how-to/build-a-connector/) — la historia
  de admisión firmada y deny-closed para los propios binarios de conector.
