---
title: Glosario
description: >-
  El vocabulario del producto, con precisión: el access map y sus ejes de
  honestidad, los tipos de observación, los primitivos de gobernanza y los
  términos operativos — cada uno definido tal como el motor lo usa realmente.
---

Los términos se definen tal como el motor los usa — varios son deliberadamente
más estrechos que su uso en la industria, y la estrechez es justo el punto.

### Access map (mapa R/RW)

El grafo del módulo III de **orígenes** (agentes, identidades, sesiones) y los
**recursos** que tocan, cada arista clasificada por [modo](#modo) y etiquetada
con su [signal source](#signal-source), [atribución](#atribución-confianza)
y [nivel de cobertura](#nivel-de-cobertura). Una capacidad clave diferenciada — uno de los 30
módulos, no el producto entero. Ver [¿Qué es Olivares AI?](/es/start/what-is-olivares-ai/).

### Estados de actuación: `v1` / `on-demand` / `seam`

Los tres estados honestos de la mitad *actuante* de cada módulo. **`v1`** — en vivo en
el binario por defecto sin aprovisionamiento. **`on-demand`** — construido y cableado,
pero deny-closed o degradado hasta que un operador lo aprovisiona (deploy
apply/retire, disparo de orquestación, despacho de voz). **`seam`** — una interfaz declarada
sin backend. El [catálogo de módulos](/es/reference/modules/overview/)
marca cada módulo; un guard de regresión en CI mantiene la tabla honesta.

### Agente

Un sistema de IA (un agente de código, un agente de servicio, un paso de workflow
orquestado) gobernado como una entidad de primera clase, distinto de la
[identidad](#identidad--nhi) (credencial) bajo la que corre. Enlazar agentes a
identidades es lo que afina la [atribución](#atribución-confianza).

### Agent sprawl

El término de analistas para los agentes de IA, copilots y servidores MCP que proliferan
por una organización más rápido de lo que nadie mantiene un inventario — agentes
desconocidos con acceso desconocido. Es el problema que el
[access map](#access-map-mapa-rrw) y el descubrimiento existen para hacer visible. Ver
[Vocabulario de analistas](/es/explanation/positioning/analyst-vocabulary/).

### AI TRiSM

*AI Trust, Risk and Security Management* — un framework **acuñado y propiedad de
Gartner** para gobernar la confianza, el riesgo y la seguridad de la IA. Mapeamos nuestras
capacidades a sus **temas** (gobernanza, inspección en runtime, enforcement en runtime,
gobernanza de información); **no** reproducimos el modelo exacto de Gartner, no afirmamos
conformidad, ni implicamos respaldo — la taxonomía es investigación propietaria de Gartner. Ver
[Vocabulario de analistas](/es/explanation/positioning/analyst-vocabulary/).

### Aprobación (HITL)

Una petición gobernada para realizar una acción con gate, abierta **deny-closed y
acotada en el tiempo**, vinculada al plan exacto, decidida por humanos autorizados con
separación de funciones y caducidad forzada del lado servidor, y registrada en el
[ledger](#audit-ledger). Ver la [receta](/es/how-to/cookbook/hitl-approvals/).

### Atribución (confianza)

Cómo de firmemente está atado un acceso observado a un origen *específico*:
**`attributed`** (hay una identidad por agente en el rastro) o
**`approximate`** (inferido — una service account compartida, un almacén con pérdidas, un
proceso de kernel aún no enlazado a un agente). El mapa muestra el nivel en lugar
de fabricar certeza; la consola también renderiza las aristas attributed como
*firmes*. Mejorar la atribución es un problema de identidad:
[SSO/SCIM y fuentes de identidad](/es/how-to/connectors/sso-scim-identity/).

### Audit ledger

El registro append-only, hash-chained, de toda decisión de gobernanza y toda
lectura privilegiada, protegido por firmas Ed25519 — cada registro lleva
`seq`, `prev_hash`, `hash`, `sig`, así que reescribir la historia es criptográficamente
detectable. Nunca contiene PII. Expuesto como exportación pull, sink push,
y verificación offline (`olivares audit verify`).

### Break-glass

Una elevación de emergencia gobernada y auditada para acciones con gate *específicas* —
deliberadamente **no** disponible para todo: re-habilitar un
[kill switch](#kill-switch) o finalizar el ciclo de vida de una identidad nunca puede
hacerse vía break-glass.

### Checkpoint

Un ancla firmada sobre la cadena del ledger de un tenant, escrita en un intervalo
(default 1h). Una copia **off-box** del checkpoint y la clave pública es
lo que hace la verificación resistente al atacante tras un compromiso del host.

### Collector

El proceso de borde solo-push (`olivares collector`) que corre
[fuentes](#fuente) cerca de los sistemas observados y empuja observaciones al
core sobre gRPC (opcionalmente mTLS). Los collectors **no tienen listener entrante**.

### Ruta cooperativa

Observación que depende de que el agente reporte — telemetría OTLP, hooks.
La máxima fidelidad cuando está presente, estructuralmente evadible, motivo por el cual el
[respaldo de kernel](#respaldo-de-kernel) y la auditoría nativa del almacén existen junto a ella.

### Nivel de cobertura

La fidelidad de la señal de un *recurso*, ortogonal a la atribución:
**clean** (la auditoría nativa clasifica R/W literalmente — pgAudit, CloudTrail),
**lossy** (las aristas aterrizan pero imprecisamente), **opaque / imposible pasivamente**
(sin superficie de auditoría pasiva utilizable — el producto lo dice en lugar de adivinar);
**mixed** marca una arista construida a partir de más de un nivel.

### Estate de demo

El estate sintético `serve --seed-demo` carga a través del bus de eventos **real**
(solo loopback, contraseña pública del árbol de fuentes, rechaza binds no-loopback).
Una herramienta de aprendizaje, nunca una ruta de instalación.

### Destino (conector de salida)

La mitad de entrega del catálogo de conectores: Slack, Teams, PagerDuty,
webhook, Splunk HEC, ServiceNow, Jira, email y similares — entregan
hallazgos y notificaciones, y no tienen nivel de cobertura porque no observan
nada.

### Bundle DR / KEK

El backup cifrado, **seguro para la continuidad del ledger**, que produce `olivares dr backup`;
sellado bajo una key-encryption key (derivada de passphrase o
provista por KMS) que debe viajar separada de los bundles.
Ver [backup y restauración](/es/how-to/backup-and-restore/).

### Drift (least-privilege drift)

El diff entre [Permitido y Observado](#permitido-frente-a-observado): la brecha
entre acceso concedido y ejercido. Tres clases — **acceso inesperado**
(observado, nunca concedido), **grant no usado** (concedido, nunca observado),
**reconciliación pendiente** (observado, enlace de identidad sin resolver).
[Receta de triage](/es/how-to/cookbook/drift-triage/).

### Edge / cost / finding

El **conjunto cerrado** de tipos de observación que una fuente puede emitir: una relación
de acceso, un hecho de coste de uso, o un hallazgo de detective. Cerrado por diseño — un
conector no puede inventar tipos nuevos, que es lo que mantiene el contrato de datos mínimos
exigible.

### Estate

Todo lo que gobiernas en un despliegue: los agentes, identidades, servidores MCP,
modelos, recursos y sus relaciones, a través de todas tus
organizaciones.

### Finding

Una observación de guardrail / postura / red-team / forense, que lleva un hash de
cualquier detalle sensible en lugar del detalle. Enrutada por el rail de notificación
y a [sinks SIEM](/es/how-to/cookbook/push-to-siem/).

### Guardian agent

El término de **Gartner** para IA que monitoriza o interviene sobre *otros* agentes de IA.
Olivares AI entrega el **resultado de gobernanza** de la categoría — observar,
diferenciar permitido-frente-a-observado, hacer gate deny-closed, registrar de forma inmutable — pero como un
**control plane de lectura primero fuera del data path**, no un LLM en línea
montando guardia. Ver [Vocabulario de analistas](/es/explanation/positioning/analyst-vocabulary/);
contrasta con el [guardian loop](#guardian-loop) en producto.

### Guardian loop

Una regla de gobernanza que vigila hallazgos y engancha contención
automáticamente — incluido el [kill switch](#kill-switch) — con la
ruta automática pasando por exactamente el mismo gate que un stop humano.

### Identidad / NHI

Un principal portador de credencial: humano, o **identidad no humana** (service
accounts, workload identities, API keys, identidades de agente). Los rosters llegan
de [fuentes de identidad](/es/how-to/connectors/sso-scim-identity/); enlazarlos
a agentes es el puente de la observación a la gobernanza.

### Respaldo de kernel

La ruta de observación no cooperativa: Tetragon captura eventos de fichero/red
del kernel fuera del control del agente; la fuente `ebpf` consume su exportación.
Siempre [`approximate`](#atribución-confianza) hasta que una identidad enlaza el
proceso a un agente. Ver [eBPF/Tetragon](/es/how-to/connectors/ebpf-tetragon/).

### Kill switch

El stop de emergencia del estate (o por agente): una llamada de nivel admin mata toda
actuación gobernada, fail-closed; re-habilitar requiere dos humanos distintos
más una post-revisión, sin break-glass a su alrededor.
[Receta de simulacro](/es/how-to/cookbook/kill-switch-drill/).

### Anotación MCP

El `readOnlyHint` / `destructiveHint` auto-declarado de un servidor — **no fiable por
la especificación MCP**, ingestado solo como un indicio de capacidad declarada
(`approximate`, ni observado ni permitido), corroborado y nunca
fiado por sí solo. Ver [gobernanza MCP](/es/how-to/connectors/mcp-governance/).

### Datos mínimos

La propiedad a nivel de wire por la que las observaciones llevan identificadores y
clasificaciones, nunca payloads, cuerpos SQL, prompts, secretos o PII. Una
propiedad del vocabulario del conector, no un ajuste.

### Modo

La clasificación de lectura/escritura de una arista: `read`, `write`, `readwrite`, o
`unknown` — tomada literalmente de la señal y **nunca inferida**; `unknown`
es una respuesta honesta, no una ausente.

### Observado / Permitido

Ver [Permitido frente a Observado](#permitido-frente-a-observado).

### Tokens opacos

Las credenciales del producto: tokens aleatorios, revocables, validados del lado servidor
(sesiones `olvs_…`, API keys `olvk_…`, el token de setup de un solo uso `olst_…`) —
deliberadamente no JWTs, así que poseer una clave de firma nunca puede acuñar acceso.

### Organización (tenant)

La frontera de aislamiento. Toda lectura y escritura de módulo está acotada al tenant; en
Postgres, la row-level security la respalda (el motor rehúsa correr como un
rol que pudiera saltarse RLS).

### Permitido frente a Observado

Las dos mitades que el access map diferencia: las aristas **permitidas** vienen de grants
declarados y política; las aristas **observadas** de telemetría y auditoría nativa. El
diff es [drift](#drift-least-privilege-drift).

### Admisión sellada

El gate de confianza deny-closed para plugins de conector fuera de proceso: digest
fijado + atestación Sigstore verificada contra anclas de confianza fijadas por el operador,
sin escotilla de escape.
Ver [construir un conector](/es/how-to/build-a-connector/).

### Token de setup

El token `olst_…` de un solo uso impreso a stdout en el primer arranque — toda la
historia de credencial de bootstrap; no hay credenciales por defecto. Solo se
almacena su hash.

### Signal source

Qué observador produjo una arista: `pg_audit`, `cloudtrail`, `otel`, `ebpf`,
`mcp_annotation`, un grant de política declarado, una señal A2A. La procedencia
nunca se colapsa: una READ de pgAudit y un indicio MCP no son la misma evidencia.

### Sink

Una suscripción de eventing que entrega eventos a un SIEM en su dialecto
(Splunk HEC, Sentinel DCR, Datadog, New Relic, o un webhook genérico firmado con HMAC),
en OCSF/CEF/LEEF/syslog/OTLP/JSON.
Ver [push a SIEM](/es/how-to/cookbook/push-to-siem/).

### SLI / SLO

Los niveles de servicio publicados: disponibilidad vía `/readyz`, éxito de petición,
latencia p99 de API e ingest — con los niveles de nodo único y HA enunciados
por separado y honestamente.
Ver [monitorización](/es/how-to/monitor-with-prometheus/).

### Fuente

Un conector de observación: hace `Open` con config, `Gather` de observaciones
al sink del motor, y `Close`. Scheduling propiedad del motor, vocabulario de datos
mínimos, Apache-2.0, nunca importa el core.
Ver [conectar una fuente](/es/how-to/connect-a-source/).

### Stop gate

El chequeo de enforcement que toda actuación gobernada hace contra el estado del
[kill switch](#kill-switch) — comprobado antes que cualquier otro gate, fallando
**cerrado** (el inverso del chequeo de budget, que falla abierto: un medidor roto
no debe causar una caída, pero un chequeo de stop roto sí).
