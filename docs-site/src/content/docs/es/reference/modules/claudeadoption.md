---
title: "Adopción de Claude Code — el read-model de cómo se usa Claude Code"
description: >-
  Un modelo de solo lectura de la adopción y productividad de Claude Code: sesiones, líneas
  de código, commits, PRs, aceptación/rechazo de herramientas y tokens por modelo, agregados
  por equipo/desarrollador/día. Por equipo por defecto, drill-down por desarrollador opt-in.
  Frontera solo Claude API; nunca transporta coste.
---

La adopción de Claude Code (`modules/claudeadoption`) es uno de los 30 módulos. Es un
**read-model puro de cuánto se usa Claude Code y cuánto de lo que propone conservan
los desarrolladores** — la pregunta de adopción/ROI que se hace un estate centrado en
Claude, servida junto a la superficie de coste de [FinOps](/es/reference/modules/xi-finops/)
en lugar de dentro de ella. **Solo observa**: no hay superficie de actuación, y
**nunca transporta coste** (el coste es la superficie autoritativa de FinOps, así que una
medida aquí nunca puede contarlo dos veces).

## Qué ingiere

Consume la señal de bus `metric.sampled` que emiten ambos conectores de Claude — los
datapoints de productividad por sesión del receptor OTLP y los totales por
desarrollador/día del feed de administración de Analytics — y los pliega en un
read-model por `(sujeto, métrica, día, dimensión)`. Las métricas reconocidas son los
nombres de Claude Code: sesiones, líneas de código (añadidas/eliminadas), commits, pull
requests, uso de tokens (por modelo), decisiones de aceptación/rechazo de herramientas, y
tiempo activo. Una muestra cuyo nombre esté fuera de ese conjunto se ignora — el módulo
nunca persiste una medida que no pueda interpretar. La reingesta es idempotente: un día
re-obtenido o un delta re-entregado se pliega sobre la misma fila de clave natural en
lugar de contar dos veces.

## Las dos lentes (nunca se suman)

La misma actividad de Claude Code se reporta desde dos planos, mantenidos distintos y
**que nunca se suman**:

- **`analytics`** — el feed de administración de Analytics, la vista autoritativa por
  desarrollador/día (lleva el email del desarrollador como sujeto de ROI).
- **`telemetry`** — el plano OTLP, por sesión y en tiempo real, que lleva el tiempo
  activo y las etiquetas de equipo suministradas por el operador.

Son dos puntos de vista sobre la misma actividad, así que las superficies los presentan
uno junto a otro en lugar de como un total único.

## Las cuatro superficies

| Ruta | Responde a | Permiso |
|---|---|---|
| `GET /summary` | el resumen de productividad para ambas lentes en una ventana, más recuentos de desarrolladores/equipos distintos | `adoption:metrics:read` |
| `GET /trend` | una serie por día para una lente (por defecto `analytics`) | `adoption:metrics:read` |
| `GET /teams` | el desglose por equipo (de la lente de telemetría, la única que lleva etiquetas de equipo) | `adoption:metrics:read` |
| `GET /developers` | el drill-down de ROI por desarrollador, que expone el email del desarrollador | `adoption:developer:read` |

Las rutas se montan bajo `/v1/m/adoption/`. Los agregados de equipo/organización van en el
nivel ordinario de lectura del viewer — **por equipo por defecto**, sin exponer ningún
desarrollador individual. El drill-down por desarrollador es una **lectura privilegiada y
deny-closed** detrás de un permiso separado (por desarrollador **opt-in**), y una
organización puede acotarlo aún más mediante roles a medida.

## Frontera, dicho con claridad

- **Solo Claude API.** El read-model cubre únicamente lo que fluye por el plano de la
  Claude API — el feed de administración de Analytics y el exportador OTLP. Un estate de
  Claude Code servido por Claude Platform on AWS, Microsoft Foundry, Amazon Bedrock o
  Vertex AI que no exporte esta telemetría es **invisible aquí**, así que la ausencia de
  adopción nunca es prueba de ausencia.
- **Nunca transporta coste.** El coste es la superficie autoritativa de FinOps /
  `api_request`; este módulo mide actividad, no gasto.
- **Solo observación.** No tiene mitad de actuación — no tiene nada que desplegar,
  despachar, enviar ni aplicar.

## Relacionado

- [Coste y FinOps](/es/reference/modules/xi-finops/) — la superficie de coste
  autoritativa junto a la que se sitúa este módulo.
- [Referencia de eventos](/es/reference/events/) — la señal `metric.sampled` que
  consume.
- [Catálogo de módulos](/es/reference/modules/overview/) — los 30 módulos y su
  madurez honesta.
