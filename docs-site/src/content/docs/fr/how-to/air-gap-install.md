---
title: Installer dans un environnement air-gapped
description: >-
  Transportez un bundle de release signé de l'autre côté de la coupure, vérifiez
  chaque image et le chart Helm entièrement hors ligne, mettez-les en miroir
  dans un registre privé par digest, et installez — sans appel sortant du côté déconnecté.
---

Olivares AI est **conçu d'abord pour l'auto-hébergement et prêt pour
l'air-gap**. Ce guide fait passer une release signée à travers une coupure
réseau avec **aucun réseau côté déconnecté** : vous vérifiez chaque image et le
chart Helm hors ligne face à une clé publique, vous les mettez en miroir dans
votre registre privé **par digest**, puis vous installez. Le produit ne fait
**aucun appel sortant obligatoire au démarrage**, donc rien à l'intérieur de la
coupure n'atteint internet. La seule commande qui atteindrait un point de
terminaison de l'éditeur est `olivares upgrade` ; `--endpoint` ou `--bundle` la
dirige vers votre propre miroir.

Le flux a deux côtés :

1. **En ligne, une fois** — un mainteneur construit un bundle autonome.
2. **À l'intérieur de la coupure** — vous le vérifiez hors ligne et le mettez en
   miroir dans votre registre.

Cette page documente comment **utiliser** le bundle et les scripts livrés ; elle
ne reconstruit pas le pipeline de release.

## 1. Construire le bundle (en ligne, une fois)

Sur une machine connectée, `scripts/airgap-bundle.sh` récupère chaque image
**figée par digest**, empaquette et signe le chart Helm, rassemble le
SBOM/OpenVEX/provenance, et émet une seule archive tarball avec un `VERIFY.md` :

```bash
scripts/airgap-bundle.sh \
  --version v26.8.0 \
  --image docker.io/olivaresai/olivares:26.8.0-amd64 \
  --chart deploy/helm/olivares \
  --cosign-key cosign.key \
  [--collector-image <ref>] [--out dist/airgap] [--gpg-key <id>]
```

L'image est récupérée depuis Docker Hub par sa coordonnée officielle
(`docker.io/olivaresai/olivares`) ; le même contenu se trouve aussi sur
`ghcr.io/olivaresai/olivares`, identique par digest, si vous préférez le mettre
en miroir depuis là. Docker Hub limite le débit des pulls **anonymes** ; ghcr.io ne le fait
pas pour les images publiques, ce qui aide sur un hôte de build non authentifié.

:::caution[Le SBOM/VEX/provenance sont fournis, pas générés]
Le bundler copie le SBOM, l'OpenVEX et la provenance dans le bundle **au mieux à
partir de variables d'environnement** (`OLIVARES_SBOM_FILES`,
`OLIVARES_VEX_FILES`, `OLIVARES_PROV_FILES`). Si elles ne sont pas définies, les
répertoires `sbom/`, `vex/` et `prov/` du bundle sont vides — définissez-les
pour que votre site déconnecté reçoive les attestations.
:::

### Ce que contient le bundle

```text
images/<name>/   cosign-saved image + its signatures/attestations (offline)
chart/<chart>.tgz   packaged Helm chart  (+ .tgz.sig cosign, + .prov if gpg)
sbom/  vex/  prov/   SBOM, OpenVEX and SLSA provenance for the release
cosign.pub          the public key to verify everything offline (key mode)
digests.txt         the pinned digest of every image (the manifest of record)
VERIFY.md           the exact offline verification + mirror walkthrough
```

Le bundle transporte également des copies de `airgap-mirror.sh` et
`verify-release.sh`, de sorte que le côté déconnecté n'a besoin de rien venant
du réseau.

## 2. Vérifier et mettre en miroir à l'intérieur de la coupure

Côté déconnecté, vous n'avez besoin que de `cosign`, `crane`, `helm` et `tar` —
ainsi que d'un **registre privé** accessible. Pas d'internet.

### Vérifier chaque image hors ligne (sans journal de transparence)

```bash
for d in images/*/; do
  cosign verify --local-image "$d" --insecure-ignore-tlog --key cosign.pub
done
```

`--insecure-ignore-tlog` ignore le journal de transparence en ligne de Sigstore ;
la confiance provient du `cosign.pub` fourni dans le bundle. (Ce n'est *pas* la
même chose que le flag keyless `--offline` — en mode clé, la racine de confiance
hors ligne est la clé publique.)

### Vérifier le chart Helm hors ligne

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --signature chart/*.tgz.sig chart/*.tgz
# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz
# (needs the signer's GPG public key in your keyring)
```

### Mettre en miroir dans votre registre privé par digest

`scripts/airgap-mirror.sh` vérifie chaque image hors ligne, la charge dans votre
registre, et **la refige par digest** pour confirmer que le digest a survécu à la
mise en miroir (il utilise `crane` et `cosign load` — et **non** `oras`) :

```bash
scripts/airgap-mirror.sh \
  --bundle olivares-airgap-v26.8.0.tar.gz \
  --registry registry.internal:5000 [--insecure]
```

### Installer par digest, jamais par tag

```bash
helm install olivares \
  oci://registry.internal:5000/charts/olivares \
  --version <chart-version> \
  --set image.repository=registry.internal:5000/olivares \
  --set image.digest=<digest-from-digests.txt>
```

Installez toujours à partir du **digest** présent dans `digests.txt`, jamais d'un
tag mutable — un digest est immuable et c'est ce que vous avez vérifié.

## À l'intérieur de la coupure, rien n'appelle vers l'extérieur

> Le moteur ne fait **aucun appel sortant obligatoire au démarrage** (il se lie à
> la loopback par défaut), donc rien à l'intérieur de la coupure n'atteint internet.
> `olivares upgrade` est la seule commande qui atteindrait un point de terminaison
> de l'éditeur ; `--endpoint` ou `--bundle` la dirige vers votre propre miroir.

La licence est validée **hors ligne** (une signature Ed25519, pas de serveur de
licence), et aucune des étapes de vérification ou d'installation ci-dessus ne
touche internet une fois le bundle passé de l'autre côté de la coupure. Il n'y a
aucun défaut de télémétrie-maison à désactiver.

C'est du côté **en ligne** que l'éditeur est contacté, et c'est voulu : construire le
bundle télécharge la release, et pour un parc commercial l'abonnement est le
justificatif d'accès avec lequel les add-ons, leurs mises à jour et leurs correctifs
sont récupérés. C'est le modèle SUSE/Novell — un parc air-gapped est servi depuis un
miroir local qui porte toujours l'entitlement. Voir
[auto-hébergement](/fr/how-to/self-hosting/).

:::note[Valeurs par défaut d'écoute : conteneur vs. binaire]
Exécuté directement, le binaire se lie à la **loopback** par défaut. La commande
par défaut de l'**image conteneur** de release se lie à `0.0.0.0` à l'intérieur
du conteneur afin que vous puissiez le placer derrière votre ingress/service —
il s'agit d'une liaison intra-conteneur, pas d'un appel sortant. Définissez
explicitement vos adresses d'écoute pour votre déploiement.
:::

## Variantes FIPS / STIG

Des variantes de build durcies existent (un build en mode FIPS liant le module
cryptographique Go validé CMVP, et une image orientée STIG). Elles sont
**post-v1** et portent leur propre registre d'honnêteté — notamment, **aucune ATO
FedRAMP/DoD n'est revendiquée**, et seule la version du module spécifiquement
validée doit être représentée comme validée. Considérez-les comme
disponibles-mais-pas-encore-v1 plutôt que comme une offre certifiée.

## Voir aussi

- [Vérifier ce que vous avez téléchargé](/how-to/verify-a-release/) — la chaîne
  de vérification non air-gapped (signature, SBOM, OpenVEX, SLSA).
- [Auto-héberger le control plane](/how-to/self-hosting/) — les parcours binaire
  unique, Compose et Kubernetes, et leurs valeurs par défaut sécurisées.
