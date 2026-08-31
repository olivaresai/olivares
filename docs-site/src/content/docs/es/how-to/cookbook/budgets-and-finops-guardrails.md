---
title: "Receta: presupuestos y guardrails de FinOps"
description: >-
  Pon un límite de gasto en dólares firme al consumo de IA — por modelo, equipo,
  workspace o una sola identidad: alerta en umbrales y luego limita o bloquea en
  el tope. Más coste-por-resultado para que el gasto tenga un denominador.
sidebar:
  order: 2
---

**Objetivo:** "los agentes de este equipo dejan de gastar a 500 $/mes" —
declarado una vez, aplicado en vivo, con umbrales de alerta en el camino de
subida.

La aplicación de presupuestos es una de las actuaciones que están **operativas
en el binario por defecto**: un presupuesto en modo enforcing al llegar a su tope
deniega el gasto sin aprovisionamiento adicional
([el catálogo de módulos](/es/reference/modules/overview/) lo marca como
`v1 | v1`).

## Crear un presupuesto

```bash
curl -ks -X POST "$BASE/v1/m/finops/budgets" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "dimension": "team",
    "key": "payments",
    "limit_micro_usd": 500000000,
    "period": "monthly",
    "thresholds": [0.5, 0.8, 1.0],
    "action": "block"
  }'
```

- **El dinero está en micro-USD** (`limit_micro_usd: 500000000` = 500 $), de modo
  que no hay ambigüedad de coma flotante en el contrato.
- **`dimension` + `key`** acotan el presupuesto. Las dimensiones acotables
  incluyen `global`, `model`, `provider`, `agent`, `session`, `team`, `project`,
  `workspace`, `api_key`, `actor`, `service_tier`, `context_window`,
  `inference_geo`, `gateway` e `identity`.
- **`action`** es el modo de aplicación:

| `action` | En el tope |
|---|---|
| `alert` (por defecto) | solo showback — las alertas se disparan, no se deniega nada |
| `throttle` | el seam de actuación ralentiza el nuevo gasto |
| `block` | el seam de actuación deniega el nuevo gasto |

## Presupuestar una sola identidad

`dimension: "identity"` acota sobre el **external id de una identidad de roster
firme** — la identidad de workload o de agente que registraron tus
[fuentes de identidad](/es/how-to/connectors/sso-scim-identity/):

```json
{ "dimension": "identity", "key": "spiffe://corp/agent/billing-reconciler",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "throttle" }
```

La identidad se resuelve en la ingesta de coste a partir del binding de agente,
la clave de API o el actor de la muestra — de modo que el presupuesto sigue a la
identidad a través de las superficies, no a una sola clave de API.

## Verlo funcionar

```bash
# Live consumption vs limit, with run-rate projection:
curl -ks "$BASE/v1/m/finops/budgets/$BUDGET_ID/status" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Threshold crossings (your 50% / 80% / 100% alerts):
curl -ks "$BASE/v1/m/finops/alerts" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

En el tope, la comprobación de un presupuesto en modo enforcing devuelve
`allowed: false` con la acción (`throttle` o `block`) y el presupuesto que se
disparó — la denegación nombra su motivo. Las alertas también viajan por el flujo
de notificaciones, de modo que un [destino](/es/how-to/forward-audit-to-splunk/) de
Slack o PagerDuty oye el cruce del 80 % antes de la denegación del 100 %.

En la consola, **Cost & FinOps** muestra el gasto por dimensión con el estado del
presupuesto en línea:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="La vista Cost & FinOps con tendencias de gasto y postura de presupuesto." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="La vista Cost & FinOps con tendencias de gasto y postura de presupuesto." />

## Darle al gasto un denominador: los resultados (outcomes)

El coste-por-resultado es lo que convierte un presupuesto en una conversación de
negocio. Reporta resultados (un ticket resuelto, un PR mergeado, un caso cerrado)
y lee los paneles de valor:

```bash
curl -ks -X POST "$BASE/v1/m/finops/outcomes" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"kind":"ticket.resolved","subject_ref":"agent:support-triage","count":1}'

curl -ks "$BASE/v1/m/finops/value" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

El resumen de valor incluye el **riesgo de cancelación** — consumo sin resultados
— que es el inverso honesto de una métrica de éxito.

## Notas

- **Fail-open, deliberadamente:** si la propia comprobación del presupuesto falla
  (un fallo de lectura de FinOps), la inferencia se permite en lugar de
  bloquearse en silencio — un medidor roto no debe convertirse en una caída del
  servicio. El fallo se registra y es visible.
- La capacidad reservada (`reserved_micro_usd`) cuenta hacia el límite, de modo
  que un presupuesto no puede esquivarse reservando por adelantado.
- `cost_type` deliberadamente **no** es una dimensión de presupuesto — las líneas
  de fallback estimado viajan por la dimensión a la que pertenecen en lugar de
  formar un pool paralelo.
