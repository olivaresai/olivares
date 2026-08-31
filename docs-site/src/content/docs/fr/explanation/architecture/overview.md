---
title: "Vue d'ensemble de l'architecture"
description: "Comment Olivares AI est construit : un moteur, des modules et des connecteurs — le modèle de plateforme, les huit sous-systèmes du cœur, l'access map et les topologies de déploiement."
---

Cette page explique comment Olivares AI est structuré et pourquoi. C'est une *explication*, pas un guide pratique : elle vous donne le modèle mental nécessaire pour raisonner sur le control plane avant de l'installer, de le configurer ou de l'étendre. Pour des instructions pas à pas, suivez les [guides pratiques](/fr/how-to/self-hosting/) ; pour les contrats exacts, consultez la [référence de l'API](/reference/api/) et la [référence des événements](/fr/reference/events/).

:::note[Stade de conception]
Une grande partie de ce qui suit décrit un système qui est **en bêta** et, par endroits, au stade de la conception. Le modèle de plateforme, le modèle de données, le chemin d'ingestion coopératif et le différenciateur access map sont spécifiés et construits de manière incrémentale ; certaines capacités au niveau des modules sont planifiées plutôt que livrées. Là où une capacité n'est pas encore construite, cette page le dit. Considérez ceci comme l'architecture visée, et non comme une affirmation que chaque couche est aujourd'hui prête pour la production de bout en bout.
:::

<img class="light:sl-hidden" src="/diagrams/02-architecture-dark.svg" alt="Architecture : les surfaces d'agent, les sources d'audit, les pairs MCP et A2A et les sources de contenu sont collectés de trois manières vers un unique binaire Go auto-hébergé avec la console intégrée, qui porte les modules du produit, la couche de politique et d'application et le journal de preuves signé au-dessus d'un stockage cloisonné par locataire ; il sert la console, l'API REST, un sous-ensemble gRPC ciblé, la CLI et le fournisseur Terraform, le plan de contrôle cloud (construit, non déployé) et le portail de licences (déployé, livraison désactivée) formant des plans distincts." />
<img class="dark:sl-hidden" src="/diagrams/02-architecture-light.svg" alt="Architecture : les surfaces d'agent, les sources d'audit, les pairs MCP et A2A et les sources de contenu sont collectés de trois manières vers un unique binaire Go auto-hébergé avec la console intégrée, qui porte les modules du produit, la couche de politique et d'application et le journal de preuves signé au-dessus d'un stockage cloisonné par locataire ; il sert la console, l'API REST, un sous-ensemble gRPC ciblé, la CLI et le fournisseur Terraform, le plan de contrôle cloud (construit, non déployé) et le portail de licences (déployé, livraison désactivée) formant des plans distincts." />

## Le modèle de plateforme : un moteur, des modules, des connecteurs

Olivares AI n'est pas un outil à usage unique. C'est une **plateforme modulaire** dans la lignée de Grafana, Backstage et du control plane Kubernetes : **un moteur (le cœur) plus des modules plus des connecteurs**. Le produit couvre un catalogue de modules — inventaire, sessions, access map, gouvernance, FinOps, évaluations, guardrails et bien d'autres — mais ils reposent tous sur un unique moteur partagé.

La contrainte qui gouverne l'architecture est la **règle « pas de ré-architecture »** : le moteur est conçu de sorte que *n'importe quel* module du catalogue puisse être ajouté sans toucher au cœur ni aux autres modules. Concrètement, chaque nouveau module :

1. **Consomme** des événements et des données normalisés issus du moteur ;
2. **Déclare** ses propres entités dans le modèle de données partagé ;
3. **Expose** ses propres endpoints d'API et ses vues d'interface.

Aucun module n'accède aux internes d'un autre, et aucun ne remodèle le cœur pour s'y adapter. Le moteur paie le coût initial d'être multi-tenant, piloté par les événements et API-first dès le premier jour, précisément pour que l'étendue puisse être ajoutée plus tard sans refonte. Le même principe explique l'ordre de construction — le moteur CLI d'abord, le web par-dessus : la CLI *est* le moteur et expose l'ensemble des fonctionnalités via CLI et API ; le web est une couche de présentation au-dessus de la **même API**, sans logique dupliquée. Construire le moteur puis la façade visuelle par-dessus n'est pas une ré-architecture.

La capacité différenciante — la read/write access map avec le diff permitted-versus-observed — est elle-même **un module** (le module III) au-dessus du modèle partagé, et non un pipeline sur mesure. C'est ce qui maintient la plateforme honnête : la fonctionnalité phare obéit aux mêmes règles que tout le reste.

## Les huit sous-systèmes du moteur

Le moteur (le cœur, la « couche 0 ») est l'ensemble des sous-systèmes partagés auxquels tout le reste se rattache. Il y en a huit.

| Sous-système | Ce qu'il fait | Pourquoi il vit dans le cœur |
|---|---|---|
| **Ingestion + bus d'événements** | Reçoit l'entrée OTLP et celle des connecteurs, la normalise et distribue les événements aux modules | Les modules réagissent aux événements sans se coupler les uns aux autres |
| **SDK de connecteurs** | Une interface d'entrée/sortie de connecteur stable — l'épine dorsale de l'étendue | Les tiers étendent la plateforme sans forker le cœur |
| **Runtime de modules** | Charge et exécute les modules : compilés en-process plus plugins out-of-process | Ajoute un module sans ré-architecturer ni recompiler le cœur |
| **Modèle de données général** | Entités et relations multi-tenant servant tout le catalogue | Un schéma unique que tous les modules partagent et étendent |
| **API (REST/gRPC) + manage-as-code** | Toutes les fonctionnalités via une API, plus un provider Terraform | La CLI et le web parlent la même API ; le panneau est GitOps-able |
| **AuthN/Z + multi-tenancy** | RBAC/ABAC, orgs et tenants, isolation | Rétrofiter les permissions et la tenancy coûte ruineusement cher — donc, dès le premier jour |
| **Audit + intégrité** | Audit ledger append-only et hash-chained | La preuve d'altération est transversale, jamais optionnelle |
| **Licence / entitlement** | Validation de licence Ed25519 hors ligne | Commercial en self-serve, fonctionne en air-gapped |

Quelques points précis méritent d'être soulignés :

- **Runtime de modules.** Les modules du cœur sont compilés dans le binaire ; les modules et connecteurs out-of-process s'exécutent comme des plugins via gRPC en utilisant `hashicorp/go-plugin`. Cela apporte une isolation des pannes et permet d'ajouter un module sans recompiler le cœur.
- **Bus d'événements.** En-process par défaut (canaux Go). Le binding distribué via **NATS est optionnel**, pas requis — les déploiements mono-nœud n'y touchent jamais.
- **Manage-as-code.** L'API est le contrat de référence ; la surface manage-as-code ajoute un provider Terraform afin que le control plane lui-même puisse être déclaré et versionné.
- **Audit + intégrité.** L'audit ledger est **append-only et hash-chained**, avec des **checkpoints signés Ed25519**. Les entrées portent un numéro de séquence, le hash précédent, le hash courant et une signature — et ne portent jamais de PII. L'audit ledger sort de la machine par deux voies : un endpoint d'export **pull** émet du CEF, LEEF, syslog, OTLP (une requête d'export complète et postable ; `otlp_envelope` en est un alias exact, et la projection simple de LogRecord est le token séparé `otlp_log_record`) ou OCSF, et un **push** — réel dès qu'un abonnement d'eventing `audit.recorded` est configuré — livre chaque enregistrement scellé au moins une fois via le transport durable. Voir [comment transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/).
- **Licence.** La validation est **hors ligne**, via Ed25519, et le moteur n'émet aucun appel de licence — ce qui rend l'opération air-gapped viable. La seule commande qui sort sur le réseau est `olivares upgrade` : par défaut elle récupère depuis les releases GitHub du dépôt public, ou depuis le worker de licences (`licenses.olivares.ai`) avec `--enterprise` — sauf si `--endpoint` la dirige vers votre propre miroir ou si `--bundle` installe depuis un bundle transporté.

Pour les détails d'authentification et d'autorisation (bearer tokens opaques, setup token au premier démarrage, le policy decision point) voir le [modèle de sécurité](/fr/explanation/security/security-model/) ; ils ne sont résumés ci-dessous que là où l'architecture en dépend.

### Le modèle de données général

Un unique schéma multi-tenant sert tout le catalogue. Chaque entité du cœur porte un `tenant_id`, et l'isolation est imposée au niveau de la requête / de la ligne. Les entités du cœur couvrent orgs et tenants, agents, sessions, modèles et providers, serveurs MCP, skills et outils, ressources (bases de données, serveurs, stores, APIs), identités, policies, enregistrements de coûts, résultats d'évaluation, findings, événements d'audit, état de santé et déploiements — et, de manière centrale, l'**`AccessEdge`**.

Chaque module enregistre ses propres entités et relations via un registre de types et des tables par module, sans casser le cœur. C'est le mécanisme qui sous-tend la règle « pas de ré-architecture » au niveau de la couche de données.

Le store démarre en **SQLite** (le driver pur-Go `modernc`, de sorte que le binaire n'a pas besoin de CGO et tourne en air-gapped) pour les déploiements mono-nœud, et passe à **Postgres avec row-level security** pour le multi-tenant et le passage à l'échelle.

## Module III : l'access map comme vue sur le modèle

Le module phare est la **read/write access map** et son **diff permitted-versus-observed** — le least-privilege drift. Le point architectural critique est qu'il s'agit d'**une vue sur le modèle de données général, pas d'un schéma séparé**. La map est matérialisée à partir des entités `AccessEdge`, et l'`AccessEdge` lui-même **porte à la fois le côté permitted et le côté observed**, ainsi que la source du signal et un niveau de confiance. Le diff est donc une requête sur le même modèle multi-tenant que tous les autres modules utilisent.

### Read-first et minimal-data

La map est **read-first** : elle observe depuis les logs, OpenTelemetry et (en filet de sécurité) eBPF — elle n'est jamais dans le data path des appels de l'agent. Elle est aussi **minimal-data** : elle stocke la *relation* (un agent lit/écrit une ressource), jamais les payloads, les secrets ou les PII. L'asymétrie est délibérée — signal élevé, risque faible.

### Le chemin coopératif croisé avec l'audit natif du store

La fidélité provient du croisement de deux types d'évidence indépendants :

- **Le chemin coopératif** — Claude Code et les agents émettent de la télémétrie via **OpenTelemetry (OTLP)**, complétée par l'**introspection MCP** des outils et ressources qu'un serveur expose. Le récepteur OTLP fait partie de l'ingestion du cœur et écoute sur loopback par défaut. Voir [connecter Claude Code](/fr/how-to/connect-claude-code/).
- **L'audit natif du store** — le store vous dit ce qui s'est réellement passé. **pgAudit classifie `READ` versus `WRITE`** mot pour mot sur Postgres ; **CloudTrail expose `readOnly`** pour S3 ; un audit natif équivalent existe pour d'autres moteurs.

Quand le chemin coopératif et l'audit propre du store concordent sur une edge, vous avez une relation read/write corroborée.

### Le filet de sécurité eBPF, les annotations non fiables et la couverture par paliers

Trois propriétés supplémentaires rendent la map digne de confiance plutôt que naïve :

- **eBPF / Tetragon est le filet de sécurité non coopératif.** Pour les chemins qui ne coopèrent pas, un observateur au niveau du noyau fournit une vérité de terrain sur l'intention read/write au niveau du processus et de l'hôte. Il s'exécute hors du contrôle de l'agent (anti-évasion) mais est aveugle aux payloads TLS — ce qui n'est pas un problème, car la map n'a besoin que de la *relation*, pas du contenu.
- **Les annotations MCP sont non fiables.** Les indications read-only / destructive de MCP sont un signal utile, mais la spécification MCP elle-même indique que les clients doivent les traiter comme non fiables. La map les **corrobore** donc avec d'autres sources et **ne fait jamais confiance à une annotation seule**.
- **La couverture est par paliers, et le produit le dit.** Certains stores sont **clean** à observer passivement (bases de données SQL, object stores, entrepôts) ; certains sont **lossy** (Mongo, bases de données vectorielles) ; et certains sont **impossibles à observer passivement** (Redis, SQLite, D1). La map affiche des niveaux de confiance (attributed versus approximate) plutôt que de prétendre à une précision qu'elle n'a pas.

:::caution[Une dépendance dure : une identité par agent]
L'audit natif attribue l'activité à une credential ou à un rôle, pas à un agent. Un compte de service partagé plus un connection pool font s'effondrer l'attribution — vous ne pouvez plus dire quel agent a fait quoi. Résoudre cela exige d'émettre ou d'imposer une **identité par agent**, ce qui est le pont entre l'access map et le module de gouvernance. C'est au stade de la conception, et un proof-of-concept sur le chemin coopératif (Claude Code OTEL + MCP vers Postgres pgAudit) est le verrou décisif avant que le module ne soit développé.
:::

### Atteindre la map

Visualiser l'access graph est une **action privilégiée** : limitée au tenant, disponible pour le rôle editor et au-dessus (jamais le rôle viewer le plus bas), et **chaque lecture est auditée**. Les routes de la map — le graphe et le résultat du drift — ne font pas partie du contrat stable du cœur ; elles sont publiées dans la [référence des routes de module](/reference/api-beta/) **bêta** distincte (servie à `/openapi.beta.json`), et leurs formes au niveau des champs vivent dans des interfaces Go et TypeScript typées. Le résultat permitted-versus-observed est exposé à la route `drift` du moteur (`/v1/m/accessmap/drift`) ; il n'y a pas d'endpoint `diff` séparé. La surface REST stable du cœur — 53 chemins rendus depuis le propre contrat OpenAPI 3.1 du produit — est documentée dans la [référence de l'API](/reference/api/). Pour la liste complète des modules, voir le [catalogue des modules](/fr/reference/modules/overview/).

## Topologie de déploiement

Le même binaire prend en charge plusieurs topologies. Une contrainte vaut pour toutes : le **data plane — les collecteurs — s'exécute toujours sur l'infrastructure du client**. C'est ce qui rend possibles la confidentialité et l'opération air-gapped. Il n'y a pas de télémétrie obligatoire et, par défaut, aucune sortie du plan de contrôle. Ne franchit le périmètre du client que ce que celui-ci configure à cette fin : les appels à ses API de modèles, les sorties SIEM/webhook qu'il raccorde et, s'il en provisionne un, un fournisseur externe d'embeddings.

### Binaire unique

Le défaut. Un unique binaire Go statique embarque le moteur CLI, l'**interface web embarquée via `go:embed`** (servie depuis la même origine que l'API), et **SQLite** comme store. Vous livrez un seul artefact et l'auto-hébergez. C'est la topologie derrière le [tutoriel zero-to-graph](/fr/tutorials/zero-to-graph/) et le [guide de self-hosting](/fr/how-to/self-hosting/).

### Distribuée

Pour les estates multi-hôtes, à l'échelle et multi-tenant : les collecteurs en périphérie **poussent vers un cœur central via gRPC avec mutual TLS**, le store devient **Postgres** (avec row-level security), et le bus d'événements tourne sur **NATS**. Les collecteurs n'ont pas de listener entrant — ils poussent, ils ne servent pas — ce qui maintient la surface d'attaque de la périphérie minimale.

### Air-gapped

Dans cette topologie, tout s'exécute localement avec **zéro egress** : le store est local et la licence est validée **hors ligne**. `olivares upgrade` — la seule commande qui nous contacterait sinon — installe ici depuis un bundle transporté (`--bundle`) plutôt que depuis le canal de mises à jour. Voir [installation air-gap](/fr/how-to/air-gap-install/).

### Managée (à venir)

Un control plane hébergé figure dans la roadmap. Même alors, la contrainte tient : **les collecteurs s'exécutent toujours sur l'infrastructure du client**, et seul le control plane est hébergé. C'est au stade de la conception.

:::tip[La topologie, en une ligne]
Le control plane (le moteur) peut être auto-hébergé comme un binaire unique ou, à l'avenir, managé ; le data plane (les collecteurs) est toujours sur l'infrastructure du client. Le web est toujours une vue sur la propre API du moteur — jamais un service séparé avec sa propre logique.
:::

## Frontières de confiance et licences

Deux frontières façonnent l'architecture au-delà de la topologie d'exécution :

- **La frontière des connecteurs.** Un connecteur **n'importe jamais depuis le cœur** — il ne dépend que du SDK. Cela empêche les connecteurs tiers de contaminer le cœur et garde la frontière de licence propre.
- **La frontière de licence.** Le cœur, les modules et le web sont **AGPL-3.0-only** ; le SDK et les connecteurs sont **Apache-2.0** ; le tier enterprise est commercial. La frontière des connecteurs ci-dessus est ce qui rend le découpage Apache/AGPL applicable dans le code. Voir [open core et licences](/fr/explanation/open-core-and-licensing/).

## Posture de sécurité, en bref

L'architecture est secure-by-design : observation read-first (risque faible et asymétrique), collecteurs push-only sans listener entrant, mutual TLS entre collecteur et cœur, données minimales (edges, jamais payloads), preuve d'altération via l'audit ledger append-only et hash-chained, isolation multi-tenant ancrée dans le modèle de données et self-hosting sans télémétrie obligatoire ni sortie du plan de contrôle par défaut. Ne franchit le périmètre du client que ce que celui-ci configure à cette fin : les appels à ses API de modèles, les sorties SIEM/webhook qu'il raccorde et, s'il en provisionne un, un fournisseur externe d'embeddings. L'analyse complète — y compris comment chaque frontière de confiance est défendue et ce qui est explicitement hors périmètre — vit dans le [modèle de sécurité](/fr/explanation/security/security-model/) et le [modèle de menaces](/fr/explanation/security/threat-model/).

## Où aller ensuite

- [Catalogue des modules](/fr/reference/modules/overview/) — l'ensemble complet des modules et comment ils se rattachent aux couches ci-dessus.
- [Référence des événements](/fr/reference/events/) — les événements normalisés que la couche d'ingestion distribue aux modules.
- [Modèle de menaces](/fr/explanation/security/threat-model/) — les adversaires, les frontières de confiance et les mitigations.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce qui tourne aujourd'hui versus ce qui est planifié.
