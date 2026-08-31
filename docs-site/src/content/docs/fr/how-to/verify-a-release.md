---
title: Vérifier ce que vous avez téléchargé
description: >-
  Vérifiez la signature, la provenance SLSA, le SBOM et les attestations
  OpenVEX d'une release avant de l'exécuter — en ligne (sans clé) ou
  entièrement hors ligne (avec clé). Ne canalisez jamais un installeur
  directement dans un shell.
---

Un control plane est un produit de sécurité, donc la première chose à faire avec une release est
de **prouver que c'est bien celle que le projet a publiée**. Les releases d'Olivares AI livrent
tout ce dont vous avez besoin pour vérifier cryptographiquement : une signature sur les sommes de
contrôle, une attestation de provenance SLSA, un SBOM (SPDX + CycloneDX) et une attestation
OpenVEX — toutes référencées **par empreinte (digest), jamais par tag**.

:::danger[Jamais `curl | bash`]
Ne canalisez pas un installeur dans un shell. Téléchargez les artefacts, **vérifiez-les**, et
seulement ensuite exécutez-les. Les étapes ci-dessous expliquent comment.
:::

## Ce qui est livré avec une release

| Artefact | Ce que c'est |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 de chaque artefact, avec une signature et un certificat cosign |
| `*_<os>_<arch>.tar.gz` | la ou les archives de release |
| `*.sbom.sigstore.json` | SBOM (SPDX) sous forme d'attestation in-toto signée |
| `*.vex.sigstore.json` | OpenVEX sous forme d'attestation in-toto signée |
| `*.intoto.jsonl` | provenance SLSA Build L3 |
| image conteneur + chart Helm | publiés sur un registre à la release, épinglés par empreinte |

## Le chemin en une commande

Le dépôt fournit `scripts/verify-release.sh`, qui exécute la chaîne complète : vérifie la
signature sur `checksums.txt`, recalcule le SHA-256 de chaque artefact, puis vérifie les
attestations SBOM, OpenVEX et SLSA.

```bash
# Default: keyless (Sigstore). Needs network access to the transparency log (Rekor).
scripts/verify-release.sh

# Key-based (air-gap friendly): verify against the project's public key.
scripts/verify-release.sh --key cosign.pub

# Fully offline: no Rekor / no transparency-log network at all.
scripts/verify-release.sh --key cosign.pub --offline

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.8.0
```

Avec `--offline` (ou dès qu'une clé est fournie), le script ajoute `--insecure-ignore-tlog` à
chaque appel cosign, de sorte qu'aucun réseau Sigstore/Rekor n'est utilisé — c'est le chemin pour
les environnements déconnectés.

## Ce qu'il vérifie, étape par étape

Si vous préférez exécuter les contrôles vous-même, voici ce que fait le script :

1. **Signature sur les sommes de contrôle** — sans clé, vérifiée par rapport à l'identité GitHub
   Actions du projet et à l'émetteur OIDC :

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Intégrité des artefacts** — chaque artefact téléchargé doit correspondre à `checksums.txt` :

   ```bash
   sha256sum --check checksums.txt
   ```

3. **Attestation SBOM (SPDX) :**

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **Attestation OpenVEX** (la déclaration de vulnérabilité du projet fondée sur l'atteignabilité) :

   ```bash
   cosign verify-blob-attestation --type openvex \
     --bundle <artifact>.vex.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

5. **Provenance SLSA :**

   ```bash
   slsa-verifier verify-artifact <artifact> \
     --provenance-path <artifact>.intoto.jsonl \
     --source-uri github.com/olivaresai/olivares
   ```

## Vérifier l'image conteneur

Pour l'image publiée, résolvez l'empreinte et vérifiez par rapport à l'identité GitHub Actions
(ce chemin est sans clé et nécessite le réseau) :

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

Déployez toujours l'image **par empreinte** (`@sha256:…`), jamais par un tag mutable.

## Dans un environnement air-gapped

Si vous ne pouvez pas atteindre le réseau du tout, utilisez le **bundle air-gap**, qui embarque
une clé publique et vérifie tout hors ligne (sans Rekor). Voir
[Installer dans un environnement air-gapped](/how-to/air-gap-install/).

:::note[Note honnête sur la disponibilité des attestations]
La vérification n'est complète que dans la mesure des attestations qu'une release donnée a
réellement publiées. Le vérificateur rapporte chaque étape qu'il exécute ; si une release omet un
artefact (par exemple un build qui n'a pas attaché de SBOM), l'étape correspondante n'a rien à
vérifier. Le workflow de release attache les artefacts SBOM, OpenVEX et SLSA nommés ci-dessus
pour le build standard.
:::
