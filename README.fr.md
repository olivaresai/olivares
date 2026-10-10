<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Langues :** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · **Français**

**Exécutez l'IA que votre équipe utilise déjà, avec le même contrôle que sur le reste de votre infrastructure.**

[Ce qu'il fait](#ce-quil-fait) · [Installation](#installation) · [Console](#un-aperçu-de-la-console) · [Éditions](#éditions-et-tarifs) · [Documentation](#documentation) · [Communauté](#communauté) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.1](https://img.shields.io/badge/release-0.1-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.1)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

La prochaine version est <!-- release -->`0.1`<!-- /release --> ; sa release GitHub n’est pas encore publiée. Les commandes ci-dessous décrivent les artefacts prévus. Compilez depuis les sources jusqu’à la publication, puis vérifiez chaque artefact avant utilisation. L’état observé figure dans <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->. Kubernetes OCI: `publication-unverified`.

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
- **Fournir des preuves quand on vous les demande.** Chaque décision est inscrite dans un journal signé où toute modification ultérieure est détectable. Business Compliance Packs rattache les preuves à 26 catalogues de cadres et produit des rapports pour votre équipe de sécurité et vos auditeurs. Community conserve les preuves stockées et leurs exports JSON/CSV.

Il fonctionne avec vos outils actuels : Claude Code, Codex, Grok, Cursor, gemini-cli, opencode, OpenHands et les modèles locaux via Ollama. **32 modules** et **136 intégrations** : [tous les modules](docs-site/src/content/docs/reference/modules/overview.md) · [tous les connecteurs](connectors/README.md).

Community conserve l’observabilité locale, les paramètres enregistrés et l’export des sauvegardes. L’envoi SIEM/ITSM, la télémétrie externe et l’export de posture sont inclus dans l’édition de base Business.

## Installation

**Docker Compose.** The container qualification job exercises this installation path.
Set the release image explicitly so a cached `:latest` image cannot select an
older release.

<!-- release -->
```sh
set -e
cd /path/to/your/project   # the host folder your sessions will work on
export OLIVARES_PROJECT_DIR="$PWD"
git clone --depth 1 https://github.com/olivaresai/olivares.git "$HOME/olivares"
export OLIVARES_IMAGE=docker.io/olivaresai/olivares:0.1
# On Linux hosts whose AppArmor policy mediates user namespace creation:
if [ -r /sys/kernel/security/apparmor/features/namespaces/mask ] &&
   grep -qw userns_create /sys/kernel/security/apparmor/features/namespaces/mask; then
  sudo install -m 0644 "$HOME/olivares/deploy/apparmor/olivares-sessions.conf" /etc/apparmor.d/olivares-sessions
  sudo apparmor_parser -r /etc/apparmor.d/olivares-sessions
  export OLIVARES_APPARMOR_PROFILE=olivares-sessions
fi
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" up --wait --wait-timeout 120
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" exec olivares \
  olivares first-boot --data-dir /var/lib/olivares --new-token
```
<!-- /release -->

Open the console address printed by `first-boot --new-token` and use the replacement
one-time setup token it prints to create the first administrator. This invalidates
the previous setup token and is available only before the first administrator exists.
The stack uses SQLite and a persistent data volume.
The AppArmor step needs an AppArmor 4 parser and runs on the Docker daemon host.
See [session confinement on AppArmor hosts](deploy/compose/README.md#session-confinement-on-apparmor-hosts).
It publishes ports on every host interface by default; set `OLIVARES_BIND=127.0.0.1`
to restrict access to this host.

Sessions in the container work on one host folder, mounted at `/project`: the absolute
path in `OLIVARES_PROJECT_DIR`, set before `up`. Without it, `/project` is an empty Docker
volume, never the directory you run Compose from. A session there can change everything in
that folder: never set the variable to your home directory. In the console, choose **Change folder**
on the New session form and enter `/project`. On a Linux host the container user
(UID 65532) needs write access; see
[work on a host project folder](deploy/compose/README.md#work-on-a-host-project-folder).

Gate coverage is not a passing release result: see the `qualify-compose-ready` job in
[container qualification](.github/workflows/compose-ready.yml). The release must also
pass its first-hour journey before it is qualified.

Other installation methods are **not qualified** by the first-hour gate. Their commands
and limits are in [INSTALL.md](INSTALL.md#installation-qualification), including the
shell installer, standalone Docker, Kubernetes, native packages, Homebrew, source builds
and offline installs. [Verify release artifacts](INSTALL.md#verifying-a-release) before
running them; see [upgrading and uninstalling](INSTALL.md#upgrading--uninstalling) for an
existing installation.

- Helm, l’opérateur Kubernetes et le fournisseur Terraform sont distribués avec Business. [Editions](https://olivares.ai/pricing).
Install the chart from source; see [Kubernetes installation](INSTALL.md#kubernetes).

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
| **Périmètre** | Un fournisseur d'identité actif | Une entreprise, une instance active à la fois | Défini dans le contrat |

**Regulated Operations** ajoute des durées minimales de conservation réglementaires, le rapprochement des gels légaux sur les archives et des archives WORM sur Azure et GCS. **AI Runtime Security** ajoute une inspection plus approfondie de ce que les agents envoient, reçoivent et exécutent. **Compliance Packs** prépare des brouillons du registre d'information DORA et du dossier ISO/IEC 42001 pour votre auditeur. **Identity & Scale** connecte plusieurs fournisseurs d'identité à la fois et accompagne les déploiements plus grands.

[olivares.ai/pricing](https://olivares.ai/pricing) · [Ce que comprend chaque édition](docs/editions.md) · [Ce qui est ouvert et ce qui est commercial](LICENSING.md)

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
