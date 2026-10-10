---
title: Referencia de la consola — cada pantalla y el permiso que necesita
description: >-
  Todas las rutas que publica la consola de Olivares AI, agrupadas por las áreas
  de la consola, con el permiso RBAC que requiere cada una y la página de
  referencia que abre su enlace de ayuda dentro del producto. Generado a partir
  del censo de rutas de la consola.
---

Esta página es el mapa de la consola. Enumera **todas las rutas que monta la aplicación**,
no una selección ni las que alguien recordó documentar, junto al permiso que necesita un
principal para entrar y el lugar donde puede leer más.

La consola tiene **una sola estructura de navegación: nueve áreas con secciones**. Cada
pantalla está en un área y en una sección de ella, y todo lo que muestra dónde está una
pantalla nombra el mismo lugar: Todas las áreas (en la barra lateral, y Más en un teléfono), las
páginas de directorio de área, la ruta de navegación, la paleta de comandos y la fila de
enlaces sobre una pantalla, que enumera las demás pantallas de su sección. La barra lateral
fija una lista corta de pantallas, el primer trabajo primero (Inicio, Sesiones, Herramientas de IA, Aprobaciones), después Trabajo, y una pantalla fijada
sigue marcada en las demás pantallas de su sección. Un filtro de navegación sustituye el
árbol agrupado por una lista ordenada de coincidencias autorizadas; la paleta de comandos
usa el mismo índice, el mismo orden y la misma proyección de permisos. Un directorio de área
es una página de enlaces —las entradas que permiten tus permisos—, no una lectura en vivo de
capacidad, disponibilidad o preparación.

Una instalación nueva muestra solo el primer trabajo: Inicio, Sesiones, Herramientas de IA
con Proveedores, Aprobaciones, el asistente de configuración y Ajustes. Cualquier otra
pantalla es allí una vista previa: su dirección, su API y su CLI siguen funcionando, y la
navegación, la paleta de comandos y los atajos no la muestran. Una instalación que ya
existía antes de la actualización muestra todas las pantallas que mostraba, igual que una
cuyo administrador ha elegido sus módulos (desde el siguiente arranque) o que usa los datos
de demostración.

Es una página **generada**. El inventario procede de `web/src/features/route-census.json`,
el censo append-only que `registry.route-conservation.test.ts` coteja con el router compilado,
por lo que ninguna pantalla puede añadirse, moverse ni perderse sin que esta página cambie con
ella. El nombre y la descripción de una línea de cada pantalla son **las propias cadenas de la
consola**, tomadas del mismo catálogo de traducción que renderiza la barra lateral: lo que lees
aquí es lo que ves en el producto. Las tablas siguientes agrupan esas filas por área, después de Inicio (la vista general) y
antes de inicio de sesión, configuración y cuenta. Las nueve páginas de directorio de área
aparecen junto a las rutas montadas fuera del registro de funciones.

:::note[Los permisos los aplica el motor, no esta tabla]
La columna `Requiere` indica el permiso que comprueba la consola antes de ofrecer una
ruta, usando los permisos efectivos devueltos por el motor. El motor autoriza las
peticiones a la API de forma independiente, incluidas las realizadas fuera de la consola.
Que una entrada sea visible no acredita que el módulo esté configurado o preparado para
funcionar. Consulta [Roles y permisos](/es/reference/modules/vi-governance/).
:::

## Cómo leer esta página

- **Pantalla**: el nombre que usan la barra lateral, los directorios de área y la paleta
  de comandos.
- **Ruta**: la URL bajo el origen de la consola de tu despliegue. Es un contrato publicado:
  un marcador, un enlace profundo de un runbook y una referencia cruzada de la documentación
  usan todos esta cadena.
- **Requiere**: el permiso RBAC. `cualquier usuario autenticado` significa que la ruta está
  abierta a todo principal autenticado; **sin inicio de sesión** significa que se sirve antes
  de que exista una sesión.
- **Referencia**: la página que abre el propio enlace de ayuda de la consola para esa pantalla.

Los encabezados siguientes son las áreas, en el orden en que las lista Todas las áreas:
Infraestructura, IA, Datos y contexto, Trabajo y comunicaciones, Automatización,
Seguridad e identidad, Despliegue, Observabilidad y evidencias, y Sistema y ajustes.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

La consola publica **83 rutas**. Todas figuran en las tablas siguientes, con el permiso que
requieren y la página de referencia que abre su enlace de ayuda dentro del producto.

### Inicio

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Inicio | `/` | Visión general del estado y la salud del estate | cualquier usuario autenticado | [inicio de la documentación](/es/) |

### Infraestructura

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Entorno | `/estate` | Inspecciona los recursos configurados y sus relaciones, incluidas las dependencias de los elementos de trabajo. | `sessions:run:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Inventario | `/inventory` | Descubre y cataloga los agentes, servidores MCP y modelos que los conectores observaron. | `inventory:catalog:read` | [reference/modules/i-inventory](/es/reference/modules/i-inventory/) |
| Espacio de trabajo | `/workspace` | Agentes, sesiones, recursos y actividad con scope de un espacio de trabajo | `tenant:read` | [reference/modules/xx-multi-tenancy](/es/reference/modules/xx-multi-tenancy/) |

### IA

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Herramientas de agentes | `/agent-tools` | Detecta, instala y actualiza las herramientas de agentes en este host y sigue cada instalación; solo para administradores del despliegue | `system:admin` | [how-to/add-a-provider](/es/how-to/add-a-provider/) |
| Sesiones | `/agentops` | Inicia sesiones y sigue lo que hace cada una, también las que encuentra Olivares | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/es/how-to/run-claude-code-with-olivares/) |
| Servidores MCP | `/mcp-servers` | Conecta servidores MCP remotos a esta organización, pruébalos y elige qué herramientas pueden usar las sesiones | `tenant:admin` | [how-to/connectors/mcp-governance](/es/how-to/connectors/mcp-governance/) |
| Operaciones de modelos | `/model-operations` | Modelos propios, admisión y despliegues | `models:registry:read` | [reference/modules/xxiii-model-operations](/es/reference/modules/xxiii-model-operations/) |
| Modelos | `/models` | Modelos, enrutado y claves de proveedor | `models:catalog:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Plataformas | `/platforms` | Superficies de despliegue, matriz de compliance y ciclo de vida de modelos por plataforma | `models:platforms:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Cuentas de proveedor | `/provider-accounts` | Lista las cuentas de proveedor con nombre y adopta un perfil de proveedor existente como cuenta | `sessions:account:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Vínculos de fuentes | `/provider-bindings` | Dedica fuentes configuradas, en la revisión que aplicó este nodo, a perfiles de proveedor | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Perfiles de proveedor | `/provider-profiles` | Registra y administra los directorios de proveedor bajo los que se lanzan las sesiones, y lee su configuración bajo demanda | `sessions:profile:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Proveedores | `/providers` | Registra las claves de API y los endpoints con los que arrancan las sesiones; pruébalos, rótalos y revócalos | `sessions:provider:read` | [how-to/add-a-provider](/es/how-to/add-a-provider/) |
| Límites de uso | `/rate-limits` | Inventario de límites de Anthropic (solo lectura) | `models:ratelimits:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Sandbox | `/sandbox` | Pruebas aisladas de agentes y replay | `sandbox:run:read` | [reference/modules/xvii-sandbox](/es/reference/modules/xvii-sandbox/) |
| Sesiones | `/sessions` | Inicia sesiones y sigue lo que hace cada una, también las que encuentra Olivares | `sessions:live:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Voz | `/voice` | Sesiones de voz y tiempo real | `voice:session:read` | [reference/modules/xvi-voice](/es/reference/modules/xvi-voice/) |
| Plantillas de workspace | `/workspace-templates` | Snapshots reutilizables de configuración de sesión: hooks, settings, connectors y policies. | `sessions:template:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |

### Datos y contexto

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Artefactos de agente | `/agent-artifacts` | Skills, extensiones MCP y ficheros de instrucciones: registro, postura y BOM de cadena de suministro | `models:registry:read` | [reference/modules/xxiii-model-operations](/es/reference/modules/xxiii-model-operations/) |
| MCP y skills | `/capabilities` | Gobierna servidores MCP, skills y herramientas | `capabilities:catalog:read` | [reference/modules/v-capabilities](/es/reference/modules/v-capabilities/) |
| Catálogo | `/catalog` | Agentes y capacidades aprobados y curados | `catalog:entry:read` | [reference/modules/xiv-catalog](/es/reference/modules/xiv-catalog/) |
| Conocimiento | `/knowledge` | Bases de conocimiento, RAG y linaje de datos | `knowledge:kb:read` | [reference/modules/viii-knowledge](/es/reference/modules/viii-knowledge/) |
| Catálogo de skills | `/skills` | Explora packs de skills y asígnalos a departamentos, grupos de agentes y agentes | `skills:catalog:read` | [reference/console](/es/reference/console/) |

### Trabajo y comunicaciones

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Comunicaciones | `/communications` | Canales, avisos directos y bandeja personal del workspace seleccionado | `sessions:channel:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Administración de canales | `/communications/administration` | Administra canales: configuración e historial de concesiones, cada acto bajo el ETag actual del canal | `sessions:channel:admin` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Traspasos | `/communications/handoffs` | Ofertas de responsabilidad de trabajo dirigidas a ti: lee el contexto y acepta o rechaza | `sessions:delivery:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Bandeja de comunicaciones | `/communications/inbox` | Tu bandeja exacta: entregas dirigidas a ti, leídas en fresco y acusadas de forma explícita | `sessions:delivery:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Nuevo canal | `/communications/new` | Crea un canal con concesiones iniciales explícitas | `sessions:channel:write` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Enlaces de protocolo | `/communications/protocol-bindings` | Compón y reconcilia enlaces gobernados A2A y MCP | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Trabajo | `/work` | Trabajo compartido: unidades, dependencias, aceptación y decisiones | `sessions:work:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |

### Automatización

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Alertas | `/alerting` | Enruta hallazgos a destinos e inspecciona entregas | `notify:route:read` | [reference/modules/xv-notify](/es/reference/modules/xv-notify/) |
| Automatizaciones | `/automations` | Los tres raíles de automatización y su catálogo de triggers | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/es/reference/modules/iv-orchestration/) |
| Webhooks y eventos | `/eventing` | Suscripciones a webhooks salientes, su log de entregas y la cola de mensajes fallidos. | `eventing:subscription:read` | [reference/modules/eventing](/es/reference/modules/eventing/) |
| Orquestación | `/orchestration` | Coordinación entre agentes y programaciones | `orchestration:graph:read` | [reference/modules/iv-orchestration](/es/reference/modules/iv-orchestration/) |

### Seguridad e identidad

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Mapa de acceso | `/access-map` | Qué lee y escribe cada agente (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/es/reference/modules/iii-access-map/) |
| Exportación a AgentCore | `/agentcore-export` | Planifica, revisa y aplica la proyección de las reglas de gobierno de este tenant sobre AWS AgentCore como políticas Cedar; planificar no escribe nada | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Gobierno de Claude Code | `/claude-policy` | Policy gestionada, hooks, MCP, sandbox y policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/es/how-to/connectors/claude-code-hooks-pep/) |
| Identidad y NHI | `/identity` | SSO, SCIM, el inventario NHI y el grafo WIF | `governance:identity:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Proxy de inferencia | `/inference-proxy` | Compuertas del proxy, reglas DLP de egreso y aprobaciones de dispositivos | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/es/reference/modules/inferenceproxy/) |
| Kill switch | `/killswitch` | Parada de emergencia, recuperación con doble control y contención guardiana | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/es/how-to/cookbook/kill-switch-drill/) |
| Permisos | `/permissions` | Identidad, roles y aprobaciones | `governance:identity:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Red-teaming | `/red-team` | Pruebas adversariales de tus agentes | `redteam:target:read` | [reference/modules/xviii-redteam](/es/reference/modules/xviii-redteam/) |
| Residencia de datos | `/residency` | Fija cada organización a una región, o déjala sin fijar | `system:admin` | [reference/modules/xiii-compliance](/es/reference/modules/xiii-compliance/) |
| Políticas de rutinas | `/routine-policies` | Intervalos mínimos entre ejecuciones, topes de concurrencia, requisitos de aprobación y allowlists cron para rutinas de Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Seguridad | `/security` | Guardrails, forense y anomalías | `security:finding:read` | [reference/modules/ix-security](/es/reference/modules/ix-security/) |

### Despliegue

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Despliegue | `/deploy` | Aprovisiona y conecta agentes a la infraestructura | `deploy:deployment:read` | [reference/modules/vii-deploy](/es/reference/modules/vii-deploy/) |
| Publicación Git | `/git-publication` | Publicar commits, abrir pull requests y fusionar mediante destinos Git aprobados | `gitpublish:target:read` | [reference/modules/gitpublish](/es/reference/modules/gitpublish/) |

### Observabilidad y evidencias

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Adopción de Claude Code | `/adoption` | Productividad, aceptación y mix de modelos | `adoption:metrics:read` | [reference/modules/claudeadoption](/es/reference/modules/claudeadoption/) |
| Cadena de suministro | `/attestation` | Atestación de releases: SLSA, SBOM, VEX y Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/es/how-to/verify-a-release/) |
| Registro de auditoría | `/audit` | Ledger de evidencia con alteraciones detectables | `audit:read` | [reference/modules/ix-security](/es/reference/modules/ix-security/) |
| Cumplimiento | `/compliance` | Marcos, controles y evidencia | `compliance:framework:read` | [reference/modules/xiii-compliance](/es/reference/modules/xiii-compliance/) |
| Paneles | `/dashboards` | KPIs ejecutivos e informes | cualquier usuario autenticado | [reference/modules/xxi-executive-dashboards](/es/reference/modules/xxi-executive-dashboards/) |
| Evals | `/evals` | Calidad, evals y regresión | `evals:run:read` | [reference/modules/xii-evals](/es/reference/modules/xii-evals/) |
| Coste y FinOps | `/finops` | Coste de tokens, presupuestos y gasto | `finops:spend:read` | [reference/modules/xi-finops](/es/reference/modules/xi-finops/) |
| Salud y SLA | `/health` | Disponibilidad y SLA de agentes y MCP | `health:status:read` | [reference/modules/xxii-health](/es/reference/modules/xxii-health/) |
| Observabilidad | `/observability` | Salud de la ingesta por estándar y exploración de trazas | `health:status:read` | [reference/modules/observability](/es/reference/modules/observability/) |
| Exportación de postura | `/posture-export` | Exporta la postura real para una torre de control | `posture:export:read` | [reference/modules/posture-export](/es/reference/modules/posture-export/) |
| Grabaciones | `/recordings` | Grabación y replay de sesiones privilegiadas | `recording:session:admin` | [reference/modules/recording](/es/reference/modules/recording/) |
| Informes | `/reporting` | Genera y descarga informes de gobernanza | `reporting:report:read` | [reference/modules/reporting](/es/reference/modules/reporting/) |
| Visor de sesiones | `/session-viewer/$id` (solo enlace profundo) | Cronología completa de una sesión grabada, accesible desde una fila de Grabaciones y no desde la barra lateral. | `recording:session:admin` | [reference/modules/recording](/es/reference/modules/recording/) |
| Costes por equipo | `/team-costs` | Gasto atribuido por equipo, ampliable al desglose por proyecto y por modelo. | `finops:spend:read` | [reference/modules/xi-finops](/es/reference/modules/xi-finops/) |

### Sistema y ajustes

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| API Playground | `/api-playground` | Explora y prueba de forma interactiva la API del control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/es/reference/modules/xix-api-manage-as-code/) |
| Copias de seguridad | `/backups` | Inicia, programa, descarga y restaura copias de seguridad, con una segunda confirmación en la vía destructiva. | `system:admin` | [how-to/backup-and-restore](/es/how-to/backup-and-restore/) |
| Administración | `/console` | Usuarios, SSO/IdP, workspaces, grupos de agentes, roles, secretos, conectores, claves API y la licencia de esta instalación | `tenant:admin` | [reference/modules/xx-multi-tenancy](/es/reference/modules/xx-multi-tenancy/) |
| Diferencia de origen | `/console/sources/diff` | Compara una revisión base y una revisión head de un repositorio Git conectado, archivo por archivo | `system:admin` | [reference/console](/es/reference/console/) |
| Registros | `/logs` | Flujo en vivo del log del motor, filtrado por nivel y módulo, con búsqueda y pausa. | `system:admin` | [how-to/troubleshooting](/es/how-to/troubleshooting/) |
| Asistente de configuración | `/onboarding` | Configuración del despliegue paso a paso | `system:admin` | [start/quickstart](/es/start/quickstart/) |
| Tenants | `/tenants` | Retira o restaura el servicio de un tenant | `system:admin` | [how-to/troubleshooting](/es/how-to/troubleshooting/) |

### Inicio de sesión, configuración y cuenta

Estas rutas se montan fuera del registro de funcionalidades. Las marcadas como **sin inicio de
sesión** se sirven antes de que exista una sesión; son las únicas rutas de consola que lo hacen.

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Aceptar una invitación | `/accept-invite` | Destino de un enlace de invitación enviado por correo: la persona invitada define una contraseña y se une al workspace, sin sesión previa. | **sin inicio de sesión** | — |
| IA | `/areas/ai` | Directorio del área IA: observación y gestión de sesiones, perfiles y entornos de proveedor, modelos, ejecución especializada y referencia del proveedor. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Automatización | `/areas/automation` | Directorio del área Automatización: flujos y orquestación, eventos y notificaciones. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Datos y contexto | `/areas/data-context` | Directorio del área Datos y contexto: capacidades, conocimiento y artefactos. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Despliegue | `/areas/deployment` | Directorio del área Despliegue: preparación y control de despliegues. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Infraestructura | `/areas/infrastructure` | Directorio del área Infraestructura: inventario y espacios de trabajo del entorno. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Observabilidad y evidencias | `/areas/observation` | Directorio del área Observabilidad y evidencias: estado y actividad, costes y adopción, auditoría, evaluación y evidencias. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Seguridad e identidad | `/areas/security-identity` | Directorio del área Seguridad e identidad: identidad y acceso, políticas y fronteras de gobierno, protección y respuesta. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Sistema y ajustes | `/areas/system` | Directorio del área Sistema y ajustes: administración, instalación y mantenimiento, herramientas de desarrollo y preferencias personales. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
| Trabajo y comunicaciones | `/areas/work-communications` | Directorio del área Trabajo y comunicaciones: trabajo duradero entre sesiones y comunicaciones gobernadas. Enumera las entradas que permiten tus permisos. | cualquier usuario autenticado | — |
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
El listado de un directorio de área no es una lectura en vivo de capacidad o preparación.
