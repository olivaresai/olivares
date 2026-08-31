---
title: Connecteurs vérifiés (tiers)
description: >-
  L'index curé des connecteurs tiers dont les mainteneurs ont re-vérifié les
  versions — frontière, signature, provenance et revue des données minimales —
  et comment soumettre le vôtre.
---

Cette page est l'**index curé des connecteurs tiers**. C'est le pendant externe
du [catalogue des connecteurs de première partie](/fr/reference/connectors/) :
les connecteurs de première partie sont livrés à l'intérieur du produit ; les
connecteurs listés ici sont construits, publiés et maintenus par **leurs
éditeurs** à l'aide du [SDK de connecteurs](/fr/how-to/build-a-connector/) public.

## Ce que « vérifié » signifie

Une version listée a été re-vérifiée par les mainteneurs, à la main, selon
cette liste de contrôle :

1. **Frontière de licence** — le connecteur se construit hors arborescence et
   ne lie rien du moteur AGPL (`go list -deps` ne montre aucun
   `github.com/olivaresai/olivares/core`) ; il importe uniquement le SDK
   Apache-2.0.
2. **Signature et provenance** — le bundle d'attestation Sigstore publié se
   vérifie face à l'identité ou à la clé publique déclarée de l'éditeur, et son
   condensé de sujet correspond au binaire publié.
3. **Conformité du contrat** — `Descriptor.Name` est en notation pointée et
   préfixé du namespace du fournisseur, les `ConfigFields` déclarés
   correspondent à ce que `Open` lit, les secrets sont déclarés `Secret: true`
   et pris par référence.
4. **Données minimales** — le connecteur émet des références et des métadonnées,
   jamais de charges utiles, de prompts ou de valeurs de secrets (revue
   ponctuelle des chemins d'émission).

**Ce que cela ne signifie pas :** la vérification n'est pas un audit de sécurité
de l'éditeur ni du système observé, ni une recommandation, et **pas une racine
de confiance** — un opérateur câblant un connecteur vérifié épingle toujours la
clé ou l'identité de l'éditeur dans `connector_trust` et le condensé de la
version dans le bloc `plugin` de la source. L'admission sur l'hôte reste
deny-closed (elle échoue en mode fermé) dans tous les cas.

Un connecteur privé n'a pas besoin de figurer ici pour être gouverné. Si un
opérateur épingle son condensé et son ancre de confiance dans `connector_trust`,
le moteur applique la même admission en mode fermé et la même gouvernance à
l'exécution. Cet index est une piste de certification pour la découverte et la
re-vérification, pas une racine de confiance.

## Index

Aucun connecteur tiers n'est listé pour l'instant — le programme s'ouvre avec
cette version. Les connecteurs de première partie figurent dans le
[catalogue des connecteurs](/fr/reference/connectors/).

| Connecteur (`Descriptor.Name`) | Éditeur | Type | Version vérifiée | Signature | Source |
|---|---|---|---|---|---|
| _aucun pour l'instant_ | | | | | |

## Soumettre un connecteur

Ouvrez une pull request sur cette page en ajoutant une ligne de tableau, qui
renvoie vers :

- le dépôt source et la version (binaire + `sha256` + bundle Sigstore) ;
- l'identité face à laquelle vérifier (identité OIDC + émetteur pour le mode
  keyless, ou la clé publique) ;
- la sortie de `./scripts/check-boundary.sh` et l'exécution des tests dans
  votre CI.

Les mainteneurs reproduisent la liste de contrôle ci-dessus sur les artefacts
exacts de la version. Une nouvelle version d'un connecteur listé nécessite une
mise à jour de la ligne (la re-vérification est par version, car le verdict est
lié au condensé). Les versions périmées ou retirées sont supprimées.

## Liens connexes

- [Construire et livrer un connecteur](/fr/how-to/build-a-connector/) — le cycle de
  vie complet
- [Module XIV — catalogue interne et marketplace](/fr/reference/modules/xiv-catalog/) —
  certification dans le produit (entrées de connecteurs + admission signée)
- [Stabilité de l'API](/fr/reference/api-stability/) — le contrat de stabilité du SDK
- [Vérifier une version](/fr/how-to/verify-a-release/) — la même discipline de
  chaîne d'approvisionnement pour les artefacts du produit lui-même
