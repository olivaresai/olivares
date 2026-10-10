---
title: Qu'est-ce qu'Olivares AI ?
description: >-
  Intégrer, gérer et sécuriser l'IA que vous exécutez, d'une seule machine à tout un parc
  informatique — une seule ground truth : Claude Code au niveau le plus profond, Codex et
  Grok Build à ses côtés. Un binaire unique auto-hébergé qui donne à votre IA du contexte,
  l'accès aux ressources et des sessions gérées, et qui vous donne les permissions, les
  politiques, les budgets et les preuves d'audit pour l'exploiter à travers votre
  infrastructure — sans télémétrie obligatoire ni sortie du plan de contrôle par défaut.
  Ne franchit votre périmètre que ce que vous configurez à cette fin, des appels à vos API
  de modèles aux sorties SIEM/webhook que vous raccordez.
---

Olivares AI **intègre, gère et sécurise l'IA que vous exécutez** — sur une seule machine
ou à l'échelle de tout un parc informatique, une seule ground truth : Claude Code au
niveau le plus profond, Codex et Grok Build à ses côtés, qu'elle complète plutôt que de
les concurrencer. À mesure que vous mettez davantage de modèles, d'agents, de serveurs
MCP et d'outils au travail à travers une infrastructure réelle et hétérogène, deux choses
deviennent difficiles à la fois : rendre l'IA véritablement utile et la garder sous
contrôle. Cela vaut autant pour une seule machine auto-hébergée que pour un parc
réglementé ; seule l'échelle change, pas la nature.

Olivares AI fait les deux. D'un côté il donne à votre IA ce dont elle a besoin pour
travailler — du contexte, l'accès aux bonnes ressources, des sessions gérées. De l'autre il
vous donne les **permissions granulaires, les politiques, les budgets et les preuves
d'audit** pour exploiter tout cela : quel modèle et quel agent peut atteindre quoi, les
données qu'ils touchent, ce qu'ils sont autorisés à exécuter, ce qu'ils dépensent, et la
preuve que vous pouvez remettre à un régulateur.

Tout tourne comme un **binaire unique auto-hébergé** sur vos propres hôtes. Il n'y a pas
de télémétrie obligatoire et, par défaut, aucune sortie du plan de contrôle. Ne franchit
votre périmètre que ce que **vous** configurez à cette fin : les appels à vos API de
modèles, les sorties SIEM/webhook que vous raccordez et, si vous en provisionnez un, un
fournisseur externe d'embeddings. C'est une propriété de l'architecture et de votre
configuration ; c'est une description, **pas une garantie**.

## Une capacité : la carte d'accès lecture/écriture

Parmi ces capacités figure la **carte d'accès L/L-É**. Pour chaque origine qu'une
source a observée (un agent, une identité non-humaine, une session), elle construit
une arête vers chaque ressource que ce signal montre qu'elle touche, classée
**read**, **write**, **read-write** ou **unknown**, et étiquetée avec :

- **d'où provient le signal** (`SignalSource`) — OpenTelemetry depuis un agent coopératif,
  une classification READ/WRITE pgAudit Postgres, un enregistrement AWS CloudTrail, un
  filet de sécurité eBPF/Tetragon au niveau noyau, une annotation MCP (traitée comme
  **non fiable** et corroborée, jamais crue seule), une autorisation de politique déclarée,
  ou un signal agent-à-agent (A2A) ; et
- **à quel point faire confiance à l'attribution** (`Confidence`) — `attributed` quand elle
  est fermement liée à une identité par-agent, `approximate` quand elle est déduite (un
  compte de service partagé, ou un store à perte).

En son centre se trouve le diff : **Permis vs Observé**. Les arêtes permises proviennent
d'autorisations déclarées ; les arêtes observées proviennent de la télémétrie réelle et de
l'audit. Les comparer fait ressortir les *accès inattendus* (un agent lisant une table qui
ne lui a jamais été accordée), les *autorisations inutilisées* (une permission qu'aucun
agent n'a jamais exercée), et les arêtes *en attente de réconciliation* (un accès que le
système ne peut pas encore attribuer avec fermeté).

Le produit est **honnête sur la fidélité**. La couverture est **par niveaux** : clean sur
les stores avec audit natif (SQL, stockage objet, entrepôts), lossy sur certains stores
(document/vectoriel), et impossible à reconstruire passivement sur d'autres (par ex. Redis,
SQLite, D1). Là où la nature lecture/écriture ne peut être déterminée, le mode est
`unknown` — le produit ne fabrique jamais une classification.

## Une plateforme, pas une seule fonctionnalité

La carte d'accès est une capacité parmi beaucoup. Le produit est une **plateforme
modulaire** (dans l'esprit de Grafana ou Backstage) : un moteur plus des modules plus des
connecteurs, conçu pour que tout module se rattache sans réarchitecturer le reste. Il
embarque **32 modules** — inventaire et sessions en direct, la carte L/L-É, l'orchestration
d'agents (A2A, en développement), la gestion MCP et des compétences, l'identité et l'identité non-humaine, le
déploiement, la connaissance et le contexte, la sécurité et les guardrails, la gestion des
modèles et des fournisseurs, le coût/FinOps, les évals et un bac à sable de test, le
red-teaming, la conformité et les preuves, un catalogue interne, les intégrations de sortie
et le push SIEM, la voix/temps réel, et la santé/SLA — plus des capacités de plateforme
non comptées parmi les 30 (sa propre API et la gestion-en-tant-que-code, le
multi-tenant, les tableaux de bord exécutifs) — à travers
**136 intégrations** (un décompte mesuré depuis le code par `scripts/check-public-counts.sh`).
Quelques capacités sont pré-v1 ou des coutures deny-closed jusqu'à provisionnement ; la
documentation est explicite sur lesquelles.

Community conserve l’observabilité locale, les paramètres enregistrés et l’export des sauvegardes. L’envoi SIEM/ITSM, la télémétrie externe et l’export de posture sont inclus dans l’édition de base Business.

Voir le [catalogue des modules](/fr/reference/modules/overview/) pour la liste complète, et
l'[aperçu de l'architecture](/fr/explanation/architecture/overview/) pour comprendre comment
le moteur et les modules s'assemblent.

## Comment il observe : lecture-d'abord, données-minimales

La carte d'accès observe les logs, OpenTelemetry et eBPF hors du chemin des
données. La défaillance d'un collecteur d'observation crée une lacune de visibilité
sans bloquer le trafic des agents. Le graphe d'accès stocke des relations — origine
→ ressource, lecture/écriture, source, confiance, horodatage — jamais de charges
utiles, corps SQL, secrets ou données personnelles.

Les points d'application sont inline et refusent l'accès en cas d'échec.
Les sessions Claude Code gérées installent des hooks d'appel d'outil qui contactent
le point d'application de politique (PEP) du moteur. Le moteur expose ce PEP par
défaut. S'il est inaccessible pendant une panne ou un redémarrage du moteur, tout
appel d'outil gouverné est refusé. Planifiez la disponibilité du moteur en conséquence.
Le proxy d'inférence inline, le gate MCP tools/call et le gate de délégation A2A
appliquent les règles au trafic qui les traverse ; les appels aux modèles ne passent
pas par le proxy d'inférence par défaut.

C'est aussi pourquoi il est auto-hébergeable et compatible air-gap : il n'y a pas de
télémétrie obligatoire et, par défaut, aucune sortie du plan de contrôle. Ne franchit votre
périmètre que ce que **vous** configurez à cette fin — les appels à vos API de modèles, les
sorties SIEM/webhook que vous raccordez et, si vous en provisionnez un, un fournisseur
externe d'embeddings. Olivares AI n'est pas sur cette liste : l'éditeur n'est jamais dans le
chemin des données. Il n'est contacté que lorsque vous lui demandez quelque chose —
`olivares upgrade`, ou un téléchargement par abonnement des modules commerciaux et de leurs
mises à jour — jamais comme effet de bord de l'exécution. Et `olivares upgrade --endpoint` dirige même cela vers votre propre miroir.
C'est un argument solide pour la résidence des données, le RGPD et les environnements air-gapped.

## Où aller ensuite

- **L'essayer :** le [tutoriel zéro-au-graphe](/fr/tutorials/zero-to-graph/) démarre le binaire
  unique et atteint un graphe Permis-vs-Observé peuplé.
- **Le comprendre :** l'[aperçu de l'architecture](/fr/explanation/architecture/overview/) et
  le [modèle de sécurité & de menaces](/fr/explanation/security/threat-model/).
- **L'exploiter :** [auto-hébergement](/fr/how-to/self-hosting/) et
  [installation air-gapped](/fr/how-to/air-gap-install/).

:::note[Statut]
Olivares AI est **pré-1.0**. Le binaire unique se compile, démarre et atteint un graphe
d'accès peuplé aujourd'hui (ceci est exercé de bout en bout par la suite de tests), mais
plusieurs capacités sont au stade de conception ou post-v1. La documentation est explicite
sur ce qui tourne maintenant versus ce qui est planifié — voir
[Honnêteté & limites](/fr/start/honesty-and-limits/).
:::
