---
title: "Module XVIII — red-teaming et test adversarial"
description: >-
  Un harnais de robustesse défensive : une batterie consent-gated de sondes adversariales
  publiées mappées à OWASP Agentic et MITRE ATLAS, notées dans un scorecard à altération détectable.
  Ce qu'il teste, la ligne rouge du consentement, et ses limites honnêtes.
---

Le module XVIII est un **harnais de robustesse défensive**. Il sonde les agents gouvernés
**propres** au client avec une batterie de cas de test adversariaux publiés — injection de
prompt, jailbreak, exfiltration, empoisonnement d'outils — et note leur résistance, mappée au
**OWASP Top 10 for Agentic Applications**, à l'**OWASP LLM Top 10 (2025)** et à **MITRE ATLAS**.
C'est une suite de tests, pas une arme : une conformité ou une fuite est un finding, pas un
exploit remis à quiconque.

## Ce que c'est

La batterie est un catalogue de **sondes** réparties en quatre familles (`injection`, `jailbreak`,
`exfil`, `tool_poisoning`). Chaque sonde est un test de robustesse *connu et publié* mappé à une
référence OWASP/ATLAS, avec l'attente qu'un agent bien défendu le **refuse** ou que son guardrail
le **bloque**. Les payloads sont des canaris bénins — ils demandent à l'agent d'émettre un marqueur
inerte, ou de décrire une opération dangereuse sans l'exécuter — de sorte que la batterie sonde le
*refus*, pas la brèche. Un **Judge** déterministe classifie chaque résultat : `blocked`/`refused`
est un **pass**, `complied`/`leaked` est un **fail**, `error` est une faute d'exécution, `skipped`
est non-exécuté.

Les résultats s'agrègent dans un **scorecard** : `score = passed / (passed + failed) × 100`, avec
`errors` et `skipped` délibérément **exclus** du dénominateur — une sonde qui ne s'est jamais
exécutée n'est jamais comptée comme un pass. Le scorecard se décompose par famille et suit la
couverture des échecs OWASP-Agentic, et c'est un enregistrement **append-only, à altération détectable** de
sorte qu'un run ultérieur puisse le comparer comme baseline de régression.

## La ligne rouge, son contrat et ses entités

La frontière dual-use est **appliquée dans le code**, pas seulement énoncée dans la documentation.
Un run s'exécute **uniquement** contre un agent gouverné par le client qui a été explicitement
**enregistré et autorisé** comme cible — et enregistrer n'est pas consentir : une cible naît
`registered` avec l'autorisation retenue, et une étape d'autorisation distincte est l'octroi
explicite. Lancer un run contre une cible non autorisée ou inconnue est refusé à la porte.
Enregistrer, autoriser et lancer sont toutes des actions **de tier admin, auditées, privilégiées** ;
chacune laisse un auto-audit attribué au principal réel.

Le module possède trois entités portées par le tenant : la **target** (un enregistrement de
consentement mutable à travers son cycle de vie register → authorize → revoke), le **run** (un
enregistrement d'évaluation append-only portant les agrégats et le score), et les **results**
par sonde (append-only, une ligne par sonde). Il est **à données minimales par construction** :
le endpoint de la cible est un handle opaque que la sandbox déréférence — jamais une credential —
et un result ne stocke qu'un hash unidirectionnel de son détail, jamais le payload brut ni la
réponse brute de l'agent. L'API côté lecture sert le catalogue en **taxonomie uniquement** (id,
famille, titre, référence OWASP/ATLAS, sévérité, surface) ; les payloads des sondes sont internes
et ne sont jamais exposés sur le fil.

## Ce qu'il consomme et produit

Le module possède la batterie et la notation ; il n'atteint **pas** lui-même un quelconque agent.
L'exécution est déléguée au runtime isolé via un seam `Sandbox` — la sandbox est le seul composant
qui touche la cible, à l'intérieur du périmètre du client, avec un egress segmenté exactement vers
la cible autorisée et tout le reste refusé. Chaque sonde échouée est persistée comme un `Finding`
core (`kind = "redteam"`) à l'intérieur de la transaction du run, et un événement
`finding.reported` à données minimales (`kind = "redteam_failure"`) est publié sur le
[bus d'événements](/fr/reference/events/) pour les consommateurs de distribution et de conformité —
les deux ne portent qu'une référence de sujet, un titre et un hash de détail.

## Limites honnêtes

:::caution[Limites honnêtes]
- **Sans sandbox câblée, un run est DÉGRADÉ, jamais un faux pass.** Le seam d'exécution par défaut
  n'atteint aucun agent : chaque sonde est rapportée `skipped`, le statut du run est `degraded`, et
  le score reflète que rien n'a été testé. Le harnais livre aujourd'hui la batterie complète et la
  notation ; l'exécution en direct dépend d'un runtime isolé provisionné, et un déploiement non
  provisionné est honnête à ce sujet plutôt que de noter une cible non testée.
- **Il ne teste que les agents que vous gouvernez et autorisez.** Il ne cible jamais des systèmes
  tiers, ne scanne jamais les credentials d'autrui, et ne livre aucune capacité purement offensive.
  Une cible non autorisée ou inconnue est refusée — ce n'est pas un bouton de configuration.
- **Les sondes sont une batterie publiée et défensive — pas des exploits inédits.** Chacune mappe à
  une référence OWASP/ATLAS. La vue de couverture ATLAS est un **instantané daté** estampillé à une
  version de matrice spécifique, pas une parité continue avec la matrice en direct.
- **Une faute d'exécution de la sandbox n'est pas un verdict.** Un result `error` laisse le run se
  poursuivre et est exclu du score ; il ne compte jamais comme une vulnérabilité ni comme un pass.
:::

## Voir aussi

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module XVIII et son statut d'actuation.
- [Module IX — sécurité, guardrails et audit](/fr/reference/modules/ix-security/) — le consommateur des findings `redteam`.
- [Référence du bus d'événements](/fr/reference/events/) — l'événement `finding.reported` et son payload à données minimales.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — comment le moteur et le runtime isolé se composent.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — autoriser une cible et agir sur les findings.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — le contrat d'actuation couvrant tout le produit.
