---
title: "Module VII — déploiement et intégration"
description: >-
  Le seul module qui agit sur votre infrastructure : il planifie et gouverne le
  cycle de vie déclaratif des agents et des serveurs MCP ainsi que leur câblage à
  l'estate. Les mutations sont soumises à un contrôle HITL, en dry-run-avant-apply,
  et réversibles — et l'apply en direct reste deny-closed (503) tant qu'un
  exécuteur n'est pas provisionné.
---

Le module VII est le **seul** module qui mute l'infrastructure du client — toute autre
partie du produit est read-first. Il provisionne, met à jour et retire des agents et des
serveurs MCP sous forme d'opérations **déclaratives, versionnées, réversibles**, et déclare
la connectivité et l'identité référencée qu'un agent utilise pour atteindre une ressource
d'entreprise. Parce qu'il agit, son exigence de sécurité est la plus élevée du produit, et
l'actionnement en direct est maintenu derrière une jonction deny-closed jusqu'à ce qu'un
opérateur la provisionne explicitement.

## Planifier et gouverner, puis (peut-être) appliquer

Le cycle de vie est `plan → apply → verify → retire`, réconciliant un état **désiré** avec
l'état **réel**. La distinction qui compte est **déclarer ≠ muter** :

- **Déclarer** l'état désiré — créer, mettre à jour, faire un rollback d'une définition
  (également via la ressource manage-as-code `olivares_deployment`) — relève uniquement du
  control plane et **ne touche jamais à l'infrastructure**.
- **`plan`** est un diff dry-run pur ; **`verify`** vérifie la dérive et rafraîchit le
  snapshot. Aucun des deux ne mute.
- **`apply` et `retire`** sont les seules opérations mutantes. Elles sont **en deux phases**
  et **deny-by-default** : la phase un calcule le diff et *demande* une approbation humaine
  liée au hash du plan sans rien changer ; la phase deux ne se poursuit que si l'approbation
  est `approved` **et** que le hash du plan correspond toujours — tout autre état (pending,
  expired, rejected, pas de gate, plan obsolète) est refusé et enregistré. Re-spécifier des
  changements modifie le hash et invalide l'approbation (anti-TOCTOU).

L'apply/retire mutant n'est **pas en direct par défaut**. La jonction d'actionnement
([`Executor`](/fr/reference/modules/overview/)) est deny-closed : sans exécuteur
provisionné, apply/retire/plan/verify **échouent en fail closed avec un `503`** — le
control plane peut déclarer l'état désiré mais ne peut pas réconcilier avec l'infrastructure
réelle. Un véritable moteur (Tofu/Terraform, GitOps, Kubernetes, Docker, Nomad, Crossplane)
plus une source de credentials attestée, éphémère et par opération, ne se câblent **que sur
configuration de l'opérateur** ; à défaut, le module n'agit jamais en silence.

## Entités et le contrat déclaré

Le module déclare quatre entités namespacées plus le `Deployment` du cœur comme snapshot
appliqué :

| Entité | Rôle |
|---|---|
| **definition** | état désiré — version désirée vs appliquée, hash de spec, lien vers le `Deployment` du cœur |
| **revision** | historique de spec append-only, immuable — la source réversible pour le rollback |
| **wiring** | la connectivité **permise** `agent → resource` qu'il déclare (le contrat que le module III met en regard) |
| **operation** | registre de change-management append-only — version, hash de plan, qui a approuvé, résultat |

La spec désirée est **typée et re-sérialisée à partir de la structure** (jamais un aller-retour
JSON de l'opérateur) : les champs inconnus sont rejetés, une garde anti-credential-inline
s'exécute, et une spec portant du matériel de credential en clair est **refusée à la
déclaration**. Les credentials voyagent **par référence uniquement** (`<scheme>:<locator>`,
schéma sur allow-list) — une propriété du fil, jamais un secret stocké.

## Ce qu'il produit sur le bus (le côté PERMITTED du module III)

Le module VII n'écrit jamais l'access map ; le module III est le seul rédacteur de ses arêtes.
Lors d'un `apply` committé, pour chaque wiring le module publie un événement
[`edge.observed`](/fr/reference/events/) de type policy-grant (`Source = policy`) ne portant
que des références et le mode. Le module III le réconcilie dans le côté **PERMITTED** de son
diff permitted-vs-observed — de sorte que ce que ce module déclare est exactement ce que le
module III met en regard de ce qu'il observe. L'identité est liée par agent via la
gouvernance : une identité non humaine ferme et unique produit une arête `attributed` ; une
identité partagée ou absente est rapportée comme `approximate` — **marquée, jamais falsifiée**.

:::caution[Limites honnêtes]
- **L'apply en direct est une jonction deny-closed.** Sans exécuteur provisionné,
  `apply`/`retire` (et `plan`/`verify`) renvoient un `503` clair. Le module planifie,
  gouverne, versionne et déclare l'état désiré aujourd'hui ; il ne réconcilie avec
  l'infrastructure réelle qu'une fois qu'un opérateur a câblé un exécuteur — jamais par
  défaut, jamais un no-op silencieux.
- **L'approbation et l'attribution échouent aussi en sécurité.** Sans le gate d'approbation,
  chaque mutation est refusée ; sans le lieur d'identité, l'attribution d'un wiring est
  dégradée, pas fabriquée. `Start()` avertit une fois par jonction non câblée afin qu'un
  déploiement cassé soit visible.
- **Retirer un wiring ne rétracte pas son arête PERMITTED publiée.** Le modèle d'arête n'a
  pas de verbe de rétractation ; le wiring est marqué révoqué et le module III réconcilie
  l'obsolescence. Déclaré, pas caché.
- **La profondeur des backends varie.** Parmi les backends d'actionnement, certains chemins
  d'observation sont moins profonds que d'autres (p. ex. santé de surface sur certains
  runtimes) ; ce sont des lacunes honnêtement notées, jamais rapportées comme un in-sync
  fabriqué.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — la distinction Govern/Observe vs Actuate et la jonction `503`.
- [Module III — l'access map](/fr/reference/modules/iii-access-map/) — consomme le wiring PERMITTED que ce module déclare.
- [Référence du bus d'événements](/fr/reference/events/) — l'événement `edge.observed` et sa charge utile minimal-data.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — le flux d'approbation HITL derrière chaque mutation.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce qui actionne aujourd'hui et ce qui ne le fait pas.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — où se situe le module VII dans la couche Management.
