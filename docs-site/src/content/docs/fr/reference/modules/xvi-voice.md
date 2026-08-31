---
title: "Module XVI — agents vocaux et temps réel"
description: >-
  Le plan d'observation et de gouvernance pour les agents conversationnels/temps réel. Il
  gouverne qui peut ouvrir une session vocale, avec quel modèle et quel fournisseur, sous une
  politique deny-par-défaut — et suit les métadonnées de session avec une interdiction stricte
  de tout contenu audio ou transcription.
---

Le module XVI gouverne les **agents conversationnels et temps réel**. C'est un plan
**d'observation et de gouvernance** : il ne réimplémente **pas** un SDK vocal (Realtime API,
WebRTC, ASR ou TTS) et il n'ouvre jamais lui-même un flux média. Il décide *qui* peut ouvrir
une session vocale, avec *quel* modèle et fournisseur, sous *quelle* politique, et suit les
métadonnées de cette session — jamais son contenu.

## Ce que c'est

Ouvrir une interface vocale est traité comme une **action privilégiée**, pas comme une
opération libre. La politique est **deny-par-défaut** : une session sans politique l'autorisant
est refusée. Une ouverture est **en deux phases** et **gated par human-in-the-loop** via la
[porte d'approbation](/fr/how-to/govern-and-approve/) ; elle est liée à un `plan_hash` afin qu'une
approbation ne puisse pas être silencieusement promue vers un modèle plus puissant (anti-TOCTOU),
auditée au **principal réel** (jamais `system`), et attestée en **append-only**. Le module
lui-même n'appelle jamais un fournisseur — l'actuation sort par un seam de dispatch distinct.

L'autre moitié est l'**observation** : le module ne suit que les métadonnées de session —
état dérivé (live/idle/ended, calculé au moment de la lecture à partir de la récence d'activité,
sans colonne de cycle de vie stockée), nombre de tours, durée, latence (moyenne et maximum
honnêtes issus d'échantillons réels), et langue BCP-47. À partir de là, il lève des **findings**
de gouvernance : une violation de politique lorsque la télémétrie nomme un agent/modèle/fournisseur
qu'aucune politique n'autorise, un finding de latence dégradée lorsque la latence dépasse un SLA
de politique, et un finding d'ouverture non gouvernée lorsqu'une ouverture est tentée sans porte
câblée — l'écart est exposé et l'ouverture est tout de même refusée.

## Contrat et entités

Le module déclare trois entités dans le modèle de données partagé :

| Entité | Mutabilité | Objet |
|---|---|---|
| **session** | mutable (upsert) | métadonnées de session ; **zéro contenu** |
| **policy** | mutable | déclaration de gouvernance — qui peut ouvrir avec quel modèle/fournisseur (deny-par-défaut) |
| **decision** | **append-only** | registre immuable des décisions d'ouverture/fermeture |

Une politique correspond sur l'agent, le modèle autorisé et le fournisseur autorisé (chacun
spécifique ou wildcard), avec des bornes optionnelles de minutes de session et de SLA de latence.
**Aucune politique correspondante signifie DENY.** Le registre de décisions enregistre chaque
`open_request`, `open` et `close` avec son verdict de politique, son statut de porte et son statut
de résultat. L'accès en lecture est le rôle viewer et au-dessus ; déclarer une politique et ouvrir
une session sont des actions administratives, portées par le tenant et auditées. Ces routes de
module sont publiées dans la [référence des routes de module](/reference/api-beta/)
**bêta** distincte, et non dans le contrat stable du cœur — leurs formes au niveau des champs
vivent dans les interfaces typées du produit. Les montants en dollars ne sont **pas** ici ;
FinOps (module XI) assume le coût.

## Ce qu'il consomme et produit

Le module possède un seam d'ingestion deny-closed — son propre événement `voice.telemetry.observed`
— par lequel une sonde **in-process** alimenterait les métadonnées de session. Le fil est
**à données minimales par construction** : le parser de télémétrie porte une allow-list et
**rejette l'événement entier** s'il voit une clé interdite, de sorte qu'aucun audio, texte de
transcription, texte ASR/TTS, contenu de prompt/réponse ou PII de locuteur ne peut jamais être
persisté. Le seul signal de transcription conservé est un hash unidirectionnel d'un *localisateur*
de transcription *externe* — la preuve qu'une transcription existe, jamais la transcription. Les
findings de gouvernance sont émis comme [`finding.reported`](/fr/reference/events/) avec un détail
hashé, après commit.

## Statut d'actuation

Une ouverture gouvernée dispatche **en direct** : une fois qu'un dispatcher vocal est provisionné
par l'opérateur, une ouverture approuvée frappe une **credential éphémère côté serveur** et ne
retourne que cette credential plus les coordonnées de connexion — modèle, voix, outils et détection
de tour sont fixés **depuis la politique**, jamais depuis le client, et la clé maître du fournisseur
ne quitte jamais le serveur. Sans ce provisionnement, le seam de dispatch est **deny-closed** : une
ouverture approuvée est honnêtement enregistrée comme « déclarée, non ouverte » plutôt que simulée.

:::caution[Limites honnêtes]
- **L'observation est dormante dans ce build.** Aucun connecteur ou sonde vocale n'est encore
  livré, donc la moitié observation reste **honnêtement vide** jusqu'à ce qu'une sonde in-process
  publie de la télémétrie. Le module avertit au démarrage lorsque rien ne l'alimente. Un plugin
  hors-processus **ne peut pas** l'alimenter (le proto gRPC du control plane ne porte aucun RPC
  d'événement) — la sonde doit être in-process.
- **Aucun contenu, jamais.** C'est une propriété stricte du fil, pas un réglage : le schéma n'a
  aucune colonne de contenu et le parser rejette les clés inconnues. La latence est affichée comme
  moyenne/max honnêtes issues d'échantillons réels — jamais un p50/p95 fabriqué.
- **Aucun finding de « stall ».** La fin d'une session vocale est un silence normal (comme un agent
  terminé). Sans baseline honnête, un finding de stall serait un faux positif, il est donc
  délibérément omis.
- **Pré-1.0.** Comme une grande partie de la plateforme, ce module est en profondeur au stade de
  conception — voir [Honnêteté et limites](/fr/start/honesty-and-limits/).
:::

## Voir aussi

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module XVI et son statut d'actuation.
- [Référence du bus d'événements](/fr/reference/events/) — `finding.reported` porte les findings vocaux.
- [Module IV — orchestration](/fr/reference/modules/iv-orchestration/) — le seam de dispatch sœur (tir en direct).
- [Module X — routage de modèles et fournisseurs](/fr/reference/modules/x-models/) — quels modèles une politique peut autoriser.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — la porte d'ouverture en deux phases en pratique.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — la séparation observation/gouvernance/actuation.
