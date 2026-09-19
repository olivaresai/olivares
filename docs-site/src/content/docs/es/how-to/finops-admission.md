---
title: "Leer una denegación de admisión FinOps y la conciliación"
description: >-
  Cómo se ve una denegación de presupuesto en el cable, cómo fijar la postura
  ante un almacén inalcanzable y cómo leer la deriva reserva-frente-a-commit.
sidebar:
  order: 21
---

Un efecto facturable debe **reservar** el gasto estimado antes de ejecutarse.
El plano de control luego **confirma** el coste medido o **libera** la retención.

## Cómo se ve una denegación

Un tope duro (`action=block`) responde **HTTP 402**. Un tope suave
(`action=throttle`) responde **HTTP 429**. El cuerpo nombra la acción, el presupuesto y el margen que faltó.

```json
{
  "allowed": false,
  "action": "block",
  "budget_name": "eng-cap",
  "reason": "budget \"eng-cap\" block cap reached (monthly): no headroom to reserve 10000000 µUSD"
}
```

Este endpoint exige el permiso de escritura de presupuestos, así que responde a un
administrador y la cifra es suya. Lo que recibe la petición de una persona usuaria es
otro texto: el proxy de inferencia nunca repite esta razón. Un tope deniega con
`budget limit reached`, un límite por asiento con `spend limit reached`, y ninguno
lleva nombre de presupuesto ni importe.

```bash
olivares finops admission reserve --data @reserve.json -o json
```

## Almacén inalcanzable (denegar por defecto)

Si el almacén de presupuestos no se puede leer, la admisión **rehúsa**. El
motivo estable es `budget store unreachable (deny-closed)`. HTTP **503**.
También se escribe una fila de auditoría `finops.admission.denied`.

La postura por defecto es **deny**. Para fallar abierto en una petición, use
`"unreachable": "allow"`. El arranque de sesión también honra
`OLIVARES_SESSION_BUDGET_AVAILABILITY=fail-open`.

## Cómo leer la conciliación

```bash
olivares finops admission reconciliation -o json
olivares finops admission reconcile -o json
```

`drift` es verdadero cuando hay reservas expiradas sin asentar, activas
caducadas o filas de idempotencia huérfanas. Entonces FinOps emite un hallazgo
`finops_reservation_drift`. Trátelo como postura, no como reescritura del gasto.
