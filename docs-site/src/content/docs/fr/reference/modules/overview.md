---
title: Catalogue des modules
description: >-
  Les 30 modules d'Olivares AI — organisés par les neuf domaines de
  fonctionnalités, avec la maturité honnête de chaque module. Olivares AI
  intègre, gère et sécurise l'IA en entreprise, une seule ground truth : Claude Code au niveau le plus profond, Codex et Grok Build à ses côtés
  Claude Code ; ceci est la référence par module.
---

Olivares AI intègre, gère et sécurise l'IA en entreprise, une seule ground truth :
Claude Code au niveau le plus profond, Codex et Grok Build à ses côtés. C'est une **plateforme modulaire** — un moteur, une
console, et **30 modules** câblés dans un binaire unique — qui observe où
s'exécutent les agents, gouverne ce qu'ils sont autorisés à faire, et (sur un
sous-ensemble croissant) agit sur votre infrastructure réelle. Chaque module
(a) consomme des événements/données normalisés du cœur (core), (b) déclare ses
entités dans le modèle de données partagé, et (c) expose ses propres points de
terminaison d'API et vues d'UI — sans toucher au cœur ni aux autres modules.

Les 30 modules sont organisés par les **neuf domaines de fonctionnalités**
ci-dessous. Lisez le statut de chaque module en **deux moitiés** :
*Gouverner/Observer* (cataloguer, observer, contrôler, rapporter) est construit
et câblé aujourd'hui ; *Actionner* (agir sur l'infrastructure réelle — déployer,
dispatcher, envoyer, appliquer, exécuter) relève d'états honnêtes — **live**
dans le binaire par défaut pour un sous-ensemble, **à la demande** pour
plusieurs (le backend est construit et câblé à un point d'injection mais reste
fermé par défaut (deny-closed) ou dégradé jusqu'à ce qu'un opérateur le
provisionne via une configuration d'environnement), **PARTIAL** là où la surface
est contrôlée/opt-in, et une **jonction (seam) fermée par défaut** déclarée pour
le reste. En particulier, **deploy** planifie et gouverne les déploiements mais
ne les **applique pas** à l'infrastructure en production tant qu'un exécuteur
n'est pas provisionné : `apply`/`retire` renvoient un `503` clair. La profondeur
varie selon le module, et une grande partie du produit est en pré-1.0 / au stade
de conception là où c'est indiqué (voir
[Honnêteté et limites](/fr/start/honesty-and-limits/)).

La **carte d'accès** (`iii-access-map`) — le graphe lecture/lecture-écriture de
ce que chaque agent peut toucher et touche effectivement, avec la dérive de
moindre privilège = `Permitted ≠ Observed` — est **l'une des fonctionnalités les
plus utiles parmi les 30**, pas le produit tout entier. L'étendue est l'enjeu :
neuf domaines, un moteur, une console.

## Les 30 modules, par domaine de fonctionnalités

Chaque ligne renvoie à sa page de module (`/reference/modules/<slug>/`). La
colonne **Actionner** correspond à l'état honnête de la moitié « agir » ; `—`
signifie que le module gouverne/observe par nature et n'a pas de surface
d'actionnement.

### Observer

| Module | Actionner | Objet |
|---|---|---|
| [Inventaire et découverte](/fr/reference/modules/i-inventory/) | — | Découvrir et cataloguer chaque agent/session/serveur MCP/outil/modèle/identité du parc. |
| [Opération en direct et sessions](/fr/reference/modules/ii-sessions/) | — | État en temps réel de chaque agent et session ; héberge aussi le runtime gouverné de session Claude Code. |
| [Carte d'accès et de ressources (R/RW)](/fr/reference/modules/iii-access-map/) | — | Ce à quoi chaque agent accède, et s'il lit ou écrit ; dérive de moindre privilège = `Permitted ≠ Observed`. |
| [Orchestration et A2A](/fr/reference/modules/iv-orchestration/) | à la demande | Observer-et-gouverner le graphe de délégation/communication en direct ; le dispatch est câblé à la demande, fermé par défaut jusqu'au provisionnement. |
| [MCP, compétences et capacités](/fr/reference/modules/v-capabilities/) | — | Gouverner les outils et capacités des agents, visuellement. |
| [Santé, SLA et disponibilité](/fr/reference/modules/xxii-health/) | — | Fiabilité des agents et serveurs MCP du parc ; vérifications, incidents, carte de dépendances. |
| [Modèle de lecture d'observabilité](/fr/reference/modules/observability/) | — | Le modèle de lecture du moteur sur lui-même : standards d'interopérabilité épinglés, vue journal/trace corrélée W3C, attestation de la chaîne d'approvisionnement. |
| [Adoption de Claude Code](/fr/reference/modules/claudeadoption/) | — | Modèle de lecture de l'adoption/productivité de Claude Code : sessions, lignes de code, commits, PR, accept-reject d'outils, tokens par modèle, par équipe/développeur/jour ; par équipe par défaut, drill-down par développeur opt-in. Frontière API-Claude-uniquement ; ne porte jamais de coût. |
| [Live-ingest](/fr/reference/modules/live-ingest/) | PARTIAL | Producteur in-process d'événements détectifs qu'un connecteur ne peut émettre ; contrôlé par environnement, fermé par défaut, données minimales. |

### Gouverner et appliquer

| Module | Actionner | Objet |
|---|---|---|
| [Identité, permissions et gouvernance](/fr/reference/modules/vi-governance/) | — | Qui et quoi peut faire quoi, de façon granulaire : Cedar RBAC + deny-overlay + grants à portée définie, réconciliation de roster, admin/rôles personnalisés à portée définie, break-glass, kill-switch. |
| [Cadrage des sources et identifiants](/fr/reference/modules/sourcescope/) | — | Lier des sources à un workspace/groupe d'agents ; résolveur à portée définie fermé par défaut + identifiants à portée définie au moment de la résolution. |
| [Déploiement et intégration](/fr/reference/modules/vii-deploy/) | à la demande (503) | Planifier et gouverner les déploiements vers l'infrastructure réelle ; l'exécuteur est à la demande — les `apply`/`retire` en production renvoient `503` jusqu'au provisionnement. |

> **Identité et accès** vit à l'intérieur de la [gouvernance](/fr/reference/modules/vi-governance/) —
> il n'y a pas de module séparé. Le cycle de vie NHI, la fédération d'identité
> d'agents, le step-up AAL3, et SSO/SCIM sont des capacités de gouvernance.

### Écosystème Claude et agents

| Module | Actionner | Objet |
|---|---|---|
| [Gestion des modèles et fournisseurs](/fr/reference/modules/x-models/) | à la demande (503) | Gouverner l'ensemble de la pile modèles/fournisseurs : accès aux modèles, fenêtre de contexte par surface, contrôle par groupe de modèles ; l'*exécution* de modèle est à la demande — `503` jusqu'à ce qu'un identifiant d'inférence soit provisionné. |
| [Proxy d'inférence en ligne](/fr/reference/modules/inferenceproxy/) | PARTIAL | Configuration de sortie d'inférence par tenant + DLP pour le proxy PEP en ligne `/v1/messages` ; la configuration du module est live, l'écouteur est opt-in, par défaut en loopback, fermé en cas d'échec (fail-CLOSED). |
| [Catalogue interne et marketplace](/fr/reference/modules/xiv-catalog/) | — | Marketplace curé d'agents, serveurs MCP et compétences approuvés/signés. |
| [Agents vocaux et temps réel](/fr/reference/modules/xvi-voice/) | à la demande | Observer-et-gouverner les agents conversationnels/temps réel (DENY par défaut, HITL en deux phases) ; n'ouvre jamais de flux média ; dispatch à la demande. |

### Sécurité et protection des données

| Module | Actionner | Objet |
|---|---|---|
| [Sécurité, garde-fous et audit](/fr/reference/modules/ix-security/) | live | Garde-fous (PII/injection/jailbreak), anomalies, chronologies d'incidents ; BYOK/DLP/RTBF/rétention/WORM/résidence vivent dans ce plan. |
| [Enregistrement de sessions privilégiées](/fr/reference/modules/recording/) | live | Enregistrement aligné PAM des sessions privilégiées : trames chaînées par hachage, expurgation à l'écriture, ancré au journal. |
| [Données, connaissances et contexte](/fr/reference/modules/viii-knowledge/) | à la demande | Plan de données gouverné : KB + RAG, récupération gouvernée, lignage, registre de prompts, mémoire d'agent ; les embeddings sémantiques basés sur modèle sont à la demande. |

### Conformité et preuves

| Module | Actionner | Objet |
|---|---|---|
| [Conformité et réglementaire](/fr/reference/modules/xiii-compliance/) | — | 26 catalogues de référentiels + preuves scellées dérivées du journal avec vérification de chaîne en direct. |
| [Forwarder SIEM/ITSM](/fr/reference/modules/siemforward/) | live | Expédie le journal scellé + les findings vers les tours SIEM (OCSF 1.8/CEF/LEEF/syslog/OTLP), parcours de curseur contrôlé par leader, au moins une fois. |
| [Export de posture](/fr/reference/modules/posture-export/) | PARTIAL | Récupération en lecture seule de la posture/inventaire pour les tours de contrôle (JSON neutre) ; ne **revendique pas** de push natif vérifié en aval. |
| [Reporting](/fr/reference/modules/reporting/) | — | Génération professionnelle de rapports PDF/HTML à partir des données de conformité, d'audit et FinOps de la plateforme — cinq types intégrés ; un auditeur télécharge un document au lieu de copier-coller du JSON. |

### FinOps

| Module | Actionner | Objet |
|---|---|---|
| [Coûts et FinOps IA](/fr/reference/modules/xi-finops/) | live | Budgets actifs qui refusent/limitent au plafond, coût par résultat, risque d'annulation ; budget arrimé à l'identité. |

### Évaluations et sûreté

| Module | Actionner | Objet |
|---|---|---|
| [Qualité, évaluations et tests](/fr/reference/modules/xii-evals/) | — | Juge LLM calibré + un contrôle de régression CI bloquant ; juge hors ligne → SKIPPED, jamais un succès silencieux. |
| [Bac à sable d'agents](/fr/reference/modules/xvii-sandbox/) | à la demande | Environnement sûr pour tester les agents avant la production ; l'isolation OS réelle (gVisor/Firecracker) est à la demande. |
| [Red-teaming et tests adversariaux](/fr/reference/modules/xviii-redteam/) | à la demande | Batterie adversariale soumise au consentement ; DÉGRADÉ — jamais un faux succès — jusqu'à ce qu'un runtime de bac à sable soit provisionné. |

### Plateforme et intégrations

| Module | Actionner | Objet |
|---|---|---|
| [Intégrations de sortie et notifications](/fr/reference/modules/xv-notify/) | live | Routeur de notifications vers les systèmes que l'entreprise exploite déjà ; le dispatch est câblé en live, les destinations sont provisionnées par l'opérateur. |
| [Eventing](/fr/reference/modules/eventing/) | live | Surface d'abonnement externe sur le bus : abonnements typés, livraison durable au moins une fois, retry/backoff, DLQ, rejeu de curseur. |
| [Vues de console enregistrées](/fr/reference/modules/consoleviews/) | — | Instantanés nommés et partageables de l'état des vues de console (filtres, plages), stockés côté serveur par tenant : enregistrez une investigation, partagez-la avec l'équipe. Accepte un objet JSON plafonné à 4096 octets destiné aux paramètres de vue — n'y stockez ni données sensibles ni résultats de requêtes. Création/mise à jour réservées au propriétaire ; les admins/propriétaires du tenant et les superadmins peuvent supprimer pour nettoyage ; chaque mutation est auditée. |

Colonne **Actionner** : `live` = l'actionnement est câblé et live dans le binaire
par défaut, aucun provisionnement requis (p. ex. l'application des budgets FinOps
refuse au plafond, le routeur de notifications dispatche) ; `à la demande` /
`à la demande (503)` = le backend est construit et câblé à un point d'injection
mais reste **fermé par défaut ou dégradé jusqu'à ce qu'un opérateur le
provisionne** via une configuration d'environnement (deploy répond `503`
jusqu'à ce qu'un exécuteur existe ; le dispatch orchestration/voice est fermé par
défaut jusqu'à configuration ; le red-team s'exécute en mode DÉGRADÉ jusqu'à ce
qu'un runtime de bac à sable soit provisionné ; l'exécution de modèle et les
embeddings sémantiques renvoient `503` jusqu'à ce qu'un identifiant soit
provisionné) ; `PARTIAL` = la surface est réelle mais contrôlée/opt-in ou ne
revendique pas d'aval vérifié (l'écouteur du proxy d'inférence est opt-in et par
défaut en loopback ; live-ingest est contrôlé par environnement ; posture-export
est une projection neutre en lecture seule) ; `—` = le module gouverne/observe
par nature et n'a pas de surface d'actionnement. Cette répartition est le contrat
honnête : le produit **observe et gouverne largement aujourd'hui, et actionne sur
un sous-ensemble croissant, principalement soumis au provisionnement** — voir
[Honnêteté et limites](/fr/start/honesty-and-limits/). Le catalogue est dérivé de
la racine de composition (`cmd/olivares/wire.go`) : les 30 modules y sont
construits et enregistrés via `rt.AddModule` (vérifié le 2026-08-01,
main @ f632f03f).

## Capacités plateforme et cœur (non comptées parmi les 30 modules)

Ce sont des capacités réelles et livrées, mais ce sont des **capacités
moteur/cœur/web**, pas des modules de l'ensemble `modules/` — elles ne sont donc
pas comptées dans les 30 :

- [Propre API + manage-as-code](/fr/reference/modules/xix-api-manage-as-code/) —
  **Capacité moteur/cœur.** L'API REST/gRPC versionnée du moteur lui-même plus le
  provider Terraform ; gérer la plateforme elle-même par API et IaC.
- [Multi-tenancy et gestion d'organisation](/fr/reference/modules/xx-multi-tenancy/) —
  **Capacité moteur/cœur.** Hiérarchie d'organisation et administration déléguée,
  avec isolation des tenants par sécurité au niveau ligne (row-level-security)
  Postgres.
- [Tableaux de bord exécutifs](/fr/reference/modules/xxi-executive-dashboards/) —
  **Capacité web.** Vues de console pour les dirigeants aux côtés de l'UI
  technique. (Le backend de génération de rapports est le module
  [reporting](/fr/reference/modules/reporting/), qui EST compté parmi les 30.)
- [Opérations des modèles (modèles propres)](/fr/reference/modules/xxiii-model-operations/) —
  **Capacité du module models** (comptée via la ligne du module X, pas une ligne
  à part) : le registre gouverné des modèles propres, l'admission des modèles
  signés, les enregistrements de lignage des datasets/jobs de fine-tuning, la
  gouvernance des déploiements d'inférence locale et les preuves AIBOM/model card.

**Prévu :** l'**exécution** du fine-tuning de modèles propres et de l'inférence
locale ([xxiii-fine-tuning](/fr/reference/modules/xxiii-fine-tuning/)) — la
plateforme gouverne et enregistre ce travail aujourd'hui (voir les opérations des
modèles ci-dessus) mais n'exécute pas l'entraînement et ne sert pas l'inférence
elle-même ; la moitié exécutante est un travail **prévu** documenté, **non livré**
et pas l'un des 30.

## Comment les modules apparaissent dans l'API et le bus

- **REST.** La [référence d'API](/reference/api/) rend la surface REST du cœur
  à partir du contrat OpenAPI 3.1 du produit. Certaines routes de module sont
  accessibles mais **délibérément absentes** de ce document ; leurs contrats au
  niveau des champs vivent dans les interfaces typées du produit.
- **Événements.** Les modules réagissent au [bus d'événements](/fr/reference/events/) :
  la carte d'accès consomme `edge.observed`, FinOps consomme `cost.sampled`, et la
  sécurité consomme `finding.reported` et `guardrail.observed`.

## Couches

Les 30 modules s'appuient sur des couches au-dessus du moteur, aux côtés des
capacités moteur/cœur et web ci-dessus :

- **Moteur (couche 0)** — les capacités propre-API/manage-as-code et
  multi-tenancy (cœur, non comptées dans les 30).
- **Cœur (couche 1)** — inventory, sessions, access-map, models, health,
  observability.
- **Gestion (couche 2)** — capabilities, governance, sourcescope, deploy,
  knowledge.
- **Intelligence (couche 3)** — orchestration, security, recording, inference
  proxy, finops, evals, compliance, reporting, siemforward, posture-export, catalog, notify,
  eventing, voice, sandbox, redteam, live-ingest, consoleviews.
- **Web (couche 4)** — l'UI et la capacité de tableaux de bord exécutifs.

Voir l'[aperçu de l'architecture](/fr/explanation/architecture/overview/) pour
savoir comment le moteur et ces couches se composent.
