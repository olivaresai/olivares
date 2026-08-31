> Traduction automatique. La version anglaise fait foi.

# ADR-0015: Chaîne d'approvisionnement — publications signées, SBOM, provenance SLSA, OpenVEX, distroless

- **Status:** accepted
- **Date:** 2026-06
- **Décideurs :** Fran Olivares
- **Références :** décisions de stack (T6/T7) ; conception de la chaîne d'approvisionnement et de la vérification des publications

## Contexte et énoncé du problème

Pour un produit de sécurité, une publication non signée ou non vérifiable est
inacceptable. Les acheteurs doivent pouvoir vérifier *ce qu'ils ont téléchargé* — y
compris entièrement hors ligne, dans des environnements air-gapped — et connaître la
provenance ainsi que le statut des vulnérabilités connues de chaque artefact.

## Facteurs de décision

- Vérifiabilité cryptographique de chaque artefact, en ligne et hors ligne.
- Provenance (qui l'a construit, à partir de quelle source) et une nomenclature logicielle (SBOM).
- Une image d'exécution minimale et épinglée.

## Options envisagées

- **Signatures cosign/sigstore + SBOM (syft) + provenance SLSA Build L3 (SLSA v1.2) + OpenVEX +
  images distroless épinglées par digest**, avec un chemin de vérification hors ligne et
  un bundle air-gap.
- **Sommes de contrôle uniquement / publications non signées.**

## Résultat de la décision

Option retenue : **l'ensemble complet de la chaîne d'approvisionnement**. Les publications
embarquent des signatures cosign, des SBOM SPDX + CycloneDX, une provenance SLSA Build L3
et des attestations OpenVEX ; les images de conteneurs sont **distroless, épinglées par
digest**. Un script de vérification contrôle l'ensemble de la chaîne, y compris un mode
**entièrement hors ligne**, et un **bundle air-gap** embarque une clé publique afin qu'un
site déconnecté puisse tout vérifier sans journal de transparence.

### Conséquences

- **Avantages :** chaque artefact est vérifiable, en ligne ou hors ligne ; la provenance et
  un SBOM accompagnent chaque publication ; l'image d'exécution est minimale et immuable
  (par digest).
- **Inconvénients / compromis :** davantage de machinerie de publication à maintenir ; le
  bundle air-gap nécessite que le SBOM/VEX/provenance soit fourni au bundler.
- **Neutre :** le déploiement se fait toujours par digest, jamais par tag.

## Pourquoi les alternatives ont été rejetées

- **Sommes de contrôle uniquement / non signées** — n'offre aucune provenance, aucune
  racine de confiance hors ligne, ni aucun énoncé de vulnérabilité ; inacceptable dans un
  produit de sécurité.

## Addendum (2026-07-03) : formulation SLSA v1.2 + évaluation du track Source

La formulation SLSA est normalisée en **SLSA Build L3 (SLSA v1.2)**. Dans SLSA
v1.2, le track Build s'arrête à L3 ; cette ADR ne revendique donc que le niveau du
track Build.

L'évaluation du track Source reste distincte. Source L1-L3 exigerait de conserver
les révisions de source ainsi que les attestations de provenance du système de
gestion des sources ; Source L3 ajoute une application continue à altération détectable, par
exemple avec gittuf ou des attestations de plateforme.

État actuel : la protection des branches est automatisée dans
`scripts/apply-branch-protection.sh`, mais les attestations de provenance de la
source ne sont pas déployées.

Décision : aucun niveau du track Source n'est revendiqué ; il faut suivre le track
Source et réexaminer la décision au lancement public.
