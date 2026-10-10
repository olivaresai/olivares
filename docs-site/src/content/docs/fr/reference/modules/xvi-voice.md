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

Une ouverture gouvernée dispatche **en direct** : une fois le dispatcher voice
provisionné par l’opérateur, une ouverture approuvée crée un **credential éphémère
côté serveur** et le renvoie avec les coordonnées de connexion. La configuration
de session opérateur fournit voix et détection de tours ; un modèle configuré
remplace celui demandé. Sans modèle configuré, le dispatcher utilise le modèle
demandé autorisé par la politique du tenant. La clé maître du fournisseur ne quitte
jamais le serveur. Sans provisionnement, le dispatch **refuse en cas d’échec** :
l’ouverture approuvée est honnêtement enregistrée « déclarée, non ouverte ».

## Configurer et tester une ouverture gouvernée

Activez le module existant avec `olivares modules on voice`. Le moteur enregistre
la sélection et redémarre une fois si l’ensemble des modules actifs change ;
`olivares modules ls` indique s’il tourne. Voice exige FinOps et governance selon
la spécification des modules. Le désactiver conserve politiques, métadonnées de
session et ledger des décisions.

Le dispatcher est provisionné sur l’hôte du moteur via
`OLIVARES_VOICE_DISPATCH_CONFIG`, chemin absolu d’un fichier JSON détenu par
l’opérateur. Seul le compte du moteur doit pouvoir le lire. Les clés maîtres des
fournisseurs appartiennent à ce fichier, jamais aux arguments CLI, lignes de
politique ou bundles de connexion client. Pour un adaptateur OpenAI :

```json
{
  "providers": [
    {"ref": "openai", "kind": "openai", "api_key": "<server-held provider key>"}
  ],
  "policies": [
    {
      "agent_ref": "contact-agent",
      "provider_ref": "openai",
      "model": "<your permitted realtime model>",
      "voice": "marin",
      "max_duration_seconds": 60
    }
  ]
}
```

Définissez la variable d’environnement pour le service du moteur et redémarrez-le.
Un fichier fourni illisible ou invalide empêche le démarrage. Sans configuration
de dispatcher, le comportement reste « déclaré, non ouvert ». Le fichier opérateur
choisit adaptateurs et paramètres de session ; la politique voice du tenant
autorise séparément l’agent, le modèle et le fournisseur demandés. Utilisez les
mêmes références de modèle et de fournisseur dans les deux.

Après `olivares login`, déclarez cette politique et demandez une approbation :

```sh
olivares voice policies set --agent-ref contact-agent \
  --allowed-model-ref '<your permitted realtime model>' --allowed-provider-ref openai \
  --max-session-minutes 1 --max-latency-ms 300
olivares voice sessions open --session-ref contact-1 --agent-ref contact-agent \
  --model-ref '<your permitted realtime model>' --provider-ref openai -o json
```

La première demande renvoie `op_status: requested`, une `approval_ref` et le code
de sortie CLI 7. Elle n’ouvre aucune connexion média et ne crée aucun credential
fournisseur. Les approbateurs indépendants requis approuvent cette référence via
la page d’approbation governance ou
`olivares governance approvals approve <approval-ref>`. Pour les nouvelles demandes
via le pont local d’approbation par défaut, le demandeur ne peut pas approuver sa
propre demande, même avec un autre credential du même compte. Répétez la même
ouverture avec `--approval-ref <approval-ref>`. Un refus de politique ou une
approbation en attente renvoie 403 et le code CLI 3 ; un échec d’adaptateur renvoie
502. Les contrôles de budget et d’arrêt de l’estate restent applicables.

Une demande configurée réussie renvoie `op_status: dispatched`. Son `dispatch_ref`
est une chaîne JSON contenant le `credential` temporaire, les coordonnées `connect`,
le `transport`, le modèle et l’expiration. Traitez cette réponse comme un credential :
ne la copiez pas dans des rapports ou logs. Pour OpenAI, le client échange une offre
SDP à l’URL `connect` renvoyée avec le credential temporaire, puis possède la
connexion média WebRTC. La création du credential ne prouve pas une connexion
média établie. Le ledger conserve une empreinte SHA-256 du bundle contenant un
credential, pas le credential de connexion. Les anciens bundles stockés sont
également hachés à la lecture ; les lignes append-only existantes ne sont pas
réécrites. Les handles fournisseur simples conservent leur valeur.

Inspectez les métadonnées et décisions conservées :

```sh
olivares voice sessions get contact-1 -o json
olivares voice sessions decisions contact-1 -o json
olivares voice policies ls -o json
```

Utilisez le même répertoire de données entre redémarrages. La politique et les
décisions append-only restent disponibles après redémarrage et après désactivation
puis réactivation de voice ; les credentials fournisseur restent à provisionner
séparément. Les commandes JSON utilisent les mêmes routes `/v1/m/voice`, délimitées
par tenant, que l’API.

:::caution[Limites honnêtes]
- **L’attribution des approbations a un périmètre.** Le pont local par défaut
  conserve le demandeur authentifié pour les nouvelles ouvertures humaines. Les
  approbations existantes conservent leur attribution stockée. Un pont explicitement
  configuré avec token de service attribue les demandes à son credential de service ;
  il ne garantit pas la même séparation de la personne à l’origine de la demande.
- **L’observation exige un producteur configuré.** Le plan d’appels OpenAI Realtime
  SIP optionnel utilise `OLIVARES_VOICE_CALL_CONFIG` pour vérifier les webhooks et
  attribuer tenant et projet, avec les credentials fournisseur du dispatcher. Sans
  cette configuration ou un producteur de télémétrie in-process, l’observation reste
  vide. Créer un credential WebRTC ne remplit ni le nombre de tours ni la latence.
  Un plugin hors processus ne peut pas publier l’événement du module via le plan
  de contrôle gRPC, qui n’expose aucun RPC d’événement.
- **Le client possède les médias des sessions créées.** Ce module n’implémente pas
  de client WebRTC et ne ferme pas sa connexion audio. Le contrôleur SIP optionnel
  est un chemin séparé. Un test local avec audio synthétique ne qualifie ni la voix
  du fournisseur, ni la facturation, ni l’observation SIP, ni l’arrêt des médias.
- **La console a un périmètre distinct.** La vue voice modifie les politiques et
  affiche sessions, décisions et flux de métadonnées. Le provisionnement du
  dispatcher et la connexion média client en sont séparés ; une journey API ou CLI
  ne qualifie pas une action navigateur.
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
