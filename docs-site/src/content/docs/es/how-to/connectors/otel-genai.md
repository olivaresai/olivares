---
title: "OpenTelemetry GenAI (cualquier agente instrumentado)"
description: >-
  Alimenta el mapa de accesos y FinOps desde CUALQUIER agente instrumentado con
  OTel — LangChain, LangGraph, CrewAI, AutoGen, Google ADK y similares — mediante
  el perfil de ingesta neutral gen_ai.*: opt-in, fijado a semconv v1.41.1,
  normalizando los tres dialectos de GenAI que coexisten en flotas reales.
sidebar:
  order: 4
---

Claude Code es la fuente cooperativa canónica, pero no es el único agente
cooperativo que ejecutas. El mismo conector que recibe la telemetría de Claude
Code (`kind: claude`) lleva un **perfil OpenTelemetry GenAI opt-in y neutral
respecto al proveedor**: apunta cualquier agente o framework instrumentado con
OTel al mismo receptor OTLP, y su telemetría `gen_ai.*` alimenta el mapa de
accesos (access map) y la canalización de costes — LangChain, LangGraph, CrewAI,
AutoGen, Google ADK y cualquier otra cosa que emita las convenciones semánticas
de GenAI en spans o eventos de log.

## Por qué es opt-in

Las convenciones GenAI de OpenTelemetry están en **estado Development**
(pre-estables) en upstream, y en 2026 coexisten realmente tres dialectos en las
flotas. Por eso el perfil está desactivado por defecto y se controla
exactamente igual que lo controlan los SDK de OTel — mediante el token de
opt-in:

```json
{
  "sources": [{
    "name": "agents-otel",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "semconv_opt_in": "gen_ai_latest_experimental"
    }
  }]
}
```

`semconv_opt_in` refleja `OTEL_SEMCONV_STABILITY_OPT_IN`: una lista separada por
comas que debe contener `gen_ai_latest_experimental`. Con el perfil
**desactivado**, un registro `gen_ai.*` sigue alimentando el watchdog de
actividad de sesión pero **no se mapea** — ausencia honesta, no ingesta
silenciosa.

## Qué acepta el normalizador

El perfil está fijado a **semconv v1.41.1** y normaliza los tres dialectos de
GenAI que coexisten en estates reales, sellando cada evento normalizado con el
pin de semconv del dialecto para que la procedencia sobreviva:

| Dialecto | Forma |
|---|---|
| OpenLLMetry heredado | atributos indexados `gen_ai.prompt.{i}.*` |
| v1.36 y anteriores | los eventos por mensaje, ya obsoletos |
| v1.37+ | la generación `messages` |

Además de las formas de mensaje mapea las **convenciones `mcp.*` (v1.39)** y el
**split client/internal de `invoke_agent` más `invoke_workflow` (v1.41)** — de
modo que las invocaciones de agente y workflow orquestadas por el framework
aterrizan como topología estructurada, no como ruido. Tanto la emisión basada en
spans (cómo instrumentan LangGraph, LangChain, CrewAI, AutoGen y Google ADK)
como la basada en logs se ingieren.

Las muestras de coste se deduplican por span id de W3C, de modo que un agente
cuya telemetría llega tanto por la ruta de spans como por la de logs nunca se
factura dos veces.

## Cómo conectar un agente

El receptor es el propio endpoint OTLP del conector (gRPC `127.0.0.1:4317`,
HTTP `127.0.0.1:4318` por defecto). En el lado del agente se aplica la
configuración estándar del SDK de OTel — endpoint del exportador hacia el
receptor en loopback, y el opt-in de GenAI si tu instrumentación lo exige:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
```

:::caution[La misma regla de loopback que Claude Code]
La ingesta cooperativa es **no autenticada** y se enlaza a loopback por defecto.
Cualquier cosa que pueda alcanzar el socket puede falsificar telemetría —
mantenlo en loopback (`allow_public_bind` existe y está deliberadamente marcado
como PELIGROSO). Los agentes fuera del host son trabajo del backstop del kernel,
no de un puerto OTLP público.
:::

## Qué verás en la consola

Las sesiones instrumentadas aparecen en **Sessions** como actividad en vivo,
atribuidas al agente emisor; sus llamadas al modelo alimentan **Cost & FinOps**;
los spans de MCP y de herramientas aportan aristas al **Mapa de accesos** como
cualquier fuente cooperativa:

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="La vista Sessions mostrando actividad de sesión de agente en vivo desde telemetría cooperativa." />
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="La vista Sessions mostrando actividad de sesión de agente en vivo desde telemetría cooperativa." />

## Límites honestos

- **Convenciones pre-estables, ingesta fijada.** El perfil está fijado a
  v1.41.1; cuando upstream avance, el pin avanzará mediante una actualización
  deliberada, no por deriva silenciosa. La instrumentación que emita un cuarto
  dialecto no se adivina.
- **Cooperativo significa cooperativo.** Un agente que no emite es invisible a
  esta ruta — para eso están [eBPF/Tetragon](/es/how-to/connectors/ebpf-tetragon/)
  y la auditoría nativa del store.
- **Las peculiaridades de span-kind de los frameworks son reales.** Algunos
  frameworks emiten spans cuyo kind no coincide con las reglas client/internal
  de v1.41; el normalizador mapea lo que puede demostrar y deja el resto sin
  mapear en lugar de atribuirlo mal.

## Relacionado

- [Conectar Claude Code](/es/how-to/connect-claude-code/) — el mismo receptor,
  superficie específica de Claude.
- [OTel empresarial para Claude Code](/es/how-to/claude-code-enterprise-otel/) —
  postura de telemetría a nivel de flota.
- [Referencia de eventos](/es/reference/events/) — las observaciones normalizadas
  que esto produce.
