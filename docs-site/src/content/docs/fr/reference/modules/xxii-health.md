---
title: "Module XXII — santé, SLA et disponibilité"
description: >-
  La fiabilité des agents et serveurs MCP de l'estate AI : ce qui est sain, ce qui est
  dégradé ou hors service, et ce qui dépend de quoi. Comment la santé est dérivée de
  signaux que le produit peut prouver, ce qu'il matérialise, et les limites honnêtes.
---

Le module XXII répond à trois questions sur les composants AI de l'estate — **ce qui est
sain, ce qui est dégradé ou hors service, et ce qui dépend de quoi**. Il est borné à la
**fiabilité des agents et des serveurs MCP**, non à la santé des hôtes ou de
l'infrastructure en général. Cette page est la référence de ce que le module mesure, de ce
qu'il matérialise, et de l'emplacement de ses bords honnêtes.

## Ce que c'est

XXII est un **consommateur du cœur**, non un sondeur : ouvrir des sockets dans
l'infrastructure du client est une affaire de connector, et l'ensemble d'observations
scellé n'a aucun type santé. La santé est donc **dérivée** de signaux que le module peut
prouver :

- **Liveness (passif).** Une session ou un agent touchant un serveur MCP — ou un agent qui
  agit — est la preuve que le sujet est en vie. Cela rafraîchit le marqueur de dernière
  observation du sujet et intègre une arête de dépendance.
- **Résultats de sondes actives.** Un vérificateur de santé externe ou l'agent lui-même
  poste un résultat sur un endpoint de rapport par vérification — le chemin d'ingestion
  honnête pour les « health checks / métriques OTEL ».
- **Obsolescence (staleness).** Un sujet connu qui cesse d'être observé dans sa cadence
  attendue est en soi un signal. Un balayage en arrière-plan le fait passer à `degraded`,
  puis `down`, et ouvre un incident. Le balayage ne fait que **dégrader ou marquer hors
  service** ; la reprise vient exclusivement d'une liveness réelle, de sorte qu'une
  vérification fraîchement créée n'émet jamais de reprise erronée.

## Son contrat et ses entités

Le module détient quatre entités. Une **health check** est un sujet surveillé déclaré par
un opérateur (un agent ou un serveur MCP) avec une cadence attendue et une cible SLA ; elle
porte l'état d'instantané courant du sujet — `healthy`, `degraded`, `down` ou `unknown`.
Un **health event** est un ledger de transitions en ajout seul à partir duquel la
disponibilité et le SLA sont *reconstruits* — jamais stockés comme un compteur courant. Un
**health incident** est le cycle de vie ouvert→résolu d'une période dégradée ou hors
service, avec un seul incident ouvert imposé par sujet. Une **health dependency** est une
arête `origin → target` auto-découverte — la carte de dépendances, accumulée de manière
idempotente.

La santé est **matérialisée uniquement pour les vérifications déclarées**. Un sujet observé
en vie **sans vérification déclarée** est présenté honnêtement sur la carte de dépendances
comme `observed` — *vu en vie, santé non mesurée* — un état distinct de `healthy` (une
vérification déclarée a signalé) et de `unknown` (nommé, sans preuve de liveness). Le
produit ne fabrique jamais un état mesuré-sain qu'il n'a pas calculé. XXII reflète aussi
l'état courant d'un sujet dans l'entité `HealthStatus` du cœur lorsque le sujet est un id du
cœur, afin que d'autres plans puissent lire la santé d'un agent ou d'un MCP.

## Ce qu'il consomme et produit

XXII consomme [`edge.observed`](/fr/reference/events/) depuis le bus pour la liveness passive
et la carte de dépendances, ainsi que les rapports de sondes actives qui arrivent sur son
API. Il **produit, il ne livre pas** : les signaux down, degraded, recovered et
violation-de-SLA sont émis comme des `FindingReport` à donnée minimale sur le canal
[`finding.reported`](/fr/reference/events/) — le flux d'alertes commun à tout le produit que
le [module XV (notifications)](/fr/reference/modules/xv-notify/) achemine vers Slack, PagerDuty
ou un SIEM. XXII ne livre jamais, et ne s'abonne jamais à ses propres findings.

:::caution[Limites honnêtes]
- **Il ne mesure que ce qui est déclaré.** La santé est matérialisée uniquement pour les
  vérifications déclarées. Un sujet vivant mais non déclaré se lit `observed` (vu en vie,
  non mesuré) — jamais `healthy`. La fiabilité n'est aussi complète que les vérifications
  qu'un opérateur déclare.
- **Ce n'est pas un sondeur.** XXII n'ouvre jamais de sockets dans votre infrastructure. Il
  dérive la fiabilité de la liveness, des résultats de sondes postés et du silence — de
  sorte que pour un sujet qui n'émet aucune télémétrie et n'a aucun vérificateur externe,
  une absence de signal est traitée comme un signal (obsolescence), non comme une preuve de
  santé.
- **La disponibilité et le SLA sont reconstruits depuis un ledger en ajout seul**, et non
  conservés comme un compteur en direct ; les chiffres reflètent les transitions
  enregistrées pour la fenêtre demandée.
- **Aucune actuation.** Ce module gouverne et observe par nature — il n'a aucune surface
  d'actuation (voir la [vue d'ensemble des modules](/fr/reference/modules/overview/)). Il
  détecte et signale ; la remédiation est une affaire humaine ou en aval.
- **Donnée minimale sur le fil.** L'état stocké est le statut, les métriques de fiabilité
  et les relations de dépendance — jamais de payloads, prompts, secrets ou PII. Le seul
  détail sensible qu'une sonde peut porter (un message d'erreur) est réduit à un hash à sens
  unique ; seul un résumé court et non sensible est affiché.
:::

## Liens connexes

- [Référence du bus d'événements](/fr/reference/events/) — `edge.observed` (liveness) et `finding.reported` (les signaux qu'émet XXII).
- [Module XV — intégrations de sortie et notifications](/fr/reference/modules/xv-notify/) — achemine les findings de santé de XXII vers leurs destinations.
- [Vue d'ensemble des modules](/fr/reference/modules/overview/) — où se situe XXII et la séparation de l'actuation.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur, le bus et la couche cœur.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce que le produit observe aujourd'hui face à ce qu'il actue.
