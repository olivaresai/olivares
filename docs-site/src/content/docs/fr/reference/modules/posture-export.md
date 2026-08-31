---
title: "Export de posture vers les tours de contrôle"
description: >-
  Une projection sortante en lecture seule de la posture de référence (ground
  truth) du moteur — inventaire découvert, dérive de moindre privilège et
  findings de sécurité — qu'une tour de contrôle récupère pour enrichir sa
  propre vue. Une projection JSON neutre, non un push natif vérifié.
---

L'export de posture (`modules/posture-export`) est la **surface de posture
sortante** du moteur : un unique point de terminaison en lecture seule qu'une
tour de contrôle interroge pour enrichir son propre inventaire avec le
[graphe d'accès](/fr/reference/modules/iii-access-map/) de référence du moteur,
la dérive de moindre privilège, l'inventaire découvert et la posture de sécurité.
C'est le versant « intégrer, pas concurrencer » de la plateforme — il n'émet
jamais d'identité (cela est entrant, possédé par la
[gouvernance](/fr/reference/modules/vi-governance/)), seulement de la posture, et
il ne change rien.

## Ce qu'il expose

Une route, `GET /v1/m/posture/export`, contrôlée par `posture:export:read` et
épinglée à la portée d'un seul tenant. La réponse est un document JSON neutre
assemblé à l'intérieur d'**une transaction auditée** avec trois projections :

- **`inventory`** — les entités découvertes actives (type, ref, statut, sources
  de signaux, hôtes, première/dernière observation, nombre d'occurrences),
  optionnellement filtrées par `?kind=`.
- **`posture_drift`** — la dérive de moindre privilège réconciliée : accès
  observés-mais-non-permis, plus les décomptes de grants inutilisés et de grants
  d'inventaire.
- **`findings`** — les findings de sécurité projetés en tant que refs et
  `detail_hash` uniquement, filtrables par plancher `?severity=` et `?category=`.

Chaque export est en **données minimales** — refs, hachages et relations
uniquement, jamais une charge utile brute ni un secret — et une passe défensive
d'expurgation nettoie chaque champ en texte libre. L'export lui-même déplace des
données hors du système, il **s'auto-audite** donc dans le journal avec le
principal réel, au sein de la même transaction que les lectures.

## Maturité et contexte délimité

**PARTIAL.** L'action d'export est live et auditée ; ce qui n'est *pas* vérifié,
c'est l'autre extrémité. Les formats d'ingestion des tours nommées —
**Microsoft Agent 365** et **ServiceNow AI Control Tower** — n'ont pas d'API de
source primaire contre laquelle le moteur pourrait valider, c'est donc une
**projection JSON neutre honnête qu'une tour récupère (ou qu'un opérateur route
via un sink configuré), explicitement PAS un push natif fonctionnel**. Chaque
réponse porte cette note de provenance en ligne.

Des plafonds par requête bornent l'inventaire, la dérive et les findings ; un
export partiel rapporte ses propres indicateurs de troncature et n'est jamais
étiqueté comme faisant autorité.

## Voir aussi

- [Transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/) — le plan
  `siemforward`, le pendant *push* qui expédie le journal scellé et les findings
  vers une tour SIEM.
- [Module XIII — conformité et réglementaire](/fr/reference/modules/xiii-compliance/) —
  les preuves scellées avec lesquelles cette posture partage sa référence.
- [Module III — carte d'accès et de ressources](/fr/reference/modules/iii-access-map/) —
  la dérive réconciliée que l'export projette.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — pourquoi c'est une
  projection, non un push vérifié.
- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe l'export
  de posture parmi les 30 modules livrés.
