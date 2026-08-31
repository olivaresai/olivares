> Traduction automatique. La version anglaise fait foi.

# ADR-0013 : PDP d'autorisation — Cedar embarqué + adaptateur OPA-over-HTTP

- **Status:** accepted (restrict-only, limité à la couture `auth.PolicyEvaluator` créée
  par ce registre) — **modifiée par l'ADR-0019 (2026-06-15)**, qui supprime le permit
  de base : une règle permit de l'opérateur que cette surcouche neutralisait
  silencieusement accorde désormais, et qui a déplacé Cedar vers un moteur positif
  d'octrois cadrés dans une couture distincte. Les politiques uniquement forbid sont
  inchangées ; dans cette lecture, la formulation « ne jamais élargir » du Contexte et
  des Critères de décision est remplacée — voir la note de modification à la fin.
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares
- **References:** contrat NHI/MCP-auth ; modifiée par l'ADR-0019 (octrois Cedar cadrés)

## Contexte et énoncé du problème

Au-delà du RBAC, la plateforme a besoin d'un point de décision de politique (PDP) pour une
autorisation basée sur les attributs. Les organisations diffèrent : certaines veulent un
moteur autonome, d'autres disposent déjà d'un parc (estate) OPA existant. Le PDP ne doit
jamais *élargir* l'accès — uniquement le restreindre.

## Critères de décision

- Fonctionner de façon autonome (binaire unique, air-gap) sans service de politique externe.
- S'intégrer à un déploiement OPA existant lorsque l'opérateur en a un.
- Un invariant restrict-only : la politique peut refuser, jamais accorder au-delà du RBAC.

## Options envisagées

- **Les deux :** Cedar embarqué (principal, pur Go) **et** un adaptateur OPA-over-HTTP
  derrière un seul seam, sélectionné par l'opérateur.
- **Cedar seul.**
- **OPA seul.**

## Décision retenue

Option choisie : **les deux, derrière un seul seam `PolicyEvaluator`**. **Cedar** est le PDP
principal, embarqué et pur Go ; un adaptateur **OPA-over-HTTP** est disponible ; l'opérateur
sélectionne le moteur via `OLIVARES_PDP_ENGINE = cedar | opa | none`. Le seam ABAC **ne fait
que restreindre** (il applique un ET logique avec le RBAC et n'élargit jamais). L'invariant
restrict-only est testé de bout en bout.

### Conséquences

- **Bon :** autonome par défaut (Cedar, sans sidecar) ; s'intègre à un parc OPA si souhaité ;
  un seam, deux moteurs.
- **Mauvais / compromis :** deux adaptateurs à maintenir ; le durcissement du transport de la
  voie OPA (p. ex. mTLS vers le sidecar) est une extension documentée, pas encore terminée.
- **Neutre :** `none` désactive la couche ABAC, laissant le RBAC en deny-by-default (refus
  par défaut).

## Pourquoi les alternatives ont été rejetées

- **Cedar seul** — exclut les organisations standardisées sur OPA.
- **OPA seul** — impose un service de politique externe à chaque installation, brisant la
  configuration par défaut autonome / air-gap.

## Modification (2026-06-15, ADR-0019)

*(La décision modificatrice est datée du 2026-06-15 ; cette note a été ajoutée le
2026-08-17, lorsqu'un examen du registre des décisions a trouvé les deux documents
signés à onze jours d'intervalle sans lien de précédence entre eux. Rien de ce qui
précède n'est réécrit.)*

**Ce qui ne tient plus tel qu'écrit.** Les affirmations du Contexte « Le PDP ne doit
jamais élargir l'accès — uniquement le restreindre » et du critère « la politique
peut refuser, jamais accorder au-delà du RBAC » se lisent, telles qu'écrites, comme
des affirmations sur **l'ensemble de la décision d'autorisation** et sur **Cedar**.
Depuis l'**ADR-0019**, aucune n'est vraie dans cette lecture : Cedar a été promu
d'une surcouche deny-only à un moteur d'**octrois** trivalent et sensible au cadre,
et le `permit` de base implicite qui limitait une décision Cedar à la restriction a
été supprimé ; un `permit` rédigé accorde donc désormais réellement.

**Ce qui est vrai à la place.** L'invariant restrict-only subsiste **dans les limites
de la couture créée par ce registre** : `auth.PolicyEvaluator` s'exécute toujours
après le RBAC et ne peut que restreindre davantage
(`core/auth/authorizer.go:100-104`). L'octroi positif vit dans une couture **nouvelle
et distincte**, `auth.ScopedAuthorizer`, câblée **à côté** de la surcouche de refus
plutôt qu'en son sein, et l'Authorizer les combine ainsi :
`Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay`
(`core/auth/authorizer.go:157-163`, algèbre aux lignes `:161` et `:200`). Le refus
par défaut, la priorité de forbid sur permit et l'échec en mode fermé en cas d'erreur
sont préservés, et un déploiement sans octroi rédigé décide exactement comme sous ce
registre. Tout le reste reste valable : les deux moteurs derrière une même couture et
le sélecteur `OLIVARES_PDP_ENGINE = cedar | opa | none` constituent le comportement
livré (`cmd/olivares/wire.go:994-1018`).

**Où réside la décision actuelle.** `docs/adr/0019-cedar-scoped-grants.md` (accepted,
2026-06-15, Fran Olivares), qui référence explicitement ce registre. Un lecteur qui
ne cite que cette ADR — celle vers laquelle mène le terme *ABAC* — peut conclure que
le chemin d'octrois positifs livré enfreint une décision signée. Ce n'est pas le cas :
il suit l'ADR-0019.
