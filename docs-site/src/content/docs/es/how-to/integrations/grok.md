---
title: Integrar Grok Build
description: >-
  Incorpora Grok Build al plano de control de gobierno: el conector, el hook gobernado
  y lo que muestra la consola una vez en funcionamiento.
---

La integración `grok` gobierna **Grok Build, el agente de terminal**, desde el host donde se
ejecuta. Lee en modo solo lectura su configuración TOML, el perfil de sandbox, los nombres de
servidores MCP, los requisitos de sistema y el fichero que desactiva hooks. Opcionalmente recibe
trazas OTLP. No es el conector de la API de xAI, no consulta modelos remotos y no necesita un
secreto del proveedor. El control preventivo de herramienta viaja por `olivares grok-hook` y un
PEP local separado.

## Agregar Grok Build

### Requisitos previos

- Olivares AI y Grok Build instalados en el mismo host, o con las rutas de configuración de Grok
  montadas en el host del conector como solo lectura.
- El UUID del tenant al que se atribuirá la postura.
- Permisos del usuario de servicio de Olivares para leer `~/.grok/config.toml`,
  `/etc/grok/requirements.toml`, `~/.grok/disabled-hooks` y, si se declara, el
  `managed-settings.json` compatible.
- Una cuenta superadmin con AAL3 si el alta se realiza desde la consola.

No introduzca una clave xAI en esta fuente: no hay ningún campo secreto y no se realiza ninguna
llamada a la API de inferencia.

1. Abra **Control console** (`/console`) y la pestaña **Connectors**.
2. Añada una fuente de tipo `grok`, nombre `grok-demo` —o un nombre de host estable—, tenant,
   intervalo por lotes y estado habilitado. `60` segundos permite ver cambios de postura durante
   un piloto sin convertir la lectura local en un bucle continuo.
3. Guarde, use **Test** y recargue. La fila confirma el roster; el primer `Gather` posterior es el
   que lee realmente los ficheros y emite hallazgos.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Configura quién entra y qué puede administrar: alta de usuarios, conexión de SSO y gestión de workspaces y grupos de agentes.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Configura quién entra y qué puede administrar: alta de usuarios, conexión de SSO y gestión de workspaces y grupos de agentes.">

## Configurar Grok Build

### 1. Inventario y requisitos del host

| Ajuste de la fuente | Predeterminado | Qué mide |
|---|---|---|
| `agent_ref` | `grok-build` | Referencia estable que aparecerá en los hallazgos. |
| `config_path` | `~/.grok/config.toml` | Perfil de sandbox y nombres de servidores MCP declarados por el usuario. |
| `requirements_path` | `/etc/grok/requirements.toml` | Capa de sistema que acota la configuración efectiva. |
| `disabled_hooks_path` | `~/.grok/disabled-hooks` | Nombres de hooks desactivados por el usuario, uno por línea. |
| `managed_settings_path` | vacío | `managed-settings.json` de Claude Code que Grok honra por compatibilidad; vacío significa “no medido”. |
| `otlp_http` | `false` | Receptor de trazas; apagado hasta que el operador decida reservar un puerto. |

En Linux, un requisito mínimo para imponer el sandbox es:

```toml
[sandbox]
profile = "strict"
```

Distribúyalo en `/etc/grok/requirements.toml` con propiedad administrativa. `strict` limita la
escritura al workspace, `~/.grok/` y temporales, y bloquea red según la garantía documentada para
Linux. El mismo valor en `~/.grok/config.toml` es solo preferencia de usuario: las opciones de
línea de comandos y el entorno pueden participar en la configuración, mientras que
`requirements.toml` es la capa que acota.

Para limitar MCP, declare en `requirements.toml` únicamente las tablas
`[mcp_servers.<nombre-aprobado>]` que la flota puede usar. Olivares inventaría los nombres, no los
comandos, URLs ni credenciales contenidos dentro de esas tablas. Un fichero ausente, ilegible o
presente sin `[mcp_servers]` produce estados distintos; “no medido” nunca se muestra como
“ninguno”.

Grok también puede leer `/etc/claude-code/managed-settings.json` por compatibilidad. Configure
`managed_settings_path` solo si desea que Olivares mida esa superficie. No reutilice a ciegas un
hook de Claude: los payloads de Grok usan claves camelCase y eventos snake_case, y requieren
`olivares grok-hook`.

### 2. Hook gobernado

Instale `olivares grok-hook` mediante el descubrimiento nativo de la versión de Grok desplegada:
un fichero de ajustes JSON del que Grok consume la clave `hooks`, o un fichero `*.json` en un
directorio de hooks como `~/.grok/hooks/`. Grok carga esos ficheros por nombre. La forma completa
del wrapper de autoría no está definida por Olivares ni se conserva en este árbol; use el esquema
de la versión instalada y establezca como comando exactamente:

```text
olivares grok-hook
```

El PEP se monta cuando `OLIVARES_GROK_HOOK_PEP_CONFIG` apunta, al arrancar Olivares, a una
configuración válida:

```json
{
  "listen": "127.0.0.1:8449",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Una instancia gobierna un tenant y exige identidad firme. El cliente lee
`OLIVARES_GROK_HOOK_URL`, `OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`,
`OLIVARES_GROK_HOOK_AGENT`, `OLIVARES_GROK_HOOK_ORG` y `OLIVARES_GROK_HOOK_ACCOUNT`. Entréguelas
desde el gestor de procesos y secretos; el token no pertenece al JSON del hook.

El nombre que asigne al hook importa. Un usuario puede añadirlo a
`~/.grok/disabled-hooks` y el dispatcher lo omitirá sin considerar si procedía de una capa
administrada. Ni `requirements.toml` ni MDM acotan ese fichero. El conector lo lee y genera un
hallazgo alto con la lista de nombres desactivados, pero no puede impedir la desactivación.

### 3. Trazas OTLP opcionales

Al habilitar `otlp_http=true`, el receptor escucha por defecto en `127.0.0.1:4318` y acepta
`POST /v1/traces`, la ruta medida para Grok Build. Es una entrada sin autenticación y debe seguir
en loopback. Si otro conector ya ocupa `4318`, elija un puerto local libre y aplique el mismo valor
en `otlp_http_addr` y en el endpoint OTLP del agente.

La recogida reduce las trazas a atribución, nombre de span y `session_id`; no conserva contenido.
En esta versión se emite un hallazgo agregado con spans, sesiones y descartes desde el último
sondeo. Para el timeline y el control por herramienta, use el hook.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Se requiere reautenticación reforzada — AAL3 (hardware, resistente al phishing)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Se requiere reautenticación reforzada — AAL3 (hardware, resistente al phishing)">

## Uso por CLI

Los ejemplos siguientes se ejecutaron con el binario del worktree el 30 de agosto de 2026. Se
omiten los mensajes generales de arranque.

### Registrar la fuente local

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --kind grok \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 60 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "grok-demo" (kind "grok", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → grok
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 60
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

En SQLite, pare el motor para una mutación offline del roster o use la consola en vivo. En
PostgreSQL, el comando puede ejecutarse junto al motor. `--actor` y `--reason` dejan atribuida la
modificación de procedencia.

Para rutas no predeterminadas, añada configuraciones explícitas:

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --config config_path=/srv/grok-home/.grok/config.toml \
  --config requirements_path=/etc/grok/requirements.toml \
  --config disabled_hooks_path=/srv/grok-home/.grok/disabled-hooks \
  --config managed_settings_path=/etc/claude-code/managed-settings.json \
  --actor platform-operator \
  --reason grok-paths-for-service-user
```

### Prueba de conectividad y lectura real

La medición reproducible del 30 de agosto de 2026 en el host de capturas registró:

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "grok-demo" (grok): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

Terminó con código `0`. En esa estación había una sesión de Grok activa y
`~/.grok/config.toml` estaba presente; `/etc/grok/requirements.toml` y
`~/.grok/disabled-hooks` estaban ausentes. Nada de ello fue leído por `sources test`: `Open` solo
resuelve la configuración y `test` cierra inmediatamente, sin `Gather`. Por tanto, `ANSWERED` no
prueba la sesión, el sandbox ni los hallazgos. La prueba de lectura es recargar el motor y observar
los findings que produzca el siguiente sondeo.

### Verificar el fail-closed del cliente de hook

Con el endpoint sin configurar:

```sh
printf '%s' '{"hookEventName":"pre_tool_use","sessionId":"s-1","toolName":"Bash"}' | olivares grok-hook
```

Salida estándar:

```json
{"decision":"deny","reason":"no governance endpoint is configured (deny-closed)"}
```

Salida de error:

```text
no governance endpoint is configured (deny-closed)
```

El código de salida es `2`, que Grok interpreta como veto para `pre_tool_use`. En los demás
eventos, una negativa se registra pero no puede impedir el hecho; el cliente lo anuncia en stderr
en vez de simular enforcement.

## Panel de control

| Dónde | Qué se ve | Límite operativo |
|---|---|---|
| **Control console > Connectors** (`/console`) | Roster `grok`, rutas configuradas, intervalo, modo y acciones Test/Save/Reload. | La prueba abre y cierra; no lee los TOML. |
| **Health > Connectors** (`/health`) | Estado de la fuente, mensaje, tendencia y último sondeo. | La salud del proceso no garantiza que un fichero ausente esté gobernado. |
| **Observability > Ingestion** (`/observability`) | Hallazgos emitidos por `olivares.grok`, primer/último registro y, si se habilita, actividad agregada de OTLP. | Contadores globales desde el arranque; se reinician y no son por tenant. |
| **Security** (`/security`) | Perfil de sandbox observado e impuesto, nombres MCP, presencia/validez de requisitos, compatibilidad de managed settings y nombres de hooks desactivados. | “Ilegible” se conserva como desconocido, no como ausencia. |
| **Sessions** (`/sessions`) | Sesión, acción, identidad, modo de permisos, última actividad y postura `enforced` u `observed`. | Requiere eventos del hook. El inventario local no crea una sesión. |
| **Audit** (`/audit`) | Decisiones atribuibles del PEP y evidencia encadenada. | Solo existe para llamadas que llegaron al PEP; un hook desactivado deja un hueco. |

No espere catálogo de modelos, gasto de xAI ni prompts: esta fuente no usa la API de xAI y el
receptor OTLP descarta contenido.

<img class="light:sl-hidden" src="/console/observability-counters-dark.png" alt="Salud de ingesta basada en estándares y desglose de trazas correlacionadas con el libro mayor. Las cifras son de todo el motor (globales del proceso), no por inquilino; los estándares se fijan a las versiones y madureces que declaran los organismos correspondientes.">
<img class="dark:sl-hidden" src="/console/observability-counters-light.png" alt="Salud de ingesta basada en estándares y desglose de trazas correlacionadas con el libro mayor. Las cifras son de todo el motor (globales del proceso), no por inquilino; los estándares se fijan a las versiones y madureces que declaran los organismos correspondientes.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Hallazgos de guardrail, la postura de enforcement, la cola de anomalías y el forense de incidentes con evidencia a prueba de manipulación. El plano es detective por defecto: registra, no bloquea por su cuenta salvo que el enforcement esté habilitado y gobernado.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Hallazgos de guardrail, la postura de enforcement, la cola de anomalías y el forense de incidentes con evidencia a prueba de manipulación. El plano es detective por defecto: registra, no bloquea por su cuenta salvo que el enforcement esté habilitado y gobernado.">

## Uso profesional

- **Baselines de estaciones Linux:** distribuya `requirements.toml` como fichero root-owned y
  sondee cada host. La ausencia se convierte en hallazgo accionable, no en un default verde.
- **Control MCP:** compare los nombres declarados por el usuario con los fijados por el
  administrador. La variable `GROK_CONFIG` no puede añadir tablas peligrosas como MCP, auth o
  egreso; esa protección procede de Grok y Olivares la reporta sin duplicarla.
- **Canario de hooks:** pruebe primero una herramienta inocua y confirme evento, decisión y efecto.
  Después vigile `disabled-hooks` continuamente; el control puede desaparecer por nombre.
- **Estaciones compartidas:** configure rutas absolutas al `HOME` real de la cuenta que ejecuta
  Grok. El `~` del servicio de Olivares puede resolver a otro usuario y producir una medición
  honesta pero del host equivocado.
- **Telemetría mínima:** active OTLP solo si necesita la señal agregada y reserve un socket local
  propio. Para gobierno preventivo, el esfuerzo prioritario es asegurar la ejecución del hook.

## Qué impone y qué solo observa

| Superficie | Resultado real |
|---|---|
| Fuente `grok` | **Observa, solo lectura.** Lee archivos y emite hallazgos; no modifica Grok Build ni llama a xAI. |
| `/etc/grok/requirements.toml` | **Impone en el agente** los valores acotados de sandbox y MCP. Olivares verifica su presencia y efecto declarado. |
| `~/.grok/config.toml` | **Preferencia observada.** No es por sí sola una política administrativa. |
| `olivares grok-hook` en `pre_tool_use` | **Puede impedir la herramienta** cuando el comando corre y responde con exit `2`. El cliente niega cerrado si el PEP falta o falla. |
| Otros eventos de Grok | **Observa.** La negativa queda como evidencia, pero el evento no ofrece veto equivalente. |
| Timeout, crash o hook que no llega a ejecutarse | **Fail-open del agente.** Grok continúa; el fail-closed interno de `olivares grok-hook` solo sirve si el proceso fue invocado. |
| `~/.grok/disabled-hooks` | **Puede apagar incluso un hook administrado.** Olivares lo detecta después, pero ninguna capa de requisitos lo impide. |
| Receptor OTLP | **Observa agregados.** No autentica, no conserva contenido y no sustituye el timeline del hook. |

Una implantación no debe declararse “enforced” solo porque el sandbox esté fijado. El cierre exige
requirements efectivos, hook realmente ejecutado, ausencia vigilada de su nombre en
`disabled-hooks`, evento visible y una prueba de veto `pre_tool_use`.
