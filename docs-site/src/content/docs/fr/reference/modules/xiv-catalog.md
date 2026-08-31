---
title: "Module XIV — catalogue interne & marketplace"
description: >-
  Le registre interne et curé des agents, serveurs MCP, skills, templates,
  modèles et connecteurs tiers approuvés pour l'organisation. Comment une entrée est
  versionnée, gelée, épinglée par hachage et signée à l'approbation, comment l'instanciation
  en self-service est gouvernée, et les limites.
---

Le module XIV est le **catalogue interne** de l'organisation — un registre curé et gouverné
des agents, serveurs MCP, skills, templates, modèles et connecteurs tiers
qui ont été **approuvés pour la réutilisation**
à travers l'entreprise. Il existe pour qu'un estate se standardise sur des capacités versionnées et
validées plutôt que sur des copies ad-hoc, et pour qu'« approuvé » signifie quelque chose de vérifiable
plutôt qu'un mot dans un wiki. Il se situe dans la couche Intelligence et n'a **aucune surface
d'actionnement** : il cure et enregistre, tandis que le provisionnement a lieu ailleurs.

## Ce que c'est

Le catalogue est un **registre**, pas un magasin de documents. Une **entrée** est une définition curée,
versionnée, d'une capacité réutilisable, de kind `agent`, `mcp`, `skill`,
`template`, `model` ou `connector`. Chaque `(kind, slug, version)` est son **propre artefact immuable** —
publier une nouvelle version crée une nouvelle entrée, et l'approbation et la signature se font **par
version**. Une entrée traverse un cycle de vie fixe :

`draft → pending → approved → deprecated`

Seul un **draft** est mutable ; **l'approbation le gèle**. La spec d'une entrée est une
*définition* rédigée par l'opérateur — transport, références de modèle et de prompt, portée, et
**références** vers des secrets — jamais une valeur d'identifiant. Le chemin de création/approbation refuse une
spec qui porte des identifiants en ligne, de sorte que le module stocke des définitions, des références et des
métadonnées de gouvernance, et jamais de secrets ni de payloads.

## Versionnement, gel et signature

L'approbation est l'endroit où « approuvé » devient vérifiable :

- **Hachage de contenu.** À l'approbation, l'entrée est épinglée par un **hachage de contenu SHA-256** sur
  sa préimage canonique, sérialisée de façon déterministe. Chaque champ rédigé par l'opérateur est
  couvert, de sorte que toute mutation ultérieure d'une entrée approuvée est **détectable** — les altérations
  restent détectables même sans signature.
- **Attestation au registre.** L'approbation est enregistrée dans le registre d'audit en ajout seul, à
  chaînage de hachage, attribuée au **principal réel** qui l'a approuvée.
- **Signature Ed25519.** Lorsqu'une clé de signature de catalogue est provisionnée, l'approbation produit aussi
  une **signature Ed25519 détachée** sur le hachage de contenu, portant la clé publique
  et une empreinte courte — « approuvé = vérifiable ». La clé de signature est chargée ou
  générée au démarrage sous la jointure de clé fail-closed du moteur, **indépendante de** la clé du registre
  d'audit ; le module possède sa clé de catalogue et n'atteint jamais le signataire d'audit interne du
  moteur, gardant la frontière de confiance propre.

La vérification recalcule le hachage et, lorsqu'une clé est configurée sur le nœud, traite la
signature comme l'**ancre de confiance** : une signature dépouillée (downgrade) ou faite par toute
autre clé (substitution) est rapportée **non vérifiée**. `GET …/pubkey` indique si
la signature est activée ; l'état `verified` / `signed` / `signed_by` par entrée est retourné par
les routes d'entrée et de vérification.

## Connecteurs tiers vérifiés

Une entrée `connector` cure un **plugin connecteur tiers publié** — un
binaire compilé ou un artefact OCI. Sa spec enregistre ce qu'elle cure : le `sha256` de l'artefact
(`artifact_digest`), la référence release/OCI, l'éditeur et le nom du
descripteur du connecteur. L'entrée est le **registre de certification** côté tenant de
l'écosystème de connecteurs externes : « approuvé » peut être amené à signifier « son
attestation de chaîne d'approvisionnement a été vérifiée », et pas seulement « quelqu'un a cliqué sur approuver ».

Le flux reflète la paire d'admission des entrées MCP, avec ses propres enregistrements de policy et de verdict
(les preuves sont comptées par kind, de sorte que les verdicts de connecteurs ne partagent jamais de tables
avec les verdicts MCP) :

- `GET`/`PUT …/connector-admission/policy` — la racine de confiance par tenant :
  `require_signed`, `require_subject_digest` optionnel, épinglages d'identité/émetteur
  Sigstore, clés publiques nues, racines CA, et l'**allow-list de prédicats** in-toto
  (par défaut SLSA provenance v1/v0.2 et SBOM SPDX/CycloneDX — formes de provenance et
  de SBOM, car un connecteur est un artefact compilé, pas des poids de modèle). Aucune
  policy signifie le **mode observe** — rien n'est mis sous barrière jusqu'à ce que le tenant opte pour, et
  l'endpoint de policy le dit honnêtement.
- `POST …/entries/{id}/admit` — une route partagée unique, dispatchée par kind d'entrée
  (`mcp` ou `connector`) : vérifie un bundle d'attestation Sigstore fourni par l'opérateur
  et enregistre un **verdict revendiqué-vs-vérifié** par entrée. Lorsque la
  requête n'épingle pas d'`expected_digest`, la liaison **dérive par défaut du
  `spec.artifact_digest` de l'entrée** — l'entrée nomme l'artefact qu'elle cure, de sorte que
  l'admission se lie à cet artefact sauf surcharge explicite. Un bundle malformé
  est un `400` ; un bundle bien formé qui échoue à la vérification est un **verdict négatif
  enregistré**, pas une erreur.
- `GET …/connector-admissions` — les verdicts enregistrés, filtrables par entrée
  (`entry_ref`) et restreignables aux verdicts vérifiés (`verified=true`).
- **Barrière d'approbation deny-closed.** Avec `require_signed` activé, une entrée connecteur ne peut
  être approuvée (et donc listée comme connecteur *vérifié*) qu'avec un verdict d'admission de
  provenance/SBOM vérifié **lié au digest que l'entrée cure actuellement**
  (`spec.artifact_digest`) ; avec `require_subject_digest` activé, cette liaison
  d'artefact doit elle-même être confirmée. Éditer le digest curé après une
  admission invalide la barrière — une ré-admission par rapport au nouvel artefact est
  requise.

:::caution[Limites honnêtes]
Le catalogue **certifie**, il n'exécute pas : la barrière côté hôte qui décide
si un plugin connecteur peut réellement *s'exécuter* vit dans le control plane, pas
ici. Les bundles d'attestation sont fournis par l'opérateur (`cosign download attestation` /
`gh attestation download`) — les récupérer depuis les referrers OCI est une étape
externe, et l'**inclusion** dans le journal de transparence Rekor n'est pas vérifiée nativement (le
verdict enregistre la présence du matériel et dit exactement ce qui a été vérifié).
:::

## Instanciation en self-service gouvernée

Une **instance** est une requête en self-service pour instancier une entrée **approuvée** — seule une
entrée approuvée peut être instanciée. Le module enregistre la requête, sa **provenance**
(de quelle version d'entrée elle provient), sa cible et son statut de gouvernance, et applique une
machine à états saine (`requested → approved`/`rejected → active`). Il ne décide **pas**
qui peut approuver, et ne provisionne pas : la **décision** d'approbation appartient à la gouvernance
et le provisionnement réel au déploiement. Approuver, déprécier, signer et
instancier sont des actions **privilégiées, sous barrière RBAC et auto-auditées** au principal réel.

:::caution[Limites honnêtes]
- **Aucun actionnement, aucun provisionnement.** Le module XIV enregistre et gouverne la *requête* ; il
  ne met jamais une capacité en place. La décision d'approbation est celle de la gouvernance et le câblage est
  celui du déploiement — et l'`apply`/`retire` live là-bas est lui-même une jointure deny-closed (`503`
  jusqu'à ce qu'un exécuteur soit provisionné). Voir [Honnêteté & limites](/fr/start/honesty-and-limits/).
- **La signature est réelle mais dépendante de la clé.** La signature Ed25519 est implémentée et la clé de
  signature est provisionnée au démarrage, activée par défaut. Sur un nœud avec **aucune clé configurée** (ou une
  clé invalide), une entrée approuvée est **épinglée par hachage et attestée au registre mais non signée** —
  l'API le dit honnêtement via `signing_enabled`/`signed` plutôt que d'impliquer qu'une
  signature existe.
- **Curé, pas observé.** Le catalogue **ne** s'abonne **pas** au bus d'événements et n'émet pas dessus ;
  il est peuplé par des personnes via son API, pas dérivé d'observations live. Il
  asserte ce que l'organisation a *approuvé pour la réutilisation*, pas ce qui s'exécute actuellement.
- **Le module n'applique pas la policy d'approbation.** Il applique la machine à états et
  les paliers de verbes RBAC ; *qui* peut approuver et sous quelles conditions est décidé par la gouvernance.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module XIV et la
  séparation gouverner/observer vs actionner.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — le flux d'approbation human-in-the-loop.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur, les couches et
  les entrées du modèle de données partagé que les modules déclarent.
- [Référence du bus d'événements](/fr/reference/events/) — le bus que ce module ne consomme délibérément pas.
- [Honnêteté & limites](/fr/start/honesty-and-limits/) — la posture d'actionnement à travers le produit.
