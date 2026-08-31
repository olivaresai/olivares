> Traduction automatique. La version anglaise fait foi.

# ADR-0021: Backend durable JetStream pour le bus d'événements (at-least-once + déduplication à la frontière du bus) en tant qu'add-on enterprise fermé

- **Status:** accepted (extends ADR-0017's "JetStream remains the upgrade path")
- **Date:** 2026-06-24
- **Deciders:** Fran Olivares (scale/reliability lever); design re-anchored against HEAD + a subscriber-idempotency re-census
- **References:** ADR-0017 (the at-most-once Core-NATS bridge), ADR-0020 (enterprise private-repo distribution),
  `LICENSING.md`, `enterprise/durablebus`, `core/eventbus/natsbus`

## Contexte et énoncé du problème

L'ADR-0017 a livré le bus distribué sous forme d'un fan-out local en processus + un bridge
**Core-NATS, at-most-once**, et a explicitement **rejeté JetStream pour la v1** (option C),
car le recensement des subscribers du 2026-06-12 avait montré que la plupart ne toléraient
pas les doublons — l'at-least-once aurait livré des doublons à des handlers qui les gèrent
mal. JetStream restait « la voie de mise à niveau vers l'at-least-once, **conditionnée à
une passe d'idempotence des subscribers** ».

Un plan de contrôle de gouvernance ne peut pas perdre silencieusement un événement qui
déclenche une DÉCISION. Avec le bridge ouvert, un finding.reported / cost.sampled perdu entre
des nœuds HA (redémarrage du serveur, débordement du tampon de reconnexion, abandon dû à un
consommateur lent) est un signal d'enforcement manqué en silence. Le niveau enterprise de
montée en charge/fiabilité (levier n° 4) doit combler cette lacune pour la classe des
événements d'enforcement — sans la passe d'idempotence par subscriber anticipée par
l'ADR-0017 (un nouveau recensement a confirmé que les subscribers ne sont encore
idempotents que « **suffisamment** » : par exemple, `modules/security` déduplique les
findings par un **scan best-effort borné**, et non par une garantie stricte — `observed.go`,
`anomaly.go`).

## Facteurs de décision

- **Résoudre la non-idempotence au niveau du BUS, et non en faisant confiance aux
  handlers.** L'ADR-0017 conditionnait JetStream à l'idempotence de chaque subscriber. Cette
  approche est fragile (un invariant distribué parmi environ 17 handlers, que toute
  modification future peut à nouveau rompre) et n'a jamais été menée à terme. Une seule
  déduplication maîtrisée à la frontière du bus constitue la solution durable : les
  subscribers gagnent en durabilité sans que chacun doive rester correct à jamais.
- **Aucun rug pull, aucune régression du chemin chaud.** La contrainte structurante de
  l'ADR-0017 demeure : le chemin chaud local en processus et le bridge Core-NATS ouvert
  doivent rester identiques octet pour octet dans le binaire community. La mise à niveau
  doit être ADDITIVE.
- **Calendrier de monétisation (ADR-0020).** La durabilité/HA est un levier du niveau
  enterprise. Elle est livrée sous forme de code fermé derrière le build tag `enterprise`,
  après que la séparation du dépôt privé a transformé le tag en véritable frontière.

## Options envisagées

- **A. Remplacer le bridge par JetStream pour TOUS les types.** Rejetée : cette option fait
  passer les observations à fort volume tolérant les pertes (edge/metric) par du stockage
  RAFT et modifierait le comportement du bridge ouvert (rug pull).
- **B. JetStream durable uniquement pour la classe ENFORCEMENT, avec le bridge ouvert
  embarqué pour le reste (CHOISIE).**
- **C. Table persistante de déduplication par subscriber dans le store.** Rejetée pour la
  Phase 1 : une table réservée à l'enterprise rompt le gate de parité de schéma
  open≡enterprise, et une table ouverte constitue un changement plus lourd que ne l'exige
  la garantie. L'état de déduplication réside plutôt dans JetStream KV (aucun store, aucune
  modification de schéma).

## Résultat de la décision

Option choisie : **B.** Un add-on fermé `enterprise/durablebus`
(`//go:build enterprise`, `LicenseRef-Olivares-Commercial`) qui **embarque** le
`*natsbus.Bus` ouvert et ajoute un chemin JetStream pour l'**ensemble d'enforcement**
(`finding.reported`, `cost.sampled`, `guardrail.observed`, `approval.requested`,
`policy.changed` — modifiable par l'opérateur). Mécanique :

- **Espaces de noms de subjects voisins.** Les événements durables sont publiés vers
  `<durable_prefix>.<type>` (un stream JetStream, RAFT, réplicas ≥ 3), DISJOINT du
  `<subject_prefix>.>` du bridge Core — un type est donc livré par exactement un transport,
  jamais les deux. Le bridge embarqué reçoit l'instruction d'EXCLURE l'ensemble durable du
  Core-bridging (`natsbus.Options.BridgeExclude`, inerte dans le binaire ouvert). Les types
  hors enforcement conservent la portée at-most-once du bridge ouvert (aucune régression).
- **La publication confirme le PubAck** (`Nats-Msg-Id = event.ID`) : un événement durable
  est soit stocké durablement, soit l'échec est exposé — jamais abandonné silencieusement ;
  la fenêtre de doublons du stream réduit un retry / une double publication lors d'un
  failover à une seule copie stockée.
- **Consumer durable conditionné par le leader** (ack-explicit), lié lors d'une promotion et
  arrêté lors d'une rétrogradation au moyen d'un watcher `Active()` (l'elector n'expose pas
  OnDemote) ; sa position côté serveur survit au failover. L'enforcement s'exécute une fois
  à l'échelle du cluster.
- **Déduplication par event.ID à la frontière d'injection**, à deux niveaux : une fenêtre
  temporelle en mémoire (rapide, même nœud) et un bucket **JetStream KV** (répliqué par
  RAFT, borné par TTL, survit aux crashs/redémarrages et déduplique entre les nœuds).
  LECTURE-avant-injection (supprimer un doublon) + ENREGISTREMENT-après-injection (pour qu'un
  crash réinjecte plutôt qu'il ne perde).

**Sémantique honnête : at-least-once, JAMAIS exactly-once.** Aucune PERTE ne survient en
fonctionnement normal et modérément dégradé (enregistrement après injection ; une
publication confirmée est durable ; le consumer reprend depuis sa position acquittée). Le
SEUL chemin de perte résiduel est borné par la rétention : le stream conserve un message
pendant au plus `MaxAge` (72 h par défaut, `LimitsPolicy`) ; un événement stocké est donc
abandonné si AUCUN leader ne le draine pendant plus de `MaxAge` — perte totale du quorum /
panne sans leader ou partitionnée de plusieurs jours. Cette fenêtre devient observable via
le SLI `olivares_durablebus_stream_pending` (une backlog approchant `MaxAge` peut déclencher
une alerte) ; l'abandon n'est donc jamais silencieux. L'opérateur augmente `MaxAge` ou
rétablit un leader pour le maintenir à zéro. Un DOUBLON n'est possible que dans deux
fenêtres bornées — le chevauchement de leadership ≤2 s et un crash brutal entre l'injection
et l'enregistrement de déduplication — toutes deux absorbées en aval (l'index
`(tenant_id, event_id)` de la capture d'événements et la déduplication par scan borné de
security). Le bridge ouvert reste at-most-once et inchangé.

### Conséquences

- **Avantages :** les événements d'enforcement survivent à la livraison entre nœuds
  (at-least-once) avec une garantie de déduplication maîtrisée ; le binaire community est
  identique octet pour octet (l'add-on est absent ; l'unique jointure ouverte,
  `BridgeExclude`, est inerte) ; aucune modification du schéma du store (la déduplication
  réside dans JetStream KV) ⇒ parité de schéma intacte ; fail-boot-closed (un backend
  durable déclaré qui ne peut pas être établi interrompt le boot ; un binaire enterprise
  sans licence se dégrade de manière VISIBLE vers le bridge Core-NATS ouvert, jamais
  silencieusement vers un seul nœud).
- **Inconvénients / compromis :** la livraison durable coûte un aller-retour JetStream lors
  de la publication (PubAck) et une lecture KV lors de l'injection — acceptable pour la
  classe d'enforcement de volume modéré, et l'opérateur peut réduire l'ensemble durable ;
  les événements durables n'atteignent les subscribers que sur le leader (via le consumer),
  si bien que les propres publications durables d'un nœud ne sont pas diffusées localement
  en fan-out (cohérent avec « enforcement uniquement sur le leader ») ; le gate de licence
  du bus s'applique au boot (installer une licence pour activer la durabilité exige un
  redémarrage, contrairement au plafond de sièges appliqué à chaud).
- **Neutre :** la Phase 2+ du levier (échelle DR, multirégion, silo/CMEK par tenant) est une
  feuille de route documentée (`enterprise/durablebus/doc.go`), NON construite.

## Pourquoi les alternatives ont été rejetées

A impose un rug pull au bridge ouvert et pénalise le chemin chaud ; C échange un petit KV
contre une modification du schéma du noyau qui rompt le gate de parité. B cantonne le
changement à du code fermé et additif, et résout le problème de tolérance aux doublons de
l'ADR-0017 à la frontière du bus au lieu de dépendre de la passe par subscriber jamais
achevée.
