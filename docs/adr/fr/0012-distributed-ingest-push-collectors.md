> Traduction automatique. La version anglaise fait foi.

# ADR-0012 : Ingestion distribuée — les collecteurs poussent vers le noyau via gRPC + mTLS

- **Status:** accepted
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares (boot decision CB-1)
- **References:** roadmap boot decisions (CB-1 → option C); runtime-ingestion contract

## Contexte et énoncé du problème

Le plan d'ingestion avait besoin d'une décision de topologie. Les collecteurs observent sur
les hôtes du client ; le noyau agrège. Les options allaient du tout-en-processus à un modèle
push entièrement distribué, avec des implications sur l'isolation, la frontière de confiance
réseau et le packaging.

## Critères de décision

- Garder le plan de données sur l'infrastructure du client avec un franchissement réseau
  durci.
- Préserver le binaire unique pour le cas simple.
- Isoler les dépendances des collecteurs du noyau.

## Options envisagées

- **C — push distant :** un collecteur exécute localement les connecteurs de source et
  **pousse** les observations vers le noyau via **gRPC + mTLS**, **sans listener entrant**
  sur le collecteur.
- **B — local hors-processus :** connecteurs en tant que sous-processus locaux (AutoMTLS),
  le substrat mono-nœud.
- **A — en-processus :** sources liées dans le binaire (fast-path first-party).

## Décision retenue

Option choisie : **C (push distant) comme cible distribuée**, avec B comme substrat
mono-nœud et A conservé comme fast-path en-processus pour les sources first-party. Tous les
transports entrent en v1 ; C n'est **pas** différé. Le mécanisme réside dans le runtime ;
le packaging distribué (DaemonSet/Helm) est livré avec les travaux sur la chaîne
d'approvisionnement.

### Conséquences

- **Bon :** les données franchissent la frontière réseau de façon durcie (mTLS + bearer +
  authz) ; le collecteur n'expose aucun port entrant ; le binaire unique est préservé.
- **Mauvais / compromis :** davantage de pièces mobiles pour le déploiement distribué.
- **Neutre :** la configuration par défaut en binaire unique utilise les chemins
  en-processus / sous-processus local.

## Pourquoi les alternatives ont été rejetées

- Ni **A** ni **B** seules ne couvrent la montée en charge multi-hôtes ; elles sont
  conservées respectivement comme fast-path et comme substrat mono-nœud, et non comme la
  réponse distribuée.
