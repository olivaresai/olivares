---
title: Verifica lo que has descargado
description: >-
  Verifica la firma de una release, su procedencia SLSA, el SBOM y las
  atestaciones OpenVEX antes de ejecutarla. Nunca canalices un instalador
  directamente a un shell.
---

> Los paquetes de despliegue se suministran por el canal Business; su publicación no está verificada aquí. Verifica el paquete del chart y su editor con las instrucciones del canal antes de usar el chart local. El ejemplo de manifiesto usa un archivo Business llamado `business-install.yaml`. La instalación aislada requiere Enterprise.


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.

La próxima release es <!-- release -->`0.1`<!-- /release -->; todavía no está publicada en GitHub. Los comandos siguientes describen los artefactos previstos. Compila desde el código fuente hasta su publicación y verifica cada artefacto antes de usarlo. El estado observado está en <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->.

Un control plane es un producto de seguridad, así que lo primero que deberías hacer con una
release es **demostrar que es la que el proyecto publicó**. Las releases de Olivares AI incluyen
todo lo necesario para verificarlas criptográficamente: una firma sobre los checksums, una
atestación de procedencia SLSA, y un SBOM (SPDX) y un documento OpenVEX, ambos atestados
a la imagen del contenedor — todo referenciado **por digest, nunca por etiqueta**.

:::danger[Nunca `curl | bash`]
No canalices un instalador a un shell. Descarga los artefactos, **verifícalos** y
solo entonces ejecútalos. Los pasos de abajo explican cómo.
:::

## Qué incluye una release

| Artefacto | Qué es |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 de los archivos, paquetes, instalador, `release-commit.txt` y `release-build-context.json`, con una firma y certificado de cosign |
| `*_<os>_<arch>.tar.gz` | el/los archivo(s) de la release |
| `olivares.spdx.sbom.json` | el SBOM (SPDX) de la release, generado a partir de la imagen del contenedor y atestado a ella por digest |
| `olivares.vex.openvex.json` | el OpenVEX de la release, atestado a la imagen del contenedor por digest |
| `*.intoto.jsonl` | procedencia SLSA Build L3 |
| imagen del contenedor | publicada en GHCR y Docker Hub, verificada y fijada por digest |
| fuente del chart de Helm | instalar desde `./business-chart`; su publicación OCI no está verificada (`publication-unverified`: nunca publicado desde este repositorio) |

Las releases hasta 26.10.1<!-- release-fixed --> incluyen en su lugar un bundle de SBOM y otro de OpenVEX por archivo
(`*.sbom.sigstore.json`, `*.vex.sigstore.json`); las releases posteriores no.

## La ruta de un solo comando

El repositorio incluye `scripts/verify-release.sh`, que ejecuta la cadena completa:
verifica la firma sobre `checksums.txt`, recalcula el SHA-256 de cada artefacto,
y luego verifica los bundles de SBOM y OpenVEX por archivo de las releases hasta 26.10.1<!-- release-fixed -->
(las releases posteriores informan estos dos pasos como omitidos) y la procedencia SLSA.

<!-- release -->
```bash
# The verifier is in a source checkout of the release tag, not a release asset; running it
# trusts the checkout. Without one, INSTALL.md shows the cosign + sha256sum commands.
# Run it from the directory that holds the downloaded files.

# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
/path/to/olivares/scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
/path/to/olivares/scripts/verify-release.sh --source-tag 0.1

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
/path/to/olivares/scripts/verify-release.sh --key /path/to/your-cosign.pub
```
<!-- /release -->

`--key` comprueba las firmas contra una clave pública en lugar de la identidad del workflow de
release e ignora el registro de transparencia. Demuestra que los archivos se firmaron con la
clave privada correspondiente, no que el proyecto los publicara. Obtén esa clave pública de su
propietario por un canal separado de los archivos que verificas.

`--offline` quita solo la consulta a Rekor de las llamadas de cosign; no hace que la verificación
funcione sin red. La verificación sin clave sigue necesitando el material de raíz de confianza de
Sigstore, que cosign descarga salvo que ya esté en caché, y el script no tiene la opción
`--trusted-root`. La comprobación de los bundles de SBOM y OpenVEX por archivo (releases hasta 26.10.1<!-- release-fixed -->) necesita una raíz de confianza
incluso con `--key`, y el paso de SLSA ejecuta `slsa-verifier` sin ninguna opción offline.

## Qué comprueba, paso a paso

Si prefieres ejecutar las comprobaciones tú mismo, esto es lo que hace el script:

1. **Firma sobre los checksums** — sin clave, verificada contra la identidad de GitHub
   Actions del proyecto y el emisor OIDC:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Integridad de los artefactos** — cada artefacto descargado debe coincidir con `checksums.txt`:

   ```bash
   # checksums.txt lists all release artifacts; skip files you did not download.
   # Before use, ensure each downloaded artifact is listed and reports OK.
   # GNU sha256sum fails if no listed file is present or a checksum mismatches.
   sha256sum --check --ignore-missing checksums.txt
   ```

3. **Atestación de SBOM (SPDX)** — bundle por archivo, solo releases hasta 26.10.1<!-- release-fixed -->. Las
   releases posteriores incluyen un SBOM atestado a la imagen del contenedor; consulta *Verificar la imagen del contenedor* más abajo.

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **Atestación OpenVEX** (la declaración de vulnerabilidades del proyecto basada en alcanzabilidad) —
   bundle por archivo, solo releases hasta 26.10.1<!-- release-fixed -->. Las releases posteriores incluyen un OpenVEX
   atestado a la imagen del contenedor; consulta *Verificar la imagen del contenedor* más abajo.

   ```bash
   cosign verify-blob-attestation --type openvex \
     --bundle <artifact>.vex.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

5. **Procedencia SLSA:**

   ```bash
   slsa-verifier verify-artifact <artifact> \
     --provenance-path <artifact>.intoto.jsonl \
     --source-uri github.com/olivaresai/olivares
   ```

## Verificar la imagen del contenedor

Para la imagen publicada, resuelve el digest y verifica contra la identidad de GitHub Actions
(esta ruta es sin clave y necesita red). Las atestaciones `spdxjson` y `openvex` son el SBOM
(`olivares.spdx.sbom.json`) y el OpenVEX (`olivares.vex.openvex.json`) de la release, atestados
a la imagen por digest:

```bash
IMAGE=docker.io/olivaresai/olivares
DIGEST="$(crane digest "$IMAGE:<version>")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
slsa-verifier verify-image "$REF" \
  --source-uri github.com/olivaresai/olivares --source-tag <version>
```

Despliega siempre la imagen **por digest** (`@sha256:…`), nunca por una etiqueta mutable.

## En un entorno aislado de red

Un operador construye el **bundle aislado de red** con una clave cosign propia, y el bundle
incluye la `cosign.pub` correspondiente. Sus scripts comprueban el chart de Helm y las imágenes
guardadas contra esa clave sin Rekor; una imagen solo pasa si lleva una firma hecha con esa clave.
Una clave tomada del mismo bundle que verifica no autentica ese bundle: antes de confiar en el
bundle, compárala con una copia que el propietario de la clave te haya entregado por un canal
separado. Consulta [Instala en un entorno aislado de red](/how-to/air-gap-install/).

:::note[Nota honesta sobre la disponibilidad de atestaciones]
La verificación es solo tan completa como las atestaciones que una release concreta haya
publicado realmente. El verificador informa de cada paso que ejecuta; si una release omite un
artefacto (por ejemplo una build que no adjuntó un SBOM), el paso correspondiente no tiene nada
que comprobar. El workflow de release adjunta los artefactos de SBOM, OpenVEX y SLSA nombrados
arriba para la build estándar.
:::
