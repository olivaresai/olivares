---
title: "Módulo VII — despliegue e integración"
description: >-
  El único módulo que actúa sobre tu infraestructura: planifica y gobierna el
  ciclo de vida declarativo de agentes y servidores MCP y su conexión con el
  estate. Las mutaciones se controlan con human-in-the-loop, son dry-run antes de
  aplicar y reversibles — y el apply en vivo permanece deny-closed (503) hasta
  que se aprovisiona un ejecutor.
---

El módulo VII es el **único** módulo que muta la infraestructura del cliente — el
resto del producto es read-first. Aprovisiona, actualiza y retira agentes y
servidores MCP como operaciones **declarativas, versionadas y reversibles**, y
declara la conectividad y la identidad referenciada que un agente usa para alcanzar
un recurso empresarial. Como actúa, su listón de seguridad es el más alto del
producto, y la actuación en vivo queda retenida tras una junta deny-closed hasta que
un operador la aprovisiona explícitamente.

## Planifica y gobierna, luego (quizá) aplica

El ciclo de vida es `plan → apply → verify → retire`, reconciliando un estado
**deseado** contra el **real**. La separación que importa es **declarar ≠ mutar**:

- **Declarar** el estado deseado — crear, actualizar, hacer rollback de una
  definición (también vía el recurso manage-as-code `olivares_deployment`) — es
  exclusivo del control plane y **nunca toca la infraestructura**.
- **`plan`** es un diff dry-run puro; **`verify`** comprueba el drift y refresca el
  snapshot. Ninguno muta.
- **`apply` y `retire`** son las únicas operaciones que mutan. Son **de dos fases** y
  **deny-by-default**: la fase uno calcula el diff y *solicita* una aprobación humana
  ligada al hash del plan sin cambiar nada; la fase dos solo procede si la aprobación
  está `approved` **y** el hash del plan sigue coincidiendo — cualquier otro estado
  (pendiente, expirado, rechazado, sin gate, plan obsoleto) se rechaza y se registra.
  Re-especificar cambia el hash e invalida la aprobación (anti-TOCTOU).

El apply/retire que muta **no es en vivo por defecto**. La junta de actuación
([`Executor`](/es/reference/modules/overview/)) es deny-closed: sin ejecutor
aprovisionado, apply/retire/plan/verify **fallan en cerrado con un `503`** — el
control plane puede declarar el estado deseado pero no puede reconciliar con la
infraestructura real. Un motor real (Tofu/Terraform, GitOps, Kubernetes, Docker,
Nomad, Crossplane) más una fuente de credenciales de vida corta, por operación y
atestiguada se conectan **solo bajo configuración del operador**; en su ausencia, el
módulo nunca actúa en silencio.

## Entidades y el contrato declarado

El módulo declara cuatro entidades con namespace propio más el `Deployment` del
núcleo como snapshot aplicado:

| Entidad | Rol |
|---|---|
| **definition** | estado deseado — versión deseada vs aplicada, hash del spec, enlace al `Deployment` del núcleo |
| **revision** | historial de specs append-only e inmutable — la fuente reversible para el rollback |
| **wiring** | la conectividad **permitida** `agent → resource` que declara (el contrato que el módulo III contrasta) |
| **operation** | ledger de change-management append-only — versión, hash del plan, quién aprobó, resultado |

El spec deseado está **tipado y re-serializado desde la struct** (nunca un round-trip
de JSON del operador): los campos desconocidos se rechazan, corre un guard de
credenciales inline, y un spec que lleve material de credencial en claro se **rechaza
en la declaración**. Las credenciales viajan **solo por referencia**
(`<scheme>:<locator>`, esquema en allow-list) — una propiedad del cable, nunca un
secreto almacenado.

## Lo que produce en el bus (el lado PERMITTED del módulo III)

El módulo VII nunca escribe el access map; el módulo III es el único escritor de sus
aristas. En un `apply` confirmado, por cada wiring el módulo publica un evento de
policy-grant [`edge.observed`](/es/reference/events/) (`Source = policy`) que lleva solo
referencias y el modo. El módulo III lo reconcilia en el lado **PERMITTED** de su
diff permitted-vs-observed — de modo que lo que este módulo declara es exactamente lo
que el módulo III contrasta contra lo que observa. La identidad se liga por agente a
través del gobierno: una identidad no humana firme y única produce una arista
`attributed`; una identidad compartida o ausente se reporta como `approximate` —
**marcada, nunca falseada**.

:::caution[Límites honestos]
- **El apply en vivo es una junta deny-closed.** Sin ejecutor aprovisionado,
  `apply`/`retire` (y `plan`/`verify`) devuelven un `503` claro. El módulo planifica,
  gobierna, versiona y declara el estado deseado hoy; reconcilia con la
  infraestructura real solo cuando un operador conecta un ejecutor — nunca por
  defecto, nunca un no-op silencioso.
- **La aprobación y la atribución también fallan en seguro.** Sin el gate de
  aprobación toda mutación se deniega; sin el binder de identidad la atribución de un
  wiring se degrada, no se fabrica. `Start()` avisa una vez por cada junta sin
  conectar para que un despliegue roto sea visible.
- **Retirar un wiring no retracta su arista PERMITTED publicada.** El modelo de
  aristas no tiene verbo de retracción; el wiring se marca como revocado y el módulo
  III reconcilia la obsolescencia. Declarado, no oculto.
- **La profundidad del backend varía.** Entre los backends de actuación, algunos
  caminos de observación son más superficiales que otros (p. ej. salud a nivel de
  superficie en ciertos runtimes); estos se anotan como gaps honestos, nunca se
  reportan como un in-sync fabricado.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — la separación Govern/Observe vs Actuate y la junta `503`.
- [Módulo III — el access map](/es/reference/modules/iii-access-map/) — consume el wiring PERMITTED que este módulo declara.
- [Referencia del bus de eventos](/es/reference/events/) — el evento `edge.observed` y su payload minimal-data.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — el flujo de aprobación HITL tras cada mutación.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué actúa hoy y qué no.
- [Resumen de arquitectura](/es/explanation/architecture/overview/) — dónde se sitúa el módulo VII en la capa de gestión.
