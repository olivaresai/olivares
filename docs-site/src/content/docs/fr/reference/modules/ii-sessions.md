---
title: "Module II — opération en direct et sessions"
description: >-
  La surcouche opérationnelle en direct par session d'agent : action en cours,
  tokens/coût en direct, un état Claude Code dérivé et une chronologie rejouable,
  diffusés via server-sent events. Ce qu'il dérive, ce qui reste honnêtement vide,
  et les limites.
---

Le module II est la vue de l'**opération en direct** du parc (estate) : ce que chaque
session d'agent est en train de faire à l'instant présent, ses totaux de tokens et de coût
en direct, un état Claude Code dérivé, et une chronologie reconstructible. Là où le module I
(inventaire) matérialise le parc durable, le module II maintient une **surcouche
opérationnelle en direct** par session au-dessus du même flux d'observations — et ne montre
que ce que ce flux porte honnêtement.

## Ce qu'il est

Le module II est un module de couche Core piloté par le bus, frère de l'inventaire. Il
maintient un enregistrement en direct indexé par la référence externe de chaque session,
construit à partir du flux d'observations coopératif — jamais interrogé par scrutation,
jamais fabriqué. Par session, il suit :

- l'**action en cours** (le dernier outil utilisé) et la ressource/le mode qu'elle a touchés ;
- les **totaux de tokens et de coût en direct**, lus depuis les échantillons de coût (le
  journal de coût canonique et FinOps sont le module XI, pas ici — ceci n'est que le chiffre
  en direct) ;
- un **état Claude Code dérivé** (`cc_state`) ; et
- une **chronologie** à laquelle chaque événement observé est ajouté dans l'ordre d'ingestion.

## Son contrat et ses entités

Le module enregistre deux entités à portée de tenant. `sessions.live` contient
l'enregistrement en direct par session — action/ressource/mode en cours, référence de
modèle, tokens d'entrée/sortie en direct, coût en direct, compteurs d'événements et d'appels
d'outils, et horodatages de premier/dernier événement. `sessions.timeline` contient une ligne
rejouable par événement, ordonnée par ingestion. Il n'y a **aucune colonne de cycle de vie
stockée** : le flux coopératif ne porte aucun signal de fin-ou-échec, de sorte que le seul
signal de vivacité honnête est le `cc_state` dérivé.

`cc_state` est dérivé **au moment de la lecture** à partir de la récence des événements —
`active` / `idle` / `ended` — et bascule vers un état d'évasion silencieuse lorsque le
connecteur émet ce finding (il n'est jamais écrit par le module lui-même). Les lectures sont
servies sous des routes de module (liste en direct, session unique, chronologie par session)
plus un flux SSE en direct ; chaque lecture exige la permission de lecture de session, et
**l'ouverture du flux est auto-auditée**. Le canal SSE est strictement **isolé par tenant**
(un client ne reçoit que des instantanés pour son tenant autorisé) et **au mieux (best-effort)**
(un client lent abandonne la trame intermédiaire et reçoit la suivante — l'ingestion ne bloque
jamais).

## Ce qu'il consomme (et ce qu'il dérive)

Le module II consomme le même flux d'observations à donnée minimale que l'inventaire —
[`edge.observed`](/fr/reference/events/), `cost.sampled` et `finding.reported`. Seules les arêtes
dont l'origine est une **session** produisent de l'opération en direct ; les échantillons de
coût liés à une session s'ajoutent au chiffre de tokens/coût en direct (aucun `CostRecord`
n'est écrit ici) ; les findings dont le sujet est une session sont annotés, et un finding
anti-évasion marque l'état d'évasion. Deux champs sont **dérivés en direct** à partir de ces
mêmes signaux : `agent_ref` à partir de l'agent attribué à une session, et `summary` à partir
d'un finding de compaction de contexte (forensique) dont le titre est sûr pour le résumé par
contrat — jamais un résumé fabriqué par un LLM.

:::caution[Limites honnêtes]

- **`goal` reste vide — honnêtement.** Le flux coopératif est à donnée minimale et ne porte
  **pas** l'objectif ou la liste de tâches d'une session ; ils sont expurgés au connecteur et
  aucun texte de prompt en cours de processus n'est sur le fil. L'enregistrement en direct
  modélise le champ pour que le contrat et l'UI soient prêts et qu'un futur canal de métadonnées
  puisse le peupler, mais le module **ne l'invente jamais**.
- **Aucun cycle de vie stocké.** Le flux n'a aucun signal de fin/échec, donc la vivacité d'une
  session est le `cc_state` **dérivé** par récence — non un statut persisté. Un état `ended`
  signifie *aucun événement récent*, et non un arrêt propre confirmé.
- **Le chiffre en direct n'est pas le journal.** Les tokens/coût en direct sont une lecture
  opérationnelle issue des échantillons de coût ; l'enregistrement de coût faisant autorité et
  réconciliable est le journal FinOps du module XI. Ne traitez pas le chiffre en direct comme la
  vérité de facturation.
- **La donnée minimale est une propriété du fil.** Seuls des références, des classifications et
  des compteurs de vivacité/coût sont portés et persistés — jamais de charges utiles, de prompts,
  de commandes ou de PII.
:::

## En lien

- [Référence du bus d'événements](/fr/reference/events/) — les événements `edge.observed`,
  `cost.sampled` et `finding.reported` que ce module consomme.
- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module II et la
  répartition honnête de l'actionnement.
- [Carte d'accès et de ressources](/fr/reference/modules/iii-access-map/) — le module Core frère
  qui possède le graphe d'accès R/RW.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur et les
  couches.
- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — commencer à produire le flux en direct.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce que le produit fait et ne fait pas
  aujourd'hui.
