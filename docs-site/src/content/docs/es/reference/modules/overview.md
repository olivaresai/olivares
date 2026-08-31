---
title: Catálogo de módulos
description: >-
  Los 30 módulos de Olivares AI — organizados por las nueve áreas de capacidad,
  con la madurez honesta de cada módulo. Olivares AI integra, gestiona y asegura
  la IA en la empresa, un único ground truth: Claude Code al nivel más profundo, Codex y Grok Build a su lado; esta es la
  referencia por módulo.
---

Olivares AI integra, gestiona y asegura la IA en la empresa, un único ground truth:
Claude Code al nivel más profundo, Codex y Grok Build a su lado. Es una **plataforma modular** — un motor, una
consola y **30 módulos** integrados en un único binario — que observa dónde se
ejecutan los agentes, gobierna lo que se les permite hacer y (sobre un subconjunto
creciente) actúa sobre tu infraestructura real. Cada módulo (a) consume
eventos/datos normalizados del núcleo, (b) declara sus entidades en el modelo de
datos compartido y (c) expone sus propios endpoints de API y vistas de UI — sin
tocar el núcleo ni otros módulos.

Los 30 módulos se organizan por las **nueve áreas de capacidad** de abajo. Lee el
estado de cada módulo como **dos mitades**: *Gobernar/Observar* (catalogar,
observar, restringir, informar) está construido y cableado hoy; *Actuar* (actuar
sobre infraestructura real — desplegar, despachar, enviar, aplicar, ejecutar) cae
en estados honestos — **live** en el binario por defecto para un subconjunto,
**on-demand** para varios (el backend está construido y cableado a un punto de
inyección pero permanece deny-closed o degradado hasta que un operador lo
aprovisiona vía configuración de entorno), **PARTIAL** donde la superficie está
restringida/opt-in, y un **seam deny-closed** declarado para el resto. En
particular, **deploy** planifica y gobierna despliegues pero **no** los aplica a
la infraestructura en vivo hasta que se aprovisiona un executor: `apply`/`retire`
devuelven un `503` claro. La profundidad varía según el módulo, y buena parte del
producto está pre-1.0 / en fase de diseño donde así se indica (consulta
[Honestidad y límites](/es/start/honesty-and-limits/)).

El **access map** (`iii-access-map`) — el grafo de lectura/lectura-escritura de lo
que cada agente puede tocar y de hecho toca, con la desviación de mínimo
privilegio = `Permitted ≠ Observed` — es **una de las capacidades más útiles entre
las 30**, no el producto entero. La amplitud es lo que importa: nueve áreas, un
motor, una consola.

## Los 30 módulos, por área de capacidad

Cada fila enlaza a su página de módulo (`/reference/modules/<slug>/`). La columna
**Actuar** es el estado honesto de la mitad que actúa; `—` significa que el módulo
gobierna/observa por naturaleza y no tiene superficie de actuación.

### Observar

| Módulo | Actuar | Propósito |
|---|---|---|
| [Inventario y descubrimiento](/es/reference/modules/i-inventory/) | — | Descubre y cataloga cada agente/sesión/servidor MCP/herramienta/modelo/identidad del estate. |
| [Operación en vivo y sesiones](/es/reference/modules/ii-sessions/) | — | Estado en tiempo real de cada agente y sesión; también aloja el runtime gobernado de sesiones de Claude Code. |
| [Mapa de acceso y recursos (R/RW)](/es/reference/modules/iii-access-map/) | — | A qué accede cada agente, y si lee o escribe; desviación de mínimo privilegio = `Permitted ≠ Observed`. |
| [Orquestación y A2A](/es/reference/modules/iv-orchestration/) | on-demand | Observa-y-gobierna el grafo en vivo de delegación/comunicación; el despacho está cableado on-demand, deny-closed hasta aprovisionarse. |
| [MCP, skills y capacidades](/es/reference/modules/v-capabilities/) | — | Gobierna las herramientas y capacidades de los agentes, visualmente. |
| [Salud, SLA y uptime](/es/reference/modules/xxii-health/) | — | Fiabilidad de los agentes y servidores MCP del estate; comprobaciones, incidentes, mapa de dependencias. |
| [Modelo de lectura de observabilidad](/es/reference/modules/observability/) | — | El modelo de lectura que el motor tiene de sí mismo: estándares de interoperabilidad fijados, vista de ledger/traza correlacionada con W3C, atestación de la cadena de suministro. |
| [Adopción de Claude Code](/es/reference/modules/claudeadoption/) | — | Modelo de lectura de la adopción/productividad de Claude Code: sesiones, líneas de código, commits, PRs, accept-reject de herramientas, tokens por modelo, por equipo/desarrollador/día; por equipo por defecto, drill-down por desarrollador opt-in. Frontera solo-API-de-Claude; nunca lleva coste. |
| [Live-ingest](/es/reference/modules/live-ingest/) | PARTIAL | Productor in-process de eventos detective que un conector no puede emitir; restringido por entorno, deny-closed, datos mínimos. |

### Gobernar y aplicar

| Módulo | Actuar | Propósito |
|---|---|---|
| [Identidad, permisos y gobernanza](/es/reference/modules/vi-governance/) | — | Quién y qué puede hacer qué, granular: Cedar RBAC + deny-overlay + grants acotados, reconciliación del roster, admin acotado/roles personalizados, break-glass, kill-switch. |
| [Acotamiento de fuentes y credenciales](/es/reference/modules/sourcescope/) | — | Vincula fuentes a un workspace/grupo-de-agentes; resolver acotado deny-closed + credenciales acotadas en el momento de la resolución. |
| [Despliegue e integración](/es/reference/modules/vii-deploy/) | on-demand (503) | Planifica y gobierna despliegues a infraestructura real; el executor es on-demand — `apply`/`retire` en vivo devuelven `503` hasta aprovisionarse. |

> **Identidad y acceso** vive dentro de [gobernanza](/es/reference/modules/vi-governance/) —
> no hay un módulo aparte. El ciclo de vida de NHI, la federación de identidad de
> agentes, el step-up AAL3 y SSO/SCIM son capacidades de gobernanza.

### Ecosistema de Claude y agentes

| Módulo | Actuar | Propósito |
|---|---|---|
| [Gestión de modelos y proveedores](/es/reference/modules/x-models/) | on-demand (503) | Gobierna toda la pila de modelos/proveedores: acceso a modelos, ventana de contexto por superficie, gate de grupo de modelos; la *ejecución* de modelos es on-demand — `503` hasta aprovisionar una credencial de inferencia. |
| [Proxy de inferencia inline](/es/reference/modules/inferenceproxy/) | PARTIAL | Configuración de egress de inferencia por tenant + DLP para el proxy PEP inline `/v1/messages`; la configuración del módulo está live, el listener es opt-in, loopback por defecto, fail-CLOSED. |
| [Catálogo y marketplace internos](/es/reference/modules/xiv-catalog/) | — | Marketplace curado de agentes, servidores MCP y skills aprobados/firmados. |
| [Agentes de voz y tiempo real](/es/reference/modules/xvi-voice/) | on-demand | Observa-y-gobierna agentes conversacionales/de tiempo real (default-DENY, HITL en dos fases); nunca abre un flujo de medios; despacho on-demand. |

### Seguridad y protección de datos

| Módulo | Actuar | Propósito |
|---|---|---|
| [Seguridad, guardrails y auditoría](/es/reference/modules/ix-security/) | live | Guardrails (PII/inyección/jailbreak), anomalías, cronologías de incidentes; BYOK/DLP/RTBF/retención/WORM/residencia viven en este plano. |
| [Grabación de sesiones privilegiadas](/es/reference/modules/recording/) | live | Grabación alineada con PAM de sesiones privilegiadas: frames hash-chained, expurgo en escritura, anclados al ledger. |
| [Datos, conocimiento y contexto](/es/reference/modules/viii-knowledge/) | on-demand | Plano de datos gobernado: KBs + RAG, recuperación gobernada, linaje, registro de prompts, memoria de agentes; los embeddings semánticos respaldados por modelo son on-demand. |

### Cumplimiento y evidencia

| Módulo | Actuar | Propósito |
|---|---|---|
| [Cumplimiento y regulatorio](/es/reference/modules/xiii-compliance/) | — | 26 catálogos de marcos + evidencia sellada y derivada del ledger con verificación de cadena en vivo. |
| [Forwarder SIEM/ITSM](/es/reference/modules/siemforward/) | live | Envía el ledger sellado + los findings a torres SIEM (OCSF 1.8/CEF/LEEF/syslog/OTLP), recorrido de cursor con gate de líder, at-least-once. |
| [Exportación de postura](/es/reference/modules/posture-export/) | PARTIAL | Pull de postura/inventario de solo lectura para torres de control (JSON neutral); **no** afirma un push downstream verificado. |
| [Reporting](/es/reference/modules/reporting/) | — | Generación profesional de informes PDF/HTML a partir de los datos de compliance, auditoría y FinOps de la plataforma — cinco tipos integrados; un auditor descarga un documento en vez de copiar y pegar JSON. |

### FinOps

| Módulo | Actuar | Propósito |
|---|---|---|
| [Coste y FinOps de IA](/es/reference/modules/xi-finops/) | live | Presupuestos que actúan denegando/limitando en el tope, coste-por-resultado, riesgo de cancelación; presupuesto firme a la identidad. |

### Evals y seguridad

| Módulo | Actuar | Propósito |
|---|---|---|
| [Calidad, evals y testing](/es/reference/modules/xii-evals/) | — | LLM-judge calibrado + un gate de regresión bloqueante en CI; juez offline → SKIPPED, nunca un pase silencioso. |
| [Sandbox de agentes](/es/reference/modules/xvii-sandbox/) | on-demand | Entorno seguro para probar agentes antes de producción; el aislamiento de SO real (gVisor/Firecracker) es on-demand. |
| [Red-teaming y pruebas adversariales](/es/reference/modules/xviii-redteam/) | on-demand | Batería adversarial restringida por consentimiento; DEGRADED — nunca un falso pase — hasta aprovisionar un runtime de sandbox. |

### Plataforma e integraciones

| Módulo | Actuar | Propósito |
|---|---|---|
| [Integraciones de salida y notificaciones](/es/reference/modules/xv-notify/) | live | Router de notificaciones a los sistemas que la empresa ya opera; el despacho está cableado live, los destinos aprovisionados por el operador. |
| [Eventing](/es/reference/modules/eventing/) | live | Superficie de suscripción externa sobre el bus: suscripciones tipadas, entrega durable at-least-once, retry/backoff, DLQ, replay de cursor. |
| [Vistas guardadas de la consola](/es/reference/modules/consoleviews/) | — | Instantáneas con nombre y compartibles del estado de las vistas de la consola (filtros, rangos), almacenadas server-side por tenant: guarda una investigación, compártela con el equipo. Acepta un objeto JSON con tope de 4096 bytes pensado para parámetros de vista — no guardes en él datos sensibles ni resultados de consultas. Crear/actualizar son solo del propietario; los administradores/propietarios del tenant y los superadmins pueden borrar para limpieza; toda mutación queda auditada. |

Columna **Actuar**: `live` = la actuación está cableada y live en el binario por
defecto, sin aprovisionamiento requerido (p. ej. la aplicación de presupuesto de
FinOps deniega en el tope, el router de notificaciones despacha); `on-demand` /
`on-demand (503)` = el backend está construido y cableado a un punto de inyección
pero permanece **deny-closed o degradado hasta que un operador lo aprovisiona** vía
configuración de entorno (deploy responde `503` hasta que existe un executor; el
despacho de orquestación/voz es deny-closed hasta configurarse; red-team corre
DEGRADED hasta aprovisionar un runtime de sandbox; la ejecución de modelos y los
embeddings semánticos devuelven `503` hasta aprovisionar una credencial);
`PARTIAL` = la superficie es real pero está restringida/opt-in o no afirma un
downstream verificado (el listener del inference-proxy es opt-in y loopback por
defecto; live-ingest está restringido por entorno; posture-export es una proyección
neutral de solo lectura); `—` = el módulo gobierna/observa por naturaleza y no
tiene superficie de actuación. Esta división es el contrato honesto: el producto
**observa y gobierna ampliamente hoy, y actúa sobre un subconjunto creciente, en
su mayoría restringido por aprovisionamiento** — consulta
[Honestidad y límites](/es/start/honesty-and-limits/). El catálogo se deriva de la
raíz de composición (`cmd/olivares/wire.go`): los 30 módulos se construyen ahí y
se registran vía `rt.AddModule` (verificado el 2026-08-01, main @ f632f03f).

## Capacidades de plataforma y núcleo (no contadas entre los 30 módulos)

Estas son capacidades reales, entregadas, pero son **capacidades de
motor/núcleo/web**, no módulos del conjunto `modules/` — así que no se cuentan
entre los 30:

- [API propia + manage-as-code](/es/reference/modules/xix-api-manage-as-code/) —
  **Capacidad de motor/núcleo.** La propia API REST/gRPC versionada del motor más
  el provider de Terraform; gestiona la plataforma misma por API e IaC.
- [Multi-tenancy y gestión de org](/es/reference/modules/xx-multi-tenancy/) —
  **Capacidad de motor/núcleo.** Jerarquía de org y admin delegado, con
  aislamiento de tenant por row-level-security de Postgres.
- [Dashboards ejecutivos](/es/reference/modules/xxi-executive-dashboards/) —
  **Capacidad web.** Vistas de consola para dirección junto a la UI técnica. (Su
  backend de generación de informes es el módulo
  [reporting](/es/reference/modules/reporting/), que SÍ se cuenta entre los 30.)
- [Operaciones de modelos (modelos propios)](/es/reference/modules/xxiii-model-operations/) —
  **Capacidad del módulo de modelos** (se cuenta a través de la fila del módulo X,
  no como fila aparte): el registro gobernado de modelos propios, la admisión de
  modelos firmados, los registros de linaje de datasets/trabajos de fine-tuning,
  la gobernanza de despliegues de inferencia local y la evidencia AIBOM/model card.

**Planificado:** la **ejecución** de fine-tuning de modelos propios y de
inferencia local ([xxiii-fine-tuning](/es/reference/modules/xxiii-fine-tuning/)) —
la plataforma gobierna y registra ese trabajo hoy (ver operaciones de modelos
arriba) pero no ejecuta entrenamiento ni sirve inferencia por sí misma; la mitad
ejecutora es trabajo **planificado** documentado, **no entregado** y no uno de
los 30.

## Cómo aparecen los módulos en la API y en el bus

- **REST.** La [referencia de API](/reference/api/) renderiza la superficie REST
  del núcleo a partir del contrato OpenAPI 3.1 del producto. Algunas rutas de
  módulo son alcanzables pero **deliberadamente no** están en ese documento; sus
  contratos a nivel de campo viven en las interfaces tipadas del producto.
- **Eventos.** Los módulos reaccionan al [bus de eventos](/es/reference/events/): el
  access map consume `edge.observed`, FinOps consume `cost.sampled`, y seguridad
  consume `finding.reported` y `guardrail.observed`.

## Capas

Los 30 módulos se construyen sobre capas por encima del motor, junto a las
capacidades de motor/núcleo y web de arriba:

- **Motor (capa 0)** — las capacidades de API-propia/manage-as-code y
  multi-tenancy (núcleo, no contadas en los 30).
- **Núcleo (capa 1)** — inventory, sessions, access-map, models, health,
  observability.
- **Gestión (capa 2)** — capabilities, governance, sourcescope, deploy, knowledge.
- **Inteligencia (capa 3)** — orchestration, security, recording, inference proxy,
  finops, evals, compliance, reporting, siemforward, posture-export, catalog, notify,
  eventing, voice, sandbox, redteam, live-ingest, consoleviews.
- **Web (capa 4)** — la UI y la capacidad de dashboards ejecutivos.

Consulta la [vista general de arquitectura](/es/explanation/architecture/overview/)
para ver cómo se componen el motor y estas capas.
