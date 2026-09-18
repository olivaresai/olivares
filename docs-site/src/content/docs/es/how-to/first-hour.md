---
title: "Tu primera hora con Olivares AI (v26.9.1, tal como se distribuye)"
description: >-
  Lo que una instalación limpia del binario público v26.9.1 te permite hacer
  realmente durante la primera hora: el token de configuración, el muro AAL3,
  el registro de una passkey, el inicio de sesiones con perfiles de proveedor,
  el despliegue, el conocimiento y los hooks PEP de Codex y Grok.
---

Esta página describe **v26.9.1 tal como se distribuye**. No es un asistente de
primer arranque previsto para el futuro ni una captura de pantalla de una
maqueta. Cada paso que aparece a continuación es algo que el binario público
hace hoy, junto con el fichero o la variable de entorno que lo hace posible.
Cuando el producto se niega a hacer algo, la página lo dice.

Los hechos numerados se midieron el 2026-09-04 en una instalación limpia del
binario público. Esta página cita esas mediciones y el código en el que
desembocan.

Para instalar el binario en el host, consulta
[Autoalojar Olivares AI](/how-to/self-hosting/) y
[Verificar una versión](/how-to/verify-a-release/). Esta página no te indica
que canalices `https://olivares.ai/install` hacia una shell. Una vez que el
binario esté en el host, el primer comando recomendado es
`olivares quickstart`.

:::note[Lo que esto no es]
`--seed-demo` no es un recorrido por el producto. Medido el 2026-09-04 en una
instalación limpia del binario público: **36 de las 54** rutas de la consola
siguen vacías después de un arranque con datos de muestra. Esa cifra es el
censo de la fecha de medición. La
[referencia de consola](/reference/console/) generada en este árbol lista
**75 rutas**. Esta página no vuelve a contar pestañas vacías tras
`--seed-demo` en v26.9.1. El entorno de demostración rellena el recorrido del
grafo de acceso de
[De cero a un grafo de acceso de lectura/escritura](/tutorials/zero-to-graph/),
pero no rellena el resto de la consola. No lo uses para «explorar el producto».
:::

## 1. Arranque recomendado: `olivares quickstart`

Un directorio de datos nuevo **no tiene credenciales predeterminadas**. El
primer comando recomendado es `olivares quickstart`
(`cmd/olivares/cmd_quickstart.go`). Es `serve` con valores predeterminados
seguros: TLS activado, ninguna credencial predeterminada y un token de
configuración de un solo uso. La dirección de escucha predeterminada es `:8443`
— todas las interfaces, porque esto es un servidor (`cmd/olivares/binddefaults.go`). Medido el 2026-09-04 en una instalación limpia del binario público con
TLS real (incluida una ejecución en **:8460**); el texto del panel es el mismo.

El panel de bienvenida (`announceQuickstart`, `:154-163`) está numerado. El
motor imprime `https://localhost:8443` para ese bind, y lista debajo del token
todas las demás direcciones en las que responde este host. **No uses una IP para la
passkey.** El navegador rechaza una IP como RP ID de WebAuthn
(`SecurityError`). El producto obtiene el RP ID del nombre de host de la
solicitud (`core/api/handlers_webauthn.go:33-50`). Antes de registrar la
passkey, abre la consola en `https://localhost:PORT` (o en un nombre de host
real), no en `127.0.0.1`. `PORT` es `8443`, salvo que hayas pasado `--listen`.

```text
=== WELCOME TO OLIVARES AI ===
  1. Open:   https://localhost:8443
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

El banner imprime `localhost` para el bind por defecto y lista debajo del token las
otras direcciones de este host; una ceremonia de passkey necesita un nombre, no una
dirección. Sustituye el host
por `localhost` en la barra de direcciones.

El prefijo del token es `olst_` (`cmd/olivares/e2e_binary_test.go` comprueba
`olst_[A-Z0-9]+`). La página de la consola es `/setup`. La API a la que el
asistente envía la solicitud es:

```http
POST /v1/setup
Content-Type: application/json

{"token":"olst_…","email":"you@example.com","password":"…"}
```

`olivares serve` imprime un banner parecido (`announceSetup` en
`cmd/olivares/cmd_serve.go`). Usa `quickstart` durante la primera hora: la URL,
el aviso sobre el certificado autofirmado y el token de un solo uso aparecen
en un mismo panel. Después inicia sesión con `POST /v1/auth/login`. Ahora tienes
una sesión de contraseña AAL1.

`README.md` y ese panel de bienvenida nombran el token y la URL. **No** nombran
el registro de la passkey. Eso viene a continuación, desde la consola, después
de que pulses un botón.

## 2. El muro AAL3: la consola te dirige después de pulsar, no antes

Después de la configuración, **se rechaza la creación de fuentes, conectores,
espacios de trabajo y secretos hasta que la sesión sea AAL3**. La barrera es
`requireAAL3` en `core/api/middleware.go:310`. Un principal por debajo de AAL3
recibe `403
step_up_required`. **21 puntos de llamada** pasan por esa barrera.
Entre las rutas de escritura de este árbol se incluyen:

| Superficie | Handler | Fichero |
|---|---|---|
| Alta/eliminación/recarga del catálogo de fuentes | `handlePutSource` / `handleDeleteSource` / `handleReloadRuntime` | `core/api/handlers_sources.go` |
| Escrituras y prueba de conectores | `handlers_connectors.go` | `core/api/handlers_connectors.go` |
| Alta/eliminación de secretos | `handlePutSecret` / `handleDeleteSecret` | `core/api/handlers_secrets.go` |
| Creación/actualización de espacios de trabajo | `handleCreateWorkspace` / `handleUpdateWorkspace` | `core/api/handlers_scoping.go:152` / `:199` |
| Incorporación de miembros | `handleOnboardMember` | `core/api/handlers_onboarding.go:59` |

**PIV/CAC no te lleva hasta ahí en una instalación estándar.** Sin configurar,
las rutas de PIV responden **501** `piv_not_configured`
(`core/api/handlers_piv.go`, `core/api/errors.go`).

### Lo que hace realmente la consola

La consola **sí** te dirige al registro. Lo hace de forma **reactiva**.

1. Intentas una acción privilegiada. El panel de step-up muestra
   **Autenticar con llave de seguridad**
   (`web/src/features/identity/i18n/en.json` `assurance.authenticate`;
   botón en `web/src/features/identity/assurance.tsx:230-239`).
2. Al pulsarlo, se llama a `POST /v1/auth/webauthn/authenticate/options`
   (step-up, no registro). Sin una passkey, el motor responde **400**
   `no_webauthn_credential` (`isNoWebAuthnCredential` en
   `web/src/features/identity/api.ts:260-265`).
3. A continuación, el panel te indica que primero debes registrarte en la
   pestaña **Acceso privilegiado** (`assurance.tsx:160-168` →
   `assurance.unenrolled`). Esa frase **no es un enlace**.

Debes ir allí manualmente:

1. Abre la consola en **`https://localhost:PORT`**, no en `127.0.0.1`
   (consulta §1).
2. Abre `/identity` (`web/src/features/registry.tsx` — `path: '/identity'`).
3. Pestaña **Acceso privilegiado** (`tabs.login`).
4. **Registrar passkey** (`passkeys.register`). Basta con un
   **autenticador de plataforma** (el aviso del navegador o del sistema
   operativo; no hace falta una llave física). El servidor exige
   **verificación del usuario** (`core/auth/webauthn.go:74-85`,
   `UserVerification: VerificationRequired`). Medido el 2026-09-04 en una
   instalación limpia del binario público con una ceremonia WebAuthn completa:
   registro **200**, autenticación `{"aal":3}` y después
   `PUT /v1/console/connectors` **200**.

`POST /v1/auth/webauthn/register/options` se autentica como un **principal de
sesión** y **no** llama a `requireAAL3`. Si tiene éxito, escribe **200** con
`{publicKey: …}` (`core/api/handlers_webauthn.go:76-88`). Completa la ceremonia
con `POST /v1/auth/webauthn/register`. Después, vuelve a intentar el step-up.

`README.md` y el panel de bienvenida de `olivares quickstart` **no** nombran la
pestaña Acceso privilegiado. La consola solo la nombra **después** de ese 400.

El panel de identidad nombra AAL3 (NIST SP 800-63B-4) y PIV/CAC (FIPS 201-3)
como **estándares objetivo** y declara que **no afirma disponer de ninguna
certificación** (`targetStandardsNote`). Esta página tampoco afirma que exista
ninguna certificación.

### Añadir conector: el muro, sin campos

**Añadir conector** (`web/src/features/console/i18n/en.json`
`connectors.add`) abre un diálogo donde
`<RequireAssurance minAal={AAL.HARDWARE}>` envuelve `ConnectorForm`
(`web/src/features/console/connectors-tab.tsx:348-357`). Por debajo de AAL3, el
formulario no se monta. Medido el 2026-09-04 en una instalación limpia del
binario público: ese diálogo **no tiene campos** (`inputs: []`), solo el panel
de step-up. Ver el catálogo de tipos requiere AAL1 (`ConnectorCatalog` está
fuera de la barrera, en el mismo fichero `:341-346`); **añadir** uno no es una
operación AAL1.

### `/workspace` y enlaces de protocolo: sin selector de espacio de trabajo

`/workspace` (`registry.tsx` `path: '/workspace'`) y
`/communications/protocol-bindings` necesitan un espacio de trabajo. En una
instalación limpia no tienes ninguno y no puedes crear uno hasta alcanzar AAL3
(`handleCreateWorkspace`). `WorkspaceSwitcher` **no se renderiza** cuando hay
como máximo un espacio de trabajo
(`web/src/components/layout/workspace-switcher.tsx:43-44`:
`if (workspaces.length <= 1) return null`). Lo que permanece en la barra
superior es **Cambiar de organización**
(`web/src/lib/i18n/locales/en/auth.json` `tenant.switch`). No hay ningún
selector de espacio de trabajo que puedas pulsar.

## 3. Iniciar una sesión de Claude Code desde la consola

La consola solo puede generar un proceso `claude` cuando el **host** dispone de
una fuente de credenciales de inferencia. Sin ella, los inicios stream-json se
rechazan en modo deny-closed. Medido el 2026-09-04 en una instalación limpia
del binario público: **HTTP 503**.

Configura **una** de estas opciones:

- `OLIVARES_SESSION_RUNTIME_WIF` — emisión WIF dentro del proceso (`cmd/olivares/sessionruntime.go`, `cmd/olivares/wifbroker.go`)
- `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` — ruta a un fichero de token rotado y de corta duración

La raíz de composición registra qué fuente está conectada o, en su defecto:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go:74-76`). Variables relacionadas opcionales:
`OLIVARES_SESSION_RUNTIME_WIF_RULE`, `OLIVARES_SESSION_RUNTIME_TOKEN_TTL`,
`OLIVARES_SESSION_RUNTIME_BASE_URL`, `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`
(enumeradas en `cmd/olivares/config_registry.go` y en
[Configuración](/reference/configuration/)).

Las topologías de despliegue conjunto para **Operate** (en el mismo host que
`claude`) se describen en
[Ejecutar Claude Code con Olivares](/how-to/run-claude-code-with-olivares/). La
ruta de observación OTLP se explica en
[Conectar Claude Code](/how-to/connect-claude-code/).

### Las claves de proveedor no son lo que inicia una sesión

**Modelos → Claves de proveedor** es un registro de gobernanza de
**referencias**. El formulario **nunca acepta un secreto**:

> Este formulario nunca acepta un secreto. Olivares guarda solo una referencia
> y una pista enmascarada.

(`web/src/features/models/i18n/en.json` `keys.dialog.noSecretNote`). Rellenar
esa pestaña no satisface `OLIVARES_SESSION_RUNTIME_WIF` ni
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`. No habilita los inicios de sesión.

## 4. El plan de despliegue responde 503 hasta que aprovisiones un ejecutor

Las operaciones plan/apply por `POST` del módulo de despliegue devuelven
**503** hasta que el host establezca `OLIVARES_DEPLOY_EXECUTOR_CONFIG` con la
ruta de un fichero JSON. Si falta, el módulo mantiene el ejecutor sin conectar
que opera en modo deny-closed (`cmd/olivares/deployexec_load.go:16-20`). Un
fichero ilegible hace que el arranque **falle**.

El objeto JSON es `deployExecutorConfig` en
`cmd/olivares/deployexec_load.go:26-42`. Bloques opcionales del backend:
`tofu`, `terraform`, `gitops`, `k8s`, `docker`, `nomad`, `crossplane`, además de
`credential`, `blast_radius`, `identity_binding`, `drift`.

La variable de entorno está documentada en
[Configuración](/reference/configuration/)
(`docs-site/src/content/docs/reference/configuration.md:152`). No está
documentada como un clic en la consola durante la primera hora. No hay nada
que puedas seleccionar en la interfaz de usuario para sustituir ese fichero.

El catálogo de módulos marca la actuación de Deployment como
**on-demand (503)** ([Módulos](/reference/modules/overview/)). Esa fila expresa
el mismo hecho.

## 5. Consultar una base de conocimiento pública o consultar con una identidad de agente

El embedder predeterminado es **LocalHashEmbedder**, que no genera tráfico de
salida. Al arrancar se avisa de que la recuperación es **léxica, no semántica**,
y se muestra `embed_model=local-hash` (`cmd/olivares/claude_inference.go`,
`cmd/olivares/knowledgestatus.go`). La recuperación léxica sigue devolviendo
fragmentos cuando la barrera los permite.

Sin una identidad de agente autenticada, la barrera de recuperación solo
concede contenido **público y sin restricciones**
(`modules/knowledge/query.go:120-127`). Una consulta REST humana a `/query`
sobre una base de conocimiento **interna** se deniega. Medido el 2026-09-04 en
una instalación limpia del binario público: una base de conocimiento
**pública** devolvió **1 resultado** (puntuación 0.738). Consulta una base de
conocimiento pública o usa una identidad de agente.

Hoy, una consulta denegada informa de `excluded_chunks: 0` incluso cuando se
ha excluido todo. Ese contador solo aumenta para el umbral
`excluded_sources` definido por el operador (`query.go:256-284`); una
denegación por clearance o ACL nunca lo incrementa.

## 6. Sesiones Codex y Grok: perfiles de proveedor y, después, los hooks CLI

v26.9.1 opera la CLI oficial de Codex y la CLI oficial de Grok como
controladores de sesión, además de Claude Code (`CHANGELOG.md` `[26.9.0]` Added).
La consola administra esos lanzamientos en **Provider profiles**
(`/provider-profiles`, `sessions:profile:read`) y **Source bindings**
(`/provider-bindings`, `sessions:profile-binding:read`). Ambas rutas están en
la [referencia de consola](/reference/console/) generada.

Un perfil es la identidad duradera de una instancia de proveedor configurada
en un entorno de ejecución. No es una cuenta de proveedor autenticada
(`web/src/features/agentops/types.ts`). Registrar un perfil valida homes que
ya existen en este nodo. El servidor no instala, crea ni inicia sesión.

### Registrar el controlador en el host antes de un lanzamiento

La preparación es por controlador. No hay un interruptor compartido
(`cmd/olivares/sessionruntime.go`). La variable de entorno correspondiente
registra ese controlador en este nodo:

| Controlador | Variable de entorno | Si no está definida |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (predeterminado `claude`) | la vía Claude usa el nombre predeterminado |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | los perfiles Codex siguen observables y no se pueden lanzar |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | los perfiles Grok siguen observables y no se pueden lanzar |

Los valores son binarios oficiales fijados. El motor no resuelve `codex` ni
`grok` desde `PATH`. Fuente: [Configuración](/reference/configuration/).

Los lanzamientos de Claude siguen necesitando una fuente de credencial de
inferencia, como en §3. Codex y Grok autentican con el `auth_source`
AUTORIZADO del perfil (`provider_account_home` o `managed_injection`, sin
respaldo). `CHANGELOG.md` `[26.9.0]` **no** afirma compatibilidad con una
cuenta oficial de Grok autenticada.

El diálogo de lanzamiento exige un perfil de proveedor. El extremo
productivo de creación exige `provider_profile_ref`. Omitirlo conserva el
cuerpo anterior, que esta API deniega ([CLI](/reference/cli/)
`olivares agent session create --provider-profile`).

Cómo registrar un perfil, vincular un origen, lanzar, interrumpir y detener:
[Operar una sesión de proveedor](/how-to/operate-provider-sessions/).

### Lo que sigue siendo solo CLI (hooks y configuración gestionada)

Estos comandos no son el lanzamiento de sesión en consola. Siguen existiendo:

| Comando | Qué es | Fuente |
|---|---|---|
| `olivares codex` | Renderiza los ficheros `requirements.toml` / `managed_config.toml` de Codex a partir de un JSON de Policy. **Escribe ficheros; no se comunica con el plano de control.** | `cmd/olivares/cmd_codexmanagedconfig.go` |
| `olivares codex-hook` | Hook PEP deny-closed que invoca Codex (stdin → plano de control → stdout con la forma del evento). | `cmd/olivares/cmd_codexhook.go` |
| `olivares grok-hook` | Hook PEP deny-closed que invoca Grok Build. Una denegación solo **bloquea** en `pre_tool_use`. | `cmd/olivares/cmd_grokhook.go` |

Para instalar el hook de Codex (verificado frente a la forma de `hooks.json` de
Codex que aparece en el comentario de ese fichero), `command` debe ser una
**cadena**, `olivares codex-hook`. Entorno:
`OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT` (y, opcionalmente, agente/organización/cuenta).

Entorno del hook de Grok: `OLIVARES_GROK_HOOK_URL`,
`OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`. Grok puede desactivar
un hook por su nombre mediante `~/.grok/disabled-hooks`; eso no es un control de
la consola.

No busques un **botón** «Conectar Codex» o «Conectar Grok». El alta de
conectores es **Control console → Connectors** (tipo `codex` o `grok`). Esa
es la vía de observar/gobernar en
[Integrar Codex](/how-to/integrations/codex/) e
[Integrar Grok Build](/how-to/integrations/grok/). No registra un
controlador de sesión.

## 7. `--seed-demo` no rellena la consola

`olivares serve --seed-demo` carga un entorno de demostración para que se pueda
seguir el tutorial del grafo de acceso. Medido el 2026-09-04 en una instalación
limpia del binario público: **36 de las 54** pantallas de la consola siguen
vacías en ese entorno. Esa cifra es el censo de la fecha de medición; la
referencia de consola generada lista hoy **75 rutas**. Usa `--seed-demo` solo
para el recorrido de
[De cero a un grafo de acceso de lectura/escritura](/tutorials/zero-to-graph/).
No trates las pestañas vacías después de `--seed-demo` como una instalación
rota ni el flag como un recorrido por el producto.

## Relacionado

- [Honestidad y límites](/start/honesty-and-limits/) — lo que la documentación puede afirmar.
- [Autoalojar Olivares AI](/how-to/self-hosting/) — cómo se ejecuta el binario.
- [Operar una sesión de proveedor](/how-to/operate-provider-sessions/) — perfiles, pines de controlador, lanzamiento, interrupción.
- [Configuración](/reference/configuration/) — las variables de entorno mencionadas arriba.
- [Módulos](/reference/modules/overview/) — actuación on-demand (503) frente a activa.
