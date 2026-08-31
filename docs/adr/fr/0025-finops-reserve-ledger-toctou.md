> Traduction automatique. La version anglaise fait foi.

# ADR-0025: Le ledger FinOps reserve→commit/release ferme le TOCTOU budget/limite de dépenses

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** Fran Olivares
- **References:** ADR-0023 (per-group window and spend ceilings — its FinOps budget dimensions are what this reservation ledger admits against), ADR-0001 (store abstraction — SQLite + Postgres, one descriptor), ADR-0009 (append-only hash-chained audit).

## Contexte et énoncé du problème

`finops.CheckBudget` et `finops.CheckSpendLimit` sont des vérifications d'admission pre-flight
en lecture seule : elles agrègent le read-model de coûts et répondent à la question « cette
requête est-elle dans les budgets/limites appliqués qui la scopent ? ». Entre cette réponse et
le moment où la dépense réelle est réécrite (l'ingest `CostSampled` → `onCost` du connecteur),
il existe une fenêtre. **N requêtes concurrentes lisent toutes le même état pré-dépense,
passent toutes et font collectivement exploser la limite** — un double-spend check→act
(TOCTOU). Une passe de durcissement fail-closed antérieure a fermé la dégradation `Truncated`
et la posture de disponibilité, mais la race condition elle-même est restée ouverte.

Un correctif correct doit rendre **atomique** l'enchaînement « vérifier le plafond, puis
consommer la marge disponible », et il doit être atomique **entre les réplicas sur Postgres**,
et pas seulement au sein d'un unique processus — un mutex au niveau du processus n'est donc pas
acceptable.

## Facteurs de décision

- **Le plafond doit être consommé à l'admission, et non au règlement.** La seule manière
  d'empêcher que N requêtes concurrentes passent toutes est que chaque admission soustraie
  durablement sa propre marge disponible avant que la suivante ne lise.
- **Multi-store, un seul contrat.** Le même mécanisme doit tenir sur SQLite (embarqué, writer
  unique) et sur Postgres HA (connexions multiples, READ COMMITTED). Utiliser les primitives
  d'atomicité propres au store, jamais un verrou en mémoire.
- **Le coût réel n'est connu qu'a posteriori.** Les tokens de sortie (et donc le coût) sont
  inconnus avant l'appel. L'admission doit réserver une *estimation* et réconcilier à
  l'achèvement.
- **Expiration honnête.** Un appelant crashé ne doit pas retenir indéfiniment de la marge
  disponible, et sa récupération ne doit jamais donner lieu à un double comptage.
- **Aucun nouveau moteur de schéma.** Réutiliser le descripteur `ExtensionRegistry` du module +
  la concurrence optimiste du repo générique.

## Résultat de la décision

Un **ledger de réservation dynamique** (`finops.budget_reservation`, table
`finops_budget_reservation`) doté d'un cycle de vie reserve→commit/release. `ReserveBudget` /
`ReserveSpendLimit` réservent atomiquement l'estimation face à chaque politique appliquée qui
scope la requête ; `CommitReservation` la règle avec le coût réel ; `ReleaseReservation`
restitue la marge disponible en cas d'échec. Le plafond, partout (`CheckBudget`,
`budgetStatus`, `evaluateBudgets`), vaut désormais
`committed_spend + static ReservedMicroUSD + Σ(active, unexpired reservations)`.

Ceci est **distinct du** `budgetSpec.ReservedMicroUSD` **statique** préexistant (un engagement
de capacité Priority-Tier comptabilisé dans la limite). Les deux sont sommés dans `effective` ;
cet ADR ajoute la ligne *dynamique, par requête*.

### 1. Atomicité : un `seq` monotone par scope sous un index UNIQUE (aucun verrou de processus)

Chaque réservation porte un `seq` monotone par **(policy, period_start, scope_key)**, sous
l'index UNIQUE
`finops_budget_reservation_seq_uniq (tenant_id, policy_ref, period_start, dim_key, seq)`.
Réserver = lire `max(seq)`, lire la dépense courante + les réservations actives, et s'il reste
de la place, `INSERT` avec `seq = max+1`.

- Deux réservateurs concurrents calculent le **même** `seq` suivant ; l'index UNIQUE ne laisse
  committer qu'**un seul** `INSERT` et mappe l'autre sur `store.ErrConflict` (`mapWriteErr`).
  Le perdant **rejoue la transaction entière** et relit l'état désormais committé. Cela
  sérialise reserve-check-insert **sans aucun verrou de processus**.
- **SQLite :** `MaxOpenConns=1` sérialise déjà chaque transaction sur le writer unique, si bien
  que la réservation est atomique par elle-même ; l'index seq est le backstop
  ceinture-et-bretelles.
- **Postgres READ COMMITTED (le cas porteur) :** des connexions distinctes ne voient pas les
  lignes non committées les unes des autres ; c'est donc la collision de seq qui force le
  rejeu. **Invariant d'ordre :** la réservation lit `max(seq)` **avant** la somme réservée et
  insère avec *ce* seq — ainsi une insertion réussie (sans collision) prouve que le seq lu
  était le véritable max committé, et donc que la somme (lue strictement après) a vu toutes les
  réservations antérieures. Inverser les deux lectures rouvrirait la race condition (une somme
  obsolète associée à un seq frais et sans collision conduirait à sur-admettre). Démontré par
  récurrence : la k-ième insertion réussie a vu toutes les k-1 réservations antérieures, si
  bien qu'exactement `floor(headroom/estimate)` sont admises.

Les requêtes multi-politiques réservent chaque cible dans **une seule** transaction (tout ou
rien) : le refus d'une cible ultérieure annule les insertions antérieures ; block prime sur
throttle.

### 2. Granularité de la réservation — par politique appliquée, clé sur le scope

Une **ligne de réservation par politique appliquée à laquelle la requête correspond**, clé sur
`(policy_ref, period_start, scope_key)` :

- **Budgets :** `scope_key` = la clé de dimension du budget (`""` pour global) — un scope par
  politique. Réservé sur l'ensemble des 17 dimensions non-groupe auxquelles la requête
  correspond (le cas courant par requête : model/provider/agent/workspace/identity/api_key/…).
- **Limites de dépenses par seat :** `scope_key` = l'**acteur**, de sorte qu'un plafond issu
  d'une politique org/groupe réserve **indépendamment** la marge disponible de chaque seat —
  conformément à la sémantique par acteur de `CheckSpendLimit`.
- **Les budgets de dimension groupe (`user_group`/`agent_group`) ne sont PAS réservés ici.**
  Leurs dépenses relèvent d'un fan-out des membres sur `actor`/`agent_ref`, sans colonne de
  groupe dans le read-model ; une réservation par fan-out est un design plus vaste. Ils restent
  appliqués par le chemin préventif existant de `CheckBudget`. (Suivi ouvert — voir
  ci-dessous.)

### 3. Estimation — réserver une estimation, réconcilier au commit

L'admission réserve `estimateMicroUSD` (l'estimation a priori de la jointure — p. ex. issue de
`count_tokens` sur le prompt plus une allocation de sortie `max_tokens`). À l'achèvement,
`CommitReservation(handle, actualMicroUSD)` inscrit le coût réel et bascule la ligne en
`committed`, ce qui la retire de la somme active ; la dépense réelle arrive séparément via
`onCost`. Si l'estimation était **trop basse**, le budget peut transitoirement être dépassé de
`actual − estimate` pour cette seule requête — un dépassement borné et auto-correcteur dès que
la dépense réelle est enregistrée. **La politique d'estimation par défaut est une décision
produit (voir ci-dessous) ; le mécanisme est agnostique vis-à-vis de l'estimation.**

**Ordre :** ingérer la dépense réelle, *puis* committer la réservation, afin que le plafond ne
sous-compte jamais transitoirement pendant le règlement.

### 4. Expiration — un prédicat, jamais un décrément

La somme des réservations actives filtre `state = active AND expires_at > now`. Une réservation
expirée **cesse donc de compter à l'instant où elle échoit** — il n'y a aucun compteur à
décrémenter, si bien que **le double comptage est structurellement impossible**.
`SweepExpiredReservations` ne fait qu'inscrire l'état terminal `expired` à des fins
d'observabilité/de GC ; la correction ne dépend pas de son exécution. Le TTL
(`reservationTTL`, **5 min** par défaut) est le backstop de crash pour un appelant mort entre
la réservation et le commit/release ; il doit dépasser l'actuation gouvernée la plus lente,
afin qu'une requête encore en cours ne soit jamais abandonnée.

### Conséquences

- **Avantages :** le double-spend est fermé atomiquement sur les deux moteurs ; le correctif
  est additif (une nouvelle table de descripteur — `applyModuleTables` la crée sur les bases
  neuves comme en place ; aucune migration existante n'est touchée) ; `CheckBudget`/le
  statut/les alertes reflètent désormais les réservations en vol, de sorte que le refus
  pre-flight, le signal de hard-cap et le DTO de statut concordent.
- **Coût :** une réservation représente deux écritures (réservation + règlement) face à une
  vérification en lecture seule ; sur le chemin chaud, cela représente quelques petites
  transactions supplémentaires, négligeables devant l'appel d'inférence qu'elles protègent.
- **Latent jusqu'au câblage :** le ledger ne mord qu'une fois que les jointures d'actuation
  appellent `ReserveBudget`/`Commit`/`Release` (avec une estimation) au lieu du `CheckBudget`
  en lecture seule. D'ici là, le réservé dynamique vaut 0 et le comportement est inchangé. Le
  câblage du proxy d'inférence / du gate HITL, plus le choix de l'estimation par défaut,
  constituent l'intégration restante.

## Questions ouvertes (produit)

1. **Estimation par défaut.** Quelle est l'estimation a priori lorsque la jointure n'en a
   aucune ? Options : `count_tokens(prompt)` + l'allocation de sortie `max_tokens` configurée
   au tarif du modèle ; un plancher forfaitaire par requête ; ou le coût historique p95 par
   modèle. Sous-estimer affaiblit la garantie ; surestimer bride trop tôt.
2. **TTL.** 5 min est-il le bon backstop de crash, ou devrait-il suivre le temps d'achèvement
   maximal du modèle / être défini par surface ?
3. **Réservation des budgets de groupe.** Les budgets `user_group`/`agent_group` devraient-ils
   eux aussi être réservés (fan-out des membres), ou un enforcement uniquement préventif
   est-il acceptable pour les plafonds de groupe ?
4. **Posture d'épuisement des rejeux.** À l'épuisement de `maxReserveRetries` (64), la
   réservation échoue en mode **open** (conformément au contrat de `CheckBudget`). Pour un
   budget `block` strict, une contention extrême devrait-elle au contraire échouer en mode
   **closed** ?
