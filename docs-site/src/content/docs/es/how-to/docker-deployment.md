---
title: Despliega con Docker
description: >-
  Descarga y verifica la imagen desde Docker Hub, y luego ejecuta el control plane en
  producción con Docker — SQLite endurecido de nodo único, Postgres multi-tenant,
  copias de seguridad DR programadas, terminación TLS en reverse proxy, actualizaciones y
  fijado por digest.
---

Esta guía es para ingenieros y SRE que ponen el control plane de Olivares AI en
producción con Docker. Todo el producto es una única imagen distroless — el motor
con la UI web embebida — de modo que un único host puede ejecutar la topología SQLite sin
dependencias externas, y un override de Postgres te da la topología multi-tenant
cuando la necesitas. Cada vía mantiene los mismos valores por defecto seguros: sin credenciales por defecto,
un token de configuración de un solo uso, TLS activado por defecto y el puerto del host enlazado a loopback.

:::note[Beta — todavía no se ha cortado ninguna release]
Olivares AI está en **beta**. Las coordenadas de imagen de abajo resuelven solo **después de que se publique la primera
release (CalVer `26.8.0`)**; hasta entonces los registries no tienen nada que descargar.
Trátalo como la forma de despliegue que vas a usar, no como una garantía lista para producción.
:::

Para la vista de página de decisión de todas las opciones de despliegue y sus valores por defecto, véase
[Autoaloja el control plane](/es/how-to/self-hosting/). Para sitios desconectados, véase
[Instala en un entorno air-gapped](/es/how-to/air-gap-install/); para scale-out, véase
la vía Kubernetes/Helm más abajo.

## 1. Descarga y verifica la imagen

La descarga principal del contenedor es **Docker Hub**:

```bash
docker pull docker.io/olivaresai/olivares:26.8.0
```

El mismo contenido también se publica en `ghcr.io/olivaresai/olivares` — idéntico por
digest, usado como copia de respaldo y como registry de build. Docker Hub limita la tasa de
descargas **anónimas**; ghcr.io no limita las descargas anónimas de imágenes públicas, así que
`docker login` o la coordenada de ghcr.io es la salida si un nodo de CI o una flota grande topa
con el límite. Las tags no llevan **ningún `v` inicial**:
`:26.8.0` fija una release, `:latest` flota, y `:26.8.0-fips` / `:26.8.0-stig` son
las variantes endurecidas. Las tags base y `:latest` son multi-arch
(`linux/amd64`, `linux/arm64`); `fips`/`stig` son solo `amd64`.

Un control plane es un producto de seguridad, así que verifica antes de ejecutar. La firma es
**sin claves (keyless)** (Sigstore) contra la identidad de GitHub Actions del proyecto, y funciona
de forma idéntica contra cualquiera de los registries — las firmas y atestaciones se copian a
Docker Hub mediante `cosign copy`, de modo que el digest es el mismo:

```bash
IMAGE=docker.io/olivaresai/olivares          # fallback: ghcr.io/olivaresai/olivares (same digest)
DIGEST="$(crane digest "$IMAGE:26.8.0")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

La cadena completa — firma de checksums, SBOM, OpenVEX, procedencia SLSA — está en
[Verifica lo que descargaste](/es/how-to/verify-a-release/). Una vez verificada, despliega por el
**digest** que verificaste, nunca por una tag mutable (véase [§8](#8-fija-por-digest-para-producción)).

## 2. Nodo único, SQLite

### Con `docker run` (endurecido)

El comando por defecto de la imagen enlaza `0.0.0.0` **dentro del contenedor** para que puedas ponerle
un ingress delante; el mapeo de puerto del lado del host de abajo fija la exposición a loopback. Ejecútalo
non-root, de solo lectura, con todas las capabilities eliminadas:

```bash
docker volume create olivares-data

docker run -d --name olivares \
  --user 65532:65532 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v olivares-data:/var/lib/olivares \
  -p 127.0.0.1:8443:8443 \
  -p 127.0.0.1:8444:8444 \
  docker.io/olivaresai/olivares:26.8.0 \
  serve \
    --listen=0.0.0.0:8443 \
    --grpc-listen=0.0.0.0:8444 \
    --data-dir=/var/lib/olivares \
    --checkpoint-interval=1h
```

| Flag | Por qué |
|---|---|
| `--user 65532:65532` | ejecuta como el UID non-root `nonroot` integrado en la imagen distroless |
| `--read-only` | el sistema de ficheros raíz es inmutable; solo el volumen de datos y `/tmp` son escribibles |
| `--tmpfs /tmp` | un tmpfs de scratch escribible, requerido porque el rootfs es de solo lectura |
| `--cap-drop ALL` | el motor no necesita ninguna capability de Linux |
| `--security-opt no-new-privileges` | bloquea la escalada de privilegios vía binarios setuid |
| `-v olivares-data:/var/lib/olivares` | persiste el directorio de datos (véase [§5](#5-notas-de-operación)) |
| `-p 127.0.0.1:8443:8443` | publica HTTPS (REST + UI web) **solo en loopback** |
| `-p 127.0.0.1:8444:8444` | publica gRPC (API de ingesta / ControlPlane) solo en loopback |

Lee el token de configuración de un solo uso desde los logs y crea el primer administrador:

```bash
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` acepta el certificado autofirmado que el motor emite en el primer arranque; reemplázalo
con un certificado real mediante un reverse proxy ([§6](#6-reverse-proxy--terminación-tls))
o tu propio material TLS. El token se muestra **una sola vez** y es de un solo uso.

### Con Docker Compose

El repositorio incluye un stack de Compose que cablea el volumen, el mapeo de puerto en loopback
y los mismos flags de endurecimiento que arriba:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

El fichero base fija por defecto la imagen a `docker.io/olivaresai/olivares:latest` (Docker Hub); para un
despliegue de producción verificable, fija `OLIVARES_IMAGE` en `deploy/compose/.env` a una
referencia fijada por digest (véase [§8](#8-fija-por-digest-para-producción)). Los datos persisten en
el volumen `olivares-data`.

## 3. Postgres multi-tenant

Para la topología multi-tenant, superpón el override de Postgres sobre el fichero base.
Fija primero las dos contraseñas, y luego levanta el stack:

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set POSTGRES_SUPERUSER_PASSWORD + OLIVARES_DB_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

El override levanta `postgres:16-alpine`, aprovisiona el rol **de mínimo privilegio**
`olivares_app` y la base de datos `olivares` en la primera inicialización (ejecutando el canónico
`deploy/postgres/01-app-role.sql` vía `initdb/10-app-role.sh`), y apunta el motor
a ese rol no-superusuario con `--engine=postgres`. Esto hace real el backstop de tenant FORCE-RLS:
el motor **se niega a arrancar** contra un rol superusuario/`BYPASSRLS`.

:::caution[`sslmode=disable` es solo para la demo en red]
El DSN del override usa `sslmode=disable` porque ambos contenedores comparten una red
Docker. **Producción usa TLS con `sslmode=verify-full`.** Para un despliegue endurecido
prefiere el chart de Helm con un Secret de DSN y un Postgres gestionado (o propio) — véase
[§8](#8-fija-por-digest-para-producción).
:::

## 4. Copias de seguridad de recuperación ante desastres

El perfil de backup produce bundles de DR programados y seguros para la continuidad del ledger: la
instantánea del store más las claves de firma, cifradas bajo tu KEK, con un manifiesto de las
puntas de cadena por tenant. Escribe tu passphrase en un fichero mantenido **fuera del repo y la
imagen**, y luego ejecuta el perfil `backup` de una sola vez:

```bash
printf 'a strong DR passphrase' > deploy/compose/dr-pass
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

El job comparte el volumen de datos del motor, escribe el bundle en el volumen `olivares-backups`,
y — como la imagen es distroless — deja la retención al host: poda los bundles antiguos
con un cron del host (`find <backups> -name '*.drbundle' -mtime +14 -delete`). Envuelve
la ejecución en un cron del host para un RPO programado y **replica el volumen `olivares-backups`
fuera del sitio** — una copia en el mismo host no es recuperación ante desastres. Restaura y verifica con:

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass
```

El procedimiento completo de RPO/RTO, custodia de claves y simulacro de DR vive con el runbook de DR
del repositorio; el recorrido de más alto nivel es [Copia de seguridad y restauración](/es/how-to/backup-and-restore/).

## 5. Notas de operación

**Sondea la salud desde el host, no desde el contenedor.** La imagen es **distroless** — no
tiene shell ni `curl`, así que intencionadamente no hay `HEALTHCHECK` dentro del contenedor.
El motor expone `/livez` y `/readyz` en el puerto HTTPS; sondéalos desde el host
(o tu orquestador):

```bash
# liveness — process is up; no dependency checks, so a store outage never restart-loops:
curl -fsS -k https://127.0.0.1:8443/livez

# readiness — store ping (and HA leadership): 200 when serving, 503 when the store is down:
curl -fsS -k https://127.0.0.1:8443/readyz
```

La accesibilidad de `/readyz` es la señal de disponibilidad — cablearla a tu
monitorización externa (véase [Monitoriza con Prometheus](/es/how-to/monitor-with-prometheus/)).

**El token de configuración solo aparece una vez, en los logs.** El primer arranque imprime un
token `olst_…` de un solo uso en la salida del contenedor. Captúralo con
`docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'` (o el equivalente de Compose) antes de
que el búfer rote; se consume cuando creas el primer administrador.

**Haz copia de seguridad del directorio de datos.** `/var/lib/olivares` (el volumen `olivares-data`) contiene
el **store SQLite, la clave de firma de auditoría y el material TLS**. Perderlo pierde
la identidad de firma del ledger y rompe la continuidad de auditoría, así que protege y respalda el
volumen — usa el perfil de DR de [§4](#4-copias-de-seguridad-de-recuperación-ante-desastres), no una copia ad-hoc
de un store en vivo.

## 6. Reverse proxy / terminación TLS

De serie el motor sirve su propio certificado **autofirmado**, que está bien
para evaluación pero no para clientes que validan la confianza. En producción, pon delante del
motor enlazado a loopback un reverse proxy que termine TLS con un
certificado proporcionado por el operador (de tu CA o ACME), y deja que el proxy sea lo único
expuesto en la red.

Como el propio motor habla TLS, el proxy se conecta a él sobre HTTPS en el
puerto de loopback. Un bloque server mínimo de nginx:

```nginx
server {
  listen 443 ssl;
  server_name olivares.example.com;

  ssl_certificate     /etc/ssl/olivares/fullchain.pem;   # operator-provided cert
  ssl_certificate_key /etc/ssl/olivares/privkey.pem;

  location / {
    proxy_pass         https://127.0.0.1:8443;   # engine's own TLS on loopback
    proxy_ssl_verify   off;                       # engine cert is self-signed
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
  }
}
```

El equivalente con Caddy, que aprovisiona un certificado público automáticamente:

```caddy
olivares.example.com {
  reverse_proxy https://127.0.0.1:8443 {
    transport http {
      tls_insecure_skip_verify   # engine cert is self-signed on loopback
    }
  }
}
```

Mantén los puertos de host del motor enlazados a `127.0.0.1` (los valores por defecto de arriba) para que solo el
proxy sea accesible. El puerto de ingesta gRPC (`8444`) es para colectores; exponlo
deliberadamente, con su propia vía TLS, solo si ejecutas la topología distribuida.

## 7. Actualizaciones

El volumen de datos persiste entre reemplazos de contenedor, así que una actualización es: copia de seguridad,
descarga la nueva tag fijada, recrea el contenedor.

```bash
# 1. Back up first (see §4).
# 2. Pull the new release and re-verify it (see §1):
docker pull docker.io/olivaresai/olivares:26.8.1

# docker run:
docker stop olivares && docker rm olivares
# re-run the §2 command with the new tag — the olivares-data volume is reused.

# Compose: set OLIVARES_IMAGE to the new digest in .env, then:
docker compose -f deploy/compose/docker-compose.yml up -d
```

Recrear el contenedor no toca el volumen con nombre, así que el store, la clave de firma
y el material TLS se conservan. **Haz siempre copia de seguridad antes de actualizar**, y re-verifica la nueva
imagen antes de recrear.

## 8. Fija por digest para producción

Las tags mutables (`:26.8.0`, `:latest`) son para evaluación. En producción, fija el
**digest** que verificaste — un digest es inmutable y es exactamente lo que aprobaste:

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

Para Compose, fija la referencia de digest en `deploy/compose/.env`:

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

Para scale-out y multi-nodo, usa el chart de Helm — publicado como artefacto OCI en
`oci://ghcr.io/olivaresai/charts/olivares`, firmado con cosign, y fijado por digest de imagen.
Véase [Autoaloja el control plane](/es/how-to/self-hosting/) para el comando del chart e
[Instala en un entorno air-gapped](/es/how-to/air-gap-install/) para sitios totalmente
desconectados.
