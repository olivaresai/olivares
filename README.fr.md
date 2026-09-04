<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — La ground truth de l'IA d'entreprise" width="720"></a>

**Langues :** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · **Français**

**Intégrez, gérez et sécurisez l'IA que vous exécutez réellement — depuis un seul binaire auto-hébergé.**

[Installation](#install) · [Démarrage rapide](#quickstart) · [Exemples](examples/) · [Documentation](#documentation) · [Sécurité](#security) · [Contribuer](CONTRIBUTING.md) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Bêta**, en développement actif. La première version taguée, **v26.8.0**, est livrée avec des archives signées, des paquets natifs et des images de conteneur. Les API et la surface des modules peuvent encore évoluer avant la 1.0 ; ce qui fonctionne aujourd'hui, ce qui est à la demande et ce qui est au stade de la conception sont indiqués dans [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md) et, pour chaque module, dans le [catalogue des modules](docs-site/src/content/docs/reference/modules/overview.md).

## Ce qu'est Olivares AI

Ce que vous exécutez aujourd'hui forme une estate — agents de codage, serveurs MCP, endpoints de modèles, comptes de service, tâches planifiées — répartie sur des machines qui n'ont jamais constitué un seul système. Olivares AI est le binaire Go unique auto-hébergé, console incluse, qui fédère l'ensemble : il fournit à l'IA ce dont elle a besoin pour travailler (contexte, accès aux ressources, sessions gérées) et vous fournit les permissions, les politiques, les budgets et les preuves nécessaires pour savoir ce qui s'exécute, qui l'a lancé, ce qu'il a atteint, ce qu'il a coûté et qui a donné son accord.

**Multi-fournisseur par conception.** Claude Code est intégré au niveau le plus profond — le hook `PreToolUse`/`PostToolUse`, les managed settings, le lancement et l'arrêt depuis la console, l'accès aux modèles par sujet — avec Codex et Grok Build à ses côtés comme surfaces de commande de première classe, et gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw et Hermes comme connecteurs à part entière, chacun indiquant ce qu'il peut appliquer et ce qu'il peut seulement observer. Ollama et les autres endpoints auto-hébergés sont inventoriés par le connecteur local, qui est en lecture seule par conception.

**Qui l'exécute.** La même build à toutes les échelles : un serveur à domicile (un binaire, SQLite, lié à la loopback) ; un freelance avec un tenant par client et des budgets qui refusent avant la facture ; une équipe d'ingénierie avec des éléments de travail partagés, le SSO et une piste d'audit que personne n'assemble à la main ; une entreprise réglementée avec la sécurité au niveau ligne de Postgres, la HA, des installations air-gapped et un archivage WORM. La build ouverte constitue l'intégralité de la plateforme, et les add-ons commerciaux sont du code additif par-dessus, jamais des fonctionnalités retirées ; le SSO, la HA, le WORM et les budgets qui refusent réellement sont des éléments que vous provisionnez, pas des valeurs par défaut au premier démarrage.

Il n'y a pas de télémétrie obligatoire et, par défaut, aucune sortie du plan de contrôle : ne franchit votre périmètre que ce que vous configurez à cette fin — les appels à vos API de modèles, les sorties SIEM/webhook que vous raccordez, un fournisseur d'embeddings si vous en provisionnez un. Les collecteurs lisent les systèmes que vous exploitez déjà, de sorte qu'un collecteur défaillant ne se trouve jamais sur le chemin des données de production.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840" alt="Un binaire à toutes les échelles, d'un serveur à domicile à une entreprise réglementée ; où il s'exécute et ce qu'il atteint.">
</picture>
<sub>Le même build ouvert d'un homelab à une entreprise réglementée.</sub>
</div>

## Ce qu'il fait

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840" alt="Access map : ce que chaque agent lit et écrit dans votre estate, les origines à gauche, les ressources à droite.">
</picture>
<sub><b>Access map</b> — ce que chaque agent lit et écrit dans votre estate, lectures et écritures distinguées par couleur.</sub>
</div>

- **Le voir.** Inventaire de tous les agents, sessions, modèles, serveurs MCP, outils et identités découverts ; une **access map** en lecture/écriture de ce que chacun atteint réellement, avec une vue du **drift** Permitted vs Observed ; sessions en direct, graphe d'orchestration, santé et SLA. Ce qu'il ne peut pas voir est marqué `unknown`, jamais deviné.
- **Exécuter le travail.** Éléments de travail durables avec responsable, dépendances, critères d'acceptation et décisions ; leases clôturés, afin que deux agents — ou deux personnes — ne puissent pas détenir le même élément de travail simultanément ; sessions lancées, attachées et arrêtées depuis la console ; délégation vers des pairs autorisés via A2A. Le mode shadow et l'autorité finale ne sont pas construits et sont indiqués comme absents : [Le plan de travail](docs-site/src/content/docs/explanation/work-plane.md).
- **Le gouverner et l'appliquer.** Un moteur d'autorisation Cedar et **quatre points d'application fermés par défaut (deny-closed)** — le hook Claude Code, un proxy d'inférence `/v1/messages` en ligne, un gate MCP `tools/call` et un gate de délégation A2A — afin qu'une action non autorisée soit bloquée, mise en attente pour une approbation par deux personnes ou, dans le hook, réécrite avant son exécution ; un point n'est comptabilisé que tant qu'un test exerce son chemin non configuré et vérifie le refus. Des budgets qui refusent ou limitent les dépenses, un break-glass à double contrôle et un **kill switch** de l'estate qui échoue en mode fermé.
- **L'alimenter, sous gouvernance.** Les sources de contenu (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, un système de fichiers confiné à sa racine) alimentent une récupération gouvernée : récupération lexicale zéro-egress prête à l'emploi, récupération sémantique adossée à un modèle lorsque vous provisionnez un fournisseur d'embeddings, habilitation appliquée deny-closed au moment de la récupération.
- **Le prouver.** Un audit ledger hash-chained et signé Ed25519 ; des preuves scellées mappées à **26 catalogues de cadres** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — des familles de contrôles autoévaluées, et non des certifications ; push SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF). Configurés pour chaque déploiement : les identités humaines et non humaines (WebAuthn/FIDO2, PIV/CAC, SSO à IdP unique, réconciliation SCIM, fédération d'identité d'agents), les guardrails en ligne, la DLP, le chiffrement BYOK/CMEK et le droit à l'effacement avec destruction vérifiée des clés.

**30 modules**, une console, **158 intégrations** — des décomptes dérivés du code et appliqués à chaque push par [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). Une intégration est un répertoire de connecteur contenant du code Go, et douze d'entre elles sont des packages de bibliothèque partagés : [`connectors/README.md`](connectors/README.md) en donne le détail. Chaque module avec son niveau de maturité : le [catalogue des modules](docs-site/src/content/docs/reference/modules/overview.md) ; les connecteurs raccordés par niveau de fidélité : la [référence des connecteurs](docs-site/src/content/docs/reference/connectors.md).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840" alt="Comment les agents travaillent ensemble : un plan de travail durable unique composé d'éléments de travail, de leases clôturés et de messages à portée limitée ; la délégation passe par un gate d'application ; le mode shadow et l'autorité finale sont en pointillés car ils ne sont pas construits.">
</picture>
<sub>Les agents partagent un plan de travail durable. Ce qui n'est pas construit est dessiné comme absent.</sub>
</div>

## Un aperçu de la console

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Sessions Claude Code créées, rejointes et gouvernées depuis la console."></picture><br><sub><b>Claude Code</b> — créez, attachez-vous à et gouvernez les sessions depuis la console, sans SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Travail : le backlog durable d'éléments de travail et de décisions entre les sessions."></picture><br><sub><b>Travail</b> — le backlog durable entre sessions : éléments, responsables, acceptation, décisions.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="Orchestration et A2A : le graphe de délégation agent-à-agent dérivé des signaux observés."></picture><br><sub><b>Orchestration et A2A</b> — qui délègue à qui, d'après les signaux observés.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="Inventaire : tous les agents, sessions, serveurs MCP, modèles et identités découverts dans l'estate."></picture><br><sub><b>Inventaire</b> — tous les agents, sessions, serveurs MCP, modèles et identités découverts.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Drift de moindre privilège : accès inattendus et grants inutilisés superposés à l'access map."></picture><br><sub><b>Drift de moindre privilège</b> — accès observés mais non permis, et grants que personne n'utilise.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Sécurité et investigation : findings des guardrails, file des anomalies et investigations dont toute altération est détectable."></picture><br><sub><b>Sécurité et investigation</b> — findings des guardrails, anomalies, investigations dont toute altération est détectable.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="Kill switch : l'arrêt d'urgence de l'estate avec reprise à double contrôle."></picture><br><sub><b>Kill switch</b> — un clic arrête toute surface d'actuation gouvernée ; la reprise exige deux comptes.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="Visionneuse d'enregistrement de session : activité de l'agent et preuves de gouvernance sur une même chronologie, chaîne vérifiée."></picture><br><sub><b>Enregistrement de session</b> — activité de l'agent et preuves de gouvernance sur une même chronologie, chaîne vérifiée.</sub> |

Chaque image fixe est une capture de l'estate de démo seedée servie par le binaire en cours d'exécution (`bash scripts/docs-captures.sh` régénère l'ensemble brut). La carte complète des écrans : la [référence de la console](docs-site/src/content/docs/reference/console.md).

<a name="install"></a>
## Installation

Chaque version est livrée avec une chaîne de confiance signée par cosign, vérifiée selon le type d'artefact : un manifeste de sommes de contrôle signé par cosign couvrant les archives, les paquets et les SBOM propres à chaque archive qu'il répertorie, un fichier annexe SBOM SPDX avec une attestation in-toto pour chaque archive, des signatures cosign sur l'image de conteneur avec sa propre attestation SBOM, ainsi que des déclarations OpenVEX et une provenance de build SLSA pour l'ensemble. Pour un produit de sécurité, la chaîne d'approvisionnement fait partie du modèle de confiance : [vérifiez-la](docs/RELEASE-VERIFICATION.md) avant de l'exécuter.

**Chemin pratique via HTTPS.** Le corps du script arrive par HTTPS et n'est pas pré-vérifié par le pipe ; une fois lancé, il détecte votre OS et votre architecture, exige `cosign`, vérifie le manifeste de sommes de contrôle signé et le SHA-256 de l'archive, n'installe que le binaire et n'invoque jamais `sudo`. Épinglez la version lorsque vous le transmettez à un shell :

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh -s -- --version v26.8.0
olivares quickstart        # TLS on, loopback-only, no default credentials; prints the console URL + a one-time setup token
```

**Chemin à haute assurance.** Téléchargez d'abord, vérifiez, puis exécutez : les archives, les paquets et le manifeste de sommes de contrôle se trouvent sur la [page de la version](https://github.com/olivaresai/olivares/releases/tag/v26.8.0), et [`scripts/verify-release.sh`](scripts/verify-release.sh) vérifie tout ce qui est présent et indique ce qu'il n'a pas vérifié — sans clé par défaut, avec `--key … --offline` sur un hôte déconnecté. Le [contrat de confiance de l'installeur](docs/RELEASE-INSTALLER.md) décrit les deux chemins ; l'installeur signé et versionné, avec son adaptateur de service activé sur demande, n'est fourni qu'à partir de la première version produite après son intégration, et v26.8.0 est antérieure à son intégration.

| Chemin | Ce que vous obtenez |
|---|---|
| **Paquets Linux** — `.deb`, `.rpm`, `.apk` | le binaire, une unité systemd durcie, un fichier env d'exemple et un utilisateur de service `olivares` sans login ; le service n'est pas démarré à votre place |
| **Conteneur** — `docker.io/olivaresai/olivares:26.8.0` | distroless, non-root, tags sans préfixe `v` ; `ghcr.io/olivaresai/olivares` est la même image par digest. L'image par défaut est multi-arch (amd64/arm64) ; les variantes `-fips` et `-stig` sont amd64 uniquement |
| **Homebrew** — `brew install olivaresai/tap/olivares` | le binaire de la version sur macOS et Linux, vérifié à l'aide des sommes de contrôle signées, avec la quarantaine Gatekeeper levée ; les builds darwin ne sont pas encore notarisés par Apple |
| **Kubernetes** — [`deploy/helm/olivares`](deploy/helm/olivares) ou [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) | les sources du chart Helm et un manifeste plat, sans Helm, dans l'arborescence ; le chart n'est **pas encore publié dans un registre OCI** |
| **Depuis les sources** — `task build` (Go 1.26+, [Task](https://taskfile.dev), pnpm) | `./bin/olivares quickstart`, le même premier démarrage sécurisé par défaut |

Le moteur est **sécurisé par défaut** : il se lie à la loopback, sert HTTPS avec un certificat auto-signé au premier démarrage, est livré sans identifiants par défaut et affiche un setup token à usage unique ; dans un conteneur ou un pod, le processus écoute sur son propre réseau et le mapping de l'hôte ou le Service le maintient privé. **Windows** n'est pas encore compilé — exécutez le conteneur Linux ou WSL2 ([plan](INSTALL.md#windows)). La matrice par OS et la configuration de production : [`INSTALL.md`](INSTALL.md) ; les guides de déploiement (Compose, Kubernetes, air-gapped) et les [mises à niveau](docs-site/src/content/docs/how-to/upgrade-and-rollback.md) : [`docs-site/`](docs-site/).

<a name="quickstart"></a>
## Démarrage rapide

Explorez une estate synthétique, ou lancez Olivares AI pour de vrai. Les deux exécutent le même binaire.

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

Le seed de démonstration sert uniquement à l'apprentissage (mot de passe public dans l'arborescence des sources) : ne le pointez jamais vers des données réelles. La CI parcourt le même chemin avec `task smoke:quickstart` et vérifie les compteurs de l'access map et du drift (20 nœuds / 13 arêtes, avec 8 accès inattendus et 2 grants inutilisés), afin que cette page ne puisse pas dériver silencieusement par rapport au code. Le [démarrage rapide complet](docs-site/src/content/docs/start/quickstart.md) raccorde un vrai connecteur pgAudit et renvoie vers les chemins d'installation de production.

## Éditions

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840" alt="Éditions par composition : le cœur AGPL constitue toute la plateforme, les add-ons sont du code additif par-dessus, Cloud Standard est le service géré.">
</picture>
<sub>Éditions par composition. Conditionnement et tarifs sur demande.</sub>
</div>

La build AGPL constitue l'intégralité de la plateforme et n'est jamais plafonnée en fonctionnalités depuis l'intérieur ; les add-ons commerciaux sont du code additif, jamais des fonctionnalités retirées du produit ouvert. Un abonnement est l'identifiant d'accès permettant de télécharger des packs de modules signés — un modèle de distribution, et non une clé qui déverrouille du code déjà présent sur votre disque. Les comptes utilisateurs sont illimités dans le moteur auto-hébergé, et les **quatre points d'application deny-closed** sont ouverts. La matrice domaine par domaine des capacités ouvertes, commerciales et prévues : [`LICENSING.md`](LICENSING.md) et [Open core et licences](docs-site/src/content/docs/explanation/open-core-and-licensing.md).

## Architecture

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840" alt="Architecture : les surfaces d'agents, les sources d'audit, les pairs MCP et A2A ainsi que les sources de contenu sont collectés dans un binaire unique auto-hébergé qui sert la console, l'API REST, gRPC, la CLI et le provider Terraform ; le plan de contrôle cloud (construit, non déployé) et le portail de licences (déployé, vente désactivée) sont représentés comme des plans séparés.">
</picture>
</div>

Un binaire Go statique unique embarque la console et expose quatre surfaces avec une couverture documentée : l'API REST (principale), un miroir gRPC ciblé du cœur stable, la CLI `olivares` et un provider Terraform. Les collecteurs s'exécutent au sein de votre infrastructure selon trois modes ; le store est SQLite ou Postgres avec sécurité au niveau ligne, appliquée une première fois dans l'API du store puis de nouveau par Postgres. Pour les détails, y compris le plan de travail élément par élément : [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Documentation

[docs.olivares.ai](https://docs.olivares.ai) — tutoriels d'installation testés (mononœud, Docker Compose, Kubernetes/Helm, air-gapped), guides sur les connecteurs avec de vraies captures de console, cookbook (politiques deny-closed, budgets, approbations, exercices de kill switch, push SIEM), référence API et glossaire. Commencez par [Qu'est-ce qu'Olivares AI ?](docs-site/src/content/docs/start/what-is-olivares-ai.md) et [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md).

<a name="security"></a>
## Sécurité

Signalez une vulnérabilité en privé via [`SECURITY.md`](SECURITY.md), jamais dans une issue publique. Le moteur est lecture-d'abord et données-minimales : l'access map stocke des arêtes, pas des payloads, et son ouverture est une action enregistrée. Flux des avis : [`docs/security-advisories.md`](docs/security-advisories.md) ; carte des preuves de la chaîne d'approvisionnement : [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Communauté

[`CONTRIBUTING.md`](CONTRIBUTING.md) (configuration, DCO/CLA, SPDX, frontière des connecteurs) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog 1.1, CalVer `vYY.M.PATCH`).

## Licence

`core/`, `modules/` et `web/` sont sous **AGPL-3.0-only** ; `sdk/`, `connectors/` et `clients/` sont sous **Apache-2.0**, et un connecteur n'importe jamais le moteur. Les add-ons commerciaux sont séparés, optionnels et fermés — compilés uniquement avec `-tags enterprise`, jamais dans ce dépôt ni dans le binaire ouvert ; pour une licence commerciale, contactez `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Les contributions nécessitent une signature DCO (`git commit -s`) et la [CLA](CLA.md).

> **Aucune garantie, aucune responsabilité.** Le logiciel est fourni **en l'état**, **sans garantie d'aucune sorte** et **sans responsabilité en cas de perte de données, d'interruption d'activité ou de perte de profits**. Sur un plan de contrôle, ce n'est pas une formalité : une mauvaise configuration peut bloquer un travail légitime ou laisser passer exactement ce que vous vouliez arrêter. L'AGPL-3.0-only §§15–16, l'Apache-2.0 §§7–8 et le terme supplémentaire propre à ce projet s'appliquent — [`DISCLAIMER.md`](DISCLAIMER.md).

## Soutenir le projet

Le cœur est libre et le restera ; maintenir chaque version signée, vérifiée et à jour est un travail continu. Si Olivares AI vous est utile, vous pouvez parrainer le projet via GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) ou [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — ou effectuer un don ponctuel sur Ko-fi. Le parrainage n'est pas un contrat de support et n'achète aucune priorité ([`SUPPORT.md`](SUPPORT.md)) ; les parrains qui demandent à être nommés figurent dans [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>La ground truth de l'IA d'entreprise.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
