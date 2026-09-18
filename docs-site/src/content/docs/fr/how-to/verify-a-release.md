---
title: Vérifier ce que vous avez téléchargé
description: >-
  Vérifiez la signature, la provenance SLSA, le SBOM et les attestations
  OpenVEX d'une release avant de l'exécuter. Ne canalisez jamais un installeur
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
| image conteneur | publiée sur GHCR et Docker Hub, vérifiée et épinglée par digest |
| source du chart Helm | installer depuis `deploy/helm/olivares` ; aucun chart OCI public à ce jour |

## Le chemin en une commande

Le dépôt fournit `scripts/verify-release.sh`, qui exécute la chaîne complète : vérifie la
signature sur `checksums.txt`, recalcule le SHA-256 de chaque artefact, puis vérifie les
attestations SBOM, OpenVEX et SLSA.

```bash
# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.9.1

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
scripts/verify-release.sh --key /path/to/your-cosign.pub
```

`--key` vérifie les signatures avec une clé publique au lieu de l'identité du workflow de release
et ignore le journal de transparence. Cela prouve que les fichiers ont été signés avec la clé
privée correspondante, pas que le projet les a publiés. Obtenez cette clé publique auprès de son
détenteur par un canal distinct des fichiers que vous vérifiez.

`--offline` retire seulement la consultation Rekor des appels cosign ; la vérification ne se passe
pas pour autant du réseau. La vérification sans clé a toujours besoin du matériel de racine de
confiance Sigstore, que cosign récupère s'il n'est pas déjà en cache, et le script n'a pas
d'option `--trusted-root`. La vérification des bundles SBOM et OpenVEX exige une racine de
confiance même avec `--key`, et l'étape SLSA exécute `slsa-verifier` sans option hors ligne.

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
