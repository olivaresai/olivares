---
title: "AWS CloudTrail pour S3 (palier propre R/RW)"
description: >-
  Capturez les accès en lecture/écriture aux objets S3 depuis les data events
  de CloudTrail — le flag readOnly repris tel quel, le principal IAM comme
  origine, une attribution approximative assumée lorsqu'un rôle endossé masque
  l'appelant réel.
sidebar:
  order: 2
---

La source `s3cloudtrail` transforme les **data events S3** d'AWS CloudTrail en
arêtes de la carte d'accès : une arête par événement S3, avec le mode
lecture/écriture repris **tel quel depuis le champ `readOnly` de CloudTrail** —
jamais déduit — et le principal IAM auquel CloudTrail attribue l'appel comme
origine. C'est le palier propre du stockage objet, l'équivalent S3 de
[pgAudit](/fr/how-to/connectors/pgaudit/) pour Postgres.

Le connecteur **lit des fichiers journaux locaux et n'appelle jamais AWS** :
vous livrez les fichiers CloudTrail (la disposition de livraison S3 standard que
votre trail produit déjà), il les analyse. Seuls les événements
`eventSource == s3.amazonaws.com` sont traités — les événements du plan de
gestion relèvent du
[connecteur de découverte cloud `aws`](/fr/reference/connectors/), pas de celui-ci.

## Ce qu'elle émet

| Champ | Valeur |
|---|---|
| Source du signal | `cloudtrail` |
| Mode | `readOnly: true` → `read`, `false` → `write`, absent → `unknown` — tel quel, jamais deviné |
| Origine | le principal IAM (utilisateur, session de rôle endossé, service AWS) |
| Confiance | `attributed` ; `approximate` pour les rôles endossés partagés et les appels invoqués par un service |
| Palier de couverture | propre |

## 1. Prérequis côté AWS

- Un **trail CloudTrail avec les data events S3 activés** pour les buckets que
  vous gouvernez (les data events ne figurent pas dans le trail de gestion par
  défaut).
- La livraison des fichiers journaux du trail vers un emplacement lisible par
  l'hôte du moteur — le bucket de livraison S3 standard, synchronisé ou monté
  localement. Le connecteur accepte les fichiers classiques `{"Records":[…]}`
  (en clair ou `.json.gz`) et les enregistrements délimités par retour à la
  ligne.

## 2. Déclarer la source

```json
{
  "sources": [{
    "name": "prod-s3-trail",
    "kind": "s3cloudtrail",
    "tenant": "<tenant-id>",
    "config": {
      "path": "/var/lib/cloudtrail/prod/",
      "shared_accounts": "arn:aws:iam::123456789012:role/app-runtime"
    }
  }]
}
```

| Clé | Requis | Signification |
|---|---|---|
| `path` | oui | un fichier CloudTrail, ou un répertoire de fichiers `*.json` / `*.json.gz` |
| `shared_accounts` | non | ARN de rôles séparés par des virgules, partagés par de nombreux appelants — leurs arêtes sont honnêtement `approximate` |

(`s3-cloudtrail` est accepté comme alias du `kind`.)

## 3. Ce que vous verrez dans la console

Les buckets et objets S3 rejoignent la **Access map** avec des badges de palier
propre ; les lectures et écritures sont colorées d'après le flag `readOnly`. Le
panneau de dérive les confronte aux droits déclarés exactement comme pour toute
autre source.

Dans **Inventory**, les principaux auxquels CloudTrail attribue les appels
apparaissent comme des identités, prêtes à être liées à des agents — c'est cette
liaison qui transforme un `approximate` de rôle partagé en un `attributed` par
agent.

## Limites assumées — à lire avant de faire confiance à la carte

- **Un rôle endossé partagé par de nombreux appelants ne peut pas nommer
  l'appelant réel.** CloudTrail attribue l'appel à la session de rôle ; si le
  rôle est partagé, l'arête est délibérément `approximate`. Déclarer le rôle
  dans `shared_accounts` rend cela explicite. La correction durable est
  l'identité par agent ([la dépendance à l'identité](/fr/how-to/connect-a-source/#la-dépendance-dure--lidentité-par-agent)).
- **Les data events que vous n'avez pas activés n'existent pas.** CloudTrail
  n'enregistre que ce que le trail est configuré pour enregistrer ; l'absence
  d'une arête n'est pas l'absence d'accès si les data events sont désactivés
  pour un bucket.
- **La latence de livraison est celle de CloudTrail.** Les data events arrivent
  selon le calendrier de livraison de CloudTrail (typiquement des minutes) ;
  cette source n'est pas un robinet temps réel.

## Voir aussi

- [pgAudit](/fr/how-to/connectors/pgaudit/) — la même discipline de palier propre
  pour PostgreSQL.
- [Connecter une source](/fr/how-to/connect-a-source/) — le modèle de connecteur.
- [Connecteurs et paliers de couverture](/fr/reference/connectors/) — où chaque
  magasin se situe honnêtement.
