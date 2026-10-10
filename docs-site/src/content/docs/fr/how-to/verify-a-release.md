---
title: Vérifier ce que vous avez téléchargé
description: >-
  Vérifiez la signature, la provenance SLSA, le SBOM et les attestations
  OpenVEX d'une release avant de l'exécuter. Ne canalisez jamais un installeur
  directement dans un shell.
---

> Les paquets de déploiement sont fournis par le canal Business ; leur publication n’est pas vérifiée ici. Vérifiez le paquet du chart et son éditeur selon les instructions du canal avant d’utiliser le chart local. L’exemple de manifeste utilise un fichier Business nommé `business-install.yaml`. L’installation isolée nécessite Enterprise.


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.

La prochaine version est <!-- release -->`0.1`<!-- /release --> ; sa release GitHub n’est pas encore publiée. Les commandes ci-dessous décrivent les artefacts prévus. Compilez depuis les sources jusqu’à la publication, puis vérifiez chaque artefact avant utilisation. L’état observé figure dans <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->.

Un control plane est un produit de sécurité, donc la première chose à faire avec une release est
de **prouver que c'est bien celle que le projet a publiée**. Les releases d'Olivares AI livrent
tout ce dont vous avez besoin pour vérifier cryptographiquement : une signature sur les sommes de
contrôle, une attestation de provenance SLSA, ainsi qu'un SBOM (SPDX) et un document OpenVEX,
tous deux attestés sur l'image conteneur — toutes référencées **par empreinte (digest), jamais par tag**.

:::danger[Jamais `curl | bash`]
Ne canalisez pas un installeur dans un shell. Téléchargez les artefacts, **vérifiez-les**, et
seulement ensuite exécutez-les. Les étapes ci-dessous expliquent comment.
:::

## Ce qui est livré avec une release

| Artefact | Ce que c'est |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 des archives, des paquets, de l'installeur, de `release-commit.txt` et de `release-build-context.json`, avec une signature et un certificat cosign |
| `*_<os>_<arch>.tar.gz` | la ou les archives de release |
| `olivares.spdx.sbom.json` | le SBOM (SPDX) de la release, généré à partir de l'image conteneur et attesté sur celle-ci par digest |
| `olivares.vex.openvex.json` | l'OpenVEX de la release, attesté sur l'image conteneur par digest |
| `*.intoto.jsonl` | provenance SLSA Build L3 |
| image conteneur | publiée sur GHCR et Docker Hub, vérifiée et épinglée par digest |
| source du chart Helm | installer depuis `./business-chart` ; sa publication OCI n'est pas vérifiée (`publication-unverified` : jamais publié depuis ce dépôt) |

Les releases jusqu'à 26.10.1<!-- release-fixed --> portent à la place un bundle SBOM et un bundle OpenVEX par archive
(`*.sbom.sigstore.json`, `*.vex.sigstore.json`) ; les releases ultérieures non.

## Le chemin en une commande

Le dépôt fournit `scripts/verify-release.sh`, qui exécute la chaîne complète : vérifie la
signature sur `checksums.txt`, recalcule le SHA-256 de chaque artefact, puis vérifie les
bundles SBOM et OpenVEX par archive des releases jusqu'à 26.10.1<!-- release-fixed --> (les releases ultérieures
signalent ces deux étapes comme ignorées) et la provenance SLSA.

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

`--key` vérifie les signatures avec une clé publique au lieu de l'identité du workflow de release
et ignore le journal de transparence. Cela prouve que les fichiers ont été signés avec la clé
privée correspondante, pas que le projet les a publiés. Obtenez cette clé publique auprès de son
détenteur par un canal distinct des fichiers que vous vérifiez.

`--offline` retire seulement la consultation Rekor des appels cosign ; la vérification ne se passe
pas pour autant du réseau. La vérification sans clé a toujours besoin du matériel de racine de
confiance Sigstore, que cosign récupère s'il n'est pas déjà en cache, et le script n'a pas
d'option `--trusted-root`. La vérification des bundles SBOM et OpenVEX par archive (releases jusqu'à 26.10.1<!-- release-fixed -->) exige une racine de
confiance même avec `--key`, et l'étape SLSA exécute `slsa-verifier` sans option hors ligne.

## Ce qu'il vérifie, étape par étape

Si vous préférez exécuter les contrôles vous-même, voici ce que fait le script :

1. **Signature sur les sommes de contrôle** — sans clé, vérifiée par rapport à l'identité GitHub
   Actions du projet et à l'émetteur OIDC :

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Intégrité des artefacts** — chaque artefact téléchargé doit correspondre à `checksums.txt` :

   ```bash
   # checksums.txt lists all release artifacts; skip files you did not download.
   # Before use, ensure each downloaded artifact is listed and reports OK.
   # GNU sha256sum fails if no listed file is present or a checksum mismatches.
   sha256sum --check --ignore-missing checksums.txt
   ```

3. **Attestation SBOM (SPDX)** — bundle par archive, releases jusqu'à 26.10.1<!-- release-fixed --> uniquement. Les
   releases ultérieures portent un SBOM attesté sur l'image conteneur ; voir *Vérifier l'image conteneur* ci-dessous.

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **Attestation OpenVEX** (la déclaration de vulnérabilité du projet fondée sur l'atteignabilité) —
   bundle par archive, releases jusqu'à 26.10.1<!-- release-fixed --> uniquement. Les releases ultérieures portent un
   OpenVEX attesté sur l'image conteneur ; voir *Vérifier l'image conteneur* ci-dessous.

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
(ce chemin est sans clé et nécessite le réseau). Les attestations `spdxjson` et `openvex` sont
le SBOM (`olivares.spdx.sbom.json`) et l'OpenVEX (`olivares.vex.openvex.json`) de la release,
attestés sur l'image par digest :

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

Déployez toujours l'image **par empreinte** (`@sha256:…`), jamais par un tag mutable.

## Dans un environnement air-gapped

Un opérateur construit le **bundle air-gap** avec sa propre clé cosign, et le bundle embarque le
fichier `cosign.pub` correspondant. Ses scripts vérifient le chart Helm et les images
enregistrées avec cette clé, sans Rekor ; une image n'est acceptée que si elle porte une signature
faite avec cette clé. Une clé lue dans le bundle qu'elle sert à vérifier n'authentifie pas ce
bundle : avant de faire confiance au bundle, comparez-la à une copie que le détenteur de la clé
vous a remise par un canal distinct. Voir
[Installer dans un environnement air-gapped](/how-to/air-gap-install/).

:::note[Note honnête sur la disponibilité des attestations]
La vérification n'est complète que dans la mesure des attestations qu'une release donnée a
réellement publiées. Le vérificateur rapporte chaque étape qu'il exécute ; si une release omet un
artefact (par exemple un build qui n'a pas attaché de SBOM), l'étape correspondante n'a rien à
vérifier. Le workflow de release attache les artefacts SBOM, OpenVEX et SLSA nommés ci-dessus
pour le build standard.
:::
