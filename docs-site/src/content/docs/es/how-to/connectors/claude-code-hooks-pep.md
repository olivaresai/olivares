---
title: "Hooks y enforcement de Claude Code (el PEP)"
description: >-
  La mitad de gobernanza del conector de Claude Code: hooks observados por
  defecto y un policy enforcement point opcional que responde a los hooks
  PreToolUse / PermissionRequest con deny o ask — cada control registrado como
  un finding.
sidebar:
  order: 5
---

[Conectar Claude Code](/es/how-to/connect-claude-code/) cablea la mitad de
*observación* — telemetría OTLP de entrada, aristas de acceso de salida. Esta
página es la **mitad de gobernanza**: los **hooks** de Claude Code reportan al
conector las decisiones de herramienta, y un **policy enforcement point (PEP)**
opcional convierte ese canal en un control — el conector responde a un hook
`PreToolUse` / `PermissionRequest` coincidente con una `permissionDecision` de
`deny` o `ask`, y registra cada control como un finding.

Por defecto, el comportamiento es deliberadamente **read-first**: sin ninguna
política de enforcement configurada, los hooks se *observan, nunca se
controlan*. El enforcement es un opt-in explícito y con nombre, y una política
inválida **falla en el arranque** — el conector no se ejecutará en silencio
sin gobernar.

## Cómo funciona el canal de hooks

El receptor OTLP/HTTP del conector (loopback `127.0.0.1:4318` por defecto)
también sirve el endpoint de hooks en `hook_path` (por defecto **`/hooks`**).
En la máquina del desarrollador, la configuración de hooks de Claude Code
publica sus eventos de hook en ese endpoint de loopback — la sintaxis exacta de
los ajustes de hooks pertenece a la documentación propia de Claude Code; lo que
posee este producto es el receptor y la política descrita debajo.

Los eventos de hook y la telemetría OTLP sobre la misma llamada de herramienta
se **correlacionan** (la `correlation_window`, por defecto 5s, mantiene un lado
esperando al otro), de modo que una acción controlada y su telemetría aterrizan
como una sola historia coherente, no como dos registros desconectados. Una
sesión que sigue enviando hooks pero queda en silencio OTLP más allá del
`silence_threshold` (por defecto 2m) se marca como un hueco de telemetría — la
señal anti-evasión.

## Activar el enforcement

Añade una política `enforcement` a la configuración de la fuente
(`OLIVARES_SOURCES_CONFIG`):

```json
{
  "sources": [{
    "name": "claude",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "enforcement": "{\"rules\":[{\"tool\":\"Bash\",\"decision\":\"ask\",\"reason\":\"shell needs a human\"},{\"resource_kind\":\"file\",\"mode\":\"write\",\"decision\":\"deny\"}]}"
    }
  }]
}
```

Las reglas coinciden por el nombre de la herramienta y/o por la clase de
recurso y el modo de acceso; la decisión es `deny` o `ask` (escalar al humano
de la sesión). Los hooks `PreToolUse` / `PermissionRequest` coincidentes
reciben de vuelta esa decisión como la `permissionDecision` de Claude Code;
todo lo demás pasa observado. Cada control se registra como un **finding**, de
modo que el rastro de enforcement es consultable, no folclore.

:::note[El kill switch manda sobre todo]
Si el estate (o el agente concreto) está bajo una
[parada de emergencia](/es/how-to/cookbook/kill-switch-drill/), `claude.tool.use`
se mata en la capa de gobernanza con independencia de esta política — el stop
gate se comprueba antes de cualquier regla por herramienta, y falla cerrado.
:::

## Postura de la flota: managed settings, observados

El enforcement en el hook es una capa. La capa a nivel de flota es el fichero
de **managed settings** de Claude Code, que la fuente `managed-settings`
observa en modo solo lectura:

```json
{
  "sources": [{
    "name": "fleet-policy",
    "kind": "managed-settings",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/managed-settings.json",
      "expected_policy": "{…governance-authored intent…}"
    }
  }]
}
```

| Clave | Por defecto | Significado |
|---|---|---|
| `config_path` | `/etc/claude-code/managed-settings.json` (Linux) | el fichero de managed settings vivo del host (macOS: `/Library/Application Support/ClaudeCode/…`) |
| `scope` | hostname del SO | ámbito de atribución (id de host / nombre de distribución) |
| `expected_policy` | — | intención redactada opcional; cuando se define, el conector reporta **drift** (política permitida vs configuración observada). Vacío = solo observación |

Observadores opcionales relacionados en la fuente `claude`: `managed_mcp_path`
(modela el orden de evaluación de la allowlist de MCP gestionado y marca las
entradas allow basadas solo en el nombre) y `sandbox_path` (findings de postura
sobre los ajustes de bloqueo del sandbox) — ambos de solo lectura, ambos
desactivados hasta que se apuntan a un fichero.

## Qué verás en la consola

**Claude Code governance** es la superficie de redacción y truth-loop: la
política que pretendes, la configuración que los hosts realmente llevan y el
drift entre ambas. Los controles y los findings de hueco de telemetría aterrizan
en **Security**; la sesión en sí permanece visible en **Sessions**:

<img class="light:sl-hidden" src="/console/claude-policy-dark.png" alt="La vista de gobernanza de Claude Code — redacción de políticas y postura de la flota en un solo lugar." />
<img class="dark:sl-hidden" src="/console/claude-policy-light.png" alt="La vista de gobernanza de Claude Code — redacción de políticas y postura de la flota en un solo lugar." />

## Límites honestos

- **El PEP controla lo que los hooks reportan.** Un host cuyos hooks no están
  configurados no se controla — empareja la flota con el
  [observador de managed-settings](#postura-de-la-flota-managed-settings-observados)
  para que la ausencia sea visible, y con el
  [backstop del kernel](/es/how-to/connectors/ebpf-tetragon/) para que no sea
  ciega.
- **`ask` delega en un humano de la sesión** — es fricción, no un cerrojo.
  `deny` es el cerrojo.
- **Los subprocesos quedan fuera de alcance aquí** (los hooks se disparan para
  las propias llamadas de herramienta de Claude Code); consulta la
  [página de OTel empresarial](/es/how-to/claude-code-enterprise-otel/) para saber
  qué alcanza y qué no alcanza el entorno de telemetría.

## Relacionado

- [Conectar Claude Code](/es/how-to/connect-claude-code/) — la mitad de
  observación.
- [OTel empresarial para Claude Code](/es/how-to/claude-code-enterprise-otel/) —
  telemetría de flota, etiquetas, tracing.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — el modelo de autorización
  en el que se enchufa el PEP.
