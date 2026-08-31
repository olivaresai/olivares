---
title: Honestidad y límites
description: >-
  Qué hace Olivares AI hoy, qué está en fase de diseño o es posterior a v1, y qué
  el producto deliberadamente no hace. Sin capacidades inventadas.
---

Un control plane para IA es un producto de seguridad. Si exagera lo que cubre,
da una falsa sensación de seguridad — que es peor que no tener herramienta alguna. Por eso esta página es
el contrato explícito sobre **qué corre hoy, qué está planificado y qué queda fuera de
alcance a propósito.** El resto de la documentación se atiene a ello: los comandos de los tutoriales
y de las guías how-to están pensados para ejecutarse tal como están escritos, y allí donde el producto todavía no
cubre algo, la página lo dice en lugar de dar a entender que sí lo hace.

## Qué corre hoy

- **El binario único compila, arranca y alcanza un grafo de acceso poblado.** El
  binario `olivares` compila a un único artefacto estático con la UI web embebida.
  Arrancarlo con el estate de demo (`serve --seed-demo`) y recorrer
  *discover → grafo R/RW → drift Permitido-vs-Observado → inventario* se ejercita
  **de extremo a extremo** por la suite de tests. El [tutorial](/es/tutorials/zero-to-graph/)
  reproduce exactamente ese recorrido.
- **La configuración del primer arranque no requiere credenciales.** Una instalación nueva no tiene **credenciales
  por defecto**; el motor imprime un token de configuración de un solo uso en el primer arranque.
- **La API REST y el audit ledger son reales.** La [referencia de la API](/reference/api/)
  se renderiza desde el propio contrato OpenAPI 3.1 del producto. El audit ledger es
  append-only y hash-chained con checkpoints firmados con Ed25519, y puede exportarse
  en varios formatos SIEM.
- **Las releases están firmadas y son verificables sin conexión.** Firma, procedencia SLSA, SBOM
  y OpenVEX pueden todos [verificarse sin acceso a red](/es/how-to/verify-a-release/),
  y el producto incluye un [bundle air-gap](/es/how-to/air-gap-install/). **Todavía no
  existe ninguna release etiquetada**, así que esto describe lo que una release
  llevará, no un artefacto que puedas descargar y verificar hoy — la misma salvedad
  que declara `SECURITY.md`.

## Open core — qué es abierto vs enterprise

El producto es **open core**: el binario por defecto (AGPL) es toda la plataforma de
gobierno, y una pequeña línea comercial **aditiva** (`enterprise/`, construida solo con
`-tags enterprise`, nunca en el binario público) contiene las funciones reservadas. Dos
fronteras importan para el uso diario, y el build abierto responde por ellas con honestidad
en lugar de fingirlas:

- **El SSO es abierto para un único IdP.** El login con un solo IdP — **OIDC** (Authorization
  Code + PKCE) y **SAML 2.0** (respuestas firmadas, anti-replay) — corre en el binario por
  defecto **sin** `-tags enterprise`. Correr **más de un IdP activo** (por tenant / por
  dominio), la **aplicación de SSO** (exigir SSO / bloquear login con contraseña) y el
  **SCIM gestionado** son la línea enterprise reservada; activar un segundo IdP activo
  devuelve `multi_idp_requires_enterprise` — un límite de producto explícito, nunca un 501
  falso.
- **No hay tope de usuarios: las cuentas son ilimitadas en todas las ediciones.**
  Community, Business, los add-ons y Enterprise self-hosted admiten un número ilimitado
  de cuentas de usuario, sea cual sea el estado de la licencia: válida, caducada o
  inexistente. El tope de tres cuentas activas anterior al 2026-07-27 se eliminó por
  completo (el seam de asientos sigue en el código, como un no-op de compatibilidad que
  no rechaza nada), y que una licencia caduque nunca limita, desactiva ni borra una
  cuenta. El modelo comercial es un derecho por término sobre los add-ons, nunca un
  cobro por asiento.
- **El resto de la plataforma es abierto.** El bucle completo de gobierno — inventario, el
  mapa de acceso R/RW, la política RBAC/ABAC/Cedar, el audit ledger sellado, FinOps,
  compliance, egress SIEM, MCP, HA/distribuido — corre en el binario abierto sin comprobación
  de licencia. Los add-ons aditivos de `enterprise/` (federación multi-IdP, content
  firewall/DLP, hook hardening, el catálogo compilado de threat-intel, el egress de server-tool, el conector
  de CyberArk Conjur y el incident close-loop) son código nuevo
  que nunca estuvo en el producto abierto, no funciones quitadas de él. La validación de
  licencia en el binario abierto es **solo de atestación** — nunca habilita, deshabilita ni
  bloquea nada (véase
  [Open core y licenciamiento](/es/explanation/open-core-and-licensing/)).

## Qué está en fase de diseño o es anterior a 1.0

Olivares AI es **anterior a 1.0**. Los documentos de diseño del producto son explícitos en que buena parte
de la plataforma está en fase de diseño en algunas partes incluso allí donde el motor ya corre.
Trata la profundidad a nivel de módulo como **trabajo en curso** salvo que una página indique lo contrario.

- **La cobertura del mapa R/RW es escalonada, por diseño.** La fidelidad depende de lo que la
  fuente pueda demostrar. Es **limpia** en stores con auditoría nativa (SQL vía pgAudit,
  almacenamiento de objetos vía CloudTrail, warehouses/lakes), **con pérdidas** en algunos stores
  (documento/vector), e **imposible de reconstruir pasivamente** en otros (p. ej.
  Redis, SQLite, D1) — donde no se puede determinar lectura vs escritura, la arista se marca
  como `unknown`. La atribución es **firme** cuando una fuente lleva identidad por agente y
  colapsa a **`approximate`** cuando una cuenta de servicio compartida la oculta. El producto
  muestra esto con honestidad; no inventa certeza.
- **Las fuentes canónicas R/RW están cableadas en el `serve` de fábrica.** La raíz de composición
  registra los observadores a nivel de host — `pgaudit`, `s3cloudtrail`, `ebpf`,
  `runtime` y la fuente de introspección `mcp` — junto con los observadores de warehouse/lake
  (snowflake/databricks/bigquery/mssql/oracle/mongo/redshift/gcs/
  azure-blob/iceberg/openlineage/delta-sharing), todos configurables a través de
  `OLIVARES_SOURCES_CONFIG` (el
  [quickstart](/es/start/quickstart/) cablea una fuente `pgaudit` real contra el binario
  de fábrica y el smoke test lo verifica). Las **fuentes de documentos** de conocimiento
  (gdrive/confluence/notion/sharepoint/s3content) deliberadamente *no* son fuentes
  en tiempo de ejecución — se cargan bajo demanda mediante peticiones de ingesta de conocimiento. La
  [referencia de conectores](/es/reference/connectors/) marca cada tipo.
- **El valor por defecto es un binario único; el bus de eventos distribuido existe y es honesto
  sobre su semántica.** El valor por defecto corre como un único binario con un bus de eventos
  **in-process**. La **ruta de datos del collector remoto→core está construida y distribuida**: los collectors de borde
  corren conectores de fuente localmente y empujan observaciones a un core central sobre
  TLS mutuo con cert de cliente verificado, sin listener entrante (el modo `collector`).
  El **bus de eventos distribuido** se distribuyó con el trabajo de scale-out: un híbrido
  que mantiene el fan-out in-process para la entrega local (backpressure bloqueante, sin
  pérdida local) y puentea eventos entre nodos sobre **NATS**, habilitado por
  `OLIVARES_BUS_CONFIG` (una configuración de bus mal configurada **falla el arranque** en lugar de
  particionar silenciosamente el bus). La entrega entre nodos se documenta con honestidad como
  **at-most-once** — las desconexiones del puente y los descartes se cuentan en métricas dedicadas,
  nunca en silencio ([monitorización](/es/how-to/monitor-with-prometheus/)).
- **La *actuación* gobernada tiene tres estados honestos: live, bajo demanda y seam.** El
  producto observa y gobierna ampliamente hoy. Un pequeño conjunto de actuaciones está **live en
  el binario por defecto** sin aprovisionamiento: la aplicación de presupuesto FinOps (un presupuesto que aplica
  en su tope deniega el gasto), el transporte de despacho de notificaciones (enruta
  una vez se configura un destino), los findings/guardrails detectivescos de seguridad, y
  el runner de sandbox sintético in-process (aislado por construcción). Varios más están
  **cableados bajo demanda** — el backend está construido y cableado, pero permanece **deny-closed o
  degradado hasta que un operador lo aprovisiona** vía configuración de entorno: el módulo VII (deploy)
  `apply`/`retire` (un `503` hasta que se aprovisiona un executor), la orquestación del módulo IV
  *fire* y el despacho de voz del módulo XVI (ambos deny-closed hasta que se configura un dispatcher),
  el runtime de sandbox/red-team aislado a nivel de OS (sintético / DEGRADED hasta aprovisionar),
  la recuperación **semántica** respaldada por modelo (léxica y solo pública por
  defecto), y la *ejecución* de modelo en el módulo X (`503` hasta que se aprovisiona una credencial de inferencia).
  Lo que sigue siendo un **seam declarado y deny-closed** sin backend alguno es
  la sonda de telemetría de voz dormida (el bus de eventos distribuido salió de esta lista cuando
  se distribuyó el puente NATS — véase arriba). El
  [catálogo de módulos](/es/reference/modules/overview/) marca el estado Govern/Observe
  y Actuate de cada módulo; nada afirma actuar donde no lo hace. (Esto corrige una
  lectura anterior que listaba voz, el runtime de sandbox/red-team y la recuperación semántica
  como "live" — son bajo demanda: verificado contra un arranque de fábrica `serve
  --seed-demo`, 2026-06-08.)
- **El air-gap aplica al control plane, no a la inferencia de Claude.** El control plane
  corre completamente self-hosted y puede air-gaparse (SQLite de un solo nodo, release offline
  firmada, bundle air-gap). **El propio Claude no es autoalojable** — Anthropic no
  publica los pesos — así que cualquier *inferencia* de Claude alcanza la API de Anthropic, directamente o vía
  Bedrock/Vertex/Foundry. "Air-gapped" aquí significa que el plano de *gobierno y observación*
  y sus datos permanecen dentro de tu perímetro; **no** significa que Claude corra sin conexión.
  Los modelos que genuinamente autoalojas (p. ej. vía vLLM/Ollama bajo el módulo XXIII) pueden correr
  air-gapped; los modelos frontier brokered no.
- **Las rutas de módulo son un contrato beta separado.** Los endpoints de módulo (por
  ejemplo, el grafo del access map y el drift) no forman parte del contrato estable de
  53 rutas del núcleo; se publican como un documento **beta** separado: la
  [referencia de rutas de módulo](/reference/api-beta/) (servida en
  `/openapi.beta.json`). Beta significa que las formas pueden cambiar con aviso, y el
  detalle de cada campo sigue viviendo en las interfaces tipadas del producto. La
  [referencia de la API del núcleo](/reference/api/) documenta la superficie estable;
  no es toda la superficie del producto.

## Qué el producto deliberadamente **no** hace

- **Sin funciones ofensivas.** Olivares AI **no** es un framework de command-and-control
  y **no** escanea las credenciales de otras personas. El access map es una potente
  herramienta de reconocimiento *para que los defensores gobiernen su propio estate* — verlo es una
  acción privilegiada, con alcance de tenant y completamente auditada. Esta línea defensiva es
  intencional y se mantiene explícita (véase el
  [threat model](/es/explanation/security/threat-model/)).
- **Sin forwarder S2S nativo de Splunk.** Reenviar a Splunk es una *posture* documentada
  — apunta un Universal Forwarder a un fichero que el control plane añade, o empuja sobre
  Splunk HEC — **no** un emisor nativo Splunk-a-Splunk. El
  [how-to de Splunk](/es/how-to/forward-audit-to-splunk/) es explícito sobre qué stream
  es cuál.
- **Sin webhooks salientes en el contrato REST.** El documento OpenAPI no define
  `webhooks`. La entrega firmada saliente existe como un *connector de destino* de notificación
  interno, y el endpoint SCIM Security-Event-Token es un receptor *entrante*
  — ninguno es un webhook OpenAPI. Véase la
  [referencia de la API](/reference/api/).
- **El fine-tuning de modelos (módulo XXIII) es posterior a v1.** Su ausencia es una decisión, no una
  carencia.

## Dónde los docs señalan una carencia upstream

Algunas cosas que esta documentación expone son **carencias en el producto**, reportadas a
los equipos que poseen el contrato relevante en lugar de taparlas aquí:

- El fichero OpenAPI comprometido que el sitio renderiza ahora se **regenera desde — y
  CI lo verifica byte a byte contra — el propio generador del motor**, así que ya no
  va por detrás de él (la carencia de endpoint anterior se reconcilió). La anterior subdocumentación
  de la lista de formatos de `/v1/audit/export` también se arregló upstream: el resumen y el
  mensaje de bad-request se construyen ambos desde el registro de formatos del motor
  (`audit.FormatList()`), así que no pueden volver a divergir — esta sección mantiene
  el registro porque ediciones anteriores de estos docs reportaron la carencia, y
  porque la misma podredumbre también había ocultado `leef` y `ocsf` de la ayuda y la
  autocompleción de la CLI hasta el 2026-07-25.
- La ruta de **push** del audit-ledger se distribuyó con el trabajo de interop SIEM/ITSM: una
  suscripción de eventing `audit.recorded` activa un ledger pump por tenant que
  reenvía registros sellados **at-least-once** a un sink configurado (Splunk HEC,
  Sentinel, Datadog, New Relic, o un webhook firmado con HMAC). La exportación de **pull**
  sigue siendo la forma adecuada para archivado WORM y re-verificación offline. Véase
  [push a tu SIEM](/es/how-to/cookbook/push-to-siem/) y el
  [how-to de Splunk](/es/how-to/forward-audit-to-splunk/). Lo que sigue sin existir
  es un emisor nativo de **protocolo S2S** de Splunk (abajo).

Si encuentras un comando que no se comporta como está documentado, eso es un bug en los docs
o en el producto — por favor repórtalo.
