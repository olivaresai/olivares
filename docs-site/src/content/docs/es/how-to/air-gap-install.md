---
title: Instalar en un entorno air-gapped
description: >-
  Lleva un bundle de release firmado al otro lado de la brecha, verifica cada
  imagen y el chart de Helm por completo offline, refléjalos en un registro
  privado por digest e instala — sin llamadas salientes en el lado desconectado.
---

Olivares AI es **self-host-first y air-gap-ready**. Esta guía lleva una release
firmada a través de una brecha de aire **sin red en el lado desconectado**:
verificas cada imagen y el chart de Helm offline contra una clave pública, los
reflejas en tu registro privado **por digest** e instalas. El producto **no realiza
llamadas salientes obligatorias en el arranque**, así que nada dentro de la brecha
llega a internet. El único comando que alcanzaría un endpoint del fabricante es
`olivares upgrade`; `--endpoint` o `--bundle` lo apuntan a tu propio mirror.

El flujo tiene dos lados:

1. **Online, una vez** — un mantenedor construye un bundle autocontenido.
2. **Dentro de la brecha** — lo verificas offline y lo reflejas en tu registro.

Esta página documenta cómo **usar** el bundle y los scripts entregados; no reconstruye
el pipeline de release.

## 1. Construir el bundle (online, una vez)

En una máquina conectada, `scripts/airgap-bundle.sh` descarga cada imagen **fijada por
digest**, empaqueta y firma el chart de Helm, reúne el SBOM/OpenVEX/procedencia y emite
un único tarball con un `VERIFY.md`:

```bash
scripts/airgap-bundle.sh \
  --version v26.8.0 \
  --image docker.io/olivaresai/olivares:26.8.0-amd64 \
  --chart deploy/helm/olivares \
  --cosign-key cosign.key \
  [--collector-image <ref>] [--out dist/airgap] [--gpg-key <id>]
```

La imagen se descarga de Docker Hub por su coordenada oficial (`docker.io/olivaresai/olivares`);
el mismo contenido está también en `ghcr.io/olivaresai/olivares`, idéntico por digest,
si prefieres reflejar desde allí. Docker Hub limita la tasa de descargas **anónimas** y ghcr.io
no la limita para imágenes públicas, lo que ayuda en un host de construcción sin autenticar.

:::caution[El SBOM/VEX/procedencia se suministran, no se generan]
El bundler copia el SBOM, OpenVEX y la procedencia en el bundle **best-effort a partir
de variables de entorno** (`OLIVARES_SBOM_FILES`, `OLIVARES_VEX_FILES`,
`OLIVARES_PROV_FILES`). Si no están definidas, los directorios `sbom/`, `vex/` y `prov/`
del bundle quedan vacíos — defínelas para que tu sitio desconectado reciba las
atestaciones.
:::

### Qué contiene el bundle

```text
images/<name>/   cosign-saved image + its signatures/attestations (offline)
chart/<chart>.tgz   packaged Helm chart  (+ .tgz.sig cosign, + .prov if gpg)
sbom/  vex/  prov/   SBOM, OpenVEX and SLSA provenance for the release
cosign.pub          the public key to verify everything offline (key mode)
digests.txt         the pinned digest of every image (the manifest of record)
VERIFY.md           the exact offline verification + mirror walkthrough
```

El bundle también lleva copias de `airgap-mirror.sh` y `verify-release.sh`, de modo que
el lado desconectado no necesita nada de la red.

## 2. Verificar y reflejar dentro de la brecha

En el lado desconectado solo necesitas `cosign`, `crane`, `helm` y `tar` — y un
**registro privado** accesible. Nada de internet.

### Verificar cada imagen offline (sin transparency log)

```bash
for d in images/*/; do
  cosign verify --local-image "$d" --insecure-ignore-tlog --key cosign.pub
done
```

`--insecure-ignore-tlog` omite el transparency log online de Sigstore; la confianza
proviene del `cosign.pub` incluido. (Esto *no* es lo mismo que el flag keyless
`--offline` — en modo clave la raíz de confianza offline es la clave pública.)

### Verificar el chart de Helm offline

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --signature chart/*.tgz.sig chart/*.tgz
# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz
# (needs the signer's GPG public key in your keyring)
```

### Reflejar en tu registro privado por digest

`scripts/airgap-mirror.sh` verifica cada imagen offline, la carga en tu registro y
**la vuelve a fijar por digest** para confirmar que el digest sobrevivió al reflejo
(usa `crane` y `cosign load` — **no** `oras`):

```bash
scripts/airgap-mirror.sh \
  --bundle olivares-airgap-v26.8.0.tar.gz \
  --registry registry.internal:5000 [--insecure]
```

### Instalar por digest, nunca por tag

```bash
helm install olivares \
  oci://registry.internal:5000/charts/olivares \
  --version <chart-version> \
  --set image.repository=registry.internal:5000/olivares \
  --set image.digest=<digest-from-digests.txt>
```

Instala siempre desde el **digest** de `digests.txt`, nunca un tag mutable — un digest
es inmutable y es lo que verificaste.

## Dentro de la brecha nada llama al exterior

> El motor **no realiza llamadas salientes obligatorias en el arranque** (se enlaza a
> loopback por defecto), así que nada dentro de la brecha llega a internet.
> `olivares upgrade` es el único comando que alcanzaría un endpoint del fabricante;
> `--endpoint` o `--bundle` lo apuntan a tu propio mirror.

La licencia se valida **offline** (una firma Ed25519, sin servidor de licencias), y
ninguno de los pasos de verificación o instalación anteriores toca internet una vez que
el bundle está al otro lado de la brecha. No hay un comportamiento de telemetría-home
por defecto que desactivar.

Al proveedor se le llega por el lado **online**, y es a propósito: construir el bundle
descarga la release, y en un parque comercial la suscripción es la credencial con la
que se obtienen los add-ons, sus actualizaciones y sus parches. Ése es el modelo
SUSE/Novell — un parque air-gapped se sirve desde un mirror local que sigue llevando
el entitlement. Consulta [autoalojamiento](/es/how-to/self-hosting/).

:::note[Defaults de escucha: contenedor frente a binario]
Ejecutado directamente, el binario se enlaza a **loopback** por defecto. El comando por
defecto de la **imagen de contenedor** de la release se enlaza a `0.0.0.0` dentro del
contenedor para que puedas anteponerle tu ingress/service — eso es un enlace
intra-contenedor, no una llamada saliente. Define tus direcciones de escucha de forma
explícita para tu despliegue.
:::

## Variantes FIPS / STIG

Existen variantes de compilación endurecidas (una compilación en modo FIPS que enlaza el
módulo criptográfico de Go validado por CMVP, y una imagen orientada a STIG). Son
**post-v1** y llevan su propio registro de honestidad — en particular, **no se afirma
ningún FedRAMP/DoD ATO**, y solo la versión del módulo específicamente validada debe
representarse como validada. Trátalas como disponibles-pero-aún-no-v1, no como una
oferta certificada.

## Véase también

- [Verifica lo que has descargado](/how-to/verify-a-release/) — la cadena de
  verificación no air-gapped (firma, SBOM, OpenVEX, SLSA).
- [Autoaloja el control plane](/how-to/self-hosting/) — los caminos de binario único,
  Compose y Kubernetes y sus defaults seguros.
