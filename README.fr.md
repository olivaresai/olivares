<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Langues :** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · **Français**

**Exécutez l'IA que votre équipe utilise déjà, avec le même contrôle que sur le reste de votre infrastructure.**

[Ce qu'il fait](#ce-quil-fait) · [Installation](#installation) · [Console](#un-aperçu-de-la-console) · [Éditions](#éditions-et-tarifs) · [Documentation](#documentation) · [Communauté](#communauté) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10](https://img.shields.io/badge/release-26.10-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.1)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

Vos développeurs travaillent avec Claude Code et Codex. Les agents appellent des serveurs MCP, des modèles et des API internes, et les tâches planifiées s'exécutent seules. Chaque composant a ses propres journaux et permissions. Des questions simples restent donc sans réponse rapide : quel agent a modifié ce fichier, qui l'a approuvé, combien l'IA nous a-t-elle coûté ce mois-ci ?

Olivares AI rassemble les réponses au même endroit. Il se connecte aux agents et aux outils que vous utilisez déjà, montre ce que chacun fait, applique vos règles avant l'exécution d'une action et conserve un enregistrement signé de tout. C'est un seul programme qui tourne sur vos propres serveurs. Le produit complet est gratuit et open source.

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>La carte d'accès</b> — ce que chaque agent lit et écrit, et l'écriture que personne n'a autorisée.</sub>
</div>

## Ce qu'il fait

- **Savoir ce qui tourne.** Tous les agents, sessions, modèles, serveurs MCP et outils dans un seul inventaire. La carte d'accès montre ce que chacun lit et écrit, et signale les accès qu'aucune règle n'autorise.
- **Arrêter une action avant qu'elle ne cause des dégâts.** Olivares AI dispose de **quatre points d'application fermés par défaut (deny-closed)** qui vérifient chaque action avant son exécution : dans Claude Code, dans le proxy de modèles, à chaque appel d'outil MCP et entre agents. Une action risquée attend une deuxième personne ; une action interdite ne s'exécute pas. Un seul interrupteur arrête tous les agents à la fois. Si une vérification ne peut pas trancher, l'action ne s'exécute pas.
- **Maîtriser les dépenses d'IA.** Les budgets par équipe, agent ou modèle avertissent, ralentissent ou arrêtent les dépenses avant l'arrivée de la facture.
- **Donner aux agents un accès sûr aux connaissances de votre entreprise.** Connectez SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3 et PostgreSQL. Chaque agent ne voit que ce que la personne qui l'utilise est autorisée à voir.
- **Poursuivre le travail entre les sessions.** Les tâches, les responsables et les décisions restent quand une session se termine. Démarrez, rejoignez et arrêtez des sessions Claude Code, Codex et Grok depuis le navigateur, sans SSH.
- **Fournir des preuves quand on vous les demande.** Chaque décision est inscrite dans un journal signé où toute modification ultérieure est détectable. Votre équipe de sécurité et vos auditeurs en tirent leurs rapports, avec des preuves rattachées à 26 catalogues de cadres.

Il fonctionne avec vos outils actuels : Claude Code, Codex, Grok, Cursor, gemini-cli, opencode, OpenHands et les modèles locaux via Ollama. **31 modules** et **159 intégrations**, tous dans l'édition gratuite : [tous les modules](docs-site/src/content/docs/reference/modules/overview.md) · [tous les connecteurs](connectors/README.md).

## Installation

Choisissez une méthode et copiez son bloc. À la fin, `olivares quickstart` affiche l'adresse de la console et un jeton à usage unique pour créer le premier administrateur. Chaque version est signée, et chaque méthode vérifie le téléchargement avant de l'installer ([vérifier vous-même un téléchargement](INSTALL.md#verifying-a-release)).

**Linux et macOS, une commande.** Détecte votre système, vérifie la version, installe uniquement le binaire et n'utilise jamais `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker.** Multi-architecture. Les images de conteneur reposent sur Debian 13 slim (avec Node.js 24 pour les outils d'agent) et s'exécutent en tant qu'utilisateur non root. Écoute sur toutes les interfaces de l'hôte ; ajoutez `127.0.0.1:` avant chaque `-p` pour limiter l'accès à la machine locale.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

**Docker Compose.** SQLite sur un seul nœud, avec Postgres et sauvegardes en option.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**Kubernetes.** Le chart Helm de ce dépôt (le chart n'a pas encore de version publiée en OCI : `publication-unverified`).

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
```

Sans Helm :

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**Debian et Ubuntu.** Le paquet ajoute un utilisateur `olivares` sans connexion possible et un service durci ; vous le démarrez.

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.1/olivares_26.10.1_linux_amd64.deb
sudo dpkg -i olivares_26.10.1_linux_amd64.deb && sudo systemctl enable --now olivares
```

**RHEL, Fedora et SUSE.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.1/olivares_26.10.1_linux_amd64.rpm
sudo rpm -i olivares_26.10.1_linux_amd64.rpm && sudo systemctl enable --now olivares
```

**Alpine.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.1/olivares_26.10.1_linux_amd64.apk
sudo apk add --allow-untrusted olivares_26.10.1_linux_amd64.apk && sudo rc-service olivares start
```

Sur les serveurs ARM, utilisez `arm64` à la place de `amd64`. Tous les fichiers de la version : la [page de la version](https://github.com/olivaresai/olivares/releases/tag/26.10.1).

**Homebrew.** macOS et Linux.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**Depuis les sources.** Go 1.26+, [Task](https://taskfile.dev) et pnpm.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
task build && ./bin/olivares quickstart
```

**Réseaux hors ligne :** réunissez l'image signée, le chart et les éléments de vérification, puis [installez en environnement isolé](docs-site/src/content/docs/how-to/air-gap-install.md). **Windows** n'a pas encore de binaire natif : utilisez l'image Docker ou WSL2. Mises à niveau et retour arrière : [guide](docs-site/src/content/docs/how-to/upgrade-and-rollback.md). Toutes les options en détail : [`INSTALL.md`](INSTALL.md).

**Essayez d'abord avec les données de démonstration**, uniquement sur votre machine (le mot de passe de la démo est public) :

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
```

Ouvrez ensuite http://127.0.0.1:8901.

## Un aperçu de la console

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>Carte d'accès</b> — qui lit et écrit quoi.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Drift</b> — les accès que personne n’a autorisés et les permissions que personne n’utilise.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>Sessions</b> — démarrer, rejoindre et arrêter des sessions d’agents depuis le navigateur.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>Travail</b> — les tâches, responsables et décisions qui restent après une session.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>Sécurité</b> — les actions bloquées, les anomalies et un enregistrement où toute altération est détectable.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>Dépenses</b> — les coûts par modèle et agent, les budgets et les prévisions.</sub> |

Tous les écrans : la [référence de la console](docs-site/src/content/docs/reference/console.md).

## Éditions et tarifs

Community est le produit complet, gratuit et open source. Business ajoute ce dont une entreprise a besoin pour l'exploiter en production. Enterprise s'adresse aux groupes dont les infrastructures sont plus grandes ou soumises à réglementation.

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **Prix** | Gratuit, AGPL-3.0 | 129 USD/mois ou 1 290 USD/an | Contrat annuel |
| **Contenu** | Le produit complet : utilisateurs illimités et les quatre points d'application deny-closed | Tout Community, plus Regulated Operations, AI Runtime Security, Compliance Packs et Identity & Scale, la licence commerciale, les mises à jour signées et le support par e-mail | Tout Business, plus davantage d'entreprises, de déploiements et de fournisseurs d'identité, des miroirs hors ligne et des conditions de support convenues avec vous |
| **Périmètre** | Un fournisseur d'identité actif | Une entreprise, deux déploiements de production avec un environnement de staging chacun, cinq fournisseurs d'identité | Défini dans le contrat |

**Regulated Operations** conserve les enregistrements aussi longtemps que la loi l'exige, avec gel légal et archives inaltérables. **AI Runtime Security** filtre ce que les agents envoient, reçoivent et exécutent. **Compliance Packs** fournit des preuves prêtes pour ISO 42001, DORA et NIS 2. **Identity & Scale** connecte plusieurs fournisseurs d'identité à la fois et accompagne les déploiements plus grands.

[olivares.ai/pricing](https://olivares.ai/pricing) · [Ce qui est ouvert et ce qui est commercial](LICENSING.md)

## Architecture

Un seul binaire Go avec la console intégrée. Il fournit une API REST, une API gRPC, la ligne de commande `olivares` et un fournisseur Terraform. Les collecteurs tournent dans votre réseau, et les données restent dans SQLite ou PostgreSQL sur vos serveurs. [Comment tout s'articule](ARCHITECTURE.md).

## Documentation

[docs.olivares.ai](https://docs.olivares.ai) propose des guides d'installation, un guide pour chaque connecteur, des recettes de politiques courantes et la référence de l'API. Commencez par [Qu'est-ce qu'Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). Ce qui fonctionne aujourd'hui et ce qui reste prévu : [Honnêteté et limites](docs-site/src/content/docs/start/honesty-and-limits.md). Versions : [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

Sur le site : [produit](https://olivares.ai/product) · [solutions](https://olivares.ai/solutions) · [comment ça marche](https://olivares.ai/how-it-works) · [architecture](https://olivares.ai/architecture) · [sécurité](https://olivares.ai/security) · [confiance](https://olivares.ai/trust) · [comparer](https://olivares.ai/compare) · [démo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [statut](https://olivares.ai/status) · [feuille de route](https://olivares.ai/roadmap) · [marque](https://olivares.ai/brand) · [presse](https://olivares.ai/press).

## Sécurité

Vous avez trouvé une vulnérabilité ? Signalez-la en privé via [`SECURITY.md`](SECURITY.md). Olivares AI enregistre quel agent a accédé à quelle ressource, pas le contenu, et la consultation de cet enregistrement est elle-même journalisée. Les licences sont vérifiées hors ligne ; le cœur open source ne nous contacte jamais.

## Communauté

Les contributions sont les bienvenues. [`CONTRIBUTING.md`](CONTRIBUTING.md) explique la mise en place, le sign-off et l'intégration des connecteurs. [Code de conduite](CODE_OF_CONDUCT.md) · [Support](SUPPORT.md) · [Gouvernance](GOVERNANCE.md) · [Journal des modifications](CHANGELOG.md).

## Soutenir le projet

Olivares AI est développé ouvertement. S'il vous est utile, soutenez son développement sur GitHub Sponsors — [olivaresai](https://github.com/sponsors/olivaresai) ou [fran-olivares](https://github.com/sponsors/fran-olivares) — ou offrez-nous un café sur Ko-fi. Les sponsors qui souhaitent être cités figurent dans [`SUPPORTERS.md`](SUPPORTERS.md). Le parrainage n'est pas un contrat de support ([`SUPPORT.md`](SUPPORT.md)).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

## Licence

Le moteur, les modules et la console sont sous **AGPL-3.0-only** ; le SDK, les connecteurs et les clients sous **Apache-2.0**. Le code commercial est compilé séparément et ne figure pas dans ce dépôt ; licences commerciales : `enterprise@olivares.ai`. Les contributions exigent un DCO sign-off (`git commit -s`) et la [CLA](CLA.md).

> Fourni **en l'état**, sans garantie d'aucune sorte et sans responsabilité en cas de perte de données, d'interruption d'activité ou de manque à gagner. Voir [`DISCLAIMER.md`](DISCLAIMER.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>La ground truth de l'IA d'entreprise.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
