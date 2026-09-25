---
title: "Tu primera hora con Olivares AI"
description: >-
  Instala el plano de control, abre la consola, conecta un agente de código,
  ejecuta una sesión gobernada que permite una acción y deniega otra, y lee
  la evidencia. Tres formas: local (SQLite), equipo (Postgres y Docker), híbrida.
---

Esta página es la primera hora **en este árbol**. No es una maqueta. Cada
comando de la forma local es el que ejecuta `task smoke:first-hour` contra
`./bin/olivares`. Si una forma necesita Docker o Postgres, la página lo dice.

El producto imprime el siguiente paso. `olivares quickstart` nombra el
registro de passkey, `olivares agent tool detect`, el inventario, el PEP de
hooks y `olivares doctor` después del token de configuración.
`olivares doctor` informa `first-hour-coding-agent`, `first-hour-hook-pep` y
`first-hour-next-step`. Esas comprobaciones son opcionales. No marcan como
fallida una instalación sana.

## Qué es esta hora

Instalar → consola → conectar **un** agente de código → verlo en el
inventario → sesión gobernada que **permite una acción y deniega otra** →
leer la evidencia.

Esta página usa el **PEP de hooks de Claude Code** que este árbol ya incluye
(`olivares claude-hook`). Lanzar un CLI oficial como proceso de sesión es la
costura del runtime Community. Esta hora no duplica ese controlador. No envía un
turno de modelo.

`--seed-demo` no es esta hora.

## Forma 1 — Local (SQLite, esta caja)

Este contenedor **no tiene Docker**. PostgreSQL **no está en marcha**. La
forma local usa SQLite embebido y HTTP en loopback. Esa es la forma que
reproduce la prueba de humo.

### 1. Compilar y arrancar

```bash
task build
./bin/olivares version
DATA="$(mktemp -d)"
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$DATA"
```

La prueba de humo usa `serve --insecure` para que `curl` no tenga que
confiar un certificado autofirmado, y pasa `--listen 127.0.0.1:8443` de forma
explícita: ese bind en loopback es de la prueba de humo, no el valor
predeterminado del producto. Un operador interactivo ejecuta
`olivares quickstart` (TLS activado, ninguna credencial predeterminada, un
token de configuración de un solo uso). Su dirección de escucha predeterminada
es `:8443` — **todas las interfaces**, porque esto es un servidor
(`cmd/olivares/binddefaults.go`); pasa `--listen 127.0.0.1:8443` para
restringirla a esta máquina. Medido en esta caja el 2026-09-17,
`quickstart --quiet` imprimió el token en **3 s**.

El panel de bienvenida imprime:

```text
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

Para el bind comodín predeterminado, el banner imprime
`https://localhost:8443` y lista debajo del token todas las demás direcciones
en las que responde este host: sirven para llegar a la consola desde otra
máquina. Si el banner ya se ha ido de la pantalla, `olivares first-boot`
vuelve a imprimir la dirección o direcciones de la consola y el estado de la
configuración inicial. Abre `https://localhost:PORT` antes de registrar una
passkey: el navegador rechaza una IP como relying party de WebAuthn, y el
producto ya lo dice en el aviso de dirección
(`cmd/olivares/consoleaddr.go`).

### 2. Crear el administrador y el tenant

Un solo comando completa la configuración contra el motor en marcha, y es el que
imprime el panel de arranque. El token se lee de la entrada estándar para que nunca
aparezca en la tabla de procesos:

```bash
# pega el token olst_… que el motor imprimió al arrancar
olivares auth bootstrap --server https://127.0.0.1:8443 \
  --ca-cert <data-dir>/tls.crt \
  --setup-token-file - \
  --email admin@local --password-file ./admin.pw \
  --organization "First hour" --save-context
```

`--save-context` inicia sesión y guarda la sesión, de modo que el siguiente comando ya
está autenticado. Medido el 2026-09-18 sobre un directorio de datos limpio: 263 ms.

Lo mismo por la API, si programas directamente contra los endpoints:

```bash
BASE=http://127.0.0.1:8443
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
TENANT=$(curl -sf -X POST "$BASE/v1/system/orgs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')
```

### 3. Conectar un agente de código y verlo en el inventario

```bash
./bin/olivares agent tool detect -o json
curl -sf -X POST "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}'
curl -sf "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares agent managed-settings --out ./managed-settings.json
```

`POST /v1/agents` **no** exige AAL3. Crear fuentes, conectores, espacios de
trabajo y secretos **sí**.

### 4. Sesión gobernada: permitir Read, denegar Bash

Reinicia con `OLIVARES_HOOK_PEP_CONFIG` y una política deny-closed (Read
allow, Bash deny). Luego:

```bash
export OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/
export OLIVARES_HOOK_PEP_TOKEN="$TOKEN"
export OLIVARES_HOOK_PEP_TENANT="$TENANT"
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' \
  | ./bin/olivares claude-hook
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | ./bin/olivares claude-hook
```

### 5. Leer la evidencia

```bash
curl -sf "$BASE/v1/audit?action=hook.tool.allow&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
curl -sf "$BASE/v1/audit?action=hook.tool.deny&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares doctor --mode user
task smoke:first-hour
```

## Forma 2 — Equipo (Postgres y Docker)

Este contenedor **no tiene Docker**. PostgreSQL **no está en marcha** aquí.
No trates los comandos siguientes como medidos en esta caja.

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
```

Sin el pool `olivares_admin`, `POST /v1/setup` responde
`501 cross_tenant_admin_pool_not_configured`. Esa negativa es intencionada.
Véase [Desplegar con Docker](/how-to/docker-deployment/).

## Forma 3 — Híbrida (plano local, agente en una estación)

Mantén el plano de control en SQLite local. Ejecuta el agente en una
estación que ya tenga `claude`. El plano no necesita el CLI oficial en el
mismo host. Una sesión oficial en vivo (PTY) es del runtime Community, no de esta página.

## El muro AAL3 (sigue siendo cierto)

Crear fuentes, conectores, espacios de trabajo y secretos se **rechaza**
hasta AAL3 (`requireAAL3`). PIV/CAC responde **501** en una instalación
estándar. Abre `https://localhost:PORT` e **Identity → Privileged login**.

## 3. Iniciar una sesión de Claude Code desde la consola

Sin una fuente de credencial de inferencia, los lanzamientos stream-json
están deny-closed:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go`). Fija **una** de
`OLIVARES_SESSION_RUNTIME_WIF` o `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`.
A partir de v26.10, pendiente de publicar, esa ya no será la única vía: registra la credencial en la consola
y vincúlala a un perfil. Véase
[Añadir un proveedor y lanzar un agente](/es/how-to/add-a-provider/) y
[Operar una sesión de proveedor](/how-to/operate-provider-sessions/).

## Relacionado

- [Honestidad y límites](/start/honesty-and-limits/)
- [Autoalojar Olivares AI](/how-to/self-hosting/)
- [Operar una sesión de proveedor](/how-to/operate-provider-sessions/)
- [PEP de hooks de Claude Code](/how-to/connectors/claude-code-hooks-pep/)
- [Desplegar con Docker](/how-to/docker-deployment/)
