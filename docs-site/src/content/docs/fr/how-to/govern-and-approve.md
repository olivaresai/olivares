---
title: "Gouverner et approuver (human-in-the-loop)"
description: "Comment un opérateur gouverne l'estate : identité et permissions, le modèle RBAC deny-by-default, la couture de politique restrict-only, et la posture human-in-the-loop où les décisions sont consignées dans le ledger d'audit."
---

Cette page s'adresse à l'opérateur qui a connecté au moins une source et doit
maintenant **gouverner** l'estate : décider qui et quoi peut agir, examiner ce que la
plateforme fait remonter, et agir en conséquence. La gouvernance vit dans le **module VI
(identité, permissions, gouvernance)**, repose sur le même noyau d'autorisation que le
reste de l'API, et est **entièrement auditée**.

:::caution[Périmètre honnête : le moteur d'approbation est construit ; la console opérateur mûrit encore]
Ce qui tourne aujourd'hui est le **noyau d'autorisation** — RBAC deny-by-default, une
couture de politique restrict-only, un accès scopé au locataire, et un ledger d'audit
signé append-only qui consigne chaque décision de gouvernance et chaque lecture
privilégiée — **plus un moteur d'approbation human-in-the-loop fonctionnel** : des
demandes d'approbation gouvernées liées à un hash de plan, ouvertes deny-closed et
limitées dans le temps, avec **séparation des tâches, déduplication du décideur et
expiration appliquées côté serveur**, et des endpoints approve/deny sous l'espace de noms
du module de gouvernance. Ce qui **mûrit encore** est la **surface de revue opérateur**
plus riche — une console de file d'approbation complète et une UI de revue structurée.
Cette page décrit le modèle, les endpoints en service et la garantie de décisions
consignées ; là où l'UI opérateur est encore au stade de conception, elle le dit.
:::

## Le modèle d'autorisation au sein duquel vous gouvernez

Chaque décision de gouvernance est prise par le même noyau d'autorisation qui protège le
reste du control plane. Comprenez ses trois propriétés avant de modifier quoi que ce
soit.

### Le RBAC est deny-by-default

L'autorisation exécute le **RBAC d'abord**. Un principal sans appartenance à un locataire
est **refusé** — il n'y a pas de grant implicite. Les permissions sont scopées à un
locataire, et le handler n'agit que sur le **seul locataire vers lequel la requête s'est
résolue**, jamais un qu'il re-dérive, ce qui ferme par construction les classes de
confused-deputy et d'IDOR.

Les rôles intégrés forment une échelle de capacité croissante :

| Rôle | Ce qu'il peut faire |
|---|---|
| `viewer` | lire les données opérationnelles et la piste d'audit |
| `editor` | ce qui précède, plus écrire les données opérationnelles |
| `admin` | ce qui précède, plus l'IAM du locataire — utilisateurs, appartenances, tokens, paramètres |
| `owner` | toutes les permissions au sein du locataire |

Un module déclare ses propres permissions avec espace de noms
(`<namespace>:<resource>:<verb>`), et les rôles se voient accorder ces permissions **par
palier de verbe** (viewer mappe sur read, editor sur write, admin et owner sur admin). Un
nouveau module introduit donc une surface de gouvernance sans release du moteur.

:::note[Consulter le graphe d'accès est une action privilégiée — par conception]
La R/RW access map du module III est l'actif le plus sensible du produit : une carte de
ce que chaque agent peut toucher est une feuille de route de reconnaissance pour un
attaquant. Donc **lire le graphe d'accès est une action privilégiée**, accordée à partir
du rôle **editor et au-dessus — jamais le viewer le plus bas**. Elle est **scopée au
locataire** (une lecture ne peut voir que le graphe d'un seul locataire), et **chaque
lecture est écrite dans le ledger d'audit** — qui a regardé l'accès de qui, et quand.
Privilège, scoping au locataire et auto-audit sont superposés délibérément ; voir le
[modèle de sécurité](/fr/explanation/security/security-model/).
:::

### La couture de politique (ABAC/PDP) ne fait que restreindre

Par-dessus le RBAC, l'opérateur peut câbler un **policy decision point (PDP)** externe
pour des règles basées sur attributs. Vous choisissez le moteur avec une seule variable
d'environnement :

```bash
# Choose one. Cedar is the embedded, pure-Go primary; OPA is an over-HTTP adapter.
OLIVARES_PDP_ENGINE=cedar   # or: opa | none
```

Les deux moteurs se placent derrière une seule couture, et la couture a un invariant qui
régit la façon dont vous devez raisonner à son sujet :

:::tip[Le PDP ne peut que retirer de l'accès, jamais en ajouter]
La couture de politique se compose comme **RBAC ∩ ABAC natif ∩ PDP externe**,
intersectés. Un PDP **ne fait que restreindre ; il n'élargit jamais** ce que le RBAC
autorisait déjà. Vous ne pouvez pas utiliser une politique Cedar ou OPA pour *accorder*
un accès que le modèle de rôle refuse — seulement pour refuser un accès que le modèle de
rôle autoriserait autrement. C'est appliqué, ce n'est pas une convention.
:::

Les deux adaptateurs préservent cet invariant de manières différentes, et vous rédigez la
politique en conséquence :

- **Cedar (embarqué, primaire, pure-Go).** Vous écrivez des règles `forbid`. Une règle
  qui correspond est une restriction ; un ensemble de règles vide signifie que la décision
  RBAC tient. Un `permit` dans Cedar ne peut jamais élargir la décision.
- **OPA (en HTTP).** Votre Rego doit être **permit-by-default** (`default allow := true`,
  avec des clauses `allow := false` pour vos refus). Un résultat `true` signifie aucune
  restriction ; `false`, un résultat manquant, ou toute erreur de transport ou non-2xx
  **échoue en fermé** — la requête est refusée.

Une **configuration de PDP invalide ne désactive que le PDP externe** et journalise le
fait — l'ABAC natif et le RBAC continuent de gouverner. Un moteur de politique mal
configuré ne laisse jamais des requêtes non gouvernées et ne met jamais le control plane
à l'arrêt. **Chaque restriction que le PDP applique est auditée.**

## Ce que les surfaces vous disent d'examiner

La gouvernance human-in-the-loop est pilotée par ce que la plateforme observe et
présente. Deux flux disent à un opérateur ce qui justifie une décision :

| Flux | Module | Ce qu'il fait remonter |
|---|---|---|
| **Least-privilege drift** | III (access map) | le diff **permitted-vs-observed** — une capacité accordée utilisée d'une façon que personne n'avait prévue, ou un chemin atteignable mais jamais exercé |
| **Constats** | IX (sécurité, guardrails, forensique) | les constats de guardrail et de red-team, plus le flux de notification que la plateforme route |

Le module III, l'access map, est **read-first** — il observe via les logs, OpenTelemetry
et (comme filet de sécurité noyau non-coopératif) eBPF, et n'est **jamais dans le chemin
de données de l'agent**, de sorte qu'une défaillance de collecteur ne peut pas casser la
production. Il est aussi **minimal-data** : il stocke la relation
`agent → resource (read/write)`, jamais les payloads, secrets ou PII. Le signal qu'il
porte est honnête sur sa propre confiance (`attributed` vs `approximate`) et sur sa
propre portée.

:::caution[La couverture est en paliers — le drift n'est pas uniformément complet]
La fidélité de l'access map dépend de la ressource. La couverture est en **paliers** :
*clean* pour les bases SQL, les object stores et les warehouses (l'audit natif classe
read vs write mot pour mot) ; *lossy* pour les stores comme les bases documentaires et
vectorielles ; et **impossible à observer passivement** pour les stores en mémoire et
embarqués. Gouvernez en gardant ceci à l'esprit : une absence d'accès observé n'est pas
une preuve d'absence d'accès là où la couverture est lossy ou absente. Lisez
[le threat model](/fr/explanation/security/threat-model/) pour ce que chaque palier peut et
ne peut pas attester.
:::

Une classe de signal nécessite un jugement de gouvernance explicite. Les annotations
d'outils MCP (`readOnlyHint` / `destructiveHint`) sont un indice read/write utile mais
sont **non fiables selon la spécification MCP** — les clients doivent les traiter comme
non fiables. La plateforme les **corrobore** avec des signaux fiables et ne leur fait
jamais confiance seules, et vous devriez faire de même en agissant sur un item de drift
qui ne repose que sur une annotation.

## La posture human-in-the-loop

La boucle de gouvernance prévue est : **les surfaces présentent** (le drift du module
III, les constats du module IX) → **un opérateur autorisé décide** → **la décision est
consignée dans le ledger d'audit**.

Les trois parties de cette boucle tournent aujourd'hui. **Les surfaces sont réelles** —
le module III produit le diff permitted-vs-observed et le module IX produit les constats.
**Le moteur d'approbation est réel** — une demande d'approbation gouvernée s'ouvre contre
le module de gouvernance (deny-closed, liée au hash de plan, limitée dans le temps) ; un
opérateur autorisé approuve ou rejette via l'endpoint de décision, et **la séparation des
tâches, la déduplication du décideur et l'expiration sont appliquées côté serveur** de
sorte que le demandeur ne peut jamais décider de sa propre demande et qu'une demande
expirée ne peut jamais lier. Et **la consignation est réelle et solide** — voir la
garantie ci-dessous. Ce qui est **encore au stade de conception** est la **console de
revue opérateur** aboutie — une UI riche de file d'approbation ; les endpoints et le
moteur sont livrés, la surface de revue soignée est la voie à suivre pour le module VI.

La dépendance qui rend cette boucle crédible est l'**identité par agent**. L'audit de la
plateforme attribue l'activité à une crédential ou un rôle, pas intrinsèquement à un
agent ; un compte de service partagé avec un pool de connexions effondre l'attribution.
Bien gouverner signifie donc **émettre et appliquer une identité par agent** — le pont de
l'observation (module III) à la gouvernance (module VI). Le côté identité de cela est
construit autour de crédentials first-party opaques et révocables et d'un roster
d'identités non-humaines ; la **seule primitive de frappe de crédentials** du produit est
opt-in, attestée, auditée, et ne persiste jamais le token frappé. Voir le
[catalogue des modules](/fr/reference/modules/overview/) pour la façon dont identité,
permissions et gouvernance se composent à travers l'estate.

:::tip[La garantie des décisions consignées]
Quelle que soit la profondeur du workflow au-dessus, **une décision de gouvernance est un
fait consigné**. Les actions mutantes sont ajoutées au ledger d'audit avec l'**acteur
réel** dans la **même transaction** que le changement, et les lectures sensibles (le
graphe d'accès, le ledger lui-même) s'auto-auditent dans une écriture committée. Le
ledger est **append-only, à chaîne de hachages, et protégé par des signatures Ed25519** —
chaque enregistrement porte `seq`, `prev_hash`, `hash` et `sig`, de sorte que réécrire
l'historique est cryptographiquement détectable, et **il ne contient jamais de PII**.
Vous ne pouvez pas faire un changement non gouverné que le ledger oublie silencieusement.
:::

### Obtenir l'enregistrement d'emblée

Pour une copie externe et immuable — la chose qu'un auditeur d'entreprise demande et que
la télémétrie native ne fournit pas — le ledger est exposé comme un **export pull
authentifié** :

```bash
# Pull the signed, hash-chained ledger for offline re-verification.
# Requires a token whose role can read the audit trail (viewer and up).
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Les valeurs de `format` prises en charge sont `cef`, `leef`, `syslog`, `otlp`,
`otlp_envelope`, `otlp_log_record` et `ocsf` — `otlp` émet la requête d'export
complète et postable, `otlp_envelope` en est un alias exact, et `otlp_log_record`
est la projection simple à un LogRecord par ligne.
Chaque enregistrement porte les champs d'intégrité de chaîne pour que votre SIEM ou store
WORM puisse **re-vérifier la chaîne hors ligne**. La signature détachée protège contre une
compromission limitée à la base (injection, sauvegarde ou réplica volé, rôle contournant
le RLS) et contre la suppression de checkpoint ; une **copie hors machine** est le
contrôle contre un hôte entièrement compromis. Voir
[forwarder l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/) pour un pipeline
file-tail complet.

Le least-privilege drift sur lequel ces décisions agissent est le résultat
permitted-vs-observed de l'access map. Le [tutoriel zero-to-graph](/fr/tutorials/zero-to-graph/)
guide pour l'atteindre concrètement sur l'estate de démo ; la surface du module access map
est soumise aux mêmes RBAC deny-by-default, scoping au locataire et audit par lecture que
tout le reste, ce qui explique pourquoi la lire est une action editor-et-au-dessus.

## Où aller ensuite

- [Modèle de sécurité](/fr/explanation/security/security-model/) — privilège, scoping au
  locataire, auto-audit, et la posture minimal-data en détail.
- [Threat model](/fr/explanation/security/threat-model/) — les actifs, les frontières de
  confiance, et ce que chaque palier de couverture peut attester.
- [Catalogue des modules](/fr/reference/modules/overview/) — comment identité, permissions et
  gouvernance (module VI) se composent avec l'access map (module III) et les constats
  (module IX).
- [Connecter une source](/fr/how-to/connect-a-source/) — câblez les signaux à partir
  desquels le drift et les constats sont construits.
