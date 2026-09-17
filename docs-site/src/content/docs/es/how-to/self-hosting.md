---
title: Autoaloja Olivares AI
description: >-
  Ejecuta Olivares AI por tu cuenta — binario único, Docker Compose o
  Kubernetes — con valores por defecto seguros: sin credenciales por defecto, un
  token de configuración de un solo uso y TLS activado por defecto, sin telemetría
  obligatoria ni egreso del plano de control de forma predeterminada. Solo cruza tu
  perímetro lo que tú configuras para que lo cruce, desde llamadas a tus API de modelos
  hasta las salidas SIEM/webhook que conectas.
---

Olivares AI es **autoalojable por diseño**. Todo el producto es un único binario estático con la
interfaz web embebida, así que el despliegue más sencillo es un solo fichero; existen rutas con
Compose y Kubernetes para multinodo y producción. Todas las rutas comparten los mismos valores
por defecto seguros — sin credenciales por defecto, un token de configuración de un solo uso, TLS
activado por defecto —, sin telemetría obligatoria ni egreso del plano de control de forma
predeterminada. Solo cruza tu perímetro lo que **tú** configuras para que lo cruce: llamadas a
tus API de modelos, las salidas SIEM/webhook que conectas y un proveedor externo de embeddings
si aprovisionas uno.

Esta guía es la **página de decisión** del despliegue — las opciones y sus valores por defecto
seguros de un vistazo. Para la instalación paso a paso de cada escenario, los tutoriales de inicio
recorren cada ruta de principio a fin:
[nodo único (systemd)](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[aislado de red (air-gapped)](/tutorials/getting-started/air-gapped/). Para verificar los artefactos
criptográficamente primero, consulta [Verifica lo que has descargado](/how-to/verify-a-release/);
para sitios desconectados, consulta
[Instala en un entorno aislado de red](/how-to/air-gap-install/).

## Valores por defecto seguros (todas las rutas)

| Valor por defecto | Comportamiento |
|---|---|
| **Credenciales** | ninguna. El primer arranque imprime un **token de configuración de un solo uso** (`olst_…`); con él creas el primer administrador. |
| **TLS** | activado por defecto. `--insecure` (texto plano) es solo para desarrollo en localhost. |
| **Bind** | **todas las interfaces** (`:8443`, `:8444`) por defecto: esto es un servidor. Usa `--listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444` para restringirlo a este host. |
| **Licencia** | En el binario abierto (AGPL), la licencia se valida **offline** (Ed25519) y solo sirve como atestación — nunca restringe ni degrada el producto abierto, y eso no cambia. Los add-ons comerciales son un derecho por término pagado, entregado como **acceso por suscripción a los repositorios enterprise** (el modelo SUSE/Novell): para obtenerlos y recibir sus actualizaciones — incluidas las actualizaciones de seguridad — se requiere ese derecho. Los entornos aislados de red se atienden igual que en SUSE, mediante un reflejo local que sigue sujeto a ese derecho. |
| **Telemetría-home** | desactivada. El motor no hace llamadas salientes obligatorias en el arranque. |

## Opción 1 — binario único

Compila el único artefacto estático (almacén SQLite en Go puro, sin cadena de herramientas C) y ejecútalo:

```bash
task build                      # compiles ./bin/olivares with the web embedded
./bin/olivares serve \
  --listen 127.0.0.1:8443 \
  --grpc-listen 127.0.0.1:8444 \
  --data-dir /var/lib/olivares
```

En el primer arranque el motor imprime el banner de configuración:

```text
=== FIRST-BOOT SETUP ===
No accounts exist yet. Open the console and create the first administrator
with this one-time token — setup also creates your first organization and
makes that administrator its owner:

  Console:  https://127.0.0.1:8443
  Token:    olst_…

The console serves HTTPS with a self-signed certificate on first boot — your
browser will warn once; that is expected.

Passkeys will not work at that address:
a browser will not run a passkey ceremony at an IP address. Reach the
console by a host name.
On this machine the same console also answers at
  https://localhost:8443
and at that address the relying party is derived from the name, which the
verifier accepts.

The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
========================
```

Crea el primer administrador y luego inicia sesión:

```bash
curl -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

El directorio de datos contiene la base de datos SQLite, la clave de firma de auditoría y el material
TLS — haz copia de seguridad y protégelo.

### Directorio de datos personalizado (`layout: custom`)

El diseño nativo predeterminado es `/var/lib/olivares`. El adaptador de servicio
firmado (`install.sh --data-dir`, `scripts/install-service.sh`) admite un
directorio de datos **personalizado** por **forma**, no por lista de permitidos.
El manifiesto de propiedad registra `"layout": "custom"` (`CHANGELOG.md`
`[26.9.0]` Added; `INSTALL.md`).

SDD 04 §6: cada campo configurable declara propietario, esquema, fuentes
aceptadas y validador. Aquí el adaptador posee el directorio dedicado; el
operador posee el padre. Estas son las cadenas de rechazo del propio adaptador
(`scripts/install-service.sh`):

| Condition | What the adapter prints and exits 1 |
|---|---|
| Path is not `/*/*` (a top-level directory) | `custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $data_dir` |
| The data directory would contain the binary, config or unit | `custom data directory $data_dir must not contain the installed path $path` |
| Any path component is a symbolic link | `path component is a symbolic link ($prefix -> …); pass the resolved path instead of provisioning through a link: $1` |
| Parent of a new custom directory does not exist | `parent of the custom data directory does not exist; create it with the intended owner first: $(dirname -- "$data_target")` |
| Existing system directory mode is not 0700 or 0750 | `existing system data directory mode is $data_mode; require 0700 or 0750` |
| Path is under `/dev`, `/proc` or `/sys` | `data directory $data_dir is under an API file system (/dev, /proc, /sys): those hold kernel and device interfaces rather than durable state…; choose a real directory` |
| Path under `/tmp` or `/var/tmp` on systemd older than 235 | `data directory $data_dir is under /tmp or /var/tmp and this host runs systemd $running: creating a BindPaths= destination inside the private /tmp needs systemd 235 or later…` |
| A BindPaths= path contains `:` | `$2 $1 contains ':' and this location can only be reached with BindPaths=, whose value uses ':' to separate source from destination; choose a path without it` |

`install-agentops.sh` usa la misma regla de dos niveles para `OLIVARES_DATA_DIR`:
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`. Honra `OLIVARES_DATA_DIR` y un `OLIVARES_WORKSPACE_DIR`
seleccionado de forma explícita.

Una ruta bajo `/home`, `/root` o `/run/user` se renderiza con
`ProtectHome=tmpfs` y `BindPaths=` solo para ese directorio. Una bajo `/tmp` o
`/var/tmp` conserva `PrivateTmp=true` y recibe `BindPaths=` para ese directorio
solo (`sandbox_access` en `scripts/install-service.sh`).

`olivares uninstall` admite ese directorio personalizado solo cuando la unidad
en su ruta indexada ejecuta el motor con él, o cuando un preserve ya dejó un
testigo de desinstalación junto a la configuración del servicio. Diagnostica el
layout AgentOps registrado con `olivares doctor` — véase
[Resolución de problemas](/how-to/troubleshooting/#agentops-layout-check).

En instalaciones por paquete el valor predeterminado sigue siendo
`/var/lib/olivares`. Véase [Instalar desde un paquete](/how-to/install-from-packages/).
En macOS, [Instalar con Homebrew](/how-to/install-from-homebrew/).

## Opción 2 — Docker Compose (nodo único, SQLite)

El repositorio incluye un stack de Compose:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token from the logs:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

Para un backend Postgres multiinquilino, define las contraseñas y superpón el override de Postgres:

```bash
cp deploy/compose/.env.example deploy/compose/.env     # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

:::note[El valor por defecto del contenedor se enlaza dentro del contenedor]
El comando por defecto del contenedor se enlaza a `0.0.0.0` *dentro del contenedor* para que puedas
ponerlo detrás de tu ingress; el stack de Compose mapea el puerto del host a `127.0.0.1`.
No hay una receta de `docker run` a secas — usa Compose (o el chart de Helm) para que el volumen de
datos, los puertos y el flujo de primer arranque queden cableados correctamente.
:::

## Opción 3 — Kubernetes (Helm)

El chart de Helm en `deploy/helm/olivares` despliega el control plane como un **StatefulSet del núcleo**
(escritor único; su directorio de datos contiene la clave de firma de auditoría y el material TLS) y,
para la topología distribuida, un **DaemonSet de colectores** que empuja observaciones al núcleo
sobre **gRPC + mTLS**. La release v26.9.0 del motor no publica el chart en un registro OCI:
ninguna etiqueta independiente `chart-v*` ha ejecutado todavía ese workflow. Instala el
chart revisado desde un checkout y fija la imagen publicada por digest.

```bash
helm install olivares \
  deploy/helm/olivares \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> Cuando se publique un chart, `release-chart.yml` firmará su manifiesto OCI con cosign y no
> emitirá una capa GPG `.prov`. Ese futuro artefacto se verificará por digest; esta instalación
> desde fuente no se presenta como una descarga OCI firmada. Véase `deploy/helm/README.md`.

El chart descarga la imagen del contenedor desde Docker Hub (`docker.io/olivaresai/olivares`); la
misma imagen está también en `ghcr.io/olivaresai/olivares`, idéntica por digest; apunta
`image.repository` allí si te estorba el límite de descargas **anónimas** de Docker Hub
(ghcr.io no lo aplica a imágenes públicas). El chart sale de `deploy/helm/olivares`
hasta que tenga su propia publicación.

Despliega siempre **por digest**, nunca una etiqueta mutable. Para un clúster totalmente desconectado,
replica el bundle primero — consulta [instalación aislada de red](/how-to/air-gap-install/).

## Elegir una topología

| Topología | Cuándo | Almacén | Bus de eventos |
|---|---|---|---|
| **Binario único** | nodo único, laboratorio, estate pequeño, aislado de red | SQLite (embebido) | en proceso |
| **Distribuida** | multi-host, escala, multiinquilino | Postgres + RLS | en proceso + **puente NATS** (`OLIVARES_BUS_CONFIG`; la entrega entre nodos es, con honestidad, como mucho una vez) |
| **Aislada de red** | sin egreso permitido | SQLite o Postgres | en proceso (puente NATS opcional dentro del perímetro) |

El **data-plane (colectores) siempre se ejecuta en tu infraestructura** — el control
plane es lo único cuya ubicación de alojamiento eliges. La
[visión general de la arquitectura](/explanation/architecture/overview/) explica las disyuntivas.

## Conectar fuentes reales

Una instalación nueva tiene un estate vacío. Cablea fuentes reales (Postgres pgAudit,
CloudTrail, OpenTelemetry desde agentes, eBPF) para que el access map se pueble — consulta
[conectar una fuente](/how-to/connect-a-source/) y
[conectar Claude Code](/how-to/connect-claude-code/). Para la superficie de configuración,
consulta la [referencia de configuración](/reference/configuration/).
