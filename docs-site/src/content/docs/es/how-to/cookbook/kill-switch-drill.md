---
title: "Receta: el kill switch del estate (y cómo ensayarlo)"
description: >-
  Una sola llamada detiene toda actuación gobernada del estate — o un agente.
  Rápido de activar por diseño; reactivarlo exige dos humanos, y el incidente
  deja un evidence pack. Ensáyalo antes de necesitarlo.
sidebar:
  order: 5
---

**Objetivo:** cuando un agente se descontrola a velocidad de máquina,
detenerlo — o detenerlo todo — *ahora*, con una sola llamada autenticada, y
levantar la parada más tarde bajo control dual con todo el incidente en el
registro.

La asimetría es el diseño: **activarlo es rápido** (tier de admin, sin gate de
aprobación — una parada de emergencia nunca debe esperar en una cola),
**reactivarlo es lento** (dos humanos distintos, y el incidente deja un
paquete de evidencia para la revisión posterior). No hay deliberadamente ningún break-glass en torno a la parada:
detenido *es* el estado seguro.

## Activar

```bash
# Stop the whole estate:
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"scope_kind":"estate","reason":"runaway agent incident #1234"}'

# Or stop one agent (by UUID or external id):
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"scope_kind":"agent","scope_ref":"agent:billing-reconciler","reason":"…"}'
```

Lo que se detiene, de inmediato y fail-closed: las superficies de **actuación**
gobernada — `claude.tool.use`, `mcp.tool.call`, `deploy.apply`,
`deploy.retire`, `orchestration.schedule.fire`, `voice.session.open`. Las
aprobaciones de actuación pendientes dentro del alcance se **cancelan en la
misma transacción**, de modo que nada aprobado-pero-aún-no-ejecutado se cuela
después de la parada.

Lo que deliberadamente *no* se detiene: la observación, y el propio governance
(hallazgos, ciclo de vida de identidad, compliance) — sigues pudiendo ver y
gobernar mientras está detenido. Reactivar una activación sobre un alcance ya
detenido devuelve `409` (es idempotente sobre el alcance, no una pila).

```bash
# Live posture — is anything stopped right now?
curl -ks "$BASE/v1/m/governance/killswitch/state" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Las reglas de guardián pueden activar la misma parada automáticamente (acciones
`stop_agent` / `stop_estate`) cuando se dispara una regla de contención — la
vía automática y la vía humana son el mismo gate, y una parada automática emite
un hallazgo CRITICAL.

## Reactivar (control dual)

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/reenable" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"reason":"root cause fixed: …"}'
```

Esto **abre una aprobación**, nunca levanta la parada directamente. La acción
está preclasificada como CRITICAL: **dos aprobadores humanos distintos**,
autenticación fuerte (AAL3) por decisión — y el suelo de dos humanos es
estructural, impuesto en la transacción aunque una política de aprobación
intente degradar el tier. El solicitante no puede ser uno de los que deciden;
una petición rechazada o caducada abre un quórum nuevo.

Tras la reactivación, una **post-revisión** por otro humano más (distinto del
que activó, del solicitante *y* de los que reactivaron) cierra el incidente —
hasta que queda registrada, el mismo alcance no puede volver a ser
detenido-y-reactivado sin revisión:

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/review" … 
curl -ks "$BASE/v1/m/governance/killswitch/$STOP_ID/evidence"   # the evidence pack
```

El endpoint de evidencia devuelve el pack del incidente — la parada, las
aprobaciones canceladas, las decisiones y el rastro — listo para el auditor.

## La consola

**Kill switch** en la sección Management es la versión de un clic del mismo
gate, con el estado en vivo y el flujo de reactivación:

<img class="light:sl-hidden" src="/console/killswitch-dark.png" alt="La vista de consola del Kill switch: estado del estate e historial por parada." />
<img class="dark:sl-hidden" src="/console/killswitch-light.png" alt="La vista de consola del Kill switch: estado del estate e historial por parada." />

## Ensáyalo

Un kill switch que nunca has accionado es una hipótesis. Trimestralmente, en
una ventana de mantenimiento:

1. Activa una parada **de alcance agente** sobre un agente de bajo riesgo;
   verifica que sus llamadas a herramientas se deniegan y que el hallazgo se
   dispara.
2. Recorre la reactivación: dos aprobadores, post-revisión, evidence pack
   extraído y archivado.
3. Cronometra el bucle de principio a fin — ese número es tu latencia real de
   contención, y el ensayo deja un rastro de ledger completo que lo demuestra.
