---
title: "Module XV — intégrations de sortie et notifications"
description: >-
  Le routeur de notifications du control plane : il décide QUEL signal atteint
  QUI, sur QUEL canal et QUAND, puis distribue le résultat expurgé via les
  connecteurs de sortie — Slack/Teams, PagerDuty/Opsgenie, webhook signé, SIEM. Le
  seam d'actuation de bout en bout éprouvé, avec un défaut deny-closed et un registre de preuves.
---

Le module XV est le **routeur de notifications** du control plane : lorsqu'un module
transforme une alerte en finding sur le bus d'événements, ce module décide à quelle
route du tenant elle correspond, construit une notification expurgée, supprime les
doublons et les tempêtes d'alertes, puis la **distribue en direct** vers les canaux
que l'entreprise utilise déjà. Il assume la décision *quoi/qui/quand* ; les connecteurs
de sortie assument le *comment* de la distribution — il consomme ce transport, il ne le
réimplémente jamais.

## Ce que c'est

Chaque module du produit signale une alerte sous forme de finding à données minimales sur
le bus ([`finding.reported`](/fr/reference/events/)) avec un `Kind` à espace de noms — fiabilité
(`health_subject_down`), dépense (`finops_budget`), sécurité (`security_guardrail`),
régression d'eval (`eval_regression`), résidence (`compliance_residency_violation`),
cadence d'orchestration, voix, et plus encore. Le module XV s'abonne **uniquement** à ce
seul canal d'alerte couvrant l'ensemble du produit et route par `Kind`, sévérité, module
source et sujet. Il ne s'abonne délibérément **pas** à la télémétrie brute telle que
`cost.sampled` ou `edge.observed` : une *alerte* de dépense arrive comme un finding
`finops_budget`, pas comme un échantillon de coût. C'est le seam qui transforme les
findings de tout le produit en notifications actionnables.

## Contrat et entités

Le module déclare deux entités portées par le tenant dans le modèle de données partagé :

| Entité | Mode | Ce qu'elle contient |
|---|---|---|
| **route** | mutable, auditée | Une règle de routage : un prédicat sur les types d'événements, des globs de finding-kind (par ex. `health_*`), une sévérité minimale, des modules sources et des kinds de sujet → une **destination** nommée, avec des fenêtres de dédup et de throttle par route et une priorité. Ne contient **aucune credential de destination** — uniquement un nom de destination non secret. |
| **delivery** | append-only | Le registre de preuves de chaque *tentative* de distribution : route, destination, kind du finding, sévérité, référence de sujet, titre court, un hash de corrélation, et une classe de résultat (`delivered`, `failed`, `no_dispatcher`, `unknown_destination`). |

À chaque finding, le module évalue les routes activées du tenant par ordre de priorité ;
toute dimension de prédicat laissée vide signifie *n'importe quelle*, et la correspondance
glob accepte la forme exacte ou `prefix*`. La correspondance se fait dans une vue de lecture,
**la distribution réseau s'exécute strictement en dehors de toute transaction de store**,
et le résultat est ensuite écrit dans le registre append-only. Créer, modifier ou supprimer
une route, et envoyer une notification de test, sont des actions **privilégiées et
auto-auditées** attribuées au principal réel. Les routes route et delivery sont publiées dans la
[référence des routes de module](/reference/api-beta/) **bêta** distincte, et non dans le
contrat stable du cœur ; leurs formes au niveau des champs vivent dans les interfaces typées
du produit.

## Ce qu'il consomme et produit

- **Consomme** [`finding.reported`](/fr/reference/events/) — l'unique canal d'alerte couvrant
  tout le produit. C'est un routeur, pas une sonde ni un compteur : il n'interroge jamais
  l'infrastructure et ne mesure jamais.
- **Produit** des notifications sortantes via un seam de dispatch, adossé aux connecteurs
  de sortie (Slack/Teams, PagerDuty/Opsgenie, webhook signé, et une destination SIEM
  couvrant Splunk/Elastic via CEF/LEEF/syslog/OTLP). Une notification ne porte que les
  champs d'affichage déjà sûrs du finding — titre, kind, sévérité, référence de sujet et
  un hash de corrélation — **jamais** un payload, un prompt, un secret ou des données
  personnelles (PII). **Les données minimales sont une propriété du fil**, pas un filtre
  a posteriori. Le secret de destination ne vit que dans la configuration du connecteur
  que l'opérateur provisionne, référencé ici par un nom non secret.

:::caution[Limites honnêtes]
- **Le binaire par défaut embarque un dispatcher deny-closed.** Tant qu'un opérateur n'a
  pas provisionné de destinations, le dispatcher est câblé mais vide : une distribution
  sans correspondance est enregistrée comme `no_dispatcher`, et une destination mal
  configurée ou de kind inconnu se résout en `unknown_destination` dans le registre. Il ne
  **simule jamais un succès** — une non-distribution est toujours visible.
- **Le webhook sortant est un connecteur de destination, pas un webhook OpenAPI.** C'est
  un canal de sortie vers lequel le control plane pousse, pas un callback que vous
  enregistrez auprès de l'API du produit.
- **La dédup et le throttle suppriment l'*envoi*, pas un résultat.** Une notification
  dédupliquée ou throttlée n'est intentionnellement **pas** écrite dans le registre de
  delivery (elle n'est donc jamais gonflée). Chaque *tentative* de distribution réelle,
  en revanche, est enregistrée — `delivered`, `failed`, `no_dispatcher` et
  `unknown_destination` indifféremment — de sorte qu'une non-distribution est toujours
  visible, jamais silencieusement abandonnée.
- **L'erreur brute du connecteur n'est jamais persistée ni journalisée** — seule une
  classe de résultat non sensible l'est — parce qu'une erreur de transport peut transporter
  le secret de destination dans son URL.
:::

## Voir aussi

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module XV et la séparation Govern/Actuate.
- [Pousser vers votre SIEM](/fr/how-to/cookbook/push-to-siem/) — le driver de push S2S
  (`modules/siemforward`) qui reformate les findings et le registre d'audit scellé dans
  le dialecte natif d'une tour (OCSF/CEF/LEEF/syslog/OTLP) et s'appuie sur la distribution
  durable de la plateforme d'événements — le complément push aux destinations ci-dessus.
- [Référence du bus d'événements](/fr/reference/events/) — l'événement `finding.reported` et son payload `FindingReport`.
- [Carte des accès et ressources](/fr/reference/modules/iii-access-map/) — une référence Core/Intelligence sœur.
- [Transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/) — câbler une destination SIEM.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur les findings que ce module route.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — la posture deny-closed-par-défaut à travers le produit.
