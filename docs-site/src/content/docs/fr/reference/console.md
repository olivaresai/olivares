---
title: Référence de la console — chaque écran et la permission requise
description: >-
  Toutes les routes publiées par la console Olivares AI, indexées par catégories
  historiques de hub, avec la permission RBAC requise par chacune et la page de
  référence ouverte par son lien d'aide dans le produit. Généré depuis le
  recensement des routes de la console.
---

Cette page est la carte de la console. Elle répertorie **toutes les routes montées par
l'application** — pas une sélection, ni celles que quelqu'un a pensé à documenter — avec la
permission nécessaire à un principal pour y accéder et l'endroit où en savoir plus.

La barre latérale actuelle compte **neuf domaines avec des sections**, et non cinq hubs.
Après Vue d’ensemble, elle affiche les domaines que ce principal peut réellement ouvrir,
chacun avec une page d’annuaire et des modules dépliables, puis le raccourci épinglé
Paramètres. Un filtre de navigation remplace cet arbre groupé par une liste classée des
correspondances autorisées ; la palette de commandes utilise le même index, le même
classement et la même projection de permissions. Un annuaire de domaine est une page de
liens — les entrées que vos permissions autorisent — et non une lecture en direct de
capacité, de disponibilité ou d’état de préparation.

Elle est **générée**. La liste provient de `web/src/features/route-census.json`, le
recensement append-only que `registry.route-conservation.test.ts` compare au routeur compilé ;
un écran ne peut donc être ajouté, déplacé ou perdu sans que cette page change avec lui. Le
nom et la description en une ligne de chaque écran sont **les propres chaînes de la console**,
tirées du même catalogue de traduction que la barre latérale : ce que vous lisez ici est ce
que vous voyez dans le produit. Les tableaux ci-dessous regroupent encore ces lignes selon
les cinq catégories historiques de hub (Exploiter, Automatiser, Connecter, Gouverner,
Prouver), plus connexion, configuration et compte. Ce regroupement est un index, pas l’ordre
actuel de la barre latérale. Les neuf pages d’annuaire de domaine figurent pour l’instant
parmi les routes montées hors du registre de fonctionnalités.

:::note[Les permissions sont appliquées par le moteur, pas par ce tableau]
La colonne `Requis` indique la permission vérifiée par la console avant de proposer une
route, à partir des permissions effectives renvoyées par le moteur. Le moteur autorise
les requêtes API indépendamment, y compris celles effectuées en dehors de la console.
La visibilité d’une entrée ne garantit pas que le module soit configuré ou prêt à fonctionner.
Consultez [Rôles et permissions](/fr/reference/modules/vi-governance/).
:::

## Comment lire cette page

- **Écran** — le nom employé par la barre latérale, les annuaires de domaine et la palette
  de commandes.
- **Chemin** — l'URL sous l'origine de la console de votre déploiement. C'est un contrat
  publié : un favori, un lien profond de runbook et une référence croisée de la documentation
  utilisent tous cette chaîne.
- **Requis** — la permission RBAC. `tout utilisateur connecté` signifie que la route est
  ouverte à chaque principal authentifié ; **sans connexion** signifie qu'elle est servie
  avant même l'existence d'une session.
- **Référence** — la page qu'ouvre le propre lien d'aide de la console pour cet écran.

Les titres suivants sont ces catégories historiques de hub, dans l’ordre de l’index généré —
pas l’ordre d’affichage de la barre latérale. La barre actuelle liste les domaines ainsi :
Infrastructure, IA, Données & contexte, Travail & communications, Automatisation,
Sécurité & identité, Déploiement, Observabilité & preuves, puis Système & paramètres.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

La console publie **75 routes**. Elles figurent toutes dans les tableaux ci-dessous, avec la
permission requise et la page de référence ouverte par leur lien d'aide dans le produit.

### Exploiter

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Vue d'ensemble | `/` | Vue d'ensemble du parc et état de santé en un coup d'œil | tout utilisateur connecté | [accueil de la documentation](/fr/) |
| Claude Code | `/agentops` | Créez, attachez-vous à et gouvernez les sessions Claude Code — sans SSH | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/fr/how-to/run-claude-code-with-olivares/) |
| Sauvegardes | `/backups` | Déclenchez, planifiez, téléchargez et restaurez des sauvegardes, avec une seconde confirmation sur le chemin destructif. | `system:admin` | [how-to/backup-and-restore](/fr/how-to/backup-and-restore/) |
| Communications | `/communications` | Canaux, avis directs et boîte de réception personnelle de l'espace de travail sélectionné | `sessions:channel:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Administration des canaux | `/communications/administration` | Administrer les canaux : configuration et historique des droits, chaque acte sous l'ETag actuel du canal | `sessions:channel:admin` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Transferts | `/communications/handoffs` | Offres de responsabilité de travail qui vous sont adressées : lisez le contexte, puis acceptez ou refusez | `sessions:delivery:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Boîte de réception des communications | `/communications/inbox` | Votre boîte exacte : les remises qui vous sont adressées, lues à neuf et accusées explicitement | `sessions:delivery:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Nouveau canal | `/communications/new` | Créez un canal avec des droits initiaux explicites | `sessions:channel:write` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Santé et SLA | `/health` | Disponibilité et SLA des agents et des MCP | `health:status:read` | [reference/modules/xxii-health](/fr/reference/modules/xxii-health/) |
| Coupe-circuit | `/killswitch` | Arrêt d'urgence, reprise en double contrôle et confinement par gardien | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/fr/how-to/cookbook/kill-switch-drill/) |
| Journaux | `/logs` | Flux en direct du journal du moteur, filtré par niveau et module, avec recherche et pause. | `system:admin` | [how-to/troubleshooting](/fr/how-to/troubleshooting/) |
| Observabilité | `/observability` | Santé de l'ingestion par standard et exploration des traces | `health:status:read` | [reference/modules/observability](/fr/reference/modules/observability/) |
| Liaisons de sources | `/provider-bindings` | Dédiez des sources configurées, à la révision appliquée par ce nœud, à des profils de fournisseur | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Profils de fournisseur | `/provider-profiles` | Enregistrez et administrez les répertoires de fournisseur sous lesquels les sessions se lancent, et lisez leur configuration à la demande | `sessions:profile:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Bac à sable | `/sandbox` | Test isolé des agents et relecture | `sandbox:run:read` | [reference/modules/xvii-sandbox](/fr/reference/modules/xvii-sandbox/) |
| Sessions | `/sessions` | Opération des agents en direct et chronologies | `sessions:live:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Locataires | `/tenants` | Retirer ou rétablir le service d'un locataire | `system:admin` | [how-to/troubleshooting](/fr/how-to/troubleshooting/) |
| Voix | `/voice` | Sessions vocales et en temps réel | `voice:session:read` | [reference/modules/xvi-voice](/fr/reference/modules/xvi-voice/) |
| Travail | `/work` | Le backlog durable entre sessions : éléments, dépendances, acceptation et décisions | `sessions:work:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Espace de travail | `/workspace` | Agents, sessions, ressources et activité limités à un espace de travail | `tenant:read` | [reference/modules/xx-multi-tenancy](/fr/reference/modules/xx-multi-tenancy/) |
| Modèles de workspace | `/workspace-templates` | Instantanés réutilisables de configuration de session : hooks, settings, connectors et policies. | `sessions:template:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |

### Automatiser

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Alertes | `/alerting` | Acheminez les constats vers des destinations et inspectez les livraisons | `notify:route:read` | [reference/modules/xv-notify](/fr/reference/modules/xv-notify/) |
| Automatisations | `/automations` | Les trois rails d'automatisation et leur catalogue de déclencheurs | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/fr/reference/modules/iv-orchestration/) |
| Webhooks et événements | `/eventing` | Abonnements aux webhooks sortants, leur journal de livraison et la file des lettres mortes. | `eventing:subscription:read` | [reference/modules/eventing](/fr/reference/modules/eventing/) |
| Orchestration | `/orchestration` | Coordination d'agent à agent et planifications | `orchestration:graph:read` | [reference/modules/iv-orchestration](/fr/reference/modules/iv-orchestration/) |

### Connecter

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| API Playground | `/api-playground` | Explorez et testez interactivement l'API du control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/fr/reference/modules/xix-api-manage-as-code/) |
| MCP et compétences | `/capabilities` | Gouvernez les serveurs MCP, les compétences et les outils | `capabilities:catalog:read` | [reference/modules/v-capabilities](/fr/reference/modules/v-capabilities/) |
| Catalogue | `/catalog` | Agents et capacités curés et approuvés | `catalog:entry:read` | [reference/modules/xiv-catalog](/fr/reference/modules/xiv-catalog/) |
| Liaisons de protocole | `/communications/protocol-bindings` | Composez et réconciliez les liaisons A2A et MCP gouvernées | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/fr/reference/modules/ii-sessions/) |
| Déploiement | `/deploy` | Provisionnez et câblez les agents à l'infrastructure | `deploy:deployment:read` | [reference/modules/vii-deploy](/fr/reference/modules/vii-deploy/) |
| Inventaire | `/inventory` | Découvrez et cataloguez chaque agent, MCP et modèle | `inventory:catalog:read` | [reference/modules/i-inventory](/fr/reference/modules/i-inventory/) |
| Connaissances | `/knowledge` | Bases de connaissances, RAG et lignage des données | `knowledge:kb:read` | [reference/modules/viii-knowledge](/fr/reference/modules/viii-knowledge/) |
| Opérations sur les modèles | `/model-operations` | Modèles détenus, admission et déploiements | `models:registry:read` | [reference/modules/xxiii-model-operations](/fr/reference/modules/xxiii-model-operations/) |
| Modèles | `/models` | Modèles, routage et clés de fournisseur | `models:catalog:read` | [reference/modules/x-models](/fr/reference/modules/x-models/) |
| Assistant de configuration | `/onboarding` | Configuration du déploiement étape par étape | `system:admin` | [start/quickstart](/fr/start/quickstart/) |
| Plateformes | `/platforms` | Surfaces de déploiement, matrice de conformité et cycle de vie des modèles par plateforme | `models:platforms:read` | [reference/modules/x-models](/fr/reference/modules/x-models/) |

### Gouverner

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Carte des accès | `/access-map` | Ce que chaque agent lit et écrit (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/fr/reference/modules/iii-access-map/) |
| Export AgentCore | `/agentcore-export` | Planifiez et appliquez l'export de policy Cedar vers AWS AgentCore, et examinez les changements avant leur application. | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |
| Gouvernance de Claude Code | `/claude-policy` | Politique gérée, hooks, MCP, bac à sable et policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/fr/how-to/connectors/claude-code-hooks-pep/) |
| Console de contrôle | `/console` | Intégrez les utilisateurs, connectez SSO/IdP et structurez les espaces de travail et les groupes d'agents. | `tenant:admin` | [reference/modules/xx-multi-tenancy](/fr/reference/modules/xx-multi-tenancy/) |
| Identité et NHI | `/identity` | SSO, SCIM, le répertoire des NHI et le graphe WIF | `governance:identity:read` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |
| Proxy d'inférence | `/inference-proxy` | Gates du proxy, règles DLP de sortie et approbations d'appareils | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/fr/reference/modules/inferenceproxy/) |
| Permissions | `/permissions` | Identité, rôles et approbations | `governance:identity:read` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |
| Limites de débit | `/rate-limits` | Inventaire des limites de débit Anthropic (lecture seule) | `models:ratelimits:read` | [reference/modules/x-models](/fr/reference/modules/x-models/) |
| Résidence des données | `/residency` | Épinglez chaque organisation à une région, ou laissez-la non épinglée | `system:admin` | [reference/modules/xiii-compliance](/fr/reference/modules/xiii-compliance/) |
| Politiques de routines | `/routine-policies` | Planchers de cadence, plafonds de concurrence, exigences d'approbation et listes d'autorisation cron pour les routines Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/fr/reference/modules/vi-governance/) |

### Prouver

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Adoption de Claude Code | `/adoption` | Productivité, acceptation et répartition des modèles | `adoption:metrics:read` | [reference/modules/claudeadoption](/fr/reference/modules/claudeadoption/) |
| Agent Artifacts | `/agent-artifacts` | Skills, extensions MCP et fichiers d'instructions : registre, posture et BOM de chaîne d'approvisionnement | `models:registry:read` | [reference/modules/xxiii-model-operations](/fr/reference/modules/xxiii-model-operations/) |
| Chaîne d'approvisionnement | `/attestation` | Attestation de version : SLSA, SBOM, VEX et Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/fr/how-to/verify-a-release/) |
| Registre d'audit | `/audit` | Registre de preuves à altération détectable | `audit:read` | [reference/modules/ix-security](/fr/reference/modules/ix-security/) |
| Conformité | `/compliance` | Référentiels, contrôles et preuves | `compliance:framework:read` | [reference/modules/xiii-compliance](/fr/reference/modules/xiii-compliance/) |
| Tableaux de bord | `/dashboards` | Indicateurs clés et reporting pour la direction | tout utilisateur connecté | [reference/modules/xxi-executive-dashboards](/fr/reference/modules/xxi-executive-dashboards/) |
| Évaluations | `/evals` | Qualité, évaluations et régression | `evals:run:read` | [reference/modules/xii-evals](/fr/reference/modules/xii-evals/) |
| Coût et FinOps | `/finops` | Coût des tokens, budgets et dépenses | `finops:spend:read` | [reference/modules/xi-finops](/fr/reference/modules/xi-finops/) |
| Export de posture | `/posture-export` | Exportez la posture réelle pour une tour de contrôle | `posture:export:read` | [reference/modules/posture-export](/fr/reference/modules/posture-export/) |
| Enregistrements | `/recordings` | Enregistrement et relecture des sessions privilégiées | `recording:session:admin` | [reference/modules/recording](/fr/reference/modules/recording/) |
| Équipe rouge | `/red-team` | Test adverse de vos agents | `redteam:target:read` | [reference/modules/xviii-redteam](/fr/reference/modules/xviii-redteam/) |
| Rapports | `/reporting` | Générez et téléchargez des rapports de gouvernance | `reporting:report:read` | [reference/modules/reporting](/fr/reference/modules/reporting/) |
| Sécurité | `/security` | Garde-fous, investigation et anomalies | `security:finding:read` | [reference/modules/ix-security](/fr/reference/modules/ix-security/) |
| Visionneuse de sessions | `/session-viewer/$id` (lien profond uniquement) | Chronologie complète d'une session enregistrée, atteinte depuis une ligne des Enregistrements et non depuis la barre latérale. | `recording:session:admin` | [reference/modules/recording](/fr/reference/modules/recording/) |
| Coûts par équipe | `/team-costs` | Dépenses attribuées par équipe, développables en ventilation par projet et par modèle. | `finops:spend:read` | [reference/modules/xi-finops](/fr/reference/modules/xi-finops/) |

### Connexion, configuration et compte

Ces routes sont montées hors du registre des fonctionnalités. Celles marquées **sans
connexion** sont servies avant l'existence d'une session ; ce sont les seules routes de la
console dans ce cas.

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Accepter une invitation | `/accept-invite` | Destination d'un lien d'invitation envoyé par e-mail : l'invité définit un mot de passe et rejoint le workspace, sans session préalable. | **sans connexion** | — |
| IA | `/areas/ai` | Répertoire du domaine IA : observation et gestion des sessions, profils et environnements des fournisseurs, modèles, exécution spécialisée et référence des fournisseurs. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Automatisation | `/areas/automation` | Répertoire du domaine Automatisation : flux et orchestration, événements et notifications. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Données & contexte | `/areas/data-context` | Répertoire du domaine Données & contexte : capacités, connaissances et artefacts. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Déploiement | `/areas/deployment` | Répertoire du domaine Déploiement : préparation et contrôle des déploiements. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Infrastructure | `/areas/infrastructure` | Répertoire du domaine Infrastructure : inventaire et espaces de travail de l’environnement. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Observabilité & preuves | `/areas/observation` | Répertoire du domaine Observabilité & preuves : état et activité, coûts et adoption, audit et enregistrements, évaluation et preuves. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Sécurité & identité | `/areas/security-identity` | Répertoire du domaine Sécurité & identité : identité et accès, politiques, protection et réponse, frontières de gouvernance. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Système & paramètres | `/areas/system` | Répertoire du domaine Système & paramètres : administration, installation et maintenance, outils de développement et préférences personnelles. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Travail & communications | `/areas/work-communications` | Répertoire du domaine Travail & communications : travail persistant entre sessions et communications gouvernées. Liste les entrées autorisées par vos permissions. | tout utilisateur connecté | — |
| Se connecter | `/login` | Page de connexion par identifiants et token pour un compte déjà provisionné. | **sans connexion** | — |
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
