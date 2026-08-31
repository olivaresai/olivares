<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Integrar Claude Code con Olivares AI

> Borrador editorial para la documentación pública. Las capturas pendientes se señalan de
> forma explícita para que el equipo de publicación las produzca desde el estate de demostración.

Esta integración incorpora Claude Code al plano de gobierno sin convertir Olivares AI en un
proxy obligatorio. El conector `claude` recibe telemetría OTLP y eventos de hooks, correlaciona
sesiones y materializa accesos R/RW, costes y hallazgos. Cuando se necesita control preventivo,
el hook administrado `olivares claude-hook` consulta el PEP local de Olivares antes de cada uso
de herramienta. Ambos planos son independientes: recibir telemetría no implica estar imponiendo
política.

## Agregar Claude Code

### Requisitos previos

- Un binario de Olivares AI que incluya el conector de primera parte `claude`.
- El identificador UUID del tenant empresarial al que se atribuirán las observaciones.
- Claude Code instalado en los equipos que se van a gobernar. El receptor local no necesita una
  clave de la API de Anthropic.
- Conectividad local desde Claude Code al receptor de Olivares. Los valores predeterminados son
  `127.0.0.1:4317` para OTLP/gRPC y `127.0.0.1:4318` para OTLP/HTTP y hooks cooperativos.
- Una ruta temporal ejecutable para el servicio de Olivares. `claude` se ejecuta como plugin
  aislado; en sistemas con `/tmp` montado con `noexec`, configure `TMPDIR` en la unidad del
  servicio hacia un directorio dedicado, propiedad del usuario de Olivares.

No exponga los receptores OTLP o el endpoint cooperativo fuera de loopback. No autentican al
emisor y una máquina alcanzable podría fabricar telemetría. El PEP gobernado es otra superficie:
usa su propio socket local, autentica cada petición y registra la decisión.

1. Entre en **Control console** (`/console`) y abra la pestaña **Connectors**. El roster de
   conectores es global: se necesita una cuenta superadmin; guardar, probar y recargar requieren
   elevación AAL3.
2. Añada una fuente con tipo `claude`, un nombre operativo estable —por ejemplo,
   `claude-code-prod`—, el tenant correspondiente, modo `live`, intervalo `0` y estado habilitado.
   Un intervalo cero es correcto: este conector mantiene receptores, no realiza sondeos por lotes.
3. Guarde y ejecute **Reload**. La fila confirma nombre, tipo, modo y estado. El botón de prueba
   de la consola no está disponible para `claude` porque es un conector fuera de proceso; la
   validación ocurre al guardar y la prueba completa se hace con `olivares sources test`, que sí
   lanza el plugin.

[CAPTURA: alta de `claude-code-prod` en Control console > Connectors, con tipo `claude`, modo `live`, intervalo 0 y estado habilitado; variantes clara y oscura del estate sembrado.]

## Configurar Claude Code

Hay dos configuraciones que conviene distribuir juntas: la fuente de observación y la política
administrada del agente.

### 1. Receptor y minimización de datos

La configuración segura inicial es la predeterminada:

| Ajuste de la fuente | Valor inicial | Efecto |
|---|---:|---|
| `enable_grpc` | `true` | Sirve OTLP/gRPC en `grpc_addr` (`127.0.0.1:4317`). |
| `enable_http` | `true` | Sirve OTLP/HTTP y el hook cooperativo en `http_addr` (`127.0.0.1:4318`). |
| `hook_path` | `/hooks` | Ruta del hook cooperativo dentro del receptor HTTP. |
| `content_capture` | vacío | Conserva estructura, no prompts, cuerpos de herramientas ni cuerpos de API. El razonamiento extendido siempre se redacta. |
| `enforcement` | vacío | Observa hooks; no devuelve decisiones preventivas desde esta fuente. |
| `allow_public_bind` | `false` | Rechaza el bind fuera de loopback. |

Si hay varios receptores OTLP en el mismo host, asigne a cada uno una dirección de loopback
distinta y use el mismo valor en el agente. Claude, Codex y Grok comparten `4318` como valor
predeterminado en algunas modalidades y no pueden reservar el mismo socket simultáneamente.

### 2. Managed settings y PEP gobernado

Genere el fichero de sistema de Claude Code con el propio binario de Olivares:

```sh
olivares agent managed-settings \
  --otel-endpoint http://127.0.0.1:4317 \
  --out /etc/claude-code/managed-settings.json
```

El generador instala `allowManagedHooksOnly: true`, un `PreToolUse` que ejecuta
`olivares claude-hook`, y el `PostToolUse` de redacción. También activa OTLP con protocolo
`grpc`; por eso el endpoint anterior usa el receptor `4317`, no el receptor HTTP `4318`.
El fichero vive en la capa administrada del sistema y no en el `HOME` de la sesión.

El servidor PEP se habilita al arrancar Olivares mediante un fichero indicado por
`OLIVARES_HOOK_PEP_CONFIG`. Esta es una política de ejemplo válida para un tenant:

```json
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "11111111-1111-4111-8111-111111111111",
      "require_firm_identity": true,
      "enforcement": "enforce",
      "policy": {
        "version": "claude-prod-v1",
        "default": "allow",
        "rules": [
          {
            "tool": "Bash",
            "decision": "ask",
            "reason": "Los comandos de shell requieren confirmación humana"
          }
        ]
      }
    }
  ]
}
```

Las sesiones lanzadas por Olivares reciben de forma efímera
`OLIVARES_HOOK_PEP_URL`, `OLIVARES_HOOK_PEP_TOKEN`, `OLIVARES_HOOK_PEP_TENANT` y la atribución
del agente. En un lanzamiento independiente, el operador debe suministrar esos valores por su
canal de secretos; no los escriba en `managed-settings.json`. Si el endpoint falta o no responde,
`olivares claude-hook` devuelve `deny`.

Para desplegar primero sin bloqueo, use el modo `observe` con un `observe_until` RFC3339 futuro.
Ese permiso es temporal; una fecha ausente, inválida o vencida resuelve a `enforce`. Las
invariantes de plataforma —identidad, tenant, kill switch, firewall y errores fail-closed— siguen
imponiéndose incluso durante la observación de reglas de negocio.

[CAPTURA: configuración de Claude Code con el receptor OTLP en loopback, captura de contenido estructural y managed settings con hook administrado; variantes clara y oscura, sin mostrar secretos.]

## Uso por CLI

Los fragmentos de salida siguientes se midieron con el binario construido desde este worktree el
30 de agosto de 2026. Se omiten los mensajes generales de arranque del motor.

### Registrar la fuente

En SQLite, detenga el motor antes de mutar el roster por CLI: es un perfil de escritor único. Con
PostgreSQL puede ejecutar la operación junto al motor. Para cambios en vivo sobre SQLite, use la
consola.

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --kind claude \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 0 \
  --config mode=live \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "claude-code-prod" (kind "claude", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → claude
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 0
  enabled: - → true
  config.mode: - → live
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

`--actor` y `--reason` son obligatorios porque el cambio altera la procedencia de datos y queda
registrado en el ledger de auditoría.

### Validar y abrir el conector

```sh
olivares sources validate \
  --data-dir /var/lib/olivares \
  --name claude-code-prod

olivares sources test \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --timeout 20s
```

```text
source "claude-code-prod"
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
source "claude-code-prod" (claude): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

`validate` no abre sockets. `test` ejecuta `Open` y `Close`, pero no llama a `Gather`, no conecta
la fuente al motor y no demuestra que Claude Code ya esté enviando telemetría. Si el plugin falla
con `permission denied` aun teniendo bit de ejecución, revise si el `TMPDIR` del proceso está en
un volumen `noexec`.

### Comprobar el comportamiento fail-closed del hook

Con el endpoint deliberadamente sin configurar, el cliente produce una negativa en el formato
que Claude Code entiende:

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed PEP endpoint not configured (deny-closed)"}}
```

Esta sonda comprueba el cliente local, no una decisión de política remota. En producción, pruebe
además una regla permitida, una regla denegada y una solicitud `ask` con identidad firme antes de
ampliar el despliegue.

## Panel de control

Una fuente añadida no crea datos históricos por sí sola. Después de recargar y recibir el primer
evento, el operador encuentra:

| Dónde | Qué se ve | Cómo interpretar el estado |
|---|---|---|
| **Control console > Connectors** (`/console`) | Nombre, tipo `claude`, modo, configuración no secreta y estado del roster; acciones de guardar y recargar. | “Guardado” prueba persistencia. No prueba que haya llegado un evento. |
| **Health > Connectors** (`/health`) | Salud del conector, mensaje operativo, tendencia y último sondeo/actividad conocidos. | Un receptor abierto puede estar sano aunque el agente todavía esté silencioso. |
| **Observability > Ingestion** (`/observability`) | Registros por fuente, tipos `edge`, `cost` y `finding`, señal, primer y último evento. | Son contadores globales del proceso desde el arranque; se reinician al reiniciar y no son una vista por tenant. |
| **Sessions** (`/sessions`) | Sesión, estado, acción, modelo, tokens, coste, última actividad y postura `enforced` u `observed`. | La postura resume la evidencia de eventos; no se deduce del mero alta del conector. |
| **Access map** (`/access-map`) | Aristas R/RW atribuidas desde herramientas, MCP y recursos observados. | Una arista observada describe actividad; no equivale a una autorización previa. |
| **Cost & FinOps** (`/finops`) | Muestras de coste y tokens derivadas de la telemetría recibida. | Solo cubre lo exportado por la flota; no reconstruye llamadas que nunca emitieron OTLP. |
| **Security** (`/security`) | Brechas de telemetría, postura de sandbox/MCP y otros hallazgos emitidos. | Un hallazgo ausente no convierte una superficie no observada en conforme. |
| **Claude Policy** (`/claude-policy`) | Autoría, distribución, versiones y check-in de las superficies administradas de Claude Code. | La distribución y la verificación de drift son hechos distintos y se muestran por separado. |

[CAPTURA: vista de una sesión Claude Code activa en `/sessions`, con postura, timeline y enlaces a Access map, FinOps y Security; variantes clara y oscura del estate sembrado.]

## Uso profesional

- **Despliegue gradual:** empiece con contenido estructural y reglas en modo observado con fecha
  de caducidad; revise falsos positivos, después promocione a `enforce` por tenant.
- **Administración de flota:** distribuya `/etc/claude-code/managed-settings.json` mediante RPM,
  imagen inmutable, Ansible, Salt o el gestor corporativo equivalente. Compruebe el fichero vivo
  con una segunda fuente de tipo `managed-settings` para detectar ausencia o drift.
- **Separación de funciones:** el equipo de plataforma mantiene receptores y disponibilidad; el
  equipo de seguridad versiona reglas; los propietarios del tenant revisan solicitudes `ask` y
  hallazgos. Todas las mutaciones privilegiadas quedan atribuidas.
- **Mínimo dato:** mantenga `content_capture` vacío salvo que exista una necesidad forense
  aprobada, con residencia y retención definidas. Para adopción y coste suelen bastar los datos
  estructurales.
- **Hosts endurecidos:** preserve loopback, un directorio temporal ejecutable mínimo para el
  plugin y permisos de solo lectura sobre la política. No relaje `noexec` globalmente para resolver
  el arranque del conector.

## Qué impone y qué solo observa

| Superficie | Resultado real |
|---|---|
| Telemetría OTLP y hook cooperativo del conector `claude` | **Observa.** El emisor coopera; el receptor loopback no autentica y una señal puede faltar o ser fabricada por un proceso local. |
| `enforcement` vacío en la fuente | **Observa.** Es el valor predeterminado y no bloquea herramientas. |
| `olivares claude-hook` + PEP + managed settings | **Impone** `allow`, `ask` o `deny` en eventos que Claude Code puede vetar, y registra la decisión. Un fallo del endpoint se niega cerrado. |
| `allowManagedHooksOnly` en la capa administrada | **Endurece la instalación** frente a hooks de usuario o proyecto que compitan con el PEP. |
| `PostToolUse` | **Observa y redacta después del acto.** No puede deshacer efectos que la herramienta ya produjo. |
| Acciones fuera del proceso/hook de Claude Code | **No quedan cubiertas por este wire.** Use controles del sistema operativo, auditoría nativa y políticas de red como respaldo. |

La comprobación operativa completa requiere cuatro pruebas diferentes: roster persistido,
conector abierto, evento visible en **Ingestion**, y una herramienta realmente bloqueada por el
PEP. Ninguna de ellas sustituye a las otras tres.
