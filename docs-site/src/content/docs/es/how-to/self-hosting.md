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
| **Bind** | el binario se enlaza a **loopback** por defecto; expónlo de forma deliberada. |
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
browser will warn once; that is expected. The token is shown ONCE and is
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

El chart de Helm firmado despliega el control plane como un **StatefulSet del núcleo**
(escritor único; su directorio de datos contiene la clave de firma de auditoría y el material TLS) y,
para la topología distribuida, un **DaemonSet de colectores** que empuja observaciones al núcleo
sobre **gRPC + mTLS**. En la publicación, el chart se publica en un registro OCI y se firma con cosign,
así que verificas en la instalación y fijas por digest. (La primera publicación sigue siendo un
**borrador**: hasta que se corte una etiqueta `chart-v*` la ruta del registro está vacía, así que el
comando de abajo es la ruta que usarás una vez se publique una release.)

```bash
helm install olivares \
  oci://ghcr.io/olivaresai/charts/olivares \
  --version <chart-version> \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> El chart publicado está **firmado con cosign sobre el manifiesto OCI**, no con GPG: la pipeline
> de release no emite capa `.prov`, así que `helm --verify` no puede comprobarlo. Verifica con
> `cosign verify` contra la identidad `release-chart.yml@refs/tags/chart-v*` — ver
> `deploy/helm/README.md`.

El chart descarga la imagen del contenedor desde Docker Hub (`docker.io/olivaresai/olivares`); la
misma imagen está también en `ghcr.io/olivaresai/olivares`, idéntica por digest; apunta
`image.repository` allí si te estorba el límite de descargas **anónimas** de Docker Hub
(ghcr.io no lo aplica a imágenes públicas). El propio artefacto del **chart** permanece en
`oci://ghcr.io/olivaresai/charts/olivares`.

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
