---
title: Stabilité, versionnage, dépréciation et retrait de l'API
description: >-
  Le schéma de versionnage, les paliers de stabilité, la signalisation de dépréciation
  (en-têtes RFC 9745 / RFC 8594) et les fenêtres de support minimales pour l'API REST,
  le miroir gRPC, le contrat filaire d'ingestion en direct, le provider Terraform et les
  SDK clients.
---

Cette page constitue le **contrat de stabilité** de tout ce qui programme contre le
control plane. Elle énonce ce qui est stable, comment une rupture de compatibilité est
signalée, et combien de temps une surface dépréciée continue de fonctionner. L'application
de ces règles se trouve dans la base de code, pas dans la prose : la table de dépréciation,
les en-têtes de réponse, les marqueurs OpenAPI et les vérifications de fenêtre ci-dessous
sont tous pilotés par une unique déclaration dans le code (`core/api/stability.go`), et un
retrait planifié plus tôt que la politique ne l'autorise **fait échouer le build**.

:::note[Statut pré-1.0]
Olivares AI est en pré-1.0 (voir [Honnêteté et limites](/fr/start/honesty-and-limits/)).
Les mécanismes de signalisation présentés sur cette page sont déjà actifs ; les **fenêtres
de support minimales s'appliquent à partir de la release 1.0/GA**. Jusque-là, la surface
publiée est maintenue stable en pratique, mais les fenêtres formelles ci-dessous sont
l'engagement que vous pourrez nous opposer à partir de la GA.
:::

## Surfaces couvertes et paliers

| Surface | Versionnée par | Palier aujourd'hui |
|---|---|---|
| Contrat REST cœur — les chemins du [document OpenAPI servi](/reference/api/) | majeur d'URL (`/v1/…`) | **stable** |
| Miroir gRPC — `ControlPlane` dans le package proto `olivares.api.v1` | majeur du package proto | **stable** (miroir figé) |
| Ingestion en direct / fil de connecteur — package proto `olivares.sdk.v1` | majeur du package proto + `ProtocolVersion` du plugin | **stable** (figé) |
| SDK de connecteur (Go) — modules `sdk`, `sdk/plugin` (surface d'auteur) | semver de module — tags `sdk/v*`, `sdk/plugin/v*` à partir de la première release publique | **stable v1** (contrat Go ; ligne filaire ci-dessus) |
| [Contrat du bus d'événements](/fr/reference/events/) (AsyncAPI 3.0) — ses types d'événements sont aussi ce que la plateforme d'eventing livre aux [abonnements webhook externes](/fr/reference/events/#abonnements-externes-plateforme-deventing) ; les routes de gestion des abonnements sont des routes de module (`/v1/m/eventing/`, hors contrat), mais chaque **type d'événement** porte son propre palier de stabilité issu du catalogue dans le code | `info.version` (`1.0.0-preview`) | **beta** (document) ; paliers par type pour les types d'événements |
| Provider Terraform | son propre semver (tags `terraform-provider-v*`) | **stable**, MAJEUR suit l'API v1 |
| SDK clients (Go / Java / Python / TypeScript) | leur propre semver ; le MAJEUR suit le majeur de l'API à partir de la GA | **beta** (packages pré-1.0) |
| Tout ce qui n'est pas listé — routes de module `/v1/m/<ns>/`, SCIM, fédération, internes | — | **hors contrat** |

**Paliers.** Une surface *stable* ne change pas de manière incompatible au sein de sa
version majeure ; la supprimer ou la modifier exige le processus de dépréciation ci-dessous.
Une surface *beta* peut encore changer de forme, mais bénéficie de la même signalisation et
d'une fenêtre plus courte. Une surface *hors contrat* (notamment les routes de module
délibérément placées en dehors du document OpenAPI — voir l'[aperçu de la référence](/fr/reference/))
ne porte aucune promesse de compatibilité ; ses contrats vivent dans les interfaces typées
livrées avec le produit.

Chaque opération du document OpenAPI porte un marqueur `x-stability` lisible par machine,
et le document lui-même renvoie à cette page dans `info.x-stability-policy`.

## Ce qui compte comme une rupture de compatibilité

Pour une surface stable, tout ce qui suit constitue une rupture et est soumis au processus
ci-dessous :

- supprimer ou renommer un chemin, une méthode, un champ de requête, un champ de réponse ou
  un `code` d'erreur ;
- changer le type ou la signification d'un champ, ou rendre obligatoire un champ de requête
  optionnel ;
- durcir l'authentification/autorisation au point qu'un appel auparavant valide échoue ;
- pour gRPC/protobuf : tout ce que `buf breaking` (jeu de règles FILE) rejette.

Ce qui suit **n'est pas** une rupture : ajouter des endpoints, ajouter des paramètres de
requête optionnels, ajouter des champs de réponse, ajouter de nouveaux codes d'erreur pour
de nouveaux modes de défaillance, et ajouter des en-têtes de réponse. Les clients doivent
tolérer les champs JSON inconnus.

## Versionnage

- **REST** est versionné dans l'URL : l'intégralité du contrat stable vit sous
  `/v1/`. Un changement incompatible est livré sous `/v2/` et `/v1/` entre en
  dépréciation — jamais de rupture sur place.
- **gRPC** est versionné par package proto : `olivares.api.v1` /
  `olivares.sdk.v1`. Un changement incompatible exige un nouveau majeur de package
  (`…v2`) ; les deux contrats sont protégés par `buf breaking` contre `main`
  (`task proto:breaking`).
- **Le provider Terraform** est publié indépendamment
  (tags `terraform-provider-v*`) ; son MAJEUR suit le majeur de l'API qu'il parle.
- **Les SDK clients** embarquent `API_VERSION` (le majeur de contrat à partir duquel ils
  ont été générés) et `SPEC_HASH` (l'instantané OpenAPI exact) — `APIVersion` et
  `SpecHash` en Go ; à partir de la GA leur MAJEUR suit le majeur de l'API.
- **Le SDK de connecteur** (le contrat Go contre lequel les connecteurs tiers se
  construisent) est versionné par des tags semver par module (`sdk/vX.Y.Z`,
  `sdk/plugin/vX.Y.Z`) et protégé par le même mur `buf breaking` sur son fil.
  Les interfaces qu'un auteur implémente ne gagnent jamais de méthodes au sein d'un majeur ;
  toute nouvelle capacité arrive sous forme de nouvelles interfaces optionnelles. La politique
  complète est livrée avec le module (`sdk/VERSIONING.md`) ; le cycle de vie de l'auteur est dans
  [Construire et livrer un connecteur](/fr/how-to/build-a-connector/).

## Processus de dépréciation et signalisation

Une dépréciation, c'est une entrée déclarée dans la table dans le code plus un guide
de migration ; tout le reste en découle mécaniquement.

1. **Annoncer.** L'entrée arrive avec sa date d'annonce et l'URL du guide de migration.
   À partir de ce moment, chaque réponse de la route dépréciée porte
   l'en-tête [RFC 9745](https://www.rfc-editor.org/rfc/rfc9745) et un lien vers
   le guide, et l'opération OpenAPI gagne `deprecated: true`,
   `x-deprecated-at` et `x-migration-guide` :

   ```http
   Deprecation: @1780272000
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="deprecation"
   ```

2. **Planifier le retrait.** Lorsque la date de retrait est engagée, les réponses
   ajoutent l'en-tête [RFC 8594](https://www.rfc-editor.org/rfc/rfc8594) (et la
   spec gagne `x-sunset-at`) :

   ```http
   Sunset: Thu, 01 Jun 2028 00:00:00 GMT
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="sunset"
   ```

3. **Supprimer** — au plus tôt à la date de retrait, normalement avec le majeur d'API
   suivant.

**Fenêtres de support minimales** (annonce de dépréciation → retrait) :

| Palier | Fenêtre minimale |
|---|---|
| stable | **24 mois** |
| beta | **12 mois** |

Ces fenêtres sont appliquées par des tests contre la table de déclaration : une entrée
dont le retrait viole la fenêtre de son palier, ou qui pointe vers une route qui
n'existe pas, ne se construit pas.

Pour **gRPC**, la dépréciation est exprimée avec l'option protobuf `deprecated`
(qui apparaît dans le code généré) plus les mêmes fenêtres ; les contrats filaires
sont par ailleurs figés et `buf breaking` rejette d'emblée les modifications incompatibles.

## Ce que voient les clients

- **Provider Terraform** — émet un WARN `tflog` (méthode, endpoint, dates,
  guide) une fois par méthode et chemin de requête uniques par exécution lorsqu'une réponse
  du control plane porte un signal de dépréciation (une route paramétrée dépréciée
  avertit une fois par ressource qu'elle touche), et envoie un `User-Agent` versionné
  pour que l'usage de clients dépréciés soit attribuable côté serveur.
- **SDK Go** — fait remonter une `DeprecationNotice` une fois par endpoint (par défaut : un
  avertissement `slog` ; à surcharger avec `WithDeprecationHandler`). Les opérations dépréciées
  portent des marqueurs Go `// Deprecated:`, de sorte que les éditeurs et `staticcheck`
  les signalent au moment du développement.
- **SDK Python** — un `DeprecationWarning` par endpoint (ou votre
  callback `on_deprecation`) ; les opérations dépréciées sont marquées dans les docstrings.
- **SDK TypeScript** — un `console.warn` par endpoint (ou votre
  callback `onDeprecation`) ; les opérations dépréciées portent un JSDoc `@deprecated`.

## Pour aller plus loin

- [Référence de l'API REST](/reference/api/) — le contrat stable lui-même
- [Utiliser les SDK clients](/fr/how-to/use-the-client-sdks/)
- [Construire et livrer un connecteur](/fr/how-to/build-a-connector/) — le contrat et le cycle de
  vie du SDK de connecteur
- [Gérer comme du code (Terraform)](/fr/how-to/manage-as-code/)
- [Module XIX — propre API + gérer-comme-du-code](/fr/reference/modules/xix-api-manage-as-code/)
- [Bus d'événements (AsyncAPI 3.0)](/fr/reference/events/)
- [Honnêteté et limites](/fr/start/honesty-and-limits/)
