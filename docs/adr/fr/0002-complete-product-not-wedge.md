> Traduction automatique. La version anglaise fait foi.

# ADR-0002: Livrer le produit complet (28 modules), pas un wedge

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** registre des décisions produit (P1) ; catalogue des modules (les 28 modules)

## Contexte et énoncé du problème

Une mise sur le marché courante pour un produit d'infrastructure consiste à viser un
« wedge » étroit : livrer une seule capacité pointue, gagner une tête de pont, puis
s'étendre. Pour Olivares AI, le wedge candidat était la carte d'accès read/write
(lecture/écriture) à elle seule. La question était de savoir s'il fallait publier le wedge
ou le control plane complet.

## Facteurs de décision

- Première impression : les acheteurs d'entreprise (CTO/SOC/sécurité) évaluent un control
  plane comme une plateforme, pas comme une fonctionnalité.
- La carte R/RW a plus de valeur *au sein* d'une plateforme complète qu'en tant qu'outil
  autonome.
- Éviter une ré-architecture : une plateforme modulaire admet de nouveaux modules sans
  refonte.

## Options envisagées

- **Produit complet** — publier les 28 modules comme une plateforme cohérente unique (la
  gestion de modèle propre / le fine-tuning est une capacité planifiée, pas l'un des 28).
- **Wedge étroit** — publier la carte R/RW seule, puis s'étendre.

## Résultat de la décision

Option retenue : **produit complet**. La publication initiale est la plateforme entière,
construite autour de Claude et de Claude Code — inventaire, sessions en direct, la carte
R/RW, la gouvernance, le cadrage des sources/credentials, le déploiement, la connaissance,
la sécurité, l'enregistrement des sessions privilégiées, la gestion des modèles/fournisseurs,
le proxy d'inférence inline, le FinOps, les évaluations, la conformité, le forwarder SIEM, le
catalogue, les intégrations de sortie, l'eventing, la voix, le sandbox, le red-teaming et la
santé — avec l'API propre du moteur, la multi-tenance et les tableaux de bord comme capacités
core/console. La carte R/RW est **une capacité différenciée clé au sein** de ce produit, et
non le produit lui-même.

### Conséquences

- **Bon :** une plateforme complète et crédible dès le premier jour ; la carte d'accès
  prend tout son sens dans son contexte.
- **Mauvais / compromis :** une surface v1 bien plus large à construire et à maintenir
  honnête ; la profondeur varie selon le module et doit être documentée honnêtement (voir
  *Honnêteté et limites*).
- **Neutre :** la gestion de modèle propre / le fine-tuning est une capacité planifiée, pas
  l'un des 28 modules livrés.

## Pourquoi les alternatives ont été rejetées

- **Wedge étroit** — rejeté : il sous-vend un produit de plateforme et fait courir le risque
  que la carte R/RW soit perçue comme un « visualiseur de sessions » banalisé plutôt que
  comme le moteur de least-privilege drift (dérive de moindre privilège) qu'elle est.
