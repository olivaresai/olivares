---
title: "Module XIX — l'API propre et la surface manage-as-code"
description: >-
  La surface fondatrice du moteur : chaque action du control plane via une seule API
  REST/gRPC, plus un provider Terraform pour que le control plane lui-même soit déclaré et
  versionné. Quel est le contrat de l'API, ce que gère le provider, et les
  limites honnêtes de chacun.
---

Le module XIX n'est pas une fonctionnalité greffée sur le moteur — il **est** la surface du moteur.
Tout autre module atteint le monde extérieur via la même API first-party, et
l'interface web est une couche de présentation sur ce contrat exact, pas un contrat parallèle. Cette page
est la référence de ce que cette surface expose aujourd'hui et de la façon de gérer le control
plane sous forme de code, avec ses frontières réelles.

## Le contrat de l'API

Le moteur parle une seule API REST sous `/v1` (routeur chi, `http.Server` durci) et un
**miroir gRPC focalisé et gelé** de celle-ci (`olivares.api.v1` : info serveur, lecture/création
d'agent, vérification d'audit, plus le service de santé standard). gRPC est un sous-ensemble délibéré,
pas une parité complète — les nouveaux endpoints atterrissent d'abord en REST. Les deux câbles exécutent la **même**
chaîne `authenticate → resolve-tenant → authorize` et mappent les erreurs de façon identique, de sorte qu'un
not-found est indiscernable d'une ressource inter-tenant sur l'un ou l'autre fil.

La surface REST est publiée sous forme de **contrat OpenAPI 3.1** rendu sur la
[référence de l'API](/reference/api/) directement depuis le schéma rédigé du produit. Ce
document est le contrat de référence de la surface stable du cœur ; les routes de module
sont publiées séparément dans un document **bêta** — la
[référence des routes de module](/reference/api-beta/) (voir les limites honnêtes ci-dessous). La même
fonctionnalité est aussi pilotable depuis le terminal — voir la
[référence CLI](/fr/reference/cli/) — car la CLI est le moteur, pas un wrapper par-dessus.

## Authentification et le fil

L'authentification se fait par **jetons bearer opaques côté serveur**, pas par JWT.
Un jeton est préfixé par finalité
(session vs. clé d'API) ; le serveur persiste uniquement un sélecteur public et un SHA-256 du
secret, et compare le secret en temps constant. Les conséquences qui comptent pour un
workflow manage-as-code : les jetons sont **immédiatement révocables**, ne portent **aucune revendication ni
secret**, et n'ajoutent aucune surface d'attaque de crypto-parsing. Un jeton d'API est lié à un
`(tenant, role)` ou est un identifiant système non lié ; une requête dont l'en-tête de tenant
est en désaccord avec un jeton lié est refusée, jamais silencieusement élargie.

## Manage-as-code : le provider Terraform

Le provider `terraform-provider-olivares` est un **module Go séparé** et un pur client
REST — il n'importe jamais le noyau du moteur ni le SDK des connecteurs, gardant le vaste
arbre de dépendances du provider hors de la chaîne d'approvisionnement du noyau. Configuré avec un endpoint, un
jeton d'API sensible et un tenant optionnel, il gère un ensemble délibérément réduit et déclaré
d'objets :

| Kind | Nom | Gère |
|---|---|---|
| resource | `olivares_agent` | la définition catalogue d'un agent (CRUD complet + import) |
| resource | `olivares_policy` | une déclaration de policy de gouvernance |
| resource | `olivares_agent_identity_binding` | la liaison d'un agent à une identité non humaine |
| resource | `olivares_deployment` | une **définition** de déploiement (état désiré, déclaratif) |
| data source | `olivares_policies` / `olivares_identities` | des vues en lecture seule du roster gouverné |
| data source | `olivares_access_edges` | la carte d'accès R/RW et son drift permis-vs-observé |
| data source | `olivares_deployment` / `olivares_server_info` | une définition de déploiement ; les métadonnées du moteur |

Ce sont les **seules** resources et data sources que le provider sert. Déclarer un
`olivares_deployment` enregistre l'état désiré dans le control plane — cela ne touche **pas**
l'infrastructure ; le chemin apply appartient au [module VII](/fr/reference/modules/vii-deploy/)
et est une jointure deny-closed.

:::caution[Limites honnêtes]
- **`olivares_deployment` déclare ; il ne déploie pas.** La resource écrit une
  *définition* de déploiement via les routes du module VII. L'`apply`/`retire` live contre une vraie
  infrastructure est une **jointure deny-closed qui retourne `503`** jusqu'à ce qu'un opérateur
  provisionne un exécuteur — déclarer un déploiement en HCL ne mute jamais votre estate.
- **L'OpenAPI stable n'est pas tout le fil.** La surface stable du cœur figure dans le
  contrat publié (`/openapi.json`) ; les routes de module (par exemple les lectures de
  carte d'accès et de drift, ainsi que les routes de gouvernance et de déploiement
  qu'utilise le provider) sont publiées dans un document **bêta** distinct
  (`/openapi.beta.json`, la [référence des routes de module](/reference/api-beta/)).
  Leurs formes au niveau des champs vivent dans les interfaces typées du produit, pas
  dans le schéma stable.
- **gRPC est un sous-ensemble gelé, pas l'API complète.** Il reflète quelques opérations de lecture/création et
  d'audit pour l'automatisation first-party ; ne supposez pas qu'un endpoint existe sur gRPC parce
  qu'il existe sur REST.
- **La surface du provider est petite à dessein.** Quatre resources et cinq data sources —
  pas toute l'API en IaC. Tout ce qui est en dehors de cet ensemble est géré via REST/CLI, pas
  déclaré en HCL aujourd'hui.
- **La licence est une attestation, jamais une barrière de fonctionnalité.** Le produit est entier sous sa
  licence ; la vérification de licence hors ligne enregistre uniquement le détenteur et le statut et ne
  désactive, dégrade ou bloque jamais aucune requête d'API ni aucun démarrage.
:::

## Sécurisé par défaut

Le moteur de service est sécurisé par défaut : TLS est activé (un certificat auto-signé est généré au
premier démarrage si aucun n'est fourni), le bind est par défaut sur localhost, et écouter localement n'est
**pas** une exemption d'autorisation. Une installation fraîche n'a aucun identifiant — elle génère une
clé de configuration unique sur stdout et refuse chaque endpoint protégé jusqu'à ce que le premier
administrateur soit créé. L'audit est en ajout seul et à chaînage de hachage, avec des points de contrôle
signés Ed25519 qui rendent la réécriture de l'historique avant un point de contrôle cryptographiquement détectable.

## La plateforme d'événements (la moitié sortante du module XIX)

Depuis la livraison de la plateforme d'événements (`modules/eventing`), la surface du module XIX inclut aussi des
**abonnements aux événements en self-service par tenant** : des abonnements typés sur le
catalogue d'événements du bus (`edge.observed`, `cost.sampled`, `finding.reported`,
`audit.recorded`, …) avec une **diffusion durable at-least-once** — réessais avec backoff,
une file de lettres mortes (dead-letter queue), et un rejeu depuis un curseur — vers un webhook signé HMAC ou un
[sink SIEM](/fr/how-to/cookbook/push-to-siem/). Le module notify
([XV](/fr/reference/modules/xv-notify/)) reste le *routeur* d'alertes vers des destinations
provisionnées par l'opérateur ; eventing est la plateforme orientée intégrateur.
Un **export de posture** en lecture seule l'accompagnant (`modules/posture-export`) permet à une tour de
contrôle d'interroger la posture de vérité de terrain du produit — graphe d'accès, drift, inventaire,
findings — uniquement sous forme de refs/hachages/relations, l'export lui-même étant audité.

## Liens connexes

- [Référence de l'API](/reference/api/) — le contrat OpenAPI 3.1 rendu pour la surface centrale.
- [Politique de stabilité de l'API](/fr/reference/api-stability/) — versionnement, signalement de dépréciation/sunset et fenêtres de support pour cette surface.
- [Utiliser les SDK clients](/fr/how-to/use-the-client-sdks/) — les clients first-party Go/Python/TypeScript.
- [Référence CLI](/fr/reference/cli/) — la même fonctionnalité depuis le binaire `olivares`.
- [Gérer le control plane sous forme de code](/fr/how-to/manage-as-code/) — le guide du provider Terraform.
- [Module VII — déploiement](/fr/reference/modules/vii-deploy/) — où `olivares_deployment` actionne (la jointure `503`).
- [Catalogue des modules](/fr/reference/modules/overview/) — la séparation Gouverner/Observer vs Actionner.
- [Honnêteté & limites](/fr/start/honesty-and-limits/) — ce qui actionne aujourd'hui et ce qui n'actionne pas.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — la couche moteur sur laquelle repose cette surface.
