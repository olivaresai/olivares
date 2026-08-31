---
title: Référence gRPC — services, méthodes et types de messages
description: >-
  Chaque rpc enregistré par le moteur Olivares AI et l'hôte de plugins, avec sa forme
  de streaming, ses messages de requête et de réponse et la chaîne de méthode complète
  utilisée. Généré depuis les tables d'enregistrement des serveurs eux-mêmes.
---

Olivares AI parle gRPC à deux endroits, dans des directions opposées :

- **L'API de control plane du moteur** (`olivares.api.v1.ControlPlane`) — un petit miroir
  de la surface REST pour les appelants qui préfèrent un stub typé. Le contrat REST de la
  [référence de l'API](/reference/api/) reste le plus large des deux.
- **Le contrat filaire des plugins** (`olivares.sdk.v1.*`) — le contrat versionné parlé par
  chaque connector et module out-of-process. C'est celui que vous implémentez lorsque vous
  [construisez un connector](/fr/how-to/build-a-connector/) dans un autre langage que Go.

Cette page est **générée depuis les tables d'enregistrement que les serveurs remettent à
gRPC**, et non depuis les fichiers `.proto`. Cette distinction est intentionnelle : un
`.proto` modifié sans régénération décrit un service que le binaire ne sert pas, et le contrôle
qui sous-tend cette page signale ce désaccord au lieu de publier la version la plus élégante.
Une méthode listée ici est une méthode qu'un client peut appeler.

:::note[Stabilité]
Le contrat de plugins `olivares.sdk.v1` est versionné et protégé par le détecteur de changements
incompatibles de buf : un changement incompatible exige un nouveau package major. Notre
engagement et sa durée sont décrits dans [Stabilité de
l'API](/fr/reference/api-stability/).
:::

## Transport et authentification

Toutes les méthodes des services ci-dessous, sauf `GetServerInfo`, exigent un principal
authentifié et autorisé. Deux exemptions sont délibérées et nommées ici plutôt que de vous
laisser les découvrir : `GetServerInfo` répond anonymement, et le service standard
`grpc.health.v1.Health` (`Check`, `List`, `Watch`) est servi sur le même listener sans
principal, car une sonde ou un service mesh doit pouvoir l'atteindre sur chaque pod exactement
comme un kubelet atteint `/livez`. L'absence de bearer token laisse une requête anonyme au lieu
de la rejeter ; un token présent mais invalide est rejeté. Le service de control plane est
accessible sur le listener gRPC du moteur ; les services de plugins sont appelés par le broker
go-plugin (connectors dans l'hôte) ou via gRPC avec TLS mutuel (collector distant). Configurez
le listener avec les variables `OLIVARES_*` de la
[référence de configuration](/fr/reference/configuration/).

<!-- BEGIN GENERATED olivares-grpc-reference — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Le moteur et l'hôte de plugins enregistrent **28 rpc** dans **7 services**. Les tableaux
ci-dessous sont lus dans les tables d'enregistrement générées que les serveurs remettent à
gRPC ; une méthode listée ici est donc une méthode qu'un client peut appeler.

### `olivares.api.v1.ControlPlane`

Défini dans `apiv1/api.proto` ; 5 rpc.

| Méthode | Méthode complète | Type | Requête | Réponse | Fonction |
|---|---|---|---|---|---|
| `CreateAgent` | `/olivares.api.v1.ControlPlane/CreateAgent` | unary | `CreateAgentRequest` | `Agent` | Enregistre un nouvel agent dans l'inventaire et renvoie l'enregistrement stocké, y compris l'identifiant utilisé par le reste de l'API. |
| `GetAgent` | `/olivares.api.v1.ControlPlane/GetAgent` | unary | `GetAgentRequest` | `Agent` | Renvoie un agent par identifiant, avec les mêmes champs que l'endpoint REST d'inventaire. |
| `GetServerInfo` | `/olivares.api.v1.ControlPlane/GetServerInfo` | unary | `Empty` | `ServerInfo` | Indique la version, l'édition et l'état de readiness. C'est la seule méthode de ce service qui n'exige pas de principal authentifié. |
| `ListAgents` | `/olivares.api.v1.ControlPlane/ListAgents` | unary | `ListAgentsRequest` | `ListAgentsResponse` | Liste page par page les agents visibles par le principal appelant. |
| `VerifyAudit` | `/olivares.api.v1.ControlPlane/VerifyAudit` | unary | `VerifyAuditRequest` | `VerifyAuditResponse` | Revérifie la chaîne d'audit sur une plage et indique si les hachages restent liés, y compris l'état du checkpoint. |

### `olivares.sdk.v1.ContentSourceService`

Défini dans `olivaresv1/v1.proto` ; 7 rpc.

| Méthode | Méthode complète | Type | Requête | Réponse | Fonction |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.ContentSourceService/Close` | unary | `Empty` | `Empty` | Termine la session ouverte par Open et libère ce que le connector conservait pour elle. |
| `DeltaList` | `/olivares.sdk.v1.ContentSourceService/DeltaList` | server-streaming | `ContentDeltaRequest` | `ContentChange` (stream) | Diffuse les changements depuis un curseur. Appelée seulement lorsque le connector annonce la capacité content.delta. |
| `Describe` | `/olivares.sdk.v1.ContentSourceService/Describe` | unary | `Empty` | `DescribeResponse` | Renvoie le descripteur du connector : son identité, ses champs de configuration et les capacités qu'il annonce. |
| `Fetch` | `/olivares.sdk.v1.ContentSourceService/Fetch` | unary | `ContentFetchRequest` | `ContentDocument` | Renvoie le corps et les métadonnées d'un document pour la référence choisie par l'hôte dans le stream List. |
| `FetchACL` | `/olivares.sdk.v1.ContentSourceService/FetchACL` | unary | `ContentFetchRequest` | `ContentACLResult` | Renvoie les références de permissions qui gouvernent un document. Un résultat vide signifie que la valeur par défaut de la base de connaissances s'applique. |
| `List` | `/olivares.sdk.v1.ContentSourceService/List` | server-streaming | `ContentListRequest` | `ContentDocRef` (stream) | Diffuse les références de documents page par page, bornées par les plafonds transmis par l'hôte afin qu'un corpus ne puisse pas être chargé en mémoire en un seul appel. |
| `Open` | `/olivares.sdk.v1.ContentSourceService/Open` | unary | `OpenRequest` | `Empty` | Démarre une session avec la configuration fournie par l'hôte, avant tout appel de contenu. |

### `olivares.sdk.v1.HostService`

Défini dans `olivaresv1/v1.proto` ; 3 rpc.

| Méthode | Méthode complète | Type | Requête | Réponse | Fonction |
|---|---|---|---|---|---|
| `Log` | `/olivares.sdk.v1.HostService/Log` | unary | `LogRecord` | `Empty` | Écrit un enregistrement de log structuré par le moteur, afin qu'un module out-of-process journalise au même endroit qu'un module in-process. |
| `Publish` | `/olivares.sdk.v1.HostService/Publish` | unary | `Event` | `Empty` | Publie un événement sur le bus du moteur pour le compte d'un module out-of-process. |
| `Subscribe` | `/olivares.sdk.v1.HostService/Subscribe` | server-streaming | `SubscribeRequest` | `Event` (stream) | Diffuse les événements du bus au module, filtrés selon les types demandés. Un filtre vide signifie tous les types. |

### `olivares.sdk.v1.IngestService`

Défini dans `olivaresv1/v1.proto` ; 1 rpc.

| Méthode | Méthode complète | Type | Requête | Réponse | Fonction |
|---|---|---|---|---|---|
| `Push` | `/olivares.sdk.v1.IngestService/Push` | client-streaming | `IngestEnvelope` (stream) | `IngestSummary` | Accepte un stream d'observations envoyées par un daemon collector, élève chacune sur le bus d'événements et renvoie un résumé à la fin du stream. |

### `olivares.sdk.v1.ModuleService`

Défini dans `olivaresv1/v1.proto` ; 4 rpc.

| Méthode | Méthode complète | Type | Requête | Réponse | Fonction |
|---|---|---|---|---|---|
| `Describe` | `/olivares.sdk.v1.ModuleService/Describe` | unary | `Empty` | `DescribeResponse` | Renvoie le descripteur du module : son identité et la configuration qu'il accepte. |
| `Init` | `/olivares.sdk.v1.ModuleService/Init` | unary | `InitRequest` | `Empty` | Remet sa configuration au module et lui permet de se préparer avant tout démarrage. |
| `Start` | `/olivares.sdk.v1.ModuleService/Start` | unary | `Empty` | `Empty` | Démarre le travail du module après un Init réussi. |
| `Stop` | `/olivares.sdk.v1.ModuleService/Stop` | unary | `Empty` | `Empty` | Arrête le module et lui permet de libérer ce qu'il détient. |

### `olivares.sdk.v1.OutputService`

Défini dans `olivaresv1/v1.proto` ; 4 rpc.

| Méthode | Méthode complète | Type | Requête | Réponse | Fonction |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.OutputService/Close` | unary | `Empty` | `Empty` | Termine la session ouverte par Open et libère ce que le connector conservait pour elle. |
| `Describe` | `/olivares.sdk.v1.OutputService/Describe` | unary | `Empty` | `DescribeResponse` | Renvoie le descripteur du connector : son identité, ses champs de configuration et les capacités qu'il annonce. |
| `Notify` | `/olivares.sdk.v1.OutputService/Notify` | unary | `NotifyRequest` | `NotifyResponse` | Livre une notification à la destination et indique ce que celle-ci en a fait, ce qui détermine si l'hôte réessaie. |
| `Open` | `/olivares.sdk.v1.OutputService/Open` | unary | `OpenRequest` | `Empty` | Démarre une session avec la configuration fournie par l'hôte, avant toute livraison. |

### `olivares.sdk.v1.SourceService`

Défini dans `olivaresv1/v1.proto` ; 4 rpc.

| Méthode | Méthode complète | Type | Requête | Réponse | Fonction |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.SourceService/Close` | unary | `Empty` | `Empty` | Termine la session ouverte par Open et libère ce que le connector conservait pour elle. |
| `Describe` | `/olivares.sdk.v1.SourceService/Describe` | unary | `Empty` | `DescribeResponse` | Renvoie le descripteur du connector : son identité, ses champs de configuration et les capacités qu'il annonce. |
| `Gather` | `/olivares.sdk.v1.SourceService/Gather` | server-streaming | `Empty` | `Observation` (stream) | Diffuse les observations vers l'hôte, qui élève chacune sur le bus d'événements. Le stream se termine à la fin d'une exécution par lot ou lorsque l'hôte l'annule. |
| `Open` | `/olivares.sdk.v1.SourceService/Open` | unary | `OpenRequest` | `Empty` | Démarre une session avec la configuration fournie par l'hôte, avant la collecte de toute observation. |

<!-- END GENERATED olivares-grpc-reference -->

## Forme des messages

Les tableaux nomment chaque message de requête et de réponse ; leurs champs sont déclarés dans
les fichiers `.proto` indiqués avec chaque service. Ces fichiers sont livrés dans le dépôt et
servent de source à la génération des stubs. Deux conventions sont à connaître avant de les
lire :

- **Les champs de vocabulaire sont des chaînes, pas des enums fermés** — mode d'accès,
  source du signal, confiance, sévérité et type d'événement. Un connector tiers peut introduire
  sa propre source de signal sans attendre une version du SDK.
- **Les formes de payload sont fermées.** Le payload d'une `Observation` ou d'un `Event` est
  un `oneof` des types de messages connus, plus un fallback JSON pour les payloads d'événements
  définis par les modules. Un payload non reconnu est une erreur de contrat ; il n'est pas
  ignoré silencieusement.

## Générer un client

Les fichiers `.proto` constituent le contrat. Pointez la chaîne d'outils protobuf de votre
langage vers `sdk/plugin/proto/olivaresv1/v1.proto` pour le contrat des plugins, ou vers
`core/api/proto/apiv1/api.proto` pour le miroir du control plane. Les clients prêts à l'emploi
pour Go et TypeScript sont décrits dans [Utiliser les SDK clients](/fr/how-to/use-the-client-sdks/).
