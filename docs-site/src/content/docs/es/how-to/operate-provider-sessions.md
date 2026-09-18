---
title: Operar una sesión de proveedor
description: >-
  Registra un perfil de proveedor para un home existente de Claude, Codex o Grok
  en este nodo, fija el binario oficial del controlador, lanza una sesión
  gobernada desde la consola o la CLI, e interrumpe o detén el turno sin inventar
  un ejecutor.
---

Esta página es la vía de **operación** de las CLI oficiales de proveedor. El
plano de control lanza un proceso hijo propio bajo un **perfil de proveedor**.
No instala el proveedor, no crea su home ni inicia un inicio de sesión
interactivo en el navegador.

No es la vía del conector ni del hook. Para inventariar o gobernar los ficheros
de configuración de Grok Build o de Codex, usa
[Integrar Grok Build](/how-to/integrations/grok/) o
[Integrar Codex](/how-to/integrations/codex/). Para co-desplegar Claude Code en
el mismo host, usa
[Ejecutar Claude Code con Olivares](/how-to/run-claude-code-with-olivares/).

Fuente de este comportamiento: sección `[26.9.0]` de `CHANGELOG.md`
(perfiles de proveedor, controlador Codex, controlador Grok), las referencias
generadas de [consola](/reference/console/) y
[configuración](/reference/configuration/), `cmd/olivares/sessionruntime.go` y
`web/src/features/agentops/types.ts`.

## Precondiciones

Complétalas antes de un lanzamiento. Un elemento que falta es una denegación,
no un respaldo.

1. Olivares AI está instalado y existe el primer administrador.
   Consulta [Tu primera hora](/how-to/first-hour/) para el token de
   configuración y el muro AAL3 de passkey. Crear orígenes y las operaciones
   privilegiadas de sesión exigen AAL3 (`core/api/middleware.go` `requireAAL3`).
2. La CLI oficial del proveedor ya está instalada en **este nodo**. El perfil
   registra homes que ya existen. El servidor resuelve las rutas (absolutas,
   enlaces simbólicos resueltos, directorio existente) y no crea, instala ni
   inicia sesión (`web/src/features/agentops/types.ts` `CreateProfileRequest`).
3. Tienes `sessions:profile:read` para abrir **Provider profiles**
   (`/provider-profiles`) y `sessions:profile:write` para registrar.
   Vincular un origen necesita `sessions:profile-binding:write` más
   administración de orígenes. Lanzar una ejecución necesita `sessions:run:write`.
   Permisos: [referencia de consola](/reference/console/).
4. El controlador correspondiente está **registrado en este nodo** al fijar su
   binario oficial. La preparación es por controlador. No hay un interruptor
   compartido (`cmd/olivares/sessionruntime.go`).

| Controlador | Fija esta variable de entorno | Si no está definida |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (predeterminado `claude`) | la vía Claude usa el nombre de ejecutable predeterminado |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Si no está definida, una instalación gestionada **registrada** (`olivares agent tool install --driver codex`) fija el ejecutable del recibo. En caso contrario, los perfiles Codex siguen observables y no se pueden lanzar. El motor no busca en `PATH`. |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Si no está definida, una instalación gestionada **registrada** (`olivares agent tool install --driver grok`) fija el ejecutable del recibo. En caso contrario, los perfiles Grok siguen observables y no se pueden lanzar. El motor no busca en `PATH`. |

El valor es el binario oficial que este nodo puede operar. El motor no
resuelve `codex` ni `grok` desde `PATH`. La tabla de configuración generada
también lista `OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` con la misma regla de
registro; esta página no añade más afirmaciones sobre OpenCode.

Los lanzamientos de Claude siguen necesitando una fuente de credencial de
inferencia (`OLIVARES_SESSION_RUNTIME_WIF` o
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`). Consulta
[Tu primera hora §3](/how-to/first-hour/#3-iniciar-una-sesión-de-claude-code-desde-la-consola).
Codex y Grok usan solo el `auth_source` AUTORIZADO del perfil:
`provider_account_home` o `managed_injection`, sin respaldo entre ellos y sin
valor predeterminado (`CHANGELOG.md` `[26.9.0]`; `ProviderProfileDTO.auth_source`).

:::caution[Lo que esta página no afirma]
`CHANGELOG.md` `[26.9.0]` indica que el comportamiento del controlador Grok se
prueba contra un hijo ACP falso propio a través del HTTP, el runtime, el
almacén y el grupo de procesos reales. **La compatibilidad con una cuenta
oficial de Grok autenticada es trabajo aparte y no se afirma aquí.**
:::

## 1. Registrar un perfil de proveedor

Un perfil de proveedor es la identidad duradera de **una** instancia de
proveedor configurada en **un** entorno de ejecución: controlador, entorno
propietario y los `config_home` / `user_home` canónicos bajo los que corre el
hijo. Es identidad de configuración y de almacenamiento, no una cuenta de
proveedor autenticada (`CHANGELOG.md` `[26.9.0]` B1; texto de consola
`agentops.profiles.subtitle`).

### Consola

1. Abre **Provider profiles** (`/provider-profiles`).
2. Elige **Register profile**.
3. Indica el controlador (`claude`, `codex` o `grok`), el `config_home`
   existente y el `user_home` existente. `environment_ref` puede omitirse
   (este nodo).
4. Guarda. La lista muestra `profile_ref`, controlador, estado y si el perfil
   está habilitado en este entorno. Las rutas **no** están en la lista ordinaria.
5. Para ver los homes almacenados, usa **Reveal configuration**
   (`sessions:profile:admin`). Esa lectura es bajo demanda y se descarta al
   ocultarla. Sigue sin llevar ningún valor de credencial.

Renombrar, deshabilitar y habilitar conservan el mismo id y los mismos homes.
**Retire** es irreversible, se confirma escribiendo y libera el home para un
id **nuevo**.

### Lo que el motor deniega

- Un perfil cuyo controlador no está registrado en este nodo sigue visible y
  no se puede lanzar (`operable` no es una garantía de lanzamiento;
  `GET …/launch-readiness` es el panel de requisitos).
- Un perfil de otro entorno de ejecución se muestra como extranjero y nunca
  se lanza desde este nodo.
- Un lanzamiento que reenvía `HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` o
  `GROK_HOME` se deniega. Esos nombres pertenecen al perfil
  (`agentops.create.profileEnvConflict`).

No hay captura de esta pantalla en el conjunto publicado. No trates una imagen
de la pestaña Connectors como este formulario.

## 2. Vincular un origen (opcional, para atribución observada)

Un origen puede dedicarse a un perfil en la revisión exacta del roster que
aplicó este nodo. La clave es el id persistente de la fila, nunca su nombre
editable (`CHANGELOG.md` `[26.9.0]` B1; **Source bindings** `/provider-bindings`).

1. Abre **Source bindings** (`/provider-bindings`).
2. Vincula el `id` persistente del origen y el `applied_revision` que cableó
   el reconciliador de este nodo. `GET /v1/console/sources` informa ambos.
3. Revoca el vínculo para detener la atribución **nueva** al perfil. Un
   sobre anterior conserva su vínculo histórico durante la reproducción.

Sin vínculo, un registro conocido sigue apareciendo como fila de observación
`source`. No se fusiona en una ejecución gestionada. Consulta
[Operación en vivo y sesiones](/reference/modules/ii-sessions/).

## 3. Lanzar

El extremo productivo de creación exige `provider_profile_ref`. Omitirlo
conserva el cuerpo de petición anterior, que esta API deniega
(`CHANGELOG.md` `[26.9.0]` B2; flag CLI `--provider-profile`).

El diálogo de lanzamiento ofrece los perfiles **activos**. Ningún perfil
viene preseleccionado. Las selecciones de workspace y plantilla se pueden
borrar; el perfil no (`CHANGELOG.md` `[26.9.0]` Fixed).

### Consola

1. Abre **Operate sessions** (`/agentops`) u **Observe sessions** (`/sessions`).
   Comparten pantalla ([referencia de consola](/reference/console/)).
2. Abre el diálogo de lanzamiento.
3. Elige **Provider profile** (`agentops.create.profile`). La pista indica
   que el perfil es obligatorio.
4. Opcionalmente indica workspace, plantilla, modelo y effort. Modelo y
   effort siguen siendo cadenas abiertas del proveedor en los flags oficiales
   del agente para Grok (`CHANGELOG.md` `[26.9.0]`).
5. Envía **Request launch**. Solo se publica la **referencia** del perfil. El
   servidor resuelve los homes.

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

Añade `--server`, `--tenant` y `--token` (o el contexto de cliente activo)
como en la [referencia CLI](/reference/cli/). El aislamiento es `native` en
esta versión; `container` y `sandbox` los acepta la API y el lanzador los
deniega hasta que existan esos ejecutores (ayuda CLI generada).

Resultado: un recurso de ejecución. La fila viva gestionada es única por
ámbito de observación e id externo. Las lecturas que nombran una fila usan
`live_ref`, no el id de sesión del proveedor que dos homes pueden compartir.

## 4. Interrumpir o detener

| Intención | Consola | CLI | Resultado |
|---|---|---|---|
| Terminar el turno activo, conservar el proceso y la conversación | control interrupt de la sesión viva | `olivares agent session interrupt <run-ref>` | el turno termina; el proceso sigue usable para el siguiente (`CHANGELOG.md` `[26.9.0]`) |
| Terminar la ejecución | control stop | `olivares agent session stop <run-ref>` | el recurso de ejecución; el runtime sigue recogiendo el hijo |

Las ejecuciones ligadas a trabajo envían su cerca de arrendamiento exacta. Los
resultados obsoletos o inciertos siguen explícitos. Reanudar continúa solo en
el mismo home demostrado.

Una interrupción Grok usa ACP `session/cancel`, que es una notificación sin
acuse. La interrupción resuelve aprobaciones pendientes, cancela y deja el
turno abierto hasta que vuelve el resultado correlacionado del propio prompt
(`CHANGELOG.md` `[26.9.0]`). No trates una cancelación silenciosa como un
recibo confirmado del proveedor.

## Relacionado

- [Tu primera hora](/how-to/first-hour/) — token de configuración, AAL3, fuente de credencial Claude.
- [Ejecutar Claude Code con Olivares](/how-to/run-claude-code-with-olivares/) — topologías de co-despliegue.
- [Integrar Codex](/how-to/integrations/codex/) / [Integrar Grok Build](/how-to/integrations/grok/) — conector y hook PEP.
- [Operación en vivo y sesiones](/reference/modules/ii-sessions/) — `live_ref` y atribución.
- [Configuración](/reference/configuration/) — variables de fijación del controlador.
- [Referencia CLI](/reference/cli/) — `olivares agent session *` (generada desde el binario).
