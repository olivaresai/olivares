<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Langues :** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · **Français**

**Le plan de contrôle de l'IA que vous faites réellement tourner.** Intégrez-le, mettez-le au travail, connectez-le à vos systèmes et gouvernez-en chaque partie — un binaire auto-hébergé, d'un serveur à domicile à une entreprise réglementée.

[Installation](#install) ·
[Démarrage rapide](#quickstart) ·
[Exemples](examples/) ·
[Architecture](#architecture) ·
[Documentation](#documentation) ·
[Sécurité](SECURITY.md) ·
[Contribuer](CONTRIBUTING.md) ·
[olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

<!-- OpenSSF Best Practices Badge (self-certification).
     Registration at https://www.bestpractices.dev is pending (a maintainer action); the
     evidence map is in docs/openssf-badge.md. Once a project ID is assigned, uncomment:
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/PROJECT_ID/badge)](https://www.bestpractices.dev/projects/PROJECT_ID)
-->

</div>

> Statut : **beta**, en développement actif. Le moteur fonctionne de bout en bout — un binaire statique unique avec la console embarquée, qui ingère des signaux réels depuis les systèmes où s'exécute votre IA. Les API, les schémas et la surface des modules peuvent encore évoluer avant la 1.0, et certaines jointures d'actionnement (seams) — des points d'intégration déclarés, deny-closed — restent fermées jusqu'à leur provisionnement (voir [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md)). Les versions sont produites depuis ce dépôt ; les [chemins d'installation](#install) ci-dessous seront publiés avec la première version taguée.

> Chaîne d'approvisionnement : les versions sont compilées sur GitHub Actions avec une chaîne de confiance signée par type d'artefact — les archives sont livrées avec des SBOM SPDX et des attestations in-toto, les images de conteneur sont signées par cosign avec une attestation SBOM d'image, et chaque artefact (paquets et chart compris) est couvert par le manifeste de checksums signé par cosign, ainsi qu'un document OpenVEX et une provenance de build SLSA pour l'ensemble. Vérifiez toute version avec [`scripts/verify-release.sh`](scripts/verify-release.sh) ; la chaîne exacte par type d'artefact, le chemin air-gapped et le chart Helm sont documentés dans [`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md) et [`deploy/`](deploy/).

## Ce qu'est Olivares AI

L'IA a cessé depuis longtemps d'être une seule fenêtre de discussion. Ce que vous exécutez réellement est désormais un petit parc (estate) : des agents de programmation dans des terminaux, des serveurs MCP, des endpoints de modèles, des comptes de service et des tâches planifiées, répartis sur des machines qui n'ont jamais été conçues pour former un seul système. Rien ne les fédère, si bien que les questions ordinaires deviennent coûteuses à résoudre : ce qui s'exécute, qui l'a lancé, ce qu'il a atteint, ce qu'il a coûté et qui a donné son accord.

**Olivares AI est le plan qui les fédère.** Il a deux moitiés, livrées dans le même binaire :

- **L'exécuter et le connecter** — un plan durable pour le travail lui-même. Des éléments de travail avec propriétaire, dépendances, critères d'acceptation et décisions ; des leases qui font de la propriété une autorité qu'un détenteur périmé ne peut pas continuer à utiliser ; des sessions lancées, jointes et arrêtées depuis la console, avec des entrées fournies à une exécution en cours ; de la délégation vers un pair distant sur A2A ; MCP comme surface d'outils ; et des sources de contenu gouvernées qui alimentent la récupération. C'est la moitié décrite dans [Le plan de travail](#the-work-plane) ci-dessous, avec l'état de chaque élément indiqué sans détour.
- **Le voir et le gouverner** — l'inventaire de tout ce qui est découvert, une access map read/write de ce que chaque agent et identité atteint réellement, des politiques Cedar, de l'application deny-closed, des budgets qui peuvent refuser la dépense, et un registre signé hash-chained pour prouver ensuite l'ensemble.

Aucune moitié n'est une décoration pour l'autre. Une gouvernance sans plan de travail n'est qu'un tableau de bord sans rien sur quoi agir ; un plan de travail sans gouvernance est un travail dont personne ne peut rendre compte après coup.

**Multi-fournisseur par conception.** Claude Code est intégré au niveau le plus profond — le hook `PreToolUse`/`PostToolUse`, les managed settings, le lancement et l'arrêt depuis la console, l'accès aux modèles par sujet — avec Codex et Grok Build à ses côtés comme surfaces de commande de première classe, et gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw et Hermes portés comme connecteurs à part entière. Chacun indique ce qu'il peut appliquer et ce qu'il peut seulement observer ; aucun n'est le centre de gravité du produit. Ollama et les autres endpoints auto-hébergés sont inventoriés et attribués par le connecteur local, qui est en lecture seule par conception ; les règles de politique et de budget s'appliquent là où l'inférence franchit le proxy gouverné, qui est le seul endroit où elles puissent s'appliquer.

**Qui l'exécute.** La build ouverte constitue toute la plateforme à chacune de ces échelles — les add-ons commerciaux sont du code additif qui s'y superpose, jamais un produit différent :

| Vous êtes | Ce que cela donne |
|---|---|
| **Exploitant un serveur à domicile ou un réseau homelab** | un binaire, SQLite, un volume Docker, lié à la loopback, aucun service externe — la topologie Compose livrée s'exécute sans privilège root et en lecture seule dans 1 CPU et 1 GiB ([`deploy/compose/docker-compose.yml`](deploy/compose/docker-compose.yml)) |
| **Freelance ou consultant indépendant** | un tenant par client — chaque opération de module est épinglée à l'un d'eux — des budgets qui peuvent refuser ou limiter avant la facture, et une exportation de posture que vous pouvez remettre |
| **Un professionnel ou un utilisateur avancé** | le même moteur que celui qu'utilise une entreprise, sans aucune restriction : la build ouverte est l'intégralité de la plateforme ; ce que vous apprenez sur votre propre machine est donc ce que vous exploitez au travail |
| **Équipe d'ingénierie ou petite entreprise** | des éléments de travail partagés et des leases, afin que deux agents — ou deux personnes — ne puissent pas détenir simultanément le même élément de travail ; SSO, rôles et une piste d'audit que personne n'a besoin d'assembler à la main |
| **Entreprise réglementée** | Postgres avec sécurité au niveau ligne, HA avec un rédacteur unique et des répliques de secours, installations air-gapped, preuves mappées à **26 catalogues de cadres**, et archivage WORM sur un support immuable |

Chaque ligne décrit la même build. Plusieurs de ces capacités — SSO, HA, archivage WORM, budgets qui refusent réellement — sont des éléments que vous **provisionnez**, et non des valeurs par défaut disponibles dès le premier démarrage ; la matrice ci-dessous et [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md) précisent, capacité par capacité, ce qui relève de l'un ou de l'autre.

Il s'exécute comme un **binaire Go unique auto-hébergé** avec la console embarquée — sur Linux, Docker, Kubernetes, on-prem ou totalement air-gapped. Il n'y a aucune télémétrie obligatoire ni aucun egress de plan de contrôle par défaut : ce qui traverse votre périmètre est ce que vous configurez pour le traverser — les appels vers vos API de modèles, les sorties SIEM/webhook que vous câblez, un fournisseur d'embeddings externe si vous en provisionnez un. Les collecteurs lisent depuis les systèmes que vous exploitez déjà (pgAudit, CloudTrail, eBPF, MCP, votre IdP), de sorte qu'un collecteur défaillant ne se trouve jamais sur le chemin de données de production.

La couverture et l'attribution portent des niveaux explicites (`firm`/`approximate`/`unknown`, `clean`/`lossy`/`opaque`), l'application est deny-closed là où elle est câblée et une jointure (seam) déclarée là où elle ne l'est pas, et la documentation indique clairement ce qui fonctionne aujourd'hui par rapport à ce qui est au stade de conception. Le produit ne fabriquera pas une certitude qu'il ne peut pas prouver — voir [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md).

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840"
       alt="Carte des accès: Ce que chaque agent lit et écrit dans votre parc — les origines à gauche, les ressources qu’ils touchent à droite, R/RW par couleur.">
</picture>

<sub><b>Carte des accès</b> — Ce que chaque agent lit et écrit dans votre parc — les origines à gauche, les ressources qu’ils touchent à droite, R/RW par couleur.</sub>

</div>

**Voyez-le vous-même en deux commandes** (Go 1.26+, [Task](https://taskfile.dev), pnpm — [prérequis](#quickstart-prerequisites)) :

```sh
task build
./bin/olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 \
  --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps
```

La CI parcourt le même chemin : `task smoke:quickstart` démarre ce parc (estate) de démonstration contre le binaire réel et vérifie ses compteurs d'access map et de dérive. Pour les chemins d'installation et leurs valeurs par défaut opérationnelles, utilisez [Installation](#install) et [Démarrage rapide](#quickstart).

<a name="the-work-plane"></a>
<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840"
       alt="Un seul binaire à toutes les tailles : un serveur domestique ou homelab, un indépendant avec un locataire par client, une équipe d'ingénierie ou une petite entreprise, et une entreprise réglementée. Il tourne sur Linux, Docker, Kubernetes, Helm et en isolement réseau, avec le cloud géré au lancement, et il atteint les fournisseurs de modèles, les clouds et annuaires, les sources de contenu gouvernées et les connecteurs de sortie — la carte des accès y étant une capacité parmi d'autres et non le centre.">
</picture>

<sub>Le même build d'un homelab à une entreprise réglementée.</sub>
</div>

## Le plan de travail

Le plan qui porte le travail est la partie d'Olivares AI que les agents et les personnes partagent, et c'est la partie le plus souvent décrite comme si elle était achevée partout. Ce n'est pas le cas ; voici donc chaque élément, avec ce qui le soutient réellement et jusqu'où il va aujourd'hui.

| Élément | État | Où il se trouve |
|---|---|---|
| **Éléments de travail** — brief, provenance, dépendances, critères d'acceptation, décisions, propriétaire et historique des événements, durables, avec un document de commande unique partagé par REST, CLI et les appelants in-process | **en service, API publique** | [`modules/sessions/work_model.go`](modules/sessions/work_model.go), routes dans [`modules/sessions/work_api.go`](modules/sessions/work_api.go) |
| **Leases** — la propriété comme autorité clôturée et expirante : acquérir, renouveler, libérer, reprendre, révoquer ; un détenteur périmé ne peut pas continuer à agir, et une acquisition concurrente produit exactement un gagnant | **en service, API publique** | [`modules/sessions/work_lease.go`](modules/sessions/work_lease.go) |
| **Messages, accusés de réception et transferts** — une conversation durable liée à un élément de travail, avec replay et rejet des époques périmées | **en service derrière un workflow d'orchestration ; la boîte de réception publique générale n'est délibérément pas câblée** | [`modules/sessions/communication_model.go`](modules/sessions/communication_model.go) ; le test de démarrage qui interdit de câbler le plan public est [`cmd/olivares/communicationauthorityboot_test.go`](cmd/olivares/communicationauthorityboot_test.go) |
| **Lancement pour le travail** — réserver, prendre le lease, *puis* lancer la session, en persistant travail/époque/clôture/exécution afin qu'une nouvelle tentative soit sûre | **en service via l'orchestration** | [`modules/sessions/runtime_work_launch.go`](modules/sessions/runtime_work_launch.go) |
| **Exécution distante sur A2A** — planifier, tester, démarrer, observer et annuler le travail sur un pair autorisé, avec des reçus durables | **en service, et seulement lorsqu'une destination est configurée** ; sans cible autorisée, la jointure n'est pas montée du tout | [`cmd/olivares/wire.go`](cmd/olivares/wire.go), [`cmd/olivares/orchremote.go`](cmd/olivares/orchremote.go) |
| **Mode shadow et autorité finale** — double rapport face au système existant et à un comparateur avant que le plan ne fasse autorité | **non construit** | conception uniquement |

Lisez ce tableau comme la version honnête de « des agents qui se parlent » : les éléments de travail et les leases sont une surface d'API ordinaire que vous pouvez utiliser aujourd'hui ; la conversation entre agents est réelle et durable, mais limitée à un workflow d'orchestration, et il n'existe pas de bus de messages général pour des agents arbitraires ; la délégation distante fonctionne et refuse les pairs inconnus. Ce qui n'existe pas n'est pas présenté comme « bientôt disponible » dans l'interface — c'est listé ici comme absent.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840"
       alt="Comment les agents travaillent ensemble : les surfaces d'agent alimentent un plan de travail durable fait d'éléments de travail, de baux clôturés où un seul détenteur agit à la fois, du lancement pour le travail, et de messages et accusés limités à un espace de travail. La délégation atteint un pair autorisé à travers sa porte d'application. Le plan produit un graphe d'orchestration, un bus d'événements, une carte des accès avec dérive, et un journal signé qui atteint votre SIEM. Le mode fantôme et l'autorité finale sont dessinés en pointillés parce qu'ils ne sont pas construits.">
</picture>

<sub>Les agents partagent un plan de travail durable. Ce qui n'est pas construit est dessiné comme absent.</sub>
</div>

## Ce qu'il couvre

Un binaire, **30 modules**, une console — sur l'ensemble de l'empreinte de votre IA, pas une fonctionnalité isolée. Chaque capacité porte un état de maturité explicite — en service, à la demande, observée, ou jointure deny-closed déclarée — indiqué élément par élément dans [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md).

- **Exécuter le travail.** Éléments de travail durables, leases, lancement orchestré et délégation A2A comme décrit dans [Le plan de travail](#the-work-plane) ; la vue Travail de la console est la surface opérateur du même store, et la vue Orchestration dessine la topologie de délégation à partir des signaux observés.
- **Le voir.** Inventaire de chaque agent, session, modèle, serveur MCP, outil et identité **découvert** — la couverture suit ce que vous connectez, porte des indicateurs explicites et marque ce qu'elle ne peut pas voir comme `unknown` au lieu de deviner ; une **access map** read/write de ce que chacun atteint réellement, avec une vue de **dérive** Permitted-vs-Observed ; sessions en direct, graphe d'orchestration, santé et SLA.
- **Le gouverner et l'appliquer.** Un moteur d'autorisation Cedar (RBAC + deny-overlay + grants positifs scopés) et **quatre points d'application fermés par défaut (deny-closed)** — le hook `PreToolUse`/`PostToolUse` de Claude Code, un proxy d'inférence `/v1/messages` en ligne, une porte MCP `tools/call` et une porte de délégation A2A — afin que les actions non autorisées ne s'exécutent pas : elles sont bloquées, envoyées à une approbation à deux personnes ou réécrites avant de s'exécuter. Cet adjectif est mesuré, non affirmé : un point ne compte que tant qu'un test parcourt son chemin *non configuré* — aucune porte câblée, un document de politique vide, un magasin de politiques qui ne répond pas — et vérifie le refus. Le recensement jointure-preuve est [`scripts/enforcement-seams.tsv`](scripts/enforcement-seams.tsv) ; retirez une preuve, le décompte baisse et la build échoue. La politique s'étend jusque dans la session elle-même : des règles allow/ask/deny par chemin et par sous-arbre dans le hook, des budgets de fenêtre de contexte par surface et par groupe, et un scoping des sources jusqu'au niveau de la session, de l'agent, de l'utilisateur, du groupe ou du rôle. Avec en plus une administration scopée et des rôles personnalisés, le break-glass à double contrôle et un **kill-switch** de parc (estate) qui échoue en mode fermé.
- **Claude et l'écosystème d'agents.** Gouverner Claude Code dans le hook ; lancer, s'attacher à, gouverner et arrêter les sessions Claude Code et leur espace de travail depuis la console ; livrer des managed-settings d'entreprise ; gouverner quel modèle chaque sujet peut utiliser et sur quelle surface ; MCP (serveur de ressources protégé par OAuth, posture, registre, `.mcpb`) ; A2A v1 entre pairs autorisés ; et des surfaces pour les agents que vos équipes utilisent réellement — gemini-cli, Cursor, Codex CLI, opencode, goose, cline, OpenHands, OpenClaw et Hermes (application là où chaque surface l'expose, observation de posture en lecture seule là où elle ne l'expose pas ; chaque connecteur indique lequel) — ainsi que des notifications Teams avec des liens profonds d'approbation.
- **L'alimenter, sous gouvernance.** Le versant contexte de la même médaille : des sources de contenu (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, plus une source de système de fichiers confinée à sa racine pour les montages locaux/NFS/SMB) alimentent un pipeline RAG gouverné avec des réglages par défaut fonctionnels — récupération lexicale zéro-egress prête à l'emploi, récupération sémantique adossée à un modèle lorsque vous provisionnez un fournisseur d'embeddings (Voyage, compatible OpenAI ou auto-hébergé ; `embed_policy=model_backed` échoue en mode fermé au lieu de se dégrader silencieusement), provenance par source, habilitation et scoping appliqués deny-closed au moment de la récupération — plus un catalogue de produits de données avec des contrats versionnés et des portes de qualité. Voir [Governed data for Claude](docs-site/src/content/docs/how-to/governed-data-for-claude.md).
- **Identité et accès.** Identité humaine (WebAuthn/FIDO2, PIV/CAC, élévation AAL) et cycle de vie des **identités non humaines** ; fédération d'identité d'agents (Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE) ; réconciliation du roster depuis AD/LDAP/Okta/Entra/Vault/Infisical avec SCIM.
- **Sécuriser les données.** Guardrails en ligne (PII, prompt-injection, jailbreak), DLP en sortie, chiffrement par enveloppe BYOK/CMEK sur trois backends KMS (AWS KMS, Google Cloud KMS, Azure Key Vault), enregistrement des sessions privilégiées, droit à l'effacement avec destruction vérifiée des clés, rétention et legal-hold, attestation de résidence, et établissement de clé TLS 1.3 hybride post-quantique (X25519MLKEM768 lorsque le pair le prend en charge ; les signatures restent classiques à ce jour).
- **Le prouver.** Un audit ledger hash-chained et signé Ed25519 ; des preuves de conformité scellées et append-only mappées à **26 catalogues de cadres** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) ; push SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF).
- **Bien l'exécuter.** Des budgets FinOps qui peuvent refuser ou limiter la dépense ; des évaluations LLM-judge calibrées avec une porte CI bloquante (à la demande — sans identifiant de juge, les exécutions rapportent `SKIPPED`, jamais une réussite silencieuse) ; des bacs à sable red-team isolés au niveau OS (gVisor/Firecracker ; sans bac à sable provisionné, les exécutions rapportent `DEGRADED`, jamais une réussite fabriquée) ; un tableau de bord de santé des connecteurs avec une page de statut publique ; des sauvegardes et une restauration gérées depuis la console.

Sur **158 intégrations** avec les clouds, les annuaires, les coffres de secrets, les fournisseurs de modèles, les surfaces d'agents, les SIEM et les pipelines que vous exploitez déjà — un décompte dérivé du code et appliqué à chaque push par [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). L'unité est le répertoire de connecteur qui contient du code Go : parmi les 159 répertoires de l'arborescence, 158 répondent à ce critère, et le contrôle dérive ce chiffre ainsi à chaque push. Douze d'entre eux sont des packages partagés de contrats/bibliothèques plutôt que des capacités — ils sont comptés, et [`connectors/README.md`](connectors/README.md) présente le détail complet de ce qu'est chaque répertoire. La carte complète de chaque capacité et de sa maturité se trouve dans [`docs-site/`](docs-site/), et sa propre suite de tests la contrôle.

<a name="whats-open-whats-enterprise-whats-planned"></a>
## Ce qui est ouvert, ce qui est enterprise, ce qui est prévu

Ce tableau associe chaque domaine de capacité à l'endroit où il est livré — la build ouverte (AGPL) ou l'un des add-ons commerciaux séparés et optionnels ; la maturité par capacité est indiquée honnêtement dans [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md). La liste complète des jointures réservées est déclarée dans l'arborescence publique elle-même ([`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go)) : une capacité que le binaire ouvert réserve répond `501` ou est sans effet, et son commentaire le dit — rien n'est caché et rien de ce qui est ouvert n'est retiré.

| Domaine | Ouvert (AGPL) | Add-ons commerciaux | Prévu |
|---|---|---|---|
| Travail et orchestration | éléments de travail durables (brief, dépendances, acceptation, décisions, événements), leases clôturés avec reprise et révocation, lancement orchestré de sessions associées à un élément de travail, avec des entrées et un arrêt clôturés par l'élément de travail dans l'API des sessions, délégation A2A vers des pairs autorisés avec reçus durables, messages/accusés de réception/transferts limités au workflow, vues Travail et Orchestration dans la console | — | double rapport shadow et le basculement d'autorité qui fait de ce plan le système de référence |
| Visibilité | inventaire des agents/sessions/modèles/serveurs MCP/outils/identités, access map read/write avec dérive Permitted-vs-Observed, sessions en direct, graphe d'orchestration, santé/SLA | — | — |
| Politique et application | moteur d'autorisation Cedar (RBAC + deny-overlay + grants scopés), quatre points d'application deny-closed (hook Claude Code, proxy `/v1/messages` en ligne, porte MCP `tools/call`, porte de délégation A2A), approbations à deux personnes, break-glass à double contrôle, kill-switch de parc | durcissement des hooks, contrôle d'egress des outils serveur, porte de gouvernance computer-use, épinglage des définitions d'outils MCP (deny-closed en cas de définition modifiée), disjoncteur automatique avec escalade vers le kill-switch | — |
| Claude et l'écosystème d'agents | Claude Code gouverné dans le hook, lancement/attachement/gouvernance/arrêt des sessions Claude Code depuis la console, livraison de managed-settings d'entreprise, accès aux modèles par sujet/par surface, MCP (serveur de ressources protégé par OAuth, posture, registre, `.mcpb`), A2A v1, surfaces pour gemini-cli/Cursor/Codex CLI/opencode/goose/cline/OpenHands/OpenClaw/Hermes (application là où la surface l'expose, observation de posture là où elle ne l'expose pas), notifications Teams avec liens profonds d'approbation | inspection du contenu rendu des MCP Apps, médiation de l'elicitation/du sampling | — |
| Contexte et connaissance | dix sources de contenu en service (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL) plus une source de système de fichiers confinée à sa racine (montages locaux/NFS/SMB), RAG gouverné (récupération lexicale par défaut, sémantique adossée à un modèle avec un fournisseur d'embeddings provisionné — échoue en mode fermé sous `embed_policy=model_backed`) avec habilitation deny-closed au moment de la récupération, provenance par source, catalogue de produits de données avec contrats versionnés et portes de qualité | — | — |
| Identité et accès | SSO à IdP unique (OIDC + SAML 2.0), WebAuthn/FIDO2, PIV/CAC, élévation AAL, cycle de vie des identités non humaines, fédération d'identité d'agents (Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE), réconciliation du roster (AD/LDAP/Okta/Entra/Vault/Infisical) avec SCIM, récepteur d'événements CAEP | fédération multi-IdP, application du SSO, SCIM managé, rotation NHI CyberArk Conjur, transmetteur CAEP (SET signés vers des récepteurs SSF) | — |
| Sécurité des données | guardrails en ligne (PII, prompt-injection, jailbreak), DLP en sortie, BYOK/CMEK sur trois backends KMS (AWS KMS, Google Cloud KMS, Azure Key Vault), enregistrement des sessions privilégiées, droit à l'effacement avec destruction vérifiée des clés, rétention et legal-hold, attestation de résidence, établissement de clé TLS 1.3 hybride PQC (X25519MLKEM768) | pare-feu de contenu/DLP | — |
| Preuves et conformité | audit ledger hash-chained et signé Ed25519, preuves scellées append-only, 26 catalogues de cadres, archive dir/S3 avec export/vérification (le répertoire n'est WORM que sur un support immuable ; S3 utilise Object Lock), export OSCAL (trois modèles ouverts), vue ouverte du risque TIC DORA, push SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF) | ingestion de profils/SSP OSCAL + constructeur de POA&M, planchers de rétention réglementaires + verrou de mode conformité (SEC 17a-4/FINRA 4511/CFTC 1.31), Register-of-Information DORA + rapports d'incidents majeurs, legal holds WORM de longue durée + bundles de preuves de niveau examinateur, puits WORM Azure/GCS, pack AIMS ISO 42001, packs de profondeur de conformité + de classification NIS2, reporting enterprise | — |
| Opérations | budgets FinOps qui refusent ou limitent la dépense, évaluations LLM-judge calibrées avec porte CI bloquante (à la demande : identifiant de juge requis, sinon `SKIPPED`), bacs à sable red-team isolés au niveau OS (gVisor/Firecracker ; les exécutions non provisionnées rapportent `DEGRADED`), tableau de bord de santé des connecteurs avec page de statut publique, sauvegardes et restauration gérées depuis la console, requêtes ouvertes de chemins d'attaque | catalogue de threat-intel compilé, boucle de clôture d'incident | — |
| Plateforme et déploiement | binaire statique unique avec console embarquée, SQLite ou Postgres avec sécurité au niveau ligne, Docker/Kubernetes/Helm/air-gapped, provider Terraform, SDK clients générés (Go, Java, Python, TypeScript), bus in-process ouvert + pont Core-NATS | bus JetStream durable (livraison au moins une fois + déduplication) | paquets Windows (aujourd'hui : conteneur Linux ou compilation depuis les sources), fine-tuning de modèles post-v1, sonde de télémétrie vocale (jointure deny-closed déclarée aujourd'hui) |

La build AGPL est toute la plateforme et n'est jamais plafonnée en fonctionnalités depuis l'intérieur. Les add-ons commerciaux sont du nouveau code additif, jamais des fonctionnalités retirées du produit ouvert. Un abonnement est le droit d'accès qui permet de télécharger des artefacts signés — le modèle SUSE — et non une clé qui déverrouille du code déjà présent sur votre disque. Les comptes utilisateurs sont illimités dans le moteur auto-hébergé : aucune de ses éditions n'applique de plafond de sièges, et le point d'extension des sièges du binaire est un no-op inconditionnel. L'offre Cloud hébergée est la seule exception : son plan de contrôle admet des sièges par tenant, ce qui est une propriété de ce service et non de ce binaire. Voir [`LICENSING.md`](LICENSING.md) et [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840"
       alt="Ce que contient chaque édition : le cœur AGPL est la plateforme entière et les modules commerciaux sont du code additif par-dessus. Community est le produit AGPL complet avec des utilisateurs illimités. Business ajoute de la profondeur commerciale sur les rapports, l'intégration, le renseignement sur les menaces, la posture PQC et NIS2. Regulated Operations ajoute un gouverneur de rétention, une archive d'audit WORM, la conservation légale et la profondeur d'effacement. Business Max, c'est Business avec les quatre modules. Cloud Standard est le service géré, avec des quotas de forfait qui incluent des sièges de service. Un abonnement est l'identifiant avec lequel vous téléchargez des artefacts signés.">
</picture>

<sub>Éditions par composition. Conditionnement et tarifs sur demande.</sub>
</div>

## Un aperçu de la console

<div align="center">

<img src=".github/assets/olivares-reel.gif" width="720" alt="Une courte séquence qui fait défiler des vues réelles de la console Olivares AI : access map, sessions, politiques, FinOps et conformité.">

<sub>Quelques secondes de la console réelle. Chaque image fixe ci-dessous est une capture du parc (estate) de démonstration seedé servie par le binaire en cours d'exécution — régénérez vous-même les captures brutes avec <code>bash scripts/docs-captures.sh</code> (la sélection présentée ici est tirée de sa sortie).</sub>

</div>

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Dérive de moindre privilège: Superposer la différence de moindre privilège : mettre en évidence les accès inattendus (observés, non permis) et les octrois inutilisés."></picture><br><sub><b>Dérive de moindre privilège</b> — Superposer la différence de moindre privilège : mettre en évidence les accès inattendus (observés, non permis) et les octrois inutilisés.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="Orchestration et A2A: Topologie agent-à-agent — qui délègue à qui, les flux de délégation en direct et les cadences déclarées. Les lectures du graphe de communication sont privilégiées et auto-auditées."></picture><br><sub><b>Orchestration et A2A</b> — Topologie agent-à-agent — qui délègue à qui, les flux de délégation en direct et les cadences déclarées. Les lectures du graphe de communication sont privilégiées et auto-auditées.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="Inventaire: Chaque agent, session, MCP, modèle et identité découvert dans votre parc."></picture><br><sub><b>Inventaire</b> — Chaque agent, session, MCP, modèle et identité découvert dans votre parc.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/observability-dark.png"><img src="docs-site/public/console/observability-light.png" alt="Observabilité et interopérabilité: Santé d’ingestion fondée sur les standards et exploration des traces corrélées au registre. Les chiffres concernent l’ensemble du moteur (globaux au processus), et non chaque locataire ; les standards sont épinglés aux versions et niveaux de maturité déclarés par les organismes en amont."></picture><br><sub><b>Observabilité et interopérabilité</b> — Santé d’ingestion fondée sur les standards et exploration des traces corrélées au registre. Les chiffres concernent l’ensemble du moteur (globaux au processus), et non chaque locataire ; les standards sont épinglés aux versions et niveaux de maturité déclarés par les organismes en amont.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/dashboards-dark.png"><img src="docs-site/public/console/dashboards-light.png" alt="Vue d’ensemble pour la direction: Coût, utilisation, risque et conformité en un coup d’œil — accédez à la vue opérationnelle pour le détail."></picture><br><sub><b>Vue d’ensemble pour la direction</b> — Coût, utilisation, risque et conformité en un coup d’œil — accédez à la vue opérationnelle pour le détail.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/home-dark.png"><img src="docs-site/public/console/home-light.png" alt="Vue d’ensemble: Votre parc d’IA en un coup d’œil — inventaire, activité, risque, conformité, dépense et état."></picture><br><sub><b>Vue d’ensemble</b> — Votre parc d’IA en un coup d’œil — inventaire, activité, risque, conformité, dépense et état.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Sécurité et investigation: Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée."></picture><br><sub><b>Sécurité et investigation</b> — Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="Visionneuse d&#x27;enregistrement de session: Chronologie unifiée de l&#x27;activité de l&#x27;agent et des preuves de gouvernance pour une session unique."></picture><br><sub><b>Visionneuse d&#x27;enregistrement de session</b> — Chronologie unifiée de l&#x27;activité de l&#x27;agent et des preuves de gouvernance pour une session unique.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/identity-dark.png"><img src="docs-site/public/console/identity-light.png" alt="Identité &amp; NHI: SSO, SCIM, inventaire des identités, cycle de vie NHI, graphe WIF et connexion privilégiée — observés, gouvernés et audités."></picture><br><sub><b>Identité &amp; NHI</b> — SSO, SCIM, inventaire des identités, cycle de vie NHI, graphe WIF et connexion privilégiée — observés, gouvernés et audités.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/knowledge-dark.png"><img src="docs-site/public/console/knowledge-light.png" alt="Données, connaissances et contexte: Bases de connaissances gouvernées, traçabilité de la récupération, registre des invites, mémoire des agents et politiques de contexte."></picture><br><sub><b>Données, connaissances et contexte</b> — Bases de connaissances gouvernées, traçabilité de la récupération, registre des invites, mémoire des agents et politiques de contexte.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-apply-refused-dark.png"><img src="docs-site/public/console/work-apply-refused-light.png" alt="Plan: Planification de la modification. Rien n&#x27;est écrit à cette étape."></picture><br><sub><b>Plan</b> — Planification de la modification. Rien n&#x27;est écrit à cette étape.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="Coupe-circuit: L’arrêt d’urgence du parc : un clic stoppe toutes les surfaces d’actuation gouvernées. L’activation est volontairement peu coûteuse ; la reprise exige deux comptes utilisateur distincts et un post-examen imposé."></picture><br><sub><b>Coupe-circuit</b> — L’arrêt d’urgence du parc : un clic stoppe toutes les surfaces d’actuation gouvernées. L’activation est volontairement peu coûteuse ; la reprise exige deux comptes utilisateur distincts et un post-examen imposé.</sub> |

<a name="install"></a>
## Installation

Chaque version est livrée sous une **chaîne de confiance signée par cosign** — un manifeste de checksums signé par cosign couvrant chaque artefact, qui couvre transitivement les archives et les binaires statiques, une attestation SBOM in-toto par archive, des signatures cosign directes sur l'image de conteneur — avec une attestation SBOM pour l'image de conteneur — et sur le chart Helm, ainsi qu'une provenance de build SLSA pour l'ensemble. Pour un produit de sécurité, la chaîne d'approvisionnement fait partie du modèle de confiance ; [verify it](docs/RELEASE-VERIFICATION.md) avant de l'exécuter. La matrice complète par OS et la configuration de production se trouvent dans [`INSTALL.md`](INSTALL.md) ; les tutoriels de déploiement (Compose, Kubernetes/Helm, air-gapped) se trouvent dans [`docs-site/`](docs-site/).

Le moteur est **sécurisé par défaut** : il se lie à la loopback, sert du HTTPS avec un certificat auto-signé au premier démarrage, est livré sans identifiants par défaut et imprime un jeton de configuration à usage unique dans la console. La première commande que vous exécutez est la commande sécurisée.

**Depuis les sources** (le chemin pris en charge jusqu'à la première version taguée) :

```sh
# Build the single binary (Go 1.26+, Task, pnpm — the web console is embedded).
task build

# Start it — one guided, secure-by-default command (TLS on, loopback-only, no
# default credentials). It prints your console URL and a one-time setup token.
./bin/olivares quickstart
```

**Avec la première version**, le chemin recommandé devient une installation vérifiée unique — des paquets `.deb`/`.rpm`/`.apk` avec une unité systemd durcie, une image Docker multi-arch, un cask Homebrew et un chart Helm, chacun couvert par le manifeste de checksums de la version signé par cosign (images signées directement), chacun installable en une étape et toujours sécurisé par défaut. Ils ne sont pas encore publiés ; jusqu'à ce que le tag soit posé, compilez depuis les sources comme ci-dessus. **Windows** n'est pas encore compilé — exécutez le conteneur Linux ou compilez depuis les sources ([plan in `INSTALL.md`](INSTALL.md#windows)).

> Vous voulez d'abord explorer, sans câbler de sources réelles ? Un parc (estate) synthétique tourne sur la loopback avec une seule commande — voir [Démarrage rapide](#quickstart) ci-dessous.

<a name="quickstart"></a>
## Démarrage rapide

Deux façons d'entrer : explorer immédiatement un parc (estate) synthétique, ou pointer le moteur vers une source réelle. Les deux exécutent le même binaire réel.

### L'évaluer en cinq minutes

1. Compilez avec `task build` (Go 1.26+, Task, pnpm ; voir les [prérequis](#quickstart-prerequisites)).
2. Démarrez le parc (estate) de démonstration avec la commande exacte de l'étape 2a ci-dessous.
3. Dans la console, inspectez l'access map et sa dérive Permitted-vs-Observed (20 nœuds / 13 arêtes, avec 8 accès inattendus et 2 grants inutilisés), une politique Cedar et un flux d'approbation, la vue des preuves de conformité (26 catalogues de cadres), et un budget FinOps.
4. Lisez ensuite ce qui est réel et ce qui est prévu : la matrice de capacités ci-dessus, [Le plan de travail](#the-work-plane) et [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md).

<a name="quickstart-prerequisites"></a>
Prérequis pour la compilation depuis les sources : Go 1.26+, [Task](https://taskfile.dev) (go-task) et pnpm (l'interface web est embarquée). Voir [`CONTRIBUTING.md`](CONTRIBUTING.md) pour la configuration de développement complète.

**1. Compiler :**

```sh
task build && ./bin/olivares version
```

**2a. Explorer le parc (estate) de démonstration** — des observations synthétiques à travers le moteur réel, loopback uniquement (il refuse les adresses non-loopback), aucune donnée réelle :

```sh
./bin/olivares serve --seed-demo --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$(mktemp -d)"
```

Ouvrez `http://127.0.0.1:8901`, connectez-vous avec les identifiants de démonstration figurant dans la bannière de démarrage, et parcourez la console — inventaire, access map et sa dérive, sessions, orchestration, politiques, FinOps, conformité. Le seed de démonstration sert uniquement à l'apprentissage (mot de passe public dans l'arborescence des sources) ; ne le pointez jamais vers des données réelles.

**2b. Ou lancez-le pour de vrai** — une commande unique, guidée et sécurisée par défaut :

```sh
./bin/olivares quickstart        # TLS on, loopback; prints the console URL + a one-time setup token
```

Ouvrez la console à l'URL affichée et créez votre premier administrateur avec le jeton — pas de curl, pas d'étapes supplémentaires. (`olivares serve` est le même moteur avec des flags explicites, pour la production et les conteneurs.) Connectez ensuite une source. Le [full quickstart](docs-site/src/content/docs/start/quickstart.md) câble un **vrai connecteur pgAudit** sur un journal d'audit PostgreSQL — sans seed de démonstration — et renvoie vers les chemins d'installation de production (systemd, Docker Compose, Kubernetes via [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml), air-gapped).

Le parc (estate) de démonstration est déterministe. Les chiffres ne sont pas des objectifs — `task smoke:quickstart` parcourt ce même chemin contre le binaire réel (avec ses propres ports et répertoire de données) et vérifie les compteurs d'access map et de dérive indiqués ci-dessus, afin que cette section ne puisse pas dériver silencieusement par rapport au code.

## Architecture

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840"
       alt="Architecture : les surfaces d'agent, les sources d'audit, les pairs MCP et A2A et les sources de contenu sont collectés de trois manières vers un unique binaire Go auto-hébergé avec la console intégrée, qui porte les modules du produit, la couche de politique et d'application et le journal de preuves signé au-dessus d'un stockage cloisonné par locataire ; il sert la console, l'API REST, un sous-ensemble gRPC ciblé, la CLI et le fournisseur Terraform, le plan de contrôle cloud (construit, non déployé) et le portail de licences (déployé, livraison désactivée) formant des plans distincts.">
</picture>
</div>

Le moteur est un binaire Go statique unique (`olivares`) qui embarque l'interface web et expose ses capacités sur quatre surfaces, chacune avec une couverture documentée : une API REST (la surface principale), un miroir gRPC ciblé et figé du cœur stable, la CLI `olivares` elle-même — 68 commandes de premier niveau regroupées, de `quickstart` et `serve` à `work`, `orchestration`, `agent`, `mcp` et `compliance`, avec un test qui maintient le total des groupes d'aide afin qu'aucune nouvelle commande n'arrive sans groupe — et un provider Terraform pour les ressources gérées comme du code. Les collecteurs s'exécutent dans l'infrastructure du client selon trois modes : sources fast-path in-process, plugins hors processus supervisés par le moteur sur un canal authentifié propre à chaque lancement (AutoMTLS), et déploiement distant collector→core opt-in sur TLS mutuel avec certificat client vérifié. Le core stocke les données dans SQLite (mononœud, air-gap) ou Postgres avec sécurité au niveau ligne, où chaque opération de module est épinglée à un tenant dans l'API du store et où Postgres l'applique à nouveau avec FORCE row-level security. Le rôle de l'application est refusé au démarrage s'il est assez privilégié pour la contourner silencieusement (superuser ou `BYPASSRLS`), et le seul moyen de lever ce refus est un flag opt-in explicite qui en indique le coût. Les lectures système inter-tenants passent par un pool d'administration `BYPASSRLS` distinct, aux privilèges minimaux, qui n'est jamais utilisé pour le travail circonscrit à un tenant — une porte déclarée, pas une porte absente.

Vue d'ensemble : [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Open core, par répertoire

Les licences sont réglées dès le premier commit : **open core** — le produit complet sous AGPL, un SDK et des connecteurs permissifs pour que l'écosystème puisse croître sans friction copyleft, et un petit ensemble d'add-ons commerciaux **additifs** — compilés uniquement avec `-tags enterprise`, chacun licencié séparément sous conditions commerciales et absents du binaire public — pour les capacités réservées. La build AGPL constitue l'intégralité de la plateforme de gouvernance et n'est jamais bridée pour pousser à la montée en gamme ; les add-ons commerciaux *ajoutent* du nouveau code qui n'a jamais fait partie du produit ouvert — de sorte qu'une build enterprise n'est pas identique à la build ouverte, sans pour autant rien retirer à ce qui est livré en open. Chaque fichier source porte un en-tête `SPDX-License-Identifier`, appliqué en CI.

| Répertoire | Licence | Contenu |
|---|---|---|
| `core/` | `AGPL-3.0-only` | Moteur : ingestion, bus d'événements, modèle de données, runtime de modules, API, authn/z, audit, multi-tenancy |
| `modules/` | `AGPL-3.0-only` | Les 30 modules produit (inventaire, access map, travail et leases, identité, FinOps, évaluations, guardrails, …) |
| `web/` | `AGPL-3.0-only` | Interface React, embarquée dans le binaire via `go:embed` |
| `sdk/` | `Apache-2.0` | Interfaces stables `SourceConnector` / `OutputConnector` / `Module` + contrat gRPC + types |
| `connectors/` | `Apache-2.0` | Connecteurs first-party et communautaires (Claude, MCP, pg-audit, eBPF, cloud, SIEM, …) |
| `clients/` | `Apache-2.0` | SDK clients générés (Go, Java, Python, TypeScript) |
| Add-ons commerciaux *(dépôt privé séparé)* | `LicenseRef-Olivares-Commercial` | Familles d'add-ons additives et licenciées séparément couvrant l'application, MCP, l'identité, la sécurité des données, la profondeur de conformité, les opérations et la plateforme — énumérées par domaine dans [la matrice ci-dessus](#whats-open-whats-enterprise-whats-planned), chacune étant une jointure déclarée dans [`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go) — compilées uniquement avec `-tags enterprise`, jamais dans ce dépôt ni dans le binaire public |
| `docs/`, `docs-site/` | — | Documents de conception et site de documentation produit |

Un connecteur ne peut importer que depuis `sdk/`, jamais depuis `core/`. Cela maintient la frontière AGPL / Apache propre et permet à des tiers d'écrire des connecteurs sans obligations copyleft — appliqué par [`scripts/check-boundary.sh`](scripts/check-boundary.sh) en CI.

## Sécurité et chaîne d'approvisionnement

Olivares AI s'exécute sur les hôtes du client et cartographie ce que chaque agent peut toucher ; le niveau de sécurité est donc élevé par conception : lecture d'abord ; données minimales dans le plan d'observation (l'access map stocke des arêtes, pas des charges utiles — le store Knowledge gouverné ne contient que le contenu que vous ingérez explicitement) ; moindre privilège ; mTLS ; audit append-only hash-chained avec checkpoints signés ; versions signées. L'access map elle-même est une surface privilégiée et auditée — l'ouvrir est une action enregistrée, tout comme lire le graphe de communication agent-à-agent.

Pour signaler une vulnérabilité ou lire la politique de divulgation, voir [`SECURITY.md`](SECURITY.md) (signalement privé — jamais une issue publique). Le flux d'avis est documenté dans [`docs/security-advisories.md`](docs/security-advisories.md) ; les preuves de préparation de la chaîne d'approvisionnement figurent dans la carte Best Practices de [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Documentation

La documentation produit se trouve dans [`docs-site/`](docs-site/) — un site Diátaxis avec des tutoriels d'installation testés (mononœud, Docker Compose, Kubernetes/Helm, air-gapped), des guides par connecteur avec de vraies captures de console, un cookbook (politiques deny-closed, budgets, approbations, exercices de kill-switch, push SIEM), une référence API et un glossaire. Commencez par [What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) et [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md) — la page qui dit clairement ce qui fonctionne aujourd'hui, ce qui est au stade de conception et ce que le produit ne fait délibérément pas.

## Communauté et gouvernance

Les fichiers de santé communautaire et de gouvernance attendus par un adopteur sont présents et à jour :

- **Comment les décisions sont prises :** [`GOVERNANCE.md`](GOVERNANCE.md) (dirigé par les mainteneurs / open-core, honnête sur le stade du projet) et [`.github/CODEOWNERS`](.github/CODEOWNERS) (routage des revues mappé sur la frontière des licences).
- **Contribuer :** [`CONTRIBUTING.md`](CONTRIBUTING.md) (configuration, DCO/CLA, SPDX, frontière des connecteurs) — chaque changement est soumis via le [pull-request template](.github/PULL_REQUEST_TEMPLATE.md).
- **Conduite :** [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1).
- **Obtenir de l'aide :** [`SUPPORT.md`](SUPPORT.md) — et où **ne pas** signaler les problèmes de sécurité.
- **Changements :** [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog 1.1 + CalVer `vYY.M.PATCH` ; beta).

## Licence

Le produit (`core/`, `modules/`, `web/`) est sous licence **GNU Affero General Public License, version 3** (`AGPL-3.0-only`). Le SDK de connecteurs, les connecteurs et les SDK clients (`sdk/`, `connectors/`, `clients/`) sont sous licence **Apache-2.0**. La licence qui régit un fichier donné est indiquée dans son en-tête SPDX et, pour une version, dans son SBOM.

> **Aucune garantie, aucune responsabilité — à lire avant tout déploiement.** Le logiciel libre est fourni **en l'état**, **sans garantie d'aucune sorte** et **sans responsabilité en cas de perte de données, de corruption, d'interruption d'activité ou de perte de profits**. Ce n'est pas une formalité sur un plan de contrôle : une mauvaise configuration peut bloquer un travail légitime et interrompre la production, ou laisser passer exactement ce que vous vouliez arrêter. L'AGPL-3.0-only §§15–16 et l'Apache-2.0 §§7–8 s'appliquent, ainsi que le terme supplémentaire propre à ce projet au titre de l'AGPL §7(a) — le texte complet, y compris les usages à haut risque, les résultats de conformité et les composants tiers, se trouve dans [`DISCLAIMER.md`](DISCLAIMER.md).

Une **licence commerciale** fournit une exception privée à l'AGPL pour les organisations qui ne peuvent pas opérer sous ses termes. Les capacités additives `enterprise/` — les familles d'add-ons énumérées par domaine dans [la matrice ci-dessus](#whats-open-whats-enterprise-whats-planned), chacune étant une jointure déclarée dans l'arborescence publique — sont proposées comme **add-ons séparés et optionnels** sous leurs propres conditions commerciales : code fermé compilé uniquement avec `-tags enterprise`, jamais présent dans le binaire ouvert. Conditionnement et tarifs sur demande. Le cœur AGPL est complet et n'est jamais plafonné en fonctionnalités depuis l'intérieur. Pour les licences commerciales ou les demandes d'entreprise, contactez `enterprise@olivares.ai`. Voir [`LICENSING.md`](LICENSING.md).

Les contributions nécessitent une signature DCO (`git commit -s`) et un Contributor License Agreement ; voir [`CONTRIBUTING.md`](CONTRIBUTING.md) et [`CLA.md`](CLA.md).

## Soutenir le projet

Olivares AI est sous AGPL-3.0 et auto-hébergé : le cœur est libre et le restera. S'il vous est utile et que vous souhaitez soutenir ce travail directement, vous pouvez le parrainer via le bouton **Sponsor** de ce dépôt.

Le parrainage n'est **pas** un contrat de support et n'achète aucune priorité : pour le traitement des questions et des rapports de bug, voir [`SUPPORT.md`](SUPPORT.md) ; pour les conditions commerciales et les add-ons enterprise, [`LICENSING.md`](LICENSING.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>La vérité de terrain pour l'IA d'entreprise.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
