---
title: "Configurar OpenTelemetry empresarial para Claude Code"
description: >-
  La postura de telemetría empresarial recomendada para una flota de Claude Code:
  el env de managed-settings que activa la exportación OTel sancionada, las
  etiquetas de operador vía OTEL_RESOURCE_ATTRIBUTES que se convierten en dimensiones
  FinOps, la beta de tracing para la jerarquía de subagentes y los controles de
  privacidad — con sus deberes — detallados.
---

La exportación de OpenTelemetry de Claude Code es la **ruta de observación sancionada**
para una flota gobernada: no está limitada por plan, transporta telemetría atribuida a
la sesión y la capa de managed settings puede activarla para cada desarrollador — sin
hacer de proxy de nada. Esta página es la configuración *empresarial* sobre
[Conectar Claude Code](/es/how-to/connect-claude-code/): qué establecer a nivel de flota,
qué te aporta cada control y qué deber genera. Los nombres de clave y la semántica de
más abajo se verificaron contra la propia documentación de Claude Code el 2026-06-10
(cliente 2.1.17x); vuelve a comprobarlos allí antes de codificar nuevos — evolucionan
rápido.

:::note[El env gestionado gobierna solo Claude Code]
El bloque `env` gestionado configura el **proceso de Claude Code**. Las variables
OTEL_* **no** se propagan a los subprocesos (comandos Bash, hooks, servidores MCP);
solo `TRACEPARENT` lo heredan los subprocesos de shell mientras el tracing está activo.
Planifica la observabilidad de los subprocesos por separado (el respaldo de
kernel/eBPF).
:::

## Qué obtienes

| Control | Qué te aporta | Deber que genera |
|---|---|---|
| Telemetría gestionada `env` | Cada sesión exporta OTLP a tu collector — observación que sobrevive a la configuración propia del desarrollador | Ninguno — telemetría estructural por defecto |
| `OTEL_RESOURCE_ATTRIBUTES` | Etiquetas definidas por la organización (equipo, proyecto, centro de coste) en **cada datapoint de métrica y cada registro de evento**; el control plane las enruta hacia las dimensiones de gasto FinOps | Mantén los valores de etiqueta no sensibles; el connector las pasa por allowlist y las depura |
| Beta de tracing | Los spans `claude_code.llm_request` / `claude_code.tool` transportan `agent_id` / `parent_agent_id` — la **jerarquía de subagentes por instancia** en el grafo de accesos | Superficie beta: verifica al actualizar |
| `OTEL_LOG_TOOL_DETAILS=1` | `tool_parameters` en los eventos de herramienta — incluyendo **qué comando se rechazó** en una decisión de herramienta denegada | Las entradas de herramienta salen del host: un deber de residencia/expurgo que debes asumir |
| `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` | `app.entrypoint` (cli / sdk-ts / claude-vscode …) — qué superficie lanzó cada sesión | Ninguno (etiqueta de baja cardinalidad) |

## Paso 1 — activa la exportación desde la capa gestionada

Redacta el `env` de telemetría en tu política de managed settings (el helper
`TelemetryEnv` del connector `managed-settings` renderiza exactamente esta postura):
activa la telemetría, apunta el exportador OTLP al collector del control plane y exporta
tanto métricas como logs. Deriva la referencia completa de variables a la propia
documentación de monitorización de Claude Code — no copies valores a mano desde aquí.

:::caution[Nunca pongas credenciales del collector en línea]
Un fichero de managed-settings está en texto plano en cada host. La capa de redacción
rechaza `OTEL_EXPORTER_OTLP_HEADERS` con un valor precisamente por esta razón —
autentica el collector con mTLS o una referencia a un gestor de secretos, nunca un token
en línea.
:::

La captura de contenido (prompts, cuerpos de herramienta) permanece **desactivada** a
menos que te suscribas explícitamente — y el connector del control plane retiene de forma
independiente solo datos estructurales, sea lo que sea lo que emita el cliente.

## Paso 2 — etiqueta la flota para FinOps

Establece `OTEL_RESOURCE_ATTRIBUTES` en el mismo env gestionado, usando un formato W3C
Baggage estricto (codifica los valores con percent-encoding; sin espacios ni comillas):

```
OTEL_RESOURCE_ATTRIBUTES=team=payments,project=atlas,cost_center=cc-42
```

Desde el cliente 2.1.161 estos valores viajan en **cada datapoint de métrica y cada
registro de evento**, no solo en el bloque de recurso OTLP — y las claves personalizadas
nunca sobrescriben los atributos estándar. En el control plane, lista las claves que
honras en la allowlist `resource_labels` del connector de claude; el connector depura los
valores y los adjunta como etiquetas en las aristas de identidad de la sesión y en cada
muestra de coste. FinOps promueve `team` y `project` a dimensiones de gasto de primer
nivel, así que "segmentar el gasto de Claude Code por equipo" funciona de extremo a
extremo. Las claves que no estén en la allowlist se descartan — datos mínimos por defecto.

## Paso 3 — jerarquía de subagentes (beta de tracing)

Activa la beta de telemetría mejorada más un exportador de trazas en el env gestionado
para obtener spans. Los atributos de identidad de subagente (`agent_id`,
`parent_agent_id`) son **solo de span** — no aparecen en ninguna métrica ni en ningún
evento de log — y viven en los spans `claude_code.llm_request` (desde 2.1.139) y
`claude_code.tool` (desde 2.1.145). El connector los mapea al grafo de accesos como:

- `session → identity.subagent` — la **instancia** de subagente que actuó, y
- `parent agent → identity.subagent` — **quién la generó** (ausente para los agentes que
  la sesión principal generó directamente).

Esto es lo que hace distinguibles a dos subagentes concurrentes del mismo tipo — el
`subagent_type` de la herramienta `Agent` por sí solo es una etiqueta de tipo, no una de
instancia.

## Paso 4 — controles opcionales de fidelidad

- `OTEL_LOG_TOOL_DETAILS=1` añade `tool_parameters` a los eventos de herramienta — también
  en las decisiones de herramienta denegadas (desde 2.1.157), de modo que un finding de
  rechazo puede nombrar el comando saneado que se bloqueó. El connector reduce las entradas
  a referencias de recurso expurgadas en el momento de la ingesta y nunca las almacena en
  bruto; pero los valores SÍ salen del host del desarrollador, así que activar esto es una
  decisión de residencia deliberada.
- `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` añade `app.entrypoint` a todas las métricas y
  eventos (desactivado por defecto). El connector lo registra como topología de sesión —
  una flota embebida en SDK tiene una postura de riesgo distinta al uso interactivo por
  CLI.

## Límites honestos de esta ruta

- **Ingesta por loopback sin autenticar.** El receptor cooperativo se enlaza a loopback
  por defecto y debe permanecer ahí; cualquier cosa que lo alcance puede falsificar
  telemetría (ver [Conectar Claude Code](/es/how-to/connect-claude-code/)).
- **Los subprocesos no están cubiertos.** OTEL_* no llega a los subprocesos de
  Bash/hook/MCP; solo `TRACEPARENT` se hereda bajo tracing.
- **El feed del admin plane no puede ver proveedores de terceros.** La Claude Code
  Analytics API solo rastrea el uso en la Claude API — Claude Platform on AWS, Microsoft
  Foundry, Amazon Bedrock y Gemini Enterprise Agent Platform (anteriormente Vertex AI) no se incluyen. Para una flota en esas superficies,
  **esta ruta OTel es la única observación que tienes**, y el detector de shadow-auth del
  feed de admin no puede dejarlas limpias.
- **Las cifras de coste aquí son estimaciones.** La telemetría de coste por petición se
  reconcilia contra los informes de coste autoritativos; una sola fuente de coste por
  sesión, nunca ambas.

## Próximos pasos

- [Conectar Claude Code](/es/how-to/connect-claude-code/) — el cableado base sobre el que se
  construye esta página.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — la mitad de aplicación (managed
  settings, hooks, el PEP).
- [Reenviar la auditoría a Splunk](/es/how-to/forward-audit-to-splunk/) — envía a tu SIEM los
  findings que produce esta telemetría.
