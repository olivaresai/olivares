---
title: "Connecter une source"
description: "Câblez une véritable source d'observation dans le control plane, comprenez le modèle de connecteur, et choisissez le bon signal par système."
---

Cette page explique le modèle de connecteur général et comment câbler une véritable source dans le moteur. Si vous voulez seulement connecter un agent de codage, commencez par [Connecter Claude Code](/fr/how-to/connect-claude-code/) — c'est une source spécifique sur le chemin coopératif, et cette page est le modèle qui la sous-tend.

## Le modèle de connecteur

Une source fait un seul travail : elle **observe** un système externe et **émet des observations normalisées**. Elle ne se place jamais dans le chemin de données, ne proxyfie jamais le trafic, et ne lit jamais les payloads. Le R/RW access map est construit à partir de ce que la source rapporte, pas de l'interception de ce qui transite.

Concrètement, une source implémente une petite interface — `Open` (configurer une fois), `Gather` (s'exécuter, en émettant), `Close` (libérer) — et pendant `Gather` elle remet au moteur une observation à la fois à travers un sink. Le moteur possède l'ordonnancement : une source en flux (un tail de log, un récepteur) se bloque dans `Gather` et émet jusqu'à son annulation ; une source par lots fait son travail et retourne, et le moteur décide quand la relancer. Le connecteur ne possède jamais son propre minuteur.

Il existe exactement trois sortes d'observations qu'une source peut émettre :

| Observation | Ce qu'elle transporte | Utilisée par |
|---|---|---|
| `edge` | Une origine (agent / identité / session) a touché une ressource, avec un mode lecture/écriture | Le R/RW access map |
| `cost` | Coût d'usage modèle/fournisseur | FinOps |
| `finding` | Un finding de guardrail / red-team / forensique | Sécurité |

L'ensemble est fermé par conception — un tiers ne peut pas introduire une nouvelle sorte d'observation. Le moteur **relève** chaque observation émise sur le bus d'événements in-process, où les modules la consomment sans se coupler à la source qui l'a produite. Pour l'access map en particulier, le moteur résout les références sous forme de chaînes du connecteur en entités et fusionne l'observation dans une arête d'accès persistée.

:::note[Données minimales, par contrat]
Une observation d'arête ne transporte que des identifiants et une classification lecture/écriture — jamais de corps SQL, de payloads de requête, de secrets ni de PII. Un finding transporte un hash de tout détail sensible, jamais le détail lui-même. C'est une propriété du vocabulaire de fil que parle le connecteur, pas une option de configuration que vous pourriez désactiver. Voir l'[aperçu de l'architecture](/fr/explanation/architecture/overview/) pour situer cela dans la conception read-first.
:::

### Les connecteurs sont en Apache-2.0 et n'importent jamais le core

Un connecteur importe le SDK de connecteur et rien d'autre du produit. Il n'importe jamais `/core` (le moteur AGPL). Cette frontière est appliquée en CI, et c'est ce qui permet aux connecteurs d'être livrés sous Apache-2.0 et aux tiers de construire les leurs sans friction de copyleft. Le même binaire de connecteur s'exécute in-process ou out-of-process via gRPC de manière identique. Voir [Open core et licences](/fr/explanation/open-core-and-licensing/) pour la frontière complète.

## Provenance et confiance : pourquoi la source compte

Chaque arête enregistre **quelle source l'a produite** et un niveau de **confiance**, et le produit montre les deux plutôt que de les fusionner. Un READ `pg_audit` et un indice `mcp_annotation` ne sont pas la même preuve et ne sont jamais traités comme la même chose.

Les deux niveaux de confiance sont honnêtes, pas cosmétiques :

- **`attributed`** — l'accès est fermement lié à son origine (par exemple, une identité par agent présente dans la piste d'audit).
- **`approximate`** — l'attribution est inférée ou avec perte (un compte de service partagé, ou un store dont l'audit ne peut pas séparer proprement les appelants).

Le mode d'accès est l'un de `unknown`, `read`, `write`, `readwrite`. `unknown` est explicite et jamais deviné — le produit préfère montrer « nous n'avons pas pu classer cela » plutôt que de fabriquer un label lecture/écriture.

## Catégories de source de première partie, par signal

Les sources de première partie diffèrent par le **signal** qu'elles transportent. Choisissez la source en fonction de ce que le système que vous observez peut honnêtement vous dire.

### `pg_audit` — READ/WRITE PostgreSQL

La source pgAudit fait un tail du propre log d'audit structuré de PostgreSQL et émet une arête par accès aux données audité. Le mode lecture/écriture est pris **verbatim du champ CLASS de pgAudit** (READ, WRITE, DDL) — jamais inféré du texte SQL. L'origine est le rôle ou l'`application_name` auquel le log attribue l'accès. Le connecteur est en lecture seule sur le fichier de log ; il ne se connecte jamais à la base ni n'y écrit. C'est le niveau propre : un store objet/relationnel qui classe l'accès dans sa propre piste native.

### `cloudtrail` — readOnly S3 AWS

La source CloudTrail lit les fichiers de log CloudTrail et émet une arête par événement S3. Le mode lecture/écriture est pris **verbatim du champ `readOnly` de CloudTrail**, jamais inféré. L'origine est le principal IAM auquel CloudTrail attribue l'appel. Un rôle assumé partagé entre de nombreux appelants est marqué `approximate`, délibérément, parce que la piste ne peut pas séparer les véritables appelants derrière lui.

### `otel` — agents coopératifs

C'est le chemin coopératif : un agent qui émet de la télémétrie d'outil OpenTelemetry rapporte ce qu'il a fait, et le moteur l'ingère. Claude Code est la source de première partie canonique ici, combinant la télémétrie OTLP avec l'introspection MCP — voir [Connecter Claude Code](/fr/how-to/connect-claude-code/). La télémétrie coopérative est le signal de plus haute fidélité quand il est présent, mais il dépend de la coopération de l'agent, ce qui est la raison pour laquelle un filet de sécurité noyau existe.

### `ebpf` — filet de sécurité noyau Tetragon (chemin non coopératif)

La source eBPF est la moitié anti-évasion du map : là où le chemin coopératif voit ce qu'un agent *rapporte*, celle-ci voit ce que le noyau a réellement fait — lectures/écritures de fichiers et connexions réseau — même quand un agent désactive sa propre télémétrie. Elle s'exécute **hors du contrôle de l'agent**.

Deux contraintes honnêtes la définissent :

- Elle ne charge **pas** elle-même de programmes eBPF. La capture noyau est faite par Tetragon, déployé comme service durci séparé ; cette source est un consommateur en lecture seule du flux d'événements de Tetragon et n'a besoin d'aucune capacité noyau propre.
- Elle est **aveugle au corps TLS**. Elle observe des relations d'accès, jamais des payloads.

Ses arêtes sont toujours `approximate`, pour une raison précise : le noyau attribue un accès à un processus ou un conteneur — une identité d'exécution — pas à un agent résolu. L'accès lui-même est ground truth (l'appel système a eu lieu) ; la confiance qualifie l'*attribution*, que le module d'access map met à niveau une fois qu'il lie l'identité à un agent.

:::caution[Le filet de sécurité noyau est en phase de conception dans sa profondeur non coopérative]
Le chemin coopératif (audit natif du store, OTEL) est le cas vérifié, de haute fidélité. Le filet de sécurité noyau est sain dans sa conception mais son attribution de bout en bout est la partie encore en cours de preuve. Traitez-le comme un filet de sécurité qui relève le plancher, pas comme une source primaire achevée. Voir [Honnêteté et limites](/fr/start/honesty-and-limits/).
:::

### `mcp_annotation` — non fiable

La source d'introspection MCP liste les outils, ressources et prompts d'un serveur et dérive un *indice* lecture/écriture de chaque `readOnlyHint` / `destructiveHint` d'outil. Selon la spécification MCP, un client **DOIT considérer ces annotations comme non fiables** sauf si le serveur lui-même est fiable, et les valeurs par défaut sont asymétriques. Ce signal est donc un **indice de capacité déclarée, jamais un accès observé** : toute arête de ce genre est `approximate` et n'est marquée ni observée ni permise. Elle fournit la *surface de capacité* à diffuser contre — pas la preuve que quoi que ce soit a réellement été fait. Elle doit être corroborée par une source observée, jamais crue seule.

## La dépendance dure : l'identité par agent

L'attribution ne vaut que ce que vaut l'identité que le système sous-jacent enregistre. L'audit natif attribue un accès à un **identifiant ou un rôle**, pas à un agent. Si de nombreux agents partagent un compte de service ou un pool de connexions, chaque accès observé s'effondre sur cette unique identité et l'attribution devient `approximate` — le produit le dira plutôt que de prétendre pouvoir distinguer les agents.

Pour obtenir des arêtes `attributed`, donnez à chaque agent sa propre identité. C'est le pont vers la gouvernance : émettre ou appliquer une identité par agent est ce qui rend l'access map net.

:::tip[Si l'attribution semble grossière, vérifiez d'abord l'identité]
Avant de suspecter le connecteur, vérifiez si les agents partagent un identifiant. Un compte de service partagé est la raison la plus courante pour laquelle un store propre produit quand même des arêtes `approximate`.
:::

## Couverture par niveaux — soyez réaliste

La couverture est par niveaux selon ce que la surface d'audit d'un système peut honnêtement supporter :

- **Propre** — bases SQL, stores objet et entrepôts qui classent l'accès nativement (Postgres, S3, et leurs pairs). Lecture/écriture pris verbatim.
- **Avec perte** — stores dont l'audit ne peut pas séparer proprement lecture d'écriture ou appelant d'appelant (stores documentaires et vectoriels). Les arêtes atterrissent, mais souvent `approximate`.
- **Impossible passivement** — systèmes sans surface d'audit passive utilisable (caches en mémoire, bases mono-fichier embarquées). Il n'y a aucun signal read-first honnête à capturer ; le produit ne prétend pas le contraire.

Choisissez le niveau délibérément. Un store de niveau propre avec une identité par agent est l'endroit où le map est le plus net.

## Câbler une véritable source

Les sources réelles (non-démo) sont câblées depuis un unique fichier de configuration opérateur nommé par la variable d'environnement `OLIVARES_SOURCES_CONFIG`, lu **avant le démarrage du moteur**. La configuration est un document JSON ; les secrets vivent dans ce fichier (référencés par valeur) et ne sont jamais persistés par le moteur.

Le document déclare une liste de sources. Chaque entrée de source sélectionne un connecteur par sorte, nomme le tenant auquel ses observations appartiennent, et porte les propres réglages du connecteur. La forme générale est :

```json
{
  "sources": [
    {
      "name": "prod-postgres",
      "kind": "pgaudit",
      "tenant": "acme",
      "config": {
        "...": "connector-specific settings"
      }
    }
  ]
}
```

Les champs au-dessus du bloc `config` par connecteur — un nom de source, la sorte `kind` du connecteur, le `tenant` propriétaire, et un intervalle de polling optionnel pour les sources par lots — sont le contrat de câblage stable.

:::caution[Les clés de config par connecteur sont décrites génériquement ici à dessein]
Les clés exactes à l'intérieur du bloc `config` de chaque connecteur (chemins de log, endpoints, références d'identifiants) sont possédées par chaque connecteur et ne sont pas reproduites ici, parce que publier une clé non vérifiée serait pire que de l'omettre. Lisez la propre documentation du connecteur pour ses réglages, ou décrivez-le génériquement jusqu'à avoir confirmé les clés contre le connecteur que vous déployez. Ne copiez pas de schéma que vous n'avez pas vérifié. Voir [Honnêteté et limites](/fr/start/honesty-and-limits/).
:::

### Une source non configurée avertit honnêtement

Le moteur échoue en sécurité, pas en bruit, quand rien n'est câblé :

- Si `OLIVARES_SOURCES_CONFIG` est **non défini**, le moteur démarre sans aucune source.
- Si le fichier est **manquant, illisible, ou pas du JSON valide**, le moteur **avertit et continue** sans aucune source — il ne plante pas au démarrage.
- Si la liste de sources est **vide**, le moteur avertit qu'aucun connecteur n'ingèrera et que l'estate tourne sans aucun trafic en direct.

Dans tous les cas, le log de démarrage vous dit clairement que rien de réel n'est câblé, plutôt que d'apparaître silencieusement sain avec un map vide. Un avertissement honnête est le design : un access map vide ne devrait jamais ressembler à un map propre.

## Où cela s'exécute

Le data plane — les collecteurs qui exécutent ces sources — **s'exécute toujours sur l'infrastructure du client**, que le control plane soit un binaire auto-hébergé unique, un déploiement distribué, ou air-gapped. La source observe localement et le moteur ingère. Il n'y a pas de télémétrie obligatoire et, par défaut, aucune sortie du plan de contrôle. Ne franchit votre périmètre que ce que **vous** configurez à cette fin : les appels à vos API de modèles, les sorties SIEM/webhook que vous raccordez et, si vous en provisionnez un, un fournisseur externe d'embeddings. Voir [Auto-hébergement](/fr/how-to/self-hosting/) et [Installation air-gap](/fr/how-to/air-gap-install/) pour les topologies de déploiement.

## Connexe

- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — le chemin coopératif `otel`, de bout en bout.
- [Aperçu des modules](/fr/reference/modules/overview/) — les modules qui consomment ces observations (inventaire, le R/RW access map, FinOps, sécurité).
- [Aperçu de l'architecture](/fr/explanation/architecture/overview/) — où se situent le SDK de connecteur, le bus d'événements et l'access map dans la conception.
