---
title: Comment cette documentation est organisée
description: >-
  Ces docs suivent Diátaxis — quatre modes (tutoriels, guides pratiques,
  référence, explication), chacun répondant à un besoin différent. Voici comment
  y naviguer.
---

Cette documentation est organisée avec le framework
**[Diátaxis](https://diataxis.fr/start-here/)**. Diátaxis observe que la
documentation technique sert quatre besoins distincts, et que les mélanger rend
les docs pires pour tout le monde. Le haut de la barre latérale est donc
**quatre modes**, et non une liste de fonctionnalités produit :

| Mode | Orientation | Répond à | Quand vous êtes… |
|---|---|---|---|
| **[Tutoriels](/fr/tutorials/zero-to-graph/)** | apprentissage | « Menez-moi de rien à un résultat fonctionnel. » | nouveau, et vous voulez apprendre en faisant |
| **[Guides pratiques](/fr/how-to/self-hosting/)** | une tâche | « Comment accomplir *cette chose précise* ? » | en train de travailler, et vous avez besoin d'une recette |
| **[Référence](/fr/reference/)** | information | « Quels sont exactement l'API, les événements, les modules, les flags ? » | en train de construire dessus, et vous avez besoin de précision |
| **[Explication](/fr/explanation/)** | compréhension | « *Pourquoi* est-ce construit ainsi ? » | en train d'évaluer, et vous voulez le raisonnement |

Une carte rapide de l'emplacement des choses :

- **Tutoriels** — les parcours d'apprentissage : [de zéro à un graphe d'accès
  en lecture/écriture](/fr/tutorials/zero-to-graph/), et la prise en main par
  scénario réel — [nœud unique](/fr/tutorials/getting-started/single-node/),
  [Docker Compose](/fr/tutorials/getting-started/docker-compose/),
  [Kubernetes](/fr/tutorials/getting-started/kubernetes/),
  [air-gapped](/fr/tutorials/getting-started/air-gapped/).
- **Guides pratiques** — installer et opérer
  ([self-hosting](/fr/how-to/self-hosting/),
  [sauvegarde et restauration](/fr/how-to/backup-and-restore/),
  [monitoring](/fr/how-to/monitor-with-prometheus/),
  [dépannage](/fr/how-to/troubleshooting/)), les
  [guides par connecteur](/fr/how-to/connectors/pgaudit/) (pgAudit, CloudTrail,
  eBPF, Claude Code, MCP, identité), et le
  [cookbook](/fr/how-to/cookbook/deny-closed-policies/) de recettes de gouvernance
  (politiques deny-closed, budgets, approbations, triage de dérive, le kill
  switch, push SIEM).
- **Référence** — l'[API REST](/reference/api/) (rendue à partir du contrat
  OpenAPI 3.1 du produit lui-même), la
  [politique de stabilité de l'API](/fr/reference/api-stability/), le
  [bus d'événements](/fr/reference/events/) (un contrat AsyncAPI 3.0), le
  [catalogue des modules](/fr/reference/modules/overview/), la
  [CLI](/fr/reference/cli/) et la [configuration](/fr/reference/configuration/).
- **Explication** — l'[architecture](/fr/explanation/architecture/overview/), le
  [modèle de sécurité](/fr/explanation/security/security-model/) et le
  [modèle de menace](/fr/explanation/security/threat-model/), le
  [licensing open-core](/fr/explanation/open-core-and-licensing/).

## Conventions

- **La recherche** est locale et côté client (Pagefind). Elle s'exécute
  entièrement dans votre navigateur ; rien n'est envoyé à un service de
  recherche externe — en cohérence avec la conception self-hosted du produit,
  dans laquelle vous décidez de ce qui franchit votre périmètre.
- **Versionnée.** La documentation est versionnée : lorsqu'une nouvelle version
  du produit est livrée, les docs de la précédente sont préservées. Le sélecteur
  de version se trouve dans la barre du haut.
- **Honnête sur les limites.** Là où une capacité est au stade de conception,
  post-v1, ou simplement pas encore construite, les docs le disent clairement.
  Voir [Honnêteté et limites](/fr/start/honesty-and-limits/). Les commandes des
  tutoriels et des guides pratiques sont faites pour être **exécutées telles
  qu'écrites**.
- **Langues.** La documentation canonique est en anglais ; des traductions sont
  disponibles en espagnol, chinois simplifié, russe, japonais, allemand et
  français (traduction automatique, l'anglais faisant foi, avec retour à
  l'anglais pour les pages pas encore traduites).
