> Traduction automatique. La version anglaise fait foi.

# ADR-0017: Bus d'événements distribué = fan-out local in-proc + pont NATS, core NATS at-most-once (pas de JetStream en v1)

- **Status:** accepted (modifie la ligne de sémantique de livraison de l'ADR-0006) —
  **étendue par l'ADR-0021**, qui livre le backend JetStream at-least-once comme
  extension enterprise fermée et résout le problème de sûreté face aux doublons par
  déduplication à la frontière du bus (et non par la passe d'idempotence par abonné
  envisagée ci-dessous) ; ce pont Core NATS OUVERT reste at-most-once et inchangé.
- **Date:** 2026-06-12
- **Décideurs :** conception éprouvée sous pression par un panel adversarial à 3 perspectives avant l'implémentation
- **Références :** `docs/contracts/S02-sdk-runtime-eventbus.md §4`,
  `core/eventbus/natsbus`, recensement de l'idempotence des abonnés (recon, 2026-06-12)

## Contexte et énoncé du problème

L'ADR-0006 a laissé le bus in-proc avec un emplacement NATS. La HA est arrivée, donc le
multi-nœuds existe — et le bus ne traverse pas les nœuds : un événement publié sur un standby
(sources en arrière-plan, balayages d'identité) n'atteint jamais le traitement du leader ; la
capture de la plateforme d'événements — la frontière de durabilité — le manque silencieusement.
Deux questions devaient recevoir une réponse fondée sur des preuves, pas sur des valeurs par
défaut : **(a)** le backend distribué remplace-t-il le chemin de livraison local ou le pontifie-t-il,
et **(b)** core NATS (at-most-once) ou JetStream (at-least-once) ?

L'ADR-0006 décrivait le bus comme « at-least-once ; les consommateurs dédupliquent ». **Cette
ligne était fausse en tant que description de l'implémentation** : le bus in-proc est at-most-once
(les erreurs de handler sont journalisées, non réessayées ; les événements en file sont
abandonnés à la fermeture — `core/eventbus/inproc.go`), le contrat S02 §4 documente une
contre-pression bloquante SANS redelivery, et `modules/eventing/capture.go` indique « le bus
lui-même est at-most-once (S02) et le replay commence À la capture ». La formule at-least-once de
l'ADR-0006 décrivait la ré-émission au niveau de la source (`Gather` se ré-exécute), et non la
livraison du bus.

## Facteurs de décision

- Le recensement des abonnés du 2026-06-12 : la plupart des ~17 abonnés du bus ne sont PAS
  tolérants aux doublons (eventing double-capture, security/notify persistent ou envoient des
  doublons, les replis count/aggregate se gonflent). Une livraison at-least-once AUJOURD'HUI
  serait une régression sémantique déguisée en amélioration.
- La garantie écrite de S02 §4 — Publish bloque en cas de saturation, « perdre des événements
  silencieusement serait pire que de freiner un éditeur » — est porteuse :
  `olivares_ingest_duration_seconds` est documenté comme LE SLI de contre-pression (docs/17 §1.4)
  et le runbook de contre-pression du collecteur indique « aucun événement n'est perdu — le bus
  bloque plutôt que d'abandonner ». Router le chemin chaud local à travers un serveur inverserait
  ce contrat sur 100 % du trafic de production (le LB draine les standbys).
- Le trafic que le backend existe pour sauver (les événements d'origine standby) est le chemin à
  FAIBLE volume ; le chemin local est le chemin chaud. La conception ne doit pas troquer le chemin
  chaud contre le froid.

## Options envisagées

- **A. Transport NATS pur** — chaque publish/subscribe traverse le serveur ; un seul chemin de
  code. Rejetée : inverse S02 §4 sur le chemin local (abandons silencieux de consommateurs lents
  là où le contrat promet un blocage sans perte), ajoute des fenêtres de perte au
  redémarrage/à la reconnexion du serveur sur la livraison intra-nœud, et dégrade le sens du SLI
  d'ingestion.
- **B. Hybride : fan-out local in-proc + pont NATS avec NoEcho (RETENUE).**
- **C. JetStream (at-least-once)** — rejetée pour la v1 : le recensement montre que les abonnés ne
  sont pas tolérants aux doublons ; JetStream ne devient un travail réalisable qu'APRÈS une passe
  d'idempotence sur l'ensemble des abonnés (suivie comme le chemin de mise à niveau explicite
  ci-dessous).

## Résultat de la décision

Option retenue : **B + core NATS**. `core/eventbus/natsbus` embarque le bus in-proc : Publish
diffuse d'abord localement (chaque garantie de S02 §4 intacte — contre-pression bloquante, zéro
perte locale, isolation des panics, aucun codec sur le chemin chaud), puis pontifie l'événement
vers NATS en best-effort. La connexion du pont positionne **NoEcho**, de sorte que son unique
abonnement wildcard ne reçoit que les événements d'ORIGINE DISTANTE, qu'il re-matérialise (proto
`Event` figé en oneof pour les trois charges d'observation, JSON + registre de décodeurs pour les
types définis par les modules) et injecte dans le fan-out local — aucune double livraison, ordre
par éditeur préservé entre les types (une connexion par nœud, un abonnement ordonné).

**Sémantique inter-nœuds, documentée honnêtement : at-most-once.** Fenêtres de perte :
redémarrage du serveur NATS (pas de persistance), débordement du tampon de reconnexion/jamais
reconnecté (« bufferisé ≠ livré »), et abandons de consommateur lent lorsque le tampon en attente
de l'abonnement du pont se remplit — chacun comptabilisé (`olivares_eventbus_bridge_*`) et
alertable, jamais silencieux. HA : les événements distants ne sont **injectés que sur le leader**
(`SetInjectGate(store.Leader().Active)`), ce qui élimine la classe des effets de bord côté standby
(notifications en double, findings dérivés en double, tempêtes de logs ErrNotLeader) à la
frontière du bus ; le chevauchement de bascule ≤2s peut provoquer une double injection, absorbée
par l'index unique `(tenant_id, event_id)` de la capture d'eventing. La configuration
(`OLIVARES_BUS_CONFIG`) est fail-boot-closed : un nœud qui serait silencieusement retombé en
in-proc fonctionnerait partitionné.

### Conséquences

- **Avantages :** les observations d'origine standby atteignent le leader (comblant l'écart
  inter-nœuds) ; le binaire mono-nœud par défaut est inchangé octet pour octet ; la sémantique de
  livraison locale est inchangée ; la perte inter-nœuds est comptabilisée, non silencieuse.
- **Inconvénients / compromis :** le chemin inter-nœuds n'est sollicité que par le trafic d'origine
  standby — son chemin de codec/injection porte des tests d'intégration dédiés (nats-server
  embarqué) précisément parce que la production le sollicite rarement ; les événements pontés
  ajoutent un encodage par publish sur les nœuds AVEC le pont configuré.
- **Neutre :** JetStream reste le chemin de mise à niveau at-least-once, conditionné à une passe
  d'idempotence sur les abonnés (le recensement est la liste de travail) ; l'interface `Bus` n'a
  rien gagné — Stats et les abonnements nommés sont des interfaces d'extension optionnelles.

## Pourquoi les alternatives ont été rejetées

Voir les facteurs : A inverse un contrat écrit sur le chemin chaud pour simplifier le chemin
froid ; C livre des doublons à des abonnés qui, de façon démontrable, les gèrent mal.
