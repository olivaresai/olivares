---
title: "Módulo IX — seguridad, guardrails y auditoría"
description: >-
  El control plane defensivo: guardrails deterministas que producen findings de
  datos mínimos, anomalías priorizadas y líneas de tiempo de incidentes
  verificadas con hash-chain — detective por defecto, con la aplicación en línea
  como seam opcional, gobernado y desactivado por defecto.
---

El módulo IX es la **capa defensiva y transversal** de Olivares AI. Convierte los
eventos del estate y el ledger de evidencia con alteraciones detectables en
**findings**, **anomalías priorizadas** y **líneas de tiempo de incidentes
reconstruibles**, de modo que un defensor pueda *ver* y *demostrar* qué hizo cada
agente. Es **detective por defecto**: observa y entrega evidencia, y nunca se
sitúa en la ruta de datos del agente.

## Qué es

El módulo abarca tres responsabilidades acotadas:

- **Guardrails** — una cadena de detectores deterministas y explicables
  inspecciona el texto del agente en las superficies `input`, `output` y
  `tool_args` en busca de secretos/PII, prompt-injection, jailbreak, contenido no
  permitido, violaciones del esquema de salida y el OWASP Agentic Top 10. Las
  detecciones portan referencias de framework (OWASP LLM Top 10 2025, OWASP
  Agentic Top 10 2026, MITRE ATLAS) literales de fuentes primarias, nunca
  inventadas. Un clasificador opcional y conectable (un guardrail-LLM hospedado)
  se ejecuta *detrás* de los detectores deterministas: solo puede **añadir**
  detecciones, nunca suprimir una, y su fallo se registra y se ignora.
- **Detección de anomalías** — correlaciona el drift de Permitido-frente-a-Observado
  que computa [el módulo III](/es/reference/modules/iii-access-map/) con findings de
  severidad alta, y une las señales anti-evasión del lado del kernel y del lado
  cooperativo: un agente que silencia su propia telemetría se trata como una
  señal, no como un punto ciego.
- **Forense / IR** — agrupa la evidencia en un **caso** y reconstruye su **línea
  de tiempo** a partir del ledger append-only y hash-chained, *verificando* la
  cadena y sus checkpoints firmados en lugar de confiar en ellos. Un ledger
  manipulado se reporta, no se oculta.
- **Grabación de sesiones privilegiadas** — un registro inmutable y reproducible
  de lo que una sesión de operador privilegiado hizo realmente sobre las
  superficies de módulo más sensibles del producto: un frame append-only por
  acción grabada (quién, cuándo, forma de la ruta, permiso, objetivos, resultado,
  digest de la petición), hash-chained por sesión y anclado al ledger de
  evidencia (apertura → anclajes periódicos → sellado), de modo que reescribir
  cualquier frame rompe tanto la cadena de la sesión como sus anclajes firmados
  en el ledger. El gate se ejecuta *antes* de la acción y es deny-closed: en una
  superficie grabada, sin rastro de evidencia adjuntable no hay acción
  privilegiada.

## Su contrato y entidades

El módulo IX es el **primer productor de la entidad core `Finding`**; no posee
ningún ledger ni captura, los consume. Sobre `Finding` posee tres entidades: un
**caso** mutable (ciclo de vida `open` → `investigating` → `contained` →
`closed`, con un snapshot de integridad tomado en el momento de la apertura), un
**enlace de caso** append-only que forma la cadena de custodia (el conjunto de
evidencia de un incidente es en sí mismo evidencia y no puede reescribirse) y una
**política de aplicación** por clase — donde la ausencia de una fila significa
*detective*.

Sus rutas se montan bajo la API del módulo y se envuelven con authn + tenant +
authz, con permisos read/write/admin con espacio de nombres. Leer findings es
sencillo (un finding es la propia alerta); las lecturas **sensibles para
reconocimiento** — la línea de tiempo verificada, la exportación a SIEM, la vista
de anomalías y la verificación de integridad independiente — son **privilegiadas
y auto-auditadas**: el acto de mirar queda registrado en la misma cadena que
inspecciona. Cada mutación (triaje, ciclo de vida del caso, postura de
aplicación) también se auto-audita. Las exportaciones a WORM/SIEM (CEF, syslog,
OTLP) portan campos de integridad por línea para que la cadena pueda
reverificarse **offline** mediante un store inmutable externo.

## Qué consume y produce en el bus

El módulo IX reacciona a [`finding.reported`](/es/reference/events/) (persistiendo
los findings de severidad alta de otros módulos en la vista de seguridad del
tenant) y a [`guardrail.observed`](/es/reference/events/), el canal de entrada
detective de texto observado ya expurgado. Produce un `FindingReport` por
detección sobre claves de enrutamiento `security_*` con espacio de nombres, que
la entrega aguas abajo enruta a SIEM/Slack/PagerDuty y que el cumplimiento mapea
a controles. El feed en vivo `guardrail.observed` proviene de la capa de
ingestión en runtime descrita en la [referencia del bus de eventos](/es/reference/events/):
es **deny-closed y de adhesión explícita** (apagado salvo que un operador lo
habilite), y el texto inspeccionado es la *referencia de recurso ya expurgada*
del connector de una arista `tool_args` — nunca el argumento crudo.

:::caution[Límites honestos]
- **Detective por defecto; la aplicación es un seam opcional.** El módulo observa
  y aporta evidencia. La aplicación en línea (bloquear una salida o una acción)
  está **apagada por defecto**, es de nivel admin y — donde hay un gate de
  aprobación HITL cableado — gobernada. Habilitarla es la única capacidad que
  toca producción; deshabilitarla (el valor seguro por defecto) siempre está
  permitido. Un guardrail que falla nunca debe romper producción.
- **El feed en vivo tiene una frontera de cobertura real.** En la superficie en
  vivo `guardrail.observed`, solo es detectable **PII o un secreto embebido en
  una referencia de recurso** (y patrones de recurso anómalos/sensibles).
  Prompt-injection y jailbreak necesitan el *contenido* del argumento, que se
  descarta en la fuente cooperativa y nunca llega al bus; las superficies
  `input` / `output` / `tool_result` requieren una fuente de contenido en proceso
  que este build no proporciona. Esto se declara, no se falsea.
- **La verificación de integridad puede no estar disponible, nunca falseada.** La
  cadena de hash siempre se verifica para la consistencia interna, pero la
  atestación de *checkpoints firmados* necesita la clave pública del ledger
  cableada; sin ella, la verificación de checkpoints se reporta como **no
  disponible** en lugar de fingirse. Un checkpoint falsificado se detecta, no se
  confía en él.
- **La cobertura hereda los niveles del access map.** Las anomalías construidas
  sobre el drift están acotadas por la cobertura de auditoría por niveles del
  módulo III; el catálogo de contenido (contenido no permitido) es un conjunto
  inicial conservador y no exhaustivo, mostrado como tal.
:::

## Relacionado

- [Referencia del bus de eventos](/es/reference/events/) — `finding.reported`, `guardrail.observed` y el canal de ingestión en runtime.
- [Live-ingest — el productor de observación en proceso](/es/reference/modules/live-ingest/) — el módulo deny-closed que publica el feed en vivo `guardrail.observed` que este módulo consume.
- [Módulo III — el read/write access map](/es/reference/modules/iii-access-map/) — el drift que este módulo correlaciona.
- [Catálogo de módulos](/es/reference/modules/overview/) — la capa del módulo IX y su estado de actuación.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre findings y aplicación.
- [Reenviar auditoría a Splunk](/es/how-to/forward-audit-to-splunk/) — exportar evidencia verificable a un store SIEM/WORM.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué está construido, observado y actuado hoy.
