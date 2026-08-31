---
title: "Grabación de sesiones privilegiadas"
description: >-
  Un registro inmutable, anclado al ledger y reproducible de lo que una sesión de
  operador privilegiada hizo realmente sobre las superficies más sensibles del
  motor: frames append-only, expurgados en escritura, hash-chained por sesión y
  anclados al audit ledger firmado por PayloadHash. Alineado con PAM, LIVE.
---

La grabación (`modules/recording`) es el plano de **grabación de sesiones
privilegiadas** — el control alineado con PAM que los compradores de alta garantía
esperan para consolas y acceso de emergencia. Captura, como evidencia
estructurada, lo que una sesión de operador privilegiada hizo realmente sobre las
superficies de módulo más sensibles, y vincula esa evidencia al audit ledger
con alteraciones detectables para que no pueda reescribirse a posteriori. **Madurez: LIVE.**

## Qué graba

Una **sesión de grabación** es la ventana privilegiada de una credencial — la
sesión de login de un operador humano, o un token de servicio en el suelo de
break-glass — dentro de un tenant. Sus **frames** son un rastro append-only
(guardas de inmutabilidad a nivel de BD), un frame por acción de ruta-de-módulo
sobre una superficie grabada: quién, cuándo, la forma de la ruta y el permiso,
identificadores de destino expurgados, delegación, el resultado, y un SHA-256
unidireccional del cuerpo de la petición. Los frames son **eventos de acción
estructurados, nunca transcripciones ni cuerpos** — los valores de parámetro pasan
por un mecanismo de expurgo acotado en escritura, así que un valor con forma de email o de
credencial nunca persiste.

La captura se sitúa en el wrapper de ruta-de-módulo del motor y es **deny-closed**:
sobre una superficie grabada, sin evidencia añadible no hay acción privilegiada. El
ámbito grabado es cada ruta de break-glass para cada principal (el suelo
obligatorio y no configurable) más los namespaces privilegiados configurados por
tenant.

## Integridad y replay

Los frames de cada sesión están **hash-chained**, y la punta de la cadena queda
**anclada al audit ledger firmado** por `PayloadHash` — un evento de apertura
cuando la sesión empieza, anclajes periódicos mientras corre, y un sello cuando se
cierra. Reescribir cualquier frame rompe tanto la cadena de la sesión como sus
anclajes sellados al ledger. `GET /sessions/{id}/verify` recalcula la cadena y
comprueba cada anclaje; `GET /sessions/{id}/replay` reconstruye la cronología
legible por humanos correlacionada con la ventana de ledger de la sesión. La
superficie tiene su raíz en `/v1/m/recording/` (`sessions`, `replay`, `verify`,
`seal`, `config`, `ack`).

## Contexto acotado, dicho con claridad

- Graba **rutas de módulo** (`/v1/m/<ns>`); las superficies del núcleo `/v1` están
  auditadas al ledger pero no grabadas por frames — el replay las correlaciona en
  su lugar a través de la ventana de ledger de la sesión.
- En una sesión **activa**, los frames posteriores al último anclaje periódico
  quedan ligados solo por la punta de la cadena hasta el siguiente anclaje o sello;
  `verify` informa de `anchored_through` para que la frontera sea explícita, nunca
  implícita.
- No implementa **ni purga ni legal hold** — retención/legal-hold posee el borrado;
  los anclajes al ledger sobreviven a cualquier purga.
- Este es el subsistema de grabación que usa el **panel de gobernanza de agentops**
  para la grabación de E/S por sesión: cada frame de Claude Code puenteado se
  pliega al mismo patrón hash-chained y anclado al ledger.

## Relacionado

- [Seguridad](/es/reference/modules/ix-security/) — el plano circundante de
  seguridad y protección de datos (guardrails, DLP, retención, residencia).
- [Sesiones](/es/reference/modules/ii-sessions/) — aloja el runtime gobernado de
  sesiones de Claude Code cuya E/S por sesión graba este subsistema.
- [Honestidad y límites](/es/start/honesty-and-limits/) — la postura live /
  on-demand / deny-closed a lo largo del motor.
