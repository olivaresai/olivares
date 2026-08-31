---
title: "Explication"
description: "Vue d'ensemble orientée compréhension d'Olivares AI : comment il intègre, gère et sécurise l'IA d'entreprise en une seule ground truth : Claude Code au niveau le plus profond, Codex et Grok Build à ses côtés — son architecture modulaire répartie sur 30 modules, l'access map read-first et le modèle open-core."
---

Cette section est orientée compréhension. Elle explique *pourquoi* Olivares AI est
façonné de la manière dont il l'est — les principes de conception, la posture de
sécurité et le modèle de licence — plutôt que de vous guider à travers une tâche. Si
vous voulez *faire* quelque chose, commencez par le [tutoriel](/fr/tutorials/zero-to-graph/)
ou les [guides pratiques](/fr/how-to/connect-claude-code/) ; si vous avez besoin d'un
contrat exact, utilisez la [référence](/fr/reference/). Pour savoir où vit chaque type
de page, voir [Comment la documentation est organisée](/fr/start/how-the-docs-are-organized/).

:::note[Produit au stade de la conception]
Une grande partie de la profondeur décrite ici est pré-1.0 et au stade de la
conception. Ces pages sont honnêtes sur ce qui tourne aujourd'hui versus ce qui est
planifié ou post-v1. Quand une capacité n'est pas construite, ou que sa couverture
est partielle, la page le dit. Voir [Honnêteté et limites](/fr/start/honesty-and-limits/)
pour les divulgations permanentes du projet.
:::

## Une plateforme modulaire : moteur + modules + connecteurs

Olivares AI aide les entreprises à **intégrer, gérer et sécuriser l'IA qu'elles
exploitent déjà** — une seule ground truth : Claude Code au niveau le plus profond, Codex et Grok Build à ses côtés, en les complétant
plutôt qu'en les concurrençant. Il est livré comme un unique binaire Go statique
(`olivares`) avec l'interface web embarquée et servie depuis la même origine que
l'API. L'architecture est une plateforme, pas un outil unique : un **moteur cœur**
fournit les sous-systèmes partagés — l'ingestion et un bus d'événements en-process,
le SDK de connecteurs, le runtime de modules, un modèle de données multi-tenant,
l'API REST/gRPC, l'authentification et l'autorisation, et l'audit ledger
append-only — et chaque capacité est l'un des **30 modules** qui se rattachent à ces
sous-systèmes sans ré-architecturer le cœur. Les **connecteurs** alimentent le
moteur depuis l'extérieur via un SDK stable ; un connecteur n'importe jamais depuis
le cœur, ce qui garde la frontière de licence propre.

Le store par défaut est SQLite (pur-Go) pour l'usage mono-nœud et air-gapped,
passant à Postgres avec row-level security pour le multi-tenant et le passage à
l'échelle. Le bus d'événements est en-process par défaut ; NATS est un binding
distribué optionnel, pas une exigence. La plateforme livre aujourd'hui **30 modules**,
chacun à sa propre maturité honnête — la plupart opérationnels et câblés de bout en
bout, certains partiels ou opt-in — répartis sur neuf domaines de capacité ; le
registre de modèles propriétaires et le fine-tuning sont une **capacité planifiée**,
pas un module livré.

→ Lisez la [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/)
pour le moteur, le modèle de données et les topologies de déploiement au complet.

## L'access map : read-first, minimal-data, Permitted-vs-Observed

Parmi les plus utiles des 30 capacités figure la **R/RW access map**. Elle construit
un graphe de quel agent lit ou écrit quelle ressource, et elle le fait avec deux
contraintes délibérées :

- **Read-first.** La map observe via la télémétrie, les logs d'audit natifs et un
  filet de sécurité eBPF au niveau du noyau — elle se situe hors du data path,
  jamais dedans. Elle ne proxie pas, n'intercepte pas et ne gate pas le trafic en
  direct.
- **Minimal-data.** Elle ne stocke que la relation (agent → ressource, lecture ou
  écriture) ainsi que la source du signal et un niveau de confiance. Elle ne stocke
  pas de payloads, de secrets ou de PII.

Au-dessus de ce graphe se trouve la vue la plus distinctive : le **diff
Permitted-vs-Observed**, qui fait remonter le least-privilege drift en comparant ce
que la policy *permet* à ce que les agents sont *observés* en train de faire. Le
chemin coopératif, haute fidélité, est Claude Code via OpenTelemetry plus
l'introspection MCP, corroboré par l'audit natif du store (par exemple, pgAudit
classifiant lectures et écritures, ou CloudTrail exposant l'accès read-only sur
l'object storage) ; le filet de sécurité non coopératif est eBPF au niveau du noyau.
Les annotations MCP sont traitées comme non fiables conformément à la spécification
MCP et sont corroborées, jamais accordées de confiance seules.

:::caution[La couverture est par paliers]
La fidélité dépend de la source. Elle est clean pour les bases de données SQL, les
object stores et les entrepôts ; lossy pour des systèmes tels que les bases de
données documentaires et vectorielles ; et non réalisable passivement pour certains
stores (par exemple Redis, SQLite ou D1). La map affiche son niveau de confiance
plutôt que de fabriquer une attribution qu'elle n'a pas.
:::

→ Lisez le [Modèle de sécurité](/fr/explanation/security/security-model/) pour la
posture et le [Modèle de menaces](/fr/explanation/security/threat-model/) pour les
hypothèses et les limites.

## Auto-hébergé et open-core

Le data plane — les collecteurs — **s'exécute toujours sur l'infrastructure du
client**, de sorte que les données de l'estate n'ont pas à quitter le périmètre du
client. Le control plane peut tourner comme un unique binaire auto-hébergé, comme un
déploiement distribué (les collecteurs poussant vers un cœur central via gRPC avec
mTLS, adossé à Postgres), ou entièrement air-gapped avec zéro egress et une licence
hors ligne ; une option managée est un travail à venir.

Les licences sont open-core. Le cœur du moteur, les modules et l'interface web sont
AGPL-3.0-only ; le SDK et les connecteurs sont Apache-2.0 ; un tier enterprise est
commercial. Ce découpage est ce qui permet aux tiers de construire des connecteurs
sans que la frontière copyleft n'atteigne leur code.

→ Lisez [Open core et licences](/fr/explanation/open-core-and-licensing/) pour la carte
des licences par répertoire et ce que cela signifie en pratique.

## Décisions d'architecture

Le raisonnement derrière les choix porteurs — bearer tokens opaques au lieu de JWT,
le PDP d'autorisation pluggable derrière une jointure unique, SQLite-vers-Postgres,
l'audit ledger hash-chained et signé — est consigné sous forme d'Architecture
Decision Records.

## Réglementation, positionnement & adéquation

Deux autres fils orientés compréhension accompagnent l'architecture. Le premier est
**réglementaire** : comment le control plane transforme le comportement en direct de
votre estate en preuves techniques dont un dossier au titre du règlement IA de l'UE
a besoin, générées depuis les données d'exécution et stockées par le control plane
que vous exploitez vous-même.

→ Lisez [Preuves pour le règlement IA de l'UE à partir des données d'exécution](/fr/explanation/eu-ai-act-evidence/).

Le second est **où le produit se situe sur le marché** — défini honnêtement, avec
chaque statistique tracée jusqu'à une source primaire. Ces pages expliquent le
vocabulaire des analystes (agent sprawl, guardian agents, AI TRiSM), comment
Olivares AI se rapporte aux outils adjacents (LLM gateways/observabilité, tours de
contrôle IA — nous intégrons, nous ne concurrençons pas), la verticale de
l'enseignement supérieur, et d'où proviennent les données et les affirmations.

→ Parcourez [Positionnement & adéquation](/fr/explanation/positioning/market-context-and-sources/),
en commençant par le [contexte de marché & sources](/fr/explanation/positioning/market-context-and-sources/)
vérifié.
