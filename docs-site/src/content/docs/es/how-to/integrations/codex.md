---
title: Integrar Codex
description: >-
  Incorpora Codex al plano de control de gobierno: el conector, la managed config,
  el hook gobernado y lo que muestra la consola una vez en funcionamiento.
---

Olivares AI integra Codex en tres planos complementarios. La fuente `codex` lee, en modo
solo lectura, Analytics, Compliance, Audit Logs y coste facturado mediante credenciales de
automatización enterprise. El conector `codex-managed-config` inventaría y comprueba la política
de sistema desplegada. Finalmente, `olivares codex-hook` lleva las sesiones y las decisiones de
herramienta al PEP local. Una sesión iniciada con una suscripción personal de ChatGPT no concede
por sí sola acceso a las APIs enterprise.

## Agregar Codex

### Requisitos previos

- Un tenant empresarial de Olivares AI y una cuenta superadmin con AAL3 para operar el roster.
- Para ingestión enterprise, una clave de API de plataforma o un access token de workspace con
  los alcances de lectura correspondientes, además del `workspace_id`. El login de la CLI de
  Codex mediante ChatGPT no es una credencial del conector.
- Acceso administrativo a la capa de sistema del host para distribuir
  `/etc/codex/requirements.toml`, `/etc/codex/managed_config.toml` y el hook confiable.
- Un socket de loopback dedicado para el PEP de Codex. Su valor predeterminado es
  `127.0.0.1:8448`; no lo comparta con Claude o Grok porque cada agente interpreta una forma de
  respuesta distinta.

1. Entre en **Control console** (`/console`) y abra **Connectors**.
2. Añada una fuente de tipo `codex`, un nombre estable, el tenant y un intervalo por lotes. Un
   valor de `300` segundos es un punto de partida razonable para un piloto; ajuste la frecuencia
   al presupuesto de las APIs y al objetivo de frescura.
3. Para un alta enterprise, introduzca la credencial en el campo secreto `api_key`, seleccione
   `auth_mode` (`api_key` o `access_token`) y complete `workspace_id`. La consola sella el valor y
   nunca lo devuelve. Guarde, pruebe y recargue.

También se puede añadir `codex` sin credencial para inventario local del catálogo. Ese modo no
consulta Analytics, Compliance, Audit Logs ni Costs y `Gather` no emite observaciones remotas.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Configura quién entra y qué puede administrar: alta de usuarios, conexión de SSO y gestión de workspaces y grupos de agentes.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Configura quién entra y qué puede administrar: alta de usuarios, conexión de SSO y gestión de workspaces y grupos de agentes.">

## Configurar Codex

### 1. Fuente enterprise de solo lectura

Los ajustes que definen la cobertura son:

| Ajuste | Predeterminado | Uso |
|---|---:|---|
| `api_key` | vacío | Referencia a una credencial de automatización. Vacío activa solo el catálogo offline. |
| `auth_mode` | `api_key` | Identifica si la credencial es `api_key` o `access_token`; ambas viajan como Bearer. |
| `workspace_id` | vacío | Obligatorio para Analytics y Compliance, que son por workspace. |
| `analytics` | `true` | Uso y adopción de Codex; produce muestras y hallazgos estructurales. |
| `compliance` | `true` | Logs de Compliance de Codex como evidencia de actividad. |
| `audit` | `true` | Audit Logs de organización como evidencia. |
| `costs` | `false` | Coste facturado diario. Habilítelo junto con `project_id` para no atribuir gasto ajeno a Codex. |
| `attribute_email` | `false` | Mantiene `user_id` como actor estable y evita usar correo como PII de atribución. |
| `compliance_prompt_scan` | `false` | Si se activa, analiza transitoriamente patrones de riesgo y solo conserva hallazgos estructurales. |
| `otlp_http` | `false` | Receptor experimental de logs, apagado porque abre un puerto. Actualmente cuenta y drena eventos; no los convierte en sesiones. |

Mantenga `otlp_http` desactivado para la integración inicial. El plano de sesiones completo es el
hook gobernado; habilitar OTLP en esta versión no sustituye esa instalación.

En CLI, guarde la credencial fuera del historial y referénciela por nombre:

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name codex/enterprise \
  --value-file /run/secrets/codex-enterprise-token \
  --actor platform-operator \
  --reason codex-enterprise-onboarding

olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-enterprise \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --config api_key=store:codex/enterprise \
  --config auth_mode=access_token \
  --config workspace_id=ws_eng \
  --actor platform-operator \
  --reason codex-enterprise-onboarding
```

Si habilita `costs=true`, añada también `project_id=<project-id>`. La API de costes es de ámbito
organizativo cuando no se restringe y puede mezclar gasto que no procede de Codex.

### 2. Requisitos de sistema y valores administrados

Olivares separa correctamente dos capas:

- `requirements.toml` contiene restricciones que el usuario no puede ampliar: políticas de
  aprobación, modos de sandbox, búsqueda web, control remoto, confianza de hooks, lecturas
  prohibidas y servidores MCP permitidos.
- `managed_config.toml` contiene valores iniciales administrados. Son defaults; una restricción
  que deba ser inamovible pertenece a `requirements.toml`.

Este documento de política es válido y mantiene red, búsqueda, control remoto y MCP cerrados por
defecto, con escritura limitada al workspace:

```json
{
  "requirements": {
    "allowed_approval_policies": ["untrusted", "on-request"],
    "allowed_sandbox_modes": ["read-only", "workspace-write"],
    "allowed_web_search_modes": [],
    "allow_remote_control": false,
    "allow_managed_hooks_only": true,
    "deny_read": ["~/.ssh"],
    "allowed_mcp_servers": []
  },
  "managed_config": {
    "approval_policy": "on-request",
    "sandbox_mode": "workspace-write",
    "web_search": "disabled",
    "network_access": false
  }
}
```

Valide antes de distribuir y genere los dos artefactos con el mismo comando:

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate

olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --requirements-out /etc/codex/requirements.toml \
  --managed-config-out /etc/codex/managed_config.toml
```

El render falla antes de escribir si la política contiene un enum desconocido, un MCP sin
identidad o TOML inválido. Para verificar posteriormente el estado vivo y el drift, registre una
fuente adicional de tipo `codex-managed-config`; lee ambos ficheros de sistema y no los modifica.

### 3. Hook de sesión y PEP

Codex lee el hook medido en `$CODEX_HOME/hooks.json`. `command` debe ser una cadena, no un array:
un array puede parsear sin que el hook llegue a ejecutarse. La tabla `[hooks]` inline de
`config.toml` tampoco fue leída por la versión medida.

```json
{
  "description": "olivares governed hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ]
  }
}
```

El servidor se monta al arrancar Olivares cuando
`OLIVARES_CODEX_HOOK_PEP_CONFIG` apunta a un JSON válido:

```json
{
  "listen": "127.0.0.1:8448",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Una instancia gobierna un tenant y la decisión procede del PDP ya configurado en Olivares. El
cliente usa `OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT`, `OLIVARES_CODEX_HOOK_AGENT`, `OLIVARES_CODEX_HOOK_ORG` y
`OLIVARES_CODEX_HOOK_ACCOUNT`. Entregue esos valores mediante el gestor de procesos y secretos;
no los incruste en `hooks.json`.

`allow_managed_hooks_only=true` es imprescindible para presentar el hook como control de flota.
Sin confianza, Codex puede omitir un hook sin evento ni aviso: una instalación silenciosa no es
evidencia de enforcement.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Se requiere reautenticación reforzada — AAL3 (hardware, resistente al phishing)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Se requiere reautenticación reforzada — AAL3 (hardware, resistente al phishing)">

## Uso por CLI

Los ejemplos de salida se midieron el 30 de agosto de 2026. Los logs generales de arranque se
omiten para conservar solo la respuesta del comando.

### Alta offline reproducible

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "codex-demo" (kind "codex", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → codex
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 300
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

En SQLite, ejecute las mutaciones offline con el motor detenido; en PostgreSQL pueden convivir
con el motor. La consola es la vía recomendada para cambios en vivo sobre SQLite.

### Prueba de conectividad y su límite

La medición reproducible del 30 de agosto de 2026 en el host de capturas registró este resultado:

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "codex-demo" (codex): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

El proceso terminó con código `0`. La estación tenía la CLI de Codex autenticada con ChatGPT,
pero `codex-demo` no tenía `api_key`: esta respuesta prueba el catálogo offline y que `Open`
aceptó la configuración. No prueba autenticación contra OpenAI, no llama a `Gather` y no lee una
sola fila de Analytics o Compliance. Incluso con credencial, `sources test` no realiza una
petición upstream porque `Open` solo construye los clientes; la primera prueba de datos es un
sondeo real del motor seguido de observaciones visibles.

### Validar la política administrada

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate
```

```text
ok: policy renders to valid Codex managed-config TOML
```

### Probar la negativa local del hook

Con el endpoint intencionadamente ausente:

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-1"}' | olivares codex-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed endpoint not configured (deny-closed)"}}
```

La salida tiene código `0` porque el veto viaja en el JSON que Codex interpreta. Esta sonda
verifica el cliente fail-closed; la aceptación de un `PreToolUse` por Codex debe probarse también
en un host donde el hook esté marcado como confiable.

## Panel de control

| Dónde | Qué se ve | Condición para que aparezca |
|---|---|---|
| **Control console > Connectors** (`/console`) | Fuente, modo, frecuencia, configuración no secreta y acciones Test/Save/Reload. | El alta persistida aparece de inmediato; los datos no. |
| **Health > Connectors** (`/health`) | Estado del conector, mensaje, tendencia y última actividad. | Tras recargar el roster. |
| **Observability > Ingestion** (`/observability`) | Contadores por `olivares.codex`, tipos de observación y primera/última recepción. | Después de que `Gather` emita datos. Son contadores globales desde el arranque y se reinician. |
| **Cost & FinOps** (`/finops`) | Uso estimado de Analytics y, si se habilita, coste diario facturado. | Credencial válida, `workspace_id` y APIs autorizadas; `costs` requiere su opt-in. |
| **Security** (`/security`) | Hallazgos de adopción, superficies enterprise no disponibles y análisis estructural opt-in de Compliance. | Después de una recogida; los 403/404 de superficies enterprise se materializan como postura, no como éxito. |
| **Sessions** (`/sessions`) | Sesiones y timeline con acción, modelo, identidad, coste y postura. | Procede del hook gobernado. La fuente batch por sí sola no crea la sesión en vivo. |
| **Audit** (`/audit`) | Evidencia de actividad importada y decisiones del PEP ancladas en el ledger. | Una vez recibidos logs o decisiones atribuibles. |

No use el catálogo offline como prueba de que el panel de modelos contiene inventario remoto: el
conector ofrece un catálogo al runtime, pero en este árbol no hay un consumidor de módulo que lo
publique en esa pantalla.

<img class="light:sl-hidden" src="/console/health-dark.png" alt="Liveness, fiabilidad y dependencias de tu infraestructura — derivado de la actividad observada y del barrido de inactividad, nunca sondeando la infraestructura.">
<img class="dark:sl-hidden" src="/console/health-light.png" alt="Liveness, fiabilidad y dependencias de tu infraestructura — derivado de la actividad observada y del barrido de inactividad, nunca sondeando la infraestructura.">
<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Coste de tokens en todo el estate — tendencias, imputación, conciliación, presupuestos y previsión. Las cifras son tal cual las reporta el ledger de FinOps.">
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Coste de tokens en todo el estate — tendencias, imputación, conciliación, presupuestos y previsión. Las cifras son tal cual las reporta el ledger de FinOps.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Hallazgos de guardrail, la postura de enforcement, la cola de anomalías y el forense de incidentes con evidencia a prueba de manipulación. El plano es detective por defecto: registra, no bloquea por su cuenta salvo que el enforcement esté habilitado y gobernado.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Hallazgos de guardrail, la postura de enforcement, la cola de anomalías y el forense de incidentes con evidencia a prueba de manipulación. El plano es detective por defecto: registra, no bloquea por su cuenta salvo que el enforcement esté habilitado y gobernado.">

## Uso profesional

- **Piloto sin credencial:** valide empaquetado y roster con `codex-demo`, pero etiquételo como
  catálogo offline. No lo use como semáforo de conectividad enterprise.
- **Ingestión de gobierno:** emplee una identidad de automatización de solo lectura y el mínimo
  conjunto de APIs. Mantenga `attribute_email=false` salvo necesidad de chargeback aprobada.
- **Control de estación:** genere los TOML desde una política versionada, distribúyalos mediante
  el sistema de configuración de la flota y sondee el estado con `codex-managed-config` para
  distinguir intención, despliegue y drift.
- **Control de sesión:** instale primero hooks en un grupo canario, compruebe que `PreToolUse`
  bloquea una acción sin efectos y, solo entonces, amplíe el anillo. Un hook que no produjo un
  evento no debe contabilizarse como gobernado.
- **FinOps preciso:** habilite `costs` únicamente cuando `project_id` delimite gasto Codex. Use
  Analytics para adopción y la API de Costs para el importe facturado; no los sume como si fueran
  dos facturas.

## Qué impone y qué solo observa

| Superficie | Resultado real |
|---|---|
| Fuente `codex` y APIs enterprise | **Observa, solo lectura.** No cambia configuración de OpenAI ni intercepta inferencias. |
| Modo sin `api_key` | **Catálogo offline.** No prueba la suscripción ChatGPT, la API remota ni el workspace. |
| `requirements.toml` | **Impone restricciones del sistema** que el usuario no puede ampliar, incluida la confianza exclusiva en hooks administrados. |
| `managed_config.toml` | **Establece defaults administrados.** No sustituye una restricción de `requirements.toml`. |
| `codex-managed-config` | **Observa y compara drift.** Nunca corrige los ficheros del host. |
| `olivares codex-hook` en `PreToolUse` o `PermissionRequest` | **Puede impedir la acción.** Codex no acepta un `permissionDecision=allow`; Olivares representa allow como no interferencia y una solicitud `ask` se traduce a negativa. |
| `PostToolUse` y eventos de ciclo de vida | **Evidencia con capacidad desigual.** Un bloqueo posterior no deshace una herramienta ya ejecutada y `SessionEnd` no tiene salida de veto. |
| Receptor OTLP de Codex | **Recepción parcial en esta versión.** Cuenta y drena eventos; todavía no los transforma en sesiones ni hallazgos. |

El criterio de cierre es acumulativo: fuente recargada, primer `Gather` con datos enterprise,
política de sistema verificada, hook confiable observado y veto real de `PreToolUse`. `ANSWERED`
solo cubre la primera parte de `Open`.
