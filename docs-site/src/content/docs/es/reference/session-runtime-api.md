---
title: API de runtime de sesión (CLI oficiales)
description: >-
  Superficie HTTP Community que lista, adjunta, alimenta y detiene procesos
  propios de Claude Code, Codex y Grok CLI. Permisos, PTY, reanudación y reconexión.
---

El plano de control **lanza la CLI del proveedor**. No sustituye Claude Code,
Codex ni Grok Build. Las sesiones y los terminales son un módulo de este
producto, no el producto.

Esta página documenta las rutas Community de operate bajo `/v1/m/sessions/runs`.
Ya existen en el [documento OpenAPI beta](/reference/api-beta/). v26.10 añade
el contrato de driver, un runner PTY local y los recorridos J01–J08 como tests.

## Corte de edición

| Edición | Qué hace | Qué no hace |
|---|---|---|
| **Community (esta página)** | Hijo local propio: lanzar, stdin/stdout/stderr, attach con cursor, reanudar la conversación exacta, reconectar un stream vivo, detener con código de salida observado. Las filas de sesión y la evidencia siguen en el módulo II. | Motor Identity & Scale de varios paneles, listener mTLS, sesiones de entrada comerciales, chunk xterm |
| **Overlay Identity & Scale** | Motor comercial session-cockpit (listener, paneles, ledger). Las rutas viven bajo `/v1/m/session-cockpit/` cuando el add-on está presente. | No sustituye `/v1/m/sessions/runs` |

Una build Community responde el namespace del overlay por **ausencia** (404).
No monta un stub 501.

La ruta del hook de Claude Code sigue siendo `olivares claude-hook` (PEP
PreToolUse). Es observación y enforcement, no una CLI de recambio.

## Permisos

Enforcement existente en las rutas del módulo:

| Permiso | Rutas |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

Un viewer puede listar. Crear, input y stop exigen write. El autorizador de la
ruta es el punto de enforcement; un permiso ausente es 403.

## Rutas que lee la consola

Base: `/v1/m/sessions`. Autentica. Envía `X-Olivares-Tenant`.

| Método | Ruta | Resultado |
|---|---|---|
| `GET` | `/runs` | Página de ejecuciones gestionadas |
| `GET` | `/runs/{ref}` | Una ejecución. `state` se deriva. `exit_code` se observa. No hay campo de éxito fabricado |
| `GET` | `/runs/{ref}/events` | Filas de evidencia de ciclo de vida |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: marcos `output`, `lag` si el anillo desalojó por debajo del cursor, `end` o un `notice` de no vivo |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json usa `line`/`message`. Codex/Grok usan `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM y luego SIGKILL del grupo. Código de salida observado en la fila |
| `POST` | `/runs/{ref}/resume` | Nueva generación de proceso. Conversación almacenada exacta |
| `POST` | `/runs/{ref}/interrupt` | Cancela el turno activo. El proceso permanece |

Reconectar tras un attach cortado es `GET …/attach?from={last+1}` sobre el
**mismo** proceso vivo. Tras pérdida de proceso, attach dice que no está vivo.
Resume arranca una generación nueva. Reconnect no inventa un proceso de
recambio (SDD R04).

## Transporte (Community)

Cada CLI oficial se lanza en el transporte que necesita su propia forma de
operate, y `cliruntime.LaunchTransport` declara cuál. Hoy las tres formas son
protocolos stdio, así que el composition root cablea
`sessions.NewProcRunner()`: stdin, stdout y stderr son tuberías y siguen siendo
flujos distintos.

**Claude Code rechaza un terminal en stdin.** Su forma `--print` con
stream-json responde `Error: Input must be provided either through stdin or as
a prompt argument when using --print` y sale 1 sin un solo marco de protocolo.
Un terminal es lo que necesita una forma interactiva; `sessions.NewPTYRunner()`
sigue disponible en Linux para eso. El aislamiento container y sandbox se
rechaza en ambos.

El contrato de driver vive en `modules/sessions/cliruntime`. Clases: `claude`,
`codex`, `grok`. La conformidad corre siempre contra un fake en proceso y
contra un peer PTY local. Si `claude` / `codex` / `grok` están en PATH, un test
aparte posee el binario real, lo detiene y registra la salida. No envía un
turno de modelo.

## Relacionado

- [Operar una sesión de proveedor](/how-to/operate-provider-sessions/)
- [Módulo II — operación en vivo](/reference/modules/ii-sessions/)
- [Conectar Claude Code](/how-to/connect-claude-code/)
- [OpenAPI beta](/reference/api-beta/)
