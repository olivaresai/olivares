---
title: "Cockpit de sesiones (disponibilidad)"
description: >-
  Descriptor de disponibilidad del espacio de nombres API session-cockpit.
  Community registra ese espacio de nombres sin manejadores y sin cockpit
  interactivo. Cómo confirmar la ausencia frente a sesiones en vivo y AgentOps,
  y qué capacidades abiertas usar.
---

El binario Community registra un descriptor de disponibilidad para el espacio
de nombres API `session-cockpit`. Ese espacio de nombres tiene actualmente
**cero manejadores** y **ningún cockpit interactivo**. Las peticiones bajo
`/v1/m/session-cockpit` reciben **404 por ausencia**. El descriptor no es uno
de los 30 módulos de producto del catálogo.

## Disponibilidad actual

| Superficie | Community (este artefacto) |
|---|---|
| Espacio de nombres API | `session-cockpit` (`/v1/m/session-cockpit`) |
| Descriptor | `olivares.session-cockpit` `0.1.0` — título `Session cockpit (availability)` |
| Rutas / manejadores registrados | ninguno |
| Cockpit interactivo | ninguno |
| Permiso declarado | `session-cockpit:availability:read` (declarado, no enrutado) |
| Ciclo de vida | vacío (`Init` / `Start` / `Stop` no hacen nada) |

Un 404 en este espacio de nombres es la respuesta esperada de Community. No
significa que el plano de control haya fallado al instalarse.

## Cómo diagnosticar la ausencia

Confirma que las superficies de sesión que sí se entregan siguen funcionando:

1. Las rutas de módulo de sesiones en vivo bajo el espacio de nombres
   `sessions` —
   [Operación en vivo y sesiones](/es/reference/modules/ii-sessions/).
2. La consola **Sesiones** (`/sessions`), **Claude Code** (`/agentops`) y
   **Work** (`/work`) — [referencia de consola](/es/reference/console/).
3. El ciclo de vida de las CLI oficiales en el apartado siguiente.

Si esas responden y `/v1/m/session-cockpit` es 404, el descriptor coincide con
este artefacto.

## Capacidades abiertas

La instalación, el lanzamiento, la observación y la gestión de las CLI
oficiales siguen en el producto Community abierto:

- [Operación en vivo y sesiones](/es/reference/modules/ii-sessions/) — sesiones
  de agentes en vivo, cronologías, perfiles de proveedor y `live_ref`.
- [Operar una sesión de proveedor](/es/how-to/operate-provider-sessions/) —
  lanzar Claude, Codex o Grok bajo un binario oficial fijado.
- [Ejecutar Claude Code con Olivares](/es/how-to/run-claude-code-with-olivares/) —
  co-despliegue AgentOps de sesiones oficiales `claude`.
- Guías de connector y PEP-hook (observar/gobernar, no lanzar sesiones):
  [Claude Code](/es/how-to/integrations/claude-code/),
  [Codex](/es/how-to/integrations/codex/),
  [Grok Build](/es/how-to/integrations/grok/).
- [Grabación de sesiones privilegiadas](/es/reference/modules/recording/)
- [Identidad, permisos y gobernanza](/es/reference/modules/vi-governance/)

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/)
- [Honestidad y límites](/es/start/honesty-and-limits/)
