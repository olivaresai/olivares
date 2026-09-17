---
title: Verifica lo que has descargado
description: >-
  Verifica la firma de una release, su procedencia SLSA, el SBOM y las
  atestaciones OpenVEX antes de ejecutarla. Nunca canalices un instalador
  directamente a un shell.
---

Un control plane es un producto de seguridad, así que lo primero que deberías hacer con una
release es **demostrar que es la que el proyecto publicó**. Las releases de Olivares AI incluyen
todo lo necesario para verificarlas criptográficamente: una firma sobre los checksums, una
atestación de procedencia SLSA, un SBOM (SPDX + CycloneDX) y una atestación
OpenVEX — todo referenciado **por digest, nunca por etiqueta**.

:::danger[Nunca `curl | bash`]
No canalices un instalador a un shell. Descarga los artefactos, **verifícalos** y
solo entonces ejecútalos. Los pasos de abajo explican cómo.
:::

## Qué incluye una release

| Artefacto | Qué es |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 de cada artefacto, con una firma y certificado de cosign |
| `*_<os>_<arch>.tar.gz` | el/los archivo(s) de la release |
| `*.sbom.sigstore.json` | SBOM (SPDX) como atestación in-toto firmada |
| `*.vex.sigstore.json` | OpenVEX como atestación in-toto firmada |
| `*.intoto.jsonl` | procedencia SLSA Build L3 |
| imagen del contenedor | publicada en GHCR y Docker Hub, verificada y fijada por digest |
| fuente del chart de Helm | instalar desde `deploy/helm/olivares`; aún no hay chart OCI público |

## La ruta de un solo comando

El repositorio incluye `scripts/verify-release.sh`, que ejecuta la cadena completa:
verifica la firma sobre `checksums.txt`, recalcula el SHA-256 de cada artefacto,
y luego verifica las atestaciones de SBOM, OpenVEX y SLSA.

```bash
# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.9.0

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
scripts/verify-release.sh --key /path/to/your-cosign.pub
```

`--key` comprueba las firmas contra una clave pública en lugar de la identidad del workflow de
release e ignora el registro de transparencia. Demuestra que los archivos se firmaron con la
clave privada correspondiente, no que el proyecto los publicara. Obtén esa clave pública de su
propietario por un canal separado de los archivos que verificas.

`--offline` quita solo la consulta a Rekor de las llamadas de cosign; no hace que la verificación
funcione sin red. La verificación sin clave sigue necesitando el material de raíz de confianza de
Sigstore, que cosign descarga salvo que ya esté en caché, y el script no tiene la opción
`--trusted-root`. La comprobación de los bundles de SBOM y OpenVEX necesita una raíz de confianza
incluso con `--key`, y el paso de SLSA ejecuta `slsa-verifier` sin ninguna opción offline.

## Qué comprueba, paso a paso

Si prefieres ejecutar las comprobaciones tú mismo, esto es lo que hace el script:

1. **Firma sobre los checksums** — sin clave, verificada contra la identidad de GitHub
   Actions del proyecto y el emisor OIDC:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Integridad de los artefactos** — cada artefacto descargado debe coincidir con `checksums.txt`:

   ```bash
   sha256sum --check checksums.txt
   ```

3. **Atestación de SBOM (SPDX):**

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **Atestación OpenVEX** (la declaración de vulnerabilidades del proyecto basada en alcanzabilidad):

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
(esta ruta es sin clave y necesita red):

```bash
IMAGE=docker.io/olivaresai/olivares
DIGEST="$(crane digest "$IMAGE:<version>")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
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
