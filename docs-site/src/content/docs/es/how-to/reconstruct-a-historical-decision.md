---
title: "Reconstruir una decisión de autorización histórica"
description: >-
  Qué prueba una reconstrucción, qué no puede probar, y cómo un auditor
  reproduce el allow del lunes tras la revocación del martes.
sidebar:
  order: 22
---

Un auditor puede preguntar qué **decidió** una política en una fecha
pasada. El plano de control responde desde el **libro de evidencia**: la
versión de política registrada y las entradas de esa decisión. **No**
evalúa la política activa hoy.

## Comando

```bash
olivares policy replay \
  --at 2026-09-15T12:00:00Z \
  --principal agent-7 \
  --resource public.customers \
  --resource-kind postgres.table \
  --action SELECT
```

O una fila almacenada:

```bash
olivares policy replay --decision-id <decision-id> -o json
```

HTTP: `POST /v1/m/governance/decisions/replay` y
`GET /v1/m/governance/decisions/{id}/reconstruct`. Este corte no añade
botón en consola. Use la CLI o HTTP.

## Qué prueba

El estado `reconstructed` significa: existe una decisión
`live_authorization` registrada; el artefacto nombrado está
**retenido**; este binario puede ejecutar el evaluador (Cedar); la
reevaluación usa el artefacto **original**; no se leyó el PDP en vivo.

`policy_version_id` es el id del artefacto. No es el `PolicyVersion` del
testigo de ruta ni el número de revisión de autoría.

## Qué no prueba

- Honestidad del punto de aplicación original. Un allow almacenado no
  autoriza a repetir el efecto.
- Autenticidad del productor ni integridad de la cadena.
- Actividad externa sin decisión de Olivares: la respuesta es
  `COULD NOT RECONSTRUCT`.
- Motores que este binario no ejecuta (OPA/Rego solo se redacta aquí).

## COULD NOT RECONSTRUCT

```
COULD NOT RECONSTRUCT
missing: authorization_decision
```

`missing` nombra el hecho ausente. Las filas antiguas quedan
**unknown**; el almacén no las rellena con la política actual.

## Lunes, martes, miércoles

Lunes: registrar allow. Martes: revocar. Miércoles: `--at <lunes>`
sigue respondiendo **allow** desde el artefacto del lunes.
