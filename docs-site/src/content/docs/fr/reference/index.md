---
title: "Référence"
description: "La référence orientée information : l'API REST, le bus d'événements, le catalogue des modules, la CLI et la configuration — précise et exhaustive, rien d'inféré."
---

La référence est **orientée information**. Son rôle est d'être précise et complète, non
d'enseigner ou de persuader : elle énonce ce que sont les interfaces, quelles sont leurs
entrées et sorties, et quels sont les défauts — et s'arrête là. La prose est sèche à
dessein. Si vous voulez apprendre le système en pratiquant, commencez par le
[tutoriel](/fr/tutorials/zero-to-graph/) ; si vous voulez accomplir une tâche précise,
utilisez un [guide pratique](/fr/how-to/connect-a-source/) ; si vous voulez comprendre
*pourquoi* le système est construit ainsi, lisez l'[explication](/fr/explanation/architecture/overview/).
Cette section est faite pour quand vous construisez contre le produit et avez besoin du
contrat exact.

L'essentiel de ce qui suit est généré ou dérivé à la main **directement à partir des
propres artefacts source du produit**, de sorte que la référence ne peut pas dériver
silencieusement de ce que le moteur sert réellement. Là où une capacité est en phase de
conception ou post-v1, la page concernée le dit clairement ; voir [Honnêteté &
limites](/fr/start/honesty-and-limits/) pour le contrat global.

## Les domaines de référence

| Domaine | Ce qu'il documente | Source de vérité |
|---|---|---|
| **[API REST](/reference/api/)** | L'API HTTP du control plane : auth, setup, tenancy, agents, l'access map R/RW, tokens et l'audit ledger. | Le contrat **OpenAPI 3.1** du produit (53 chemins de cœur), rendu au build à partir du fichier réel — pas une copie. |
| **[Routes de module (bêta)](/reference/api-beta/)** | Les routes de module du produit (`/v1/m/<ns>/…`) — FinOps, conformité, gouvernance, sessions, modèles, knowledge, … — dans un document OpenAPI **bêta** distinct. | Le même contrat OpenAPI 3.1, reflété au build à partir des routes enregistrées par les modules. |
| **[Politique de stabilité](/fr/reference/api-stability/)** | Versioning, niveaux de stabilité, signalement dépréciation/sunset et fenêtres de support minimales pour l'API, le provider et les SDK clients. | La table de dépréciation in-code et ses tests de fenêtre qui font échouer le build. |
| **[gRPC](/fr/reference/grpc/)** | Le miroir gRPC du moteur et le contrat wire versionné des plugins utilisé par chaque connecteur et module hors processus. | Les tables d'enregistrement `grpc.ServiceDesc` que les serveurs transmettent à gRPC. |
| **[Bus d'événements](/fr/reference/events/)** | Le bus d'événements interne : l'enveloppe d'événement, les types d'événements first-party et les payloads d'observation que les connecteurs y hissent. | Un contrat **AsyncAPI 3.0**, dérivé à la main du SDK Go. |
| **[Écrans de la console](/fr/reference/console/)** | Chaque route publiée par la console, avec la permission RBAC requise et la page de référence qu'ouvre son lien d'aide intégré au produit. | Le recensement des routes de la console, épinglé au router construit. |
| **[Catalogue des modules](/fr/reference/modules/overview/)** | Les 30 modules du produit — ce qu'est chacun, son statut, et quelles routes (le cas échéant) il expose hors de l'API de cœur. | Le catalogue de capacités du produit et les interfaces de modules typées. |
| **[CLI](/fr/reference/cli/)** | Le binaire `olivares` et ses sous-commandes — `serve`, `collector`, `audit`, `license`, `openapi`, `version` — et leurs flags. | Les définitions de commandes compilées. |
| **[Configuration](/fr/reference/configuration/)** | Variables d'environnement et options runtime : le répertoire de données, le câblage des sources, le moteur d'autorisation et la signature du ledger. | Les chargeurs de configuration du moteur. |

## API REST

La [référence de l'API REST](/reference/api/) est rendue au build à partir du contrat
**OpenAPI 3.1** du produit — le même document que le moteur sert à son propre endpoint
`/openapi.json`. Rien n'est transcrit à la main, de sorte que la référence rendue est le
contrat. Elle couvre le flux de premier boot sans credential (`POST /v1/setup` avec le
token de setup à usage unique, puis `POST /v1/auth/login`), l'identité et la tenancy, les
agents, l'access map lecture/écriture (`GET /v1/access-edges` ; son *drift* least-privilege
réconcilié est servi par le module access-map plutôt que par la surface de cœur), la
gestion des tokens et l'audit ledger.

Le contrat décrit **53 chemins de cœur**. C'est délibéré : c'est la surface stable et
versionnée du control plane, pas chaque route à laquelle le moteur peut répondre. Ce à quoi
« stable » s'engage — versioning, signalement de dépréciation et fenêtres de support
minimales — est la [politique de stabilité de l'API](/fr/reference/api-stability/).

:::note[Les routes de module forment un contrat bêta distinct]
Les routes de module — par exemple les `/v1/m/accessmap/graph`,
`/v1/m/accessmap/neighbors` et `/v1/m/accessmap/drift` du module access-map — ne font
**pas** partie du document stable de 53 chemins du cœur. Elles sont publiées dans un
document OpenAPI **bêta** distinct à [`/reference/api-beta/`](/reference/api-beta/)
(servi à `/openapi.beta.json` et reflété à partir des routes que les modules enregistrent
réellement), de sorte que la surface stable reste identifiable tandis que la surface
complète du produit demeure programmable. Bêta signifie que les formes peuvent changer
avec préavis (une fenêtre de support plus courte que stable) ; le détail au niveau des
champs vit toujours dans les interfaces Go et TypeScript typées. Le résultat least-privilege
de l'access map est la route `drift` ; il n'existe pas d'endpoint `diff` distinct.
:::

### Miroir gRPC (`olivares.api.v1`)

Le control plane expose aussi une surface **gRPC** — le service `ControlPlane` dans le
package proto versionné `olivares.api.v1`. C'est un **miroir ciblé et figé** d'un
sous-ensemble du contrat REST ci-dessus (server info, agent list/get/create, audit verify),
utilisé là où un contrat binaire typé est préféré (par exemple les collectors). Il reflète
le contrat REST plutôt que de l'étendre ; le document OpenAPI reste la surface canonique
pour l'API complète.

## Bus d'événements

La [référence du bus d'événements](/fr/reference/events/) est un contrat **AsyncAPI 3.0**. Le
bus est **in-process par défaut** — les connecteurs y hissent des observations normalisées
sous forme d'événements typés, et les modules et connecteurs de sortie s'abonnent **par type
d'événement** et réagissent, sans qu'aucun n'appelle l'autre directement. Un binding
distribué sur NATS est optionnel, non requis.

Le contrat est **dérivé à la main du SDK Go**, non généré : les définitions faisant autorité
sont l'enveloppe d'événement, les types d'événements first-party et les payloads
d'observation (les observations d'accès agent→ressource, les échantillons de coût et les
rapports de finding). Là où le bus ne formalise pas encore quelque chose, la référence le
dit plutôt que de l'inventer.

## Catalogue des modules

Le [catalogue des modules](/fr/reference/modules/overview/) énumère les **30 modules** qui
reposent sur le moteur de cœur, à travers neuf domaines de capacité. L'un des plus utiles
est l'**access map R/RW** avec son diff **Permitted-vs-Observed** : il lit depuis les logs,
OTEL et (comme backstop non coopératif) eBPF plutôt que de siéger dans le data path, et il
ne stocke que la relation *quel agent peut lire ou écrire quelle ressource* — jamais les
payloads, secrets ou PII.

Le catalogue est honnête sur le statut et la couverture. Chaque module porte sa propre
maturité — la plupart vivants et câblés de bout en bout, certains partiels ou opt-in.
L'observation passive est **classée par niveaux** selon le type de store — clean pour les
stores SQL, object et entrepôt ; lossy pour les document et vector stores ; impossible sans
coopération pour les stores en mémoire ou embarqués — et le catalogue marque où un module est
en phase de conception. Le registry de modèles propres et le fine-tuning sont une **capacité
prévue**, pas l'un des 30 modules livrés.

## CLI

La [référence CLI](/fr/reference/cli/) documente le binaire unique `olivares` et ses
sous-commandes. Celle que vous exécutez pour opérer le control plane est `serve`, qui
démarre les listeners HTTP (REST + UI web embarquée) et gRPC ; **TLS est activé par
défaut**. Les autres sous-commandes couvrent le collector, l'audit ledger (`verify`,
`checkpoint`, `export`), l'outillage de licence et l'émission du document OpenAPI.

:::caution[D'abord build, puis run]
Il n'y a pas de raccourci `task run` ni de `docker run` nu. Soit vous construisez et
invoquez le binaire directement — `task setup`, `task build`, puis `./bin/olivares serve`
—, soit vous le démarrez avec le fichier Compose fourni et lisez le token de setup à usage
unique depuis les logs. La page CLI liste les flags `serve` vérifiés et leurs défauts.
:::

## Configuration

La [référence de configuration](/fr/reference/configuration/) liste les variables
d'environnement et options runtime qui façonnent un déploiement. Les plus déterminantes sont
le répertoire de données (`OLIVARES_DATA_DIR`), le câblage des sources réelles (non-démo) lu
depuis `OLIVARES_SOURCES_CONFIG` avant le démarrage du moteur, et le sélecteur de moteur
d'autorisation `OLIVARES_PDP_ENGINE` (`cedar`, `opa` ou `none`).

Deux règles de conception traversent la surface de configuration. Une **source non
configurée avertit honnêtement** plutôt que de faire échouer le moteur. Et le seam
d'autorisation **ne fait jamais que restreindre, jamais élargir** : le RBAC est
deny-by-default, consulter l'access graph est une action privilégiée, et chacune de ces
lectures est auditée.
