---
title: Référence de la console — chaque écran et la permission requise
description: >-
  Toutes les routes publiées par la console Olivares AI, regroupées dans ses cinq hubs,
  avec la permission RBAC requise par chacune et la page de référence ouverte par son
  lien d'aide dans le produit. Généré depuis le recensement des routes de la console.
---

Cette page est la carte de la console. Elle répertorie **toutes les routes montées par
l'application** — pas une sélection, ni celles que quelqu'un a pensé à documenter — avec la
permission nécessaire à un principal pour y accéder et l'endroit où en savoir plus.

Elle est **générée**. La liste provient de `web/src/features/route-census.json`, le
recensement append-only que `registry.route-conservation.test.ts` compare au routeur compilé ;
un écran ne peut donc être ajouté, déplacé ou perdu sans que cette page change avec lui. Le
nom et la description en une ligne de chaque écran sont **les propres chaînes de la console**,
tirées du même catalogue de traduction que la barre latérale : ce que vous lisez ici est ce
que vous voyez dans le produit.

:::note[Les permissions sont appliquées par le moteur, pas par ce tableau]
La colonne `Requis` indique la permission vérifiée par la console avant de proposer la route,
et reflète le RBAC du moteur. Le moteur reste l'autorité : un lien profond vers un écran pour
lequel vous ne détenez pas la permission est refusé par l'API, et pas seulement masqué dans la
barre latérale. Consultez [Rôles et permissions](/fr/reference/modules/vi-governance/).
:::

## Comment lire cette page

- **Écran** — le nom employé par la barre latérale et la palette de commandes.
- **Chemin** — l'URL sous l'origine de la console de votre déploiement. C'est un contrat
  publié : un favori, un lien profond de runbook et une référence croisée de la documentation
  utilisent tous cette chaîne.
- **Requis** — la permission RBAC. `tout utilisateur connecté` signifie que la route est
  ouverte à chaque principal authentifié ; **sans connexion** signifie qu'elle est servie
  avant même l'existence d'une session.
- **Référence** — la page qu'ouvre le propre lien d'aide de la console pour cet écran.

Les cinq titres suivants sont les hubs de la console, dans l'ordre où la barre latérale les
affiche.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

La console publie **59 routes**. Elles figurent toutes dans les tableaux ci-dessous, avec la
permission requise et la page de référence ouverte par leur lien d'aide dans le produit.

### Exploiter

| Écran | Chemin | Description | Requis | Référence |
|---|---|---|---|---|
| Vue d'ensemble | `/` | Vue d'ensemble du parc et état de santé en un coup d'œil | tout utilisateur connecté | [accueil de la documentation](/fr/) |
| Claude Code | `/agentops` | Créez, attachez-vous à et gouvernez les sessions Claude Code — sans SSH | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/fr/how-to/run-claude-code-with-olivares/) |
| Sauvegardes | `/backups` | Déclenchez, planifiez, téléchargez et restaurez des sauvegardes, avec une seconde confirmation sur le chemin destructif. | `system:admin` | [how-to/backup-and-restore](/fr/how-to/backup-and-restore/) |
| Santé et SLA | `/health` | Disponibilité et SLA des agents et des MCP | `health:status:read` | [reference/modules/xxii-health](/fr/reference/modules/xxii-health/) |
| Coupe-circuit | `/killswitch` | Arrêt d'urgence, reprise en double contrôle et confinement par gardien | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/fr/how-to/cookbook/kill-switch-drill/) |
| Journaux | `/logs` | Flux en direct du journal du moteur, filtré par niveau et module, avec recherche et pause. | `system:admin` | [how-to/troubleshooting](/fr/how-to/troubleshooting/) |
| Observabilité | `/observability` | Santé de l'ingestion par standard et exploration des traces | `health:status:read` | [reference/modules/observability](/fr/reference/modules/observability/) |
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
générale.
