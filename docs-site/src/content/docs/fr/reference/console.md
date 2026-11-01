---
title: Référence de la console — chaque écran et la permission requise
description: >-
  Toutes les routes publiées par la console Olivares AI, regroupées par domaines
  de la console, avec la permission RBAC requise par chacune et la page de
  référence ouverte par son lien d'aide dans le produit. Généré depuis le
  recensement des routes de la console.
---

:::note[Édition]
Les sources d’annuaire (LDAP, Okta/Entra et IdP d’agents/charges), le cycle de vie des identités gérées et le serveur SCIM entrant nécessitent les extensions Business/Enterprise. Community conserve l’ingestion générique, le catalogue de ressources, les constats de sécurité, l’Access Map, l’inventaire des identités, SSO avec un IdP, les rôles et associations locaux, les politiques et approbations. Les panneaux et commandes CLI payants sont masqués ; leurs routes API publiées renvoient HTTP `501`. Activer un module n’ajoute pas son implémentation Business.
:::

Cette page est la carte de la console. Elle répertorie **toutes les routes montées par
l'application** — pas une sélection, ni celles que quelqu'un a pensé à documenter — avec la
permission nécessaire à un principal pour y accéder et l'endroit où en savoir plus.

La console a **une seule navigation : la barre latérale gauche**. Elle liste les lieux en
quatre groupes — **Travail** (Accueil, Sessions, Fichiers et espaces, Travail et
communications), **Outils** (Outils d’IA, Modèles, MCP et compétences, Sources),
**Contrôle** (Règles et approbations, Usage et coût, Activité et audit, Carte des accès)
et **Administration** (Personnes et groupes, Sécurité, Exploitation, Licence) — avec
Paramètres en bas. Chaque écran appartient à un lieu, et la rangée de liens au-dessus d’un
écran liste les autres écrans de ce lieu. Un lieu n’apparaît que si vos permissions
autorisent au moins un de ses écrans, et un écran Business uniquement dans l’édition
Business. La barre latérale se replie sur ses icônes et se rouvre ; sur un écran étroit,
la même barre s’ouvre en tiroir depuis le bouton de menu. La barre supérieure contient le
titre de la page, la recherche (⌘K ou Ctrl+K), les notifications et le menu du compte. La
palette de commandes cherche les mêmes écrans avec les mêmes permissions. Les anciennes
adresses d’annuaire de domaine (`/areas/…`) ouvrent le lieu qui contient désormais leurs
écrans, en conservant la requête et le fragment.

Une nouvelle installation n’affiche que le premier travail : Accueil, Sessions, Outils d’IA,
Approbations, l’assistant de configuration et Paramètres. Tout autre
écran y est un aperçu : son adresse, son API et sa CLI continuent de fonctionner, et la
navigation, la palette de commandes et les raccourcis ne l’affichent pas. Une installation
qui existait avant la mise à niveau affiche tous les écrans qu’elle affichait, de même
qu’une installation dont l’administrateur a choisi les modules (dès le démarrage suivant)
ou qui utilise les données de démonstration.

Elle est **générée**. La liste provient de `web/src/features/route-census.json`, le
recensement append-only que `registry.route-conservation.test.ts` compare au routeur compilé ;
un écran ne peut donc être ajouté, déplacé ou perdu sans que cette page change avec lui. Le
nom et la description en une ligne de chaque écran sont **les propres chaînes de la console**,
tirées du même catalogue de traduction que la console : ce que vous lisez ici est ce
que vous voyez dans le produit. Les tableaux ci-dessous regroupent ces lignes par domaine, après Accueil (la vue d’ensemble)
et avant connexion, configuration et compte. Les neuf anciennes adresses d’annuaire de domaine figurent
parmi les routes montées hors du registre de fonctionnalités ; chacune redirige désormais vers
son lieu.

:::note[Les permissions sont appliquées par le moteur, pas par ce tableau]
La colonne `Requis` indique la permission vérifiée par la console avant de proposer une
route, à partir des permissions effectives renvoyées par le moteur. Le moteur autorise
les requêtes API indépendamment, y compris celles effectuées en dehors de la console.
La visibilité d’une entrée ne garantit pas que le module soit configuré ou prêt à fonctionner.
Consultez [Rôles et permissions](/fr/reference/modules/vi-governance/).
:::

## Comment lire cette page

- **Écran** — le nom employé par la rangée de liens au-dessus d’un écran et par la palette
  de commandes.
- **Chemin** — l'URL sous l'origine de la console de votre déploiement. C'est un contrat
  publié : un favori, un lien profond de runbook et une référence croisée de la documentation
  utilisent tous cette chaîne.
- **Requis** — la permission RBAC. `tout utilisateur connecté` signifie que la route est
  ouverte à chaque principal authentifié ; **sans connexion** signifie qu'elle est servie
  avant même l'existence d'une session.
- **Référence** — la page qu'ouvre le propre lien d'aide de la console pour cet écran.

Les titres suivants sont les domaines du registre selon lesquels le recensement des routes
regroupe les écrans :
Infrastructure, IA, Données & contexte, Travail & communications, Automatisation,
Sécurité & identité, Livraison, Supervision, puis Système & paramètres.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

La console publie **66 routes**. Elles figurent toutes dans les tableaux ci-dessous, avec la
permission requise et la page de référence ouverte par leur lien d'aide dans le produit.

### Accueil

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Accueil | `/` | Vue d'ensemble du parc et état de santé en un coup d'œil | tout utilisateur connecté | [accueil de la documentation](/fr/) |

### Infrastructure

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Parc | `/estate` | Consultez les ressources configurées et leurs relations, y compris les dépendances des éléments de travail. | `sessions:run:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Inventaire | `/inventory` | Découvrez et cataloguez les agents, serveurs MCP et modèles que les connecteurs ont observés. | `inventory:catalog:read` | [reference/modules/i-inventory](/fr/reference/modules/i-inventory/) |
| Espace de travail | `/workspace` | Agents, sessions, ressources et activité limités à un espace de travail | `tenant:read` | [reference/modules/xx-multi-tenancy](/fr/reference/modules/xx-multi-tenancy/) |

### IA

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Outils des agents | `/agent-tools` | Voir les comptes et clés d’API avec lesquels les sessions s’exécutent ; installer et connecter les outils revient aux administrateurs du déploiement | `sessions:account:read` | [how-to/add-a-provider](/fr/how-to/add-a-provider/) |
| Opérer les sessions | `/agentops` | Lancer des sessions et suivre ce que fait chacune, y compris celles qu’Olivares trouve | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/fr/how-to/run-claude-code-with-olivares/) |
| Opérations sur les modèles | `/model-operations` | Modèles détenus, admission et déploiements | `models:registry:read` | [reference/modules/xxiii-model-operations](/fr/reference/modules/xxiii-model-operations/) |
| Modèles | `/models` | Modèles, routage et gouvernance | `models:catalog:read` | [reference/modules/x-models](/fr/reference/modules/x-models/) |
| Plateformes | `/platforms` | Surfaces de déploiement, matrice de conformité et cycle de vie des modèles par plateforme | `models:platforms:read` | [reference/modules/x-models](/fr/reference/modules/x-models/) |
| Comptes de fournisseur | `/provider-accounts` | Lister les comptes de fournisseur nommés et adopter un profil de fournisseur existant comme compte | `sessions:account:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Liaisons de sources | `/provider-bindings` | Lier des sources configurées à des profils de fournisseur | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Profils de fournisseur | `/provider-profiles` | Enregistrez et administrez les répertoires de fournisseur sous lesquels les sessions se lancent, et lisez leur configuration à la demande | `sessions:profile:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Fournisseurs | `/providers` | Enregistrez les clés d'API et les points de terminaison avec lesquels les sessions démarrent ; testez-les, changez-les et révoquez-les | `sessions:provider:read` | [how-to/add-a-provider](/fr/how-to/add-a-provider/) |
| Limites de débit | `/rate-limits` | Inventaire des limites de débit Anthropic (lecture seule) | `models:ratelimits:read` | [reference/modules/x-models](/fr/reference/modules/x-models/) |
| Portée | `/reach` | Voyez quelles destinations chaque outil peut atteindre et lesquelles attendent une approbation. | `accessmap:graph:read` | [reference/modules/iii-access-map](/fr/reference/modules/iii-access-map/) |
| Bac à sable | `/sandbox` | Test isolé des agents et relecture | `sandbox:run:read` | [reference/modules/xvii-sandbox](/fr/reference/modules/xvii-sandbox/) |
| Sessions | `/sessions` | Lancer des sessions et suivre ce que fait chacune, y compris celles qu’Olivares trouve | `sessions:live:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Voix | `/voice` | Sessions vocales et en temps réel | `voice:session:read` | [reference/modules/xvi-voice](/fr/reference/modules/xvi-voice/) |
| Modèles d’espace de travail | `/workspace-templates` | Instantanés réutilisables de configuration de session : hooks, paramètres, connecteurs et politiques. | `sessions:template:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |

### Données & contexte

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Agent Artifacts | `/agent-artifacts` | Skills, extensions MCP et fichiers d'instructions : registre, posture et BOM de chaîne d'approvisionnement | `models:registry:read` | [reference/modules/xxiii-model-operations](/fr/reference/modules/xxiii-model-operations/) |
| MCP et compétences | `/capabilities` | Gouvernez les serveurs MCP, les compétences et les outils | `capabilities:catalog:read` | [reference/modules/v-capabilities](/fr/reference/modules/v-capabilities/) |
| Catalogue | `/catalog` | Agents et capacités curés et approuvés | `catalog:entry:read` | [reference/modules/xiv-catalog](/fr/reference/modules/xiv-catalog/) |
| Connaissances | `/knowledge` | Bases de connaissances, RAG et lignage des données | `knowledge:kb:read` | [reference/modules/viii-knowledge](/fr/reference/modules/viii-knowledge/) |

### Travail & communications

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Communications | `/communications` | Canaux, avis directs et boîte de réception personnelle de l'espace de travail sélectionné | `sessions:channel:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Administration des canaux | `/communications/administration` | Administrer les canaux : configuration et historique des droits | `sessions:channel:admin` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Transferts | `/communications/handoffs` | Offres de responsabilité de travail qui vous sont adressées : lisez le contexte, puis acceptez ou refusez | `sessions:delivery:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Boîte de réception des communications | `/communications/inbox` | Les messages qui vous sont adressés : ouvrez et accusez réception de chacun | `sessions:delivery:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Nouveau canal | `/communications/new` | Créez un canal avec des droits initiaux explicites | `sessions:channel:write` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Liaisons de protocole | `/communications/protocol-bindings` | Composez et réconciliez les liaisons A2A et MCP gouvernées | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Travail | `/work` | Travail partagé : unités, dépendances, acceptation et décisions | `sessions:work:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |

### Automatisation

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Alertes | `/alerting` | Acheminez les constats vers des destinations et inspectez les livraisons | `notify:route:read` | [reference/modules/xv-notify](/fr/reference/modules/xv-notify/) |
| Webhooks et événements | `/eventing` | Abonnements aux webhooks sortants, leur journal de livraison et la file des lettres mortes. | `eventing:subscription:read` | [reference/modules/eventing](/fr/reference/modules/eventing/) |

### Sécurité & identité

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Carte des accès | `/access-map` | Ce que chaque agent lit et écrit (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/fr/reference/modules/iii-access-map/) |
| Export des politiques | `/agentcore-export` | Planifier, relire et appliquer la projection des règles de gouvernance de ce tenant sur AWS AgentCore sous forme de politiques Cedar ; la planification n’écrit rien | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |
| Politique gérée | `/claude-policy` | Politique gérée, hooks, MCP, bac à sable et policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/fr/how-to/connectors/claude-code-hooks-pep/) |
| Identité et accès | `/identity` | SSO, SCIM et les identités qu’utilisent vos agents | `governance:identity:read` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |
| Proxy d'inférence | `/inference-proxy` | Gates du proxy, règles DLP de sortie et approbations d'appareils | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/fr/reference/modules/inferenceproxy/) |
| Coupe-circuit | `/killswitch` | Arrêt d'urgence, reprise en double contrôle et confinement par gardien | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/fr/how-to/cookbook/kill-switch-drill/) |
| Permissions | `/permissions` | Identité, rôles et approbations | `governance:identity:read` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |
| Politiques de routines | `/routine-policies` | Planchers de cadence, plafonds de concurrence, exigences d'approbation et listes d'autorisation cron pour les routines Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |
| Sécurité | `/security` | Garde-fous, investigation et anomalies | `security:finding:read` | [reference/modules/ix-security](/fr/reference/modules/ix-security/) |
| Coffre-fort | `/vault` | Vos mots de passe, notes et codes d’authentification | `tenant:read` | [reference/console](/fr/reference/console/) |

### Livraison

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Publication Git | `/git-publication` | Pousser des commits, ouvrir des pull requests et fusionner via des cibles Git approuvées | `gitpublish:target:read` | [reference/modules/gitpublish](/fr/reference/modules/gitpublish/) |

### Supervision

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Adoption | `/adoption` | Productivité, acceptation et répartition des modèles | `adoption:metrics:read` | [reference/modules/claudeadoption](/fr/reference/modules/claudeadoption/) |
| Chaîne d'approvisionnement | `/attestation` | Attestation de version : SLSA, SBOM, VEX et Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/fr/how-to/verify-a-release/) |
| Registre d'audit | `/audit` | Registre de preuves à altération détectable | `audit:read` | [reference/modules/ix-security](/fr/reference/modules/ix-security/) |
| Tableaux de bord | `/dashboards` | Indicateurs clés et reporting pour la direction | tout utilisateur connecté | [reference/modules/xxi-executive-dashboards](/fr/reference/modules/xxi-executive-dashboards/) |
| Évaluations | `/evals` | Qualité, évaluations et régression | `evals:run:read` | [reference/modules/xii-evals](/fr/reference/modules/xii-evals/) |
| Santé et SLA | `/health` | Disponibilité et SLA des agents et des MCP | `health:status:read` | [reference/modules/xxii-health](/fr/reference/modules/xxii-health/) |
| Observabilité | `/observability` | Santé de l'ingestion par standard et exploration des traces | `health:status:read` | [reference/modules/observability](/fr/reference/modules/observability/) |
| Conservation | `/retention` | Durée de conservation de chaque classe d’enregistrements, balayage qui l’applique et ses certificats scellés | `compliance:retention:read` | [reference/modules/xiii-compliance](/fr/reference/modules/xiii-compliance/) |
| Budgets | `/stored-budgets` | Consommation par rapport à chaque limite enregistrée. La capacité réservée compte dans la limite. | `finops:budget:read` | [reference/modules/xi-finops](/fr/reference/modules/xi-finops/) |
| Usage | `/usage` | Les limites officielles sur 5 heures et à la semaine de chaque compte, la plus proche d'abord, et les jetons et le coût du jour. | `sessions:run:read` | [reference/console](/fr/reference/console/) |

### Système & paramètres

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| API Playground | `/api-playground` | Explorez et testez interactivement l'API du control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/fr/reference/modules/xix-api-manage-as-code/) |
| Sauvegardes | `/backups` | Déclenchez, planifiez, téléchargez et restaurez des sauvegardes, avec une seconde confirmation sur le chemin destructif. | `system:admin` | [how-to/backup-and-restore](/fr/how-to/backup-and-restore/) |
| Administration | `/console` | Utilisateurs, SSO/IdP, espaces de travail, groupes d’agents, rôles, secrets, connecteurs, clés API et licence de cette installation | `tenant:admin` | [reference/modules/xx-multi-tenancy](/fr/reference/modules/xx-multi-tenancy/) |
| Comparaison de source | `/console/sources/diff` | Comparez une révision de base et une révision head d'un dépôt Git connecté, fichier par fichier | `system:admin` | [reference/console](/fr/reference/console/) |
| Journaux | `/logs` | Flux en direct du journal du moteur, filtré par niveau et module, avec recherche et pause. | `system:admin` | [how-to/troubleshooting](/fr/how-to/troubleshooting/) |
| Assistant de configuration | `/onboarding` | Configuration du déploiement étape par étape | `system:admin` | [start/quickstart](/fr/start/quickstart/) |
| Mises à jour | `/updates` | La version que ce moteur exécute, la version signée la plus récente de son canal et une action pour l'installer | `system:admin` | [how-to/upgrade-and-rollback](/fr/how-to/upgrade-and-rollback/) |

### Connexion, configuration et compte

Ces routes sont montées hors du registre des fonctionnalités. Celles marquées **sans
connexion** sont servies avant l'existence d'une session ; ce sont les seules routes de la
console dans ce cas.

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Accepter une invitation | `/accept-invite` | Destination d'un lien d'invitation envoyé par e-mail : l'invité définit un mot de passe et rejoint l'espace de travail, sans session préalable. | **sans connexion** | — |
| Se connecter | `/login` | Page de connexion par identifiants et token pour un compte déjà provisionné. | **sans connexion** | — |
| Associer un appareil | `/pair` | Destination d’un lien d’association d’appareil à usage unique : le nouvel appareil se connecte sans session préalable. | **sans connexion** | — |
| Paramètres | `/settings` | Paramètres d'espace de travail et de compte | tout utilisateur connecté | — |
| Configuration initiale | `/setup` | Page unique qui transforme un nouveau déploiement en système utilisable : elle consomme le token de configuration et crée le premier compte owner. | **sans connexion** | — |
| État public | `/status-page` | Santé des composants pour les personnes non connectées, actualisée automatiquement tant que la page reste ouverte. | **sans connexion** | — |

<!-- END GENERATED olivares-console-routes -->

## Ce que cette page ne vous dit pas

C'est une carte, pas un manuel. Elle indique les écrans existants, leur emplacement et qui
peut les ouvrir ; elle ne vous guide pas dans une tâche. Pour cela, commencez par les
[Parcours par rôle](/fr/start/paths-by-role/) ou les [guides pratiques](/fr/how-to/self-hosting/).

Les écrans dont le backend fonctionne en deny-closed jusqu'à son provisionnement par un
opérateur apparaissent ici comme les autres : la route existe et la permission est réelle.
La [vue d'ensemble des modules](/fr/reference/modules/overview/) consigne quel module agit et
lequel est gated, et [Honnêteté et limites](/fr/start/honesty-and-limits/) énonce la règle
générale. La liste d’un annuaire de domaine n’est pas une lecture en direct de capacité ou
d’état de préparation.
