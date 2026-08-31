---
title: "Ejecutar Claude Code con Olivares (co-despliegue)"
description: "Co-despliega el control plane de Olivares y un runtime de Claude Code en una misma máquina Linux, seguro por defecto, de modo que el motor lance, gobierne y desmonte sesiones de Claude Code compartiendo un workspace — en cuatro topologías."
---

Esta es la mitad **Operate** de la historia Anthropic-first: no solo *observar* y
*gobernar* Claude Code, sino **conducirlo**. El control plane lanza un proceso `claude`
real, puentea su I/O hacia un stream gobernado, ancla cada transición de ciclo de vida en
el audit ledger y lo desmonta —sobre un workspace compartido, desde la API/CLI (y,
más adelante, el portal), **sin SSH**—. Esta página co-despliega ambas mitades en un único host Linux
en cuatro topologías, seguro por defecto.

Para la vía de *observación cooperativa* (telemetría OTLP → access map) consulta
[Conectar Claude Code](/how-to/connect-claude-code/); para la vía de *gobierno* (hooks PreToolUse
como PEP) consulta el [ejemplo govern-claude-code](https://github.com/olivaresai/olivares/tree/main/examples/govern-claude-code).
Esta página trata el **co-despliegue**: poner ambos runtimes en marcha juntos.

:::note[Cómo llega realmente el gobierno a la sesión]
Una sesión está gobernada porque **el motor es dueño del stdin/stdout de `claude`** —el
transporte headless `stream-json`—. El motor genera `claude` como proceso hijo (el
procRunner nativo) y puentea cada frame NDJSON. Eso solo funciona cuando el motor y
`claude` comparten un contexto de ejecución (el mismo host, o el mismo contenedor). Las
topologías recomendadas los ponen juntos por exactamente esta razón; las topologías mixtas,
y sus restricciones honestas, están más abajo.
:::

## Dos principios antes de empezar

1. **Opt-in.** La imagen base de Olivares es distroless y **no lleva `claude`**. La
   capa Operate-Claude-Code es un artefacto *aparte* —una imagen combinada
   (`Dockerfile.agentops`) o un add-on de instalación nativa—. Si no ejecutas Claude
   Code gobernado, nunca la descargas, y su superficie adicional nunca toca tu control
   plane.
2. **Fuente oficial, nunca redistribuida.** Los términos de Anthropic no permiten
   redistribuir el binario `claude`, así que lo **instalamos desde la fuente oficial de
   Anthropic, firmada con GPG**, en build/primera ejecución (los repositorios apt/dnf/apk
   firmados), fijado y con el auto-updater desactivado. No distribuimos ningún binario de
   terceros. También puedes **traer el tuyo** (`claude`) y apuntar el motor a él.

## Las cuatro topologías de un vistazo

| # | Olivares | Claude Code | Cómo lo conduce el motor | Estado |
|---|----------|-------------|----------------------------|--------|
| 1 | Docker | Docker | **Mismo contenedor** (imagen combinada), hijo del procRunner | **Recomendada** (misma vía gobernada que la 2) |
| 2 | Nativo | Nativo | Mismo host (systemd), hijo del procRunner | **Recomendada**, probada end-to-end con smoke test |
| 3 | Docker | Nativo (host) | Cross-namespace — no gobernable tal cual | Co-localiza en su lugar (ver abajo) |
| 4 | Nativo | Docker (por sesión) | Contenedor por sesión vía la API de Docker | Pendiente (documentado) |

Las dos topologías **co-localizadas** (1, 2) son el valor por defecto seguro. La topología 2 (nativa) está
probada end-to-end por [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh);
la topología 1 reutiliza la **misma** vía gobernada del procRunner (el build/run de la imagen combinada
aún no está cableado en un test automatizado). Las topologías 3 y 4 quieren al gobernador y al gobernado en
contenedores *distintos*; puentear stdio a través de esa frontera necesita acceso a la API de Docker (un
privilegio que el motor deliberadamente **no** toma por defecto). Sus vías honestas están
detalladas en [Topologías mixtas](#topologías-mixtas-3-y-4).

---

## Topología 1 — ambos en Docker (recomendada)

Un único contenedor endurecido ejecuta el motor **y** `claude`; un volumen de workspace es el
directorio de trabajo compartido. Solo loopback, sin root, sistema de archivos raíz de solo lectura
—postura idéntica al compose base, más el runtime conducido.

### Construir la imagen combinada

`claude` se instala en tiempo de build desde el **repositorio apt firmado** de Anthropic, con la
huella de la clave de firma fijada (`31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`) y
el auto-update desactivado. Fija la base del motor por digest y verifícala primero:

```sh
# verify the engine image you build FROM (it is cosign-signed)
cosign verify docker.io/olivaresai/olivares:26.8.0 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker build -f Dockerfile.agentops \
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --build-arg CLAUDE_CHANNEL=stable \
  -t olivares-agentops:26.8.0 .
```

Trae tu propio `claude` en su lugar con `--build-arg CLAUDE_INSTALL=byo` (la imagen se distribuye
sin `claude`; monta el tuyo en runtime y configura `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`).

### Levantarlo

```sh
export OLIVARES_AGENTOPS_IMAGE=olivares-agentops:26.8.0
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.agentops.yml up -d
```

El override cambia solo lo que Operate necesita: la imagen combinada, cuatro volúmenes escribibles
(datos del motor, **workspace**, el home `~/.claude` de claude, el token de inferencia de vida corta)
y el entorno del session-runtime. Todo lo demás —puertos ligados a `127.0.0.1`, uid 65532,
raíz `read_only`, `cap_drop: ALL`, `no-new-privileges`— se hereda de la base.

:::caution[La primera sesión gobernada necesita una credencial de inferencia]
La fuente de la credencial es **deny-closed**: un lanzamiento `stream-json` lee un token bearer
*de vida corta* desde `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` (`/run/olivares/session-token`,
en el volumen `olivares-runtime`) y lo descarta —solo se almacena un `credential_id`
no sensible—. Apunta tu refrescador WIF/SPIFFE/OIDC a ese volumen. Hasta que haya un token
presente, los lanzamientos `stream-json` fallan **cerrados** —el motor sigue ejecutándose y por lo
demás es gobernable; cablear la autenticación es tu paso deliberado—. (El intercambio de token in-process
en vivo se cablea por separado.)
:::

---

## Topología 2 — ambos nativos (sin Docker)

Motor y `claude` en el host; systemd ejecuta el motor, que conduce `claude`. El
workspace vive en `/var/lib/olivares/workspaces`.

### Un comando

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
```

Detecta automáticamente la topología nativa, instala el binario del motor **verificado** (el
`install.sh` con verificación cosign), instala `claude` desde el repositorio apt/dnf/apk firmado (con
verificación de la huella de la clave —o `OLIVARES_CLAUDE_INSTALL=byo` para omitirlo—), crea el
usuario de servicio `olivares` sin login y el directorio de workspace, y coloca el override systemd
endurecido + el ejemplo de entorno. **No** arranca automáticamente un plano de gobierno —ejecutar uno
es tu decisión explícita.

### Qué cablea el instalador (y por qué)

- `packaging/systemd/olivares.service.d/agentops.conf` — un drop-in que da al
  `claude` conducido un `HOME` escribible para `~/.claude` (mantenido bajo `/var/lib/olivares`,
  para que `ProtectHome=true` siga blindando a los usuarios reales), garantiza que exista el directorio
  de workspace y levanta exactamente **una** propiedad de sandbox: `MemoryDenyWriteExecute` (el runtime de
  `claude` compila JIT y necesita memoria W→X). El resto de directivas de endurecimiento de la unidad base
  permanece en vigor.
- `/etc/olivares/agentops.env` — la configuración del session-runtime (fichero de token, TTL, URL base
  opcional del gateway, ruta opcional al `claude` BYO).

Luego, deliberadamente:

```sh
sudo nano /etc/olivares/agentops.env     # wire the short-lived inference token (refresher)
sudo systemctl enable --now olivares     # loopback-only by default
```

:::note[Por qué no hay un servicio `claude` separado]
Un demonio `claude` de larga ejecución pondría su stdin/stdout fuera del alcance del motor —y
el transporte gobernado *es* stdio—. Así que el motor lanza y posee el propio proceso `claude`;
la "unidad de runtime" es el propio servicio del motor, configurado para el rol Operate por el
drop-in.
:::

---

## Lanzar la primera sesión gobernada

Los mismos pasos en cualquiera de las topologías co-localizadas. Autentica la CLI, registra el workspace
compartido, lanza:

```sh
export OLIVARES_SERVER_URL=https://127.0.0.1:8443
export OLIVARES_TOKEN=<your-api-token>
export OLIVARES_TENANT=<your-tenant-id>

# 1) register the shared workspace (the session's working dir; jailed file API on top)
olivares agent workspace add /var/lib/olivares/workspaces/project-x --name project-x --mode rw

# 2) launch a governed session over the stream-json transport
olivares agent session create --transport stream-json \
  --permission-mode acceptEdits --model opus \
  --workspace <workspace-ref> --isolation native

# 3) attach to its live, bridged I/O (lossless replay from a cursor); send input; stop
olivares agent session attach <run-ref>
olivares agent session input  <run-ref> --line '{"type":"user","message":{"role":"user","content":"…"}}'
olivares agent session stop   <run-ref>
```

Cada transición (`created → launched → … → stopped`) queda **anclada en el audit ledger
firmado** (`olivares agent session events <run-ref>`); la API de archivos del workspace
(`olivares agent workspace files|get|put|…`) está enjaulada y auditada. El contrato de reproducibilidad
de todo esto es [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh),
que levanta el co-despliegue nativo contra un `claude` falso hermético y comprueba que la
sesión es gobernable de extremo a extremo.

:::note[Solo `--isolation native` es funcional en esta release]
`--isolation container` y `--isolation sandbox` son **valores de seam de compatibilidad hacia
adelante, aún no cableados** (el Runner de contenedor por sesión es el follow-up documentado en
[Topología 4](#topología-4--olivares-nativo-claude-en-un-contenedor-por-sesión)). El runner nativo
**rechaza** un lanzamiento container/sandbox (con un error claro) en lugar de ejecutar silenciosamente
`claude` sin el aislamiento que pediste. Usa `native` —bajo el co-despliegue de imagen combinada /
systemd esa es la propia frontera endurecida de contenedor/host del motor.
:::

:::caution[`bypassPermissions` debe ir detrás del gobierno]
Ejecutar `claude` headless con un `--permission-mode` permisivo (`bypassPermissions`,
`dontAsk`) es exactamente cuando quieres el plano de gobierno. El entorno con allowlist
del motor nunca filtra un secreto `OLIVARES_*`/`ANTHROPIC_*` al agente, y el
PEP PreToolUse / presupuesto / kill-switch deciden qué puede hacer realmente la sesión.
:::

---

## Topologías mixtas (3 y 4)

Estas separan al gobernador y al gobernado a través de una frontera de contenedor. Sé lúcido
sobre lo que eso cuesta.

### Topología 3 — Olivares en Docker, Claude en el host

**No hay vía gobernada limpia**: un motor contenedorizado no puede poseer el stdio de un
proceso en los namespaces del host, y el transporte gobernado es stdio. Alcanzar un `claude`
del host exigiría compartir el namespace PID del host y montajes dentro del contenedor del motor
—una des-aislación grande y deliberada que anula el sentido de contener el motor—. **Co-localiza
en su lugar**: ejecuta ambos en la imagen combinada (eso *es* la topología 1), o
ejecuta ambos nativos (topología 2). Es un límite real, declarado en vez de disimulado.

### Topología 4 — Olivares nativo, Claude en un contenedor por sesión

Este es el hogar natural del **aislamiento de contenedor fresco por sesión**: cada sesión obtiene
un contenedor `claude` endurecido recién creado (workspace bind-mounted, raíz de solo lectura, sin root,
cap-drop), creado y desmontado por el motor a través de la API de Docker, con stdio puenteado
vía attach/hijack de Docker. El seam del modelo de datos ya lo **modela** (`--isolation container`
es un valor válido, y la primitiva de montaje del ejecutor que consumirá ya está entregada) —pero el
runner detrás de él aún no está cableado, así que el runner nativo rechaza ese valor hoy (ver la
nota anterior).

**Es un follow-up documentado, no entregado en esta release.** Manejar contenedores hermanos
significa dar al motor acceso a la API de Docker (idealmente a través de un proxy de socket con mínimo
privilegio) —una superficie de confianza que esta release evita deliberadamente en favor de la imagen
combinada sin socket—. Elegir esta topología es elegir un aislamiento gobernador/gobernado más fuerte
*a costa de* esa concesión de la API de Docker; llegará detrás del seam `isolation=container`
existente. Hasta entonces, el valor por defecto seguro es la co-localización.

---

## Postura de seguridad (todas las topologías)

- **Loopback por defecto.** Los puertos del host se publican solo en `127.0.0.1`. En un contenedor el
  motor escucha en `0.0.0.0` *dentro* del contenedor, así que el **mapeo de puerto del host es la
  frontera de exposición** —nunca lo publiques en una dirección de host que no sea loopback sin tu propio
  proxy de autenticación con terminación TLS—. El bind nativo/systemd por defecto es loopback. Expón deliberadamente.
- **Sin root, mínimo privilegio.** uid/gid 65532, sistema de archivos raíz de solo lectura, `cap_drop:
  ALL`, `no-new-privileges` (Docker) / el conjunto completo `Protect*`/`Restrict*` menos la única
  relajación W^X documentada (systemd).
- **Datos mínimos, entorno con allowlist.** El `claude` hijo hereda solo una allowlist
  explícita (PATH, HOME, locale…) más el token de inferencia en memoria —**ninguna** clave de firma
  `OLIVARES_*`, **ningún** `ANTHROPIC_*`/`CLAUDE_CODE_*` ambiental que pudiera ensombrecer la
  credencial emitida.
- **Cadena de suministro verificada.** El motor está firmado con cosign (verifícalo / fíjalo por digest);
  `claude` se instala desde los repos firmados de Anthropic con la huella de la clave fijada. El
  instalador **se niega a ejecutar un motor no verificado** salvo que renuncies explícitamente.
- **Auditoría anclada.** Cada transición de ciclo de vida y cada mutación del workspace queda sellada en
  el ledger hash-chained y firmado mediante `PayloadHash` —los bytes de los archivos y el contenido
  de los frames nunca se persisten.

## Véase también

- [Conectar Claude Code](/how-to/connect-claude-code/) — la vía de observación cooperativa.
- [Seguridad y endurecimiento](/how-to/security-hardening/) — la postura base del motor.
- [Verificar una release](/how-to/verify-a-release/) — verificación cosign / SBOM / SLSA.
- [INSTALL.md](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment) — la matriz de instalación, incluido este co-despliegue.
