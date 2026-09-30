<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — La ground truth de l'IA d'entreprise" width="720"></a>

**Langues :** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · **Français**

**Exécutez et gouvernez l'IA que vous utilisez déjà, sur votre propre infrastructure.**

[Ce que c'est](#ce-que-cest) · [Ce qu'il fait](#ce-quil-fait) · [Installation](#installation) · [Démarrage rapide](#démarrage-rapide) · [Console](#un-aperçu-de-la-console) · [Éditions](#éditions-et-tarifs) · [Documentation](#documentation) · [Sécurité](#sécurité) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10.0](https://img.shields.io/badge/release-26.10.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Bêta.** 26.10.0 est livrée avec des archives signées, des paquets natifs et des images de conteneur. [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md) indique ce qui fonctionne aujourd'hui, ce qui fonctionne à la demande et ce qui reste à l'état de conception.

## Ce que c'est

Olivares AI est un plan de contrôle auto-hébergé pour les agents d'IA : un seul binaire Go, console incluse. Il donne aux agents du contexte, l'accès aux ressources et des sessions gérées, et vous donne les permissions, les politiques, les budgets et les preuves. Aucune télémétrie n'est obligatoire, et les installations air-gapped sont prises en charge.

Claude Code se connecte par son hook `PreToolUse`/`PostToolUse`, les paramètres gérés, et le lancement et l'arrêt depuis la console. Les CLI officielles de Codex et de Grok fonctionnent comme sessions gérées. gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes et les endpoints auto-hébergés comme Ollama sont des connecteurs ; chacun indique ce qu'il applique et ce qu'il se contente d'observer.

## Ce qu'il fait

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Diagramme animé de la carte d'accès en lecture/écriture : agents, sessions et identités à gauche, les ressources qu'ils atteignent à droite, lectures en bleu, écritures en orange, une écriture observée qui n'a jamais été permise signalée comme constat de drift.">
<br><sub><b>La carte d'accès</b> — ce que chaque agent lit et écrit dans votre estate, le permis face à l'observé.</sub>
</div>

- **Voir.** Un inventaire des agents, sessions, modèles, serveurs MCP, outils et identités que les connecteurs observent ; une **carte d'accès** en lecture/écriture avec une vue de **dérive** entre l'autorisé et l'observé ; les sessions en direct, le graphe d'orchestration, la santé et le SLA. Un accès qu'il ne sait pas classer apparaît comme `unknown`.
- **Faire le travail.** Des éléments de travail avec responsable, dépendances, critères d'acceptation et décisions ; des baux clôturés, pour que deux agents ne détiennent pas le même élément ; des sessions Claude Code, Codex et Grok lancées, rattachées, interrompues et arrêtées depuis la console ; la délégation à des pairs autorisés via A2A.
- **Gouverner et appliquer.** Un moteur d'autorisation Cedar et **quatre points d'application fermés par défaut (deny-closed)** : le hook de Claude Code, un proxy d'inférence `/v1/messages` en ligne, une porte MCP `tools/call` et une porte de délégation A2A. Une action non autorisée est bloquée, mise en attente d'une approbation à deux personnes ou réécrite avant son exécution. Les budgets refusent ou limitent la dépense, le break-glass exige deux personnes, et le **kill-switch** échoue en position fermée.
- **Alimenter, sous gouvernance.** SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL et un système de fichiers confiné à sa racine alimentent une recherche gouvernée ; l'habilitation est vérifiée au moment de la recherche.
- **Prouver.** Un registre d'audit chaîné par hachage et signé Ed25519 ; des preuves scellées associées à **26 catalogues de référentiels** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, RGPD et d'autres) sous forme de familles de contrôles auto-évaluées, non de certifications ; envoi vers SIEM et ITSM (CEF, LEEF, syslog, OTLP, OCSF) ; WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK et droit à l'effacement vérifié, configurés par déploiement.

**31 modules**, une console, **159 intégrations**, comptés à partir du code par [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). La répartition est dans [`connectors/README.md`](connectors/README.md), et chaque module avec sa maturité dans le [catalogue des modules](docs-site/src/content/docs/reference/modules/overview.md).

## Installation

Choisissez une méthode. Ensuite, `olivares quickstart` affiche l'URL de la console et un jeton de configuration à usage unique. Les versions sont signées avec cosign, avec provenance SLSA et SBOM ; chaque méthode ci-dessous vérifie avant d'installer, et `scripts/verify-release.sh` contrôle un téléchargement manuel (cosign et SHA-256, [comment](INSTALL.md#verifying-a-release)). Le moteur démarre en HTTPS, sans identifiants par défaut et avec un jeton de configuration à usage unique.

**1 · Une commande, Linux et macOS.** L'installateur détecte le système et l'architecture, vérifie les sommes de contrôle signées et le SHA-256 de l'archive, installe uniquement le binaire et n'exécute jamais `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Ajoutez `--user` pour un service utilisateur (unité systemd utilisateur ou LaunchAgent), ou lancez le script depuis un shell privilégié avec `--system --start` pour un service système. La voie manuelle et la matrice par système : [`INSTALL.md`](INSTALL.md).

**2 · Docker.** Multi-architecture, distroless, sans root. Les ports sont publiés sur toutes les interfaces de l'hôte ; préfixez les mappages `-p` par `127.0.0.1:` pour les garder en local.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` est la même image ; en production, épinglez-la par digest. Variantes FIPS et STIG : [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose.** Une pile durcie : SQLite sur un nœud, avec Postgres et sauvegarde en option.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes.** Le chart Helm de ce dépôt, ou un manifeste plat sans Helm. Le chart n'a pas encore de publication OCI (`publication-unverified`).

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Paquets Linux.** `.deb`, `.rpm` et `.apk` sur la [page de la version](https://github.com/olivaresai/olivares/releases/tag/26.10.0) : le binaire, un fichier env d'exemple, un utilisateur `olivares` sans connexion et une unité durcie. Le service ne démarre pas à l'installation.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew.** macOS et Linux, vérifié avec les sommes de contrôle signées.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · Depuis les sources.** Go 1.26+, [Task](https://taskfile.dev) et pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped :** regroupez l'image, le chart et le matériel de vérification signés, puis vérifiez hors ligne avec `scripts/verify-release.sh --key … --offline` ([guide](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** n'a pas encore de build natif : utilisez le conteneur Linux ou WSL2 ([plan](INSTALL.md#windows)). Mise à jour et retour arrière : [guide](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Démarrage rapide

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

Le mot de passe de la démo est public : n'utilisez pas la démo avec des données réelles. Le [démarrage rapide complet](docs-site/src/content/docs/start/quickstart.md) connecte une source pgAudit réelle.

## Un aperçu de la console

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Carte d'accès : ce que chaque agent lit et écrit dans votre estate, origines à gauche, ressources à droite."></picture><br><sub><b>Carte d'accès</b> — origines à gauche, ressources à droite, lecture et écriture par couleur.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Drift de moindre privilège : accès inattendus et octrois inutilisés superposés à la carte d'accès."></picture><br><sub><b>Drift de moindre privilège</b> — observé mais non permis, et octrois que personne n'utilise.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Sessions Claude Code créées, auxquelles on s'attache et qui sont gouvernées depuis la console."></picture><br><sub><b>Sessions</b> — créez, attachez-vous et gouvernez des sessions depuis la console, sans SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Travail : le backlog durable inter-sessions d'éléments de travail et de décisions."></picture><br><sub><b>Travail</b> — le backlog durable inter-sessions : éléments, titulaire, acceptation, décisions.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Sécurité et forensique : constats de guardrails, la file d'anomalies et une forensique inviolable."></picture><br><sub><b>Sécurité &amp; forensique</b> — constats de guardrails, anomalies, forensique inviolable.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps : dépenses par modèle, usage de jetons, budgets et une projection de run-rate."></picture><br><sub><b>FinOps</b> — dépenses par modèle et agent, budgets qui refusent ou limitent, run-rate.</sub> |

Les écrans viennent de la démo sur le binaire en fonctionnement. Tous les écrans : la [référence de la console](docs-site/src/content/docs/reference/console.md).

## Éditions et tarifs

Community est le produit complet sous AGPL-3.0, avec des utilisateurs illimités et les quatre points d'application deny-closed. Business et Enterprise ajoutent du code commercial, compilé uniquement avec `-tags enterprise` ; rien n'est retiré ni limité dans Community.

| Édition | Prix | Comprend |
|---|---|---|
| **Community** | Gratuit, AGPL-3.0 | Le produit complet auto-hébergé. Utilisateurs illimités, un fournisseur d'identité actif. |
| **Business** | 129 USD/mois ou 1 290 USD/an | La licence commerciale, le canal de versions signé, le support par e-mail aux heures ouvrées, ainsi que **Regulated Operations**, **AI Runtime Security**, **Compliance Packs** et **Identity & Scale**. Utilisateurs illimités ; une entité juridique ; jusqu'à deux déploiements de production, chacun avec un déploiement de préproduction ; jusqu'à cinq fournisseurs d'identité actifs. |
| **Enterprise** | Contrat | Davantage d'entités juridiques, de déploiements et de fournisseurs d'identité, miroirs air-gapped, LTS et conditions de support sur mesure, sur bon de commande annuel. |

Conditions d'achat : [olivares.ai/pricing](https://olivares.ai/pricing). Ce qui est ouvert et ce qui est commercial : [`LICENSING.md`](LICENSING.md).

## Architecture

Un binaire Go statique intègre la console et expose quatre interfaces : l'API REST, un miroir gRPC du noyau stable, la CLI `olivares` et un fournisseur Terraform. Les collecteurs s'exécutent dans votre infrastructure. Le stockage est SQLite ou Postgres avec sécurité au niveau des lignes, appliquée dans l'API de stockage puis par Postgres. Détails, plan de travail compris : [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Documentation

[docs.olivares.ai](https://docs.olivares.ai) — tutoriels d'installation testés (mononœud, Docker Compose, Kubernetes/Helm, air-gapped), guides de connecteurs avec de vraies captures de console, un cookbook (politiques deny-closed, budgets, approbations, exercices de kill-switch, push SIEM), référence API et un glossaire. Commencez par [Qu'est-ce qu'Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). Sur le site : [produit](https://olivares.ai/product) · [solutions](https://olivares.ai/solutions) · [comment ça marche](https://olivares.ai/how-it-works) · [architecture](https://olivares.ai/architecture) · [sécurité](https://olivares.ai/security) · [confiance](https://olivares.ai/trust) · [comparer](https://olivares.ai/compare) · [démo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [statut](https://olivares.ai/status) · [feuille de route](https://olivares.ai/roadmap) · [marque](https://olivares.ai/brand) · [presse](https://olivares.ai/press). Versions : [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Sécurité

Signalez une vulnérabilité en privé via [`SECURITY.md`](SECURITY.md), pas dans une issue publique. La carte d'accès stocke des arêtes, pas des contenus, et son ouverture est auditée. La vérification de licence se fait hors ligne ; le noyau AGPL ne fait aucun appel de licence. Avis : [`docs/security-advisories.md`](docs/security-advisories.md) ; preuves de chaîne d'approvisionnement : [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Communauté

[`CONTRIBUTING.md`](CONTRIBUTING.md) (configuration, DCO/CLA, SPDX, la frontière des connecteurs) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `YY.M.PATCH`).

## Licence

`core/`, `modules/` et `web/` sont sous **AGPL-3.0-only** ; `sdk/`, `connectors/` et `clients/` sous **Apache-2.0**, et un connecteur n'importe jamais le moteur. Le code commercial est compilé uniquement avec `-tags enterprise` et n'est pas dans ce dépôt. Licences commerciales : `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Les contributions exigent un DCO sign-off (`git commit -s`) et la [CLA](CLA.md).

> **Aucune garantie.** Le logiciel est fourni **en l'état**, **sans garantie d'aucune sorte** et **sans responsabilité pour la perte de données, l'interruption d'activité ou le manque à gagner**. S'appliquent AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 et la clause complémentaire de ce projet — [`DISCLAIMER.md`](DISCLAIMER.md).

## Soutenir le projet

Soutenez le projet via GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) ou [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — ou ponctuellement sur Ko-fi. Le parrainage n'est pas un contrat de support ([`SUPPORT.md`](SUPPORT.md)) ; les sponsors qui souhaitent être nommés figurent dans [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>La ground truth de l'IA d'entreprise.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
