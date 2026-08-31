---
title: Referencia de la consola — cada pantalla y el permiso que necesita
description: >-
  Todas las rutas que publica la consola de Olivares AI, agrupadas en sus cinco hubs,
  con el permiso RBAC que requiere cada una y la página de referencia que abre su
  enlace de ayuda dentro del producto. Generado a partir del censo de rutas de la consola.
---

Esta página es el mapa de la consola. Enumera **todas las rutas que monta la aplicación**,
no una selección ni las que alguien recordó documentar, junto al permiso que necesita un
principal para entrar y el lugar donde puede leer más.

Es una página **generada**. El inventario procede de `web/src/features/route-census.json`,
el censo append-only que `registry.route-conservation.test.ts` coteja con el router compilado,
por lo que ninguna pantalla puede añadirse, moverse ni perderse sin que esta página cambie con
ella. El nombre y la descripción de una línea de cada pantalla son **las propias cadenas de la
consola**, tomadas del mismo catálogo de traducción que renderiza la barra lateral: lo que lees
aquí es lo que ves en el producto.

:::note[Los permisos los aplica el motor, no esta tabla]
La columna `Requiere` muestra el permiso que comprueba la consola antes de ofrecer la ruta y
refleja el RBAC del motor. El motor sigue siendo la autoridad: un enlace directo a una pantalla
para la que no tienes permiso es rechazado por la API, no solo ocultado en la barra lateral.
Consulta [Roles y permisos](/es/reference/modules/vi-governance/).
:::

## Cómo leer esta página

- **Pantalla**: el nombre que usan la barra lateral y la paleta de comandos.
- **Ruta**: la URL bajo el origen de la consola de tu despliegue. Es un contrato publicado:
  un marcador, un enlace profundo de un runbook y una referencia cruzada de la documentación
  usan todos esta cadena.
- **Requiere**: el permiso RBAC. `cualquier usuario autenticado` significa que la ruta está
  abierta a todo principal autenticado; **sin inicio de sesión** significa que se sirve antes
  de que exista una sesión.
- **Referencia**: la página que abre el propio enlace de ayuda de la consola para esa pantalla.

Los cinco encabezados siguientes son los hubs de la consola, en el orden en que los muestra la
barra lateral.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

La consola publica **59 rutas**. Todas figuran en las tablas siguientes, con el permiso que
requieren y la página de referencia que abre su enlace de ayuda dentro del producto.

### Operar

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Resumen | `/` | Visión general del estado y la salud del estate | cualquier usuario autenticado | [inicio de la documentación](/es/) |
| Claude Code | `/agentops` | Crea, adjunta y gobierna sesiones de Claude Code — sin SSH | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/es/how-to/run-claude-code-with-olivares/) |
| Copias de seguridad | `/backups` | Inicia, programa, descarga y restaura copias de seguridad, con una segunda confirmación en la vía destructiva. | `system:admin` | [how-to/backup-and-restore](/es/how-to/backup-and-restore/) |
| Salud y SLA | `/health` | Disponibilidad y SLA de agentes y MCP | `health:status:read` | [reference/modules/xxii-health](/es/reference/modules/xxii-health/) |
| Kill switch | `/killswitch` | Parada de emergencia, recuperación con doble control y contención guardiana | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/es/how-to/cookbook/kill-switch-drill/) |
| Registros | `/logs` | Flujo en vivo del log del motor, filtrado por nivel y módulo, con búsqueda y pausa. | `system:admin` | [how-to/troubleshooting](/es/how-to/troubleshooting/) |
| Observabilidad | `/observability` | Salud de la ingesta por estándar y exploración de trazas | `health:status:read` | [reference/modules/observability](/es/reference/modules/observability/) |
| Sandbox | `/sandbox` | Pruebas aisladas de agentes y replay | `sandbox:run:read` | [reference/modules/xvii-sandbox](/es/reference/modules/xvii-sandbox/) |
| Sesiones | `/sessions` | Operación viva de agentes y cronologías | `sessions:live:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Tenants | `/tenants` | Retira o restaura el servicio de un tenant | `system:admin` | [how-to/troubleshooting](/es/how-to/troubleshooting/) |
| Voz | `/voice` | Sesiones de voz y tiempo real | `voice:session:read` | [reference/modules/xvi-voice](/es/reference/modules/xvi-voice/) |
| Trabajo | `/work` | El backlog duradero entre sesiones: elementos, dependencias, aceptación y decisiones | `sessions:work:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Espacio de trabajo | `/workspace` | Agentes, sesiones, recursos y actividad con scope de un espacio de trabajo | `tenant:read` | [reference/modules/xx-multi-tenancy](/es/reference/modules/xx-multi-tenancy/) |
| Plantillas de workspace | `/workspace-templates` | Snapshots reutilizables de configuración de sesión: hooks, settings, connectors y policies. | `sessions:template:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |

### Automatizar

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Alertas | `/alerting` | Enruta hallazgos a destinos e inspecciona entregas | `notify:route:read` | [reference/modules/xv-notify](/es/reference/modules/xv-notify/) |
| Automatizaciones | `/automations` | Los tres raíles de automatización y su catálogo de triggers | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/es/reference/modules/iv-orchestration/) |
| Webhooks y eventos | `/eventing` | Suscripciones a webhooks salientes, su log de entregas y la cola de mensajes fallidos. | `eventing:subscription:read` | [reference/modules/eventing](/es/reference/modules/eventing/) |
| Orquestación | `/orchestration` | Coordinación entre agentes y programaciones | `orchestration:graph:read` | [reference/modules/iv-orchestration](/es/reference/modules/iv-orchestration/) |

### Conectar

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| API Playground | `/api-playground` | Explora y prueba de forma interactiva la API del control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/es/reference/modules/xix-api-manage-as-code/) |
| MCP y skills | `/capabilities` | Gobierna servidores MCP, skills y herramientas | `capabilities:catalog:read` | [reference/modules/v-capabilities](/es/reference/modules/v-capabilities/) |
| Catálogo | `/catalog` | Agentes y capacidades aprobados y curados | `catalog:entry:read` | [reference/modules/xiv-catalog](/es/reference/modules/xiv-catalog/) |
| Enlaces de protocolo | `/communications/protocol-bindings` | Compón y reconcilia enlaces gobernados A2A y MCP | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Despliegue | `/deploy` | Aprovisiona y conecta agentes a la infraestructura | `deploy:deployment:read` | [reference/modules/vii-deploy](/es/reference/modules/vii-deploy/) |
| Inventario | `/inventory` | Descubre y cataloga cada agente, MCP y modelo | `inventory:catalog:read` | [reference/modules/i-inventory](/es/reference/modules/i-inventory/) |
| Conocimiento | `/knowledge` | Bases de conocimiento, RAG y linaje de datos | `knowledge:kb:read` | [reference/modules/viii-knowledge](/es/reference/modules/viii-knowledge/) |
| Operaciones de modelos | `/model-operations` | Modelos propios, admisión y despliegues | `models:registry:read` | [reference/modules/xxiii-model-operations](/es/reference/modules/xxiii-model-operations/) |
| Modelos | `/models` | Modelos, enrutado y claves de proveedor | `models:catalog:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Asistente de configuración | `/onboarding` | Configuración del despliegue paso a paso | `system:admin` | [start/quickstart](/es/start/quickstart/) |
| Plataformas | `/platforms` | Superficies de despliegue, matriz de compliance y ciclo de vida de modelos por plataforma | `models:platforms:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |

### Gobernar

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Mapa de acceso | `/access-map` | Qué lee y escribe cada agente (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/es/reference/modules/iii-access-map/) |
| Exportación a AgentCore | `/agentcore-export` | Planifica y aplica la exportación de policy Cedar a AWS AgentCore y revisa los cambios antes de que se produzcan. | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Gobierno de Claude Code | `/claude-policy` | Policy gestionada, hooks, MCP, sandbox y policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/es/how-to/connectors/claude-code-hooks-pep/) |
| Consola de control | `/console` | Da de alta usuarios, conecta SSO/IdP y gestiona workspaces y grupos de agentes. | `tenant:admin` | [reference/modules/xx-multi-tenancy](/es/reference/modules/xx-multi-tenancy/) |
| Identidad y NHI | `/identity` | SSO, SCIM, el inventario NHI y el grafo WIF | `governance:identity:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Proxy de inferencia | `/inference-proxy` | Compuertas del proxy, reglas DLP de egreso y aprobaciones de dispositivos | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/es/reference/modules/inferenceproxy/) |
| Permisos | `/permissions` | Identidad, roles y aprobaciones | `governance:identity:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Límites de uso | `/rate-limits` | Inventario de límites de Anthropic (solo lectura) | `models:ratelimits:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Residencia de datos | `/residency` | Fija cada organización a una región, o déjala sin fijar | `system:admin` | [reference/modules/xiii-compliance](/es/reference/modules/xiii-compliance/) |
| Políticas de rutinas | `/routine-policies` | Mínimos de cadencia, topes de concurrencia, requisitos de aprobación y allowlists cron para rutinas de Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |

### Demostrar

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Adopción de Claude Code | `/adoption` | Productividad, aceptación y mix de modelos | `adoption:metrics:read` | [reference/modules/claudeadoption](/es/reference/modules/claudeadoption/) |
| Artefactos de agente | `/agent-artifacts` | Skills, extensiones MCP y ficheros de instrucciones: registro, postura y BOM de cadena de suministro | `models:registry:read` | [reference/modules/xxiii-model-operations](/es/reference/modules/xxiii-model-operations/) |
| Cadena de suministro | `/attestation` | Atestación de releases: SLSA, SBOM, VEX y Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/es/how-to/verify-a-release/) |
| Registro de auditoría | `/audit` | Ledger de evidencia con alteraciones detectables | `audit:read` | [reference/modules/ix-security](/es/reference/modules/ix-security/) |
| Cumplimiento | `/compliance` | Marcos, controles y evidencia | `compliance:framework:read` | [reference/modules/xiii-compliance](/es/reference/modules/xiii-compliance/) |
| Paneles | `/dashboards` | KPIs ejecutivos e informes | cualquier usuario autenticado | [reference/modules/xxi-executive-dashboards](/es/reference/modules/xxi-executive-dashboards/) |
| Evals | `/evals` | Calidad, evals y regresión | `evals:run:read` | [reference/modules/xii-evals](/es/reference/modules/xii-evals/) |
| Coste y FinOps | `/finops` | Coste de tokens, presupuestos y gasto | `finops:spend:read` | [reference/modules/xi-finops](/es/reference/modules/xi-finops/) |
| Exportación de postura | `/posture-export` | Exporta la postura real para una torre de control | `posture:export:read` | [reference/modules/posture-export](/es/reference/modules/posture-export/) |
| Grabaciones | `/recordings` | Grabación y replay de sesiones privilegiadas | `recording:session:admin` | [reference/modules/recording](/es/reference/modules/recording/) |
| Red-teaming | `/red-team` | Pruebas adversariales de tus agentes | `redteam:target:read` | [reference/modules/xviii-redteam](/es/reference/modules/xviii-redteam/) |
| Informes | `/reporting` | Genera y descarga informes de gobernanza | `reporting:report:read` | [reference/modules/reporting](/es/reference/modules/reporting/) |
| Seguridad | `/security` | Guardrails, forense y anomalías | `security:finding:read` | [reference/modules/ix-security](/es/reference/modules/ix-security/) |
| Visor de sesiones | `/session-viewer/$id` (solo enlace profundo) | Cronología completa de una sesión grabada, accesible desde una fila de Grabaciones y no desde la barra lateral. | `recording:session:admin` | [reference/modules/recording](/es/reference/modules/recording/) |
| Costes por equipo | `/team-costs` | Gasto atribuido por equipo, ampliable al desglose por proyecto y por modelo. | `finops:spend:read` | [reference/modules/xi-finops](/es/reference/modules/xi-finops/) |

### Inicio de sesión, configuración y cuenta

Estas rutas se montan fuera del registro de funcionalidades. Las marcadas como **sin inicio de
sesión** se sirven antes de que exista una sesión; son las únicas rutas de consola que lo hacen.

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Aceptar una invitación | `/accept-invite` | Destino de un enlace de invitación enviado por correo: la persona invitada define una contraseña y se une al workspace, sin sesión previa. | **sin inicio de sesión** | — |
| Iniciar sesión | `/login` | Página de acceso con credenciales y token para una cuenta ya aprovisionada. | **sin inicio de sesión** | — |
| Ajustes | `/settings` | Ajustes del espacio de trabajo y de la cuenta | cualquier usuario autenticado | — |
| Configuración inicial | `/setup` | Página de una sola vez que convierte un despliegue nuevo en uno utilizable: consume el token de configuración y crea la primera cuenta owner. | **sin inicio de sesión** | — |
| Estado público | `/status-page` | Salud de componentes para personas que no han iniciado sesión, actualizada automáticamente mientras la página permanece abierta. | **sin inicio de sesión** | — |

<!-- END GENERATED olivares-console-routes -->

## Lo que esta página no te cuenta

Es un mapa, no un manual. Indica qué pantallas existen, dónde están y quién puede abrirlas;
no te guía paso a paso por una tarea. Para eso, empieza por [Rutas según el
rol](/es/start/paths-by-role/) o por las [guías prácticas](/es/how-to/self-hosting/).

Las pantallas cuyo backend funciona en deny-closed hasta que un operador lo aprovisiona
aparecen aquí como cualquier otra: la ruta existe y el permiso es real. La
[vista general de módulos](/es/reference/modules/overview/) registra qué módulo actúa y cuál
está gateado, y [Honestidad y límites](/es/start/honesty-and-limits/) expone la regla general.
