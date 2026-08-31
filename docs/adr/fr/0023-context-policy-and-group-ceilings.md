> Traduction automatique. La version anglaise fait foi.

# ADR-0023: Application de la politique de contexte aux trois points de transit, avec plafonds de fenêtre et de dépenses par groupe

- **Status:** accepted
- **Date:** 2026-07-08
- **Deciders:** Fran Olivares
- **References:** ADR-0022 (source scoping by subject axis — its subject resolver and `most-specific` precedence are mirrored here), ADR-0009 (append-only hash-chained audit), ADR-0003 (RRW map — permitted vs observed).

## Contexte et énoncé du problème

La politique de contexte (taille de fenêtre et stratégie de compactage) était persistée
comme donnée gouvernée, mais **aucun consumer ne l'appliquait jamais** — le consumer promis
par un commentaire du code n'existait pas ; la politique était donc une métadonnée morte.
Par ailleurs, les plafonds de tokens du proxy d'inférence s'appliquaient uniquement **par
tenant / par requête**, et FinOps porte une dimension budgétaire `team` **détective et
fail-open**. Il n'existait aucun moyen d'imposer : « ce groupe d'utilisateurs (ou d'agents)
peut consommer au maximum cette fenêtre / ce montant ».

La vision du produit exige deux capacités que la politique stockée mais inutilisée ne
pouvait pas fournir :

1. **La politique de contexte DÉCIDE aux trois points de transit** où la plateforme touche
   une requête adressée à un modèle — runtime de session, proxy d'inférence inline et
   retrieval de connaissances — au lieu de rester une donnée inerte.
2. **Des plafonds appliqués par groupe** — `user_group` et `agent_group` — pour la
   **fenêtre de contexte** comme pour les **dépenses**, deny-closed lorsque la politique
   l'exige, avec une **dégradation honnête** (jamais de bridage silencieux ni d'autorisation
   silencieuse).

## Facteurs de décision

- **Cohérence avec le scoping des sources (ADR-0022).** Réutiliser le même vocabulaire de
  subjects et la même priorité `most-specific`, afin que les opérateurs raisonnent sur la
  gouvernance du contexte exactement comme sur le scoping des sources — aucun second moteur
  de décision, surface d'attaque réduite.
- **Un plafond doit réellement être un plafond.** Une limite numérique qu'un scope plus
  spécifique peut *assouplir* n'est pas un plafond ; l'objectif central est de disposer de
  « plafonds appliqués ».
- **Dégradation honnête.** Lorsque la plateforme ne peut pas comptabiliser totalement un
  élément (dépenses approximatives d'un groupe), elle doit échouer dans le sens *sûr* et le
  signaler — ne jamais refuser à tort, ne jamais autoriser silencieusement.
- **Réutiliser les primitives existantes.** Privilégier le ledger d'audit, l'attribution des
  coûts par subject existante et le chemin de refus existant du proxy plutôt que de
  nouvelles mécaniques transversales.

## Résultat de la décision

### 1. Composition d'`Apply` — qualitatif au plus spécifique, planchers de sécurité restrictifs, `max_tokens` par MIN

`Module.Apply` (`modules/knowledge/context.go:263`) résout la politique effective d'une
requête :

- Les champs **qualitatifs** sont résolus selon **le plus spécifique l'emporte**
  (`strategy`), conformément à l'ADR-0022.
- Les **planchers de sécurité** se composent de manière **restrictive** : `forbid` est
  absolu ; `redaction_required` se compose par OR ; `excluded_sources` par union.
- **`max_tokens` se compose par MIN** (le plus restrictif ; champ dans
  `context.go:62,73`, borné dans `context.go:124`). Il s'agit d'un raffinement délibéré pour
  la limite numérique : un plafond qu'un scope plus spécifique pourrait relever ne serait
  pas un plafond. Ce comportement est réversible en environ deux lignes si un déploiement
  préfère un jour le plus spécifique pour la limite.

### 2. Identité de l'agent dans le proxy — combler le résidu accessible (E3-lite), reporter honnêtement le reste

Le credential WIF d'inférence de session (`sk-ant-oat`) **ne transite pas** par le proxy
d'inférence inline, qui authentifie uniquement les tokens `olvs` / `olvk` propres à la
plateforme. Fermer entièrement la fédération d'identité des agents pour le trafic de
*session* imposerait de réarchitecturer le credential d'inférence (plusieurs jours, dans le
cadre de la posture d'émission WIF éphémère) et est **reporté à un effort dédié (E3-full)**.

La partie accessible est désormais fermée (**E3-lite**) : `authToken` propage `AgentRef` →
`AgentIdentity`, et le resolver d'actor-scope de models respecte le **principal
authentifié** plutôt qu'une valeur déclarée par l'appelant (correction d'un bug), ce qui
active l'axe `agent_group` dans le proxy pour les appelants agent-on-behalf-of. La référence
de l'agent provient toujours du credential authentifié, jamais du corps de la requête
(`context.go:278-279`, `query.go:110-111`).

### 3. Plafond de DÉPENSES par groupe — préventif, fail-open par nature, avec un contrôle granulaire fail-closed

Budget gagne les dimensions `user_group` / `agent_group`, appliquées **préventivement** via
`CheckBudget`, les dépenses d'un groupe étant agrégées par **fan-out des membres** sur
l'attribution des coûts par subject existante (il n'y a pas de colonne de groupe ; sommer
toutes les lignes sans distinction serait une erreur d'attribution —
`modules/finops/ingest.go:75,361`).

La posture est **fail-open** — par nature pour une vérification budgétaire, et conformément
à la séparation du produit entre *sécurité = deny-closed* et *budget = fail-open*
(`modules/models/api.go:639,656`) — avec un contrôle **`fail_closed`** par budget pour les
déploiements souhaitant un arrêt strict (`modules/finops/budgets.go:102,166,182`). Cette
réalité est énoncée **honnêtement** : les dépenses préventives d'un groupe sont
*approximatives*, et non une comptabilité exacte. La couverture augmente avec
l'attribution — les dépenses pas encore attribuées sous-estiment simplement celles du
groupe, ce qui est le sens sûr (cela ne refuse jamais à tort). Le backstop détectif
ingest/finding de FinOps pour les groupes et les compteurs locaux de dégradation sont un
**suivi documenté**, volontairement non câblé à moitié.

### 4. Refus du proxy au-delà de la fenêtre — 413, sans jamais modifier le payload du client

Lorsqu'une requête dépasse la fenêtre effective de la politique/du groupe, le proxy
**refuse avec HTTP 413** et un détail (`cmd/olivares/inferenceproxy.go:449`) ; il **ne
modifie jamais le payload opaque du client** — il refuse plutôt que de le brider en silence
(`inferenceproxy.go:550`). Le compactage et la troncature signalée n'existent que là où la
plateforme assemble elle-même le contexte (retrieval et runtime de session), jamais sur le
prompt de l'appelant. Aucune dégradation silencieuse.

Les trois points d'enforcement sont câblés : retrieval
(`modules/knowledge/query.go:167` → `:354`), runtime de session
(`modules/sessions/runtime.go:285,623`) et proxy d'inférence (ci-dessus).

## Décider et consigner (dans la direction approuvée)

- **Neuf types de scope pour la politique de contexte** —
  `session > agent > user > user_group > role > agent_group > kb > workspace > tenant` — validés dans le handler
  d'écriture (`modules/knowledge/context.go:102-103`), avec un `effect` nullable et
  expand-only (réconciliation établie d'une colonne du module, sans migration numérotée).
- **`surface` et `model` ne sont pas des types de scope.** Le retrieval n'a pas de surface,
  et le proxy intègre déjà la fenêtre par surface dans le MIN ; les ajouter constituerait
  donc une généralité inutilisée (YAGNI).
- **« Métrique OTel » pour cette fonctionnalité = événements auditables + findings natifs**,
  et non un compteur dans le module. La télémétrie du produit circule sur le bus sous forme
  de findings vers observability ; un nouveau compteur serait une modification
  architecturale transversale, hors périmètre.

## Alternatives envisagées

- **Composition au plus spécifique pour `max_tokens`** (uniforme avec les champs
  qualitatifs) : rejetée — un plafond numérique qu'un scope plus spécifique peut relever
  n'est pas un plafond, ce qui compromet l'objectif. Elle reste trivialement réversible si
  un déploiement n'est pas d'accord.
- **Un compteur dédié dans le module pour la télémétrie de contexte/groupe :** rejeté en tant
  que modification architecturale transversale ; le chemin événements d'audit + findings
  du bus transporte déjà le signal.
- **Sommer toutes les lignes de dépenses par subject pour un groupe sans fan-out des
  membres :** rejeté — cela surcompte et attribue mal ; le fan-out sur l'appartenance
  authentifiée est l'attribution correcte et sûre.

## Conséquences

- La politique de contexte passe d'une métadonnée morte à une **décision active** au niveau
  du retrieval, du proxy et du runtime de session.
- Les plafonds de **fenêtre** par groupe sont **stricts et composés par MIN** ; les plafonds
  de **dépenses** par groupe sont **préventifs et honnêtement approximatifs**, avec un
  `fail_closed` opt-in.
- **Dette enregistrée, rien de câblé à moitié :** E3-full (réacheminer l'inférence de
  session via une identité gouvernée), le backstop détectif des dépenses par groupe via
  FinOps avec les compteurs locaux de dégradation, et la transmission du principal
  (`user` / `user_group`) au launch gate. Tous sont des suivis documentés.
