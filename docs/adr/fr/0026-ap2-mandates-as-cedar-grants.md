> Traduction automatique. La version anglaise fait foi.

# ADR-0026: Les mandats de paiement AP2 comme grants Cedar scopés (achats gouvernés)

- **Status:** proposed (design only; the enterprise build lands in a separate phase)
- **Date:** 2026-07-20
- **Deciders:** Fran Olivares
- **References:** ADR-0019 (Cedar scoped grants), ADR-0022 (source-scoping subject axes),
  ADR-0025 (FinOps reserve→commit/release ledger, TOCTOU-safe), ADR-0009 (append-only
  hash-chained audit); the companion AP2 governed-payment threat-model spec; the AP2 v0.2.0
  specification (github.com/google-agentic-commerce/AP2, verified 2026-07-20).

## Contexte et énoncé du problème

Les paiements agentiques arrivent sous forme de couche protocolaire. L'**AP2 (Agent Payments
Protocol)** de Google est l'un des plus visibles ; sa spécification actuelle est la **v0.2.0
(publiée le 2026-04-28)** et il a été donné à la FIDO Alliance le même jour. AP2 permet à un
utilisateur de déléguer un **mandat** signé à un agent d'achat, que l'agent lie ensuite à une
transaction concrète que des **Verifiers** (marchand, fournisseur de credentials, réseau,
processeur de paiement) contrôlent.

Deux faits déterminent la forme de cette décision :

1. **Actualité (la réalité mesurée l'emporte sur le plan).** La planification antérieure
   s'appuyait sur AP2 v0.1 et décrivait un triplet de mandats *Intent / Cart / Payment* signé
   par des « verifiable credentials ». Ce modèle est **remplacé**. v0.2 définit exactement
   **deux** types de mandats — **Checkout Mandate** et **Payment Mandate** — chacun dans un
   état **ouvert** (porteur de contraintes, signé par l'utilisateur) et un état **fermé** (lié
   à la transaction ; l'agent génère un Key Binding JWT / une Proof-of-Possession sur la clé
   figurant dans le claim `cnf` du mandat ouvert). Les mandats sont des **SD-JWT** (RFC 9901) ;
   le **hash de binding / Key Binding JWT DOIT utiliser un schéma non déterministe
   (ES256/ECDSA) et NON un schéma déterministe (Ed25519)** — la spec indique que cela protège
   le binding de hash. Cet ADR cible la **v0.2**, épinglée aux suffixes de schéma `vct` publiés
   (selon la spec v0.2, `mandate.checkout.1` / `mandate.payment.1` ; à vérifier face aux
   `docs/ap2/*` de la spec au moment du build).

2. **Ce qu'Olivares est — et n'est pas.** Olivares est un **plan de contrôle de gouvernance** :
   un Policy Decision Point (PDP) et un ledger de preuves dont toute altération est détectable.
   Ce n'est **pas** un processeur de paiement, un PSP, un réseau de cartes, un wallet ni un
   dépositaire de fonds, et cet ADR n'en fait pas un. AP2 lui-même est **pre-1.0**, avec une
   **adoption précoce et largement déclarative** (les pages de PayPal elles-mêmes ne
   mentionnent AP2 que de manière taxonomique et mettent en avant l'ACP d'OpenAI + l'UCP de
   Google ; « Agent Pay » de Mastercard est un programme distinct ; le chiffre de
   « 60+ organizations » est un décompte de lancement de septembre 2025 ; la liste des
   signataires FIDO en compte ~12). L'étiquetage honnête interdit de revendiquer un support
   d'AP2 au-delà de ce qui est vérifiable.

Le problème : **comment Olivares gouverne-t-il un achat agentique médié par AP2 avec les
primitives dont il dispose déjà, en naissant d'un cas d'usage enterprise concret et en couvrant
les lacunes qu'AP2 laisse délibérément à la couche située au-dessus de lui — sans introduire de
fall-through d'autorisation ni de downgrade silencieux de contrainte ?**

Le cas d'usage concret dont naît ce design : un **agent d'achats gouverné** — une entreprise
achète via un agent opérant sous un mandat ouvert AP2 dont les contraintes encodent la politique
d'achat (plafond budgétaire, fournisseurs autorisés, limites par article, récurrence, fenêtre
d'exécution) ; Olivares autorise chaque achat concret face à cette politique, escalade les
achats de forte valeur vers un humain et scelle le mandat+reçu comme preuve non répudiable.

**Précondition (gate in-path).** Chacune des garanties ci-dessous ne tient que là où le
déploiement route l'achat **à travers Olivares en tant que gate in-path** — l'agent DOIT obtenir
une autorisation Olivares fraîche avant de présenter un mandat fermé à la couche de règlement.
En tant que PDP latéral/consultatif, Olivares ne peut pas plus atteindre un mandat fermé déjà
remis à un marchand qu'AP2 ne le peut. Le build DOIT documenter cette exigence de déploiement.

## Facteurs de décision

- **Réutiliser le plan d'autorisation existant, ne pas le forker** — mais uniquement là où la
  sémantique correspond réellement (voir la correction Abstain-contre-deny ci-dessous).
- **Couvrir à notre niveau les lacunes déclarées d'AP2** (voir la spec de modèle de menaces
  associée) : AP2 n'a **aucune révocation**, rend le rejet du double-spend côté verifier
  **optionnel (MAY)**, ne prouve **pas** l'identité humaine / la SCA, est **muet sur la
  confiance dans l'horloge** et laisse hors périmètre la rétention/récupération des preuves
  ainsi que la responsabilité. Un PDP qui « suppose que tous les agents sont des attaquants
  potentiels » (le propre modèle de menaces d'AP2) doit rendre ces éléments obligatoires.
- **Échouer en fail-closed sur tout ce qui n'est pas modélisable.** Une contrainte que nous ne
  pouvons pas encoder, une disclosure que l'agent retient, un algorithme inconnu — chacun doit
  rejeter le mandat, jamais l'élargir.
- **Périmètre honnête et risque pre-1.0.** Concevoir maintenant, épingler sur `vct`, ne pas
  livrer d'affirmations que nous ne pouvons pas vérifier, maintenir Olivares strictement du côté
  PDP/preuves de la frontière.

## Options envisagées

- **Option A — les mandats AP2 comme grants Cedar scopés ; Olivares comme Verifier/PDP
  gouvernant.** Modéliser un **mandat ouvert** AP2 comme un **grant Cedar** rédigé (ADR-0019)
  lié à ce seul mandat, dont les conditions `when` sont les contraintes du mandat ; traiter un
  **mandat fermé** comme une **requête d'autorisation** (principal = la clé de l'agent dans
  `cnf` ; action = `purchase`/`pay` ; resource = le bénéficiaire / le checkout) évaluée
  **deny-by-default pour les actions de paiement**. Olivares exécute les règles de vérification
  d'AP2 en tant que PDP, filtre les mandats de forte valeur via l'approbation HITL à usage
  unique, réserve les budgets FinOps (ADR-0025) en fail-closed et scelle l'intégralité du
  mandat+reçu signé comme preuve.
- **Option B — un moteur de mandats AP2 sur mesure, parallèle à Cedar.**
- **Option C — observer seulement.**

## Résultat de la décision

Option retenue : **Option A**, parce que le modèle de contraintes se projette sur les conditions
de grant Cedar et que les contrôles environnants (approbations, ledger de réservation, chaîne
d'audit signée) existent déjà — **à condition que les trois corrections sémantiques ci-dessous
soient apportées**, faute de quoi la réutilisation n'est pas sûre.

### Les trois corrections sémantiques qui rendent la réutilisation saine

1. **Les actions de paiement sont DENY-BY-DEFAULT, et non abstain-délègue-au-RBAC.** Le moteur
   de grants scopés renvoie **`EffectAbstain`** (et non deny) lorsqu'aucun permit ne correspond
   — « aucun grant », « grant expiré » et « aucun grant scopé pour le tenant » produisent tous
   un Abstain, et Abstain signifie que *la décision RBAC de base prévaut*
   (`modules/governance/grants.go:31-38`, l'invariant de rétrocompatibilité RBAC). Assimiler
   naïvement « aucun mandat correspondant » à « deny » est **faux** : un cnf non concordant, un
   mandat expiré ou un grant révoqué produiraient un Abstain et pourraient retomber sur un
   **allow RBAC**. Correction : `purchase`/`pay` ne sont autorisés **que** par un grant
   correspondant, valide et lié à un mandat, **sans aucun repli RBAC**. Le build DOIT
   l'appliquer soit (i) en prouvant que l'authorizer de base n'accorde aucun permit
   `purchase`/`pay` à aucun rôle (donc Abstain→deny), soit (ii) via un overlay de paiement qui
   traite un Abstain sur une action de paiement comme un deny. Un mandat présent mais invalide
   rédige en outre un **`forbid`** explicite. Un test de conformité DOIT affirmer que le RBAC
   seul n'autorise jamais un paiement.

2. **Le traducteur mandat→grant ÉCHOUE EN FAIL-CLOSED sur toute contrainte non modélisable.**
   « Une contrainte inconnue DOIT échouer » est une obligation **au moment de la traduction**,
   et non quelque chose que le deny-by-default de Cedar fournit : si le traducteur omet
   silencieusement une contrainte qu'il ne peut pas encoder, il produit un grant **plus large
   que ce que l'utilisateur a signé**, et Cedar autorise parce qu'il n'a jamais vu la
   contrainte. Correction : traduire face à une **allowlist** de clés, opérateurs et unités de
   contrainte reconnus ; sur tout élément non reconnu, **rejeter le mandat entier et ne rédiger
   aucun grant**.

3. **La disclosure complète est obligatoire ; l'agent non fiable ne peut pas retenir une
   contrainte.** Dans SD-JWT, c'est le *holder* (l'agent non fiable) qui choisit quelles
   disclosures révéler. Il pourrait ne présenter que les disclosures qui passent et retenir une
   contrainte plus stricte. Correction : l'adaptateur de vérification énumère les digests `_sd`
   et, si un digest correspondant à un claim pertinent pour la politique est **non divulgué**,
   le traite comme une contrainte non évaluable et **échoue en fail-closed**.

### Correspondance (avec les corrections appliquées)

| Concept AP2 v0.2 | Primitive Olivares (file:line) |
|---|---|
| Mandat ouvert (contraintes, signé par l'utilisateur) | **Grant** Cedar scopé lié aux `jti`/`sd_hash` de ce mandat (`modules/governance/grants.go:67`, ADR-0019) |
| Mandat fermé | **Requête** d'autorisation, évaluée **deny-by-default pour `purchase`/`pay`** (correction 1) |
| « Verification and Processing Rules » | Vérification de chaîne par l'adaptateur + contrôle de disclosure complète (correction 3) + traduction fail-closed (correction 2) + décision du PDP |
| `payment.budget` (cumulatif) / `amount_range` (par transaction) | Ledger de réservation FinOps (`modules/finops/budgets.go`, `spendlimits.go`, ADR-0025) avec une **clé de réservation par mandat entièrement nouvelle** ; réserver face au plafond du mandat ET à tous les scopes Olivares atomiquement (NON `min()`) |
| `payment.agent_recurrence` (nombre/vélocité) | Limiteur de nombre/vélocité **entièrement nouveau** (TOCTOU-safe sous ADR-0025) — et NON un budget existant basé sur les montants |
| `allowed_payees` / `allowed_merchants` / `allowed_payment_instruments` | Conditions `when` Cedar d'appartenance à un ensemble |
| `execution_date` {not_before,not_after} | Condition temporelle face à l'**horloge dead-man DDIL signée et de confiance** (`modules/governance/ddiladopt.go`), injectée également dans l'adaptateur SD-JWT |
| Approbation de l'utilisateur ; gating de forte valeur | Consommation d'approbation **HITL à usage unique** (`modules/governance/approvals.go`) |
| Checkout/Payment Mandate + reçu (preuves de litige) | **Ledger d'audit runtime** chaîné par hash, clé sur `transaction_id` (`modules/sessions/runtime_ledger.go`, `sc.Audit().Append`, ADR-0009) — voir la décision 1 sur CE QUI est stocké |

### Les décisions que prend cet ADR

1. **Représentation du mandat — autorité et preuves sont des stores distincts.**
   - L'**autorité** est le **grant Cedar** (la politique évaluée), lié à l'id stable du mandat
     ouvert concerné (`jti`/`sd_hash`), de sorte qu'un mandat fermé ne puisse être évalué que
     face au grant rédigé à partir de *son* mandat ouvert (cela empêche la **substitution de
     mandat** : un agent détenant un mandat A laxiste ne peut pas faire évaluer un mandat
     B-fermé face au grant A). Le grant n'est **jamais** le blob brut traité comme une autorité
     auto-déclarée.
   - Les **preuves** sont l'**artefact signé complet** : le SD-JWT ouvert, le Key Binding JWT
     fermé et les **disclosures réellement présentées** — conservés (chiffrés, à accès contrôlé)
     afin qu'un litige puisse *rejouer la séquence de vérification de signature d'AP2*, ce qu'un
     hash ne permet pas. Ces preuves portent des PII (montants, bénéficiaires) : ce sont donc
     des **preuves chiffrées et minimales nécessaires, et non « jamais de PII »** — la règle de
     minimisation des données s'applique à l'*autorité/au grant* et aux logs opérationnels, pas
     à l'enregistrement de litige scellé.

2. **Vérification de signature — en chaîne, avec algorithmes épinglés et racines de confiance
   séparées.** Vérifier la chaîne SD-JWT et le lien ouvert→fermé via le Key Binding JWT (PoP)
   lié au `cnf`, confirmer que le mandat fermé préserve inchangés les claims du mandat ouvert,
   et évaluer chaque contrainte (corrections 2 et 3). Deux règles de durcissement que la spec
   brute ne donne pas :
   - **Épinglage des algorithmes.** Lier chaque clé de racine de confiance à son ensemble
     d'algorithmes autorisés et vérifier strictement par rapport à lui ; **ignorer l'`alg`
     annoncé par le token**. Rejeter `alg:none`, la confusion HS/ES et le downgrade de
     courbe/de robustesse — l'interdiction d'Ed25519 par AP2 est une règle étroite au sein
     d'une surface de négociation contrôlée par le header et pilotée par l'agent non fiable.
   - **Racines de confiance séparées.** La racine **User-Credential** (OpenID4VP) vérifie que
     l'*humain a autorisé* le mandat ouvert ; la liste **Trusted-Agent-Provider** ne gouverne
     que l'identité d'agent autorisée à **détenir/lier** la clé `cnf`. Elles attestent des faits
     différents et sont **toutes deux requises, chacune sur sa propre obligation** — jamais un
     OU interchangeable (une attestation de fournisseur d'agent ne remplace pas la signature
     d'autorisation de l'utilisateur). Deny-closed si la racine requise est absente.

3. **Expiration, usage unique et révocation (limités aux flux gouvernés par Olivares).** AP2 n'a
   **aucune révocation**. Olivares comble ce manque pour les déploiements **in-path** : (a) le
   grant lié au mandat est **révocable de première classe** — le révoquer rend deny-by-default
   toute *autorisation Olivares future* pour ce mandat (correction 1) ; cela ne peut pas
   atteindre un mandat fermé déjà transmis au règlement (même limite qu'AP2 — énoncée
   honnêtement). (b) Un mandat fermé de forte valeur consomme une **approbation à usage
   unique**, de sorte qu'une approbation ne puisse pas être rejouée. (c)
   `exp`/`execution_date`/la récurrence sont appliqués face à l'**horloge DDIL signée et de
   confiance**, et l'adaptateur SD-JWT tire son `now` de cette même horloge, si bien que les
   deux couches ne peuvent pas diverger.

4. **Rejeu / double-spend — la dé-duplication côté verifier est OBLIGATOIRE (in-path).** AP2
   place le MUST anti-double-spend sur l'*agent d'achat* (un attaquant dans son propre modèle de
   menaces) et ne fait de la vérification côté verifier qu'un MAY. Le PDP Olivares suit les
   nonces / `transaction_id` de mandats fermés présentés pour chaque mandat ouvert et refuse les
   présentations chevauchantes/répétées — pour les autorisations qui transitent par Olivares (la
   précondition in-path).

5. **Ce qu'Olivares ne fait PAS.** Aucune garde de fonds, aucune exécution de paiement, aucune
   émission de cartes/tokens, aucun rôle de PSP/réseau/wallet. Olivares est le **PDP** qui
   autorise l'achat agentique face à la politique et le **plan de preuves** qui scelle le
   mandat/reçu. Le règlement reste du ressort du marchand/PSP/réseau.

### Conséquences

- **Avantages :** réutilise Cedar / le ledger de réservation / les approbations / la chaîne
  d'audit là où la sémantique correspond véritablement ; les lacunes d'AP2 deviennent des
  garanties appliquées ; des preuves scellées et non répudiables ; un positionnement honnête et
  vérifiable.
- **Inconvénients / compromis :** la réutilisation est **conditionnelle** — elle exige un
  overlay deny-by-default pour les actions de paiement, un traducteur fail-closed, l'application
  de la disclosure complète, une clé de réservation par mandat et un limiteur de récurrence
  entièrement nouveau (rien de gratuit) ; AP2 est pre-1.0 (une v0.3 forcera un nouveau mapping,
  isolé derrière l'adaptateur et épinglé sur `vct`) ; conserver des preuves signées comportant
  des PII ajoute une obligation de chiffrement/de rétention.
- **Neutre / suivis :** la délégation de mandat d'agent à agent est **hors du périmètre d'AP2**
  → hors du nôtre ; x402 (extension AP2 pour les rails crypto) et ACP (OpenAI/Stripe) sont
  distincts et suivis, non construits ici.

## Pourquoi les alternatives ont été rejetées

- **Option B (moteur sur mesure)** — rejetée : elle duplique la mécanique ledger de
  réservation/approbation/audit pour un protocole pre-1.0 ; les corrections ci-dessus montrent
  que la réutilisation est saine dès lors que le deny-by-default sur les actions de paiement et
  la traduction fail-closed sont en place.
- **Option C (observer seulement)** — rejetée : la direction ratifiée est de concevoir
  maintenant et de démarrer tôt le build enterprise *sans bloquer la publication publique*.
  Observer seulement reviendrait à abandonner le différenciateur (dépense agentique gouvernée
  avec preuves scellées) pendant que le standard se consolide chez FIDO. La préoccupation
  d'étiquetage honnête est satisfaite en livrant le **design** maintenant et en conditionnant le
  **build** à un besoin vérifié, plutôt qu'en ne faisant rien.
