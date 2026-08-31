---
title: "Receta: aprobaciones human-in-the-loop"
description: >-
  Protege las acciones destructivas tras aprobaciones gobernadas: abre una
  petición ligada al plan exacto, deja que humanos autorizados decidan con
  separación de funciones y caducidad impuestas en el servidor, y consigue que
  la decisión quede registrada en el ledger.
sidebar:
  order: 3
---

**Objetivo:** "un apply de despliegue (o un disparo de orquestación, o la
apertura de una sesión de voz) no ocurre hasta que un humano que *no* es el
solicitante lo aprueba — y la decisión es un hecho registrado".

El motor de aprobaciones está vivo en el binario por defecto; el
[modelo de governance](/es/how-to/govern-and-approve/#la-postura-human-in-the-loop)
explica la postura. Esta receta es el cableado operativo.

## 1. Cablea el gate de aprobación

Las acciones de módulo que mutarían infraestructura pasan por el puente
human-in-the-loop. Se habilita por configuración — sin él, esas acciones
quedan deny-closed:

```bash
OLIVARES_APPROVAL_BRIDGE_CONFIG=/etc/olivares/approval-bridge.json
```

Ejecuta el componente que *abre* aprobaciones como su **propia cuenta de
servicio, que nunca está en el pool de aprobadores**. La separación de funciones
se impone en el motor (el que abre no puede decidir su propia petición, y un
token de sistema no puede aprobar en absoluto) — si la cuenta del que abre es
también aprobadora, has construido un deadlock de liveness, no un control.

## 2. Abre una petición

```bash
curl -ks -X POST "$BASE/v1/m/governance/approvals" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "subject_kind": "deployment",
    "subject_ref": "deploy:payments-api",
    "action": "deploy.apply",
    "reason": "rollout v2.4.1",
    "expires_in_seconds": 3600
  }'
```

La petición se abre **deny-closed y con límite de tiempo**, ligada al plan
exacto que cubre. Si una *política* de aprobación habilitada hace match con
`(action, subject_kind)`, el `required_approvals` de la política es la
autoridad — un solicitante no puede bajar el listón desde el lado de la
petición.

## 3. Decide

```bash
# The queue (filter by status / action):
curl -ks "$BASE/v1/m/governance/approvals?status=pending" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT"

# The decision (approval-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/approvals/$ID/decisions" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"decision":"approve","note":"reviewed the plan hash"}'
```

Lo que el motor impone en el servidor — nada de esto es convención de cliente:

- **Separación de funciones:** el que decide se identifica por el id de usuario
  estable; el solicitante no puede decidir, y el mismo humano no puede decidir
  dos veces (un índice único, no una regla de UI).
- **Caducidad:** una petición caducada nunca puede recibir una decisión
  vinculante, ni siquiera antes de que el sweeper materialice el estado.
- **Suelo por risk-tier:** las acciones preclasificadas como CRITICAL (la
  familia del kill-switch, la finalización de credenciales y similares) exigen
  **al menos dos aprobadores humanos distintos con autenticación fuerte (AAL3)
  por decisión** — y el suelo es estructural: una política de aprobación que
  intente degradar el tier vuelve a quedar topada en el suelo en el punto de
  decisión.

## 4. El registro

Cada decisión se añade al audit ledger con el actor real en la misma
transacción — `GET /v1/m/governance/approvals/{id}/decisions` es el rastro
inmutable, y la [exportación pull](/es/how-to/forward-audit-to-splunk/) lo lleva a
tu SIEM. No puedes hacer un cambio gobernado que el ledger olvide en silencio.

## Notas

- `escalate_in_seconds` notifica al equipo de SoD si una petición queda sin
  decidir — úsalo para acciones críticas en producción.
- Cancelar (`POST …/{id}/cancel`) es para el solicitante o un admin sobre una
  petición pendiente; también queda registrado.
- Lo que aún está madurando es la **consola** de revisión más rica; las
  garantías del lado del motor de arriba están vivas
  ([alcance honesto](/es/how-to/govern-and-approve/)).
