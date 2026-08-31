---
title: Honnêteté et limites
description: >-
  Ce qu'Olivares AI fait aujourd'hui, ce qui est au stade de conception ou
  post-v1, et ce que le produit ne fait délibérément pas. Aucune capacité
  fabriquée.
---

Un control plane (plan de contrôle) pour l'IA est un produit de sécurité. S'il
exagère ce qu'il couvre, il donne un faux sentiment de sécurité — ce qui est
pire que pas d'outil du tout. Cette page est donc le contrat explicite sur **ce
qui tourne aujourd'hui, ce qui est prévu, et ce qui est délibérément hors
périmètre.** Le reste de la documentation s'y tient : les commandes des
tutoriels et des guides pratiques sont faites pour être exécutées telles
qu'écrites, et là où le produit ne couvre pas encore quelque chose, la page le
dit plutôt que de laisser entendre le contraire.

## Ce qui tourne aujourd'hui

- **Le binaire unique se construit, démarre et atteint un graphe d'accès
  peuplé.** Le binaire `olivares` se compile en un seul artefact statique avec
  l'interface web embarquée. Le démarrer avec l'estate de démonstration
  (`serve --seed-demo`) et parcourir *discover → graphe R/RW → dérive
  Permitted-vs-Observed → inventaire* est exercé **de bout en bout** par la
  suite de tests. Le [tutoriel](/fr/tutorials/zero-to-graph/) reproduit exactement
  ce parcours.
- **La configuration au premier lancement est sans identifiants.** Une
  installation neuve n'a **aucun identifiant par défaut** ; le moteur affiche un
  jeton de configuration à usage unique au premier démarrage.
- **L'API REST et l'audit ledger sont réels.** La
  [référence de l'API](/reference/api/) est rendue à partir du contrat OpenAPI
  3.1 du produit lui-même. L'audit ledger est en append-only (ajout seul) et
  hash-chained (chaîné par hachage) avec des checkpoints signés en Ed25519, et
  peut être exporté dans plusieurs formats SIEM.
- **Les versions sont signées et vérifiables hors ligne.** La signature, la
  provenance SLSA, le SBOM et l'OpenVEX peuvent tous être
  [vérifiés sans accès réseau](/fr/how-to/verify-a-release/), et le produit livre
  un [bundle air-gap](/fr/how-to/air-gap-install/). **Aucune version taguée n'existe
  encore**, ceci décrit donc ce qu'une version contiendra, et non un artefact que vous
  pouvez télécharger et vérifier aujourd'hui — la même réserve que celle de
  `SECURITY.md`.

## Open core — ce qui est ouvert vs entreprise

Le produit est en **open core** : le binaire par défaut (AGPL) est l'intégralité
de la plateforme de gouvernance, et une petite ligne commerciale **additive**
(`enterprise/`, construite uniquement avec `-tags enterprise`, jamais dans le
binaire public) regroupe les fonctionnalités réservées. Deux frontières comptent
pour l'usage quotidien, et le build ouvert y répond honnêtement plutôt que de les
feindre :

- **Le SSO est ouvert pour un IdP unique.** Le login mono-IdP — **OIDC**
  (Authorization Code + PKCE) et **SAML 2.0** (réponses signées, anti-rejeu) —
  tourne dans le binaire par défaut **sans** `-tags enterprise`. Faire tourner
  **plus d'un IdP actif** (par tenant / par domaine), l'**application du SSO**
  (exiger le SSO / bloquer le login par mot de passe) et le **SCIM géré** sont la
  ligne entreprise réservée ; activer un second IdP actif renvoie
  `multi_idp_requires_enterprise` — une limite produit explicite, jamais un faux
  501.
- **Il n'y a aucun plafond d'utilisateurs — les comptes sont illimités dans toutes les
  éditions.** Community, Business, les modules complémentaires et Enterprise
  auto-hébergé admettent tous un nombre illimité de comptes utilisateurs, quel que soit
  l'état de la licence : valide, expirée ou absente. Le plafond de trois comptes actifs
  antérieur au 2026-07-27 a été supprimé (la couture de sièges reste dans le code, en
  no-op de compatibilité qui ne refuse rien), et l'expiration d'une licence ne
  plafonne, ne désactive ni ne supprime jamais un compte. Le modèle commercial est un
  droit à durée déterminée sur les add-ons, jamais une facturation par siège.
- **Le reste de la plateforme est ouvert.** La boucle de gouvernance complète —
  inventaire, carte d'accès R/RW, politique RBAC/ABAC/Cedar, l'audit ledger
  scellé, FinOps, conformité, egress SIEM, MCP, HA/distribué — tourne dans le
  binaire ouvert sans aucune vérification de licence. Les add-ons `enterprise/`
  additifs (fédération multi-IdP, content firewall/DLP, durcissement des hooks,
  le catalogue compilé de threat-intel, l'egress des server-tools, le connecteur CyberArk
  Conjur et le close-loop d'incident) sont du nouveau
  code qui n'a jamais été dans le produit ouvert, pas des fonctionnalités
  retirées de celui-ci. La validation de licence dans le binaire ouvert est en
  **attestation seule** — elle n'active, ne désactive ni ne bloque jamais quoi
  que ce soit (voir
  [Open core & licence](/fr/explanation/open-core-and-licensing/)).

## Ce qui est au stade de conception ou pré-1.0

Olivares AI est **pré-1.0**. Les documents de conception du produit sont
explicites sur le fait qu'une grande partie de la plateforme est par endroits au
stade de conception, même là où le moteur tourne déjà. Considérez la profondeur
au niveau des modules comme **un travail en cours** sauf indication contraire
sur une page.

- **La couverture de la carte R/RW est par paliers, par conception.** La
  fidélité dépend de ce que la source peut prouver. Elle est **propre** sur les
  stores dotés d'un audit natif (SQL via pgAudit, stockage objet via CloudTrail,
  warehouses/lakes), **avec pertes** sur certains stores (document/vecteur), et
  **impossible à reconstruire passivement** sur d'autres (par ex. Redis, SQLite,
  D1) — là où lecture et écriture ne peuvent être distinguées, l'arête est
  marquée `unknown`. L'attribution est **ferme** quand une source porte une
  identité par agent et retombe à **`approximate`** quand un compte de service
  partagé la masque. Le produit le montre honnêtement ; il ne fabrique pas de
  certitude.
- **Les sources R/RW canoniques sont câblées dans le `serve` standard.** La
  racine de composition enregistre les observateurs de niveau hôte —
  `pgaudit`, `s3cloudtrail`, `ebpf`, `runtime` et la source d'introspection
  `mcp` — aux côtés des observateurs warehouse/lake
  (snowflake/databricks/bigquery/mssql/oracle/mongo/redshift/gcs/
  azure-blob/iceberg/openlineage/delta-sharing), tous configurables via
  `OLIVARES_SOURCES_CONFIG` (le
  [quickstart](/fr/start/quickstart/) câble une vraie source `pgaudit` contre le
  binaire standard et le smoke test l'affirme). Les **sources de documents** de
  connaissance (gdrive/confluence/notion/sharepoint/s3content) ne sont
  délibérément *pas* des sources runtime — elles sont chargées à la demande par
  les requêtes d'ingestion de connaissance. La
  [référence des connecteurs](/fr/reference/connectors/) marque chaque type.
- **Le défaut est un binaire unique ; le bus d'événements distribué existe et
  est honnête sur sa sémantique.** Le défaut tourne comme un seul binaire avec
  un bus d'événements **en processus**. Le **chemin de données collecteur
  distant → core est construit et livré** : des collecteurs edge exécutent les
  connecteurs de source localement et poussent les observations vers un core
  central sur du TLS mutuel à certificat client vérifié, sans listener entrant
  (le mode `collector`). Le **bus d'événements distribué** a été livré avec le
  travail de scale-out : un hybride qui conserve le fan-out en
  processus pour la livraison locale (backpressure bloquante, pas de perte
  locale) et fait passer les événements entre nœuds via **NATS**, activé par
  `OLIVARES_BUS_CONFIG` (une configuration de bus mal formée **fait échouer le
  démarrage** plutôt que de partitionner silencieusement le bus). La livraison
  entre nœuds est documentée honnêtement comme **at-most-once** (au plus une
  fois) — les déconnexions et les pertes du pont sont comptées dans des
  métriques dédiées, jamais silencieuses
  ([monitoring](/fr/how-to/monitor-with-prometheus/)).
- **L'*actuation* gouvernée a trois états honnêtes : live, à la demande et
  seam.** Le produit observe et gouverne largement aujourd'hui. Un petit
  ensemble d'actuations est **live dans le binaire par défaut** sans
  provisionnement : l'application des budgets FinOps (un budget appliqué à son
  plafond refuse la dépense), le transport de distribution des notifications
  (il route une fois une destination configurée), les findings/guardrails
  détectives de sécurité, et le runner de sandbox synthétique en processus
  (isolé par construction). Plusieurs autres sont **câblées à la demande** — le
  backend est construit et câblé, mais reste **deny-closed ou dégradé jusqu'à ce
  qu'un opérateur le provisionne** via la config d'environnement : le module VII
  (deploy) `apply`/`retire` (un `503` jusqu'à ce qu'un exécuteur soit
  provisionné), l'orchestration module IV *fire* et la distribution voix module
  XVI (toutes deux deny-closed jusqu'à ce qu'un dispatcher soit configuré), le
  runtime de sandbox/red-team isolé par l'OS (synthétique / DÉGRADÉ jusqu'au
  provisionnement), la récupération **sémantique** appuyée sur modèle (lexicale
  et publique uniquement par défaut), et l'*exécution* de modèle dans le module
  X (`503` jusqu'à ce qu'un identifiant d'inférence soit provisionné). Ce qui
  reste un **seam déclaré, deny-closed** sans aucun backend du tout est la sonde
  de télémétrie voix dormante (le bus d'événements distribué a quitté cette
  liste quand le pont NATS a été livré — voir ci-dessus). Le
  [catalogue des modules](/fr/reference/modules/overview/) marque le statut
  Govern/Observe et Actuate de chaque module ; rien ne prétend agir là où il ne
  le fait pas. (Ceci corrige une lecture antérieure qui listait la voix, le
  runtime de sandbox/red-team et la récupération sémantique comme « live » — ils
  sont à la demande : vérifié face à un démarrage standard `serve --seed-demo`,
  2026-06-08.)
- **L'air-gap s'applique au control plane, pas à l'inférence Claude.** Le
  control plane tourne entièrement self-hosted (auto-hébergé) et peut être
  air-gapped (isolé du réseau) (SQLite mononœud, version hors ligne signée,
  bundle air-gap). **Claude lui-même n'est pas auto-hébergeable** — Anthropic ne
  publie pas les poids — donc toute *inférence* Claude atteint l'API
  d'Anthropic, directement ou via Bedrock/Vertex/Foundry. « Air-gapped » signifie
  ici que le plan de *gouvernance et d'observation* et ses données restent à
  l'intérieur de votre périmètre ; cela ne signifie **pas** que Claude tourne
  hors ligne. Les modèles que vous auto-hébergez réellement (par ex. via
  vLLM/Ollama sous le module XXIII) peuvent tourner en air-gap ; les modèles
  frontier brokés ne le peuvent pas.
- **Les routes de module sont un contrat séparé, en bêta.** Les endpoints de
  module (par exemple le graphe d'access map et la dérive) ne font pas partie du
  contrat de cœur stable (53 chemins de cœur) ; ils sont publiés comme un document
  **bêta** séparé — la
  [référence des routes de module](/reference/api-beta/) (servie sur
  `/openapi.beta.json`). Bêta signifie que les formes peuvent changer avec
  préavis, et le détail au niveau des champs vit toujours dans les interfaces
  typées du produit. La [référence de l'API de cœur](/reference/api/) documente la
  surface stable ; ce n'est pas l'intégralité de la surface du produit.

## Ce que le produit ne fait délibérément **pas**

- **Aucune fonctionnalité offensive.** Olivares AI **n'est pas** un framework de
  command-and-control et ne scanne **pas** les identifiants d'autrui. L'access
  map est un puissant outil de reconnaissance *destiné aux défenseurs pour
  gouverner leur propre estate* — la consulter est une action privilégiée,
  scopée au tenant et entièrement auditée. Cette ligne défensive est
  intentionnelle et maintenue explicite (voir le
  [modèle de menace](/fr/explanation/security/threat-model/)).
- **Aucun forwarder S2S Splunk natif.** Le transfert vers Splunk est une
  *posture* documentée — pointez un Universal Forwarder vers un fichier auquel le
  control plane ajoute, ou poussez via Splunk HEC — **et non** un émetteur
  Splunk-to-Splunk natif. Le
  [guide pratique Splunk](/fr/how-to/forward-audit-to-splunk/) est explicite sur le
  flux dont il s'agit.
- **Aucun webhook sortant dans le contrat REST.** Le document OpenAPI ne définit
  aucun `webhooks`. Une livraison signée sortante existe en tant que *connecteur
  de destination* de notification interne, et l'endpoint SCIM
  Security-Event-Token est un récepteur *entrant* — aucun des deux n'est un
  webhook OpenAPI. Voir la [référence de l'API](/reference/api/).
- **Le fine-tuning de modèles (module XXIII) est post-v1.** Son absence est une
  décision, pas une lacune.

## Là où les docs notent une lacune en amont

Quelques éléments que cette documentation met en évidence sont des **lacunes du
produit**, signalées aux équipes qui possèdent le contrat concerné plutôt que
masquées ici :

- Le fichier OpenAPI commité que le site rend est désormais **régénéré à partir
  de — et vérifié par la CI octet par octet face à — le propre générateur du
  moteur**, de sorte qu'il ne traîne plus derrière lui (la lacune d'endpoint
  antérieure a été réconciliée). La sous-documentation antérieure de la liste de
  formats `/v1/audit/export` a également été corrigée en amont : le résumé et le
  message de bad-request sont désormais tous deux construits à partir du registre de
  formats du moteur (`audit.FormatList()`), et ne peuvent donc plus diverger — cette
  section conserve la trace parce que des éditions antérieures de ces docs ont signalé
  la lacune, et parce que la même pourriture avait également caché `leef` et `ocsf` de
  l'aide et de la complétion de la CLI jusqu'au 2026-07-25.
- Le chemin de **push** de l'audit ledger a été livré avec le travail
  d'interop SIEM/ITSM : un abonnement d'eventing `audit.recorded` active une
  pompe de ledger par tenant qui transfère les enregistrements scellés
  **at-least-once** (au moins une fois) vers un sink configuré (Splunk HEC,
  Sentinel, Datadog, New Relic, ou un webhook signé HMAC). L'export en **pull**
  reste la bonne forme pour l'archivage WORM et la re-vérification hors ligne.
  Voir [pousser vers votre SIEM](/fr/how-to/cookbook/push-to-siem/) et le
  [guide pratique Splunk](/fr/how-to/forward-audit-to-splunk/). Ce qui **n'existe**
  toujours pas est un émetteur de **protocole S2S** Splunk natif (ci-dessus).

Si vous trouvez une commande qui ne se comporte pas comme documenté, c'est un
bug dans les docs ou le produit — merci de le signaler.
