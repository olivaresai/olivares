---
title: "Módulo XVI — agentes de voz y en tiempo real"
description: >-
  El plano de observar-y-gobernar para agentes conversacionales/en tiempo real. Gobierna quién
  puede abrir una sesión de voz, con qué modelo y proveedor, bajo una política default-DENY
  — y rastrea metadatos de sesión con una prohibición tajante de cualquier audio o contenido
  de transcripción.
---

El módulo XVI gobierna **agentes conversacionales y en tiempo real**. Es un
plano de **observar-y-gobernar**: **no** reimplementa un SDK de voz (Realtime API,
WebRTC, ASR o TTS) y nunca abre un stream de medios por sí mismo. Decide *quién* puede
abrir una sesión de voz, con *qué* modelo y proveedor, bajo *qué* política, y
rastrea los metadatos de esa sesión — nunca su contenido.

## Qué es

Abrir una interfaz de voz se trata como una **acción privilegiada**, no una operación
libre. La política es **default-DENY**: una sesión sin política que la permita se rechaza.
Una apertura es **de dos fases** y está **sujeta a human-in-the-loop** a través de la
[approval gate](/es/how-to/govern-and-approve/); está ligada a un `plan_hash` para que una
aprobación no pueda elevarse en silencio a un modelo más fuerte (anti-TOCTOU), auditada al
**principal real** (nunca `system`), y evidenciada **append-only**. El módulo
mismo nunca llama a un proveedor — la actuación sale por un seam de despacho separado.

La otra mitad es **observación**: el módulo rastrea solo metadatos de sesión —
estado derivado (live/idle/ended, computado en tiempo de lectura a partir de la recencia de actividad, sin
columna de ciclo de vida almacenada), recuento de turnos, duración, latencia (avg y max honestos a partir de
muestras reales) e idioma BCP-47. A partir de esto eleva **findings** de gobernanza: una
violación de política cuando la telemetría nombra un agente/modelo/proveedor que ninguna política permite, un
finding de latencia degradada cuando la latencia cruza un SLA de política, y un
finding de apertura no gobernada cuando se intenta una apertura sin gate cableado — la brecha se
expone y la apertura aun así se deniega.

## Contrato y entidades

El módulo declara tres entidades en el modelo de datos compartido:

| Entidad | Mutabilidad | Propósito |
|---|---|---|
| **session** | mutable (upsert) | metadatos de sesión; **cero contenido** |
| **policy** | mutable | declaración de gobernanza — quién puede abrir con qué modelo/proveedor (default-DENY) |
| **decision** | **append-only** | ledger inmutable de decisiones de apertura/cierre |

Una política coincide por agente, modelo permitido y proveedor permitido (cada uno específico o
comodín), con límites opcionales de minutos de sesión y de SLA de latencia. **Ninguna política coincidente
significa DENY.** El decision ledger registra cada `open_request`, `open` y `close`
con su veredicto de política, estado de gate y estado de resultado. El acceso de lectura es el rol
de viewer y superiores; declarar una política y abrir una sesión son acciones administrativas,
con ámbito de tenant y auditadas. Estas rutas del módulo se publican en la
[referencia de rutas de módulos](/reference/api-beta/) **beta** separada, no en el contrato
estable del núcleo — sus formas a nivel de campo viven en las interfaces tipadas del producto.
Los importes en dólares **no** están aquí; FinOps (módulo XI) es el dueño del coste.

## Qué consume y produce

El módulo es dueño de un seam de ingesta deny-closed — su propio evento `voice.telemetry.observed`
— a través del cual una sonda **in-process** alimentaría metadatos de sesión. El cable es
**de datos mínimos por construcción**: el parser de telemetría lleva una allow-list y
**rechaza el evento entero** si ve una clave prohibida, de modo que nunca puede persistirse audio, texto de
transcripción, texto ASR/TTS, contenido de prompt/respuesta ni PII de hablante. La única
señal de transcripción que se conserva es un hash de un solo sentido de un *localizador* de transcripción **externo** —
prueba de que existe una transcripción, nunca la transcripción. Los findings de gobernanza se emiten como
[`finding.reported`](/es/reference/events/) con detalle hasheado, tras el commit.

## Estado de Actuate

Una apertura gobernada despacha **en vivo**: una vez que el operador aprovisiona un dispatcher de voz,
una apertura aprobada acuña una **credencial efímera del lado del servidor** y
devuelve esa credencial y las coordenadas de conexión. La configuración de sesión
del operador fija la voz y la detección de turnos; un modelo configurado sustituye
al solicitado. Sin un modelo configurado, el dispatcher usa el modelo solicitado
que permite la política del tenant. La clave maestra del proveedor nunca sale del
servidor. Sin ese aprovisionamiento el
seam de despacho es **deny-closed**: una apertura aprobada se registra honestamente como
"declarada, no abierta" en lugar de fingirse.

## Configurar y probar una apertura gobernada

Activa el módulo existente con `olivares modules on voice`. El motor guarda la
selección y se reinicia una vez si cambia el conjunto de módulos en ejecución;
`olivares modules ls` muestra si está ejecutándose. La especificación del módulo
requiere FinOps y governance. Desactivar voice conserva sus políticas, metadatos
de sesión y ledger de decisiones.

El dispatcher se aprovisiona en el host del motor mediante
`OLIVARES_VOICE_DISPATCH_CONFIG`, la ruta absoluta de un archivo JSON del operador.
Permite su lectura solo a la cuenta del motor; las claves maestras del proveedor
pertenecen a ese archivo, nunca a argumentos CLI, filas de política o bundles
de conexión del cliente. Para un adaptador OpenAI:

```json
{
  "providers": [
    {"ref": "openai", "kind": "openai", "api_key": "<server-held provider key>"}
  ],
  "policies": [
    {
      "agent_ref": "contact-agent",
      "provider_ref": "openai",
      "model": "<your permitted realtime model>",
      "voice": "marin",
      "max_duration_seconds": 60
    }
  ]
}
```

Establece la variable de entorno para el servicio del motor y reinícialo. Un
archivo suministrado ilegible o con JSON inválido impide el arranque. Sin
configuración del dispatcher se mantiene el comportamiento «declarada, no abierta».
El archivo del operador elige adaptadores y ajustes de sesión; la política de voz
del tenant autoriza por separado el agente, modelo y proveedor solicitados. Usa
las mismas referencias de modelo y proveedor en ambos.

Tras iniciar sesión con `olivares login`, declara la política y pide aprobación:

```sh
olivares voice policies set --agent-ref contact-agent \
  --allowed-model-ref '<your permitted realtime model>' --allowed-provider-ref openai \
  --max-session-minutes 1 --max-latency-ms 300
olivares voice sessions open --session-ref contact-1 --agent-ref contact-agent \
  --model-ref '<your permitted realtime model>' --provider-ref openai -o json
```

La primera petición devuelve `op_status: requested`, un `approval_ref` y código
de salida CLI 7. No abre una conexión de medios ni acuña una credencial del
proveedor. Los aprobadores independientes requeridos deben aprobar esa referencia
desde la página de aprobaciones de governance o
`olivares governance approvals approve <approval-ref>`. Para nuevas peticiones
mediante el puente local por defecto, el solicitante no puede aprobar su propia
petición, ni con otra credencial de la misma cuenta. Repite la misma apertura
con `--approval-ref <approval-ref>`.
Una denegación de política o aprobación pendiente devuelve 403 y salida CLI 3;
un fallo del adaptador devuelve 502. Siguen aplicándose presupuesto y estate-stop.

Una petición configurada correcta devuelve `op_status: dispatched`. Su
`dispatch_ref` es una cadena JSON con `credential` de corta duración, coordenadas
`connect`, `transport`, modelo y caducidad. Trata la respuesta como credencial:
no la pegues en informes ni logs. Para OpenAI el cliente intercambia una oferta
SDP en la URL `connect` devuelta usando la credencial efímera y luego posee la
conexión de medios WebRTC. Acuñar una credencial no prueba que los medios conectaron.
El ledger de decisiones guarda una huella SHA-256 del bundle con credencial,
no la credencial de conexión. Los bundles antiguos también se convierten en
huellas al leerlos; las filas append-only existentes no se reescriben. Los
identificadores simples del proveedor conservan su valor.

Inspecciona los metadatos y decisiones conservados con:

```sh
olivares voice sessions get contact-1 -o json
olivares voice sessions decisions contact-1 -o json
olivares voice policies ls -o json
```

Usa el mismo directorio de datos entre reinicios. La política y las decisiones
append-only siguen disponibles tras reiniciar y tras desactivar y reactivar voice;
las credenciales del proveedor deben seguir aprovisionándose por separado. Los
comandos JSON anteriores usan las mismas rutas `/v1/m/voice` acotadas por tenant
que la API.

:::caution[Límites honestos]
- **La atribución de aprobación tiene un alcance.** El puente local por defecto
  conserva al solicitante autenticado de las nuevas aperturas humanas. Las
  aprobaciones existentes mantienen su atribución guardada. Un puente configurado
  explícitamente con token de servicio atribuye las peticiones a su credencial de
  servicio; no ofrece la misma garantía de separación de la persona iniciadora.
- **La observación necesita un productor configurado.** El plano opcional de
  llamadas OpenAI Realtime SIP usa `OLIVARES_VOICE_CALL_CONFIG` para verificar
  webhooks y atribuir tenant y proyecto, junto a las credenciales del proveedor
  en la configuración del dispatcher. Sin esa configuración o un productor de
  telemetría in-process, la mitad de observación queda vacía. Acuñar una credencial
  WebRTC no rellena recuentos de turnos ni latencia. Un plugin out-of-process no
  puede publicar el evento del módulo por el control plane gRPC, que no tiene RPC
  de eventos.
- **El cliente posee los medios de la sesión acuñada.** Este módulo no implementa
  un cliente WebRTC ni cierra la conexión de audio del cliente. El controlador
  opcional de llamadas SIP es una ruta separada. Un test de protocolo local con
  audio sintético no cualifica voz del proveedor, facturación, observación SIP ni
  cierre de medios.
- **El alcance de la consola es distinto.** La vista de voz edita políticas y
  muestra sesiones, decisiones y flujos de metadatos. Aprovisionar el dispatcher
  y conectar los medios del cliente son acciones distintas de esa vista; un
  recorrido de API o CLI no cualifica una acción del navegador.
- **Sin contenido, jamás.** Es una propiedad tajante del cable, no un ajuste: el
  esquema no tiene columna de contenido y el parser rechaza claves desconocidas. La latencia se muestra
  como avg/max honestos de muestras reales — nunca un p50/p95 fabricado.
- **Sin finding de "estancamiento".** El fin de una sesión de voz es silencio normal (como un agente
  terminado). Sin una línea base honesta, un finding de estancamiento sería un falso positivo, así que se
  omite deliberadamente.
- **Pre-1.0.** Como gran parte de la plataforma, este módulo está en profundidad en fase de diseño — ver
  [Honestidad y límites](/es/start/honesty-and-limits/).
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo XVI y su estado de actuate.
- [Referencia del bus de eventos](/es/reference/events/) — `finding.reported` lleva los findings de voz.
- [Módulo IV — orquestación](/es/reference/modules/iv-orchestration/) — el seam de despacho hermano (disparo en vivo).
- [Módulo X — enrutamiento de modelo y proveedor](/es/reference/modules/x-models/) — qué modelos puede permitir una política.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — la apertura gateada de dos fases en la práctica.
- [Honestidad y límites](/es/start/honesty-and-limits/) — la separación observar/gobernar/actuar.
