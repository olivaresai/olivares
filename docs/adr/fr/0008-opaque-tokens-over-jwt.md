> Traduction automatique. La version anglaise fait foi.

# ADR-0008 : Tokens opaques côté serveur, et non des JWT, pour l'authentification de première partie

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI (confirmed by adversarial review)
- **References:** API/authz/audit contract (§2, decision §13.2)

## Contexte et énoncé du problème

Le mécanisme d'authentification de première partie devait être choisi. Le périmètre initial mentionnait
« sessions/JWT ». Pour un produit de sécurité, les modes de défaillance des informations d'identification de type bearer —
révocation, claims porteurs de secrets, risque lié à la bibliothèque d'analyse (parsing) — comptent énormément.

## Facteurs de décision

- Révocation immédiate.
- Aucun secret transporté à l'intérieur du token.
- Surface d'attaque d'analyse cryptographique minimale ; sûr par défaut.

## Options envisagées

- **Tokens opaques côté serveur** (un secret aléatoire, stocké haché, résolu côté serveur).
- **JWT** pour les sessions de première partie.

## Décision retenue

Option retenue : **tokens opaques côté serveur** pour l'authentification de première partie. Les tokens sont préfixés
par usage (`olvs_` session, `olvk_` clé API) ; le serveur ne stocke qu'un sélecteur public
et un SHA-256 du secret, et les compare en temps constant. Le JWT est confiné à la
jointure (seam) SSO/fédération, et non aux sessions de première partie.

### Conséquences

- **Bon :** les tokens sont révocables, ne portent aucun secret et ne nécessitent aucune analyse
  cryptographique de claims fournis par un attaquant ; sûr par défaut.
- **Mauvais / compromis :** la validation requiert une résolution côté serveur (acceptable pour un
  control plane).
- **Neutre :** la fédération utilise toujours JWT là où le protocole l'exige.

## Pourquoi les alternatives ont été écartées

- **JWT pour les sessions de première partie** — difficile à révoquer avant expiration, tend à porter
  des claims, et ajoute une surface d'attaque d'analyse/validation sans aucun bénéfice ici.
