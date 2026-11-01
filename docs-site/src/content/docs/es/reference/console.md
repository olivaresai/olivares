---
title: Referencia de la consola — cada pantalla y el permiso que necesita
description: >-
  Todas las rutas que publica la consola de Olivares AI, agrupadas por las áreas
  de la consola, con el permiso RBAC que requiere cada una y la página de
  referencia que abre su enlace de ayuda dentro del producto. Generado a partir
  del censo de rutas de la consola.
---

:::note[Edición]
Las fuentes de directorio (LDAP, Okta/Entra e IdP de agentes/cargas), el ciclo de vida de identidades gestionadas y el servidor SCIM entrante requieren los complementos Business/Enterprise. Community conserva la ingestión genérica, el catálogo de recursos, los hallazgos de seguridad, el Access Map, el inventario de identidades, SSO con un IdP, roles y bindings locales, políticas y aprobaciones. Oculta los paneles y comandos CLI de pago; sus rutas API publicadas devuelven HTTP `501`. Activar un módulo no añade su implementación Business.
:::

Esta página es el mapa de la consola. Enumera **todas las rutas que monta la aplicación**,
no una selección ni las que alguien recordó documentar, junto al permiso que necesita un
principal para entrar y el lugar donde puede leer más.

La consola tiene **una sola navegación: la barra lateral izquierda**. Enumera los lugares
en cuatro grupos —**Trabajo** (Inicio, Sesiones, Archivos y espacios, Trabajo y
comunicaciones), **Herramientas** (Herramientas de IA, Modelos, MCP y skills, Fuentes),
**Control** (Reglas y aprobaciones, Uso y coste, Actividad y auditoría, Mapa de acceso) y
**Administración** (Personas y grupos, Seguridad, Operaciones, Licencia)— con Ajustes al
final. Cada pantalla está en un lugar, y la fila de enlaces sobre una pantalla enumera las
demás pantallas de ese lugar. Un lugar aparece solo cuando tus permisos permiten al menos
una de sus pantallas, y una pantalla Business solo en la edición Business. La barra
lateral se pliega a sus iconos y vuelve a abrirse; en una pantalla estrecha, la misma
barra se abre como un panel desde el botón de menú. La barra superior contiene el título
de la página, la búsqueda (⌘K o Ctrl+K), las notificaciones y el menú de la cuenta. La
paleta de comandos busca las mismas pantallas con los mismos permisos. Las antiguas
direcciones de directorio de área (`/areas/…`) abren el lugar que ahora contiene sus
pantallas y conservan la consulta y el fragmento.

Una instalación nueva muestra solo el primer trabajo: Inicio, Sesiones, Herramientas de IA,
Aprobaciones, el asistente de configuración y Ajustes. Cualquier otra
pantalla es allí una vista previa: su dirección, su API y su CLI siguen funcionando, y la
navegación, la paleta de comandos y los atajos no la muestran. Una instalación que ya
existía antes de la actualización muestra todas las pantallas que mostraba, igual que una
cuyo administrador ha elegido sus módulos (desde el siguiente arranque) o que usa los datos
de demostración.

Es una página **generada**. El inventario procede de `web/src/features/route-census.json`,
el censo append-only que `registry.route-conservation.test.ts` coteja con el router compilado,
por lo que ninguna pantalla puede añadirse, moverse ni perderse sin que esta página cambie con
ella. El nombre y la descripción de una línea de cada pantalla son **las propias cadenas de la
consola**, tomadas del mismo catálogo de traducción que renderiza la consola: lo que lees
aquí es lo que ves en el producto. Las tablas siguientes agrupan esas filas por área, después de Inicio (la vista general) y
antes de inicio de sesión, configuración y cuenta. Las nueve antiguas direcciones de directorio de área
aparecen junto a las rutas montadas fuera del registro de funciones; cada una redirige ahora a
su lugar.

:::note[Los permisos los aplica el motor, no esta tabla]
La columna `Requiere` indica el permiso que comprueba la consola antes de ofrecer una
ruta, usando los permisos efectivos devueltos por el motor. El motor autoriza las
peticiones a la API de forma independiente, incluidas las realizadas fuera de la consola.
Que una entrada sea visible no acredita que el módulo esté configurado o preparado para
funcionar. Consulta [Roles y permisos](/es/reference/modules/vi-governance/).
:::

## Cómo leer esta página

- **Pantalla**: el nombre que usan la fila de enlaces sobre una pantalla y la paleta de
  comandos.
- **Ruta**: la URL bajo el origen de la consola de tu despliegue. Es un contrato publicado:
  un marcador, un enlace profundo de un runbook y una referencia cruzada de la documentación
  usan todos esta cadena.
- **Requiere**: el permiso RBAC. `cualquier usuario autenticado` significa que la ruta está
  abierta a todo principal autenticado; **sin inicio de sesión** significa que se sirve antes
  de que exista una sesión.
- **Referencia**: la página que abre el propio enlace de ayuda de la consola para esa pantalla.

Los encabezados siguientes son las áreas del registro con las que el censo de rutas agrupa
las pantallas:
Infraestructura, IA, Datos y contexto, Trabajo y comunicaciones, Automatización,
Seguridad e identidad, Entrega, Supervisión, y Sistema y ajustes.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

La consola publica **66 rutas**. Todas figuran en las tablas siguientes, con el permiso que
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
| Herramientas de agentes | `/agent-tools` | Consulta las cuentas y claves de API con las que se ejecutan las sesiones; instalar las herramientas e iniciar su sesión es cosa de los administradores del despliegue | `sessions:account:read` | [how-to/add-a-provider](/es/how-to/add-a-provider/) |
| Operar sesiones | `/agentops` | Inicia sesiones y sigue lo que hace cada una, también las que encuentra Olivares | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/es/how-to/run-claude-code-with-olivares/) |
| Operaciones de modelos | `/model-operations` | Modelos propios, admisión y despliegues | `models:registry:read` | [reference/modules/xxiii-model-operations](/es/reference/modules/xxiii-model-operations/) |
| Modelos | `/models` | Modelos, enrutado y gobernanza | `models:catalog:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Plataformas | `/platforms` | Superficies de despliegue, matriz de compliance y ciclo de vida de modelos por plataforma | `models:platforms:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Cuentas de proveedor | `/provider-accounts` | Lista las cuentas de proveedor con nombre y adopta un perfil de proveedor existente como cuenta | `sessions:account:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Vínculos de fuentes | `/provider-bindings` | Vincula fuentes configuradas a perfiles de proveedor | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Perfiles de proveedor | `/provider-profiles` | Registra y administra los directorios de proveedor bajo los que se lanzan las sesiones, y lee su configuración bajo demanda | `sessions:profile:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Proveedores | `/providers` | Registra las claves de API y los endpoints con los que arrancan las sesiones; pruébalos, rótalos y revócalos | `sessions:provider:read` | [how-to/add-a-provider](/es/how-to/add-a-provider/) |
| Límites de uso | `/rate-limits` | Inventario de límites de Anthropic (solo lectura) | `models:ratelimits:read` | [reference/modules/x-models](/es/reference/modules/x-models/) |
| Alcance | `/reach` | Mira a qué destinos puede llegar cada herramienta y cuáles esperan aprobación. | `accessmap:graph:read` | [reference/modules/iii-access-map](/es/reference/modules/iii-access-map/) |
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

### Trabajo y comunicaciones

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Comunicaciones | `/communications` | Canales, avisos directos y bandeja personal del workspace seleccionado | `sessions:channel:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Administración de canales | `/communications/administration` | Administra canales: configuración e historial de concesiones | `sessions:channel:admin` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Traspasos | `/communications/handoffs` | Ofertas de responsabilidad de trabajo dirigidas a ti: lee el contexto y acepta o rechaza | `sessions:delivery:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Bandeja de comunicaciones | `/communications/inbox` | Mensajes dirigidos a ti: ábrelos y acusa recibo de cada uno | `sessions:delivery:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Nuevo canal | `/communications/new` | Crea un canal con concesiones iniciales explícitas | `sessions:channel:write` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Enlaces de protocolo | `/communications/protocol-bindings` | Compón y reconcilia enlaces gobernados A2A y MCP | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |
| Trabajo | `/work` | Trabajo compartido: unidades, dependencias, aceptación y decisiones | `sessions:work:read` | [reference/modules/ii-sessions](/es/reference/modules/ii-sessions/) |

### Automatización

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Alertas | `/alerting` | Enruta hallazgos a destinos e inspecciona entregas | `notify:route:read` | [reference/modules/xv-notify](/es/reference/modules/xv-notify/) |
| Webhooks y eventos | `/eventing` | Suscripciones a webhooks salientes, su log de entregas y la cola de mensajes fallidos. | `eventing:subscription:read` | [reference/modules/eventing](/es/reference/modules/eventing/) |

### Seguridad e identidad

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Mapa de acceso | `/access-map` | Qué lee y escribe cada agente (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/es/reference/modules/iii-access-map/) |
| Exportación de políticas | `/agentcore-export` | Planifica, revisa y aplica la proyección de las reglas de gobierno de este tenant sobre AWS AgentCore como políticas Cedar; planificar no escribe nada | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Política gestionada | `/claude-policy` | Policy gestionada, hooks, MCP, sandbox y policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/es/how-to/connectors/claude-code-hooks-pep/) |
| Identidad y acceso | `/identity` | SSO, SCIM y las identidades que usan tus agentes | `governance:identity:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Proxy de inferencia | `/inference-proxy` | Compuertas del proxy, reglas DLP de egreso y aprobaciones de dispositivos | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/es/reference/modules/inferenceproxy/) |
| Kill switch | `/killswitch` | Parada de emergencia, recuperación con doble control y contención guardiana | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/es/how-to/cookbook/kill-switch-drill/) |
| Permisos | `/permissions` | Identidad, roles y aprobaciones | `governance:identity:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Políticas de rutinas | `/routine-policies` | Intervalos mínimos entre ejecuciones, topes de concurrencia, requisitos de aprobación y allowlists cron para rutinas de Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/es/reference/modules/vi-governance/) |
| Seguridad | `/security` | Guardrails, forense y anomalías | `security:finding:read` | [reference/modules/ix-security](/es/reference/modules/ix-security/) |
| Caja fuerte | `/vault` | Tus contraseñas, notas y códigos de autenticación | `tenant:read` | [reference/console](/es/reference/console/) |

### Entrega

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Publicación Git | `/git-publication` | Publicar commits, abrir pull requests y fusionar mediante destinos Git aprobados | `gitpublish:target:read` | [reference/modules/gitpublish](/es/reference/modules/gitpublish/) |

### Supervisión

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Adopción | `/adoption` | Productividad, aceptación y mix de modelos | `adoption:metrics:read` | [reference/modules/claudeadoption](/es/reference/modules/claudeadoption/) |
| Cadena de suministro | `/attestation` | Atestación de releases: SLSA, SBOM, VEX y Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/es/how-to/verify-a-release/) |
| Registro de auditoría | `/audit` | Ledger de evidencia con alteraciones detectables | `audit:read` | [reference/modules/ix-security](/es/reference/modules/ix-security/) |
| Paneles | `/dashboards` | KPIs ejecutivos e informes | cualquier usuario autenticado | [reference/modules/xxi-executive-dashboards](/es/reference/modules/xxi-executive-dashboards/) |
| Evals | `/evals` | Calidad, evals y regresión | `evals:run:read` | [reference/modules/xii-evals](/es/reference/modules/xii-evals/) |
| Salud y SLA | `/health` | Disponibilidad y SLA de agentes y MCP | `health:status:read` | [reference/modules/xxii-health](/es/reference/modules/xxii-health/) |
| Observabilidad | `/observability` | Salud de la ingesta por estándar y exploración de trazas | `health:status:read` | [reference/modules/observability](/es/reference/modules/observability/) |
| Retención | `/retention` | Cuánto tiempo se conserva cada clase de registro, el barrido que lo aplica y sus certificados sellados | `compliance:retention:read` | [reference/modules/xiii-compliance](/es/reference/modules/xiii-compliance/) |
| Presupuestos | `/stored-budgets` | Consumo frente a cada límite guardado. La capacidad reservada cuenta para el límite. | `finops:budget:read` | [reference/modules/xi-finops](/es/reference/modules/xi-finops/) |
| Uso | `/usage` | Los límites oficiales de 5 horas y semanales de cada cuenta, la más cercana primero, y los tokens y el coste de hoy. | `sessions:run:read` | [reference/console](/es/reference/console/) |

### Sistema y ajustes

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| API Playground | `/api-playground` | Explora y prueba de forma interactiva la API del control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/es/reference/modules/xix-api-manage-as-code/) |
| Copias de seguridad | `/backups` | Inicia, programa, descarga y restaura copias de seguridad, con una segunda confirmación en la vía destructiva. | `system:admin` | [how-to/backup-and-restore](/es/how-to/backup-and-restore/) |
| Administración | `/console` | Usuarios, SSO/IdP, workspaces, grupos de agentes, roles, secretos, conectores, claves API y la licencia de esta instalación | `tenant:admin` | [reference/modules/xx-multi-tenancy](/es/reference/modules/xx-multi-tenancy/) |
| Diferencia de origen | `/console/sources/diff` | Compara una revisión base y una revisión head de un repositorio Git conectado, archivo por archivo | `system:admin` | [reference/console](/es/reference/console/) |
| Registros | `/logs` | Flujo en vivo del log del motor, filtrado por nivel y módulo, con búsqueda y pausa. | `system:admin` | [how-to/troubleshooting](/es/how-to/troubleshooting/) |
| Asistente de configuración | `/onboarding` | Configuración del despliegue paso a paso | `system:admin` | [start/quickstart](/es/start/quickstart/) |
| Actualizaciones | `/updates` | La versión que ejecuta este motor, la versión firmada más reciente de su canal y una acción para instalarla | `system:admin` | [how-to/upgrade-and-rollback](/es/how-to/upgrade-and-rollback/) |

### Inicio de sesión, configuración y cuenta

Estas rutas se montan fuera del registro de funcionalidades. Las marcadas como **sin inicio de
sesión** se sirven antes de que exista una sesión; son las únicas rutas de consola que lo hacen.

| Pantalla | Ruta | Qué es | Requiere | Referencia |
|---|---|---|---|---|
| Aceptar una invitación | `/accept-invite` | Destino de un enlace de invitación enviado por correo: la persona invitada define una contraseña y se une al workspace, sin sesión previa. | **sin inicio de sesión** | — |
| Iniciar sesión | `/login` | Página de acceso con credenciales y token para una cuenta ya aprovisionada. | **sin inicio de sesión** | — |
| Vincular un dispositivo | `/pair` | Destino de un enlace de vinculación de dispositivo de un solo uso: el nuevo dispositivo inicia sesión sin una sesión previa. | **sin inicio de sesión** | — |
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
