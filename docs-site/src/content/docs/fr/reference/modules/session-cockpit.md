---
title: "Cockpit de sessions (disponibilité)"
description: >-
  Descripteur de disponibilité de l'espace de noms API session-cockpit.
  Community enregistre cet espace de noms sans gestionnaire et sans cockpit
  interactif. Comment confirmer cette absence via les sessions en direct et
  AgentOps, et quelles capacités ouvertes utiliser.
---

Le binaire Community enregistre un descripteur de disponibilité pour l'espace
de noms API `session-cockpit`. Cet espace de noms a actuellement **zéro
gestionnaire** et **aucun cockpit interactif**. Les requêtes sous
`/v1/m/session-cockpit` reçoivent **404 par absence**. Le descripteur n'est
pas l'un des 30 modules produit du catalogue.

## Disponibilité actuelle

| Surface | Community (cet artefact) |
|---|---|
| Espace de noms API | `session-cockpit` (`/v1/m/session-cockpit`) |
| Descripteur | `olivares.session-cockpit` `0.1.0` — titre `Session cockpit (availability)` |
| Routes / gestionnaires enregistrés | aucun |
| Cockpit interactif | aucun |
| Permission déclarée | `session-cockpit:availability:read` (déclarée, non routée) |
| Cycle de vie | vide (`Init` / `Start` / `Stop` ne font rien) |

Un 404 sur cet espace de noms est la réponse Community attendue. Cela ne
signifie pas que le plan de contrôle a échoué à l'installation.

## Diagnostiquer l'absence

Vérifiez que les surfaces de session livrées fonctionnent toujours :

1. Les routes du module de sessions en direct sous l'espace de noms
   `sessions` —
   [Exploitation en direct et sessions](/fr/reference/modules/ii-sessions/).
2. La console **Sessions** (`/sessions`), **Claude Code** (`/agentops`) et
   **Work** (`/work`) — [référence de la console](/fr/reference/console/).
3. Le cycle de vie des CLI officielles dans la section suivante.

Si celles-ci répondent et que `/v1/m/session-cockpit` est 404, le descripteur
correspond à cet artefact.

## Capacités ouvertes

L'installation, le lancement, l'observation et la gestion des CLI officielles
restent dans le produit Community ouvert :

- [Exploitation en direct et sessions](/fr/reference/modules/ii-sessions/) —
  sessions d'agents en direct, chronologies, profils fournisseur et `live_ref`.
- [Exploiter une session de fournisseur](/fr/how-to/operate-provider-sessions/) —
  lancer Claude, Codex ou Grok sous un binaire officiel épinglé.
- [Exécuter Claude Code avec Olivares](/fr/how-to/run-claude-code-with-olivares/) —
  co-déploiement AgentOps des sessions `claude` officielles.
- Guides connecteur et PEP-hook (observer/gouverner, pas lancer une session) :
  [Claude Code](/fr/how-to/integrations/claude-code/),
  [Codex](/fr/how-to/integrations/codex/),
  [Grok Build](/fr/how-to/integrations/grok/).
- [Enregistrement des sessions privilégiées](/fr/reference/modules/recording/)
- [Identité, permissions et gouvernance](/fr/reference/modules/vi-governance/)

## Voir aussi

- [Catalogue des modules](/fr/reference/modules/overview/)
- [Honnêteté et limites](/fr/start/honesty-and-limits/)
