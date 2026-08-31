---
title: "Construir un workflow gobernado (DAG)"
description: "Compón acciones gobernadas existentes en un grafo de dependencias, revisa su plan de ejecución sin efectos secundarios y ejecútalo tras una aprobación humana vinculada al grafo exacto que se revisó."
---

Un **workflow** encadena acciones que la plataforma ya gobierna — disparar una programación,
señalizar a otros módulos, enviar una notificación de prueba, pausar — en un grafo de
dependencias (un DAG). Ejecutarlo es un único acto privilegiado aprobado por un humano, y cada
paso que toca algo deja una fila en el mismo decision ledger de solo anexado que dejaría un solo
disparo.

Los workflows son **composición, no poder nuevo**. Deliberadamente no hay ningún tipo de paso que
ejecute un comando, llame a una URL arbitraria o lleve un payload: un grafo solo puede reordenar
verbos que el estate ya expone, bajo las puertas que ya existen. Ejecutar un workflow es de nivel
admin *y* requiere aprobación humana, por lo que nunca es una forma de alcanzar algo a lo que no
podías acceder directamente.

## La forma de un grafo

Un workflow es un conjunto de **pasos**, cada uno con un `ref` corto y único en el workflow, un
`kind`, su `config` tipado y las refs de las que `depends_on`. El grafo debe ser acíclico; el
servidor lo exige, junto con la existencia de referencias y los límites de fan-in/fan-out, antes
de almacenar nada.

| Tipo | Qué hace | Puertas por las que pasa |
|---|---|---|
| `schedule-fire` | despacha una programación gobernada existente | kill switch, presupuesto, el seam del despachador |
| `eventing-emit` | publica un evento `workflow.signal` al que otros módulos pueden suscribirse | — |
| `notify-test` | envía la prueba sintética por una ruta de alerta | el seam del actuador de notificaciones |
| `wait` | pausa la ejecución durante un tiempo acotado (1 s–24 h) | — |
| `approval-gate` | abre una aprobación humana **a mitad del grafo** y pausa hasta que se decida | la puerta de aprobación |

`eventing-emit` publica un tipo de evento **fijo**. La config del paso solo aporta una etiqueta,
por lo que un autor de workflows nunca puede falsificar un evento first-party como `edge.observed`
en la ingesta de otro módulo.

## 1. Declarar el workflow

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{
    "name": "release-train",
    "steps": [
      {"ref":"announce","kind":"eventing-emit","config":{"label":"starting"},"depends_on":[]},
      {"ref":"hold","kind":"approval-gate","config":{"reason":"release window"},"depends_on":["announce"]},
      {"ref":"deploy","kind":"schedule-fire","config":{"schedule_id":"<id>"},"depends_on":["hold"]}
    ]}'
```

La autoría es de nivel **write**. Un grafo rechazado vuelve como un `400` que nombra el paso
infractor:

```json
{"error":{"message":"step deploy: schedule <id> is retired","step_ref":"deploy"}}
```

La consola ancla ese `step_ref` al nodo del lienzo. Sustituir el grafo después es un único
`PUT .../steps` atómico — el grafo se revisa y aprueba como un todo, nunca paso a paso.

Cada cambio añade una instantánea completa a un revision ledger, y cualquier revisión anterior
puede restaurarse mediante la misma validación que usan los verbos activos.

## 2. Revisar el plan — sin efectos secundarios

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

El dry-run devuelve los pasos en orden topológico, con lo que haría cada uno, las puertas por las
que pasaría y una advertencia cuando una referencia ha quedado obsoleta desde que se guardó el
grafo (una programación retirada la semana pasada). No escribe nada, no despacha nada ni abre
ninguna aprobación, por lo que es una **lectura**, disponible para cualquiera que pueda leer
workflows.

También devuelve el `plan_hash` — la huella del grafo exacto. Sigue leyendo.

## 3. Ejecutarlo — dos fases, vinculadas a lo que vio un humano

Ejecutarlo es de nivel admin **y** pasa por una puerta. La fase uno abre la aprobación:

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
# 202 {"op":"run_request","approval_ref":"…","gate_status":"pending", …}
```

Un humano decide mediante la API de decisiones de gobernanza. Después, la fase dos consume esa
decisión pasando la referencia de vuelta:

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{"approval_ref":"…"}'
```

La aprobación queda **vinculada al hash del plan**. Edita el grafo entre las dos fases y el hash
cambia, de modo que la aprobación ya no autoriza nada y la ejecución se deniega — el «sí» de un
humano se aplica al grafo que revisó, nunca a uno sustituido después. La ejecución entonces usa
una **instantánea** de ese grafo, de modo que una edición a mitad de ejecución no puede cambiar lo
que ya se está ejecutando.

El deny-by-default se mantiene en todo momento: sin una puerta de aprobación cableada, una
ejecución se rechaza y el hueco de gobernanza se eleva como un finding en lugar de permitirse en
silencio.

## 4. Observar la ejecución

```bash
curl -sS "$OLIVARES/v1/m/orchestration/workflows/$ID/runs/$RUN" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Cada paso informa de su propio estado. Un paso cuyo upstream falló queda como `skipped` — la
ejecución nunca sigue más allá de un fallo y nunca informa de un éxito que no tuvo. Un `wait`
muestra cuándo se reanuda; un `approval-gate` muestra la aprobación que espera. Cuando se activa
una parada de emergencia, toda la ejecución se **congela** con un `paused_reason` visible y se
reanuda cuando se levanta la parada; una parada nunca se absorbe silenciosamente ni falla una
ejecución por completo.

Los pasos avanzan con un proceso en segundo plano, por lo que las esperas y las aprobaciones a
mitad del grafo progresan sin que nadie mantenga una solicitud abierta.

### Qué registra el ledger

Cada paso que actúa añade una fila inmutable atribuida al humano que inició la ejecución. Merece
la pena conocer dos propiedades:

- También se registra una ejecución **denegada**. Las negativas son evidencia.
- Si el resultado de una actuación llega después de que el runner ya hubiera desistido de ella,
  el resultado se **reconcilia** en el ledger con la referencia real de despacho. El paso puede
  decir «resultado desconocido» — pero el ledger nunca afirma una actuación que no ocurrió, ni
  oculta una que sí ocurrió.

## Deliberadamente fuera de alcance

- **Disparadores automáticos.** Un workflow se ejecuta cuando lo aprueba un humano. Cablear cron
  o un evento para iniciar una ejecución añade una ruta de actuación desatendida y sigue el rail
  de programación existente en su propio cambio.
- **Pasos arbitrarios con efectos secundarios** (HTTP, exec). Convertirían una superficie de
  composición en un motor de ejecución general y derrotarían la propiedad de que un workflow solo
  puede reordenar verbos ya gobernados.

## Véase también

- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — el motor de aprobación por el que pasan
  la ejecución y la puerta a mitad del grafo.
- [Referencia de eventos](/es/reference/events/) — `workflow.signal` y el permiso que necesita
  un suscriptor para recibirlo.
