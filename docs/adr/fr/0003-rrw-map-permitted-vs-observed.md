> Traduction automatique. La version anglaise fait foi.

# ADR-0003: La carte R/RW avec un diff Permitted-vs-Observed est une capacité différenciée clé

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** registre des décisions produit (P2) ; architecture (module III)

## Contexte et énoncé du problème

De nombreux outils savent *observer* l'activité d'un agent, et beaucoup savent *énumérer*
les permissions accordées. Aucun, à lui seul, ne répond à la question qui compte pour la
gouvernance : **ce qu'un agent est *autorisé* à toucher est-il identique à ce qu'on
l'*observe* toucher ?** Le produit avait besoin d'une capacité défendable et difficile à
banaliser qui y réponde — l'une parmi plusieurs qu'il propose, pas le produit entier.

## Facteurs de décision

- Une capacité difficile à banaliser et directement utile à la sécurité/au SOC.
- Construite à partir de signaux que le produit peut réellement obtenir (audit, télémétrie,
  noyau).
- Honnête sur la fidélité plutôt que de sur-promettre.

## Options envisagées

- **Diff Permitted-vs-Observed** (least-privilege drift) sur une carte d'accès read/write.
- **Observed-only** — montrer ce que les agents ont fait.
- **Permitted-only** — montrer les permissions accordées.
- **Visualisation de sessions** — montrer les sessions d'agents en direct.

## Résultat de la décision

Option retenue : **la carte d'accès R/RW (module III) avec le diff Permitted-vs-Observed**.
Pour chaque arête origine→ressource, le produit classe en read/write, enregistre la source
du signal et la confiance, et compare les grants déclarés à l'usage observé pour faire
émerger la **least-privilege drift** (dérive de moindre privilège) : accès inattendus, grants
inutilisés et arêtes en attente de réconciliation.

### Conséquences

- **Bon :** un artefact distinctif et pertinent pour la sécurité, sur lequel la gouvernance
  de la plateforme s'appuie, aux côtés des autres modules — pas une fonctionnalité isolée.
- **Mauvais / compromis :** dépend d'une identité par agent pour une attribution ferme (un
  compte de service partagé retombe à une confiance *approximate*) ; la couverture est
  **par paliers** selon le store ; elle doit être honnête sur `unknown` et `approximate`
  plutôt que de fabriquer une certitude.
- **Neutre :** la carte d'accès est une *vue* sur le modèle de données général (voir
  ADR-0005), pas un schéma distinct.

## Pourquoi les alternatives ont été rejetées

- **Observed-only / Permitted-only** — chacune n'est que la moitié du tableau ; la valeur
  réside dans le *diff*.
- **Visualisation de sessions** — banalisée (les éditeurs livrent une « vue agent ») ; ce
  n'est pas un avantage durable.
