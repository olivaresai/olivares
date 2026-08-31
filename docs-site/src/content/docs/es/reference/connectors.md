---
title: Catálogo de conectores y niveles de cobertura
description: >-
  Los conectores de primera parte que el control plane puede cablear hoy,
  agrupados por el nivel de cobertura honesto que cada uno admite — limpio,
  con pérdidas, imposible pasivamente, cooperativo y aproximado por atribución
  — además de los destinos de salida.
---

Esta página es el **catálogo** de conectores de primera parte y, para cada uno, el **nivel de
cobertura honesto** que puede admitir. Es el complemento de
[conectar una fuente](/es/how-to/connect-a-source/), que explica el *modelo* de conector
(solo observación, datos mínimos, los tres tipos de observación) — léelo primero. Esta página
responde a la siguiente pregunta: *¿qué fuentes existen y cómo de buena es la señal de cada una?*

La cobertura se **escalona según lo que la superficie de auditoría de un sistema puede decirte
honestamente**, nunca según cuánto desearíamos que pudiera. Los niveles, tal como se usan a lo largo
de la documentación:

- **Cooperativo** — un agente o plataforma que informa de lo que hizo (OpenTelemetry, una
  API de administración de un proveedor). La máxima fidelidad *cuando está presente*; depende de que la fuente coopere.
- **Limpio** — un almacén que clasifica lectura frente a escritura de forma **nativa**, tomada literalmente de
  su propio rastro de auditoría (auditoría SQL, registros de acceso a datos de object-store / warehouse).
- **Con pérdidas** — un almacén cuya auditoría no puede separar limpiamente lectura de escritura ni un llamante de
  otro (document stores, linaje). Las aristas aterrizan, pero a menudo como `approximate`.
- **Imposible pasivamente** — un sistema sin superficie de auditoría pasiva utilizable (cachés
  en memoria, bases de datos embebidas de un solo fichero). No hay señal honesta de lectura primero; el
  producto no pretende lo contrario.
- **Aproximado por atribución** — el acceso es real pero la atribución es a un rol,
  proceso o credencial compartida, no a un agente resuelto, así que la arista es `approximate`.
- **Indicio no fiable** — una capacidad declarada (una anotación de herramienta MCP), corroborada,
  nunca fiada por sí sola.

:::caution[Lo que refleja este catálogo: conectores cableados en el build actual]
Esto lista los conectores **registrados en el conjunto de conectores del binario por defecto** hoy —
es decir, kinds que puedes nombrar en `OLIVARES_SOURCES_CONFIG` y que el motor cableará. El
producto es pre-1.0. Los conectores canónicos del access-map R/RW — **pgAudit**,
**S3/CloudTrail**, el respaldo **eBPF/Tetragon**, el inventario **runtime** y la introspección **MCP**
— y las **fuentes de documentos de conocimiento** ya están cableados y son configurables
en un `serve` de serie; algunos conllevan **requisitos de despliegue** (un sensor Tetragon, acceso
al host) cubiertos en [Requisitos de despliegue](#requisitos-de-despliegue-y-atribución-honesta)
más abajo. La cobertura está **escalonada honestamente**: la presencia de un conector aquí no es una afirmación de
atribución firme por agente, que sigue siendo la dependencia dura (una cuenta compartida colapsa
incluso un almacén de nivel limpio a `approximate`).
:::

## Cooperativo — Claude y telemetría de proveedores

Las fuentes de máxima fidelidad cuando están presentes. La fuente runtime de Claude Code corre
**fuera de proceso** como un plugin embebido (un dev build sencillo lo omite y el arranque avisa
honestamente en lugar de parecer sano).

| Kind | Observa | Notas |
|---|---|---|
| `claude` | Telemetría OTLP de herramientas de Claude Code + introspección MCP → aristas / coste / hallazgos | Plugin fuera de proceso; `attributed` cuando hay una identidad por agente presente, si no `approximate` |
| `claude-api` | Muestras de coste de la Admin-API de Claude + hallazgos de postura de gobernanza | En proceso; no-op offline (sin admin key) |
| `claude-compliance` | Evidencia del activity-feed de Claude Compliance → hallazgos | Solo GET por construcción; no-op offline |
| `claude-config` | Árbol estático de config de Claude (subagents / Skills / plugins) → aristas de **capacidad-declarada** | Solo metadatos — una superficie de capacidad, no un acceso observado |
| `claude-console` | IAM de la org de Claude → hallazgos de postura SSO/SCIM (roster de identidad + fuente) | |
| `claude-wif` | Roster de identidad no humana / workload-identity de Anthropic + aristas de scope permitido | Modela la federación declarada por el operador; señala los footguns de claves estáticas |
| `claude-managed-agents` | Inventario de managed agents de Claude + eventos de hilos (receptor webhook + pollers GET) | Fuente en streaming (`poll_seconds: 0`); offline es un no-op |
| `claude-projects` | Inventario de Projects de Claude Organization (membresía / claves API) + política de proyecto declarada por el operador | Admin-API de solo lectura; no-op offline |
| `claude-apps-gateway` | Postura de apps-gateway de Claude, grants de modelos declarados e ingest de eventos de auditoría → topología + hallazgos | Lee un `gateway.yaml` existente y una exportación de auditoría JSONL opcional |
| `claude-batch` | Inventario de Anthropic Message Batches + Files API, aplicación de políticas de lotes y vencimiento de retención de uploads | Nunca lee payloads ni contenido de ficheros; emite un hallazgo offline honesto sin una admin key |
| `claude-routines` | Inventario de Claude Code Routines (triggers programados) → aristas + hallazgos de cadencia/revisión | Solo GET; el contenido del prompt solo se hashea; streaming (`poll_seconds: 0`) |
| `cowork` | Receptor de logs OTLP/HTTP de Claude Cowork → evidencia de actividad | Plugin fuera de proceso (aislamiento de dependencias OTel-proto) |
| `cowork-analytics` | Analítica de uso de Claude Cowork | En proceso (solo cliente modelprovider) |
| `codex` | Muestras de coste de OpenAI Codex, evidencia de uso/auth/auditoría admin y hallazgos de adopción | Admin-API de solo lectura; las superficies sujetas a ventas se degradan a un hallazgo de postura |
| `cursor` | Coste facturado de la Admin-API de Cursor, logs de auditoría del equipo, inventario de miembros y postura de presupuesto | Un 403/404 por limitación del plan se degrada a un hallazgo, nunca falla |

### Perfil de framework GenAI neutral de proveedor (`gen_ai.*`) — opt-in

Los frameworks de agentes que promete el catálogo — **LangGraph / LangChain, CrewAI,
AutoGen / Microsoft Agent Framework, Google ADK** (y el OpenAI SDK, LlamaIndex,
Pydantic-AI, Strands, …) — **no** emiten el esquema `claude_code.*` de Claude. Convergen
en las [**convenciones semánticas GenAI** de OpenTelemetry](https://github.com/open-telemetry/semantic-conventions-genai)
(`gen_ai.*`). La misma fuente `claude` ingesta también ese perfil, de modo que una flota
instrumentada con OTel alimenta el **access map** y **FinOps** a través de un único ingest en lugar de un conector
a medida por framework — la integración de máximo apalancamiento.

**Este perfil es OPT-IN y está honestamente etiquetado como experimental.** Toda el área `gen_ai`
está en estado **Development** de OpenTelemetry (no Stable, jun-2026), así que se activa
solo cuando reflejas el propio gate de la especificación. Pon el `semconv_opt_in` del conector a una
lista separada por comas que contenga el token `gen_ai_latest_experimental` (reflejando
`OTEL_SEMCONV_STABILITY_OPT_IN`). Desactivada por defecto, una señal `gen_ai.*` sigue alimentando el
watchdog de silencio pero no mapea arista/coste — nunca afirmamos una estabilidad que las convenciones
no tienen.

Como las convenciones están en plena rotación, el ingest es **de doble nombre** (lee la
clave actual *y* el predecesor obsoleto que aún se emite en el mundo real) y
**multi-señal** (mapea **spans** de traza, el log **event** `gen_ai.client.inference.operation.details`
y reconoce las **metrics** del cliente):

| Lo que lee | Clave actual | También aceptado (obsoleta, aún emitida por) |
|---|---|---|
| Proveedor | `gen_ai.provider.name` | `gen_ai.system` (default en v1.36.0 o anterior; **Google ADK**, p. ej. `gcp.gemini`) |
| Tokens de entrada | `gen_ai.usage.input_tokens` | `gen_ai.usage.prompt_tokens` (**OpenLLMetry/Traceloop** → LangChain/LangGraph/CrewAI) |
| Tokens de salida | `gen_ai.usage.output_tokens` | `gen_ai.usage.completion_tokens` (igual) |

| atributo gen_ai | mapea a | confianza |
|---|---|---|
| `gen_ai.usage.*` (tokens) | `CostSample` (provenance **estimated** — tokens, no coste facturado) | — |
| `gen_ai.provider.name` / `request.model` / `response.model` | proveedor de coste + modelo (se prefiere response) | — |
| `gen_ai.operation.name = execute_tool` + `gen_ai.tool.name` | **arista de acceso** agente→herramienta (modo `unknown`) | `attributed` |
| `gen_ai.conversation.id` + `gen_ai.agent.{name,id}` | **arista de atribución** conversación→agente + ref de sesión | `attributed` |

#### Matriz de dialectos admitidos (normalizador multi-generación)

Las convenciones GenAI cambiaron en **tres generaciones que coexisten** en flotas reales de
2026. El ingest detecta la generación **por señal** a partir de marcadores exclusivos de cada generación
y estampa el evento normalizado con el pin de semconv correspondiente
(el hallazgo de postura `genai.semconv` registra el conjunto activo por run; un hallazgo `drift` informativo
por run señala cada dialecto **obsoleto** visto, así sabes qué flotas
necesitan actualizar su instrumentación). El **contenido** de los mensajes **nunca se lee de ninguna
generación** — las claves de contenido actúan solo como marcadores de dialecto (postura de datos mínimos).

| Dialecto detectado | Pin estampado | Marcadores exclusivos (verificados) | Emitido por (verificado jun-2026) |
|---|---|---|---|
| **OpenLLMetry/Traceloop** legacy (pre-semconv) | `openllmetry` | `gen_ai.prompt.{i}.*` / `gen_ai.completion.{i}.*` indexados, `gen_ai.usage.prompt_tokens`/`completion_tokens`, `llm.usage.total_tokens`, `llm.request.type`, `llm.vendor`, `traceloop.span.kind` | LangChain / LangGraph / CrewAI instrumentados con Traceloop fijados a **< openllmetry v0.55.0** (publicado 2026-03-29). Los proveedores en mayúsculas (`OpenAI`, `Langchain`) se pasan a minúsculas para que FinOps no se divida por mayúsculas/minúsculas |
| **eventos v1.36 o anterior** (el nombre de la propia especificación) | `1.36.0` | `gen_ai.system`; los cinco log events por mensaje `gen_ai.{system,user,assistant,tool}.message`, `gen_ai.choice` (reconocidos **por nombre** — su único atributo es opcional) | Spans LLM de Google ADK (`gcp.vertex.agent`), AutoGen (`autogen`), Microsoft Agent Framework — todos siguen emitiendo `gen_ai.system` |
| **mensajes v1.37+** (actual) | `1.41.1` | `gen_ai.provider.name`, `gen_ai.input.messages` / `gen_ai.output.messages` / `gen_ai.system_instructions`, el event `gen_ai.client.inference.operation.details`, `gen_ai.workflow.name` | Instrumentaciones oficiales de OTel; openllmetry **≥ v0.55.0** |

Una señal que solo lleva claves cuyos nombres son idénticos entre generaciones (p. ej. un
span `invoke_agent` de ADK: operación + agente + conversación, sin clave de proveedor en absoluto)
se normaliza bajo el pin actual — el mapeo aplicado es idéntico byte a byte, y la
release real del productor no es conocible desde el wire.

#### Convenciones MCP (`mcp.*`, semconv v1.39 — Development)

Existen exactamente cuatro atributos `mcp.*` upstream (`mcp.method.name`,
`mcp.protocol.version`, `mcp.resource.uri`, `mcp.session.id`); la herramienta viaja en
`gen_ai.tool.name` y el prompt en `gen_ai.prompt.name`. El ingest une estas
trazas con los propios hechos de gobernanza MCP del producto reutilizando los mismos resource
kinds que emite la ruta de Claude:

| Señal MCP | mapea a |
|---|---|
| cualquier span `mcp.*` del lado cliente con `server.address` | arista sesión→`mcp.server` (se une a las aristas `claude_code.mcp_server_connection`) |
| `tools/call` + `gen_ai.tool.name` | arista de acceso `mcp.tool` (`server.address/tool` cuando el endpoint es conocido) — el mismo kind que las invocaciones `mcp__server__tool` de Claude |
| `resources/read` / `resources/subscribe` + `mcp.resource.uri` | arista `mcp.resource` en **modo lectura** (URI saneada: credenciales/query eliminadas) |
| `prompts/get` + `gen_ai.prompt.name` | arista `mcp.prompt` en **modo lectura** (superficie de prompt) |
| spans de kind SERVER / metrics `mcp.client|server.*.duration` | solo liveness (degradación limpia — la vista del servidor no atribuye identidad de agente) |

#### Spans de agente (split CLIENT/INTERNAL de `invoke_agent` + `invoke_workflow`, semconv v1.41 — Development)

v1.41.0 dividió `invoke_agent` en una variante **CLIENT** (servicio de agente remoto) y una
variante **INTERNAL** (en proceso). Los frameworks reales violan el kind hoy (AutoGen y
Microsoft Agent Framework codifican CLIENT a fuego para agentes en proceso; Google ADK usa
INTERNAL), así que el ingest clasifica una invocación como **remota** solo cuando el span es
CLIENT **y** lleva un `server.address` — eso da una arista de delegación conversación→`genai.agent.remote`.
Todo lo demás se queda como invocación en proceso cubierta por la
arista de atribución conversación→`genai.agent`: degradada limpiamente, nunca un
"remote" fabricado. `invoke_workflow` (nuevo en v1.41; crews al estilo CrewAI) mapea una
arista conversación→`genai.workflow`. Los spans de agente siguen en **Development** upstream
(experimental) — no se afirma estabilidad alguna.

**Estable frente a experimental, honestamente:** el **mecanismo** (gate opt-in, detección de
dialecto + lecturas de doble nombre, mapeo span/event/metric, las formas selladas
`CostSample`/`EdgeObservation`) es estable en este producto. El **vocabulario**
que mapea (claves `gen_ai.*`/`mcp.*`, el enum de operación) está en **Development** upstream y
puede volver a renombrarse; ese es exactamente el motivo por el que el ingest normaliza cada generación en lugar de
fijar una sola. v1.41.1 es la última release *versionada* de las convenciones gen-ai
(se movieron a `open-telemetry/semantic-conventions-genai`, que no tiene releases a
fecha de jun-2026). Notas:

- **El coste se deduplica por id de span de W3C.** Cuando una operación reporta uso en *ambos*,
  su span y su event `operation.details` (comparten un id de span), se contabiliza una vez,
  no dos.
- **Las metrics alimentan liveness, nunca coste.** `gen_ai.client.token.usage` es un agregado;
  el span/event es el uso por operación autoritativo, así que contabilizar también la metric
  duplicaría la cuenta. Los histogramas de duración `mcp.*` de v1.39 se reconocen igual.
- **El proveedor puede ser `unknown`.** Si un span lleva un modelo pero ningún proveedor/system, el
  coste se atribuye a `unknown` en lugar de adivinarse a partir del id del modelo.
- **Un recuento de tokens solo-total no se divide.** El `llm.usage.total_tokens` legacy sin un
  split prompt/completion nunca se adivina en entrada/salida (sin coste fabricado).
- **OpenInference (Arize/Phoenix) es una convención distinta** y *no* es ingestada por
  este perfil — las claves `llm.*` que se leen aquí (`llm.request.type`, `llm.usage.total_tokens`,
  `llm.vendor`) son **marcadores legacy de OpenLLMetry**, no el namespace `llm.*` de OpenInference.

## Cooperativo — configuración de la superficie del agente local

Estas fuentes leen la configuración declarada de un agente local y emiten aristas
**permitidas**, además de hallazgos de postura. No son trazas de ejecución en vivo; cuando
un framework tiene OTEL nativo, el uso en vivo sigue llegando por el ingest `gen_ai.*`
anterior.

| Kind | Observa | Cobertura honesta |
|---|---|---|
| `opencode` | Capas JSONC locales `opencode.json` / `opencode.jsonc` → postura de permisos, postura de managed/admin-override, aristas permitidas de MCP/herramienta/agente personalizado, hallazgos de credenciales en config/share/autoupdate/OTEL y un fragmento de autoría | Solo lo declarado en la configuración. La capa gestionada se detecta localmente, pero no es un bloqueo inmutable: `OPENCODE_PERMISSION` en runtime, la redirección del directorio de tests y la configuración remota de la organización quedan fuera de este lector. OTEL nativo, cuando está habilitado, puede alimentar uso `gen_ai.*` en vivo mediante el exportador `OTEL_*` fuera de banda |
| `gemini-cli` | Capas `settings.json` de Gemini CLI (sistema/usuario/workspace) → aristas permitidas de MCP/herramientas, postura de brecha de aplicación e inventario de configuración efectiva | Solo lo declarado en la configuración; el uso en vivo viaja por el ingest `gen_ai.*` (la CLI lo emite de forma nativa). No es la API de Gemini (esa es la superficie de proveedor alojado) |
| `openhands` | `config.toml` + entorno de OpenHands → postura de sandbox/fijación de modelo/credencial/telemetría y aristas permitidas de MCP/acciones | Solo lo declarado en la configuración; uso en vivo mediante OTEL `gen_ai.*` nativo |
| `goose` | `profiles.yaml` + entorno de Goose (Block) → postura de ajustes admin/fijación de modelo/extensión/aprobación de herramientas y aristas permitidas de extensiones | Solo lo declarado en la configuración |
| `cline` | Namespaces de Cline / Kilo Code en `settings.json` de VSCode → postura de aprobación automática/allowlist MCP/credencial/fijación de modelo | Solo lo declarado en la configuración; no hay OTEL nativo upstream |
| `grok` | Grok Build (xAI) — el agente de codificación de terminal, leído por su configuración LOCAL: wire de hooks, eventos con veto documentado y postura de gobierno declarable | **NO es el conector de la API de xAI** (`xai` lee catálogo y coste, con `grok-build-0.1` entre sus MODELOS). Este lee el AGENTE, y no se solapan. La mitad de OBSERVACIÓN va por el ingest OTLP que Grok Build ya emite. `PostureEnforced` solo lo reclama `PreToolUse`, el único evento con veto documentado; el resto es `observed` |
| `openclaw` | `openclaw.json` de OpenClaw (descubrimiento JSON5, `$include` confinado) → postura de gateway/canal/herramienta/sandbox/skill/modelo por agente y aristas declaradas de canal/skill/modelo | Solo lo declarado en la configuración; no se ha verificado un hook PEP inline upstream |
| `hermes` | `config.yaml` + árboles de perfiles + ámbito gestionado de Hermes Agent → postura de terminal/canal/skill/seguridad/modelo/MCP y aristas declaradas | Solo lo declarado en la configuración; no se ha verificado un hook PEP inline ni OTEL nativo upstream |
| `google-adk` | JSON de sesión exportado de Google ADK 2.0 → inventario de agente/app, subagentes, llamadas de funciones de herramientas, transferencias, drift de herramientas aprobadas y correlación Vertex reasoningEngine | Exportación de solo lectura; nunca contenido de mensajes. Distinto de la superficie de plataforma `google-agent` |
| `agents-md` | Recorrido del repo de ficheros de instrucciones de agentes (AGENTS.md y ficheros de memoria/instrucciones por agente) → drift de línea base SHA-256 + escaneo de inyección de instrucciones / Unicode oculto / secretos | Datos mínimos: rutas saneadas + detalles hasheados, nunca contenido |
| `mcpb` | Extensiones de escritorio `.mcpb` instaladas / distribuidas → escaneo de postura del manifiesto, drift de allowlist enterprise y verificación de firma PKCS#7 | PERMITIDO-frente-a-OBSERVADO en la superficie de extensiones |
| `codex-managed-config` | Ficheros managed-config de OpenAI Codex → postura de aplicación + drift respecto a la línea base escrita | Solo observación: no puede impedir que un desarrollador eluda la capa gestionada (el equivalente de `managed-settings` para Codex) |

## Limpio — auditoría nativa del almacén (lectura/escritura literal)

Estos leen el rastro de auditoría **propio** de un almacén y toman la clasificación lectura/escritura literalmente
— nunca inferida del texto de la query. `pgaudit` y `s3cloudtrail` son las fuentes R/RW canónicas
en torno a las que se construye el [access map](/es/reference/modules/iii-access-map/) (sus
alias con guion `pg-audit` / `s3-cloudtrail` también resuelven).

| Kind | Observa |
|---|---|
| `pgaudit` | Rastro **pgAudit** de PostgreSQL (csvlog/jsonlog) → acceso a tablas R/RW, `READ`/`WRITE` literal del CLASS de pgAudit |
| `s3cloudtrail` | Eventos S3 de AWS **CloudTrail** → R/RW de objetos, lectura/escritura del flag `readOnly` de CloudTrail (también expone invocaciones de modelos Claude-on-Bedrock) |
| `snowflake-audit` | Historial de acceso nativo de Snowflake |
| `databricks-uc` | Auditoría de Databricks Unity Catalog |
| `bigquery-audit` | Auditoría de acceso a datos de BigQuery |
| `redshift-audit` | Auditoría de Amazon Redshift |
| `mssql-audit` | Auditoría de SQL Server |
| `oracle-audit` | Auditoría unificada de Oracle |
| `gcs-audit` | Auditoría de acceso a datos de Google Cloud Storage |
| `azure-blob-audit` | Auditoría de Azure Blob Storage |

## Plano de gestión cloud — inventario org/tenant + actividad del control plane

La paridad tri-cloud para el plano de **gestión** — distinto del plano de **datos** por recurso
que cubren los conectores de auditoría de almacén de arriba. Cada uno es un cliente de API en vivo,
**de solo lectura**, del control plane org/tenant de una cloud: descubre la **topología** de recursos
(aristas de inventario, `mode=unknown`, attributed) y lee el **feed de auditoría** nativo de la cloud
para la **actividad** del control plane (aristas `identity→…api`, clasificadas lectura/escritura). Completan
la matriz que AWS ya ancla con `s3cloudtrail` (plano de datos) más el
conector `aws` de IAM/CloudTrail a nivel de cuenta. Ambos corren **en proceso** y son
**offline-safe** (sin credencial ⇒ Gather es un no-op); ambos observan solo el control plane —
nunca un payload, secreto, clave o propiedad de recurso.

| Kind | Observa | Cobertura honesta |
|---|---|---|
| `gcp-audit` | GCP **Resource Manager / IAM** (topología org→folder→project→service-account) + **Cloud Audit Logs** (Admin Activity + Data Access) → `identity→gcp.api` | **Limpio** donde se registra: Admin Activity es una escritura por la definición del tipo de log, Data Access es lectura/escritura del verbo de método estándar. **Con pérdidas** donde el logging de Data Access está deshabilitado (off por defecto en GCP) o un verbo de método no es estándar (`unknown`, nunca adivinado). `approximate` para principals compartidos declarados; el `principalEmail` converge con el roster SPIFFE/SA |
| `azure-activity` | Azure **Resource Graph** (topología tenant→subscription→resource) + **Azure Monitor Activity Log** (operaciones del control plane) → `identity→azure.api` | **Limpio** para escrituras/eliminaciones del control plane (literal de la acción RBAC). El sufijo `action` genérico es **con pérdidas** (`unknown` — puede leer o escribir). Las **lecturas** del plano de datos **no están** en el Activity Log (el plano de datos `azure-blob-audit` / `azurekeyvault` cubre esas). `approximate` para llamantes compartidos; el `objectId`/`appId` del llamante converge con el roster de Entra |
| `cloudflare` | Estate de edge de Cloudflare — **Workers, buckets R2 y jobs de Logpush** mediante la REST API v4 → aristas de topología | Solo inventario (este conector no incluye un feed de auditoría); token acotado de solo lectura. Distinto de las superficies de IA `cloudflare-ai-gateway` / portales MCP |

El opt-in de **Data Access** de GCP y los huecos de **lectura-no-registrada** de Azure son las aristas
**opacas** honestas de este plano: una arista de actividad ausente no es prueba de no-acceso donde
esos logs están apagados. La tabla completa de niveles por cloud está en el contrato
incluido de conectores de gestión cloud (`docs/contracts/S165-connectors-cloud-management.md`).

## Proveedores de modelos alojados — catálogo, postura y medición

Estas fuentes gobiernan cuentas y catálogos de proveedores de modelos alojados. **No**
actúan como proxy de la inferencia; cuando un proveedor carece de una API de uso utilizable,
el conector estima el gasto mediante su Meter alrededor de la vía de inferencia, en lugar
de extraerlo de un feed de facturación agregado.

| Kind | Observa | Cobertura honesta |
|---|---|---|
| `openai` | Uso y coste de la plataforma OpenAI (API de org), además del catálogo de modelos y claves API | Clave de org/admin de solo lectura; sin payloads del data plane. Distinto de `azure-openai`, que usa las superficies reales de Azure, no las rutas de la org de OpenAI |
| `gemini` | Catálogo de modelos alojados de Gemini (Google) y una exportación de uso cableada por el operador | La superficie de proveedor alojado. Distinta de `gemini-cli`, que observa ajustes locales de la CLI, y de `vertex`, que cubre las superficies enterprise de Vertex. Google no expone una API de uso agregado en esta vía, por lo que el uso es el que cablea el operador |
| `deepseek` | Catálogo alojado de DeepSeek, disponibilidad del saldo de la cuenta y postura de soberanía PRC | Sin API de uso agregado; el coste se mide alrededor de la inferencia a partir del precio declarado |
| `mistral` | Catálogo de Mistral y postura de gobierno | Sin API pública de uso/facturación/límite de gasto; el coste se mide alrededor de la inferencia a partir del precio de lista |
| `xai` | Catálogo en vivo de xAI/Grok, endpoints de facturación, inventario de claves/ACL y postura de crédito y límite de gasto | Usa los endpoints de gestión de facturación de solo lectura para el coste; las credenciales de gestión e inferencia son distintas |
| `glm` | Catálogo declarado de Zhipu GLM / Z.ai, Meter de precio de lista en USD, prueba de entitlement y postura de soberanía | Solo catálogo + Meter: GLM no expone ninguna API verificada de uso, facturación, saldo, admin, claves u organización. La salvedad de nexo con la PRC / Entity List se aplica tanto a las superficies `z.ai` como `bigmodel.cn` |
| `vertex` | Catálogo de Google Vertex AI, uso de tokens por modelo (Cloud Monitoring), coste facturado opt-in (exportación de facturación) y postura de seguridad Model Armor opt-in | La superficie enterprise de Google que no cubre la vía AI Studio; GCP no tiene API de coste en tiempo real |
| `azure-openai` | Deployments + modelos de Azure OpenAI / AI Foundry (ARM), uso de tokens de Azure Monitor y superficies de coste | Cliente del plano de gestión de solo lectura; sin payloads del data plane |
| `openrouter` | Catálogo en vivo de OpenRouter (precios USD/MTok), postura de uso/límite de cuenta y drift de política de modelos aprobados | Coste facturado mediante el `MeterCall` exportado; no-op offline |
| `cohere` | Catálogo en vivo de modelos de Cohere (Models API paginada por cursor) | Sin API pública de uso/facturación/org (solo dashboard): salvedad de cobertura honesta; coste medido alrededor de la inferencia a partir del precio de lista |
| `fal` | Inventario del ciclo de vida de claves API de fal.ai + postura de rotación; coste medido alrededor de la API de colas | Sin API pública de uso/auditoría: el gobierno se basa en el ciclo de vida de claves; las superficies profundas están sujetas a ventas y marcadas como UNVERIFIED |

## Inferencia autoalojada — catálogos y uso locales

La inferencia autoalojada siempre está dentro del alcance, por lo que es una fuente de
primera clase y no una idea secundaria del gateway. Este nivel observa lo que sirve
realmente un runtime local.

| Kind | Observa | Cobertura honesta |
|---|---|---|
| `local` | Catálogo de modelos de Ollama (`/api/tags`), **residencia de Ollama (`/api/ps`)** — qué modelos están cargados ahora mismo, su reparto GPU/CPU y el plazo de descarga — y uso de tokens de vLLM mediante su superficie compatible con OpenAI | La residencia se informa como postura, y su gravedad es la COLOCACIÓN: un modelo enteramente en VRAM es informativo, mientras que uno residente en CPU o REPARTIDO entre CPU y GPU se marca, porque en ese caso el operador paga latencia sin que se le diga. Ollama no publica métricas de tokens agregadas, así que no aporta medición. Esta fuente todavía no proporciona identidad ni política por llamada sobre la inferencia local; gobernarlas requiere la vía del gateway u OTel. Ollama en localhost no necesita credencial, por lo que una config vacía es un valor por defecto funcional de solo lectura; deshabilitar un servidor requiere una URL vacía EXPLÍCITA, y ambos vacíos son un no-op |

## Respaldo de kernel — eBPF / Tetragon (señal limpia, atribución aproximada)

La mitad **no cooperativa** del moat: donde la ruta cooperativa ve lo que un agente
*reporta*, esto ve lo que el kernel *hizo* — lecturas/escrituras de fichero y conexiones salientes —
incluso cuando un agente deshabilita su propia telemetría. El **acceso** es ground-truth del kernel (una
señal de nivel limpio de *lo que ocurrió*); la **atribución** es deliberadamente honesta sobre
su límite — el kernel atribuye a una identidad de runtime (proceso/cgroup/contenedor), nunca
a un agente resuelto, así que toda arista eBPF es `approximate`. Nunca descifra ni inspecciona
payloads (es ciego al cuerpo TLS).

| Kind | Observa | Límite honesto |
|---|---|---|
| `ebpf` | Eventos de kernel de Tetragon → R/RW de fichero (máscara `MAY_*`) y aristas de red; hallazgo opcional anti-evasión cuando un agente actúa en el kernel sin telemetría cooperativa | Anónimo respecto al agente → siempre `approximate`; un respaldo en streaming, no un ledger por agente |

**No** carga programas eBPF por sí mismo: la captura del kernel la hace
[Tetragon](https://tetragon.io/) (un DaemonSet aparte, endurecido). Ver
[Requisitos de despliegue](#requisitos-de-despliegue-y-atribución-honesta).

## Con pérdidas — las aristas aterrizan, a menudo aproximadas

| Kind | Observa | Por qué con pérdidas |
|---|---|---|
| `mongo-audit` | Auditoría de MongoDB | Document-store; la separación de llamantes es débil |
| `openlineage` | Eventos de run de OpenLineage → linaje de datasets | El linaje no es auditoría por llamada |
| `delta-sharing` | Actividad de recipients de Delta Sharing | Atribución a recipient compartido |

## Fuentes aproximadas-por-atribución y del lado permitido

Estas emiten o bien el lado **permitido** (grants declarados) o accesos atribuidos a un
rol / proceso / credencial compartida en lugar de a un agente resuelto.

| Kind | Observa | Nivel |
|---|---|---|
| `iceberg-catalog` | Catálogo REST de Iceberg → grants permitidos + identidades de credencial servida | permitido |
| `inference-gateway` | Enrutamiento de K8s Gateway API Inference-Extension → rutas de inferencia permitidas | permitido |
| `aws-kms` / `gcp-kms` / `azure-key-vault` | Auditoría de KMS cloud → aristas de acceso a claves (nunca material de clave) | approximate |
| `external-secrets` / `sops` / `kmip` | Manifiestos de gestión de secretos / locate KMIP → aristas de aprovisionamiento/custodia | approximate (existencia, no uso) |
| `istio-telemetry` | CRDs de Istio Telemetry → aristas de mesh L7 | approximate (CRDs parseados, no flujos en vivo) |
| `egress-proxy` | Log de veredicto de egress-proxy → aristas de egress L7 | approximate |
| `kong-audit` | Logs de auditoría de Kong → hallazgos de cambio de config | approximate |
| `ai-gateway` | Registros de uso de Envoy AI Gateway → muestras de **coste** (FinOps) | flujo de coste |
| `github` | Repositorios GitHub como fuentes de datos de agentes → aristas de acceso R/RW observadas (webhook primero, reconciliación por polling de API) + aristas ACL permitidas | observado + permitido; streaming (`poll_seconds: 0`) |
| `gitlab` | Repositorios GitLab → aristas de acceso R/RW observadas + aristas ACL permitidas | observado + permitido; streaming (`poll_seconds: 0`) |

## Observadores de postura — hallazgos, no aristas de acceso

Observadores de lectura primero que exponen postura (sync/salud/drift, anomalías de auth) como
hallazgos; nunca mutan el estate.

| Kind | Observa |
|---|---|
| `runtime` | Dónde corren las cargas de trabajo de IA (procfs de Linux, demonio Docker, API de Kubernetes) → aristas de contención + hallazgos de salud (necesita acceso al host — ver [Requisitos de despliegue](#requisitos-de-despliegue-y-atribución-honesta)) |
| `argocd` / `flux` / `crossplane` | CRDs de GitOps / control plane → postura de sync, salud, drift, composición |
| `kerberos` | Telemetría de auth del KDC → hallazgos de Kerberoasting |
| `aaa` | Observaciones AAA de RADIUS / TACACS+ |
| `ssf` | Receptor de Shared-Signals / CAEP (kill-switch de agentes) |
| `edugain` / `openidfed` | Agregado de federación / cadenas de confianza OpenID-Federation → postura de federación |
| `managed-settings` | Política `managed-settings` de Claude → aristas permitidas + hallazgos de drift |
| `envoy-ai-gateway` | Exportación de **configuración declarada** de Envoy AI Gateway → postura del gateway + drift de política gateway-frente-a-Olivares (el hermano de configuración del flujo de uso `ai-gateway`) |
| `kong-agent-gateway` | Exportación de configuración declarada de Kong agent-gateway → postura + drift de política |
| `litellm` | Exportación de configuración declarada del proxy LiteLLM → postura + drift de política |
| `bedrock-kb` | Salud/configuración de recuperación de Amazon Bedrock Knowledge Bases (health check Retrieve de Agent Runtime) → hallazgos de postura por KB + aristas KB→fuente de datos. Nunca `RetrieveAndGenerate` (sin inferencia facturable), nunca contenido completo del documento |
| `tak` | Postura de `CoreConfig.xml` de TAK Server (+ prueba mTLS opcional) e ingest gobernado y de datos mínimos de Cursor-on-Target (posiciones resumidas, uid hasheado) |
| `a2a` | Peers de Agent2Agent (A2A) v1.0 → descubrimiento de Agent Card + verificación de firma JWS/JCS (nivel de confianza del peer) e interacciones observadas de tarea/mensaje como aristas agente↔agente. Solo observación: nunca despacha una tarea; emitir cards firmadas es una capacidad separada |

## Indicio no fiable — introspección MCP

La fuente `mcp` introspecciona servidores MCP (stdio + Streamable HTTP) y emite **aristas de
capacidad** que llevan los indicios R/RW *declarados* del servidor, más hallazgos de protocol-revision,
feature-surface y registry-provenance. Según la especificación MCP, una anotación de herramienta es una
declaración **no fiable** — una *afirmación* de capacidad, corroborada contra una fuente observada,
**nunca fiada por sí sola**. (La fuente cooperativa `claude` también introspecciona MCP como parte de
su ruta OTLP; `mcp` es el introspector independiente que apuntas a una lista de servidores o a un
`.mcp.json`.)

| Kind | Observa | Nivel |
|---|---|---|
| `mcp` | tools/resources/prompts de servidor MCP → aristas de capacidad-declarada + hallazgos de postura | indicio no fiable |

## Observadores de broker y mesh fuera de proceso

Estos llevan árboles de dependencias de protocolo de wire pesados, así que cada uno corre **fuera de proceso** (la
dependencia nunca se enlaza con el core). Un conector alcanza muchos targets.

| Kind | Observa |
|---|---|
| `kafka` | Actividad de topics de Kafka / Event Hubs / Redpanda / MSK |
| `amqp` | Brokers AMQP (RabbitMQ, Azure Service Bus) |
| `nats` / `mqtt` / `cloudqueue` | Actividad de NATS, MQTT, cloud queue |
| `debezium` | Flujos de change-data-capture de Debezium |
| `envoy` | Servicios de observación Envoy ALS / ext_authz / ext_proc |
| `hubble` | Datos de flujo de Cilium Hubble |

## Proveedores de roster de identidad

Estos pueblan el **roster** de identidad no humana que afina la atribución (convirtiendo
aristas `approximate` en `attributed`). Cada fuente con una superficie de grants también
emite sus aristas de **acceso-permitido** (`SignalPolicy`) desde `Gather` — el lado PERMITIDO
del diff permitido-frente-a-observado:

| Kind | Roster | Aristas permitidas |
|---|---|---|
| `vault` | entidades, grupos, políticas | grants de path de política ACL (`vault.path`), expandidos por entidad enlazada |
| `ldap` | usuarios, cuentas de servicio/computer, grupos | membresía de grupo privilegiado → grants de directorio (`ldap.directory`) |
| `idp` (Okta / Entra) | usuarios, apps/service principals, grupos | grants de app-assignment / scope (`okta.app` / `entra.app`) |
| `infisical` | identidades de máquina, miembros de org, proyectos | grants de proyecto (`infisical.project`) |
| `keycloak` | realms, clients, roles, grupos, usuarios | solo roster (`Gather` no-op) |
| `pingone` / `forgerock` | Rosters de directorio de PingOne / ForgeRock mediante el mismo lector multiproveedor (el kind establece el `provider` correspondiente; `ping` es alias de `pingone`) | solo roster (`Gather` no-op) |
| `spiffe` | entradas de registro de SPIRE | solo roster (`Gather` no-op) |

Cablea `as_source: true` en la entrada `identity` para una pasada de grants permitidos de una sola vez
por arranque, o una entrada `sources` separada con `poll_seconds` para re-escaneos periódicos —
nunca ambas para un mismo kind (`okta`/`entra` comparten el único conector `idp`, así que solo una
instancia de la familia idp puede registrarse como fuente por proceso). Las membresías de grupo/rol
viajan solo en el snapshot tipado del roster, nunca como aristas.

### Federación de identidad de agentes

Los **registros de agentes** de los hyperscalers federan de solo lectura contra el roster SPIFFE/WIF
del plano. Sus filas por agente (kinds `agent_identity` / `workload_identity`) son
identidades dedicadas, no compartidas, así que el access map las trata como atribución **firme**
por agente; las filas auxiliares de las mismas fuentes (blueprint principals, proveedores de
credenciales, agentes respaldados por service-account) se quedan aproximadas. La federación nunca escribe a
un registro; *exportar* a las torres de control es una capacidad separada y posterior.

| Kind | Federa | Gather |
|---|---|---|
| `entra-agent` | Microsoft Entra Agent ID (identidades de agente, usuarios de agente, blueprints, blueprint principals, owners/sponsors, cómputo de huérfanos en snapshot, soft-deleted opt-in) vía Graph v1.0 | hallazgos de drift `nhi_longlived_credential`, hallazgos de postura CA/agente de riesgo/gobierno/sin sponsor y aristas observadas de acceso de agentes de `auditLogs/signIns` beta opt-in — añade una entrada `sources` con `poll_seconds` |
| `agentcore` | AWS Bedrock AgentCore Identity (workload identities, proveedores de credenciales de token-vault) + motores AgentCore Policy/políticas Cedar como colecciones | hallazgos de drift `nhi_longlived_credential` (proveedores de API-key estática) — añade una entrada `sources` con `poll_seconds` |
| `google-agent` | Google Agent Identity (reasoning engines de Agent Runtime; identidades de agente basadas en SPIFFE), además de postura de Agent Registry / Agent Gateway. Las filas usan el **SPIFFE ID completo** como ref, convergiendo con el roster `spiffe`; Gather detecta agentes de registro no atribuidos, reasoning engines en sombra fuera de un registro legible, anotaciones de herramientas MCP de riesgo y postura del registro del gateway | hallazgos de postura de registro/gateway y detección de agentes en sombra — añade una entrada `sources` con `poll_seconds` |
| `agent365` | Registro de Microsoft Agent 365 (inventario a nivel de paquete, incluidos agentes *sin* una identidad Entra) mediante Graph v1.0, credenciales de cliente con permisos de aplicación o token delegado, detalles de paquete opt-in | hallazgos de higiene del registro (paquetes desplegados bloqueados; paquetes externos/compartidos desplegados para todos los usuarios) — añade una entrada `sources` con `poll_seconds` |
| `foundry-agents` | Proyectos Microsoft Foundry, aplicaciones/deployments de agentes y agentes actuales de Agent Service mediante ARM + Foundry Agent Service v1; correlaciona los enlaces de identidad de app con `entra-agent` | hallazgos de postura de aplicación derivados de ARM (falta identidad de agente Entra; deployment fallido en una app habilitada) — añade una entrada `sources` con `poll_seconds` |
| `ai-control-tower` | Inventario de digital-asset de ServiceNow AI Control Tower (Table API, solo lectura) | no-op (solo roster) |
| `oasf` | Descriptores de agente AGNTCY/OASF + verificación de Agent Badge — **EXPERIMENTAL** hasta que la especificación de identidad sea conforme a VCDM 2.0 | hallazgos de badge — añade una entrada `sources` con `poll_seconds` |
| `onepassword` | Cuenta 1Password como custodio `secret_store` | aristas de acceso a secretos por item-usage — añade una entrada `sources` con `poll_seconds` |

Para los siete kinds con un Gather re-poleable (`entra-agent`, `agent365`, `agentcore`,
`foundry-agents`, `google-agent`, `oasf`, `onepassword`), cablea la mitad del **roster** como una entrada `identity` *sin*
`as_source` y la mitad de **aristas/hallazgos** como una entrada `sources` separada con
`poll_seconds` — no ambas vía `as_source: true`, que corre el escaneo solo una vez por
arranque (y un registro duplicado del mismo kind es rechazado).

El **owner/sponsor** declarado por el registro aterriza en los registros de ciclo de vida NHI durante el sync
del roster (la misma semántica que `PUT /nhi/{ref}/ownership`), y un **huérfano** afirmado por el
registro (un agente Entra cuyo blueprint ha desaparecido) aterriza en el flag `registry_orphaned`
del mismo registro — el barrido de ciclo de vida lo combina con OR en `orphaned` y emite el
hallazgo `nhi_orphaned`, así que la detección de huérfanos vigila agentes federados con cero cableado
extra. La *fuente* `vault-audit` (bajo `sources`, no `identity`) sigue el dispositivo de auditoría
de fichero de Vault y emite la contraparte OBSERVADA de los grants permitidos de `vault` para las mismas
refs `entity:<name>`.

## Fuentes de documentos de conocimiento (no es cobertura de access-map)

Estas alimentan el módulo de **conocimiento** (módulo VIII), **no** el access map: ingestan
*contenido de documento* para recuperación gobernada, emiten **ninguna** arista R/RW y no producen **ninguna**
observación en el bus. El módulo los *extrae* (List → Fetch) ante una petición de ingest
(`POST /v1/m/knowledge/kbs/{id}/ingest {"source":"<name>"}`), así que están cableados en ese
módulo — nómbralos bajo `documents` en `OLIVARES_SOURCES_CONFIG`, no `sources`. Cada uno es
de solo lectura y datos mínimos: lleva la ACL y la procedencia de la fuente (nunca un email
personal; el módulo expurga el cuerpo antes de persistir).

| Kind | Ingesta |
|---|---|
| `gdrive` | Documentos de Google Drive (Docs/Sheets/Slides/ficheros) |
| `confluence` | Espacios y páginas de Atlassian Confluence |
| `notion` | Workspaces, bases de datos y páginas de Notion |
| `sharepoint` | Sitios y documentos de Microsoft SharePoint / OneDrive |
| `s3content` | Contenido de object-storage (objetos S3 / R2 / GCS) |
| `sap_odata` | Entidades de servicios SAP OData como documentos gobernados |
| `salesforce` | Objetos/registros de Salesforce como documentos gobernados |
| `snowflake` | Tablas/filas de Snowflake como documentos gobernados (distinto del observador R/RW `snowflake-audit`) |
| `azure_ai_search` | Documentos de índices de Azure AI Search |
| `postgres` | Filas de PostgreSQL como documentos gobernados — solo lectura por construcción, ACL declarada por fila, clasificación por columna (distinto del observador R/RW `pgaudit`; no es NL-to-SQL). Consulta [Postgres como fuente de contexto gobernada](/es/how-to/govern-postgres-content/). |
| `filesystem` | Contenido de servidor de ficheros (local / NFS / SMB) — lectura confinada a la raíz por construcción, owner/group/ACL POSIX mapeados a ACL de documentos, clasificación xattr (distinto del sink de logs `filelog`). Consulta [Gobierna tu servidor de ficheros](/es/how-to/govern-your-file-server/). |

```jsonc
// OLIVARES_SOURCES_CONFIG — las fuentes de documentos van bajo "documents", nunca "sources"
{
  "documents": [
    { "name": "eng-wiki", "kind": "confluence",
      "config": { "export_path": "/var/lib/olivares/confluence" } }
  ]
}
```

## Destinos de salida (no es cobertura)

Los conectores de salida **entregan** hallazgos y notificaciones; no observan nada y no tienen
nivel de cobertura. Se cablean por separado de las fuentes.

Kinds de destino en proceso: `slack`, `teams`, `pagerduty`, `opsgenie`, `webhook`,
`siem`, `splunkhec`, `syslog`, `servicenow`, `jira`, `email`, `twilio`, `chronicle`,
`datadog`, `elastic`, `snmp`, `filelog`, `otlplog` (logs OTLP/HTTP) y `s3archive`
(el sink WORM de S3 Object Lock: un objeto inmutable y con bloqueo verificado por
notificación).

Tres kinds de egreso a brokers se ejecutan **fuera de proceso** como plugins embebidos
(sus árboles de dependencias del protocolo de wire nunca se enlazan al motor, exactamente
igual que las fuentes plugin): `kafka`, `amqp` y `cloudqueue` — los mismos nombres de kind
que sus gemelos de fuente; como destino, cada uno entrega la notificación como CloudEvent
al broker/cola configurado. Una build sencilla de desarrollo sin `task build:connectors`
omite ese destino con un aviso honesto en el arranque, en lugar de fingir que existe.

:::note[El webhook saliente es un destino, no un webhook de API]
`webhook` es un canal de salida al que el control plane empuja, no un callback que registres
contra la API REST del producto — el documento OpenAPI no define ningún `webhooks`. Ver
[Honestidad y límites](/es/start/honesty-and-limits/).
:::

## Requisitos de despliegue y atribución honesta

Los conectores de diferencial R/RW están cableados en el binario por defecto, pero dos conllevan un
**requisito de despliegue** que el resto no — el código del conector es agnóstico del host, los
*datos* que consume no:

- **`ebpf`** consume la exportación de eventos de kernel de [Tetragon](https://tetragon.io/). **El
  conector no necesita ninguna capability de kernel** — lee un fichero/FIFO/`stdin` `0600` que
  Tetragon posee (`events_path`, default `-`). Tetragon mismo es un **DaemonSet aparte, endurecido**
  que sostiene los mínimos `CAP_BPF` + `CAP_PERFMON`, corriendo sin root con
  seccomp/AppArmor y sin listener entrante. Así que el despliegue es: corre Tetragon privilegiado
  (sus TracingPolicies de acceso a fichero + conexión TCP empaquetadas), luego apunta `ebpf` a su exportación.
  Tetragon mínimo: v1.0.
- **`runtime`** lee el procfs del host (`proc_root`, default `/proc`), el socket del demonio Docker
  (`docker_socket`, **off por defecto** — el acceso de lectura a `docker.sock` es
  equivalente a root; haz opt-in deliberadamente, idealmente vía un proxy de socket con allowlist de GET) y/o
  la API de Kubernetes (ServiceAccount in-cluster por defecto). Monta solo lo que habilites.
- **`gcp-audit`** se autentica como una service account de GCP (key JSON o un
  `access_token` emitido por WIF/ADC) y necesita solo roles de **gestión de solo lectura**:
  `roles/resourcemanager.organizationViewer` + `roles/iam.serviceAccountViewer` +
  `roles/logging.viewer` — leer entradas de **Data Access** necesita adicionalmente
  `roles/logging.privateLogViewer`. Acota `organization_id` (org walk + auditoría con scope de org)
  y/o `projects`. Los logs de auditoría de Data Access están **off por defecto en GCP**: habilítalos según
  la config de IAM/data-access, o el feed de actividad sub-reporta honestamente.
- **`azure-activity`** se autentica como un service principal de Entra (client-credentials) o
  un `access_token` de managed-identity, y necesita solo el rol **Reader** en la raíz del tenant
  (o por subscription) — ese único rol cubre Resource Graph, el listado de subscriptions y
  el Activity Log. Las subscriptions se listan automáticamente cuando `subscriptions` no está fijado.

Ambos siguen corriendo **en proceso** (transporte A); los binarios go-plugin
`cmd/{pg-audit,s3-cloudtrail,ebpf-source}` existen para un despliegue de **collector** fuera de proceso
cerca del host si prefieres aislarlos ahí.

Toda fuente es **opt-in, deny-closed**: un `log_path`/`path`/`events_path` faltante es un
error de configuración en el arranque (la fuente no se cablea), nunca un no-op silencioso. El estate
de demo ([quickstart](/es/start/quickstart/)) siembra observaciones sintéticas equivalentes a través del
bus real para que puedas ver la señal de nivel limpio de extremo a extremo antes de cablear una fuente en vivo.

:::caution[Límites honestos en todos los niveles]
- **Una arista ausente no es prueba de no-acceso** donde la cobertura es con pérdidas, imposible, o una
  fuente no está cableada. El access map es honesto sobre su propio alcance.
- **La identidad por agente es la dependencia dura.** Una service account compartida tras un
  connection pool colapsa la atribución a `approximate` incluso en un almacén de nivel limpio —
  ver [gobernar y aprobar](/es/how-to/govern-and-approve/).
- **Las anotaciones de herramienta MCP son no fiables** por la especificación MCP: un indicio de capacidad
  declarado, corroborado contra una fuente observada, nunca fiado por sí solo.
:::

## Relacionado

- [Conectar una fuente](/es/how-to/connect-a-source/) — el modelo de conector y cómo cablear uno.
- [Conectar Claude Code](/es/how-to/connect-claude-code/) — la ruta cooperativa de extremo a extremo.
- [Módulo III — el access map](/es/reference/modules/iii-access-map/) — en qué se convierten las aristas.
- [Honestidad y límites](/es/start/honesty-and-limits/) — el contrato honesto de todo el producto.
