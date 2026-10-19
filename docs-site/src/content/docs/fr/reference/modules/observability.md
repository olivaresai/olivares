---
title: "Observabilité — le modèle de lecture du moteur sur lui-même"
description: >-
  Un modèle de lecture pur sur ce qui existe déjà : quels standards
  d'interopérabilité le moteur épingle et expose, ce que le journal d'audit
  corrélé W3C dit d'une trace, et ce qui est prouvable concernant la chaîne
  d'approvisionnement du binaire en cours d'exécution. Il ne possède aucune
  entité et ne persiste rien.
---

Observabilité (`modules/observability`) est l'un des 32 modules — comme
[live-ingest](/fr/reference/modules/live-ingest/), il remplit un rôle
architectural plutôt qu'il ne comble une fonctionnalité. C'est le **modèle de
lecture du moteur sur lui-même** : trois surfaces en lecture seule sous
`/v1/m/observability/` qui répondent aux questions que rend la section System de
la console d'administration, sans posséder une seule entité de stockage.

## Les trois surfaces

| Route | Réponses |
|---|---|
| `GET /ingestion-health` | ce qui entre et sort du moteur **par standard d'interopérabilité** — les standards que le moteur épingle (OTel GenAI semconv, OCSF, ASIM, les formats SIEM unifiés, le push du journal, le texte Prometheus, W3C Trace Context), chacun avec sa version vérifiée |
| `GET /traces`, `GET /traces/{id}` | ce que le **journal d'audit corrélé W3C** dit d'une trace — la vue côté audit d'une trace distribuée, jointe par Trace Context |
| `GET /attestation` | ce qui est **prouvable concernant la chaîne d'approvisionnement du binaire en cours d'exécution** — la surface d'attestation que la [chaîne de vérification d'une version](/fr/how-to/verify-a-release/) alimente |

Les trois sont des lectures avec des permissions à portée de module ; rien ici
ne modifie quoi que ce soit.

## Pourquoi c'est un module à part entière

La console d'administration avait besoin d'une réponse faisant autorité à la
question « que parle réellement ce moteur, et à quelle version épinglée ? » — et
la manière honnête de la servir est depuis le moteur lui-même, non depuis une
documentation susceptible de dériver. La table ingestion-health est générée à
partir des mêmes épingles contre lesquelles les connecteurs et les exportateurs
sont compilés, de sorte que lorsqu'une épingle bouge, la surface bouge avec
elle.

## Contexte délimité, énoncé clairement

- **Il ne possède aucune entité de stockage et ne persiste rien** — un modèle de
  lecture pur sur des substrats qui existent déjà (les épingles, le journal, les
  preuves d'attestation).
- Ce n'est **pas** le [module XXII (santé/SLA)](/fr/reference/modules/xxii-health/),
  qui est délimité à la fiabilité des agents et serveurs MCP du *parc* (estate).
  Ce module concerne le *moteur*.
- Ce n'est **pas** le point de terminaison des métriques : les séries
  temporelles opérationnelles vivent sur
  [`/metrics`](/fr/how-to/monitor-with-prometheus/) ; ce module sert des réponses
  structurées, pas des séries.

## Reproduire le parcours du modèle de lecture

Sur une **installation Compose SQLite Enterprise neuve et jetable**, exécutez
`scripts/qualify-observability.py` avec Python 3.11 ou ultérieur. Fournissez le binaire
installé, l'adresse HTTPS, le nom du conteneur, le commit source complet et un nouveau
répertoire de preuves :

```sh
python3 scripts/qualify-observability.py /path/to/olivares \
  https://127.0.0.1:8443 olivares <source-sha> /path/to/new-evidence
```

La sonde utilise la gestion d'organisation d'Enterprise pour provisionner un second tenant
destiné à ses contrôles d'isolation. Les surfaces ingestion-health, les modèles de lecture de
trace et l'attestation restent disponibles dans Community.

La sonde crée des comptes et des agents de test par l'API, active les modules observability et
security existants, et redémarre physiquement le conteneur. Elle compare le SHA-256 de
l'exécutable démarré à celui du binaire fourni, vérifie l'ingestion à partir d'un finding
first-party, contrôle les refus entre tenants, et contrôle la conservation du détail de trace
du ledger et de l'export à travers le redémarrage et la désactivation puis la réactivation du
module. Elle vérifie aussi que les compteurs d'ingestion se réinitialisent et que l'attestation
sépare l'identité mesurée du binaire de l'état déclaré du pipeline et de la version non
publiée. Les contrôles CLI utilisent la sortie JSON. Les identifiants restent en mémoire ou
dans l'environnement des processus enfants ; les journaux conservés du moteur expurgent les
jetons de configuration.

Suivez les étapes d'installation du README avec une image candidate explicitement choisie.
N'exécutez jamais la sonde contre une installation existante. L'optimisation de Python est
refusée parce qu'elle désactive les assertions. Supprimez la pile Compose jetable et ses
volumes après l'exécution. La sonde laisse ce nettoyage à l'appelant et écrit son résultat et
le journal du moteur dans le répertoire de preuves. Elle n'exerce ni les actions de la
console, ni PostgreSQL, ni les collecteurs de fournisseurs, ni la vérification de signature
des versions.

## Voir aussi

- [Superviser avec Prometheus](/fr/how-to/monitor-with-prometheus/) — les
  métriques opérationnelles et les SLO.
- [Référence des événements](/fr/reference/events/) — le vocabulaire du bus sur
  lequel la table d'ingestion fait rapport.
- [Vérifier une version](/fr/how-to/verify-a-release/) — les preuves de chaîne
  d'approvisionnement que la surface d'attestation reflète.
