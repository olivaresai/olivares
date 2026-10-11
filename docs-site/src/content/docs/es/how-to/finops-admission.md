---
title: "Reservar, liquidar y conciliar la admisión FinOps"
description: >-
  Cómo un efecto facturable reserva gasto y recibe un único handle, cómo
  confirmar o liberar ese handle, cómo se comportan los reintentos y las
  confirmaciones tardías, y cómo leer la conciliación de admisión.
sidebar:
  order: 21
---

Los presupuestos y el análisis de gasto de FinOps son funciones de **[Business](https://olivares.ai/pricing)**. Community conserva el seguimiento de costes por sesión y la exportación de datos. Los presupuestos guardados antes de 0.1 se pueden consultar y eliminar, y se aplican mientras el módulo FinOps esté activo; Community no puede crearlos ni modificarlos. Las evaluaciones y los entornos de prueba siguen en Community.


Un efecto facturable **reserva** su gasto estimado antes de ejecutarse y recibe
**un único handle**. Cuando el efecto se ha ejecutado, quien llama **confirma**
(commit) el coste medido con ese handle. Si no se ejecutó, **libera** la
retención. Las puertas del propio motor (el proxy de inferencia, el arranque de
sesiones y los trabajos programados) lo hacen por sí mismas. Esta página es para
un conector que llama a las rutas y para quien opera y lee el resultado.

## Reservar

```bash
olivares finops admission reserve --data @reserve.json -o json
```

```json
{
  "scope": "model_gateway",
  "idempotency_key": "gateway/req-7f3a",
  "estimate_micro_usd": 2000000,
  "actor_ref": "alice",
  "dims": { "provider_ref": "anthropic", "model_ref": "claude-sonnet-4" }
}
```

La estimación se retiene contra cada presupuesto con aplicación que abarca la
petición y, si se indica `actor_ref`, contra los límites de gasto de ese actor.
La respuesta lleva el único handle:

```json
{
  "allowed": true,
  "handle": "0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c",
  "estimate_micro_usd": 2000000
}
```

Una estimación de cero no retiene nada ni devuelve handle. Un tope que ya ha
superado su límite la rechaza igualmente.

| Estado | Significado |
|---|---|
| 200 | Admitida. `handle` está vacío cuando no se retuvo nada. Con `"unreachable": "allow"`, una admisión que no se pudo establecer se admite sin retención y con la razón `admission could not be established; admitted without a hold (unreachable=allow)`. |
| 402 | Un veredicto de bloqueo: un presupuesto o límite de gasto con `action=block` no tiene margen (`spend_limit` es true cuando lo rechazó un límite por asiento), el conjunto de presupuestos es demasiado grande para evaluarlo, o el inquilino está bajo una frontera de activación del ciclo de vida de intentos o su estado no se puede leer. La razón dice cuál. |
| 429 | Un presupuesto con `action=throttle` no tiene margen. |
| 503 | No se pudo establecer la admisión: no se pudo leer el almacén de presupuestos, la clave siguió ocupada tras los reintentos (la reclamación en curso de otra llamada, o un par de retenciones que publicó una versión anterior y cuya retención restante aún retiene), la clave guarda una reclamación que dejó una versión anterior, o la lista de retenciones debidas de la clave no se puede decodificar. La razón es `budget store unreachable (deny-closed)`. |
| 409 | La clave de idempotencia se usó con otro contenido. |
| 500 | La fila de admisión de la clave no superó su comprobación de integridad. La clave se rechaza en cualquier postura hasta reparar la fila. |
| 400 | El documento no se puede atender: un ámbito desconocido, una clave ausente, una estimación negativa o un campo que el esquema no publica. |

Un 402 o un 429 por un tope es definitivo: no reintentes el mismo efecto antes
de un nuevo periodo o de un límite mayor. Un 402 por una frontera de activación
dura mientras el ciclo de vida de intentos lleva el libro mayor del inquilino.
Un rechazo 402, 429, 503 o 500 es además una fila de auditoría
`finops.admission.denied`.

La postura por defecto cuando no se puede establecer la admisión es **denegar**.
Pon `"unreachable": "allow"` en el documento de reserva para admitir esa
petición sin retención: la respuesta es 200 con la razón de arriba, y el
registro del motor la anota en ERROR con el inquilino, el ámbito, `posture=allow
outcome=admitted` y la clase del fallo, nunca con el mensaje del propio almacén.
Un valor desconocido es denegar.

## Regla de reintento

- **Reserva.** La misma clave de idempotencia con el mismo contenido, dentro de
  la ventana de repetición, recibe el handle que recibió la primera llamada
  (`"replayed": true`). La ventana dura cinco minutos desde la respuesta, o desde
  la confirmación una vez confirmada la retención. Fuera de la ventana, y para
  una llamada que no retuvo nada, la petición se evalúa de nuevo.
- **Confirmación.** Tras una respuesta incierta, repite `commit` con el mismo
  handle y el mismo importe tantas veces como haga falta. Cada repetición acaba
  en las mismas filas. Otro importe tras una confirmación se rechaza con **409**:
  vale el primer coste medido.
- **Liberación.** Repítela sin límite. Una liberación nunca deshace una
  confirmación.

## Confirmar y liberar

```bash
olivares finops admission commit --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c","actual_micro_usd":1500000}'
olivares finops admission release --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c"}'
```

Ingiere el coste medido antes de confirmar, para que el techo nunca cuente de
menos. Los documentos llevan `handle` y nada más que nombre una retención:
cualquier otro campo se rechaza con 400. Un handle vacío no liquida nada.

| Estado | Significado |
|---|---|
| 200 | Liquidada, o ya liquidada del mismo modo. |
| 409 | Otro importe tras una confirmación; una retención cuya admisión sigue siendo una reclamación en curso, así que nadie recibió ese handle; o una retención cuyo dinero pertenece ya al ciclo de vida de intentos (`lifecycle_api_required`). |
| 500 | La fila de admisión que nombra la retención no superó su comprobación de integridad. No se escribió nada: conserva el coste ingerido y repite la misma llamada cuando la fila esté reparada. |
| 400 | Un handle que no es una identidad de retención, o un importe negativo. |

`committed: true` y `released: true` dicen que la llamada se aceptó, no que
cambiara una fila: confirmar un handle vacío no liquida nada, y liberar una
retención confirmada la deja confirmada.

## Regla de confirmación tardía

Una confirmación registra que el efecto ocurrió, así que se acepta por tarde que
llegue: tras una liberación, tras caducar la retención o tras que otra llamada
se quedara con la clave. Las filas de la retención pasan a confirmadas con el
importe medido y conservan el instante en que dejaron de retener. Ningún techo
de presupuesto cambia, porque una fila liberada o caducada no retiene nada. Una
confirmación tardía de una retención caducada baja `expired_unsettled` en la
siguiente conciliación.

## Retenciones que dejó una versión anterior de la admisión

Una base de datos que ejecutó una versión anterior de la admisión puede guardar
filas que escribió esa versión. Una clave que admitió con dos retenciones se
liquida con cualquiera de las dos, y ambas se liquidan juntas. Una reclamación
que dejó en curso solo se retira bajo un instante de parada que declara quien
opera:

```bash
OLIVARES_FINOPS_ADMISSION_LEGACY_WRITERS_STOPPED_AT=2026-09-20T09:00:00Z
```

Ponlo en el instante en que se detuvo cada escritor de la versión anterior, como
hora RFC 3339 en UTC terminada en `Z`. Se lee una vez al arrancar. La
recuperación retira esa reclamación solo cuando han pasado cinco minutos desde
ese instante, y solo mientras ninguna fila de esos escritores tenga fecha
posterior. Vacío, el valor por defecto, no retira ninguna. Un texto que no sea
un instante así no retira ninguna y deja un error en el arranque. El informe
muestra el estado como `legacy_stop`: `absent`, `invalid`, `future`,
`contradicted`, `waiting` o `usable`.

## Conciliación

El motor ejecuta la recuperación cada minuto y el trabajo de conciliación cada
cinco minutos, para cada inquilino activo.

```bash
olivares finops admission reconciliation -o json
olivares finops admission reconcile -o json
```

`reconciliation` solo lee, con lectura de presupuestos: no recupera, no barre y
no emite nada. `reconcile` es el trabajo, con escritura de presupuestos: ejecuta
la recuperación, barre las retenciones que caducaron sin liquidar y emite un
hallazgo de tipo `finops_reservation_drift` cuando `drift` es true. La consola
muestra el mismo informe junto a los presupuestos.

| Campo | Significado |
|---|---|
| `active`, `committed`, `released` | Filas del libro mayor en cada estado |
| `expired_unsettled` | Filas que nadie liquidó; el TTL devolvió el margen |
| `active_lapsed` | Filas caducadas que el trabajo aún no ha barrido |
| `idempotency_orphans` | Filas de admisión reservadas cuyo handle no tiene filas en el libro mayor |
| `owed_remaining` | Retenciones aún debidas por una reclamación en curso o por una fila publicada |
| `legacy_pending`, `legacy_owes_release` | Reclamaciones y liberaciones que dejó una versión anterior |
| `unresolved` | Escrituras de recuperación cuyo resultado aún no consta; la siguiente pasada decide de nuevo |
| `undecodable` | Filas de admisión cuya lista de retenciones debidas no se puede decodificar |
| `frontier_blocked` | Retenciones que la recuperación no puede liquidar ni descartar bajo una frontera de activación |
| `corrupt` | Filas de admisión que no superan su comprobación de integridad; se cuentan y nunca se escriben |
| `drift` | True cuando `expired_unsettled`, `active_lapsed`, `idempotency_orphans`, `unresolved`, `undecodable` o `corrupt` no es cero |

`unresolved` y `frontier_blocked` solo los cuenta `reconcile`, cuya pasada de
recuperación los rellena; `reconciliation` los informa como 0.

A la recuperación no le queda nada que hacer en un inquilino cuando los
contadores de `owed_remaining` a `corrupt` son todos cero. Trata una fila
`corrupt` como un fallo de integridad del almacén de ese inquilino y compárala
con la última copia de seguridad. Las retenciones que nombra vencen por su TTL;
nada las liquida hasta que la fila se repara.

## Ámbitos

| Ámbito | Quién llama |
|---|---|
| `model_gateway` | El proxy de inferencia y el enrutado de modelos: reserva antes de la llamada y después confirma o libera |
| `session_launch` | El arranque de sesiones operadas y la apertura de voz |
| `scheduled_job` | Disparos de orquestación, jueces de evaluación y tareas MCP |
