---
title: "Receta: triaje del least-privilege drift"
description: >-
  Trabaja un resultado Permitted-vs-Observed hasta dejarlo a cero: clasifica
  accesos inesperados, concesiones sin uso y aristas pendientes de
  reconciliación, decide cada una (conceder, revocar o arreglar la identidad) y
  vuelve a comprobar — sin fiarte de una sola pista.
sidebar:
  order: 4
---

**Objetivo:** convertir el resultado de drift — la brecha entre lo que los
agentes *pueden* hacer y lo que se *observa* que hacen — en decisiones, con una
cadencia, hasta que el diff esté en silencio.

## 1. Extrae el drift

```bash
curl -ks "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

(O en HCL, para revisarlo en un PR: el data source de Terraform
`olivares_access_edges` con `include_drift = true` —
[gestionar como código](/es/how-to/manage-as-code/).)

El resultado tiene tres clases, y son problemas distintos:

| Clase | Significado | La pregunta a hacerse |
|---|---|---|
| **Acceso inesperado** | observado, pero ninguna concesión lo cubre | ¿es una concesión que falta, o una violación real? |
| **Concesión sin uso** | concedida, nunca observada en ejercicio | ¿por qué existe este permiso? |
| **Reconciliación pendiente** | observada, pero el enlace agente↔identidad está sin resolver | un problema de identidad, no (todavía) de seguridad |

## 2. Haz el triaje de cada clase

**Acceso inesperado** — lee los ejes de honestidad de la arista antes de actuar:

- `attribution_tier: firm` + `coverage_tier: clean` es el hallazgo de mayor
  calidad que vas a obtener: una identidad concreta tocó un recurso concreto y
  la propia auditoría del almacén lo clasificó. Decide: si es legítimo, declara
  la concesión (política o binding) para que el mapa refleje la intención; si
  no, revoca el acceso subyacente y trátalo como un incidente.
- Una atribución `approximate` significa que el *acceso* ocurrió pero el *quién*
  es una credencial compartida. No quemes una investigación en "qué agente
  fue" — el arreglo duradero es la
  [identidad por agente](/es/how-to/connectors/sso-scim-identity/), y hasta
  entonces la arista dice honestamente lo que no puede demostrar.
- Una arista que se apoya solo en una pista `mcp_annotation` **no es
  evidencia** — la pista es no fiable por especificación. Corrobórala con una
  fuente observada antes de decidir nada.

**Las concesiones sin uso** son sobreaprovisionamiento encontrado gratis: cada
una es una candidata a revocación, con la salvedad de que la ausencia de
observación solo es significativa allí donde hay cobertura — comprueba el tier
de cobertura del recurso antes de celebrar
([cobertura por niveles](/es/how-to/connect-a-source/#cobertura-por-niveles--sé-realista)).

**La reconciliación pendiente** se enruta al backlog de identidad: cablea o
arregla la fuente de roster que debería enlazar esa credencial, y la arista se
resuelve en la siguiente pasada.

## 3. Decide, registra, vuelve a comprobar

Toma la decisión donde está gobernada: declara las concesiones como código
([Terraform](/es/how-to/manage-as-code/)) o vía la API gobernada, protege la
dirección arriesgada tras una [aprobación](/es/how-to/cookbook/hitl-approvals/), y
deja que el ledger registre quién decidió qué. Luego vuelve a extraer el drift:
las aristas reconciliadas desaparecen del diff — solo quedan las brechas
genuinas. Esa convergencia es justamente el objetivo; el estate de demo lo
muestra en miniatura
([quickstart](/es/start/quickstart/)).

En la consola, el panel *Permitted vs observed* del **Access map** es esta
receta renderizada en vivo.

## Cadencia

El triaje de drift funciona como un bucle semanal corto más una vía de alerta
para la clase de alta señal (escrituras inesperadas firm + clean). Enruta esos
hallazgos a tu guardia mediante un
[destino de notificación](/es/how-to/forward-audit-to-splunk/) en lugar de esperar
a la pasada semanal.
